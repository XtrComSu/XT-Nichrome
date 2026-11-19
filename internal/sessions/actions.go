package sessions

import (
	"context"
	"errors"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/browser"
	"github.com/xtrcom/xt-nichrome/internal/snapshots"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

// Act performs a ref-based action on a tab.
//
// The hard part is not the click. It is deciding, when something goes wrong
// halfway, whether the page changed. Three outcomes are distinguishable:
//
//   - the action never reached the page  -> safe to report failure, retry is safe
//   - the dispatch completed             -> report completed
//   - the dispatch was sent but its result was lost (timeout, client gone,
//     renderer crash) -> report ACTION_OUTCOME_UNKNOWN and refuse to retry
//
// Reporting "failed" in the third case is the bug that makes agents double-submit
// a payment form, so the code path is explicit rather than inferred.
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

	ref, apiErr := snapshots.Resolve(t.snapshots, tabID, t.page, req.SnapshotID, req.Ref, req.Generation)
	if apiErr != nil {
		return nil, apiErr
	}
	if apiErr := checkActionable(req.Kind, ref); apiErr != nil {
		return nil, apiErr
	}

	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	objectID, apiErr := t.page.ResolveBackendNode(actx, ref.BackendID)
	if apiErr != nil {
		return nil, apiErr
	}
	defer t.page.ReleaseObject(context.Background(), objectID)

	// Identity revalidation: a snapshot reference identifies one node, not a
	// role/name pair. If the node was swapped for an identically labelled one,
	// the signature changes and the action must not proceed.
	fp, apiErr := t.page.Fingerprint(actx, objectID)
	if apiErr != nil {
		return nil, apiErr
	}
	if !fp.Connected {
		return nil, &types.APIError{Code: types.CodeStaleRef, Message: "the referenced element has been removed from the document"}
	}
	if ref.Signature != "" && fp.Signature != ref.Signature {
		return nil, &types.APIError{
			Code:    types.CodeStaleRef,
			Message: "the referenced element was replaced by a different node; take a fresh snapshot",
			Detail:  map[string]any{"ref": req.Ref},
		}
	}

	if apiErr := t.page.WaitForActionability(actx, objectID, timeout); apiErr != nil {
		return nil, apiErr
	}

	genBefore := t.page.Generation()

	// From here on the page may change. Mark the tab as having a dispatched
	// side effect until we know its result.
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
	}
	t.page.MarkInflight(false)
	t.page.TouchActivity()
	s.touch(m.cfg.Now())

	// An action that submits a form usually navigates a moment later. Reporting
	// "no fresh snapshot needed" and then having the refs die on the next call
	// is the failure mode that makes an agent retry a click it already made, so
	// wait out a short settle window before deciding.
	// A click that triggers a load shows up as a pending navigation within a few
	// milliseconds. Waiting a fixed window on every action would add that window
	// to every non-navigating click, which is most of them.
	navigated, genAfter := t.settle(actx, genBefore, 2*time.Second)
	if navigated {
		// The old reference table belongs to a document that no longer exists.
		t.snapshots.Invalidate(tabID)
	}

	if actErr != nil {
		// A dispatch that was sent and then failed ambiguously must not be
		// reported as a clean failure.
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

// settle waits briefly for a post-action navigation to commit.
//
// It returns whether the document generation moved and the generation observed
// at the end. The wait is bounded by both the caller's deadline and the settle
// window, so a page that never navigates does not delay the response.
func (t *tab) settle(ctx context.Context, from int64, window time.Duration) (bool, int64) {
	// Phase 1: a short grace period to see whether the action started a load.
	// A click with no navigation is decided here, cheaply.
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
		// Give a slow-to-start navigation one more look before deciding.
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

	// Phase 2: a load is in flight, so wait for the commit within the window.
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

func validateAction(req types.ActionRequest) *types.APIError {
	if req.Kind == "" {
		return &types.APIError{Code: types.CodeInvalidRequest, Message: "kind is required", Detail: map[string]any{"field": "kind"}}
	}
	if req.Kind != types.ActionClick && req.Kind != types.ActionFill {
		return &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "kind must be click or fill",
			Detail:  map[string]any{"field": "kind", "supported": []string{types.ActionClick, types.ActionFill}},
		}
	}
	if req.SnapshotID == "" {
		return &types.APIError{Code: types.CodeInvalidRequest, Message: "snapshotId is required", Detail: map[string]any{"field": "snapshotId"}}
	}
	if req.Ref == "" {
		return &types.APIError{Code: types.CodeInvalidRequest, Message: "ref is required", Detail: map[string]any{"field": "ref"}}
	}
	switch req.Kind {
	case types.ActionClick:
		if req.Text != "" {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "text is not valid for a click", Detail: map[string]any{"field": "text"}}
		}
		if req.Button != "" && req.Button != "left" && req.Button != "right" && req.Button != "middle" {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "button must be left, right or middle", Detail: map[string]any{"field": "button"}}
		}
		if req.ClickCount < 0 || req.ClickCount > 3 {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "clickCount must be between 1 and 3", Detail: map[string]any{"field": "clickCount"}}
		}
	case types.ActionFill:
		if req.Button != "" || req.ClickCount != 0 {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "button and clickCount are not valid for a fill", Detail: map[string]any{"field": "button"}}
		}
		if req.Text == "" {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "text is required for a fill", Detail: map[string]any{"field": "text"}}
		}
	}
	if req.TimeoutMS < 0 || req.TimeoutMS > 120000 {
		return &types.APIError{Code: types.CodeInvalidRequest, Message: "timeoutMs must be between 1 and 120000", Detail: map[string]any{"field": "timeoutMs"}}
	}
	return nil
}

// checkActionable rejects a kind that cannot apply to the referenced role.
func checkActionable(kind string, ref *snapshots.Ref) *types.APIError {
	if !ref.Actionable {
		return &types.APIError{
			Code:    types.CodeElementNotActionable,
			Message: "the referenced element does not accept actions",
			Detail:  map[string]any{"ref": ref.ID, "role": ref.Role},
		}
	}
	if kind == types.ActionFill {
		switch ref.Role {
		case "textbox", "searchbox", "combobox", "spinbutton", "textboxmultiline":
			return nil
		}
		return &types.APIError{
			Code:    types.CodeElementNotActionable,
			Message: "the referenced element is not a text input",
			Detail:  map[string]any{"ref": ref.ID, "role": ref.Role},
		}
	}
	return nil
}

// isAmbiguous reports whether a failed dispatch may still have reached the page.
func isAmbiguous(err *types.APIError) bool {
	if err == nil {
		return false
	}
	switch err.Code {
	case types.CodeTimeout, types.CodeActionOutcomeUnknown, types.CodeBrowserFailed:
		return true
	}
	// A renderer-side exception after the input was dispatched is also
	// ambiguous: the event sequence may have partly applied.
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

// ensure referenced symbols stay imported
var (
	_ = errors.Is
	_ = browser.MaxScreenshotBytes
)
