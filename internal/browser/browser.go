// Package browser owns the Chromium lifecycle: process launch, contexts, pages,
// navigation, dialogue/download handling, action dispatch and screenshots.
//
// One Process is one browser process. Ephemeral sessions share a Process and get
// an isolated browser context each; persistent sessions get a dedicated Process
// so their on-disk profile is never touched by a second launch.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/cdp"
	"github.com/xtrcom/xt-nichrome/internal/stealth"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

// Options configure a browser process.
type Options struct {
	ExecPath     string
	Headless     bool
	Persona      stealth.Persona
	ProfileDir   string
	FontConfPath string
	ProxyURL     string
	// ExtraArgs are appended last (tests use them to force failure).
	ExtraArgs []string
	// TempRoot is where --crash-dumps-dir and friends point.
	TempRoot string
	// DevShmDir overrides /dev/shm for containers.
	DevShmDir string
}

// Process is a single Chromium process plus its CDP connection.
type Process struct {
	conn    *cdp.Conn
	opts    Options
	persona stealth.Persona

	mu       sync.Mutex
	pages    map[string]*Page
	contexts map[string]bool
	closed   bool
	crashErr error
	onCrash  []func()

	nextPage atomic.Int64
	started  time.Time
	script   string
}

// Launch starts the browser.
func Launch(ctx context.Context, opts Options) (*Process, error) {
	if opts.ExecPath == "" {
		return nil, fmt.Errorf("browser: exec path is required")
	}
	if st, err := os.Stat(opts.ExecPath); err != nil {
		return nil, fmt.Errorf("browser: exec path %s: %w", opts.ExecPath, err)
	} else if st.IsDir() {
		return nil, fmt.Errorf("browser: exec path %s is a directory", opts.ExecPath)
	}
	if err := opts.Persona.Validate(); err != nil {
		return nil, err
	}
	cfg := stealth.BuildFlags(stealth.FlagsInput{
		Persona:      opts.Persona,
		ExecPath:     opts.ExecPath,
		ProfileDir:   opts.ProfileDir,
		Headless:     opts.Headless,
		ProxyURL:     opts.ProxyURL,
		FontConfPath: opts.FontConfPath,
		Languages:    strings.Join(opts.Persona.Languages, ","),
		ExtraArgs:    opts.ExtraArgs,
	})
	args := append(cfg.Args, "about:blank")

	env := append(os.Environ(), cfg.Env...)
	if opts.FontConfPath != "" {
		env = append(env, "FONTCONFIG_FILE="+opts.FontConfPath)
	}
	conn, err := cdp.Launch(cdp.Options{ExecPath: opts.ExecPath, Args: args, Env: env})
	if err != nil {
		return nil, err
	}
	p := &Process{
		conn:     conn,
		opts:     opts,
		persona:  opts.Persona,
		pages:    map[string]*Page{},
		contexts: map[string]bool{},
		started:  time.Now(),
	}
	conn.SetEventHandler(p.handleEvent)

	// Fail fast: a launch that cannot answer getVersion is not usable.
	vctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var ver struct {
		Product  string `json:"product"`
		Revision string `json:"revision"`
	}
	if err := conn.Call(vctx, "", "Browser.getVersion", nil, &ver); err != nil {
		stderr := conn.Stderr()
		_ = conn.Close()
		return nil, fmt.Errorf("browser: no CDP session (%v)%s", err, explainStderr(stderr))
	}
	p.persona.BrowserRevision = ver.Product

	// Harden the browser-wide surface: no downloads, no popup auto-open.
	_ = conn.Call(ctx, "", "Browser.setDownloadBehavior", map[string]any{"behavior": "deny"}, nil)
	_ = conn.Call(ctx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil)

	go func() {
		<-conn.Done()
		p.markCrashed(fmt.Errorf("browser: connection lost"))
	}()
	return p, nil
}

// explainStderr turns the usual container launch failures into an actionable
// message instead of "browser exited".
func explainStderr(s string) string {
	if s == "" {
		return ""
	}
	low := strings.ToLower(s)
	var hints []string
	switch {
	case strings.Contains(low, "no usable sandbox"):
		hints = append(hints, "renderer sandbox unavailable: enable unprivileged user namespaces (sysctl kernel.unprivileged_userns_clone=1) or run in a user namespace; do not disable the sandbox")
	case strings.Contains(low, "cannot open shared object"), strings.Contains(low, "error while loading shared libraries"):
		hints = append(hints, "missing shared library; install the pinned build's dependencies")
	case strings.Contains(low, "no space left"):
		hints = append(hints, "disk full")
	case strings.Contains(low, "singletonlock"), strings.Contains(low, "profile appears to be in use"):
		hints = append(hints, "profile directory is already held by another browser process")
	}
	tail := s
	if len(tail) > 1200 {
		tail = tail[len(tail)-1200:]
	}
	if len(hints) == 0 {
		return ": " + tail
	}
	return ": " + strings.Join(hints, "; ") + " | stderr: " + tail
}

// Alive reports whether the browser can still serve commands.
func (p *Process) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.crashErr != nil {
		return false
	}
	select {
	case <-p.conn.Done():
		return false
	default:
		return true
	}
}

// Persona returns the persona in force.
func (p *Process) Persona() stealth.Persona { return p.persona }

// Revision returns the browser product string.
func (p *Process) Revision() string { return p.persona.BrowserRevision }

// PID returns the browser process id.
func (p *Process) PID() int { return p.conn.PID() }

// Stderr returns captured browser stderr.
func (p *Process) Stderr() string { return p.conn.Stderr() }

// Done is closed when the browser is gone.
func (p *Process) Done() <-chan struct{} { return p.conn.Done() }

// OnCrash registers a callback invoked once when the browser disconnects.
func (p *Process) OnCrash(fn func()) {
	p.mu.Lock()
	p.onCrash = append(p.onCrash, fn)
	p.mu.Unlock()
}

func (p *Process) markCrashed(err error) {
	p.mu.Lock()
	if p.crashErr != nil || p.closed {
		p.mu.Unlock()
		return
	}
	p.crashErr = err
	cbs := append([]func(){}, p.onCrash...)
	pages := p.pages
	p.pages = map[string]*Page{}
	p.mu.Unlock()
	for _, pg := range pages {
		pg.markDead()
	}
	for _, cb := range cbs {
		cb()
	}
}

// Close shuts the browser down, letting Chromium flush the profile to disk.
func (p *Process) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Graceful path: asks the browser to exit and flush storage.
	_ = p.conn.Call(ctx, "", "Browser.close", nil, nil)
	select {
	case <-p.conn.Exited():
	case <-time.After(4 * time.Second):
	}
	return p.conn.Close()
}

// NewContext creates an isolated browser context.
func (p *Process) NewContext(ctx context.Context) (string, *types.APIError) {
	var res struct {
		BrowserContextID string `json:"browserContextId"`
	}
	err := p.conn.Call(ctx, "", "Target.createBrowserContext", map[string]any{
		"disposeOnDetach": false,
	}, &res)
	if err != nil {
		return "", browserError(err)
	}
	p.mu.Lock()
	p.contexts[res.BrowserContextID] = true
	p.mu.Unlock()
	return res.BrowserContextID, nil
}

// DisposeContext destroys a context and everything in it.
func (p *Process) DisposeContext(ctx context.Context, id string) {
	if id == "" {
		return
	}
	_ = p.conn.Call(ctx, "", "Target.disposeBrowserContext", map[string]any{"browserContextId": id}, nil)
	p.mu.Lock()
	delete(p.contexts, id)
	p.mu.Unlock()
}

// NewPage creates a page, applies the persona, and returns it ready for use.
func (p *Process) NewPage(ctx context.Context, contextID string) (*Page, *types.APIError) {
	params := map[string]any{"url": "about:blank"}
	if contextID != "" {
		params["browserContextId"] = contextID
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := p.conn.Call(ctx, "", "Target.createTarget", params, &created); err != nil {
		return nil, browserError(err)
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := p.conn.Call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": created.TargetID, "flatten": true,
	}, &attached); err != nil {
		_ = p.conn.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": created.TargetID}, nil)
		return nil, browserError(err)
	}
	pg := &Page{
		proc:     p,
		targetID: created.TargetID,
		session:  attached.SessionID,
		events:   make(chan struct{}, 64),
		done:     make(chan struct{}),
	}
	p.mu.Lock()
	p.pages[attached.SessionID] = pg
	p.mu.Unlock()

	pg.gen.Store(1)
	if apiErr := pg.initialize(ctx); apiErr != nil {
		pg.Close(context.Background())
		return nil, apiErr
	}
	return pg, nil
}

// Pages returns a snapshot of live pages.
func (p *Process) Pages() []*Page {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*Page, 0, len(p.pages))
	for _, pg := range p.pages {
		out = append(out, pg)
	}
	return out
}

func (p *Process) forget(sessionID string) {
	p.mu.Lock()
	delete(p.pages, sessionID)
	p.mu.Unlock()
}

func (p *Process) handleEvent(method, sessionID string, params json.RawMessage) {
	p.mu.Lock()
	pg := p.pages[sessionID]
	p.mu.Unlock()
	if pg != nil {
		pg.handleEvent(method, params)
		return
	}
	// Browser-scope events: popups must not open. Any page target created by a
	// page in our tree is closed immediately, except contexts we created.
	switch method {
	case "Target.targetCreated":
		var ev struct {
			TargetInfo struct {
				TargetID string `json:"targetId"`
				Type     string `json:"type"`
				OpenerID string `json:"openerId"`
				URL      string `json:"url"`
			} `json:"targetInfo"`
		}
		if json.Unmarshal(params, &ev) != nil {
			return
		}
		if ev.TargetInfo.OpenerID != "" && ev.TargetInfo.Type == "page" {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = p.conn.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": ev.TargetInfo.TargetID}, nil)
			cancel()
		}
	}
}

func browserError(err error) *types.APIError {
	if err == nil {
		return nil
	}
	if errors.Is(err, cdp.ErrClosed) {
		return types.NewError(types.CodeBrowserFailed, "browser connection lost")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return types.NewError(types.CodeTimeout, "browser operation timed out")
	}
	var pe *cdp.ProtocolError
	if errors.As(err, &pe) {
		return &types.APIError{Code: types.CodeBrowserFailed, Message: "browser rejected the operation: " + pe.Message}
	}
	return &types.APIError{Code: types.CodeBrowserFailed, Message: err.Error()}
}

// WriteFontConf renders the fontconfig file used for this daemon.
func WriteFontConf(path, fontDir, cacheDir string) error {
	tpl := `<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "urn:fontconfig:fonts.dtd">
<fontconfig>
  <dir>` + fontDir + `</dir>
  <cachedir>` + cacheDir + `</cachedir>
  <config></config>
  <alias binding="same"><family>sans-serif</family><prefer><family>Segoe UI</family></prefer></alias>
  <alias binding="same"><family>system-ui</family><prefer><family>Segoe UI</family></prefer></alias>
  <alias binding="same"><family>serif</family><prefer><family>Times New Roman</family></prefer></alias>
  <alias binding="same"><family>monospace</family><prefer><family>Consolas</family></prefer></alias>
  <alias binding="same"><family>Helvetica</family><prefer><family>Arial</family></prefer></alias>
  <alias binding="same"><family>-apple-system</family><prefer><family>Segoe UI</family></prefer></alias>
  <alias binding="same"><family>BlinkMacSystemFont</family><prefer><family>Segoe UI</family></prefer></alias>
  <alias binding="same"><family>emoji</family><prefer><family>Segoe UI Emoji</family></prefer></alias>
  <match target="font">
    <edit name="hinting" mode="assign"><bool>true</bool></edit>
    <edit name="hintstyle" mode="assign"><const>hintfull</const></edit>
    <edit name="antialias" mode="assign"><bool>true</bool></edit>
    <edit name="rgba" mode="assign"><const>none</const></edit>
    <edit name="lcdfont" mode="assign"><const>lcddefault</const></edit>
  </match>
</fontconfig>
`
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(tpl), 0o600)
}
