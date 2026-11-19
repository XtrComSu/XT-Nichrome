package sessions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/browser"
	"github.com/xtrcom/xt-nichrome/internal/snapshots"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

// newTabID makes an opaque tab id.
func newTabID() string {
	return newID("tab")
}

func (t *tab) close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.mu.Unlock()
}

func (t *tab) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// Enter acquires the per-tab serialisation slot, waiting at most the configured
// operation timeout and refusing outright when the queue is full.
//
// The queue depth is bounded so a client that pipelines hundreds of actions
// cannot make the service hold unbounded work while one slow page stalls.
func (t *tab) enter(ctx context.Context, limits Limits) (func(), *types.APIError) {
	if t.isClosed() {
		return nil, &types.APIError{Code: types.CodeNotFound, Message: "tab is closed"}
	}
	waiters := t.queued.Add(1)
	defer t.queued.Add(-1)
	if int(waiters) > limits.MaxQueuedOpsPerTab {
		// We are the (n+1)th waiter: refuse before blocking.
		if len(t.queue) >= limits.MaxQueuedOpsPerTab {
			return nil, &types.APIError{
				Code:    types.CodeBusyProfile,
				Message: "too many operations are queued for this tab",
				Detail:  map[string]any{"limit": limits.MaxQueuedOpsPerTab},
			}
		}
	}
	timeout := limits.OperationTimeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case t.queue <- struct{}{}:
		return func() { <-t.queue }, nil
	case <-ctx.Done():
		return nil, &types.APIError{Code: types.CodeTimeout, Message: "request was cancelled while waiting for the tab"}
	case <-timer.C:
		return nil, &types.APIError{
			Code:    types.CodeBusyProfile,
			Message: "the tab is busy with another operation",
			Detail:  map[string]any{"waitedMs": timeout.Milliseconds()},
		}
	}
}

// CreateTab adds a tab to a session.
func (m *Manager) CreateTab(ctx context.Context, sessionID string, req types.CreateTabRequest) (*types.Tab, *types.APIError) {
	s, apiErr := m.Session(sessionID)
	if apiErr != nil {
		return nil, apiErr
	}
	s.mu.Lock()
	if len(s.tabs) >= m.cfg.Limits.MaxTabsPerSession {
		inUse := len(s.tabs)
		s.mu.Unlock()
		return nil, &types.APIError{
			Code:    types.CodeCapacity,
			Message: "tab capacity for this session is exhausted",
			Detail:  map[string]any{"limit": m.cfg.Limits.MaxTabsPerSession, "inUse": inUse},
		}
	}
	s.mu.Unlock()

	pg, apiErr := s.proc.NewPage(ctx, s.contextID)
	if apiErr != nil {
		return nil, apiErr
	}
	t := &tab{
		id: newTabID(), page: pg, snapshots: snapshots.NewRegistry(),
		createdAt: m.cfg.Now(), queue: make(chan struct{}, 1),
	}
	// Fail the tab creation if the persona did not actually install: an agent
	// driving a half-spoofed browser produces polluted results.
	report, apiErr := pg.VerifyPersona(ctx)
	if apiErr != nil {
		pg.Close(ctx)
		return nil, apiErr
	}
	if !report.Consistent() {
		pg.Close(ctx)
		return nil, &types.APIError{
			Code:    types.CodeInternal,
			Message: "the browser identity could not be established for this tab",
			Detail:  map[string]any{"mismatches": report.Mismatches, "patchFailures": report.PatchFailures},
		}
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		pg.Close(ctx)
		return nil, &types.APIError{Code: types.CodeNotFound, Message: "session is closing"}
	}
	s.tabs[t.id] = t
	s.tabOrder = append(s.tabOrder, t.id)
	s.lastUsed = m.cfg.Now()
	s.mu.Unlock()
	m.totalTabs.Add(1)

	out := &types.Tab{TabID: t.id, State: types.StateReady, CurrentURL: pg.CurrentURL()}
	if req.URL != "" {
		res, apiErr := m.Navigate(ctx, sessionID, t.id, types.NavigateRequest{URL: req.URL})
		if apiErr != nil {
			return nil, apiErr
		}
		out.CurrentURL = res.URL
	}
	return out, nil
}

// ListTabs returns the tabs of a session.
func (m *Manager) ListTabs(sessionID string) ([]types.TabInfo, *types.APIError) {
	s, apiErr := m.Session(sessionID)
	if apiErr != nil {
		return nil, apiErr
	}
	s.mu.Lock()
	ids := append([]string{}, s.tabOrder...)
	tabs := make(map[string]*tab, len(s.tabs))
	for k, v := range s.tabs {
		tabs[k] = v
	}
	s.mu.Unlock()
	out := make([]types.TabInfo, 0, len(ids))
	for _, id := range ids {
		t, ok := tabs[id]
		if !ok {
			continue
		}
		out = append(out, types.TabInfo{
			TabID:      t.id,
			CurrentURL: t.page.CurrentURL(),
			Title:      t.page.Title(),
			Generation: t.page.Generation(),
		})
	}
	return out, nil
}

// CloseTab closes one tab.
func (m *Manager) CloseTab(ctx context.Context, sessionID, tabID string) *types.APIError {
	s, apiErr := m.Session(sessionID)
	if apiErr != nil {
		return apiErr
	}
	s.mu.Lock()
	t, ok := s.tabs[tabID]
	if ok {
		delete(s.tabs, tabID)
		s.tabOrder = removeString(s.tabOrder, tabID)
	}
	s.mu.Unlock()
	if !ok {
		return &types.APIError{Code: types.CodeNotFound, Message: "tab not found", Detail: map[string]any{"tabId": tabID}}
	}
	t.close()
	t.snapshots.Invalidate(tabID)
	t.page.Close(ctx)
	m.totalTabs.Add(-1)
	return nil
}

// lookup resolves session+tab and returns the internal handles.
func (m *Manager) lookup(sessionID, tabID string) (*session, *tab, *types.APIError) {
	s, apiErr := m.Session(sessionID)
	if apiErr != nil {
		return nil, nil, apiErr
	}
	s.mu.Lock()
	t, ok := s.tabs[tabID]
	s.mu.Unlock()
	if !ok {
		return nil, nil, &types.APIError{Code: types.CodeNotFound, Message: "tab not found", Detail: map[string]any{"tabId": tabID}}
	}
	if t.isClosed() {
		return nil, nil, &types.APIError{Code: types.CodeNotFound, Message: "tab is closed", Detail: map[string]any{"tabId": tabID}}
	}
	return s, t, nil
}

// Navigate drives a tab to a URL.
func (m *Manager) Navigate(ctx context.Context, sessionID, tabID string, req types.NavigateRequest) (*types.NavigateResult, *types.APIError) {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	if strings.TrimSpace(req.URL) == "" {
		return nil, &types.APIError{Code: types.CodeInvalidRequest, Message: "url is required", Detail: map[string]any{"field": "url"}}
	}
	// Admission check against the same policy the egress proxy enforces. Doing
	// it here is not redundant: it turns a network-layer failure into a policy
	// answer the caller can act on, and it covers about:blank and data: URLs
	// that never reach the proxy at all.
	admitted, apiErr := m.cfg.Policy.AdmitURL(req.URL)
	if apiErr != nil {
		if apiErr.Code == types.CodePolicyBlocked {
			m.deniedAdmissions.Add(1)
		}
		return nil, apiErr
	}
	req.URL = admitted.String()
	if req.WaitUntil != "" && req.WaitUntil != "load" && req.WaitUntil != "commit" {
		return nil, &types.APIError{Code: types.CodeInvalidRequest, Message: "waitUntil must be load or commit", Detail: map[string]any{"field": "waitUntil"}}
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	leave, apiErr := t.enter(ctx, m.cfg.Limits)
	if apiErr != nil {
		return nil, apiErr
	}
	defer leave()

	// A navigation invalidates every reference taken against the old document.
	t.snapshots.Invalidate(tabID)

	res, apiErr := t.page.Navigate(ctx, req.URL, req.WaitUntil, timeout)
	if apiErr != nil {
		return nil, apiErr
	}
	s.touch(m.cfg.Now())
	return res, nil
}

// Snapshot renders the accessibility tree for a tab.
func (m *Manager) Snapshot(ctx context.Context, sessionID, tabID string, req types.SnapshotRequest) (*types.SnapshotResult, *types.APIError) {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	leave, apiErr := t.enter(ctx, m.cfg.Limits)
	if apiErr != nil {
		return nil, apiErr
	}
	defer leave()

	res, apiErr := snapshots.Build(ctx, t.snapshots, tabID, t.page, snapshots.Options{
		MaxChars:      req.MaxChars,
		MaxRefs:       req.MaxRefs,
		IncludeValues: req.IncludeValues,
		Scope:         req.Scope,
		Timeout:       m.cfg.Limits.OperationTimeout,
	})
	if apiErr != nil {
		return nil, apiErr
	}
	s.touch(m.cfg.Now())
	return &res.Public, nil
}

// Persona reports the observable identity of a tab.
func (m *Manager) PersonaReport(ctx context.Context, sessionID, tabID string) (*types.PersonaReport, *types.APIError) {
	_, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	leave, apiErr := t.enter(ctx, m.cfg.Limits)
	if apiErr != nil {
		return nil, apiErr
	}
	defer leave()
	rep, apiErr := t.page.VerifyPersona(ctx)
	if apiErr != nil {
		return nil, apiErr
	}
	return &types.PersonaReport{
		Persona:             rep.Persona,
		Consistent:          rep.Consistent(),
		Mismatches:          rep.Mismatches,
		UserAgent:           rep.UserAgent,
		Platform:            rep.Platform,
		Webdriver:           rep.Webdriver,
		HardwareConcurrency: rep.HardwareConcurrency,
		DeviceMemory:        rep.DeviceMemory,
		Screen:              rep.Screen,
		Window:              rep.Window,
		UnmaskedRenderer:    rep.UnmaskedRenderer,
		Timezone:            rep.Timezone,
		PointerFine:         rep.PointerFine,
		ChromeObject:        rep.ChromeObject,
		Plugins:             rep.Plugins,
	}, nil
}

func (s *session) touch(now time.Time) {
	s.mu.Lock()
	s.lastUsed = now
	s.mu.Unlock()
}

// ensure import use
var _ = fmt.Sprintf
var _ = browser.MaxScreenshotBytes
