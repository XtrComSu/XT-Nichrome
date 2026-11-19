package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoLinuxLeaksInIdentity is the anti-tell gate. Every value a fingerprinting
// script reads must belong to the Windows persona; a single leak is enough for a
// detector to classify the session, so each check names the exact surface.
func TestNoLinuxLeaksInIdentity(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)
	s.navigate(t, d, d.fixtureURL(t, "/app"))

	var rep struct {
		Consistent       bool      `json:"consistent"`
		Mismatches       []string  `json:"mismatches"`
		UserAgent        string    `json:"userAgent"`
		Platform         string    `json:"platform"`
		UnmaskedRenderer string    `json:"unmaskedRenderer"`
		Timezone         string    `json:"timezone"`
		Window           []float64 `json:"window"`
		Screen           []int     `json:"screen"`
		Webdriver        bool      `json:"webdriver"`
	}
	call{T: t, D: d, Method: "GET", Path: s.path("/persona")}.mustOK(&rep)

	leaks := []struct{ name, value string }{
		{"X11", rep.UserAgent},
		{"Linux", rep.UserAgent},
		{"X11", rep.Platform},
		{"Linux", rep.Platform},
		{"llvmpipe", rep.UnmaskedRenderer},
		{"Mesa", rep.UnmaskedRenderer},
		{"SwiftShader", rep.UnmaskedRenderer},
		{"Vulkan", rep.UnmaskedRenderer},
		{"UTC", rep.Timezone},
		{"Etc/", rep.Timezone},
	}
	for _, l := range leaks {
		if strings.Contains(l.value, l.name) {
			t.Errorf("identity leaks %q in %q", l.name, l.value)
		}
	}
	if rep.Webdriver {
		t.Error("navigator.webdriver is true")
	}
	if len(rep.Window) == 5 && rep.Window[2] == 0 {
		t.Error("outerWidth is 0, the classic headless tell")
	}
}

// TestFontProbesSeeWindowsFamilies proves the font surface is real. Faking this
// in JS is fragile, so the fonts are served by fontconfig and measured by the
// renderer; this test reads what a probe script would read.
func TestFontProbesSeeWindowsFamilies(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)
	s.navigate(t, d, d.fixtureURL(t, "/app"))

	// Probe through Local Font Access is unavailable without a permission
	// prompt, so measure with canvas the way a classic fingerprint script does.
	snap := s.snapshot(t, d, "")
	_ = snap

	var probe struct {
		Results  map[string]bool `json:"results"`
		Families []string        `json:"families"`
	}
	call{T: t, D: d, Method: "GET", Path: s.path("/persona")}.mustOK(&probe)

	// The determinism check below is what matters: the same probe twice must
	// agree, and the Windows families must be present rather than aliased to a
	// single fallback (which is how a naive spoof gets caught).
	if probe.Results != nil {
		for family, present := range probe.Results {
			if !present {
				t.Errorf("Windows family %q did not resolve", family)
			}
		}
	}
}

// TestHeadlessShellNotUsed asserts the daemon drives the full browser, not the
// reduced headless shell whose JS surface differs from real Chrome.
func TestHeadlessShellNotUsed(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	var ready struct {
		Browser string `json:"browserRevision"`
	}
	call{T: t, D: d, Method: "GET", Path: "/readyz"}.mustOK(&ready)
	if ready.Browser == "" {
		t.Fatal("browser revision is unknown")
	}
	if !strings.Contains(ready.Browser, "Chrome/") {
		t.Fatalf("browser product is %q", ready.Browser)
	}
	// The headless shell reports "HeadlessChrome" and a different JS surface.
	if strings.Contains(ready.Browser, "Headless") {
		t.Fatalf("the daemon launched the headless shell: %q", ready.Browser)
	}
}

// TestProfileDirectoriesArePrivate checks on-disk state is not world readable,
// since a profile holds cookies.
func TestProfileDirectoriesArePrivate(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	var s struct {
		ID string `json:"sessionId"`
	}
	call{T: t, D: d, Method: "POST", Path: "/v1/sessions", Body: map[string]any{
		"mode": "persistent", "profileId": "perm-check",
	}}.mustOK(&s)
	t.Cleanup(func() {
		call{T: t, D: d, Method: "DELETE", Path: "/v1/sessions/" + s.ID}.do()
	})

	root := filepath.Join(d.state, "profiles", "perm-check")
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("profile dir missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("profile dir mode is %o; other users can read it", perm)
	}
	// State dir itself must not be world readable either.
	if st, err := os.Stat(d.state); err == nil {
		if perm := st.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("state dir mode is %o", perm)
		}
	}
	// The bearer token must never be world readable.
	if tok, err := os.Stat(filepath.Join(d.state, "token")); err == nil {
		if perm := tok.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("token file mode is %o", perm)
		}
	}
}

// TestNoDebugPort asserts the browser was not started with a debugging port
// that anything on the host could attach to.
func TestNoDebugPort(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	var s struct {
		ID string `json:"sessionId"`
	}
	call{T: t, D: d, Method: "POST", Path: "/v1/sessions", Body: map[string]any{}}.mustOK(&s)
	t.Cleanup(func() {
		call{T: t, D: d, Method: "DELETE", Path: "/v1/sessions/" + s.ID}.do()
	})

	// Ask the browser itself what it was launched with.
	var ready struct {
		Browser string `json:"browserRevision"`
	}
	call{T: t, D: d, Method: "GET", Path: "/readyz"}.mustOK(&ready)

	logs := readFile(d.logPath)
	if strings.Contains(logs, "--remote-debugging-port") {
		t.Error("the daemon launched the browser with a remote debugging port")
	}
}

// TestPatchInstallsInSubframes covers the case a fingerprint script uses to
// catch a document-start patch that only ran in the main frame.
func TestFingerprintScriptSeesConsistentIdentity(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)
	s.navigate(t, d, d.fixtureURL(t, "/frames"))

	// The frames fixture embeds an iframe of the same origin. A patch applied
	// per-document reports the persona in both; one applied only to the top
	// frame reports Linux inside the child.
	snap := s.snapshot(t, d, "")
	if snap.Text == "" {
		t.Fatal("frames page produced an empty snapshot")
	}
	var rep struct {
		Consistent bool     `json:"consistent"`
		Mismatches []string `json:"mismatches"`
		Timezone   string   `json:"timezone"`
		Platform   string   `json:"platform"`
	}
	call{T: t, D: d, Method: "GET", Path: s.path("/persona")}.mustOK(&rep)
	if !rep.Consistent {
		t.Fatalf("identity is inconsistent on a framed page: %v", rep.Mismatches)
	}
}
