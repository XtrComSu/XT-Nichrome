package tests

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestDeniedDestinationNeverReachesServer is the proof that the policy is
// enforced at the proxy and not merely reported. The fixture records every hit;
// a denied origin must leave the counter untouched, which distinguishes "we said
// no" from "we said no and it happened anyway".
func TestDeniedDestinationNeverReachesServer(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)

	// The denied fixture runs on its own port and is absent from the allowlist,
	// so any hit on it proves the request escaped the proxy.
	deniedURL := d.denied.URL + "/page"
	var e apiError
	status := call{T: t, D: d, Method: "POST", Path: s.path("/navigate"), Body: map[string]any{
		"url": deniedURL,
	}}.json(&e)
	if status != 403 || e.Code != "POLICY_BLOCKED" {
		t.Fatalf("unlisted origin gave %d %s (%s)", status, e.Code, e.Message)
	}
	if n := d.denied.Server.Hit.Total(); n != 0 {
		t.Fatalf("the blocked navigation reached the denied server (%d hits)", n)
	}

	// Subresources are a separate path through the proxy: the top-level document
	// is allowed, but its img/css/js point at the unlisted origin. Refusing the
	// document while leaking a subresource would still disclose the request.
	s.navigate(t, d, d.fixtureURL(t, "/subresource/denied"))
	time.Sleep(200 * time.Millisecond) // let the blocked subresource attempts resolve
	if n := d.denied.Server.Hit.Total(); n != 0 {
		t.Fatalf("a blocked subresource reached the denied server (%d hits): %v",
			n, d.denied.Server.Hit.Keys())
	}
	// The page must still be readable, with the script that never ran.
	snap := s.snapshot(t, d, "")
	if !strings.Contains(snap.Text, "waiting") {
		t.Errorf("the denied-subresource page is not in its unloaded state; snapshot:\n%s", snap.Text)
	}
}

// TestEgressDenialsAreCounted checks the operator-visible counter that readyz
// reports, so "is anything being blocked?" is answerable without grepping logs.
func TestEgressDenialsAreCounted(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)

	var before struct {
		Egress struct {
			Denials int `json:"denials"`
		} `json:"egress"`
	}
	call{T: t, D: d, Method: "GET", Path: "/readyz"}.mustOK(&before)

	for _, target := range []string{
		"http://blocked-one.invalid/",
		"http://blocked-two.invalid/",
	} {
		var e apiError
		call{T: t, D: d, Method: "POST", Path: s.path("/navigate"), Body: map[string]any{"url": target}}.json(&e)
	}

	var after struct {
		Egress struct {
			Denials int  `json:"denials"`
			Guard   bool `json:"guardActive"`
		} `json:"egress"`
	}
	call{T: t, D: d, Method: "GET", Path: "/readyz"}.mustOK(&after)
	if !after.Egress.Guard {
		t.Error("readyz does not report the egress guard as active")
	}
	if after.Egress.Denials <= before.Egress.Denials {
		t.Errorf("denial counter did not advance (%d → %d)", before.Egress.Denials, after.Egress.Denials)
	}
}

// TestScreenshotIsPNG checks the bytes are a real image, since a truncated or
// empty body would still decode as a successful JSON envelope elsewhere.
func TestScreenshotIsPNG(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)
	s.navigate(t, d, d.fixtureURL(t, "/app"))

	status, body := call{T: t, D: d, Method: "GET", Path: s.path("/screenshot")}.do()
	if status != 200 {
		t.Fatalf("screenshot gave %d: %s", status, body)
	}
	if len(body) < 1000 {
		t.Fatalf("screenshot is only %d bytes", len(body))
	}
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if !bytes.HasPrefix(body, png) {
		t.Fatalf("body does not start with the PNG signature: % x", body[:8])
	}
	// IHDR is the first chunk and carries the dimensions.
	if !bytes.Contains(body[:33], []byte("IHDR")) {
		t.Error("no IHDR chunk; the PNG is not well formed")
	}
	// A 1920-wide persona must not produce an 800x600 screenshot, which is what
	// an unpainted or wrongly sized viewport gives.
	w := int(body[16])<<24 | int(body[17])<<16 | int(body[18])<<8 | int(body[19])
	h := int(body[20])<<24 | int(body[21])<<16 | int(body[22])<<8 | int(body[23])
	if w < 1280 || h < 600 {
		t.Errorf("screenshot is %dx%d, smaller than the persona viewport", w, h)
	}
}

// TestNavigateWaitModes covers the two documented completion semantics.
func TestNavigateWaitModes(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)

	var commit struct {
		URL        string `json:"url"`
		Generation int64  `json:"generation"`
	}
	call{T: t, D: d, Method: "POST", Path: s.path("/navigate"), Body: map[string]any{
		"url": d.fixtureURL(t, "/form"), "waitUntil": "commit",
	}}.mustOK(&commit)
	if commit.Generation == 0 {
		t.Error("commit navigation did not bump the generation")
	}

	var loaded struct {
		URL        string `json:"url"`
		Generation int64  `json:"generation"`
	}
	call{T: t, D: d, Method: "POST", Path: s.path("/navigate"), Body: map[string]any{
		"url": d.fixtureURL(t, "/app"), "waitUntil": "load",
	}}.mustOK(&loaded)
	if loaded.Generation <= commit.Generation {
		t.Errorf("load navigation did not supersede the previous document (%d → %d)",
			commit.Generation, loaded.Generation)
	}

	var e apiError
	status := call{T: t, D: d, Method: "POST", Path: s.path("/navigate"), Body: map[string]any{
		"url": d.fixtureURL(t, "/form"), "waitUntil": "networkidle",
	}}.json(&e)
	if status != 400 || e.Code != "INVALID_REQUEST" {
		t.Errorf("an unknown waitUntil gave %d %s", status, e.Code)
	}
}

// TestNavigateTimeout checks a slow page is bounded rather than hanging forever.
func TestNavigateTimeout(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)

	var e apiError
	status := call{T: t, D: d, Method: "POST", Path: s.path("/navigate"), Body: map[string]any{
		"url": d.fixtureURL(t, "/slow?ms=5000"), "timeoutMs": 300,
	}}.json(&e)
	if status != 504 && status != 409 {
		t.Fatalf("a slow page gave %d %s (%s)", status, e.Code, e.Message)
	}
	// Whatever the outcome, the daemon must still serve.
	health := call{T: t, D: d, Method: "GET", Path: "/healthz"}
	if st, _ := health.do(); st != 200 {
		t.Fatalf("healthz gave %d after a timeout", st)
	}
}

// TestWSAndSubresources exercises the proxy paths that are not plain HTTP.
func TestWSAndSubresources(t *testing.T) {
	t.Parallel()
	d := startDaemon(t)
	s := newSession(t, d)

	s.navigate(t, d, d.fixtureURL(t, "/subresource"))
	snap := s.snapshot(t, d, "")
	if !strings.Contains(snap.Text, "sub-ok") {
		t.Fatalf("subresources did not load through the proxy; snapshot:\n%s", snap.Text)
	}

	s.navigate(t, d, d.fixtureURL(t, "/ws"))
	if snap := s.snapshot(t, d, ""); snap.Text == "" {
		t.Fatal("the websocket page produced no snapshot")
	}
}
