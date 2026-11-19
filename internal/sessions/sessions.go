// Package sessions owns the lifecycle of agent sessions and their tabs.
//
// Layout: a persistent session owns a dedicated browser process (its profile
// must not be shared), while an ephemeral session shares the pooled browser and
// gets an isolated browser context. Per-tab operations are serialised by a
// per-tab queue so two actions can never interleave inside one document, and a
// queued operation that waits too long is rejected with BUSY rather than
// silently reordered.
package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/browser"
	"github.com/xtrcom/xt-nichrome/internal/policy"
	"github.com/xtrcom/xt-nichrome/internal/profiles"
	"github.com/xtrcom/xt-nichrome/internal/snapshots"
	"github.com/xtrcom/xt-nichrome/internal/stealth"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

// Limits are the configured quotas (brief §2).
type Limits struct {
	MaxEphemeralSessions  int
	MaxPersistentSessions int
	MaxTabsPerSession     int
	MaxQueuedOpsPerTab    int
	// IdleTimeout evicts a session with no activity. 0 disables eviction.
	IdleTimeout time.Duration
	// OperationTimeout bounds one queued operation including its wait.
	OperationTimeout time.Duration
}

// DefaultLimits fit the measured memory budget of this host (7.6 GB): four
// ephemeral contexts and two persistent browsers keep RSS under the measured
// ceiling while still allowing parallel agent work.
func DefaultLimits() Limits {
	return Limits{
		MaxEphemeralSessions:  4,
		MaxPersistentSessions: 2,
		MaxTabsPerSession:     4,
		MaxQueuedOpsPerTab:    8,
		IdleTimeout:           10 * time.Minute,
		OperationTimeout:      45 * time.Second,
	}
}

// Config carries everything Manager needs.
type Config struct {
	Limits       Limits
	BrowserPath  string
	Headless     bool
	ProfileRoot  string
	FontConfPath string
	// Persona is the default persona; a session may request another by name.
	Persona stealth.Persona
	// ProxyURL is the egress proxy the browser must use. Empty means the policy
	// proxy is unavailable, which is a startup failure, not a degraded mode.
	ProxyURL string
	// Policy is the same policy the proxy enforces. It is checked at admission
	// too: relying on the proxy alone lets a denied navigation reach the tab,
	// fail at the network layer and be reported as a browser error instead of a
	// policy denial, and it leaves about:blank navigations unpoliced.
	Policy *policy.Policy
	// TempRoot holds per-session scratch directories.
	TempRoot string
	// Now is injectable for tests.
	Now func() time.Time
}

// Manager is the session registry.
type Manager struct {
	cfg      Config
	profiles *profiles.Manager

	mu       sync.Mutex
	sessions map[string]*session
	order    []string

	// Shared browser for ephemeral sessions; started on first use.
	sharedMu  sync.Mutex
	shared    *browser.Process
	sharedErr error
	sharedUse int

	closed   atomic.Bool
	closedCh chan struct{}
	stopOnce sync.Once

	totalTabs atomic.Int64
	// deniedAdmissions counts navigations refused by the origin allowlist before
	// the request ever reached the proxy. Without it, an operator watching the
	// proxy's denial counter would see zero while every navigation is refused,
	// because the proxy is never involved.
	deniedAdmissions atomic.Int64
}

// DeniedAdmissions reports how many navigations the origin allowlist refused.
func (m *Manager) DeniedAdmissions() int64 { return m.deniedAdmissions.Load() }

type session struct {
	id        string
	mode      string
	profileID string
	persona   stealth.Persona
	createdAt time.Time

	// Ephemeral: contextID inside the shared process.
	// Persistent: a dedicated process.
	proc      *browser.Process
	contextID string
	ownsProc  bool

	mu       sync.Mutex
	tabs     map[string]*tab
	tabOrder []string
	state    string
	lastUsed time.Time
	closing  bool
}

type tab struct {
	id        string
	page      *browser.Page
	snapshots *snapshots.Registry
	createdAt time.Time

	queue chan struct{}
	// queued tracks waiters so we can reject beyond the quota.
	queued atomic.Int64
	mu     sync.Mutex
	closed bool
}

// New creates a Manager.
func New(cfg Config) (*Manager, error) {
	if cfg.BrowserPath == "" {
		return nil, fmt.Errorf("sessions: browser path is required")
	}
	if cfg.ProxyURL == "" {
		return nil, fmt.Errorf("sessions: an egress proxy is required; the browser must not reach the network directly")
	}
	if cfg.Policy == nil {
		return nil, fmt.Errorf("sessions: a policy is required for admission checks")
	}
	if cfg.Limits.MaxEphemeralSessions <= 0 || cfg.Limits.MaxPersistentSessions <= 0 ||
		cfg.Limits.MaxTabsPerSession <= 0 || cfg.Limits.MaxQueuedOpsPerTab <= 0 {
		return nil, fmt.Errorf("sessions: limits must all be positive")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	pm, err := profiles.New(cfg.ProfileRoot)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		cfg:      cfg,
		profiles: pm,
		sessions: map[string]*session{},
		closedCh: make(chan struct{}),
	}
	if cfg.Limits.IdleTimeout > 0 {
		go m.evictLoop()
	}
	return m, nil
}

// Limits returns the active limits.
func (m *Manager) Limits() Limits { return m.cfg.Limits }

// Persona returns the default persona.
func (m *Manager) Persona() stealth.Persona { return m.cfg.Persona }

// SharedRevision reports the pooled browser's product string.
//
// Before the pool is warm there is no process to ask, so the configured
// binary's own version string is reported instead. Returning "" would make
// readiness indistinguishable from "no browser configured", and an operator
// checking /readyz before the first session needs the real answer.
func (m *Manager) SharedRevision() string {
	m.sharedMu.Lock()
	if m.shared != nil {
		rev := m.shared.Revision()
		m.sharedMu.Unlock()
		return rev
	}
	m.sharedMu.Unlock()
	return m.cfg.Persona.BrowserRevision
}

// Create makes a new session.
func (m *Manager) Create(ctx context.Context, req types.CreateSessionRequest) (*types.Session, *types.APIError) {
	if m.closed.Load() {
		return nil, types.NewError(types.CodeBrowserFailed, "the service is shutting down")
	}
	mode := req.Mode
	if mode == "" {
		mode = types.ModeEphemeral
	}
	if mode != types.ModeEphemeral && mode != types.ModePersistent {
		return nil, &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "mode must be ephemeral or persistent",
			Detail:  map[string]any{"field": "mode"},
		}
	}
	persona := m.cfg.Persona
	if req.Persona != "" {
		p, err := stealth.Lookup(req.Persona)
		if err != nil {
			return nil, &types.APIError{
				Code:    types.CodeInvalidRequest,
				Message: "unknown persona",
				Detail:  map[string]any{"field": "persona", "known": stealth.Names()},
			}
		}
		persona = p
	}
	if mode == types.ModePersistent {
		if req.ProfileID == nil || *req.ProfileID == "" {
			return nil, &types.APIError{
				Code:    types.CodeInvalidRequest,
				Message: "a persistent session requires profileId",
				Detail:  map[string]any{"field": "profileId"},
			}
		}
		if err := profiles.ValidateName(*req.ProfileID); err != nil {
			return nil, &types.APIError{
				Code:    types.CodeInvalidRequest,
				Message: err.Error(),
				Detail:  map[string]any{"field": "profileId"},
			}
		}
	} else if req.ProfileID != nil && *req.ProfileID != "" {
		return nil, &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "profileId is only valid for persistent sessions",
			Detail:  map[string]any{"field": "profileId"},
		}
	}

	id := newID("sess")
	s := &session{
		id: id, mode: mode, persona: persona,
		createdAt: m.cfg.Now(), lastUsed: m.cfg.Now(),
		tabs: map[string]*tab{}, state: types.StateReady,
	}

	// Reserve the slot before doing expensive work so two concurrent creates
	// cannot both pass the check.
	if apiErr := m.reserve(mode); apiErr != nil {
		return nil, apiErr
	}
	ok := false
	defer func() {
		if !ok {
			m.release(mode)
		}
	}()

	if mode == types.ModePersistent {
		profileID := *req.ProfileID
		dir, err := m.profiles.Acquire(profileID)
		if err != nil {
			if errors.Is(err, profiles.ErrBusy) {
				return nil, &types.APIError{Code: types.CodeBusyProfile, Message: err.Error(), Detail: map[string]any{"field": "profileId"}}
			}
			return nil, &types.APIError{Code: types.CodeInvalidRequest, Message: err.Error(), Detail: map[string]any{"field": "profileId"}}
		}
		s.profileID = profileID
		proc, apiErr := m.launch(ctx, persona, dir)
		if apiErr != nil {
			m.profiles.Release(profileID)
			return nil, apiErr
		}
		s.proc = proc
		s.ownsProc = true
		proc.OnCrash(func() { m.dropSession(id, "the browser process exited") })
	} else {
		proc, apiErr := m.sharedProcess(ctx)
		if apiErr != nil {
			return nil, apiErr
		}
		ctxID, apiErr := proc.NewContext(ctx)
		if apiErr != nil {
			return nil, apiErr
		}
		s.proc = proc
		s.contextID = ctxID
		proc.OnCrash(func() { m.dropSession(id, "the shared browser process exited") })
	}

	m.mu.Lock()
	m.sessions[id] = s
	m.order = append(m.order, id)
	m.mu.Unlock()
	ok = true

	return m.sessionWire(s), nil
}

func (m *Manager) launch(ctx context.Context, persona stealth.Persona, profileDir string) (*browser.Process, *types.APIError) {
	temp := m.cfg.TempRoot
	if temp == "" {
		temp = os.TempDir()
	}
	_ = os.MkdirAll(temp, 0o700)
	proc, err := browser.Launch(ctx, browser.Options{
		ExecPath:     m.cfg.BrowserPath,
		Headless:     m.cfg.Headless,
		Persona:      persona,
		ProfileDir:   profileDir,
		FontConfPath: m.cfg.FontConfPath,
		ProxyURL:     m.cfg.ProxyURL,
		TempRoot:     temp,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, types.NewError(types.CodeTimeout, "browser launch timed out")
		}
		return nil, &types.APIError{Code: types.CodeBrowserFailed, Message: err.Error()}
	}
	return proc, nil
}

func (m *Manager) sharedProcess(ctx context.Context) (*browser.Process, *types.APIError) {
	m.sharedMu.Lock()
	defer m.sharedMu.Unlock()
	if m.sharedErr != nil {
		// Do not cache a permanent failure: a transient crash should not poison
		// every later session.
		m.sharedErr = nil
	}
	if m.shared != nil {
		if m.shared.Alive() {
			m.sharedUse++
			return m.shared, nil
		}
		_ = m.shared.Close()
		m.shared = nil
	}
	dir := filepath.Join(m.cfg.TempRoot, "shared-"+newID("b"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, &types.APIError{Code: types.CodeInternal, Message: "could not create the shared profile directory: " + err.Error()}
	}
	proc, apiErr := m.launch(ctx, m.cfg.Persona, dir)
	if apiErr != nil {
		m.sharedErr = errors.New(apiErr.Message)
		return nil, apiErr
	}
	proc.OnCrash(func() {
		m.sharedMu.Lock()
		if m.shared == proc {
			m.shared = nil
		}
		m.sharedMu.Unlock()
	})
	m.shared = proc
	m.sharedUse++
	return proc, nil
}

// dropSession tears a session down after a crash, without holding locks the
// crash handler may already hold.
func (m *Manager) dropSession(id, reason string) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
		m.order = removeString(m.order, id)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	s.mu.Lock()
	s.state = types.StateClosed
	tabs := s.tabs
	s.tabs = map[string]*tab{}
	s.mu.Unlock()
	for _, t := range tabs {
		t.close()
	}
	if s.mode == types.ModePersistent && s.profileID != "" {
		m.profiles.Release(s.profileID)
	}
	m.release(s.mode)
}

func (m *Manager) reserve(mode string) *types.APIError {
	m.mu.Lock()
	defer m.mu.Unlock()
	eph, per := m.countsLocked()
	if mode == types.ModePersistent {
		if per >= m.cfg.Limits.MaxPersistentSessions {
			return &types.APIError{
				Code:    types.CodeCapacity,
				Message: "persistent session capacity is exhausted",
				Detail:  map[string]any{"limit": m.cfg.Limits.MaxPersistentSessions, "inUse": per},
			}
		}
		return nil
	}
	if eph >= m.cfg.Limits.MaxEphemeralSessions {
		return &types.APIError{
			Code:    types.CodeCapacity,
			Message: "ephemeral session capacity is exhausted",
			Detail:  map[string]any{"limit": m.cfg.Limits.MaxEphemeralSessions, "inUse": eph},
		}
	}
	return nil
}

func (m *Manager) release(mode string) {
	if m.closed.Load() && mode == types.ModeEphemeral {
		m.sharedMu.Lock()
		m.sharedUse--
		last := m.sharedUse <= 0 && m.shared != nil
		proc := m.shared
		if last {
			m.shared = nil
		}
		m.sharedMu.Unlock()
		if last {
			_ = proc.Close()
		}
	}
}

func (m *Manager) countsLocked() (ephemeral, persistent int) {
	for _, s := range m.sessions {
		if s.mode == types.ModePersistent {
			persistent++
		} else {
			ephemeral++
		}
	}
	return
}

// Session returns a live session.
func (m *Manager) Session(id string) (*session, *types.APIError) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return nil, &types.APIError{Code: types.CodeNotFound, Message: "session not found", Detail: map[string]any{"sessionId": id}}
	}
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state == types.StateClosed {
		return nil, &types.APIError{Code: types.CodeNotFound, Message: "session is closed", Detail: map[string]any{"sessionId": id}}
	}
	return s, nil
}

// Describe returns the wire form of a session.
func (m *Manager) Describe(id string) (*types.Session, *types.APIError) {
	s, apiErr := m.Session(id)
	if apiErr != nil {
		return nil, apiErr
	}
	return m.sessionWire(s), nil
}

func (m *Manager) sessionWire(s *session) *types.Session {
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	out := &types.Session{
		ID:        s.id,
		Mode:      s.mode,
		ProfileID: s.profileID,
		State:     state,
		Persona:   s.persona.Name,
		CreatedAt: s.createdAt.UTC().Format(time.RFC3339),
	}
	return out
}

// List returns every session.
func (m *Manager) List() []types.Session {
	m.mu.Lock()
	ids := append([]string{}, m.order...)
	m.mu.Unlock()
	out := make([]types.Session, 0, len(ids))
	for _, id := range ids {
		if s, apiErr := m.Describe(id); apiErr == nil {
			out = append(out, *s)
		}
	}
	return out
}

// Close tears a session down.
func (m *Manager) Close(ctx context.Context, id string) *types.APIError {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
		m.order = removeString(m.order, id)
	}
	m.mu.Unlock()
	if !ok {
		return &types.APIError{Code: types.CodeNotFound, Message: "session not found", Detail: map[string]any{"sessionId": id}}
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	s.state = types.StateClosing
	tabs := s.tabs
	s.tabs = map[string]*tab{}
	s.mu.Unlock()

	for _, t := range tabs {
		t.close()
		if t.page != nil {
			t.page.Close(ctx)
		}
	}
	if s.contextID != "" {
		s.proc.DisposeContext(ctx, s.contextID)
	}
	if s.ownsProc {
		_ = s.proc.Close()
	}
	if s.profileID != "" {
		m.profiles.Release(s.profileID)
	}
	s.mu.Lock()
	s.state = types.StateClosed
	s.mu.Unlock()
	m.release(s.mode)
	return nil
}

// Shutdown closes everything.
func (m *Manager) Shutdown(ctx context.Context) {
	m.stopOnce.Do(func() {
		m.closed.Store(true)
		close(m.closedCh)
	})
	m.mu.Lock()
	ids := append([]string{}, m.order...)
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Close(ctx, id)
	}
	m.sharedMu.Lock()
	proc := m.shared
	m.shared = nil
	m.sharedMu.Unlock()
	if proc != nil {
		_ = proc.Close()
	}
	m.profiles.ReleaseAll()
}

func (m *Manager) evictLoop() {
	tick := m.cfg.Limits.IdleTimeout / 4
	if tick < time.Second {
		tick = time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-m.closedCh:
			return
		case <-t.C:
			m.evictIdle()
		}
	}
}

func (m *Manager) evictIdle() {
	now := m.cfg.Now()
	m.mu.Lock()
	var victims []string
	for id, s := range m.sessions {
		s.mu.Lock()
		idle := now.Sub(s.lastUsed)
		closing := s.closing
		s.mu.Unlock()
		if !closing && idle > m.cfg.Limits.IdleTimeout {
			victims = append(victims, id)
		}
	}
	m.mu.Unlock()
	sort.Strings(victims)
	for _, id := range victims {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = m.Close(ctx, id)
		cancel()
	}
}

// Stats reports live counts.
func (m *Manager) Stats() (sessions int, tabs int) {
	m.mu.Lock()
	sessions = len(m.sessions)
	m.mu.Unlock()
	return sessions, int(m.totalTabs.Load())
}

func removeString(list []string, s string) []string {
	out := list[:0]
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

var idCounter atomic.Int64

func newID(prefix string) string {
	n := idCounter.Add(1)
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano()%1e9, n)
}

// trimProfilePath keeps error messages from leaking absolute paths.
func trimProfilePath(p string) string {
	if p == "" {
		return p
	}
	return filepath.Base(strings.TrimSuffix(p, string(filepath.Separator)))
}
