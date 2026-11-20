package sessions

import (
	"testing"

	"github.com/xtrcom/xt-nichrome/internal/snapshots"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

func TestValidateActionAndCheckActionable(t *testing.T) {
	// Valid click action.
	if err := validateAction(types.ActionRequest{
		Kind:       types.ActionClick,
		SnapshotID: "s1",
		Ref:        "e1",
		Button:     "left",
		ClickCount: 1,
	}); err != nil {
		t.Fatalf("validateAction(click) unexpected error: %v", err)
	}

	// Viewport scroll allows omitting Ref and SnapshotID.
	if err := validateAction(types.ActionRequest{
		Kind:      types.ActionScroll,
		Direction: "down",
		Amount:    480,
	}); err != nil {
		t.Fatalf("validateAction(scroll) unexpected error: %v", err)
	}

	// Upload requires at least one file.
	if err := validateAction(types.ActionRequest{
		Kind:       types.ActionUpload,
		SnapshotID: "s1",
		Ref:        "e2",
	}); err == nil || err.Code != types.CodeInvalidRequest {
		t.Fatalf("expected upload without files to fail validation, got %v", err)
	}

	// checkActionable: filling a button must be rejected with ELEMENT_NOT_ACTIONABLE.
	btnRef := &snapshots.Ref{ID: "e1", Role: "button", Tag: "BUTTON", Actionable: true}
	if err := checkActionable(types.ActionFill, btnRef); err == nil || err.Code != types.CodeElementNotActionable {
		t.Fatalf("expected fill on button to fail with ELEMENT_NOT_ACTIONABLE, got %v", err)
	}
	txtRef := &snapshots.Ref{ID: "e2", Role: "textbox", Tag: "INPUT", Actionable: true}
	if err := checkActionable(types.ActionFill, txtRef); err != nil {
		t.Fatalf("expected fill on textbox to pass, got %v", err)
	}

	if !isAmbiguous(&types.APIError{Code: types.CodeTimeout}) || isAmbiguous(&types.APIError{Code: types.CodeInvalidRequest}) {
		t.Fatal("isAmbiguous classification failed")
	}
}
