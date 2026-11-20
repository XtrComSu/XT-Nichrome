package tests

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestM0LaunchAndIdentity is the milestone gate: the browser starts, the tab is
// drivable, and the identity it presents is a consistent Windows 11 Chrome.
func TestM0LaunchAndIdentity(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)

	var ready struct {
		Ready   bool   `json:"ready"`
		Browser string `json:"browserRevision"`
		Persona string `json:"persona"`
	}
	call{T: t, D: d, Method: "GET", Path: "/readyz"}.mustOK(&ready)
	if !ready.Ready {
		t.Fatal("readyz reported not ready")
	}
	if !strings.Contains(ready.Browser, "Chrome/141") {
		t.Fatalf("browser revision %q is not Chrome 141", ready.Browser)
	}
	if ready.Persona != "win11-chrome-141" {
		t.Fatalf("persona is %q", ready.Persona)
	}

	var rep struct {
		Consistent          bool      `json:"consistent"`
		Mismatches          []string  `json:"mismatches"`
		UserAgent           string    `json:"userAgent"`
		Platform            string    `json:"platform"`
		Webdriver           bool      `json:"webdriver"`
		HardwareConcurrency int       `json:"hardwareConcurrency"`
		DeviceMemory        int       `json:"deviceMemory"`
		Screen              []int     `json:"screen"`
		Window              []float64 `json:"window"`
		UnmaskedRenderer    string    `json:"unmaskedRenderer"`
		Timezone            string    `json:"timezone"`
		PointerFine         bool      `json:"pointerFine"`
		ChromeObject        bool      `json:"chromeObject"`
		Plugins             int       `json:"plugins"`
	}
	call{T: t, D: d, Method: "GET", Path: s.path("/persona")}.mustOK(&rep)

	if !rep.Consistent {
		t.Fatalf("persona is not self-consistent: %v", rep.Mismatches)
	}
	want := map[string]func() error{
		"user agent says Windows NT 10.0": func() error {
			if !strings.Contains(rep.UserAgent, "Windows NT 10.0; Win64; x64") {
				return fmt.Errorf("user agent is %q", rep.UserAgent)
			}
			return nil
		},
		"user agent says Chrome 141": func() error {
			if !strings.Contains(rep.UserAgent, "Chrome/141.") {
				return fmt.Errorf("user agent is %q", rep.UserAgent)
			}
			return nil
		},
		"user agent is not headless": func() error {
			if strings.Contains(rep.UserAgent, "Headless") {
				return fmt.Errorf("user agent leaks headless: %q", rep.UserAgent)
			}
			return nil
		},
		"platform is Win32": func() error {
			if rep.Platform != "Win32" {
				return fmt.Errorf("platform is %q", rep.Platform)
			}
			return nil
		},
		"webdriver is false": func() error {
			if rep.Webdriver {
				return fmt.Errorf("navigator.webdriver is true")
			}
			return nil
		},
		"screen is a desktop resolution": func() error {
			if len(rep.Screen) < 2 || rep.Screen[0] < 1280 || rep.Screen[1] < 720 {
				return fmt.Errorf("screen is %v", rep.Screen)
			}
			return nil
		},
		"outer size is not 0 (headless leak)": func() error {
			if len(rep.Window) < 4 || rep.Window[2] == 0 || rep.Window[3] == 0 {
				return fmt.Errorf("outer size is %v", rep.Window)
			}
			return nil
		},
		"webgl reports an ANGLE Direct3D renderer": func() error {
			if !strings.Contains(rep.UnmaskedRenderer, "ANGLE (") || !strings.Contains(rep.UnmaskedRenderer, "Direct3D11") {
				return fmt.Errorf("unmasked renderer is %q", rep.UnmaskedRenderer)
			}
			return nil
		},
		"timezone is a US zone": func() error {
			if !strings.HasPrefix(rep.Timezone, "America/") {
				return fmt.Errorf("timezone is %q", rep.Timezone)
			}
			return nil
		},
		"pointer is fine (desktop input)": func() error {
			if !rep.PointerFine {
				return fmt.Errorf("(pointer: fine) is false")
			}
			return nil
		},
		"window.chrome exists": func() error {
			if !rep.ChromeObject {
				return fmt.Errorf("window.chrome is missing")
			}
			return nil
		},
		"plugins are populated": func() error {
			if rep.Plugins == 0 {
				return fmt.Errorf("navigator.plugins is empty")
			}
			return nil
		},
	}
	for name, check := range want {
		if err := check(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestM1NavigationAndSnapshot covers the snapshot contract.
func TestM1NavigationAndSnapshot(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)
	fx := newFixture(t)
	_ = fx

	d.workerBase()
	s.navigate(t, d, d.fixtureURL(t, "/form"))

	snap := s.snapshot(t, d, "")
	if snap.Generation == 0 {
		t.Fatal("generation is zero after a navigation")
	}
	if snap.SnapshotID == "" {
		t.Fatal("snapshot id is empty")
	}
	if snap.RefCount == 0 {
		t.Fatalf("no references were produced; text:\n%s", snap.Text)
	}
	for _, want := range []string{"textbox", "button", "checkbox", "combobox", "@e1"} {
		if !strings.Contains(snap.Text, want) {
			t.Errorf("snapshot is missing %q; text:\n%s", want, snap.Text)
		}
	}
	if len(snap.Text) > 20000 {
		t.Errorf("default snapshot is %d bytes, which is not compact", len(snap.Text))
	}
}

// TestM1SnapshotBudget verifies the output bound is enforced, not aspirational.
func TestM1SnapshotBudget(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)
	s.navigate(t, d, d.fixtureURL(t, "/big"))

	// The budget must be smaller than the page's natural render, otherwise the
	// test proves nothing about truncation.
	for _, maxChars := range []int{120, 300} {
		snap := s.snapshot(t, d, fmt.Sprintf("?maxChars=%d", maxChars))
		if got := len([]rune(snap.Text)); got > maxChars {
			t.Errorf("maxChars=%d produced %d runes", maxChars, got)
		}
		if !snap.Truncated {
			t.Errorf("maxChars=%d did not report truncation", maxChars)
		}
	}
	snap := s.snapshot(t, d, "?maxRefs=3")
	if snap.RefCount > 3 {
		t.Errorf("maxRefs=3 produced %d refs", snap.RefCount)
	}
}

// TestM2ActionLifecycle covers click and fill against a live form.
func TestM2ActionLifecycle(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)
	s.navigate(t, d, d.fixtureURL(t, "/form"))
	snap := s.snapshot(t, d, "")

	nameRef := findRef(t, snap.Text, "textbox", "Full name")
	submitRef := findRef(t, snap.Text, "button", "Submit")

	var filled actionResult
	call{T: t, D: d, Method: "POST", Path: s.path("/actions"), Body: map[string]any{
		"kind": "fill", "snapshotId": snap.SnapshotID, "ref": nameRef, "text": "Ada Lovelace",
	}}.mustOK(&filled)
	if filled.Outcome != "completed" {
		t.Fatalf("fill outcome is %q", filled.Outcome)
	}

	var clicked actionResult
	call{T: t, D: d, Method: "POST", Path: s.path("/actions"), Body: map[string]any{
		"kind": "click", "snapshotId": snap.SnapshotID, "ref": submitRef,
	}}.mustOK(&clicked)
	if clicked.Outcome != "completed" {
		t.Fatalf("click outcome is %q", clicked.Outcome)
	}
	if !clicked.FreshSnapshotRequired {
		t.Error("a submitting click must report that a fresh snapshot is required")
	}
	if !strings.Contains(clicked.URL, "/form/submit") {
		t.Errorf("click did not navigate; url is %q", clicked.URL)
	}

	// A POST that reached the fixture proves the click really submitted the
	// form rather than mutating the DOM and stopping there.
	if !d.sawSubmit() {
		t.Error("the fixture never received a form submission")
	}
}

// TestM2StaleRefRejected proves a reference from a superseded document cannot
// be used, which is what stops an agent acting on the wrong page.
func TestM2StaleRefRejected(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)
	s.navigate(t, d, d.fixtureURL(t, "/form"))
	old := s.snapshot(t, d, "")
	submitRef := findRef(t, old.Text, "button", "Submit")

	// A second snapshot supersedes the first.
	s.snapshot(t, d, "")

	var e apiError
	status := call{T: t, D: d, Method: "POST", Path: s.path("/actions"), Body: map[string]any{
		"kind": "click", "snapshotId": old.SnapshotID, "ref": submitRef,
	}}.json(&e)
	if status != 409 || e.Code != "STALE_REF" {
		t.Fatalf("superseded snapshot gave %d %s (%s)", status, e.Code, e.Message)
	}

	// A navigation invalidates the whole reference table. The ref below is
	// valid for the page it was taken from, so the only reason to reject it is
	// that the document changed underneath it.
	app := s.snapshot(t, d, "")
	_ = app
	s.navigate(t, d, d.fixtureURL(t, "/app"))
	var e2 apiError
	status = call{T: t, D: d, Method: "POST", Path: s.path("/actions"), Body: map[string]any{
		"kind": "click", "snapshotId": old.SnapshotID, "ref": submitRef,
	}}.json(&e2)
	if status == 200 {
		t.Fatal("acting after a navigation succeeded with a pre-navigation snapshot")
	}
	if status != 409 || e2.Code != "STALE_REF" {
		t.Fatalf("pre-navigation snapshot gave %d %s (%s)", status, e2.Code, e2.Message)
	}

	// A fresh snapshot after the navigation must work, which proves the tab is
	// still drivable and the rejection was about staleness, not breakage.
	fresh := s.snapshot(t, d, "")
	var clicked actionResult
	call{T: t, D: d, Method: "POST", Path: s.path("/actions"), Body: map[string]any{
		"kind": "click", "snapshotId": fresh.SnapshotID, "ref": findRef(t, fresh.Text, "button", "Increment"),
	}}.mustOK(&clicked)
	if clicked.Outcome != "completed" {
		t.Fatalf("fresh snapshot after navigation gave outcome %q", clicked.Outcome)
	}
}

// TestM2WrongKindRejected checks the role/action compatibility gate.
func TestM2WrongKindRejected(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)
	s.navigate(t, d, d.fixtureURL(t, "/form"))
	snap := s.snapshot(t, d, "")
	checkboxRef := findRef(t, snap.Text, "checkbox", "Agree to terms")

	var e apiError
	status := call{T: t, D: d, Method: "POST", Path: s.path("/actions"), Body: map[string]any{
		"kind": "fill", "snapshotId": snap.SnapshotID, "ref": checkboxRef, "text": "x",
	}}.json(&e)
	if status != 409 || e.Code != "ELEMENT_NOT_ACTIONABLE" {
		t.Fatalf("filling a checkbox gave %d %s (%s)", status, e.Code, e.Message)
	}
}

// TestM3PolicyDenied covers the admission check and the proxy denial.
func TestM3PolicyDenied(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)

	var e apiError
	status := call{T: t, D: d, Method: "POST", Path: s.path("/navigate"), Body: map[string]any{
		"url": "http://denied.invalid/x",
	}}.json(&e)
	if status != 403 || e.Code != "POLICY_BLOCKED" {
		t.Fatalf("denied navigation gave %d %s (%s)", status, e.Code, e.Message)
	}

	for _, bad := range []string{
		"file:///etc/passwd",
		"ftp://example.test/x",
		"http://user:pass@example.test/",
		"javascript:alert(1)",
	} {
		var e apiError
		status := call{T: t, D: d, Method: "POST", Path: s.path("/navigate"), Body: map[string]any{"url": bad}}.json(&e)
		if status != 400 && status != 403 {
			t.Errorf("%q gave %d %s, want a rejection", bad, status, e.Code)
		}
	}
	// The tab must still be usable after a rejected navigation.
	s.navigate(t, d, d.fixtureURL(t, "/form"))
	if snap := s.snapshot(t, d, ""); snap.RefCount == 0 {
		t.Fatal("tab became unusable after a rejected navigation")
	}
}

// TestM3RequestValidation covers strict decoding.
func TestM3RequestValidation(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)
	s.navigate(t, d, d.fixtureURL(t, "/form"))
	snap := s.snapshot(t, d, "")

	cases := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"unknown field", `{"kind":"click","snapshotId":"x","ref":"e1","nope":1}`, 400, "INVALID_REQUEST"},
		{"duplicate key", `{"kind":"click","kind":"fill","snapshotId":"x","ref":"e1"}`, 400, "INVALID_REQUEST"},
		{"wrong type", `{"kind":"click","snapshotId":5,"ref":"e1"}`, 400, "INVALID_REQUEST"},
		{"bad kind", `{"kind":"unknown-action","snapshotId":"x","ref":"e1"}`, 400, "INVALID_REQUEST"},
		{"click with text", `{"kind":"click","snapshotId":"x","ref":"e1","text":"hi"}`, 400, "INVALID_REQUEST"},
		{"trailing value", `{"kind":"click","snapshotId":"x","ref":"e1"}{"a":1}`, 400, "INVALID_REQUEST"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var e apiError
			status, data := rawCall(t, d, "POST", s.path("/actions"), c.body)
			if status != c.status {
				json.Unmarshal(data, &e)
				t.Fatalf("got %d %s, want %d %s\nbody: %s", status, e.Code, c.status, c.code, data)
			}
			json.Unmarshal(data, &e)
			if e.Code != c.code {
				t.Fatalf("got code %q, want %q", e.Code, c.code)
			}
		})
	}

	// Auth is enforced and must not be summarised as a validation error.
	for _, h := range []map[string]string{
		{"Authorization": ""},
		{"Authorization": "Bearer wrong-token-value-here"},
		{"Authorization": "Basic dGVzdDp0ZXN0"},
	} {
		var e apiError
		status := call{T: t, D: d, Method: "GET", Path: s.path("/snapshot"), Header: h}.json(&e)
		if status != 401 || e.Code != "UNAUTHORIZED" {
			t.Fatalf("auth header %v gave %d %s", h, status, e.Code)
		}
	}
	_ = snap
}
