// Package fixtures provides deterministic, self-contained web pages and
// controlled network endpoints for real-Chromium end-to-end tests of the
// browser automation daemon.
//
// Everything is served from an httptest.Server on 127.0.0.1: no external DNS,
// no outbound traffic, no randomness beyond the storage cookie value, and no
// timers that outlive a request. Every page is plain semantic HTML with a
// <title> and distinct accessible names, because the daemon's accessibility
// snapshots are asserted against it.
//
// Typical use:
//
//	srv := fixtures.New()
//	defer srv.Close()
//	// point the daemon at srv.URL + "/form"
//	if srv.Hit.Count("form") == 0 { t.Fatal("form was never fetched") }
package fixtures

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Server wraps one httptest.Server hosting every fixture route.
type Server struct {
	// URL is the base URL without a trailing slash, e.g. http://127.0.0.1:PORT.
	URL string
	// Hit counts requests per route ("form", "form/submit", "ws/echo", ...).
	Hit *HitLog
	// Denied observes requests that a blocked denied-host load would have
	// produced. Point SetDeniedURL at URL+"/denied" to turn a silent block
	// into an observable counter; with the default denied host nothing can
	// arrive, so a zero count proves the load was blocked.
	Denied *HitLog

	srv *httptest.Server
	h   http.Handler

	mu     sync.RWMutex
	denied string
}

const (
	// defaultDeniedURL is used until SetDeniedURL is called. The .invalid TLD
	// is guaranteed never to resolve, so loads fail silently and offline.
	defaultDeniedURL = "http://denied.invalid/x"
	// defaultDeniedBase is the origin of the denied host; denied paths are
	// appended to it so /i.png, /s.js and /s.css are distinct targets.
	defaultDeniedBase = "http://denied.invalid"

	// maxSlowMillis caps /slow so a fixture cannot pin a daemon worker.
	maxSlowMillis = 5000
	// defaultSlowMillis is used when /slow has no ms parameter.
	defaultSlowMillis = 500
	// defaultBigChars is the default body size for /big.
	defaultBigChars = 20000
	// maxBigChars caps /big so a fixture cannot exhaust memory.
	maxBigChars = 5 << 20

	// storageLocalValue is the deterministic value written to localStorage
	// and mirrored into sessionStorage/IndexedDB.
	storageLocalValue   = "fixture-value"
	storageSessionValue = "session-value"
	storageIDBValue     = "fixture-idb"
	swCacheName         = "fixture-v1"
)

// New starts the fixture server. Caller must Close it.
func New() *Server {
	s := &Server{Hit: newHitLog(), Denied: newHitLog()}
	s.h = s.buildHandler()
	s.srv = httptest.NewServer(s.h)
	s.URL = strings.TrimRight(s.srv.URL, "/")
	return s
}

// NewTLS starts the https variant with httptest's self-signed certificate.
// Caller must Close it.
func NewTLS() *Server {
	s := &Server{Hit: newHitLog(), Denied: newHitLog()}
	s.h = s.buildHandler()
	s.srv = httptest.NewTLSServer(s.h)
	s.URL = strings.TrimRight(s.srv.URL, "/")
	return s
}

// Close shuts the underlying server down. Safe to call more than once.
func (s *Server) Close() {
	if s == nil || s.srv == nil {
		return
	}
	s.srv.Close()
}

// Handler returns the http.Handler so tests can mount it on their own listener.
func (s *Server) Handler() http.Handler { return s.h }

// SetDeniedURL points the denied redirect/subresource variants at u. Passing an
// empty string restores the default http://denied.invalid/x.
func (s *Server) SetDeniedURL(u string) {
	s.mu.Lock()
	s.denied = strings.TrimRight(strings.TrimSpace(u), "/")
	s.mu.Unlock()
}

// DeniedURL returns the current denied target base.
func (s *Server) DeniedURL() string {
	s.mu.RLock()
	u := s.denied
	s.mu.RUnlock()
	if u == "" {
		return defaultDeniedBase
	}
	return u
}

// deniedURL is the full denied target path suffix (default .../x).
func (s *Server) deniedTarget(path string) string {
	s.mu.RLock()
	base := s.denied
	s.mu.RUnlock()
	if base == "" {
		return defaultDeniedBase + path
	}
	return base + path
}

// buildHandler wires every route and the hit-counting middleware.
func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/form", s.handleForm)
	mux.HandleFunc("/form/submit", s.handleFormSubmit)
	mux.HandleFunc("/spa", s.handleSPA)
	mux.HandleFunc("/spa/state", s.handleSPAState)
	mux.HandleFunc("/app", s.handleApp)
	mux.HandleFunc("/app/click", s.handleAppClick)
	mux.HandleFunc("/storage", s.handleStorage)
	mux.HandleFunc("/storage/read", s.handleStorageRead)
	mux.HandleFunc("/sw.js", s.handleServiceWorker)
	mux.HandleFunc("/redirect/ok", s.handleRedirectOK)
	mux.HandleFunc("/redirect/denied", s.handleRedirectDenied)
	mux.HandleFunc("/redirect/chain", s.handleRedirectChain)
	mux.HandleFunc("/subresource", s.handleSubresource)
	mux.HandleFunc("/subresource/denied", s.handleSubresourceDenied)
	mux.HandleFunc("/img.png", s.handleImage)
	mux.HandleFunc("/sub.js", s.handleSubJS)
	mux.HandleFunc("/sub.css", s.handleSubCSS)
	mux.HandleFunc("/ws", s.handleWSPage)
	mux.HandleFunc("/ws/echo", s.handleWSEcho)
	mux.HandleFunc("/headers", s.handleHeaders)
	mux.HandleFunc("/headers/tls", s.handleHeadersTLS)
	mux.HandleFunc("/slow", s.handleSlow)
	mux.HandleFunc("/frames", s.handleFrames)
	mux.HandleFunc("/frame/inner", s.handleFrameInner)
	mux.HandleFunc("/dialog", s.handleDialog)
	mux.HandleFunc("/popup", s.handlePopup)
	mux.HandleFunc("/download", s.handleDownload)
	mux.HandleFunc("/download/file", s.handleDownloadFile)
	mux.HandleFunc("/perm", s.handlePerm)
	mux.HandleFunc("/perm/geo", s.handlePermGeo)
	mux.HandleFunc("/big", s.handleBig)
	// /denied/ is the observation surface: when SetDeniedURL points here, any
	// request that arrives proves a denied load actually escaped the daemon.
	mux.HandleFunc("/denied/", s.handleDeniedProbe)

	return s.withHits(mux)
}

// withHits records per-path counters before dispatching.
//
// /ws/echo is excluded: its handshake is not the event under test, each
// successfully echoed frame is (see handleWSEcho), and a hijacked connection
// must not be counted twice.
func (s *Server) withHits(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "/"
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/ws/echo"):
		case strings.HasPrefix(r.URL.Path, "/denied/"):
			s.Denied.AddMethod(r.Method, p)
		default:
			s.Hit.AddMethod(r.Method, p)
		}
		next.ServeHTTP(w, r)
	})
}

// handleDeniedProbe answers requests that reached an observation surface.
func (s *Server) handleDeniedProbe(w http.ResponseWriter, r *http.Request) {
	// Already counted by withHits; this handler only answers.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("denied-probe reachable\n"))
}
