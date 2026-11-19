// Package tests runs the service against a real Chromium.
//
// These are not mocks: every test launches the pinned browser binary, drives it
// over the CDP pipe, and asserts on what the page actually reports. A test that
// passes only because a stub agreed with itself would be worthless here, since
// the whole point is that the browser cannot tell it is being automated.
package tests

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// daemon is a running service instance for one test.
type daemon struct {
	t       *testing.T
	base    string
	token   string
	cmd     *exec.Cmd
	state   string
	logPath string
	worker  http.Handler

	fixtureBase string
	fx          *fixture

	// denied is a second fixture server on its own port. Its origin is NOT in
	// the allowlist, which is the only way to observe enforcement: pointing a
	// denied target back at the allowed origin would make it allowed, and
	// pointing it at an unresolvable host makes a zero hit count prove nothing.
	denied *fixture
}

// browserPath resolves the pinned Chromium, failing the test with a clear
// message rather than silently skipping.
func browserPath(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("XT_NICHROME_BROWSER"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot resolve the home directory: %v", err)
	}
	base := filepath.Join(home, ".cache", "ms-playwright")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("no pinned Chromium under %s; run: npx playwright@1.56.1 install chromium (%v)", base, err)
	}
	best, bestRev := "", -1
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "chromium-") || strings.Contains(e.Name(), "headless") {
			continue
		}
		var rev int
		if _, err := fmt.Sscanf(strings.TrimPrefix(e.Name(), "chromium-"), "%d", &rev); err != nil {
			continue
		}
		cand := filepath.Join(base, e.Name(), "chrome-linux", "chrome")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() && rev > bestRev {
			best, bestRev = cand, rev
		}
	}
	if best == "" {
		t.Fatalf("no usable Chromium build under %s", base)
	}
	return best
}

// startDaemon launches the built binary and waits for /healthz.
func startDaemon(t *testing.T, extraArgs ...string) *daemon {
	t.Helper()
	bin := buildBinary(t, "./cmd/nichrome")
	fx := newFixture(t)
	state := t.TempDir()

	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	logPath := filepath.Join(state, "daemon.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	args := append([]string{
		"-listen", addr,
		"-state-dir", state,
		"-token", "test-token-with-enough-length",
		"-allow", fx.URL,
		"-allow-private",
		"-browser", browserPath(t),
		"-log-level", "debug",
		"-idle-timeout", "0",
	}, extraArgs...)
	cmd := exec.Command(bin, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	d := &daemon{
		t: t, base: "http://" + addr, token: "test-token-with-enough-length",
		cmd: cmd, state: state, logPath: logPath, worker: fx.Server.Handler(),
		fixtureBase: fx.URL, fx: fx, denied: newFixture(t),
	}
	// Route the fixture's blocked-resource pages at the unlisted origin.
	fx.Server.SetDeniedURL(d.denied.URL + "/denied")
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			done := make(chan struct{})
			go func() { _, _ = cmd.Process.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		}
		logFile.Close()
		if t.Failed() {
			if data, err := os.ReadFile(logPath); err == nil {
				t.Logf("daemon log (tail):\n%s", tail(string(data), 4000))
			}
		}
	})

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", d.base+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return d
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("daemon did not become healthy in 90s; log:\n%s", tail(readFile(logPath), 4000))
	return nil
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick a port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

var (
	buildMu       sync.Mutex
	buildCache    = map[string]string{}
	buildCacheKey = map[string]string{}
)

// buildBinary builds a package once per test binary run.
func buildBinary(t *testing.T, pkg string) string {
	t.Helper()
	buildMu.Lock()
	defer buildMu.Unlock()
	// Caching by package name alone means a stale binary survives a source edit,
	// which silently tests the old daemon. Key on the package plus the newest
	// source mtime in the tree.
	key := pkg + "@" + newestSourceTime(t)
	if p, ok := buildCache[pkg]; ok && buildCacheKey[pkg] == key {
		return p
	}
	dir := os.Getenv("XT_NICHROME_BUILD_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "xt-nichrome-build")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("build dir: %v", err)
	}
	name := filepath.Base(pkg)
	out := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = repoRoot(t)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, combined)
	}
	buildCache[pkg] = out
	buildCacheKey[pkg] = key
	return out
}

// newestSourceTime summarises the freshest source revision so a cached binary is
// discarded when anything that could have produced it changed.
func newestSourceTime(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	var newest int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if name := info.Name(); name == ".git" || name == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".js") && !strings.HasSuffix(path, ".mod") {
			return nil
		}
		if mt := info.ModTime().UnixNano(); mt > newest {
			newest = mt
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return strconv.FormatInt(newest, 10)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Dir(wd)
}

// call is one API request.
type call struct {
	T      *testing.T
	D      *daemon
	Method string
	Path   string
	Body   any
	Header map[string]string
}

// do performs the request and returns status and raw body.
func (c call) do() (int, []byte) {
	c.T.Helper()
	var rdr io.Reader
	if c.Body != nil {
		buf, err := json.Marshal(c.Body)
		if err != nil {
			c.T.Fatalf("marshal body: %v", err)
		}
		rdr = strings.NewReader(string(buf))
	}
	req, err := http.NewRequest(c.Method, c.D.base+c.Path, rdr)
	if err != nil {
		c.T.Fatalf("build request: %v", err)
	}
	if c.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.Header {
		req.Header.Set(k, v)
	}
	if _, ok := c.Header["Authorization"]; !ok {
		req.Header.Set("Authorization", "Bearer "+c.D.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.T.Fatalf("%s %s: %v", c.Method, c.Path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.T.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, data
}

// json performs the request and decodes into out.
func (c call) json(out any) int {
	c.T.Helper()
	status, data := c.do()
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			c.T.Fatalf("decode %s %s (%d): %v\nbody: %s", c.Method, c.Path, status, err, data)
		}
	}
	return status
}

// mustOK performs the request and fails on a non-2xx status.
func (c call) mustOK(out any) int {
	c.T.Helper()
	status := c.json(out)
	if status < 200 || status > 299 {
		_, data := c.do()
		c.T.Fatalf("%s %s: status %d\nbody: %s", c.Method, c.Path, status, data)
	}
	return status
}

// rawCall sends an exact request body, bypassing encoding, so malformed and
// duplicate-key payloads can be exercised.
func rawCall(t *testing.T, d *daemon, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, d.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, data
}

// session creates a session and its first tab, closing both at cleanup.
type session struct {
	ID  string
	Tab string
}

func newSession(t *testing.T, d *daemon) session {
	t.Helper()
	var s struct {
		ID string `json:"sessionId"`
	}
	call{T: t, D: d, Method: "POST", Path: "/v1/sessions", Body: map[string]any{}}.mustOK(&s)
	if s.ID == "" {
		t.Fatal("session id was empty")
	}
	t.Cleanup(func() {
		call{T: t, D: d, Method: "DELETE", Path: "/v1/sessions/" + s.ID}.do()
	})
	var tab struct {
		TabID string `json:"tabId"`
	}
	call{T: t, D: d, Method: "POST", Path: "/v1/sessions/" + s.ID + "/tabs", Body: map[string]any{}}.mustOK(&tab)
	return session{ID: s.ID, Tab: tab.TabID}
}

func (s session) path(suffix string) string {
	return "/v1/sessions/" + s.ID + "/tabs/" + s.Tab + suffix
}

// navigate drives the tab and fails on error.
func (s session) navigate(t *testing.T, d *daemon, url string) {
	t.Helper()
	var out struct {
		URL string `json:"url"`
	}
	call{T: t, D: d, Method: "POST", Path: s.path("/navigate"), Body: map[string]any{"url": url}}.mustOK(&out)
}

// snapshot takes a snapshot and returns the wire form.
func (s session) snapshot(t *testing.T, d *daemon, query string) snapshotResult {
	t.Helper()
	var out snapshotResult
	call{T: t, D: d, Method: "GET", Path: s.path("/snapshot" + query)}.mustOK(&out)
	return out
}

// fixtureURL builds an absolute URL against the fixture server this daemon
// was started with.
func (d *daemon) fixtureURL(t *testing.T, path string) string {
	t.Helper()
	if d.fixtureBase == "" {
		t.Fatal("daemon has no fixture base URL")
	}
	return d.fixtureBase + path
}

// sawSubmit reports whether the fixture recorded a form submission.
//
// The fixture counts hits rather than stashing values, which is the right
// trade: a soak test cannot grow its memory, and a successful POST to
// /form/submit is only possible if the real form was actually submitted (a
// rejected or never-filled field still posts, but a click that did nothing
// does not post at all).
func (d *daemon) sawSubmit() bool {
	return d.fx.Server.Hit.MethodCount("POST", "form/submit") > 0
}

// workerBase forces the worker handler to exist (used by tests that only need
// the side effect of having a fixture).
func (d *daemon) workerBase() {}
