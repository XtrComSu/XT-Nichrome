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

func TestAnalyzeSelectionAndHandleSelection(t *testing.T) {
	c := New()
	c.SetDisableExec(true)
	defer c.Close()

	// 1. Hostile / rude chat message in Discord -> sad mood + boundary reply.
	rude := AnalyzeSelection("you suck and nobody asked", "discord", "#general")
	if rude.Category != "hostile_chat" || rude.Mood != "sad" || rude.SuggestedReply == "" {
		t.Fatalf("unexpected hostile_chat analysis: %+v", rude)
	}

	// 2. Friendly greeting mentioning Ni -> excited mood.
	greet := AnalyzeSelection("hello ni :) do you wana say anything?", "discord", "#general")
	if greet.Category != "friendly_chat" || greet.Mood != "excited" {
		t.Fatalf("unexpected friendly_chat analysis: %+v", greet)
	}

	// 3. Question in chat -> curious mood.
	q := AnalyzeSelection("how do I configure the allowlist proxy?", "discord", "#dev")
	if q.Category != "question_chat" || q.Mood != "curious" {
		t.Fatalf("unexpected question_chat analysis: %+v", q)
	}

	// 4. Dangerous command -> warn mood.
	cmdWarn := AnalyzeSelection("rm -rf /tmp/build", "kitty", "zsh")
	if cmdWarn.Category != "command_warning" || cmdWarn.Mood != "warn" {
		t.Fatalf("unexpected command_warning analysis: %+v", cmdWarn)
	}

	// 5. Base64 encoded payload -> thinking mood + decoded text.
	b64 := AnalyzeSelection("SGVsbG8gTmktMjg=", "kitty", "zsh")
	if b64.Category != "encoded_data" || b64.Decoded != "Hello Ni-28" {
		t.Fatalf("unexpected base64 decode analysis: %+v", b64)
	}

	// 6. URL inspection (normal vs raw IP warning).
	uNormal := AnalyzeSelection("https://example.com/docs?page=2", "firefox", "Docs")
	if uNormal.Category != "url" || uNormal.Mood != "curious" {
		t.Fatalf("unexpected normal URL analysis: %+v", uNormal)
	}
	uSusp := AnalyzeSelection("http://192.168.1.50/login", "firefox", "Login")
	if uSusp.Category != "url" || uSusp.Mood != "warn" {
		t.Fatalf("unexpected suspicious URL analysis: %+v", uSusp)
	}

	// 7. HandleSelection with Guide=true updates CompanionState.LastSelection and shows bubble.
	tx, ty := 512.0, 384.0
	res, apiErr := c.HandleSelection(types.SelectionRequest{
		Text:        "you are an idiot",
		WindowClass: "discord",
		WindowTitle: "#chat",
		Guide:       true,
		X:           &tx,
		Y:           &ty,
	})
	if apiErr != nil {
		t.Fatalf("HandleSelection failed: %v", apiErr)
	}
	st := c.State()
	if !st.Visible || st.Mood != "sad" || st.LastSelection == nil || st.LastSelection.Category != "hostile_chat" || !res.Guided {
		t.Fatalf("HandleSelection did not update state properly: %+v", st)
	}
}
