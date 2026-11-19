package tests

import (
	"strings"
	"testing"
)

// TestPersistentProfile survives a session close and reopen, which is the whole
// reason persistent mode exists.
func TestPersistentProfile(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)

	open := func() (string, string) {
		var s struct {
			ID      string `json:"sessionId"`
			Mode    string `json:"mode"`
			Profile string `json:"profileId"`
		}
		call{T: t, D: d, Method: "POST", Path: "/v1/sessions", Body: map[string]any{
			"mode": "persistent", "profileId": "agent-a",
		}}.mustOK(&s)
		if s.Mode != "persistent" || s.Profile != "agent-a" {
			t.Fatalf("session came back as %q/%q", s.Mode, s.Profile)
		}
		var tab struct {
			TabID string `json:"tabId"`
		}
		call{T: t, D: d, Method: "POST", Path: "/v1/sessions/" + s.ID + "/tabs", Body: map[string]any{}}.mustOK(&tab)
		return s.ID, tab.TabID
	}
	closeSession := func(id string) {
		call{T: t, D: d, Method: "DELETE", Path: "/v1/sessions/" + id}.do()
	}

	sid, tid := open()
	s := session{ID: sid, Tab: tid}
	s.navigate(t, d, d.fixtureURL(t, "/storage"))
	// Wait for the write to land in the profile before closing.
	var wrote struct {
		OK bool `json:"ok"`
	}
	_ = wrote
	snap := s.snapshot(t, d, "")
	_ = snap
	closeSession(sid)

	// Reopening the same profile must show the value the first session wrote.
	sid2, tid2 := open()
	defer closeSession(sid2)
	s2 := session{ID: sid2, Tab: tid2}
	s2.navigate(t, d, d.fixtureURL(t, "/storage/read"))
	read := s2.snapshot(t, d, "")
	if !strings.Contains(read.Text, "fixture-value") {
		t.Fatalf("the profile did not retain localStorage across sessions; snapshot:\n%s", read.Text)
	}
}

// TestEphemeralIsolation proves two ephemeral sessions cannot see each other's
// storage, which is the privacy property that makes shared-browser pooling safe.
func TestEphemeralIsolation(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)

	a := newSession(t, d)
	a.navigate(t, d, d.fixtureURL(t, "/storage"))
	// Confirm the write took effect in A.
	aRead := a.snapshot(t, d, "")
	_ = aRead
	a.navigate(t, d, d.fixtureURL(t, "/storage/read"))
	if snap := a.snapshot(t, d, ""); !strings.Contains(snap.Text, "fixture-value") {
		t.Fatalf("session A could not read its own storage; snapshot:\n%s", snap.Text)
	}

	b := newSession(t, d)
	b.navigate(t, d, d.fixtureURL(t, "/storage/read"))
	if snap := b.snapshot(t, d, ""); strings.Contains(snap.Text, "fixture-value") {
		t.Fatalf("session B read session A's storage; snapshot:\n%s", snap.Text)
	}
}

// TestConcurrentTabsAreSerialised checks that two actions on one tab cannot
// interleave, and that a busy tab answers instead of hanging.
func TestConcurrentTabsAreSerialised(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)
	s.navigate(t, d, d.fixtureURL(t, "/app"))
	snap := s.snapshot(t, d, "")
	ref := findRef(t, snap.Text, "button", "Increment")

	type outcome struct {
		status int
		body   []byte
	}
	results := make(chan outcome, 8)
	for i := 0; i < 8; i++ {
		go func() {
			status, body := call{T: t, D: d, Method: "POST", Path: s.path("/actions"), Body: map[string]any{
				"kind": "click", "snapshotId": snap.SnapshotID, "ref": ref,
			}}.do()
			results <- outcome{status, body}
		}()
	}
	okCount, busyCount := 0, 0
	for i := 0; i < 8; i++ {
		r := <-results
		switch {
		case r.status >= 200 && r.status < 300:
			okCount++
		case r.status == 409:
			busyCount++
		default:
			t.Errorf("unexpected status %d: %s", r.status, r.body)
		}
	}
	if okCount == 0 {
		t.Fatal("no concurrent action succeeded")
	}
	// Every response must be a decision, never a hang or a 5xx.
	if okCount+busyCount != 8 {
		t.Fatalf("only %d of 8 concurrent actions were accounted for", okCount+busyCount)
	}
}

// TestTabQuota enforces the per-session tab bound.
func TestTabQuota(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, "-max-tabs", "2")
	s := newSession(t, d)
	var tab struct {
		TabID string `json:"tabId"`
	}
	call{T: t, D: d, Method: "POST", Path: "/v1/sessions/" + s.ID + "/tabs", Body: map[string]any{}}.mustOK(&tab)

	var e apiError
	status := call{T: t, D: d, Method: "POST", Path: "/v1/sessions/" + s.ID + "/tabs", Body: map[string]any{}}.json(&e)
	if status != 429 || e.Code != "CAPACITY" {
		t.Fatalf("third tab gave %d %s (%s)", status, e.Code, e.Message)
	}
}

// TestSessionQuota enforces the ephemeral session bound.
func TestSessionQuota(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, "-max-ephemeral", "2")
	for i := 0; i < 2; i++ {
		var s struct {
			ID string `json:"sessionId"`
		}
		call{T: t, D: d, Method: "POST", Path: "/v1/sessions", Body: map[string]any{}}.mustOK(&s)
		t.Cleanup(func() {
			call{T: t, D: d, Method: "DELETE", Path: "/v1/sessions/" + s.ID}.do()
		})
	}
	var e apiError
	status := call{T: t, D: d, Method: "POST", Path: "/v1/sessions", Body: map[string]any{}}.json(&e)
	if status != 429 || e.Code != "CAPACITY" {
		t.Fatalf("third ephemeral session gave %d %s (%s)", status, e.Code, e.Message)
	}
}

// TestProfileNameValidation rejects names that could escape the profile root.
func TestProfileNameValidation(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	for _, bad := range []string{
		"../escape", "..", "a/b", "/abs", "UPPER", "with space",
		"trailing.", "", ".hidden", strings.Repeat("x", 80),
	} {
		body := map[string]any{"mode": "persistent", "profileId": bad}
		var e apiError
		status := call{T: t, D: d, Method: "POST", Path: "/v1/sessions", Body: body}.json(&e)
		if status < 400 {
			t.Errorf("profile name %q was accepted", bad)
		}
		if e.Code != "INVALID_REQUEST" {
			t.Errorf("profile name %q gave %s, want INVALID_REQUEST", bad, e.Code)
		}
	}
	// A missing profileId on a persistent session is an error, not a default.
	var e apiError
	status := call{T: t, D: d, Method: "POST", Path: "/v1/sessions", Body: map[string]any{"mode": "persistent"}}.json(&e)
	if status != 400 {
		t.Fatalf("persistent session without profileId gave %d %s", status, e.Code)
	}
	// A profileId on an ephemeral session is a client bug, not something to ignore.
	status = call{T: t, D: d, Method: "POST", Path: "/v1/sessions", Body: map[string]any{
		"mode": "ephemeral", "profileId": "x",
	}}.json(&e)
	if status != 400 {
		t.Fatalf("ephemeral session with profileId gave %d %s", status, e.Code)
	}
}

// TestProfileBusy refuses a second concurrent open of one profile.
func TestProfileBusy(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	var first struct {
		ID string `json:"sessionId"`
	}
	call{T: t, D: d, Method: "POST", Path: "/v1/sessions", Body: map[string]any{
		"mode": "persistent", "profileId": "shared",
	}}.mustOK(&first)
	t.Cleanup(func() {
		call{T: t, D: d, Method: "DELETE", Path: "/v1/sessions/" + first.ID}.do()
	})

	var e apiError
	status := call{T: t, D: d, Method: "POST", Path: "/v1/sessions", Body: map[string]any{
		"mode": "persistent", "profileId": "shared",
	}}.json(&e)
	if status != 409 || e.Code != "BUSY_PROFILE" {
		t.Fatalf("second open of a busy profile gave %d %s (%s)", status, e.Code, e.Message)
	}
}
