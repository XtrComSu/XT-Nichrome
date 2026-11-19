package browser

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"time"
	"unicode"

	"github.com/xtrcom/xt-nichrome/internal/types"
)

// randFloat64 returns a cryptographically seeded float64 in [0, 1).
func randFloat64() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0.5
	}
	return float64(binary.LittleEndian.Uint64(b[:])>>11) / (1 << 53)
}

// randRange returns a float64 in [lo, hi).
func randRange(lo, hi float64) float64 {
	if hi <= lo {
		return lo
	}
	return lo + randFloat64()*(hi-lo)
}

// randTriangular returns a value in [-halfSpan, +halfSpan] with a triangular
// distribution peaked at 0, so target points cluster naturally near the center
// of an element without ever landing on the exact 0.500000 midpoint.
func randTriangular(halfSpan float64) float64 {
	return ((randFloat64() + randFloat64()) - 1.0) * halfSpan
}

// sleepCtx pauses for d or returns early if ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ensureCursor initialises the page's virtual cursor at a plausible desktop
// entry coordinate if no pointer movement has occurred yet on this tab.
func (p *Page) ensureCursor() (float64, float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.cursorInit {
		vw := float64(p.proc.persona.ViewportWidth)
		vh := float64(p.proc.persona.ViewportHeight)
		if vw <= 0 {
			vw = 1920
		}
		if vh <= 0 {
			vh = 919
		}
		p.cursorX = math.Round(vw*0.34 + randRange(-80, 80))
		p.cursorY = math.Round(vh*0.24 + randRange(-50, 50))
		p.cursorInit = true
	}
	return p.cursorX, p.cursorY
}

func (p *Page) setCursor(x, y float64) {
	p.mu.Lock()
	p.cursorX = x
	p.cursorY = y
	p.cursorInit = true
	p.mu.Unlock()
}

// humanPickPoint scrolls the element into view if needed and picks a natural,
// non-midpoint coordinate inside the element that hits the target node.
func (p *Page) humanPickPoint(ctx context.Context, objectID string) (float64, float64, *types.APIError) {
	expr := `(function(el, dxFrac, dyFrac){
		if (!el || !el.isConnected) return {ok:false, why:"detached"};
		let r = el.getBoundingClientRect();
		if (r.top < 4 || r.left < 4 || r.bottom > innerHeight - 4 || r.right > innerWidth - 4) {
			el.scrollIntoView({block:"center", inline:"center", behavior:"instant"});
			r = el.getBoundingClientRect();
		}
		const cx = r.left + r.width / 2;
		const cy = r.top + r.height / 2;
		const safeW = Math.max(0, r.width - 6);
		const safeH = Math.max(0, r.height - 6);
		let tx = cx + safeW * dxFrac;
		let ty = cy + safeH * dyFrac;
		// Verify the jittered point still lands on the target element tree.
		const hit = document.elementFromPoint(tx, ty);
		if (!hit || (hit !== el && !el.contains(hit) && !hit.contains(el))) {
			tx = cx;
			ty = cy;
		}
		return {
			ok: true,
			x: Math.round(tx * 100) / 100,
			y: Math.round(ty * 100) / 100,
			w: r.width,
			h: r.height,
		};
	})`
	var target struct {
		OK  bool    `json:"ok"`
		Why string  `json:"why"`
		X   float64 `json:"x"`
		Y   float64 `json:"y"`
	}
	dx := randTriangular(0.28)
	dy := randTriangular(0.28)
	if apiErr := p.callOnObjectArgs(ctx, expr, objectID, []map[string]any{
		{"objectId": objectID},
		{"value": dx},
		{"value": dy},
	}, &target); apiErr != nil {
		return 0, 0, apiErr
	}
	if !target.OK {
		return 0, 0, &types.APIError{Code: types.CodeStaleRef, Message: "element is no longer attached to the document"}
	}
	return target.X, target.Y, nil
}

// humanMoveTo moves the virtual pointer from its current position to (targetX, targetY)
// along a cubic Bézier curve using a minimum-jerk velocity profile.
func (p *Page) humanMoveTo(ctx context.Context, targetX, targetY float64) *types.APIError {
	startX, startY := p.ensureCursor()
	dx := targetX - startX
	dy := targetY - startY
	dist := math.Hypot(dx, dy)

	steps := 7
	switch {
	case dist < 3:
		steps = 2
	case dist < 40:
		steps = 4
	case dist < 220:
		steps = 6
	case dist < 600:
		steps = 8
	default:
		steps = 10
	}

	// Perpendicular vector for a natural slight arc.
	var nx, ny float64
	if dist > 1e-3 {
		nx = -dy / dist
		ny = dx / dist
	}
	arc1 := dist * randRange(-0.14, 0.14)
	arc2 := dist * randRange(-0.08, 0.08)
	c1x := startX + dx*randRange(0.22, 0.35) + nx*arc1
	c1y := startY + dy*randRange(0.22, 0.35) + ny*arc1
	c2x := startX + dx*randRange(0.65, 0.80) + nx*arc2
	c2y := startY + dy*randRange(0.65, 0.80) + ny*arc2

	for i := 1; i <= steps; i++ {
		u := float64(i) / float64(steps)
		// Flash & Hogan minimum-jerk trajectory: s(u) = 10u^3 - 15u^4 + 6u^5.
		u2 := u * u
		u3 := u2 * u
		s := 10*u3 - 15*u2*u2 + 6*u3*u2
		inv := 1 - s

		wx := inv*inv*inv*startX + 3*inv*inv*s*c1x + 3*inv*s*s*c2x + s*s*s*targetX
		wy := inv*inv*inv*startY + 3*inv*inv*s*c1y + 3*inv*s*s*c2y + s*s*s*targetY
		if i < steps && dist > 8 {
			wx += randTriangular(0.55)
			wy += randTriangular(0.55)
		} else if i == steps {
			wx = targetX
			wy = targetY
		}
		wx = math.Round(wx*100) / 100
		wy = math.Round(wy*100) / 100

		if err := p.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type":        "mouseMoved",
			"x":           wx,
			"y":           wy,
			"button":      "none",
			"buttons":     0,
			"pointerType": "mouse",
		}, nil); err != nil {
			return browserError(err)
		}
		p.setCursor(wx, wy)
		if i < steps {
			if err := sleepCtx(ctx, time.Duration(randRange(2200, 4800))*time.Microsecond); err != nil {
				return browserError(err)
			}
		}
	}
	return nil
}

// keySpec describes the Windows US-QWERTY virtual key parameters for one ASCII rune.
type keySpec struct {
	Key        string
	Code       string
	Text       string
	Unmodified string
	VK         int
	Shift      bool
}

var shiftedSymbolMap = map[rune]struct {
	code       string
	unmodified string
	vk         int
}{
	'!': {"Digit1", "1", 49},
	'@': {"Digit2", "2", 50},
	'#': {"Digit3", "3", 51},
	'$': {"Digit4", "4", 52},
	'%': {"Digit5", "5", 53},
	'^': {"Digit6", "6", 54},
	'&': {"Digit7", "7", 55},
	'*': {"Digit8", "8", 56},
	'(': {"Digit9", "9", 57},
	')': {"Digit0", "0", 48},
	'_': {"Minus", "-", 189},
	'+': {"Equal", "=", 187},
	'{': {"BracketLeft", "[", 219},
	'}': {"BracketRight", "]", 221},
	'|': {"Backslash", `\`, 220},
	':': {"Semicolon", ";", 186},
	'"': {"Quote", "'", 222},
	'<': {"Comma", ",", 188},
	'>': {"Period", ".", 190},
	'?': {"Slash", "/", 191},
	'~': {"Backquote", "`", 192},
}

var unshiftedSymbolMap = map[rune]struct {
	code string
	vk   int
}{
	'-':  {"Minus", 189},
	'=':  {"Equal", 187},
	'[':  {"BracketLeft", 219},
	']':  {"BracketRight", 221},
	'\\': {"Backslash", 220},
	';':  {"Semicolon", 186},
	'\'': {"Quote", 222},
	',':  {"Comma", 188},
	'.':  {"Period", 190},
	'/':  {"Slash", 191},
	'`':  {"Backquote", 192},
}

func lookupKeySpec(r rune) (keySpec, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		ch := string(r)
		upper := rune(r - 'a' + 'A')
		return keySpec{
			Key:        ch,
			Code:       "Key" + string(upper),
			Text:       ch,
			Unmodified: ch,
			VK:         int(upper),
			Shift:      false,
		}, true
	case r >= 'A' && r <= 'Z':
		ch := string(r)
		lower := string(r - 'A' + 'a')
		return keySpec{
			Key:        ch,
			Code:       "Key" + ch,
			Text:       ch,
			Unmodified: lower,
			VK:         int(r),
			Shift:      true,
		}, true
	case r >= '0' && r <= '9':
		ch := string(r)
		return keySpec{
			Key:        ch,
			Code:       "Digit" + ch,
			Text:       ch,
			Unmodified: ch,
			VK:         int(r),
			Shift:      false,
		}, true
	case r == ' ':
		return keySpec{
			Key:        " ",
			Code:       "Space",
			Text:       " ",
			Unmodified: " ",
			VK:         32,
			Shift:      false,
		}, true
	case r == '\n' || r == '\r':
		return keySpec{
			Key:        "Enter",
			Code:       "Enter",
			Text:       "\r",
			Unmodified: "\r",
			VK:         13,
			Shift:      false,
		}, true
	case r == '\t':
		return keySpec{
			Key:        "Tab",
			Code:       "Tab",
			Text:       "\t",
			Unmodified: "\t",
			VK:         9,
			Shift:      false,
		}, true
	}
	if sym, ok := unshiftedSymbolMap[r]; ok {
		ch := string(r)
		return keySpec{
			Key:        ch,
			Code:       sym.code,
			Text:       ch,
			Unmodified: ch,
			VK:         sym.vk,
			Shift:      false,
		}, true
	}
	if sym, ok := shiftedSymbolMap[r]; ok {
		ch := string(r)
		return keySpec{
			Key:        ch,
			Code:       sym.code,
			Text:       ch,
			Unmodified: sym.unmodified,
			VK:         sym.vk,
			Shift:      true,
		}, true
	}
	return keySpec{}, false
}

// humanClearField selects existing content in the focused field and deletes it
// with a real Backspace key event so input/keydown/keyup listeners observe a
// human clear sequence.
func (p *Page) humanClearField(ctx context.Context, objectID string) *types.APIError {
	selExpr := `(function(el){
		if (!el || !el.isConnected) return {ok:false, hasContent:false};
		const tag = el.tagName;
		if (tag === "INPUT" || tag === "TEXTAREA") {
			const val = el.value || "";
			if (val.length === 0) return {ok:true, hasContent:false};
			try { el.setSelectionRange(0, val.length); } catch (e) { el.select && el.select(); }
			return {ok:true, hasContent:true};
		}
		if (el.isContentEditable) {
			const val = el.textContent || "";
			if (val.length === 0) return {ok:true, hasContent:false};
			const r = document.createRange();
			r.selectNodeContents(el);
			const s = getSelection();
			s.removeAllRanges();
			s.addRange(r);
			return {ok:true, hasContent:true};
		}
		return {ok:false, hasContent:false};
	})`
	var st struct {
		OK         bool `json:"ok"`
		HasContent bool `json:"hasContent"`
	}
	if apiErr := p.callOnObject(ctx, selExpr, objectID, &st); apiErr != nil {
		return apiErr
	}
	if !st.HasContent {
		return nil
	}
	if err := p.Call(ctx, "Input.dispatchKeyEvent", map[string]any{
		"type":                  "keyDown",
		"key":                   "Backspace",
		"code":                  "Backspace",
		"windowsVirtualKeyCode": 8,
		"nativeVirtualKeyCode":  8,
	}, nil); err != nil {
		return browserError(err)
	}
	_ = sleepCtx(ctx, 6*time.Millisecond)
	if err := p.Call(ctx, "Input.dispatchKeyEvent", map[string]any{
		"type":                  "keyUp",
		"key":                   "Backspace",
		"code":                  "Backspace",
		"windowsVirtualKeyCode": 8,
		"nativeVirtualKeyCode":  8,
	}, nil); err != nil {
		return browserError(err)
	}
	// If an input type (such as email/number where setSelectionRange throws)
	// still retained characters after Backspace, clear the remainder cleanly.
	cleanExpr := `(function(el){
		if (el.tagName === "INPUT" || el.tagName === "TEXTAREA") {
			if (el.value !== "") {
				el.value = "";
				el.dispatchEvent(new InputEvent("input", {bubbles:true, inputType:"deleteContentBackward"}));
			}
		} else if (el.isContentEditable && el.textContent !== "") {
			el.textContent = "";
			el.dispatchEvent(new InputEvent("input", {bubbles:true, inputType:"deleteContentBackward"}));
		}
		return {ok:true};
	})`
	var ignored struct {
		OK bool `json:"ok"`
	}
	return p.callOnObject(ctx, cleanExpr, objectID, &ignored)
}

// humanTypeText dispatches per-character keyDown/keyUp events with realistic
// human cadence, Shift-key transitions, and Windows virtual key codes.
func (p *Page) humanTypeText(ctx context.Context, text string) *types.APIError {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	// Scale cadence for very long strings so large fills complete well inside
	// the operation timeout while still emitting real key events.
	holdLo, holdHi := 2500.0, 6500.0 // microseconds
	gapLo, gapHi := 4500.0, 12500.0  // microseconds
	if len(runes) > 40 {
		scale := 40.0 / float64(len(runes))
		if scale < 0.15 {
			scale = 0.15
		}
		holdLo, holdHi = holdLo*scale, holdHi*scale
		gapLo, gapHi = gapLo*scale, gapHi*scale
	}

	shiftHeld := false
	setShift := func(want bool) *types.APIError {
		if shiftHeld == want {
			return nil
		}
		evType := "keyDown"
		mod := 8
		if !want {
			evType = "keyUp"
			mod = 0
		}
		if err := p.Call(ctx, "Input.dispatchKeyEvent", map[string]any{
			"type":                  evType,
			"key":                   "Shift",
			"code":                  "ShiftLeft",
			"location":              1,
			"modifiers":             mod,
			"windowsVirtualKeyCode": 16,
			"nativeVirtualKeyCode":  16,
		}, nil); err != nil {
			return browserError(err)
		}
		shiftHeld = want
		return nil
	}
	defer func() {
		if shiftHeld {
			_ = setShift(false)
		}
	}()

	var prev rune
	for i, r := range runes {
		spec, ok := lookupKeySpec(r)
		if !ok {
			if apiErr := setShift(false); apiErr != nil {
				return apiErr
			}
			if err := p.Call(ctx, "Input.insertText", map[string]any{"text": string(r)}, nil); err != nil {
				return browserError(err)
			}
			prev = r
			continue
		}
		if apiErr := setShift(spec.Shift); apiErr != nil {
			return apiErr
		}
		mod := 0
		if spec.Shift {
			mod = 8
		}
		if err := p.Call(ctx, "Input.dispatchKeyEvent", map[string]any{
			"type":                  "keyDown",
			"key":                   spec.Key,
			"code":                  spec.Code,
			"text":                  spec.Text,
			"unmodifiedText":        spec.Unmodified,
			"modifiers":             mod,
			"windowsVirtualKeyCode": spec.VK,
			"nativeVirtualKeyCode":  spec.VK,
		}, nil); err != nil {
			return browserError(err)
		}
		if err := sleepCtx(ctx, time.Duration(randRange(holdLo, holdHi))*time.Microsecond); err != nil {
			return browserError(err)
		}
		if err := p.Call(ctx, "Input.dispatchKeyEvent", map[string]any{
			"type":                  "keyUp",
			"key":                   spec.Key,
			"code":                  spec.Code,
			"modifiers":             mod,
			"windowsVirtualKeyCode": spec.VK,
			"nativeVirtualKeyCode":  spec.VK,
		}, nil); err != nil {
			return browserError(err)
		}
		if i+1 < len(runes) {
			gap := randRange(gapLo, gapHi)
			// Digraphs across word boundaries or punctuation have a slightly
			// longer pause in human typing.
			if unicode.IsSpace(r) || unicode.IsPunct(r) || r == prev {
				gap *= randRange(1.15, 1.45)
			}
			if err := sleepCtx(ctx, time.Duration(gap)*time.Microsecond); err != nil {
				return browserError(err)
			}
		}
		prev = r
	}
	return setShift(false)
}

var _ = fmt.Sprintf
