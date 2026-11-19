package companion

import (
	"testing"

	"github.com/xtrcom/xt-nichrome/internal/types"
)

func TestCompanionControllerLifecycle(t *testing.T) {
	c := New()
	c.SetDisableExec(true)
	defer c.Close()

	st0 := c.State()
	if st0.Visible {
		t.Fatal("expected initial companion cursor to be hidden")
	}

	// Guide to a coordinate with friendly chat bubble.
	x, y := 820.0, 460.0
	st, apiErr := c.Apply(types.CompanionRequest{
		Action:  "guide",
		X:       &x,
		Y:       &y,
		Mood:    "friendly",
		Subtext: "@e4 • Submit",
		Message: "hey, you should click this to help me!",
	})
	if apiErr != nil {
		t.Fatalf("Apply(guide): %v", apiErr)
	}
	if !st.Visible || st.X != 820 || st.Y != 460 || st.Mood != "friendly" || st.Message != "hey, you should click this to help me!" {
		t.Fatalf("unexpected state after guide: %+v", st)
	}

	// Update mood to sad with a warning message.
	st2, apiErr := c.Apply(types.CompanionRequest{
		Action:  "say",
		Mood:    "sad",
		Message: "hey, this person isnt nice :(",
	})
	if apiErr != nil {
		t.Fatalf("Apply(say): %v", apiErr)
	}
	if st2.Mood != "sad" || st2.Message != "hey, this person isnt nice :(" {
		t.Fatalf("unexpected state after say: %+v", st2)
	}

	// FollowActions auto-updates position and subtext when visible.
	st3, ok := c.NotifyAction(400, 300, "e9", "click", `click "Submit"`)
	if !ok || st3.X != 400 || st3.Y != 300 || st3.TargetRef != "e9" {
		t.Fatalf("NotifyAction failed: ok=%v state=%+v", ok, st3)
	}

	// Reject unknown mood.
	if _, apiErr := c.Apply(types.CompanionRequest{Action: "guide", Mood: "invalid-mood"}); apiErr == nil {
		t.Fatal("expected error for invalid mood")
	}

	// Hide companion cursor.
	stHide, apiErr := c.Apply(types.CompanionRequest{Action: "hide"})
	if apiErr != nil || stHide.Visible {
		t.Fatalf("Apply(hide) failed: err=%v visible=%v", apiErr, stHide.Visible)
	}
}
