package detect

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDetectServerEndpoints(t *testing.T) {
	srv := NewServer()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. GET /healthz
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz failed: err=%v status=%v", err, resp)
	}
	_ = resp.Body.Close()

	// 2. GET / serves probe page HTML
	respIdx, err := http.Get(ts.URL + "/")
	if err != nil || respIdx.StatusCode != http.StatusOK {
		t.Fatalf("GET / failed: err=%v", err)
	}
	_ = respIdx.Body.Close()
	if !strings.Contains(ProbePageHTML, "__runDetectProbes") {
		t.Fatal("ProbeHTML missing __runDetectProbes entrypoint")
	}

	// 3. POST /api/report stores and classifies report; GET /api/latest returns it
	rep := Report{
		Label:          "unit-test",
		SuspicionScore: 0,
		MaxScore:       100,
		Classification: "HUMAN_DESKTOP_WIN11",
		PassedCount:    28,
	}
	b, _ := json.Marshal(rep)
	postResp, err := http.Post(ts.URL+"/api/report", "application/json", bytes.NewReader(b))
	if err != nil || postResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/report failed: err=%v", err)
	}
	_ = postResp.Body.Close()

	latest := srv.Latest()
	if latest == nil || latest.Label != "unit-test" || latest.Classification != "HUMAN_DESKTOP_WIN11" {
		t.Fatalf("unexpected Latest() report: %+v", latest)
	}
}
