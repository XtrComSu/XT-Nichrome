// Package bench measures what this service costs on top of talking to Chromium
// directly.
//
// The comparison is deliberately unflattering: the baseline is a bare CDP pipe
// with no policy proxy, no per-page persona injection and no accessibility tree
// rendering, which is the cheapest thing that can drive a page. Anything the
// service adds shows up as the delta.
package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/cdp"
	"github.com/xtrcom/xt-nichrome/internal/fixtures"
)

// browserPath locates the pinned Chromium, preferring an explicit override.
func browserPath(tb testing.TB) string {
	tb.Helper()
	if p := os.Getenv("XT_NICHROME_BROWSER"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		tb.Fatalf("home: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".cache/ms-playwright/chromium-*/chrome-linux/chrome"))
	if len(matches) == 0 {
		tb.Skip("no Chromium under ~/.cache/ms-playwright")
	}
	// The digit-bearing revisions sort correctly as strings only within one
	// magnitude; pick the numerically largest revision to be safe.
	sort.Slice(matches, func(i, j int) bool { return revOf(matches[i]) > revOf(matches[j]) })
	return matches[0]
}

func revOf(path string) int {
	base := filepath.Base(filepath.Dir(filepath.Dir(path)))
	var n int
	if _, err := fmt.Sscanf(base, "chromium-%d", &n); err != nil {
		return 0
	}
	return n
}

// stats summarises a latency series the way an operator reads it.
type stats struct {
	N      int
	Median time.Duration
	P95    time.Duration
	Min    time.Duration
	Max    time.Duration
	Mean   time.Duration
}

func summarise(d []time.Duration) stats {
	if len(d) == 0 {
		return stats{}
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	var total time.Duration
	for _, v := range s {
		total += v
	}
	return stats{
		N:      len(s),
		Median: s[len(s)/2],
		P95:    s[int(float64(len(s)-1)*0.95)],
		Min:    s[0],
		Max:    s[len(s)-1],
		Mean:   total / time.Duration(len(s)),
	}
}

func (s stats) String() string {
	return fmt.Sprintf("n=%d median=%v p95=%v min=%v max=%v mean=%v",
		s.N, round(s.Median), round(s.P95), round(s.Min), round(s.Max), round(s.Mean))
}

func round(d time.Duration) time.Duration { return d.Round(100 * time.Microsecond) }

// TestBaselineDirectCDP measures the floor: launch, navigate, tree fetch.
func TestBaselineDirectCDP(t *testing.T) {
	bin := browserPath(t)
	fx := fixtures.New()
	defer fx.Close()

	tmp := t.TempDir()
	// Launch returns as soon as the pipe is wired; the process may not have
	// executed a single instruction yet. Timing to first successful CDP call is
	// the only honest cold-start number, so the clock runs until getVersion
	// answers rather than until Launch returns.
	start := time.Now()
	proc, err := cdp.Launch(cdp.Options{
		ExecPath: bin,
		Args: []string{
			"--headless=new", "--no-sandbox", "--disable-gpu",
			"--user-data-dir=" + filepath.Join(tmp, "profile"),
			"--no-first-run", "--no-default-browser-check",
		},
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer proc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := proc.Call(ctx, "", "Browser.getVersion", nil, nil); err != nil {
		t.Fatalf("getVersion: %v", err)
	}
	launchCost := time.Since(start)
	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := proc.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &target); err != nil {
		t.Fatalf("createTarget: %v", err)
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := proc.Call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": target.TargetID, "flatten": true,
	}, &attached); err != nil {
		t.Fatalf("attach: %v", err)
	}
	sid := attached.SessionID
	for _, m := range []string{"Page.enable", "Accessibility.enable", "Runtime.enable"} {
		if err := proc.Call(ctx, sid, m, nil, nil); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
	}

	const runs = 30
	var navs, trees []time.Duration
	for i := 0; i < runs; i++ {
		mark := time.Now()
		if err := proc.Call(ctx, sid, "Page.navigate", map[string]any{"url": fx.URL + "/form"}, nil); err != nil {
			t.Fatalf("navigate: %v", err)
		}
		for {
			var ready struct {
				Result struct {
					Value string `json:"value"`
				} `json:"result"`
			}
			if err := proc.Call(ctx, sid, "Runtime.evaluate", map[string]any{
				"expression": "document.readyState", "returnByValue": true,
			}, &ready); err == nil && ready.Result.Value == "complete" {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		navs = append(navs, time.Since(mark))

		mark = time.Now()
		var tree struct {
			Nodes []json.RawMessage `json:"nodes"`
		}
		if err := proc.Call(ctx, sid, "Accessibility.getFullAXTree", map[string]any{}, &tree); err != nil {
			t.Fatalf("getFullAXTree: %v", err)
		}
		trees = append(trees, time.Since(mark))
	}

	t.Logf("baseline: direct CDP, no policy proxy, no persona, no ref rendering")
	t.Logf("  cold launch      %v", round(launchCost))
	t.Logf("  navigate+load    %s", summarise(navs))
	t.Logf("  ax tree fetch    %s", summarise(trees))
	t.Logf("  chrom RSS        %s", rssOf(proc.PID()))
}

// TestServiceRoundTrip measures the same operations through the HTTP API.
func TestServiceRoundTrip(t *testing.T) {
	if os.Getenv("XT_BENCH_SERVICE") == "" {
		t.Skip("set XT_BENCH_SERVICE=1 to build and launch the daemon")
	}
	bin := browserPath(t)
	fx := fixtures.New()
	defer fx.Close()

	daemonBin := buildDaemon(t)
	state := t.TempDir()
	token := "bench-token-with-enough-length"
	listen := "127.0.0.1:9788"

	cmd := exec.Command(daemonBin,
		"-listen", listen, "-browser", bin,
		"-headless", "-state-dir", state, "-token", token,
		"-allow", fx.URL, "-allow-private", "-log-level", "error",
	)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	defer func() {
		cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			cmd.Process.Kill()
		}
	}()
	base := "http://" + listen
	waitReady(t, base, token)

	cli := &apiClient{t: t, base: base, token: token}

	coldStart := time.Now()
	session := cli.post("/v1/sessions", map[string]any{})
	tab := cli.post("/v1/sessions/"+str(session["sessionId"])+"/tabs", map[string]any{})
	coldCost := time.Since(coldStart)

	sid, tid := str(session["sessionId"]), str(tab["tabId"])
	tabBase := "/v1/sessions/" + sid + "/tabs/" + tid

	const runs = 30
	var navs, snaps []time.Duration
	for i := 0; i < runs; i++ {
		mark := time.Now()
		cli.post(tabBase+"/navigate", map[string]any{"url": fx.URL + "/form"})
		navs = append(navs, time.Since(mark))

		mark = time.Now()
		cli.get(tabBase + "/snapshot")
		snaps = append(snaps, time.Since(mark))
	}

	t.Logf("service: HTTP API, policy proxy on, persona injected, refs rendered")
	t.Logf("  cold session+tab %v", round(coldCost))
	t.Logf("  navigate+load    %s", summarise(navs))
	t.Logf("  snapshot         %s", summarise(snaps))
	t.Logf("  daemon RSS       %s", rssOf(cmd.Process.Pid))
}

func buildDaemon(tb testing.TB) string {
	tb.Helper()
	out := filepath.Join(tb.TempDir(), "nichrome")
	cmd := exec.Command("go", "build", "-o", out, "../cmd/nichrome")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		tb.Fatalf("build daemon: %v", err)
	}
	return out
}

func waitReady(tb testing.TB, base, token string) {
	tb.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest("GET", base+"/readyz", nil)
		if err != nil {
			tb.Fatalf("request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	tb.Fatal("daemon did not become ready")
}

// rssOf reads the resident set size, the number that decides how many sessions
// fit on a host.
func rssOf(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "VmRSS:"))
		}
	}
	return "unknown"
}

type apiClient struct {
	t     testing.TB
	base  string
	token string
}

func (c *apiClient) post(path string, body any) map[string]any {
	c.t.Helper()
	return c.do("POST", path, body)
}

func (c *apiClient) get(path string) map[string]any {
	c.t.Helper()
	return c.do("GET", path, nil)
}

func (c *apiClient) do(method, path string, body any) map[string]any {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		c.t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		c.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, data)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		c.t.Fatalf("%s %s: decode %q: %v", method, path, data, err)
	}
	return out
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
