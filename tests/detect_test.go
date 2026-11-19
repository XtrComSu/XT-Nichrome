package tests

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/cdp"
	"github.com/xtrcom/xt-nichrome/internal/detect"
)

// runStockHeadless launches the same pinned Chromium binary with stock
// --headless=new flags (no XT-Nichrome persona, no stealth init script, no
// fontconfig jail) and collects the detection report it posts to srv.
func runStockHeadless(t *testing.T, detectURL string, srv *detect.Server) *detect.Report {
	t.Helper()
	conn, err := cdp.Launch(cdp.Options{
		ExecPath: browserPath(t),
		Args: []string{
			"--headless=new",
			"--user-data-dir=" + t.TempDir(),
			"--no-first-run",
			"--no-default-browser-check",
			"--mute-audio",
			"about:blank",
		},
	})
	if err != nil {
		t.Fatalf("launch stock headless chromium: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := conn.Call(ctx, "", "Target.createTarget", map[string]any{"url": detectURL}, &created); err != nil {
		t.Fatalf("stock createTarget: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if rep := srv.Latest(); rep != nil {
			return rep
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("stock headless chromium did not post a detection report within 15s")
	return nil
}

// TestAdversarialDetectionSuite compares stock headless Chromium against
// XT-Nichrome across all 9 probe categories (classic surface, high-entropy
// UA-CH, 60+ WebGL1/WebGL2 parameters, fontconfig jail + differential font
// widths, OfflineAudioContext sample hash, 2D canvas hash, permissions/media/
// speech/Intl, Function.prototype.toString integrity, and behavioral mouse/key
// virtualization).
func TestAdversarialDetectionSuite(t *testing.T) {
	t.Parallel()

	// 1. Measure stock headless Chromium against the detection server.
	stockDetect := detect.NewServer()
	stockHTTP := httptest.NewServer(stockDetect.Handler())
	t.Cleanup(stockHTTP.Close)

	stockRep := runStockHeadless(t, stockHTTP.URL, stockDetect)
	if stockRep.Classification != "HEADLESS_LINUX_BOT" {
		t.Fatalf("expected stock headless Chromium to be classified as HEADLESS_LINUX_BOT, got %q (score %d)",
			stockRep.Classification, stockRep.SuspicionScore)
	}
	if stockRep.SuspicionScore < 50 {
		t.Fatalf("expected stock headless suspicion score >= 50, got %d", stockRep.SuspicionScore)
	}
	if stockRep.FailedCount < 8 {
		t.Fatalf("expected stock headless to fail >= 8 probes, failed %d (%v)",
			stockRep.FailedCount, stockRep.FailedProbes)
	}

	// 2. Measure XT-Nichrome against the detection server, including humanized
	// fill and click actions.
	niDetect := detect.NewServer()
	niHTTP := httptest.NewServer(niDetect.Handler())
	t.Cleanup(niHTTP.Close)

	d := startDaemon(t, "-allow", niHTTP.URL)
	s := newSession(t, d)
	s.navigate(t, d, niHTTP.URL+"/")

	var initialRep *detect.Report
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if rep := niDetect.Latest(); rep != nil {
			initialRep = rep
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if initialRep == nil {
		t.Fatal("XT-Nichrome did not post an initial detection report within 15s")
	}
	if initialRep.SuspicionScore != 0 || initialRep.FailedCount != 0 {
		t.Fatalf("XT-Nichrome initial report had suspicionScore=%d failed=%v",
			initialRep.SuspicionScore, initialRep.FailedProbes)
	}
	if initialRep.Classification != "HUMAN_DESKTOP_WIN11" {
		t.Fatalf("XT-Nichrome classification = %q, want HUMAN_DESKTOP_WIN11", initialRep.Classification)
	}
	if initialRep.WebGLParamCnt < 60 {
		t.Fatalf("webglParamCount = %d, want >= 60", initialRep.WebGLParamCnt)
	}

	// 3. Drive humanized fill + click on the probe page and verify behavioral
	// telemetry passes every human-input invariant.
	snap := s.snapshot(t, d, "")
	inputRef := findRef(t, snap.Text, "textbox", "Behavioral Input Probe")
	btnRef := findRef(t, snap.Text, "button", "Verify Human Action")

	var filled actionResult
	call{T: t, D: d, Method: "POST", Path: s.path("/actions"), Body: map[string]any{
		"kind":       "fill",
		"snapshotId": snap.SnapshotID,
		"ref":        inputRef,
		"text":       "Ni-28 Human Check!",
	}}.mustOK(&filled)

	var clicked actionResult
	call{T: t, D: d, Method: "POST", Path: s.path("/actions"), Body: map[string]any{
		"kind":       "click",
		"snapshotId": snap.SnapshotID,
		"ref":        btnRef,
	}}.mustOK(&clicked)

	var actionRep *detect.Report
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if rep := niDetect.Latest(); rep != nil && rep.Label == "after-action" {
			actionRep = rep
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if actionRep == nil {
		t.Fatal("did not receive after-action detection report after clicking #probe-btn")
	}
	if actionRep.SuspicionScore != 0 || actionRep.FailedCount != 0 {
		t.Fatalf("XT-Nichrome after-action report failed probes: score=%d failed=%v",
			actionRep.SuspicionScore, actionRep.FailedProbes)
	}

	foundBehaviorProbe := false
	for _, p := range actionRep.Probes {
		if p.ID == "behavior_human_pointer_and_keys" {
			foundBehaviorProbe = true
			if !p.Passed {
				t.Fatalf("behavior_human_pointer_and_keys failed: observed=%+v", p.Observed)
			}
		}
	}
	if !foundBehaviorProbe {
		t.Fatal("behavior_human_pointer_and_keys probe was not evaluated in after-action report")
	}
	if gotInputs, _ := actionRep.Behavior["inputs"].(float64); int(gotInputs) != len("Ni-28 Human Check!") {
		t.Fatalf("expected %d input events for typed string, got %v (behavior=%+v)",
			len("Ni-28 Human Check!"), actionRep.Behavior["inputs"], actionRep.Behavior)
	}

	t.Logf("Detection Summary:\n"+
		"  Stock Headless : score=%d/100 class=%s passed=%d failed=%d (%v)\n"+
		"  XT-Nichrome    : score=%d/100 class=%s passed=%d failed=%d webglParams=%d canvasHash=%s audioHash=%s\n"+
		"  Behavior       : %+v",
		stockRep.SuspicionScore, stockRep.Classification, stockRep.PassedCount, stockRep.FailedCount, stockRep.FailedProbes,
		actionRep.SuspicionScore, actionRep.Classification, actionRep.PassedCount, actionRep.FailedCount,
		actionRep.WebGLParamCnt, actionRep.CanvasHash, actionRep.AudioHash, actionRep.Behavior)
}
