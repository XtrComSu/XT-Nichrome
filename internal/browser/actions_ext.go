package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/types"
)

const (
	maxConsoleEntries = 100
	maxNetworkEntries = 100
	maxDialogEntries  = 50
	MaxPDFBytes       = 16 << 20
)

// Hover moves the virtual pointer along a minimum-jerk Bézier trajectory onto
// the resolved element and pauses briefly so CSS :hover and JS mouseenter/
// mouseover/pointerover listeners fire naturally.
func (p *Page) Hover(ctx context.Context, objectID string) *types.APIError {
	targetX, targetY, apiErr := p.humanPickPoint(ctx, objectID)
	if apiErr != nil {
		return apiErr
	}
	if apiErr := p.humanMoveTo(ctx, targetX, targetY); apiErr != nil {
		return apiErr
	}
	if err := sleepCtx(ctx, time.Duration(randRange(55, 110))*time.Millisecond); err != nil {
		return browserError(err)
	}
	return nil
}

// Scroll performs humanized momentum mouse-wheel scrolling. When objectID is
// non-empty, the virtual cursor first moves inside the target container so
// scrollable sub-containers scroll naturally.
func (p *Page) Scroll(ctx context.Context, objectID, direction string, amount int) *types.APIError {
	if direction == "" {
		direction = "down"
	}
	if amount <= 0 {
		amount = 480
	}
	if amount > 10000 {
		amount = 10000
	}

	var x, y float64
	if objectID != "" {
		tx, ty, apiErr := p.humanPickPoint(ctx, objectID)
		if apiErr != nil {
			return apiErr
		}
		if apiErr := p.humanMoveTo(ctx, tx, ty); apiErr != nil {
			return apiErr
		}
		x, y = tx, ty
	} else {
		x, y = p.ensureCursor()
	}

	var totalDX, totalDY float64
	switch strings.ToLower(direction) {
	case "down":
		totalDY = float64(amount)
	case "up":
		totalDY = -float64(amount)
	case "right":
		totalDX = float64(amount)
	case "left":
		totalDX = -float64(amount)
	default:
		return &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "direction must be up, down, left, or right",
			Detail:  map[string]any{"field": "direction"},
		}
	}

	// Break the scroll into 6 momentum-eased wheel ticks (bell-shaped impulse:
	// gentle start, faster middle, decelerating tail) like a human trackpad or
	// notched scroll wheel.
	weights := []float64{0.10, 0.18, 0.24, 0.22, 0.16, 0.10}
	for _, w := range weights {
		dx := math.Round(totalDX*w + randTriangular(1.5))
		dy := math.Round(totalDY*w + randTriangular(1.5))
		if totalDX == 0 {
			dx = 0
		}
		if totalDY == 0 {
			dy = 0
		}
		if err := p.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type":        "mouseWheel",
			"x":           x,
			"y":           y,
			"deltaX":      dx,
			"deltaY":      dy,
			"pointerType": "mouse",
		}, nil); err != nil {
			return browserError(err)
		}
		if err := sleepCtx(ctx, time.Duration(randRange(10, 22))*time.Millisecond); err != nil {
			return browserError(err)
		}
	}
	return nil
}

// namedKeySpec describes a non-printable or chordable keyboard key.
type namedKeySpec struct {
	Key  string
	Code string
	VK   int
	Text string
}

var namedKeys = map[string]namedKeySpec{
	"enter":      {Key: "Enter", Code: "Enter", VK: 13, Text: "\r"},
	"return":     {Key: "Enter", Code: "Enter", VK: 13, Text: "\r"},
	"tab":        {Key: "Tab", Code: "Tab", VK: 9},
	"escape":     {Key: "Escape", Code: "Escape", VK: 27},
	"esc":        {Key: "Escape", Code: "Escape", VK: 27},
	"backspace":  {Key: "Backspace", Code: "Backspace", VK: 8},
	"delete":     {Key: "Delete", Code: "Delete", VK: 46},
	"space":      {Key: " ", Code: "Space", VK: 32, Text: " "},
	" ":          {Key: " ", Code: "Space", VK: 32, Text: " "},
	"arrowup":    {Key: "ArrowUp", Code: "ArrowUp", VK: 38},
	"up":         {Key: "ArrowUp", Code: "ArrowUp", VK: 38},
	"arrowdown":  {Key: "ArrowDown", Code: "ArrowDown", VK: 40},
	"down":       {Key: "ArrowDown", Code: "ArrowDown", VK: 40},
	"arrowleft":  {Key: "ArrowLeft", Code: "ArrowLeft", VK: 37},
	"left":       {Key: "ArrowLeft", Code: "ArrowLeft", VK: 37},
	"arrowright": {Key: "ArrowRight", Code: "ArrowRight", VK: 39},
	"right":      {Key: "ArrowRight", Code: "ArrowRight", VK: 39},
	"home":       {Key: "Home", Code: "Home", VK: 36},
	"end":        {Key: "End", Code: "End", VK: 35},
	"pageup":     {Key: "PageUp", Code: "PageUp", VK: 33},
	"pagedown":   {Key: "PageDown", Code: "PageDown", VK: 34},
	"insert":     {Key: "Insert", Code: "Insert", VK: 45},
	"f1":         {Key: "F1", Code: "F1", VK: 112},
	"f2":         {Key: "F2", Code: "F2", VK: 113},
	"f3":         {Key: "F3", Code: "F3", VK: 114},
	"f4":         {Key: "F4", Code: "F4", VK: 115},
	"f5":         {Key: "F5", Code: "F5", VK: 116},
	"f6":         {Key: "F6", Code: "F6", VK: 117},
	"f7":         {Key: "F7", Code: "F7", VK: 118},
	"f8":         {Key: "F8", Code: "F8", VK: 119},
	"f9":         {Key: "F9", Code: "F9", VK: 120},
	"f10":        {Key: "F10", Code: "F10", VK: 121},
	"f11":        {Key: "F11", Code: "F11", VK: 122},
	"f12":        {Key: "F12", Code: "F12", VK: 123},
}

// PressKey dispatches a humanized key press or modifier chord (such as "Enter",
// "Escape", "Tab", "ArrowDown", "Control+a", "Shift+Tab"). If objectID is
// non-empty, it clicks to focus the element before pressing the key.
func (p *Page) PressKey(ctx context.Context, objectID, chord string) *types.APIError {
	chord = strings.TrimSpace(chord)
	if chord == "" {
		return &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "key is required for press",
			Detail:  map[string]any{"field": "key"},
		}
	}
	if objectID != "" {
		if apiErr := p.Click(ctx, objectID, "left", 1); apiErr != nil {
			return apiErr
		}
	}

	parts := strings.Split(chord, "+")
	if chord == "+" {
		parts = []string{"+"}
	}
	type modInfo struct {
		key  string
		code string
		vk   int
		bit  int
	}
	var mods []modInfo
	modMask := 0
	mainToken := parts[len(parts)-1]
	for _, rawMod := range parts[:len(parts)-1] {
		switch strings.ToLower(strings.TrimSpace(rawMod)) {
		case "control", "ctrl":
			mods = append(mods, modInfo{key: "Control", code: "ControlLeft", vk: 17, bit: 2})
			modMask |= 2
		case "shift":
			mods = append(mods, modInfo{key: "Shift", code: "ShiftLeft", vk: 16, bit: 8})
			modMask |= 8
		case "alt", "option":
			mods = append(mods, modInfo{key: "Alt", code: "AltLeft", vk: 18, bit: 1})
			modMask |= 1
		case "meta", "cmd", "command", "super", "win":
			mods = append(mods, modInfo{key: "Meta", code: "MetaLeft", vk: 91, bit: 4})
			modMask |= 4
		default:
			return &types.APIError{
				Code:    types.CodeInvalidRequest,
				Message: fmt.Sprintf("unknown modifier %q in key chord %q", rawMod, chord),
				Detail:  map[string]any{"field": "key"},
			}
		}
	}

	var spec namedKeySpec
	if nk, ok := namedKeys[strings.ToLower(mainToken)]; ok {
		spec = nk
	} else if runes := []rune(mainToken); len(runes) == 1 {
		ks, ok := lookupKeySpec(runes[0])
		if !ok {
			return &types.APIError{
				Code:    types.CodeInvalidRequest,
				Message: fmt.Sprintf("unsupported key %q", mainToken),
				Detail:  map[string]any{"field": "key"},
			}
		}
		if ks.Shift && (modMask&8) == 0 {
			mods = append(mods, modInfo{key: "Shift", code: "ShiftLeft", vk: 16, bit: 8})
			modMask |= 8
		}
		txt := ks.Text
		// When Ctrl/Alt/Meta is held, do not emit printable text on keyDown.
		if (modMask & (1 | 2 | 4)) != 0 {
			txt = ""
		}
		spec = namedKeySpec{Key: ks.Key, Code: ks.Code, VK: ks.VK, Text: txt}
	} else {
		return &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: fmt.Sprintf("unsupported key %q", mainToken),
			Detail:  map[string]any{"field": "key"},
		}
	}

	curMask := 0
	for _, m := range mods {
		curMask |= m.bit
		if err := p.Call(ctx, "Input.dispatchKeyEvent", map[string]any{
			"type":                  "keyDown",
			"key":                   m.key,
			"code":                  m.code,
			"location":              1,
			"modifiers":             curMask,
			"windowsVirtualKeyCode": m.vk,
			"nativeVirtualKeyCode":  m.vk,
		}, nil); err != nil {
			return browserError(err)
		}
		_ = sleepCtx(ctx, time.Duration(randRange(4, 10))*time.Millisecond)
	}

	downParams := map[string]any{
		"type":                  "keyDown",
		"key":                   spec.Key,
		"code":                  spec.Code,
		"modifiers":             modMask,
		"windowsVirtualKeyCode": spec.VK,
		"nativeVirtualKeyCode":  spec.VK,
	}
	if spec.Text != "" && (modMask&(1|2|4)) == 0 {
		downParams["text"] = spec.Text
		downParams["unmodifiedText"] = spec.Text
	}
	if err := p.Call(ctx, "Input.dispatchKeyEvent", downParams, nil); err != nil {
		return browserError(err)
	}
	_ = sleepCtx(ctx, time.Duration(randRange(14, 32))*time.Millisecond)
	if err := p.Call(ctx, "Input.dispatchKeyEvent", map[string]any{
		"type":                  "keyUp",
		"key":                   spec.Key,
		"code":                  spec.Code,
		"modifiers":             modMask,
		"windowsVirtualKeyCode": spec.VK,
		"nativeVirtualKeyCode":  spec.VK,
	}, nil); err != nil {
		return browserError(err)
	}

	for i := len(mods) - 1; i >= 0; i-- {
		m := mods[i]
		curMask &^= m.bit
		_ = sleepCtx(ctx, time.Duration(randRange(4, 10))*time.Millisecond)
		if err := p.Call(ctx, "Input.dispatchKeyEvent", map[string]any{
			"type":                  "keyUp",
			"key":                   m.key,
			"code":                  m.code,
			"location":              1,
			"modifiers":             curMask,
			"windowsVirtualKeyCode": m.vk,
			"nativeVirtualKeyCode":  m.vk,
		}, nil); err != nil {
			return browserError(err)
		}
	}
	return nil
}

// SelectOption chooses an option inside a <select> element by value or visible
// label/text, after moving the humanized cursor onto the control and clicking.
func (p *Page) SelectOption(ctx context.Context, objectID, valueOrLabel string) *types.APIError {
	if apiErr := p.Click(ctx, objectID, "left", 1); apiErr != nil {
		return apiErr
	}
	expr := `(function(el, target){
		if (!el || !el.isConnected) return {ok:false, why:"detached"};
		if (el.tagName !== "SELECT") return {ok:false, why:"not-a-select"};
		const opts = Array.from(el.options || []);
		let idx = opts.findIndex(o => o.value === target);
		if (idx < 0) {
			idx = opts.findIndex(o => (o.label || o.text || "").trim() === target.trim());
		}
		if (idx < 0) {
			const low = target.trim().toLowerCase();
			idx = opts.findIndex(o => o.value.toLowerCase() === low || (o.label || o.text || "").trim().toLowerCase() === low);
		}
		if (idx < 0) return {ok:false, why:"option-not-found"};
		if (opts[idx].disabled) return {ok:false, why:"option-disabled"};
		el.selectedIndex = idx;
		opts[idx].selected = true;
		el.dispatchEvent(new Event("input", {bubbles:true}));
		el.dispatchEvent(new Event("change", {bubbles:true}));
		return {ok:true, value: el.value};
	})`
	var out struct {
		OK    bool   `json:"ok"`
		Why   string `json:"why"`
		Value string `json:"value"`
	}
	if apiErr := p.callOnObjectArgs(ctx, expr, objectID, []map[string]any{
		{"objectId": objectID},
		{"value": valueOrLabel},
	}, &out); apiErr != nil {
		return apiErr
	}
	if !out.OK {
		return &types.APIError{
			Code:    types.CodeElementNotActionable,
			Message: "could not select option: " + out.Why,
			Detail:  map[string]any{"value": valueOrLabel, "reason": out.Why},
		}
	}
	return nil
}

// SetChecked idempotently checks or unchecks a checkbox, radio, or switch.
func (p *Page) SetChecked(ctx context.Context, objectID string, want bool) *types.APIError {
	expr := `(function(el){
		if (!el || !el.isConnected) return {ok:false, why:"detached"};
		if (typeof el.checked === "boolean") {
			return {ok:true, checked: el.checked};
		}
		const aria = el.getAttribute && el.getAttribute("aria-checked");
		if (aria === "true" || aria === "false") {
			return {ok:true, checked: aria === "true"};
		}
		return {ok:false, why:"not-checkable"};
	})`
	var state struct {
		OK      bool   `json:"ok"`
		Why     string `json:"why"`
		Checked bool   `json:"checked"`
	}
	if apiErr := p.callOnObject(ctx, expr, objectID, &state); apiErr != nil {
		return apiErr
	}
	if !state.OK {
		return &types.APIError{
			Code:    types.CodeElementNotActionable,
			Message: "element is not checkable: " + state.Why,
		}
	}
	if state.Checked == want {
		// Already in the desired state; move cursor over it so the interaction
		// is still observable without toggling it off.
		return p.Hover(ctx, objectID)
	}
	return p.Click(ctx, objectID, "left", 1)
}

// Drag performs a humanized drag-and-drop from srcObjectID to dstObjectID.
func (p *Page) Drag(ctx context.Context, srcObjectID, dstObjectID string) *types.APIError {
	srcX, srcY, apiErr := p.humanPickPoint(ctx, srcObjectID)
	if apiErr != nil {
		return apiErr
	}
	dstX, dstY, apiErr := p.humanPickPoint(ctx, dstObjectID)
	if apiErr != nil {
		return apiErr
	}
	if apiErr := p.humanMoveTo(ctx, srcX, srcY); apiErr != nil {
		return apiErr
	}
	if err := p.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mousePressed", "x": srcX, "y": srcY,
		"button": "left", "buttons": 1, "clickCount": 1, "pointerType": "mouse",
	}, nil); err != nil {
		return browserError(err)
	}
	_ = sleepCtx(ctx, time.Duration(randRange(35, 65))*time.Millisecond)

	// Move with left button held (buttons: 1) along a minimum-jerk curve.
	steps := 8
	dx := dstX - srcX
	dy := dstY - srcY
	for i := 1; i <= steps; i++ {
		u := float64(i) / float64(steps)
		u2 := u * u
		u3 := u2 * u
		s := 10*u3 - 15*u2*u2 + 6*u3*u2
		wx := math.Round((srcX+dx*s)*100) / 100
		wy := math.Round((srcY+dy*s)*100) / 100
		if err := p.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseMoved", "x": wx, "y": wy,
			"button": "left", "buttons": 1, "pointerType": "mouse",
		}, nil); err != nil {
			return browserError(err)
		}
		p.setCursor(wx, wy)
		_ = sleepCtx(ctx, time.Duration(randRange(10, 20))*time.Millisecond)
	}

	_ = sleepCtx(ctx, time.Duration(randRange(25, 50))*time.Millisecond)
	if err := p.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseReleased", "x": dstX, "y": dstY,
		"button": "left", "buttons": 0, "clickCount": 1, "pointerType": "mouse",
	}, nil); err != nil {
		return browserError(err)
	}
	return nil
}

// ElementCenter returns the viewport center coordinates of a resolved DOM node.
func (p *Page) ElementCenter(ctx context.Context, objectID string) (float64, float64, *types.APIError) {
	return p.humanPickPoint(ctx, objectID)
}

// NavigateHistory navigates back, forward, or reloads the current page.
func (p *Page) NavigateHistory(ctx context.Context, action string, timeout time.Duration) (*types.NavigateResult, *types.APIError) {
	if timeout <= 0 {
		timeout = defaultNavigateTimeout
	}
	startGen := p.Generation()
	switch strings.ToLower(action) {
	case "reload":
		if err := p.Call(ctx, "Page.reload", map[string]any{"ignoreCache": false}, nil); err != nil {
			return nil, browserError(err)
		}
	case "back", "forward":
		var hist struct {
			CurrentIndex int `json:"currentIndex"`
			Entries      []struct {
				ID  int    `json:"id"`
				URL string `json:"url"`
			} `json:"entries"`
		}
		if err := p.Call(ctx, "Page.getNavigationHistory", nil, &hist); err != nil {
			return nil, browserError(err)
		}
		targetIdx := hist.CurrentIndex - 1
		if strings.ToLower(action) == "forward" {
			targetIdx = hist.CurrentIndex + 1
		}
		if targetIdx < 0 || targetIdx >= len(hist.Entries) {
			return nil, &types.APIError{
				Code:    types.CodeInvalidRequest,
				Message: fmt.Sprintf("cannot navigate %s: no history entry in that direction", action),
				Detail:  map[string]any{"action": action, "currentIndex": hist.CurrentIndex, "entries": len(hist.Entries)},
			}
		}
		entry := hist.Entries[targetIdx]
		if err := p.Call(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": entry.ID}, nil); err != nil {
			return nil, browserError(err)
		}
	default:
		return nil, &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "action must be back, forward, or reload",
			Detail:  map[string]any{"field": "action"},
		}
	}

	p.waitGeneration(ctx, startGen, timeout)
	p.waitLoad(ctx, 5*time.Second)
	return &types.NavigateResult{
		URL:        p.CurrentURL(),
		Generation: p.Generation(),
		Title:      p.Title(),
	}, nil
}

// WaitFor waits for visible text to appear, textGone to disappear, or a fixed
// timeMs delay.
func (p *Page) WaitFor(ctx context.Context, req types.WaitRequest) (*types.WaitResult, *types.APIError) {
	start := time.Now()
	if req.TimeMS > 0 {
		if req.TimeMS > 30000 {
			req.TimeMS = 30000
		}
		if err := sleepCtx(ctx, time.Duration(req.TimeMS)*time.Millisecond); err != nil {
			return nil, &types.APIError{Code: types.CodeTimeout, Message: "wait cancelled before timeMs elapsed"}
		}
	}

	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}

	if req.Text == "" && req.TextGone == "" {
		return &types.WaitResult{
			Status:     types.StatusOK,
			Matched:    true,
			WaitedMS:   time.Since(start).Milliseconds(),
			Generation: p.Generation(),
			URL:        p.CurrentURL(),
			Title:      p.Title(),
		}, nil
	}

	deadline := time.Now().Add(timeout)
	for {
		var bodyText string
		_ = p.Evaluate(ctx, `(document.body && document.body.innerText) || ""`, &bodyText)
		wantMatch := req.Text == "" || strings.Contains(bodyText, req.Text)
		goneMatch := req.TextGone == "" || !strings.Contains(bodyText, req.TextGone)
		if wantMatch && goneMatch {
			return &types.WaitResult{
				Status:     types.StatusOK,
				Matched:    true,
				WaitedMS:   time.Since(start).Milliseconds(),
				Generation: p.Generation(),
				URL:        p.CurrentURL(),
				Title:      p.Title(),
			}, nil
		}
		if time.Now().After(deadline) {
			return nil, &types.APIError{
				Code:    types.CodeTimeout,
				Message: "wait condition was not satisfied before timeout",
				Detail:  map[string]any{"text": req.Text, "textGone": req.TextGone, "waitedMs": time.Since(start).Milliseconds()},
			}
		}
		if err := sleepCtx(ctx, 80*time.Millisecond); err != nil {
			return nil, &types.APIError{Code: types.CodeTimeout, Message: "wait cancelled"}
		}
	}
}

// Extract pulls structured visible text, headings, links, and scroll geometry
// from the current document.
func (p *Page) Extract(ctx context.Context, req types.ExtractRequest) (*types.ExtractResult, *types.APIError) {
	maxChars := req.MaxChars
	if maxChars <= 0 {
		maxChars = 12000
	}
	if maxChars > 100000 {
		maxChars = 100000
	}
	maxLinks := req.MaxLinks
	if maxLinks <= 0 {
		maxLinks = 50
	}
	if maxLinks > 300 {
		maxLinks = 300
	}

	expr := fmt.Sprintf(`(() => {
		const root = document.querySelector("main, article, [role=main]") || document.body;
		const rawText = (root && root.innerText ? root.innerText : (document.body ? document.body.innerText : "")).replace(/\n{3,}/g, "\n\n").trim();
		const maxC = %d;
		const truncated = rawText.length > maxC;
		const text = truncated ? rawText.slice(0, maxC) : rawText;
		const headings = Array.from(document.querySelectorAll("h1, h2, h3")).slice(0, 30).map(h => (h.innerText || "").trim()).filter(Boolean);
		const links = [];
		const seen = new Set();
		for (const a of document.querySelectorAll("a[href]")) {
			if (links.length >= %d) break;
			const href = a.href || "";
			const label = (a.innerText || a.getAttribute("aria-label") || "").replace(/\s+/g, " ").trim();
			if (!href || href.startsWith("javascript:") || seen.has(href)) continue;
			seen.add(href);
			links.push({text: label.slice(0, 120), href});
		}
		const de = document.documentElement || document.body || {};
		return {
			title: document.title || "",
			text,
			truncated,
			headings,
			links,
			scrollX: Math.round(window.scrollX || 0),
			scrollY: Math.round(window.scrollY || 0),
			scrollWidth: Math.round(de.scrollWidth || innerWidth || 0),
			scrollHeight: Math.round(de.scrollHeight || innerHeight || 0),
			viewportWidth: Math.round(window.innerWidth || 0),
			viewportHeight: Math.round(window.innerHeight || 0),
		};
	})()`, maxChars, maxLinks)

	var out types.ExtractResult
	if apiErr := p.Evaluate(ctx, expr, &out); apiErr != nil {
		return nil, apiErr
	}
	out.URL = p.CurrentURL()
	out.Generation = p.Generation()
	return &out, nil
}

// GetCookies returns the cookies visible to the current page.
func (p *Page) GetCookies(ctx context.Context) ([]types.Cookie, *types.APIError) {
	var res struct {
		Cookies []types.Cookie `json:"cookies"`
	}
	if err := p.Call(ctx, "Network.getCookies", struct{}{}, &res); err != nil {
		return nil, browserError(err)
	}
	return res.Cookies, nil
}

// SetCookies sets cookies in the page's browser context.
func (p *Page) SetCookies(ctx context.Context, cookies []types.Cookie) *types.APIError {
	if len(cookies) == 0 {
		return nil
	}
	if err := p.Call(ctx, "Network.setCookies", map[string]any{"cookies": cookies}, nil); err != nil {
		return browserError(err)
	}
	return nil
}

// ClearCookies clears cookies in the browser context.
func (p *Page) ClearCookies(ctx context.Context) *types.APIError {
	if err := p.Call(ctx, "Network.clearBrowserCookies", struct{}{}, nil); err != nil {
		return browserError(err)
	}
	return nil
}

// Telemetry returns a snapshot of the tab's console, network, and dialog ring buffers.
func (p *Page) Telemetry() types.TelemetryResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	con := append([]types.ConsoleEntry{}, p.consoleLog...)
	net := append([]types.NetworkEntry{}, p.networkLog...)
	dlg := append([]types.DialogEntry{}, p.dialogLog...)
	return types.TelemetryResult{Console: con, Network: net, Dialogs: dlg}
}

func (p *Page) recordAndHandleDialog(params json.RawMessage) {
	var ev struct {
		Type          string `json:"type"`
		Message       string `json:"message"`
		DefaultPrompt string `json:"defaultPrompt"`
	}
	_ = json.Unmarshal(params, &ev)

	p.mu.Lock()
	accept := true
	if p.dialogPolicySet {
		accept = p.dialogNextAccept
	}
	promptText := p.dialogNextPrompt
	entry := types.DialogEntry{
		Type:          ev.Type,
		Message:       ev.Message,
		DefaultPrompt: ev.DefaultPrompt,
		Accepted:      accept,
		PromptText:    promptText,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
	}
	p.dialogLog = append(p.dialogLog, entry)
	if len(p.dialogLog) > maxDialogEntries {
		p.dialogLog = p.dialogLog[len(p.dialogLog)-maxDialogEntries:]
	}
	p.mu.Unlock()

	go func(acc bool, ptxt string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		args := map[string]any{"accept": acc}
		if ptxt != "" {
			args["promptText"] = ptxt
		}
		_ = p.proc.conn.Call(ctx, p.session, "Page.handleJavaScriptDialog", args, nil)
	}(accept, promptText)
}

// Dialogs returns captured JavaScript dialogs and optionally updates the next
// dialog handling policy (accept/dismiss and promptText).
func (p *Page) Dialogs(req *types.DialogPolicyRequest) types.DialogsResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	if req != nil {
		if req.Accept != nil {
			p.dialogPolicySet = true
			p.dialogNextAccept = *req.Accept
		}
		if req.PromptText != "" || req.Accept != nil {
			p.dialogNextPrompt = req.PromptText
		}
		if req.ClearLog {
			p.dialogLog = nil
		}
	}
	nextAcc := true
	if p.dialogPolicySet {
		nextAcc = p.dialogNextAccept
	}
	list := append([]types.DialogEntry{}, p.dialogLog...)
	return types.DialogsResult{
		NextAccept:     nextAcc,
		NextPromptText: p.dialogNextPrompt,
		Dialogs:        list,
	}
}

// UploadFiles moves the humanized cursor onto an <input type="file"> element
// and attaches local files via CDP DOM.setFileInputFiles.
func (p *Page) UploadFiles(ctx context.Context, backendID int, objectID string, files []string) *types.APIError {
	if len(files) == 0 {
		return &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "at least one file path is required for upload",
			Detail:  map[string]any{"field": "files"},
		}
	}
	resolved := make([]string, 0, len(files))
	for _, f := range files {
		clean := strings.TrimSpace(f)
		if clean == "" {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "file path cannot be empty"}
		}
		abs, err := filepath.Abs(clean)
		if err != nil {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "invalid file path: " + err.Error()}
		}
		st, err := os.Stat(abs)
		if err != nil || st.IsDir() {
			return &types.APIError{
				Code:    types.CodeInvalidRequest,
				Message: fmt.Sprintf("upload file %q does not exist or is a directory", clean),
				Detail:  map[string]any{"file": clean},
			}
		}
		resolved = append(resolved, abs)
	}

	if tx, ty, apiErr := p.humanPickPoint(ctx, objectID); apiErr == nil {
		_ = p.humanMoveTo(ctx, tx, ty)
	}

	params := map[string]any{
		"files": resolved,
	}
	if backendID > 0 {
		params["backendNodeId"] = backendID
	} else {
		params["objectId"] = objectID
	}
	if err := p.Call(ctx, "DOM.setFileInputFiles", params, nil); err != nil {
		return browserError(err)
	}
	// Dispatch synthetic input/change notifications if the browser didn't already.
	_ = p.callOnObject(ctx, `(function(el){
		if (!el) return true;
		el.dispatchEvent(new Event("input", {bubbles:true}));
		el.dispatchEvent(new Event("change", {bubbles:true}));
		return true;
	})`, objectID, nil)
	return nil
}

// EvaluateUser runs a JavaScript expression in the page context (or invokes a
// function on a resolved @eN DOM element when objectID is non-empty) and
// returns its JSON-serializable result and JavaScript typeof.
func (p *Page) EvaluateUser(ctx context.Context, expr string, objectID string) (*types.EvaluateResult, *types.APIError) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "expression is required",
			Detail:  map[string]any{"field": "expression"},
		}
	}

	var raw struct {
		OK    bool   `json:"ok"`
		Type  string `json:"type"`
		Value any    `json:"value"`
		Error string `json:"error"`
	}

	if objectID != "" {
		fn := fmt.Sprintf(`(async function(el) {
			try {
				const userFn = (%s);
				const val = (typeof userFn === "function") ? await userFn(el) : userFn;
				return {ok: true, type: typeof val, value: val === undefined ? null : val};
			} catch (err) {
				return {ok: false, error: String(err && err.message ? err.message : err)};
			}
		})`, expr)
		if apiErr := p.callOnObject(ctx, fn, objectID, &raw); apiErr != nil {
			return nil, apiErr
		}
	} else {
		wrapped := fmt.Sprintf(`(async () => {
			try {
				const val = await (%s);
				return {ok: true, type: typeof val, value: val === undefined ? null : val};
			} catch (err) {
				return {ok: false, error: String(err && err.message ? err.message : err)};
			}
		})()`, expr)
		if apiErr := p.Evaluate(ctx, wrapped, &raw); apiErr != nil {
			return nil, apiErr
		}
	}

	if !raw.OK {
		return nil, &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "evaluation error: " + firstLine(raw.Error),
		}
	}
	return &types.EvaluateResult{
		Value:      raw.Value,
		Type:       raw.Type,
		Generation: p.Generation(),
		URL:        p.CurrentURL(),
	}, nil
}

// PDF renders the current page to a PDF document via CDP Page.printToPDF.
func (p *Page) PDF(ctx context.Context, req types.PDFRequest) ([]byte, *types.APIError) {
	scale := req.Scale
	if scale <= 0 {
		scale = 1.0
	}
	if scale < 0.1 || scale > 2.0 {
		return nil, &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "scale must be between 0.1 and 2.0",
			Detail:  map[string]any{"field": "scale"},
		}
	}
	var res struct {
		Data string `json:"data"`
	}
	err := p.Call(ctx, "Page.printToPDF", map[string]any{
		"landscape":       req.Landscape,
		"printBackground": req.PrintBackground,
		"scale":           scale,
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
		return nil, &types.APIError{Code: types.CodeInternal, Message: "pdf was not valid base64"}
	}
	if len(raw) > MaxPDFBytes {
		return nil, &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: fmt.Sprintf("pdf exceeds the %d byte budget", MaxPDFBytes),
			Detail:  map[string]any{"bytes": len(raw)},
		}
	}
	return raw, nil
}

// Storage inspects, seeds, or clears localStorage or sessionStorage on the
// current document's origin.
func (p *Page) Storage(ctx context.Context, req types.StorageRequest) (*types.StorageResult, *types.APIError) {
	area := strings.ToLower(strings.TrimSpace(req.Area))
	if area == "" {
		area = "local"
	}
	if area != "local" && area != "session" {
		return nil, &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "area must be local or session",
			Detail:  map[string]any{"field": "area"},
		}
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "get"
	}
	if action != "get" && action != "set" && action != "clear" {
		return nil, &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "action must be get, set, or clear",
			Detail:  map[string]any{"field": "action"},
		}
	}
	entriesJSON, err := json.Marshal(req.Entries)
	if err != nil {
		return nil, &types.APIError{Code: types.CodeInternal, Message: err.Error()}
	}
	expr := fmt.Sprintf(`(() => {
		const area = %q;
		const act = %q;
		const entries = %s || {};
		try {
			const store = area === "session" ? window.sessionStorage : window.localStorage;
			if (act === "clear") {
				store.clear();
			} else if (act === "set") {
				for (const [k, v] of Object.entries(entries)) {
					store.setItem(String(k), String(v));
				}
			}
			const out = {};
			for (let i = 0; i < store.length; i++) {
				const k = store.key(i);
				if (k !== null) out[k] = store.getItem(k);
			}
			return {ok: true, origin: window.location.origin || "", entries: out};
		} catch (e) {
			return {ok: false, error: String(e && e.message ? e.message : e)};
		}
	})()`, area, action, string(entriesJSON))

	var res struct {
		OK      bool              `json:"ok"`
		Origin  string            `json:"origin"`
		Entries map[string]string `json:"entries"`
		Error   string            `json:"error"`
	}
	if apiErr := p.Evaluate(ctx, expr, &res); apiErr != nil {
		return nil, apiErr
	}
	if !res.OK {
		return nil, &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "storage access failed: " + res.Error,
		}
	}
	if res.Entries == nil {
		res.Entries = map[string]string{}
	}
	return &types.StorageResult{
		Area:    area,
		Origin:  res.Origin,
		Entries: res.Entries,
	}, nil
}

func (p *Page) recordConsoleEvent(params json.RawMessage) {
	var ev struct {
		Type string `json:"type"`
		Args []struct {
			Value       any    `json:"value"`
			Description string `json:"description"`
		} `json:"args"`
	}
	if json.Unmarshal(params, &ev) != nil {
		return
	}
	parts := make([]string, 0, len(ev.Args))
	for _, a := range ev.Args {
		if a.Description != "" {
			parts = append(parts, a.Description)
		} else if a.Value != nil {
			parts = append(parts, fmt.Sprint(a.Value))
		}
	}
	p.appendConsole(types.ConsoleEntry{
		Level:     ev.Type,
		Text:      strings.Join(parts, " "),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

func (p *Page) recordLogEvent(params json.RawMessage) {
	var ev struct {
		Entry struct {
			Level      string `json:"level"`
			Text       string `json:"text"`
			URL        string `json:"url"`
			LineNumber int    `json:"lineNumber"`
		} `json:"entry"`
	}
	if json.Unmarshal(params, &ev) != nil {
		return
	}
	p.appendConsole(types.ConsoleEntry{
		Level:     ev.Entry.Level,
		Text:      ev.Entry.Text,
		URL:       ev.Entry.URL,
		Line:      ev.Entry.LineNumber,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

func (p *Page) appendConsole(entry types.ConsoleEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.consoleLog = append(p.consoleLog, entry)
	if len(p.consoleLog) > maxConsoleEntries {
		p.consoleLog = p.consoleLog[len(p.consoleLog)-maxConsoleEntries:]
	}
}

func (p *Page) recordNetworkRequest(params json.RawMessage) {
	var ev struct {
		RequestID string `json:"requestId"`
		Type      string `json:"type"`
		Request   struct {
			Method string `json:"method"`
			URL    string `json:"url"`
		} `json:"request"`
	}
	if json.Unmarshal(params, &ev) != nil || ev.Request.URL == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.networkLog = append(p.networkLog, types.NetworkEntry{
		RequestID:    ev.RequestID,
		Method:       ev.Request.Method,
		URL:          ev.Request.URL,
		ResourceType: ev.Type,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
	})
	if len(p.networkLog) > maxNetworkEntries {
		p.networkLog = p.networkLog[len(p.networkLog)-maxNetworkEntries:]
	}
}

func (p *Page) recordNetworkResponse(params json.RawMessage) {
	var ev struct {
		RequestID string `json:"requestId"`
		Response  struct {
			Status   int    `json:"status"`
			MimeType string `json:"mimeType"`
		} `json:"response"`
	}
	if json.Unmarshal(params, &ev) != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.networkLog) - 1; i >= 0; i-- {
		if p.networkLog[i].RequestID == ev.RequestID {
			p.networkLog[i].Status = ev.Response.Status
			p.networkLog[i].MimeType = ev.Response.MimeType
			break
		}
	}
}

func (p *Page) recordNetworkFailure(params json.RawMessage) {
	var ev struct {
		RequestID string `json:"requestId"`
		ErrorText string `json:"errorText"`
	}
	if json.Unmarshal(params, &ev) != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.networkLog) - 1; i >= 0; i-- {
		if p.networkLog[i].RequestID == ev.RequestID {
			p.networkLog[i].Failed = true
			p.networkLog[i].ErrorText = ev.ErrorText
			break
		}
	}
}

// RenderCompanionOverlay updates the in-page 2nd companion cursor ("Ni-Cursor")
// inside an isolated, pointer-events:none, aria-hidden closed Shadow DOM host so
// the visual assistant cursor + chat bubble + mood badge are visible in browser
// windows and screenshots without intercepting clicks or polluting AX snapshots.
func (p *Page) RenderCompanionOverlay(ctx context.Context, st types.CompanionState) *types.APIError {
	payload, err := json.Marshal(st)
	if err != nil {
		return &types.APIError{Code: types.CodeInternal, Message: err.Error()}
	}
	expr := fmt.Sprintf(`(() => {
		const st = %s;
		let host = document.getElementById("__xtn_companion_host__");
		if (!st.visible) {
			if (host) host.remove();
			return true;
		}
		if (!document.documentElement) return false;
		if (!host) {
			host = document.createElement("div");
			host.id = "__xtn_companion_host__";
			host.setAttribute("aria-hidden", "true");
			host.style.cssText = "position:fixed;inset:0;pointer-events:none;z-index:2147483647;overflow:hidden;";
			const shadow = host.attachShadow({mode: "open"});
			shadow.innerHTML = `+"`"+`
				<style>
					.wrap {
						position: fixed;
						left: 0; top: 0;
						pointer-events: none;
						transition: transform 260ms cubic-bezier(0.22, 1, 0.36, 1);
						will-change: transform;
						font-family: "Segoe UI", system-ui, -apple-system, sans-serif;
					}
					.pulse {
						position: absolute;
						left: -14px; top: -14px;
						width: 28px; height: 28px;
						border-radius: 50%%;
						border: 2px solid #ff2e2e;
						background: rgba(255, 46, 46, 0.14);
						box-shadow: 0 0 14px rgba(255, 46, 46, 0.55);
					}
					.cursor-svg {
						position: absolute;
						left: -2px; top: -2px;
						width: 26px; height: 26px;
						filter: drop-shadow(0 3px 8px rgba(0,0,0,0.65));
					}
					.card {
						position: absolute;
						left: 22px; top: 14px;
						min-width: 150px;
						max-width: 310px;
						background: rgba(10, 10, 12, 0.94);
						color: #e8e6e3;
						border: 1.5px solid #2e2e36;
						border-left: 3px solid #ff2e2e;
						border-radius: 10px;
						padding: 8px 11px;
						box-shadow: 0 10px 28px rgba(0, 0, 0, 0.65);
					}
					.head {
						display: flex;
						align-items: center;
						gap: 6px;
						font-size: 11px;
						font-weight: 700;
						letter-spacing: 0.04em;
						color: #ff4d4d;
						margin-bottom: 3px;
					}
					.face {
						background: #1c1c22;
						color: #e8e6e3;
						padding: 1px 6px;
						border-radius: 99px;
						font-family: Consolas, monospace;
						font-size: 11px;
					}
					.sub {
						color: #a1a1aa;
						font-weight: 600;
						font-size: 10.5px;
					}
					.msg {
						font-size: 12.5px;
						line-height: 1.38;
						color: #f4f4f5;
						word-break: break-word;
					}
				</style>
				<div class="wrap" id="w">
					<div class="pulse" id="p"></div>
					<svg class="cursor-svg" viewBox="0 0 28 28">
						<path d="M2 2 L24 11 L14 14.5 L11 24.5 Z" fill="#0a0a0a" stroke="#e8e6e3" stroke-width="1.8" stroke-linejoin="round"/>
						<path d="M2 2 L12 6.2 L8.5 8.5 L6.2 12 Z" fill="#ff2e2e"/>
					</svg>
					<div class="card" id="c">
						<div class="head">
							<span>Ni</span>
							<span class="face" id="f">(^‿^)</span>
							<span class="sub" id="s"></span>
						</div>
						<div class="msg" id="m"></div>
					</div>
				</div>
			`+"`"+`;
			document.documentElement.appendChild(host);
		}
		const sh = host.shadowRoot;
		if (!sh) return false;
		const w = sh.getElementById("w");
		const p = sh.getElementById("p");
		const c = sh.getElementById("c");
		const f = sh.getElementById("f");
		const s = sh.getElementById("s");
		const m = sh.getElementById("m");

		const faces = {
			friendly: "(^‿^)",
			curious: "(•_•?)",
			warn: "(•̀_•́)!",
			sad: "(;︵;)",
			excited: "(✦‿✦)",
			thinking: "(◔_◔)"
		};
		f.textContent = faces[st.mood] || faces.friendly;
		s.textContent = st.subtext || st.targetRef || "assistant";
		m.textContent = st.message || "";
		c.style.display = (st.message || st.subtext) ? "block" : "none";
		p.style.display = st.pulse ? "block" : "none";

		const x = Math.max(8, Math.min((window.innerWidth || 1920) - 24, st.x || 240));
		const y = Math.max(8, Math.min((window.innerHeight || 919) - 24, st.y || 180));
		// Flip bubble left/up if near the bottom-right viewport edge.
		c.style.left = (x > (window.innerWidth || 1920) - 340) ? "-316px" : "22px";
		c.style.top = (y > (window.innerHeight || 919) - 120) ? "-74px" : "14px";
		w.style.transform = "translate3d(" + Math.round(x) + "px," + Math.round(y) + "px,0)";
		return true;
	})()`, string(payload))
	var ok bool
	return p.Evaluate(ctx, expr, &ok)
}
