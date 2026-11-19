package sessions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/browser"
	"github.com/xtrcom/xt-nichrome/internal/snapshots"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

// Act performs a ref-based or viewport-level action on a tab.
//
// Three outcomes are distinguishable:
//   - the action never reached the page  -> safe to report failure, retry is safe
//   - the dispatch completed             -> report completed
//   - the dispatch was sent but its result was lost (timeout, client gone,
//     renderer crash) -> report ACTION_OUTCOME_UNKNOWN and refuse to retry
func (m *Manager) Act(ctx context.Context, sessionID, tabID string, req types.ActionRequest) (*types.ActionResult, *types.APIError) {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	if apiErr := validateAction(req); apiErr != nil {
		return nil, apiErr
	}
	leave, apiErr := t.enter(ctx, m.cfg.Limits)
	if apiErr != nil {
		return nil, apiErr
	}
	defer leave()

	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		ref         *snapshots.Ref
		objectID    string
		dstObjectID string
	)

	// Scroll and Press allow omitting Ref to operate at the viewport/document level.
	if req.Ref != "" {
		ref, apiErr = snapshots.Resolve(t.snapshots, tabID, t.page, req.SnapshotID, req.Ref, req.Generation)
		if apiErr != nil {
			return nil, apiErr
		}
		if apiErr := checkActionable(req.Kind, ref); apiErr != nil {
			return nil, apiErr
		}
		objectID, apiErr = m.resolveAndVerifyNode(actx, t, ref, req.Ref, timeout)
		if apiErr != nil {
			return nil, apiErr
		}
		defer t.page.ReleaseObject(context.Background(), objectID)
	}

	if req.Kind == types.ActionDrag {
		dstRef, apiErr := snapshots.Resolve(t.snapshots, tabID, t.page, req.SnapshotID, req.TargetRef, req.Generation)
		if apiErr != nil {
			return nil, apiErr
		}
		dstObjectID, apiErr = m.resolveAndVerifyNode(actx, t, dstRef, req.TargetRef, timeout)
		if apiErr != nil {
			return nil, apiErr
		}
		defer t.page.ReleaseObject(context.Background(), dstObjectID)
	}

	genBefore := t.page.Generation()

	t.page.MarkInflight(true)
	dispatched := false
	var actErr *types.APIError
	switch req.Kind {
	case types.ActionClick:
		dispatched = true
		actErr = t.page.Click(actx, objectID, req.Button, req.ClickCount)
	case types.ActionFill:
		clear := true
		if req.ClearExisting != nil {
			clear = *req.ClearExisting
		}
		dispatched = true
		actErr = t.page.Fill(actx, objectID, req.Text, clear)
	case types.ActionHover:
		dispatched = true
		actErr = t.page.Hover(actx, objectID)
	case types.ActionScroll:
		dispatched = true
		actErr = t.page.Scroll(actx, objectID, req.Direction, req.Amount)
	case types.ActionPress:
		dispatched = true
		actErr = t.page.PressKey(actx, objectID, req.Key)
	case types.ActionSelect:
		val := req.Value
		if val == "" {
			val = req.Text
		}
		dispatched = true
		actErr = t.page.SelectOption(actx, objectID, val)
	case types.ActionCheck:
		dispatched = true
		actErr = t.page.SetChecked(actx, objectID, true)
	case types.ActionUncheck:
		dispatched = true
		actErr = t.page.SetChecked(actx, objectID, false)
	case types.ActionDrag:
		dispatched = true
		actErr = t.page.Drag(actx, objectID, dstObjectID)
	case types.ActionUpload:
		dispatched = true
		var backendID int
		if ref != nil {
			backendID = ref.BackendID
		}
		actErr = t.page.UploadFiles(actx, backendID, objectID, req.Files)
	}
	t.page.MarkInflight(false)
	t.page.TouchActivity()
	s.touch(m.cfg.Now())

	// Update the 2nd companion cursor if it is currently visible and following actions.
	if actErr == nil && m.companion != nil {
		var cx, cy float64
		if objectID != "" {
			cx, cy, _ = t.page.ElementCenter(actx, objectID)
		}
		label := ""
		if ref != nil && ref.Name != "" {
			label = fmt.Sprintf("%s %q", req.Kind, ref.Name)
		}
		if st, ok := m.companion.NotifyAction(cx, cy, req.Ref, req.Kind, label); ok {
			_ = t.page.RenderCompanionOverlay(actx, st)
		}
	}

	navigated, genAfter := t.settle(actx, genBefore, 2*time.Second)
	if navigated {
		t.snapshots.Invalidate(tabID)
	}

	if actErr != nil {
		if dispatched && isAmbiguous(actErr) {
			return &types.ActionResult{
				Status:                types.StatusUnknown,
				Outcome:               types.OutcomeUnknown,
				FreshSnapshotRequired: true,
				Generation:            genAfter,
				URL:                   t.page.CurrentURL(),
			}, actErr
		}
		return nil, actErr
	}

	return &types.ActionResult{
		Status:                types.StatusOK,
		Outcome:               types.OutcomeCompleted,
		FreshSnapshotRequired: navigated,
		Generation:            genAfter,
		URL:                   t.page.CurrentURL(),
	}, nil
}

func (m *Manager) resolveAndVerifyNode(ctx context.Context, t *tab, ref *snapshots.Ref, refLabel string, timeout time.Duration) (string, *types.APIError) {
	objectID, apiErr := t.page.ResolveBackendNode(ctx, ref.BackendID)
	if apiErr != nil {
		return "", apiErr
	}
	fp, apiErr := t.page.Fingerprint(ctx, objectID)
	if apiErr != nil {
		t.page.ReleaseObject(context.Background(), objectID)
		return "", apiErr
	}
	if !fp.Connected {
		t.page.ReleaseObject(context.Background(), objectID)
		return "", &types.APIError{Code: types.CodeStaleRef, Message: "the referenced element has been removed from the document"}
	}
	if ref.Signature != "" && fp.Signature != ref.Signature {
		t.page.ReleaseObject(context.Background(), objectID)
		return "", &types.APIError{
			Code:    types.CodeStaleRef,
			Message: "the referenced element was replaced by a different node; take a fresh snapshot",
			Detail:  map[string]any{"ref": refLabel},
		}
	}
	if apiErr := t.page.WaitForActionability(ctx, objectID, timeout); apiErr != nil {
		t.page.ReleaseObject(context.Background(), objectID)
		return "", apiErr
	}
	return objectID, nil
}

// settle waits briefly for a post-action navigation to commit.
func (t *tab) settle(ctx context.Context, from int64, window time.Duration) (bool, int64) {
	graceDeadline := time.Now().Add(120 * time.Millisecond)
	for time.Now().Before(graceDeadline) {
		if cur := t.page.Generation(); cur != from {
			return true, cur
		}
		if t.page.NavigationPending() {
			break
		}
		select {
		case <-ctx.Done():
			return false, t.page.Generation()
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !t.page.NavigationPending() {
		select {
		case <-ctx.Done():
			return false, t.page.Generation()
		case <-time.After(80 * time.Millisecond):
		}
		if cur := t.page.Generation(); cur != from {
			return true, cur
		}
		if !t.page.NavigationPending() {
			return false, t.page.Generation()
		}
	}

	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if cur := t.page.Generation(); cur != from {
			return true, cur
		}
		select {
		case <-ctx.Done():
			return false, t.page.Generation()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return false, t.page.Generation()
}

var supportedActions = []string{
	types.ActionClick,
	types.ActionFill,
	types.ActionHover,
	types.ActionScroll,
	types.ActionPress,
	types.ActionSelect,
	types.ActionCheck,
	types.ActionUncheck,
	types.ActionDrag,
	types.ActionUpload,
}

func validateAction(req types.ActionRequest) *types.APIError {
	if req.Kind == "" {
		return &types.APIError{Code: types.CodeInvalidRequest, Message: "kind is required", Detail: map[string]any{"field": "kind"}}
	}
	validKind := false
	for _, k := range supportedActions {
		if req.Kind == k {
			validKind = true
			break
		}
	}
	if !validKind {
		return &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "unsupported action kind",
			Detail:  map[string]any{"field": "kind", "supported": supportedActions},
		}
	}

	needsRef := req.Kind != types.ActionScroll && req.Kind != types.ActionPress
	if needsRef || req.Ref != "" {
		if req.SnapshotID == "" {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "snapshotId is required", Detail: map[string]any{"field": "snapshotId"}}
		}
		if req.Ref == "" {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "ref is required", Detail: map[string]any{"field": "ref"}}
		}
	}

	switch req.Kind {
	case types.ActionClick:
		if req.Text != "" || req.Value != "" || req.Key != "" || req.Direction != "" || req.TargetRef != "" {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "text, value, key, direction, and targetRef are not valid for click", Detail: map[string]any{"field": "text"}}
		}
		if req.Button != "" && req.Button != "left" && req.Button != "right" && req.Button != "middle" {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "button must be left, right or middle", Detail: map[string]any{"field": "button"}}
		}
		if req.ClickCount < 0 || req.ClickCount > 3 {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "clickCount must be between 1 and 3", Detail: map[string]any{"field": "clickCount"}}
		}
	case types.ActionFill:
		if req.Button != "" || req.ClickCount != 0 || req.Key != "" || req.Direction != "" || req.TargetRef != "" {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "button and clickCount are not valid for a fill", Detail: map[string]any{"field": "button"}}
		}
		if req.Text == "" {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "text is required for a fill", Detail: map[string]any{"field": "text"}}
		}
	case types.ActionHover, types.ActionCheck, types.ActionUncheck:
		if req.Text != "" || req.Value != "" || req.Button != "" || req.ClickCount != 0 {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "unexpected fields for " + req.Kind}
		}
	case types.ActionScroll:
		dir := strings.ToLower(req.Direction)
		if dir != "" && dir != "up" && dir != "down" && dir != "left" && dir != "right" {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "direction must be up, down, left, or right", Detail: map[string]any{"field": "direction"}}
		}
		if req.Amount < 0 || req.Amount > 10000 {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "amount must be between 1 and 10000", Detail: map[string]any{"field": "amount"}}
		}
	case types.ActionPress:
		if strings.TrimSpace(req.Key) == "" {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "key is required for press", Detail: map[string]any{"field": "key"}}
		}
	case types.ActionSelect:
		if req.Value == "" && req.Text == "" {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "value or text is required for select", Detail: map[string]any{"field": "value"}}
		}
	case types.ActionDrag:
		if req.TargetRef == "" {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "targetRef is required for drag", Detail: map[string]any{"field": "targetRef"}}
		}
	case types.ActionUpload:
		if len(req.Files) == 0 {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "files is required for upload", Detail: map[string]any{"field": "files"}}
		}
	}
	if req.TimeoutMS < 0 || req.TimeoutMS > 120000 {
		return &types.APIError{Code: types.CodeInvalidRequest, Message: "timeoutMs must be between 1 and 120000", Detail: map[string]any{"field": "timeoutMs"}}
	}
	return nil
}

// checkActionable rejects a kind that cannot apply to the referenced role.
func checkActionable(kind string, ref *snapshots.Ref) *types.APIError {
	if !ref.Actionable && kind != types.ActionHover && kind != types.ActionScroll && kind != types.ActionDrag {
		return &types.APIError{
			Code:    types.CodeElementNotActionable,
			Message: "the referenced element does not accept actions",
			Detail:  map[string]any{"ref": ref.ID, "role": ref.Role},
		}
	}
	switch kind {
	case types.ActionFill:
		switch ref.Role {
		case "textbox", "searchbox", "combobox", "spinbutton", "textboxmultiline":
			return nil
		}
		return &types.APIError{
			Code:    types.CodeElementNotActionable,
			Message: "the referenced element is not a text input",
			Detail:  map[string]any{"ref": ref.ID, "role": ref.Role},
		}
	case types.ActionSelect:
		switch ref.Role {
		case "combobox", "listbox":
			return nil
		}
		if strings.EqualFold(ref.Tag, "SELECT") {
			return nil
		}
		return &types.APIError{
			Code:    types.CodeElementNotActionable,
			Message: "the referenced element is not a select/combobox control",
			Detail:  map[string]any{"ref": ref.ID, "role": ref.Role},
		}
	case types.ActionCheck, types.ActionUncheck:
		switch ref.Role {
		case "checkbox", "radio", "switch", "menuitemcheckbox", "menuitemradio":
			return nil
		}
		return &types.APIError{
			Code:    types.CodeElementNotActionable,
			Message: "the referenced element is not a checkbox, radio, or switch",
			Detail:  map[string]any{"ref": ref.ID, "role": ref.Role},
		}
	}
	return nil
}

func isAmbiguous(err *types.APIError) bool {
	if err == nil {
		return false
	}
	switch err.Code {
	case types.CodeTimeout, types.CodeActionOutcomeUnknown, types.CodeBrowserFailed:
		return true
	}
	return err.Code == types.CodeInternal
}

// Screenshot returns a PNG for a tab.
func (m *Manager) Screenshot(ctx context.Context, sessionID, tabID string) ([]byte, *types.APIError) {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	leave, apiErr := t.enter(ctx, m.cfg.Limits)
	if apiErr != nil {
		return nil, apiErr
	}
	defer leave()
	png, apiErr := t.page.Screenshot(ctx)
	if apiErr != nil {
		return nil, apiErr
	}
	s.touch(m.cfg.Now())
	return png, nil
}

// History navigates back, forward, or reloads a tab.
func (m *Manager) History(ctx context.Context, sessionID, tabID string, req types.HistoryRequest) (*types.NavigateResult, *types.APIError) {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	leave, apiErr := t.enter(ctx, m.cfg.Limits)
	if apiErr != nil {
		return nil, apiErr
	}
	defer leave()

	t.snapshots.Invalidate(tabID)
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	res, apiErr := t.page.NavigateHistory(ctx, req.Action, timeout)
	if apiErr != nil {
		return nil, apiErr
	}
	s.touch(m.cfg.Now())
	return res, nil
}

// WaitFor waits for text to appear/disappear or a fixed duration on a tab.
func (m *Manager) WaitFor(ctx context.Context, sessionID, tabID string, req types.WaitRequest) (*types.WaitResult, *types.APIError) {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	leave, apiErr := t.enter(ctx, m.cfg.Limits)
	if apiErr != nil {
		return nil, apiErr
	}
	defer leave()

	res, apiErr := t.page.WaitFor(ctx, req)
	if apiErr != nil {
		return nil, apiErr
	}
	s.touch(m.cfg.Now())
	return res, nil
}

// Extract returns structured visible text, headings, links, and scroll state.
func (m *Manager) Extract(ctx context.Context, sessionID, tabID string, req types.ExtractRequest) (*types.ExtractResult, *types.APIError) {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	leave, apiErr := t.enter(ctx, m.cfg.Limits)
	if apiErr != nil {
		return nil, apiErr
	}
	defer leave()

	res, apiErr := t.page.Extract(ctx, req)
	if apiErr != nil {
		return nil, apiErr
	}
	s.touch(m.cfg.Now())
	return res, nil
}

// GetCookies returns the cookies visible to the tab.
func (m *Manager) GetCookies(ctx context.Context, sessionID, tabID string) (*types.CookiesResult, *types.APIError) {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	leave, apiErr := t.enter(ctx, m.cfg.Limits)
	if apiErr != nil {
		return nil, apiErr
	}
	defer leave()

	list, apiErr := t.page.GetCookies(ctx)
	if apiErr != nil {
		return nil, apiErr
	}
	s.touch(m.cfg.Now())
	return &types.CookiesResult{Cookies: list}, nil
}

// SetCookies sets cookies on a tab after verifying every target URL/domain is
// admitted by the egress policy.
func (m *Manager) SetCookies(ctx context.Context, sessionID, tabID string, req types.SetCookiesRequest) (*types.CookiesResult, *types.APIError) {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	if len(req.Cookies) == 0 {
		return nil, &types.APIError{Code: types.CodeInvalidRequest, Message: "cookies list cannot be empty", Detail: map[string]any{"field": "cookies"}}
	}
	curURL := t.page.CurrentURL()
	for i := range req.Cookies {
		c := &req.Cookies[i]
		if strings.TrimSpace(c.Name) == "" {
			return nil, &types.APIError{Code: types.CodeInvalidRequest, Message: "cookie name is required", Detail: map[string]any{"field": "name"}}
		}
		targetURL := c.URL
		if targetURL == "" && c.Domain != "" {
			scheme := "https://"
			if strings.HasPrefix(curURL, "http://") {
				scheme = "http://"
			}
			targetURL = scheme + strings.TrimPrefix(c.Domain, ".") + "/"
		}
		if targetURL == "" {
			targetURL = curURL
		}
		if targetURL == "" || targetURL == "about:blank" {
			return nil, &types.APIError{Code: types.CodeInvalidRequest, Message: "cookie requires url or domain when tab is on about:blank", Detail: map[string]any{"field": "url"}}
		}
		if _, apiErr := m.cfg.Policy.AdmitURL(targetURL); apiErr != nil {
			return nil, apiErr
		}
		if c.URL == "" {
			c.URL = targetURL
		}
	}

	leave, apiErr := t.enter(ctx, m.cfg.Limits)
	if apiErr != nil {
		return nil, apiErr
	}
	defer leave()

	if apiErr := t.page.SetCookies(ctx, req.Cookies); apiErr != nil {
		return nil, apiErr
	}
	list, apiErr := t.page.GetCookies(ctx)
	if apiErr != nil {
		return nil, apiErr
	}
	s.touch(m.cfg.Now())
	return &types.CookiesResult{Cookies: list}, nil
}

// ClearCookies clears all cookies in the tab's browser context.
func (m *Manager) ClearCookies(ctx context.Context, sessionID, tabID string) *types.APIError {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return apiErr
	}
	leave, apiErr := t.enter(ctx, m.cfg.Limits)
	if apiErr != nil {
		return apiErr
	}
	defer leave()
	if apiErr := t.page.ClearCookies(ctx); apiErr != nil {
		return apiErr
	}
	s.touch(m.cfg.Now())
	return nil
}

// Telemetry returns the captured console messages and network requests for a tab.
func (m *Manager) Telemetry(ctx context.Context, sessionID, tabID string) (*types.TelemetryResult, *types.APIError) {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	out := t.page.Telemetry()
	s.touch(m.cfg.Now())
	return &out, nil
}

// Companion updates or inspects the 2nd companion cursor ("Ni-Cursor") across
// both the Wayland/Hyprland layer-shell overlay and optional target browser tab.
func (m *Manager) Companion(ctx context.Context, req types.CompanionRequest) (*types.CompanionState, *types.APIError) {
	if m.companion == nil {
		return nil, &types.APIError{Code: types.CodeInternal, Message: "companion controller not initialised"}
	}
	var targetTab *tab
	if req.SessionID != "" && req.TabID != "" {
		_, t, apiErr := m.lookup(req.SessionID, req.TabID)
		if apiErr != nil {
			return nil, apiErr
		}
		targetTab = t
		if req.Ref != "" {
			snapID := req.SnapshotID
			if snapID == "" {
				if cur := t.snapshots.Current(req.TabID); cur != nil {
					snapID = cur.ID
				}
			}
			ref, apiErr := snapshots.Resolve(t.snapshots, req.TabID, t.page, snapID, req.Ref, 0)
			if apiErr != nil {
				return nil, apiErr
			}
			objectID, apiErr := t.page.ResolveBackendNode(ctx, ref.BackendID)
			if apiErr != nil {
				return nil, apiErr
			}
			cx, cy, apiErr := t.page.ElementCenter(ctx, objectID)
			t.page.ReleaseObject(context.Background(), objectID)
			if apiErr != nil {
				return nil, apiErr
			}
			req.X = &cx
			req.Y = &cy
			if req.Subtext == "" && ref.Name != "" {
				req.Subtext = fmt.Sprintf("@%s • %s", ref.ID, ref.Name)
			}
		}
	}

	st, apiErr := m.companion.Apply(req)
	if apiErr != nil {
		return nil, apiErr
	}
	if targetTab != nil {
		_ = targetTab.page.RenderCompanionOverlay(ctx, st)
	}
	return &st, nil
}

// CompanionController exposes the underlying companion controller.
func (m *Manager) CompanionController() any {
	return m.companion
}

// CompanionSelection analyzes highlighted text and optionally guides the
// companion cursor with a real-time contextual reply.
func (m *Manager) CompanionSelection(ctx context.Context, req types.SelectionRequest) (*types.SelectionAnalysis, *types.APIError) {
	if m.companion == nil {
		return nil, &types.APIError{Code: types.CodeInternal, Message: "companion controller not initialised"}
	}
	return m.companion.HandleSelection(req)
}

// Evaluate runs a JavaScript expression in the page context (or against a
// resolved @eN element when req.Ref is non-empty).
func (m *Manager) Evaluate(ctx context.Context, sessionID, tabID string, req types.EvaluateRequest) (*types.EvaluateResult, *types.APIError) {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	leave, apiErr := t.enter(ctx, m.cfg.Limits)
	if apiErr != nil {
		return nil, apiErr
	}
	defer leave()

	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ectx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var objectID string
	if strings.TrimSpace(req.Ref) != "" {
		snapID := req.SnapshotID
		if snapID == "" {
			if cur := t.snapshots.Current(tabID); cur != nil {
				snapID = cur.ID
			}
		}
		ref, apiErr := snapshots.Resolve(t.snapshots, tabID, t.page, snapID, strings.TrimPrefix(strings.TrimSpace(req.Ref), "@"), 0)
		if apiErr != nil {
			return nil, apiErr
		}
		objectID, apiErr = t.page.ResolveBackendNode(ectx, ref.BackendID)
		if apiErr != nil {
			return nil, apiErr
		}
		defer t.page.ReleaseObject(context.Background(), objectID)
	}

	res, apiErr := t.page.EvaluateUser(ectx, req.Expression, objectID)
	if apiErr != nil {
		return nil, apiErr
	}
	s.touch(m.cfg.Now())
	return res, nil
}

// PDF renders the current tab to a PDF document.
func (m *Manager) PDF(ctx context.Context, sessionID, tabID string, req types.PDFRequest) ([]byte, *types.APIError) {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	leave, apiErr := t.enter(ctx, m.cfg.Limits)
	if apiErr != nil {
		return nil, apiErr
	}
	defer leave()

	pdf, apiErr := t.page.PDF(ctx, req)
	if apiErr != nil {
		return nil, apiErr
	}
	s.touch(m.cfg.Now())
	return pdf, nil
}

// Storage inspects, seeds, or clears localStorage/sessionStorage on a tab.
func (m *Manager) Storage(ctx context.Context, sessionID, tabID string, req types.StorageRequest) (*types.StorageResult, *types.APIError) {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	leave, apiErr := t.enter(ctx, m.cfg.Limits)
	if apiErr != nil {
		return nil, apiErr
	}
	defer leave()

	res, apiErr := t.page.Storage(ctx, req)
	if apiErr != nil {
		return nil, apiErr
	}
	s.touch(m.cfg.Now())
	return res, nil
}

// Dialogs returns captured JavaScript dialogs on a tab and optionally updates
// how the next dialog will be handled.
func (m *Manager) Dialogs(ctx context.Context, sessionID, tabID string, req *types.DialogPolicyRequest) (*types.DialogsResult, *types.APIError) {
	s, t, apiErr := m.lookup(sessionID, tabID)
	if apiErr != nil {
		return nil, apiErr
	}
	out := t.page.Dialogs(req)
	s.touch(m.cfg.Now())
	return &out, nil
}

// ensure referenced symbols stay imported
var (
	_ = errors.Is
	_ = browser.MaxScreenshotBytes
)
