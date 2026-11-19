// Package detect implements an adversarial browser-fingerprinting and
// behavioral-telemetry probe suite. It powers both the standalone detection
// server (cmd/detect-server) and the end-to-end detection test harness
// (tests/detect_test.go).
package detect

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// ProbeResult is one named check in the adversarial detection report.
type ProbeResult struct {
	ID       string `json:"id"`
	Category string `json:"category"` // surface | uach | webgl | fonts | audio | canvas | permissions | integrity | behavior | network
	Passed   bool   `json:"passed"`
	Weight   int    `json:"weight"`
	Observed any    `json:"observed,omitempty"`
	Expected string `json:"expected,omitempty"`
	Note     string `json:"note,omitempty"`
}

// ConnectionInfo records what the HTTP server directly observed on the wire.
type ConnectionInfo struct {
	Proto          string            `json:"proto"`
	TLS            bool              `json:"tls"`
	ALPN           string            `json:"alpn,omitempty"`
	CipherSuite    string            `json:"cipherSuite,omitempty"`
	RemoteAddr     string            `json:"remoteAddr"`
	UserAgent      string            `json:"userAgent"`
	AcceptLanguage string            `json:"acceptLanguage"`
	SecCHUA        string            `json:"secChUa,omitempty"`
	SecCHPlatform  string            `json:"secChUaPlatform,omitempty"`
	SecCHMobile    string            `json:"secChUaMobile,omitempty"`
	Headers        map[string]string `json:"headers"`
	HonestNote     string            `json:"honestNote"`
}

// Report is the complete detection report submitted by the probe page or
// evaluated via CDP.
type Report struct {
	Timestamp      string         `json:"timestamp"`
	Label          string         `json:"label,omitempty"`
	SuspicionScore int            `json:"suspicionScore"`
	MaxScore       int            `json:"maxScore"`
	Classification string         `json:"classification"` // "HUMAN_DESKTOP_WIN11" | "SUSPICIOUS" | "HEADLESS_LINUX_BOT"
	PassedCount    int            `json:"passedCount"`
	FailedCount    int            `json:"failedCount"`
	Probes         []ProbeResult  `json:"probes"`
	FailedProbes   []string       `json:"failedProbes,omitempty"`
	CanvasHash     string         `json:"canvasHash"`
	AudioHash      string         `json:"audioHash"`
	WebGLParamCnt  int            `json:"webglParamCount"`
	FontSummary    map[string]any `json:"fontSummary,omitempty"`
	Behavior       map[string]any `json:"behavior,omitempty"`
	Connection     ConnectionInfo `json:"connection"`
	HonestLimits   []string       `json:"honestLimits"`
}

// Server serves the interactive detection probe page and stores the latest
// reports for inspection via curl or browser.
type Server struct {
	mu      sync.Mutex
	reports []*Report
	mux     *http.ServeMux
}

// NewServer constructs a detection HTTP server.
func NewServer() *Server {
	s := &Server{mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /", s.handleIndex)
	s.mux.HandleFunc("GET /api/connection", s.handleConnection)
	s.mux.HandleFunc("POST /api/report", s.handlePostReport)
	s.mux.HandleFunc("GET /api/latest", s.handleGetLatest)
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

// Latest returns the most recently submitted report, or nil.
func (s *Server) Latest() *Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reports) == 0 {
		return nil
	}
	return s.reports[len(s.reports)-1]
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) observeConnection(r *http.Request) ConnectionInfo {
	hdrs := make(map[string]string, len(r.Header))
	for k, v := range r.Header {
		if len(v) > 0 {
			hdrs[k] = v[0]
		}
	}
	info := ConnectionInfo{
		Proto:          r.Proto,
		TLS:            r.TLS != nil,
		RemoteAddr:     r.RemoteAddr,
		UserAgent:      r.UserAgent(),
		AcceptLanguage: r.Header.Get("Accept-Language"),
		SecCHUA:        r.Header.Get("Sec-Ch-Ua"),
		SecCHPlatform:  r.Header.Get("Sec-Ch-Ua-Platform"),
		SecCHMobile:    r.Header.Get("Sec-Ch-Ua-Mobile"),
		Headers:        hdrs,
		HonestNote:     "TLS ClientHello (JA3/JA4/ALPN) and TCP/IP OS stack traits are properties of the host kernel and Chromium network binary; they are not spoofed in JS.",
	}
	if r.TLS != nil {
		info.ALPN = r.TLS.NegotiatedProtocol
	}
	return info
}

func (s *Server) handleConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(s.observeConnection(r))
}

func (s *Server) handlePostReport(w http.ResponseWriter, r *http.Request) {
	var rep Report
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rep.Timestamp = time.Now().UTC().Format(time.RFC3339)
	rep.Connection = s.observeConnection(r)
	s.mu.Lock()
	s.reports = append(s.reports, &rep)
	if len(s.reports) > 32 {
		s.reports = s.reports[len(s.reports)-32:]
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(rep)
}

func (s *Server) handleGetLatest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	s.mu.Lock()
	var latest *Report
	if len(s.reports) > 0 {
		latest = s.reports[len(s.reports)-1]
	}
	count := len(s.reports)
	s.mu.Unlock()
	if latest == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":     "ready_no_browser_runs_yet",
			"reports":    0,
			"connection": s.observeConnection(r),
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"reports": count,
		"latest":  latest,
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(ProbePageHTML))
}
