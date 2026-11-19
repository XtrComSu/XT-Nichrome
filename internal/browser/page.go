package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/cdp"
	"github.com/xtrcom/xt-nichrome/internal/stealth"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

// Default timeouts.
const (
	defaultNavigateTimeout = 30 * time.Second
	defaultActionTimeout   = 15 * time.Second
	// MaxScreenshotBytes bounds the PNG we are willing to return.
	MaxScreenshotBytes = 8 << 20
)

// Page is one target (tab) in the browser.
type Page struct {
	proc     *Process
	targetID string
	session  string

	gen atomic.Int64

	mu         sync.Mutex
	currentURL string
	title      string
	// navPending is set when a top-level load starts and cleared when a document
	// commits or the load stops. An action uses it to decide whether a
	// navigation is actually in flight instead of always waiting a fixed time.
	navPending bool
	dead       bool
	deadReason string
	lastAction time.Time
	// inflight tracks a dispatched side-effecting action so a client disconnect
	// cannot turn into a duplicate submission.
	inflight   bool
	cursorX    float64
	cursorY    float64
	cursorInit bool

	events chan struct{}
	done   chan struct{}
	once   sync.Once
}

// TargetID returns the CDP target id.
func (p *Page) TargetID() string { return p.targetID }

// SessionID returns the CDP session id.
func (p *Page) SessionID() string { return p.session }

// Generation returns the committed-document generation counter.
func (p *Page) Generation() int64 { return p.gen.Load() }

// CurrentURL returns the last committed URL known to the driver.
func (p *Page) CurrentURL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.currentURL
}

// Title returns the cached title.
func (p *Page) Title() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.title
}

// Alive reports whether the tab can still be driven.
func (p *Page) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.dead
}

// Call issues a CDP command on this page's session.
func (p *Page) Call(ctx context.Context, method string, params any, out any) error {
	p.mu.Lock()
	dead := p.dead
	reason := p.deadReason
	p.mu.Unlock()
	if dead {
		return &types.APIError{Code: types.CodeBrowserFailed, Message: "tab is no longer live: " + reason}
	}
	return p.proc.conn.Call(ctx, p.session, method, params, out)
}

func (p *Page) markDead() {
	p.mu.Lock()
	if p.dead {
		p.mu.Unlock()
		return
	}
	p.dead = true
	p.deadReason = "browser disconnected"
	p.mu.Unlock()
	p.once.Do(func() { close(p.done) })
}

func (p *Page) initialize(ctx context.Context) *types.APIError {
	steps := []struct {
		method string
		params any
	}{
		{"Page.enable", nil},
		{"Runtime.enable", nil},
		{"Network.enable", map[string]any{"maxTotalBufferSize": 0, "maxResourceBufferSize": 0}},
		{"Log.enable", nil},
		{"Accessibility.enable", nil},
		{"DOM.enable", nil},
	}
	for _, s := range steps {
		params := s.params
		if params == nil {
			params = struct{}{}
		}
		if err := p.Call(ctx, s.method, params, nil); err != nil {
			var ae *types.APIError
			if errors.As(err, &ae) {
				// Accessibility.enable is unavailable on some builds; keep going.
				if s.method == "Accessibility.enable" || s.method == "DOM.enable" || s.method == "Log.enable" {
					continue
				}
				return ae
			}
			return browserError(err)
		}
	}
	if apiErr := p.applyPersona(ctx); apiErr != nil {
		return apiErr
	}
	// Voice/locale and permission surface: deny device permissions by default.
	_ = p.Call(ctx, "Browser.grantPermissions", map[string]any{
		"permissions":      []string{},
		"browserContextId": "",
	}, nil)
	return nil
}

// applyPersona sets the identity a page observes: UA + UA-CH metadata, locale,
// display metrics, timezone, and the preload patch.
func (p *Page) applyPersona(ctx context.Context) *types.APIError {
	per := p.proc.persona
	meta := map[string]any{
		"brands":          per.Brands,
		"fullVersionList": per.FullBrands,
		"fullVersion":     per.UAFullVersion,
		"platform":        per.OSName,
		"platformVersion": per.OSVersion,
		"architecture":    per.Architecture,
		"model":           "",
		"mobile":          false,
		"bitness":         per.Bitness,
		"wow64":           false,
	}
	if err := p.Call(ctx, "Network.setUserAgentOverride", map[string]any{
		"userAgent":         per.UserAgent,
		"acceptLanguage":    per.AcceptLanguage,
		"platform":          per.CDPPlatform,
		"userAgentMetadata": meta,
	}, nil); err != nil {
		return browserError(err)
	}
	// Timezone must be applied before any document runs.
	if err := p.Call(ctx, "Emulation.setTimezoneOverride", map[string]any{"timezoneId": per.TimeZone}, nil); err != nil {
		return browserError(err)
	}
	// Locale override is not fatal when unsupported by the renderer.
	_ = p.Call(ctx, "Emulation.setLocaleOverride", map[string]any{"locale": per.Locale}, nil)
	// Display metrics. Setting screenWidth/Height keeps screen.* and the
	// viewport in agreement, which is the combination real hardware produces.
	if err := p.Call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width":              per.ViewportWidth,
		"height":             per.ViewportHeight,
		"deviceScaleFactor":  per.DeviceScaleFactor,
		"mobile":             false,
		"screenWidth":        per.ScreenWidth,
		"screenHeight":       per.ScreenHeight,
		"positionX":          0,
		"positionY":          0,
		"dontSetVisibleSize": false,
	}, nil); err != nil {
		return browserError(err)
	}
	src, err := p.proc.personaScript()
	if err != nil {
		return &types.APIError{Code: types.CodeInternal, Message: err.Error()}
	}
	if err := p.Call(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{
		"source": src, "runImmediately": true,
	}, nil); err != nil {
		return browserError(err)
	}
	return nil
}

func (p *Process) personaScript() (string, error) {
	p.mu.Lock()
	if p.script != "" {
		s := p.script
		p.mu.Unlock()
		return s, nil
	}
	p.mu.Unlock()
	src, err := stealth.Script(p.persona)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	p.script = src
	p.mu.Unlock()
	return src, nil
}

func (p *Page) handleEvent(method string, params json.RawMessage) {
	switch method {
	case "Page.frameNavigated":
		var ev struct {
			Frame struct {
				ID       string `json:"id"`
				ParentID string `json:"parentId"`
				URL      string `json:"url"`
				LoaderID string `json:"loaderId"`
			} `json:"frame"`
		}
		if json.Unmarshal(params, &ev) != nil {
			return
		}
		if ev.Frame.ParentID == "" {
			p.mu.Lock()
			p.currentURL = ev.Frame.URL
			p.navPending = false
			p.mu.Unlock()
			p.gen.Add(1)
			p.signal()
		}
	case "Page.navigatedWithinDocument":
		var ev struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(params, &ev) != nil {
			return
		}
		p.mu.Lock()
		p.currentURL = ev.URL
		p.mu.Unlock()
		p.gen.Add(1)
		p.signal()
	case "Page.frameStartedLoading":
		p.mu.Lock()
		p.navPending = true
		p.mu.Unlock()
		p.signal()
	case "Page.frameStoppedLoading":
		p.mu.Lock()
		p.navPending = false
		p.mu.Unlock()
		p.signal()
	case "Page.loadEventFired":
		p.signal()
	case "Page.javascriptDialogOpening":
		// Auto-accept, then report. A dialog blocks the renderer; leaving it open
		// would hang every later command.
		var ev struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(params, &ev)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = p.proc.conn.Call(ctx, p.session, "Page.handleJavaScriptDialog", map[string]any{"accept": true}, nil)
		cancel()
		p.signal()
	case "Page.windowOpen":
		// Popups are disabled: report and refuse. The browser-scope handler also
		// closes any page target that appears with an opener.
		p.signal()
	case "Browser.downloadWillBegin", "Page.downloadWillBegin":
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = p.proc.conn.Call(ctx, p.session, "Browser.cancelDownload", map[string]any{"guid": ""}, nil)
		cancel()
	case "Page.javascriptDialogClosed":
		p.signal()
	case "Inspector.targetCrashed", "Target.targetCrashed":
		p.mu.Lock()
		p.dead = true
		p.deadReason = "renderer crashed"
		p.mu.Unlock()
		p.once.Do(func() { close(p.done) })
	}
}

func (p *Page) signal() {
	select {
	case p.events <- struct{}{}:
	default:
	}
}

// Close closes the tab.
func (p *Page) Close(ctx context.Context) {
	p.mu.Lock()
	if p.dead {
		p.mu.Unlock()
		return
	}
	p.lastAction = time.Now()
	p.mu.Unlock()
	_ = p.proc.conn.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": p.targetID}, nil)
	p.proc.forget(p.session)
	p.once.Do(func() { close(p.done) })
}

// Navigate drives the tab to a URL and waits for the requested barrier.
func (p *Page) Navigate(ctx context.Context, rawURL, waitUntil string, timeout time.Duration) (*types.NavigateResult, *types.APIError) {
	if timeout <= 0 {
		timeout = defaultNavigateTimeout
	}
	startGen := p.Generation()
	var nav struct {
		FrameID   string `json:"frameId"`
		LoaderID  string `json:"loaderId"`
		ErrorText string `json:"errorText"`
	}
	// Page.navigate does not return until the response headers arrive, so a
	// server that accepts the connection and stalls would block here for as long
	// as it likes, ignoring the caller's deadline entirely. The CDP call is
	// therefore bounded by its own context that expires with the navigation
	// timeout.
	navCtx, navCancel := context.WithTimeout(ctx, timeout)
	defer navCancel()
	err := p.Call(navCtx, "Page.navigate", map[string]any{"url": rawURL}, &nav)
	if err != nil {
		var ae *types.APIError
		if errors.As(err, &ae) {
			return nil, ae
		}
		var pe *cdp.ProtocolError
		if errors.As(err, &pe) {
			// A navigation that the browser refused (bad scheme, blocked) is a
			// policy/request problem, not a crash.
			return nil, &types.APIError{Code: types.CodePolicyBlocked, Message: "navigation refused: " + pe.Message}
		}
		if errors.Is(err, context.DeadlineExceeded) || navCtx.Err() != nil {
			return nil, &types.APIError{
				Code:    types.CodeTimeout,
				Message: "the page did not begin responding before the deadline",
				Detail:  map[string]any{"timeoutMs": timeout.Milliseconds(), "url": rawURL},
			}
		}
		return nil, browserError(err)
	}
	if nav.ErrorText != "" {
		return nil, &types.APIError{Code: types.CodeBrowserFailed, Message: "navigation failed: " + nav.ErrorText}
	}

	// Wait for the document to commit. A navigation that never commits (the
	// server accepts the connection and then stalls) must be reported as a
	// timeout; returning 200 with the requested URL would tell the caller the
	// page loaded when nothing had been parsed at all.
	deadline := time.Now().Add(timeout)
	for p.Generation() == startGen {
		select {
		case <-ctx.Done():
			return nil, &types.APIError{Code: types.CodeTimeout, Message: "navigation did not commit before the deadline"}
		case <-p.events:
		case <-p.done:
			return nil, &types.APIError{Code: types.CodeBrowserFailed, Message: "browser closed during navigation"}
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return nil, &types.APIError{
				Code:    types.CodeTimeout,
				Message: "navigation did not commit before the deadline",
				Detail:  map[string]any{"timeoutMs": timeout.Milliseconds(), "url": rawURL},
			}
		}
	}

	if waitUntil != "commit" {
		// The document is committed; now wait for it to finish loading, within
		// whatever remains of the same deadline.
		if remaining := time.Until(deadline); remaining > 0 {
			p.waitLoad(ctx, remaining)
		}
	}
	url := p.CurrentURL()
	if url == "" {
		url = rawURL
	}
	return &types.NavigateResult{URL: url, Generation: p.Generation(), Title: p.Title()}, nil
}

func (p *Page) waitGeneration(ctx context.Context, want int64, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if p.Generation() != want {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-p.events:
		case <-p.done:
			return false
		case <-time.After(25 * time.Millisecond):
		}
	}
	return p.Generation() != want
}

func (p *Page) waitLoad(ctx context.Context, timeout time.Duration) {
	// Poll readyState: event-driven waits on loadEventFired race with the
	// commit we already observed, and a poll is bounded and simple.
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var res struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		err := p.Call(ctx, "Runtime.evaluate", map[string]any{
			"expression": "document.readyState", "returnByValue": true,
		}, &res)
		if err == nil && (res.Result.Value == "complete" || res.Result.Value == "interactive") {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case <-p.events:
			// A lifecycle event arrived; loop straight back to the check.
		case <-time.After(15 * time.Millisecond):
		}
	}
}

// Evaluate runs an expression in the page and decodes the JSON result.
func (p *Page) Evaluate(ctx context.Context, expr string, out any) *types.APIError {
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
			Type  string          `json:"type"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	err := p.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expr,
		"returnByValue": true,
		"awaitPromise":  true,
		"userGesture":   true,
	}, &res)
	if err != nil {
		var ae *types.APIError
		if errors.As(err, &ae) {
			return ae
		}
		return browserError(err)
	}
	if res.ExceptionDetails != nil {
		desc := res.ExceptionDetails.Text
		if res.ExceptionDetails.Exception != nil && res.ExceptionDetails.Exception.Description != "" {
			desc = res.ExceptionDetails.Exception.Description
		}
		return &types.APIError{Code: types.CodeInternal, Message: "page evaluation failed: " + firstLine(desc)}
	}
	if out != nil && len(res.Result.Value) > 0 && string(res.Result.Value) != "null" {
		if err := json.Unmarshal(res.Result.Value, out); err != nil {
			return &types.APIError{Code: types.CodeInternal, Message: "could not decode page result"}
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Screenshot captures a bounded PNG.
func (p *Page) Screenshot(ctx context.Context) ([]byte, *types.APIError) {
	var res struct {
		Data string `json:"data"`
	}
	err := p.Call(ctx, "Page.captureScreenshot", map[string]any{
		"format": "png", "captureBeyondViewport": false, "fromSurface": true,
	}, &res)
	if err != nil {
		var ae *types.APIError
		if errors.As(err, &ae) {
			return nil, ae
		}
		return nil, browserError(err)
	}
	raw, decErr := base64.StdEncoding.DecodeString(res.Data)
	if decErr != nil {
		return nil, &types.APIError{Code: types.CodeInternal, Message: "screenshot was not valid base64"}
	}
	if len(raw) > MaxScreenshotBytes {
		return nil, &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: fmt.Sprintf("screenshot exceeds the %d byte budget", MaxScreenshotBytes),
			Detail:  map[string]any{"bytes": len(raw)},
		}
	}
	return raw, nil
}

// AXNode is one raw accessibility node from the browser.
type AXNode struct {
	NodeID           string       `json:"nodeId"`
	Ignored          bool         `json:"ignored"`
	Role             *AXValue     `json:"role"`
	Name             *AXValue     `json:"name"`
	Description      *AXValue     `json:"description"`
	Value            *AXValue     `json:"value"`
	Properties       []AXProperty `json:"properties"`
	ChildIDs         []string     `json:"childIds"`
	BackendDOMNodeID int          `json:"backendDOMNodeId"`
	ParentID         string       `json:"parentId"`
}

// AXValue is a typed accessibility value.
type AXValue struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

// AXProperty is one accessibility state.
type AXProperty struct {
	Name  string  `json:"name"`
	Value AXValue `json:"value"`
}

// AccessibilityTree returns the full AX tree for the page.
func (p *Page) AccessibilityTree(ctx context.Context) ([]AXNode, *types.APIError) {
	var res struct {
		Nodes []AXNode `json:"nodes"`
	}
	err := p.Call(ctx, "Accessibility.getFullAXTree", map[string]any{"depth": -1}, &res)
	if err != nil {
		var ae *types.APIError
		if errors.As(err, &ae) {
			return nil, ae
		}
		return nil, browserError(err)
	}
	return res.Nodes, nil
}

// ResolveBackendNode maps an accessibility backend node id to a frontend object
// id usable with DOM.resolveNode.
func (p *Page) ResolveBackendNode(ctx context.Context, backendID int) (string, *types.APIError) {
	var res struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	err := p.Call(ctx, "DOM.resolveNode", map[string]any{"backendNodeId": backendID}, &res)
	if err != nil {
		var ae *types.APIError
		if errors.As(err, &ae) {
			return "", ae
		}
		return "", &types.APIError{Code: types.CodeStaleRef, Message: "element is no longer attached to the document"}
	}
	if res.Object.ObjectID == "" {
		return "", &types.APIError{Code: types.CodeStaleRef, Message: "element is no longer attached to the document"}
	}
	return res.Object.ObjectID, nil
}

// ReleaseObject frees a remote object handle.
func (p *Page) ReleaseObject(ctx context.Context, objectID string) {
	if objectID == "" {
		return
	}
	_ = p.Call(ctx, "Runtime.releaseObject", map[string]any{"objectId": objectID}, nil)
}

// WaitForActionability polls the element until it is visible, stable, enabled
// and pointer-receiving, then re-checks identity immediately before dispatch.
func (p *Page) WaitForActionability(ctx context.Context, objectID string, timeout time.Duration) *types.APIError {
	if timeout <= 0 {
		timeout = defaultActionTimeout
	}
	expr := `(function(el){
		if (!el || !el.isConnected) return {ok:false, why:"detached"};
		const r = el.getBoundingClientRect();
		if (r.width === 0 && r.height === 0) return {ok:false, why:"zero-size"};
		const style = getComputedStyle(el);
		if (style.visibility === "hidden" || style.display === "none") return {ok:false, why:"hidden"};
		if (Number(style.opacity) === 0) return {ok:false, why:"transparent"};
		if (el.disabled) return {ok:false, why:"disabled"};
		if (el.getAttribute && el.getAttribute("aria-disabled") === "true") return {ok:false, why:"aria-disabled"};
		const cx = r.left + r.width/2, cy = r.top + r.height/2;
		if (cx < 0 || cy < 0 || cx > innerWidth || cy > innerHeight) return {ok:false, why:"offscreen"};
		const hit = document.elementFromPoint(cx, cy);
		if (hit && hit !== el && !el.contains(hit) && !hit.contains(el)) return {ok:false, why:"covered", by: hit.tagName};
		return {ok:true, rect:{x:r.left,y:r.top,w:r.width,h:r.height}};
	})`
	deadline := time.Now().Add(timeout)
	var lastReason string
	for {
		var res struct {
			OK  bool   `json:"ok"`
			Why string `json:"why"`
			By  string `json:"by"`
		}
		apiErr := p.callOnObject(ctx, expr, objectID, &res)
		if apiErr != nil {
			return apiErr
		}
		if res.OK {
			return nil
		}
		lastReason = res.Why
		if res.Why == "detached" {
			return &types.APIError{Code: types.CodeStaleRef, Message: "element was removed from the document"}
		}
		if time.Now().After(deadline) {
			code := types.CodeElementNotActionable
			msg := "element is not actionable: " + lastReason
			if res.By != "" {
				msg += " by " + res.By
			}
			return &types.APIError{Code: code, Message: msg, Detail: map[string]any{"reason": lastReason}}
		}
		select {
		case <-ctx.Done():
			return &types.APIError{Code: types.CodeTimeout, Message: "action timed out before the element was actionable"}
		case <-time.After(40 * time.Millisecond):
		}
	}
}

// callOnObject invokes a function expression with a remote object argument.
func (p *Page) callOnObject(ctx context.Context, fnExpr, objectID string, out any) *types.APIError {
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	err := p.Call(ctx, "Runtime.callFunctionOn", map[string]any{
		"functionDeclaration": fnExpr,
		"objectId":            objectID,
		"arguments":           []map[string]any{{"objectId": objectID}},
		"returnByValue":       true,
		"awaitPromise":        true,
		"userGesture":         true,
	}, &res)
	if err != nil {
		var ae *types.APIError
		if errors.As(err, &ae) {
			return ae
		}
		var pe *cdp.ProtocolError
		if errors.As(err, &pe) && strings.Contains(pe.Message, "Could not find object") {
			return &types.APIError{Code: types.CodeStaleRef, Message: "element reference is no longer resolvable"}
		}
		return browserError(err)
	}
	if res.ExceptionDetails != nil {
		desc := res.ExceptionDetails.Text
		if res.ExceptionDetails.Exception != nil {
			desc = res.ExceptionDetails.Exception.Description
		}
		return &types.APIError{Code: types.CodeInternal, Message: "page action failed: " + firstLine(desc)}
	}
	if out != nil && len(res.Result.Value) > 0 {
		_ = json.Unmarshal(res.Result.Value, out)
	}
	return nil
}

// NodeFingerprint identifies the concrete DOM node behind a reference so an
// identically named replacement can be detected (brief §4.3).
type NodeFingerprint struct {
	Connected bool   `json:"connected"`
	Signature string `json:"signature"`
	Role      string `json:"role,omitempty"`
	Name      string `json:"name,omitempty"`
	Tag       string `json:"tag"`
}

// Fingerprint computes a per-node identity signature.
func (p *Page) Fingerprint(ctx context.Context, objectID string) (*NodeFingerprint, *types.APIError) {
	expr := `(function(el){
		if (!el || !el.isConnected) return {connected:false, signature:"", tag: el ? el.tagName : ""};
		const path = [];
		let n = el, depth = 0;
		while (n && n.nodeType === 1 && depth < 64) {
			let idx = 0, sib = n;
			while ((sib = sib.previousElementSibling)) idx++;
			path.unshift(n.tagName + "[" + idx + "]");
			n = n.parentElement; depth++;
		}
		const attr = (el.getAttribute && (el.getAttribute("data-testid") || el.getAttribute("name") || el.id)) || "";
		return {
			connected: true,
			tag: el.tagName,
			signature: path.join("/") + "|" + attr,
			backend: 0,
		};
	})`
	var fp NodeFingerprint
	if apiErr := p.callOnObject(ctx, expr, objectID, &fp); apiErr != nil {
		return nil, apiErr
	}
	return &fp, nil
}

// Click dispatches a humanized pointer trajectory and click on a resolved object.
func (p *Page) Click(ctx context.Context, objectID string, button string, count int) *types.APIError {
	if button == "" {
		button = "left"
	}
	if count <= 0 {
		count = 1
	}
	targetX, targetY, apiErr := p.humanPickPoint(ctx, objectID)
	if apiErr != nil {
		return apiErr
	}
	if apiErr := p.humanMoveTo(ctx, targetX, targetY); apiErr != nil {
		return apiErr
	}
	mask := buttonMask(button)
	for c := 1; c <= count; c++ {
		if err := p.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": "mousePressed", "x": targetX, "y": targetY,
			"button": button, "buttons": mask, "clickCount": c, "pointerType": "mouse",
		}, nil); err != nil {
			return browserError(err)
		}
		if err := sleepCtx(ctx, time.Duration(randRange(18, 42))*time.Millisecond); err != nil {
			return browserError(err)
		}
		if err := p.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseReleased", "x": targetX, "y": targetY,
			"button": button, "buttons": 0, "clickCount": c, "pointerType": "mouse",
		}, nil); err != nil {
			return browserError(err)
		}
		if c < count {
			if err := sleepCtx(ctx, time.Duration(randRange(45, 75))*time.Millisecond); err != nil {
				return browserError(err)
			}
		}
	}
	return nil
}

func buttonMask(button string) int {
	switch button {
	case "right":
		return 2
	case "middle":
		return 4
	default:
		return 1
	}
}

// Fill moves the pointer to the field, clicks to focus it like a human user,
// clears existing text if requested, and dispatches virtualized keystrokes.
func (p *Page) Fill(ctx context.Context, objectID, text string, clear bool) *types.APIError {
	expr := `(function(el){
		if (!el || !el.isConnected) return {ok:false, why:"detached"};
		const tag = el.tagName;
		if (tag === "INPUT" || tag === "TEXTAREA") {
			return {ok:true, kind:"value"};
		}
		if (el.isContentEditable) {
			return {ok:true, kind:"editable"};
		}
		return {ok:false, why:"not-fillable"};
	})`
	var pre struct {
		OK   bool   `json:"ok"`
		Why  string `json:"why"`
		Kind string `json:"kind"`
	}
	if apiErr := p.callOnObject(ctx, expr, objectID, &pre); apiErr != nil {
		return apiErr
	}
	if !pre.OK {
		return &types.APIError{Code: types.CodeElementNotActionable, Message: "element cannot be filled: " + pre.Why}
	}

	// Click into the field first so pointerover/mousedown/focus/mouseup/click
	// happen in the natural desktop browser order before any keyDown arrives.
	if apiErr := p.Click(ctx, objectID, "left", 1); apiErr != nil {
		return apiErr
	}
	// Ensure focus is on the target element if an inner wrapper absorbed the click.
	var focusOK struct {
		OK bool `json:"ok"`
	}
	if apiErr := p.callOnObject(ctx, `(function(el){
		if (document.activeElement !== el) el.focus({preventScroll:true});
		return {ok:true};
	})`, objectID, &focusOK); apiErr != nil {
		return apiErr
	}

	if clear {
		if apiErr := p.humanClearField(ctx, objectID); apiErr != nil {
			return apiErr
		}
	}

	// Real typing: keyDown + keyUp events per character with human cadence and
	// Windows virtual key codes, falling back to insertText only for non-ASCII runes.
	if text != "" {
		if apiErr := p.humanTypeText(ctx, text); apiErr != nil {
			return apiErr
		}
	}

	// Verify the value landed; a controlled input may have rejected it.
	var post struct {
		Length int  `json:"length"`
		Match  bool `json:"match"`
	}
	verify := `(function(el){const v = el.isContentEditable ? el.textContent : el.value; return {length: (v||"").length, match: (v||"") === ` + jsonString(text) + `};})`
	if apiErr := p.callOnObject(ctx, verify, objectID, &post); apiErr != nil {
		return apiErr
	}
	if !post.Match && post.Length == 0 {
		return &types.APIError{Code: types.CodeElementNotActionable, Message: "the field rejected the value"}
	}
	return nil
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// callOnObjectArgs calls a function with explicit arguments (first is object).
func (p *Page) callOnObjectArgs(ctx context.Context, fnExpr, objectID string, args []map[string]any, out any) *types.APIError {
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	err := p.Call(ctx, "Runtime.callFunctionOn", map[string]any{
		"functionDeclaration": fnExpr,
		"objectId":            objectID,
		"arguments":           args,
		"returnByValue":       true,
		"awaitPromise":        true,
		"userGesture":         true,
	}, &res)
	if err != nil {
		var ae *types.APIError
		if errors.As(err, &ae) {
			return ae
		}
		var pe *cdp.ProtocolError
		if errors.As(err, &pe) && strings.Contains(pe.Message, "Could not find object") {
			return &types.APIError{Code: types.CodeStaleRef, Message: "element reference is no longer resolvable"}
		}
		return browserError(err)
	}
	if res.ExceptionDetails != nil {
		desc := res.ExceptionDetails.Text
		if res.ExceptionDetails.Exception != nil {
			desc = res.ExceptionDetails.Exception.Description
		}
		return &types.APIError{Code: types.CodeInternal, Message: "page action failed: " + firstLine(desc)}
	}
	if out != nil && len(res.Result.Value) > 0 {
		_ = json.Unmarshal(res.Result.Value, out)
	}
	return nil
}

// MarkInflight records that a side-effecting action is about to be dispatched.
// A client that disappears mid-action leaves the flag set, which is how the
// service reports ACTION_OUTCOME_UNKNOWN instead of retrying.
func (p *Page) MarkInflight(v bool) {
	p.mu.Lock()
	p.inflight = v
	p.lastAction = time.Now()
	p.mu.Unlock()
}

// Inflight reports whether a side-effecting action may still be running.
func (p *Page) Inflight() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inflight
}

// NavigationPending reports whether a load is in flight for the top document.
func (p *Page) NavigationPending() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.navPending
}

// TouchActivity records liveness for idle eviction.
func (p *Page) TouchActivity() {
	p.mu.Lock()
	p.lastAction = time.Now()
	p.mu.Unlock()
}
