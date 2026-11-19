package fixtures

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// getText fetches rawURL and returns the body plus the status code.
func getText(t *testing.T, c *http.Client, rawURL string) (int, string) {
	t.Helper()
	resp, err := c.Get(rawURL)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", rawURL, err)
	}
	return resp.StatusCode, string(b)
}

// newNoRedirectClient returns a client that never follows redirects.
func newNoRedirectClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func TestFormSubmitEcho(t *testing.T) {
	s := New()
	defer s.Close()

	form := url.Values{
		"name":     {"Ada Lovelace"},
		"email":    {"ada@example.test"},
		"password": {"hunter2"},
		"color":    {"green"},
		"agree":    {"yes"},
	}
	resp, err := s.client().PostForm(s.URL+"/form/submit", form)
	if err != nil {
		t.Fatalf("post form: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read form response: %v", err)
	}
	page := string(body)

	for _, want := range []string{
		"<!doctype html>",
		"<html lang=en>",
		"<h1>Form submitted</h1>",
		`<p id=echo>name=Ada Lovelace;email=ada@example.test;color=green;agree=yes;password=set</p>`,
		"/form/submit?ok=1",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("form response missing %q\npage:\n%s", want, page)
		}
	}
	if strings.Contains(page, "hunter2") {
		t.Errorf("form response echoed the password value")
	}
	if got := resp.Header.Get("Content-Location"); !strings.Contains(got, "ok=1") {
		t.Errorf("Content-Location = %q, want it to carry ok=1", got)
	}

	// A plain GET has to render too, with empty fields and unset password.
	status, page := getText(t, s.client(), s.URL+"/form/submit?ok=1")
	if status != http.StatusOK {
		t.Fatalf("GET /form/submit status = %d", status)
	}
	if !strings.Contains(page, "name=;email=;color=red;agree=no;password=unset") {
		t.Errorf("GET submit echo unexpected:\n%s", page)
	}
}

func TestFormPageMarkup(t *testing.T) {
	s := New()
	defer s.Close()

	_, page := getText(t, s.client(), s.URL+"/form")
	for _, want := range []string{
		"<h1>Fixture form</h1>",
		`id=name`,
		`<label for=name>Full name</label>`,
		`id=email`,
		`id=password`,
		`type=password`,
		`<label for=password>Password</label>`,
		`<select id=color`,
		`id=agree`,
		`type=checkbox`,
		`<button id=submit type=submit>Submit</button>`,
		`action="/form/submit"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("form page missing %q", want)
		}
	}
	if got := s.Hit.Count("form"); got != 1 {
		t.Errorf("Hit.Count(form) = %d, want 1", got)
	}
}

func TestSPAReplacesNode(t *testing.T) {
	s := New()
	defer s.Close()

	_, page := getText(t, s.client(), s.URL+"/spa")
	for _, want := range []string{
		"<h1>Stage one</h1>",
		`<button id=first type=button>Continue</button>`,
		"document.body.dataset.stage = 'one'",
		"replacement.id = 'second'",
		"replacement.textContent = 'Continue'",
		"parentNode.replaceChild",
		"stage two",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("spa page missing %q", want)
		}
	}

	status, body := getText(t, s.client(), s.URL+"/spa/state")
	if status != http.StatusOK {
		t.Fatalf("/spa/state status = %d", status)
	}
	var state struct{ Stage string }
	if err := json.Unmarshal([]byte(body), &state); err != nil {
		t.Fatalf("decode /spa/state: %v (body %q)", err, body)
	}
	if state.Stage != "one" {
		t.Errorf("initial stage = %q, want one", state.Stage)
	}
}

func TestAppClickSideEffect(t *testing.T) {
	s := New()
	defer s.Close()

	_, page := getText(t, s.client(), s.URL+"/app")
	for _, want := range []string{
		"<h1>Counter</h1>",
		`<button id=inc>Increment</button>`,
		`<input id=q`,
		`<label for=q>Query</label>`,
		`<button id=go type=submit>Search</button>`,
		"fetch('/app/click'",
		`JSON.stringify({button: 'inc'})`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("app page missing %q", want)
		}
	}

	resp, err := s.client().Post(s.URL+"/app/click", "application/json",
		strings.NewReader(`{"button":"inc"}`))
	if err != nil {
		t.Fatalf("post click: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("POST /app/click status = %d, want 204", resp.StatusCode)
	}
	if got := s.Hit.MethodCount(http.MethodPost, "app/click"); got != 1 {
		t.Errorf("POST app/click hits = %d, want 1", got)
	}

	_, page = getText(t, s.client(), s.URL+"/app?term=needle")
	if !strings.Contains(page, `<p id=result>term=needle</p>`) {
		t.Errorf("app result echo missing:\n%s", page)
	}
}

func TestStorageCookieAndLocalStorage(t *testing.T) {
	s := New()
	defer s.Close()
	jar := newJar(t)
	c := &http.Client{Jar: jar, Timeout: 10 * time.Second}

	resp, err := c.Get(s.URL + "/storage")
	if err != nil {
		t.Fatalf("GET /storage: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read /storage: %v", err)
	}
	cookies := jar.Cookies(resp.Request.URL)
	if len(cookies) == 0 || cookies[0].Name != storageCookie {
		t.Fatalf("cookie %q not set, got %+v", storageCookie, cookies)
	}
	sid := cookies[0].Value
	if !regexp.MustCompile(`^[0-9a-f]{24}$`).MatchString(sid) {
		t.Errorf("sid = %q, want 24 hex chars", sid)
	}
	page := string(body)
	for _, want := range []string{
		`<pre id=state>cookie=` + sid,
		`localStorage.setItem('k', "` + storageLocalValue + `")`,
		`sessionStorage.setItem('k', "` + storageSessionValue + `")`,
		"indexedDB.open('fixture', 1)",
		"navigator.serviceWorker.register('/sw.js')",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("/storage missing %q", want)
		}
	}

	// A fresh page in the same jar proves the cookie survived.
	_, page = getText(t, c, s.URL+"/storage/read")
	if !strings.Contains(page, `<pre id=state>cookie=`+sid) {
		t.Errorf("/storage/read did not render the cookie:\n%s", page)
	}
	if got := s.Hit.Count("storage"); got != 1 {
		t.Errorf("Hit.Count(storage) = %d, want 1", got)
	}
	if got := s.Hit.Count("storage/read"); got != 1 {
		t.Errorf("Hit.Count(storage/read) = %d, want 1", got)
	}
}

func TestServiceWorkerScript(t *testing.T) {
	s := New()
	defer s.Close()

	resp, err := s.client().Get(s.URL + "/sw.js")
	if err != nil {
		t.Fatalf("GET /sw.js: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read sw.js: %v", err)
	}
	js := string(body)
	for _, want := range []string{
		"var CACHE = 'fixture-v1'",
		"caches.open(CACHE)",
		"caches.match(req)",
		"addEventListener('fetch'",
		"type: 'pong'",
		"cache: CACHE",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("sw.js missing %q", want)
		}
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("sw.js Content-Type = %q", ct)
	}
}

func TestRedirects(t *testing.T) {
	s := New()
	defer s.Close()
	c := newNoRedirectClient()

	resp, err := c.Get(s.URL + "/redirect/ok")
	if err != nil {
		t.Fatalf("GET /redirect/ok: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("/redirect/ok status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/form" {
		t.Errorf("/redirect/ok Location = %q, want /form", got)
	}

	// Chain: /redirect/chain -> /redirect/ok -> /form, followed manually so
	// each hop is asserted.
	seen := make([]string, 0, 4)
	cur := s.URL + "/redirect/chain"
	for i := 0; i < 4; i++ {
		resp, err := c.Get(cur)
		if err != nil {
			t.Fatalf("hop %d GET %s: %v", i, cur, err)
		}
		resp.Body.Close()
		seen = append(seen, cur)
		if resp.StatusCode != http.StatusFound {
			break
		}
		loc := resp.Header.Get("Location")
		if u, err := url.Parse(loc); err == nil && u.IsAbs() {
			cur = loc
		} else {
			cur = s.URL + loc
		}
	}
	want := []string{s.URL + "/redirect/chain", s.URL + "/redirect/ok", s.URL + "/form"}
	if len(seen) != len(want) {
		t.Fatalf("redirect chain hops = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("hop %d = %q, want %q", i, seen[i], want[i])
		}
	}
	if s.Hit.Count("form") != 1 {
		t.Errorf("Hit.Count(form) = %d, want 1 after chain", s.Hit.Count("form"))
	}

	// Denied redirect: default target is the .invalid host.
	resp, err = c.Get(s.URL + "/redirect/denied")
	if err != nil {
		t.Fatalf("GET /redirect/denied: %v", err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Location"); got != defaultDeniedURL {
		t.Errorf("/redirect/denied Location = %q, want %q", got, defaultDeniedURL)
	}

	// Pointing the denied host at our own /denied/ surface makes a leak
	// observable instead of invisible.
	s.SetDeniedURL(s.URL + "/denied")
	if got := s.DeniedURL(); got != s.URL+"/denied" {
		t.Fatalf("DeniedURL = %q", got)
	}
	resp, err = c.Get(s.URL + "/redirect/denied")
	if err != nil {
		t.Fatalf("GET /redirect/denied (patched): %v", err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Location"); got != s.URL+"/denied/x" {
		t.Errorf("patched Location = %q, want %q", got, s.URL+"/denied/x")
	}
}

func TestSetDeniedURLRaceFree(t *testing.T) {
	s := New()
	defer s.Close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.SetDeniedURL(fmt.Sprintf("http://denied-%d.invalid", i))
				_ = s.DeniedURL()
				resp, err := s.client().Get(s.URL + "/subresource/denied")
				if err == nil {
					resp.Body.Close()
				}
			}
		}(i)
	}
	wg.Wait()
	if got := s.Hit.Count("subresource/denied"); got != 400 {
		t.Errorf("Hit.Count(subresource/denied) = %d, want 400", got)
	}
}

func TestSubresources(t *testing.T) {
	s := New()
	defer s.Close()

	status, page := getText(t, s.client(), s.URL+"/subresource")
	if status != http.StatusOK {
		t.Fatalf("/subresource status = %d", status)
	}
	for _, want := range []string{
		`<img id=img src="/img.png"`,
		`<script src="/sub.js"></script>`,
		`<link rel=stylesheet href="/sub.css">`,
		`<p id=sub>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("/subresource missing %q", want)
		}
	}

	client := s.client()
	for _, tc := range []struct{ path, ctype, body string }{
		{"/img.png", "image/png", "\x89PNG"},
		{"/sub.js", "javascript", "textContent = 'sub-ok'"},
		{"/sub.css", "text/css", "#sub{color:#123456}"},
	} {
		resp, err := client.Get(s.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s status = %d", tc.path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, tc.ctype) {
			t.Errorf("%s Content-Type = %q, want %q", tc.path, ct, tc.ctype)
		}
		if !strings.Contains(string(body), tc.body) {
			t.Errorf("%s body unexpected: %q", tc.path, string(body))
		}
	}

	// Denied variant points all three loads at the denied host.
	_, page = getText(t, client, s.URL+"/subresource/denied")
	if !strings.Contains(page, defaultDeniedBase) {
		t.Errorf("/subresource/denied does not reference the denied host:\n%s", page)
	}
	for _, frag := range []string{"/i.png", "/s.js", "/s.css"} {
		if !strings.Contains(page, frag) {
			t.Errorf("/subresource/denied missing %q", frag)
		}
	}
}

// wsDial performs a manual WebSocket handshake against rawURL and returns the
// connection plus the response reader.
func wsDial(t *testing.T, rawURL string) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %s: %v", rawURL, err)
	}
	conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", u.Host, err)
	}
	var kb [16]byte
	if _, err := rand.Read(kb[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	key := base64.StdEncoding.EncodeToString(kb[:])
	req := "GET /ws/echo HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("handshake status = %q", strings.TrimSpace(status))
	}
	accept := ""
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read handshake header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Sec-WebSocket-Accept") {
			accept = strings.TrimSpace(v)
		}
	}
	if want := wsAccept(key); accept != want {
		t.Fatalf("Sec-WebSocket-Accept = %q, want %q", accept, want)
	}
	return conn, br, key
}

// wsWriteMasked sends one masked client frame.
func wsWriteMasked(t *testing.T, w io.Writer, opcode byte, payload []byte) {
	t.Helper()
	var hdr [14]byte
	hdr[0] = 0x80 | opcode
	hdr[1] = 0x80 | byte(len(payload))
	for i := 0; i < 4; i++ {
		hdr[2+i] = []byte{0xa1, 0xb2, 0xc3, 0xd4}[i]
	}
	copy(hdr[6:], payload)
	masked := append([]byte(nil), payload...)
	for i := range masked {
		masked[i] ^= hdr[2+i%4]
	}
	buf := append(hdr[:6], masked...)
	if _, err := w.Write(buf); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func TestWebSocketEchoRoundTrip(t *testing.T) {
	s := New()
	defer s.Close()

	conn, br, _ := wsDial(t, s.URL+"/ws/echo")
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	wsWriteMasked(t, conn, opText, []byte("hello"))

	var hdr [2]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		t.Fatalf("read frame header: %v", err)
	}
	if op := hdr[0] & 0x0f; op != opText {
		t.Fatalf("echo opcode = %#x, want text", op)
	}
	if hdr[1]&0x80 != 0 {
		t.Fatalf("server frame must not be masked")
	}
	length := int(hdr[1] & 0x7f)
	payload := make([]byte, length)
	if _, err := io.ReadFull(br, payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if string(payload) != "hello" {
		t.Fatalf("echo = %q, want hello", payload)
	}
	if got := s.Hit.Count("ws/echo"); got != 1 {
		t.Errorf("Hit.Count(ws/echo) = %d, want 1", got)
	}

	// The handshake itself must not be counted: the frame is the event.
	if got := s.Hit.Count("ws"); got != 0 {
		t.Errorf("Hit.Count(ws) = %d, want 0 (only the page counts)", got)
	}

	// Close is answered with a close frame.
	wsWriteMasked(t, conn, opClose, []byte{0x03, 0xe8})
	var closeHdr [2]byte
	if _, err := io.ReadFull(br, closeHdr[:]); err != nil {
		t.Fatalf("read close reply: %v", err)
	}
	if op := closeHdr[0] & 0x0f; op != opClose {
		t.Errorf("close reply opcode = %#x, want %#x", op, opClose)
	}
}

func TestWebSocketPageMarkup(t *testing.T) {
	s := New()
	defer s.Close()

	_, page := getText(t, s.client(), s.URL+"/ws")
	for _, want := range []string{
		`<pre id=log>`,
		"new WebSocket(",
		"/ws/echo",
		"ws.send('hello')",
		"<received:",
		"<error:",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("/ws missing %q", want)
		}
	}
}

func TestHeadersJSON(t *testing.T) {
	s := New()
	defer s.Close()

	req, err := http.NewRequest(http.MethodGet, s.URL+"/headers", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("User-Agent", "FixtureAgent/1.0")
	req.Header.Set("Accept-Language", "en-GB,en;q=0.9")
	req.Header.Set("Sec-CH-UA", `"Fixture";v="1"`)
	req.Header.Set("Sec-CH-UA-Mobile", "?0")
	req.Header.Set("Sec-CH-UA-Platform", `"Linux"`)

	resp, err := s.client().Do(req)
	if err != nil {
		t.Fatalf("GET /headers: %v", err)
	}
	defer resp.Body.Close()
	var got headersResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode headers json: %v", err)
	}
	if got.TLS {
		t.Errorf("tls = true on the plain listener")
	}
	if got.UserAgent != "FixtureAgent/1.0" {
		t.Errorf("user_agent = %q", got.UserAgent)
	}
	if got.AcceptLanguage != "en-GB,en;q=0.9" {
		t.Errorf("accept_language = %q", got.AcceptLanguage)
	}
	if got.SecCHUA != `"Fixture";v="1"` {
		t.Errorf("sec_ch_ua = %q", got.SecCHUA)
	}
	if got.Headers["Sec-Ch-Ua-Platform"][0] != `"Linux"` {
		t.Errorf("headers map lost Sec-CH-UA-Platform: %v", got.Headers)
	}
}

func TestHeadersTLS(t *testing.T) {
	s := NewTLS()
	defer s.Close()

	resp, err := s.tlsClient().Get(s.URL + "/headers/tls")
	if err != nil {
		t.Fatalf("GET /headers/tls: %v", err)
	}
	defer resp.Body.Close()
	var got headersResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.TLS {
		t.Errorf("tls = false on the TLS listener")
	}
	if !strings.HasPrefix(s.URL, "https://") {
		t.Errorf("NewTLS URL = %q, want https scheme", s.URL)
	}
}

func TestSlowCap(t *testing.T) {
	s := New()
	defer s.Close()

	start := time.Now()
	status, page := getText(t, s.client(), s.URL+"/slow?ms=0")
	if status != http.StatusOK || !strings.Contains(page, "<h1>slow</h1>") {
		t.Fatalf("/slow?ms=0 status = %d body = %q", status, page)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("/slow?ms=0 took %v", elapsed)
	}

	// The cap is 5000ms; asking for far more must not sleep longer than that.
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(s.URL + "/slow?ms=999999")
	if err != nil {
		t.Fatalf("GET /slow?ms=999999: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<p id=ms>5000</p>") {
		t.Errorf("capped /slow body = %q", string(body))
	}
}

func TestFramesPage(t *testing.T) {
	s := New()
	defer s.Close()

	_, page := getText(t, s.client(), s.URL+"/frames")
	if got := strings.Count(page, "<iframe"); got != 2 {
		t.Fatalf("iframe count = %d, want 2\n%s", got, page)
	}
	for _, want := range []string{
		`src="/frame/inner?a"`,
		`src="/frame/inner?b"`,
		`<button id=outer-btn`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("/frames missing %q", want)
		}
	}

	_, inner := getText(t, s.client(), s.URL+"/frame/inner?a")
	if !strings.Contains(inner, `<button id=inner type=button>Inner action</button>`) {
		t.Errorf("inner frame missing the inner button:\n%s", inner)
	}
}

func TestDialogPopupDownloadPermPages(t *testing.T) {
	s := New()
	defer s.Close()
	c := s.client()

	for _, tc := range []struct {
		path  string
		wants []string
	}{
		{"/dialog", []string{"<h1>Dialogs</h1>", `id=alert`, "alert('hi')", "document.getElementById('after').textContent = 'after'", `id=confirm`, "confirm('sure?')", `<p id=after>before</p>`}},
		{"/popup", []string{"<h1>Popup</h1>", `id=open`, "window.open('/form')"}},
		{"/download", []string{"<h1>Download</h1>", `id=dl`, `download="x.txt"`, `href="/download/file"`}},
		{"/perm", []string{"<h1>Permissions</h1>", `<pre id=perm>`, "navigator.permissions.query", "'geolocation'", "'notifications'", "'camera'", "'microphone'"}},
		{"/perm/geo", []string{"<h1>Geolocation</h1>", `<pre id=geo>`, "navigator.geolocation.getCurrentPosition"}},
	} {
		status, page := getText(t, c, s.URL+tc.path)
		if status != http.StatusOK {
			t.Errorf("%s status = %d", tc.path, status)
			continue
		}
		for _, want := range tc.wants {
			if !strings.Contains(page, want) {
				t.Errorf("%s missing %q", tc.path, want)
			}
		}
	}

	resp, err := c.Get(s.URL + "/download/file")
	if err != nil {
		t.Fatalf("GET /download/file: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if got := resp.Header.Get("Content-Disposition"); !strings.Contains(got, `filename="x.txt"`) {
		t.Errorf("Content-Disposition = %q", got)
	}
	if string(body) != "hello" {
		t.Errorf("download body = %q, want hello", body)
	}
}

func TestBigPageSize(t *testing.T) {
	s := New()
	defer s.Close()

	for _, tc := range []struct {
		query string
		chars int
	}{
		{"", defaultBigChars},
		{"?chars=1234", 1234},
		{"?chars=0", 0},
		{"?chars=99999999", maxBigChars},
	} {
		_, page := getText(t, s.client(), s.URL+"/big"+tc.query)
		if !strings.Contains(page, "<h1>Big page</h1>") {
			t.Fatalf("/big%s missing the heading", tc.query)
		}
		start := strings.Index(page, "<p id=big>") + len("<p id=big>")
		end := strings.Index(page, "</p>")
		if start < len("<p id=big>") || end < start {
			t.Fatalf("/big%s has no <p id=big> block", tc.query)
		}
		if got := end - start; got != tc.chars {
			t.Errorf("/big%s filler length = %d, want %d", tc.query, got, tc.chars)
		}
	}

	status, _ := getText(t, s.client(), s.URL+"/big?chars=-5")
	if status != http.StatusBadRequest {
		t.Errorf("/big?chars=-5 status = %d, want 400", status)
	}
}

func TestIndexLinksEveryRoute(t *testing.T) {
	s := New()
	defer s.Close()

	_, page := getText(t, s.client(), s.URL+"/")
	for _, href := range []string{
		`/form`, `/spa`, `/app`, `/storage`, `/sw.js`, `/redirect/ok`,
		`/redirect/denied`, `/redirect/chain`, `/subresource`, `/subresource/denied`,
		`/img.png`, `/ws`, `/headers`, `/slow`, `/frames`, `/frame/inner`,
		`/dialog`, `/popup`, `/download`, `/perm`, `/perm/geo`, `/big`,
	} {
		if !strings.Contains(page, `href="`+href+`"`) {
			t.Errorf("index missing link %q", href)
		}
	}
	if !strings.Contains(page, "<h1>Fixture index</h1>") || !strings.Contains(page, "<ul>") {
		t.Errorf("index is not a headed list:\n%s", page)
	}
	if got := s.Hit.Count("/"); got != 1 {
		t.Errorf("Hit.Count(/) = %d, want 1", got)
	}

	status, _ := getText(t, s.client(), s.URL+"/does-not-exist")
	if status != http.StatusNotFound {
		t.Errorf("unknown path status = %d, want 404", status)
	}
}

func TestHitLogCountsAndBounds(t *testing.T) {
	s := New()
	defer s.Close()
	c := s.client()

	var wg sync.WaitGroup
	const workers, each = 16, 25
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				resp, err := c.Get(s.URL + "/form")
				if err != nil {
					t.Errorf("GET /form: %v", err)
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	want := workers * each
	if got := s.Hit.Count("form"); got != want {
		t.Errorf("Hit.Count(form) = %d, want %d", got, want)
	}
	if got := s.Hit.Total(); got != want {
		t.Errorf("Hit.Total() = %d, want %d", got, want)
	}
	snap := s.Hit.Snapshot()
	if snap["form"] != want {
		t.Errorf("Snapshot()[form] = %d, want %d", snap["form"], want)
	}
	if keys := s.Hit.Keys(); len(keys) != 1 || keys[0] != "form" {
		t.Errorf("Keys() = %v", keys)
	}

	s.Hit.Reset()
	if s.Hit.Total() != 0 || len(s.Hit.Keys()) != 0 {
		t.Errorf("Reset left state: total=%d keys=%v", s.Hit.Total(), s.Hit.Keys())
	}

	// The map is bounded: distinct keys beyond the cap fold into overflow.
	h := &HitLog{}
	for i := 0; i < maxHitKeys+50; i++ {
		h.Add(fmt.Sprintf("k%d", i))
	}
	if keys := len(h.Snapshot()); keys > maxHitKeys+1 {
		t.Errorf("HitLog kept %d keys, cap is %d (+1 overflow)", keys, maxHitKeys)
	}
	if h.Count(overflowKey) != 50 {
		t.Errorf("overflow count = %d, want 50", h.Count(overflowKey))
	}
}

func TestHandlerMountable(t *testing.T) {
	s := New()
	defer s.Close()

	// Mounting the handler on a caller-owned listener must serve the same
	// routes and count into the same HitLog.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: s.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	base := "http://" + ln.Addr().String()
	status, page := getText(t, &http.Client{Timeout: 5 * time.Second}, base+"/form")
	if status != http.StatusOK || !strings.Contains(page, "<h1>Fixture form</h1>") {
		t.Fatalf("mounted handler status = %d", status)
	}
	if got := s.Hit.Count("form"); got != 1 {
		t.Errorf("Hit.Count(form) = %d, want 1 via mounted handler", got)
	}
}

func TestParagraphTemplateSprintf(t *testing.T) {
	// Guards the denied subresource template: it must have exactly three verbs
	// so the three denied URLs land in img/link/script order.
	if got := strings.Count(subresourceDeniedPageTpl, "%s"); got != 3 {
		t.Fatalf("denied template has %d verbs, want 3", got)
	}
}

// TestWSFrameEncodings covers the three payload-length encodings directly,
// without a socket, so the 126/127 extended headers are verified.
func TestWSFrameEncodings(t *testing.T) {
	for _, n := range []int{0, 1, 125, 126, 127, 70000} {
		payload := strings.Repeat("x", n)
		var buf strings.Builder
		if err := writeWSFrame(&buf, opText, []byte(payload)); err != nil {
			t.Fatalf("write %d: %v", n, err)
		}
		raw := []byte(buf.String())
		switch {
		case n < 126:
			if raw[1]&0x7f != byte(n) {
				t.Errorf("n=%d control byte = %#x, want %#x", n, raw[1]&0x7f, n)
			}
		case n <= 0xffff:
			if raw[1]&0x7f != 126 {
				t.Errorf("n=%d control byte = %#x, want 126", n, raw[1]&0x7f)
			}
		default:
			if raw[1]&0x7f != 127 {
				t.Errorf("n=%d control byte = %#x, want 127", n, raw[1]&0x7f)
			}
		}
		fin, op, got, err := readWSFrame(bufio.NewReader(strings.NewReader(buf.String())))
		if err != nil {
			t.Fatalf("read %d: %v", n, err)
		}
		if !fin || op != opText || string(got) != payload {
			t.Fatalf("round trip %d: fin=%v op=%#x len=%d", n, fin, op, len(got))
		}
	}
}

// TestWSReadRejectsOversizePayload proves the 1 MiB frame guard.
func TestWSReadRejectsOversizePayload(t *testing.T) {
	var hdr [10]byte
	hdr[0] = 0x81
	hdr[1] = 127
	binary.BigEndian.PutUint64(hdr[2:10], 1<<21)
	_, _, _, err := readWSFrame(bufio.NewReader(bytes.NewReader(hdr[:])))
	if err == nil {
		t.Fatalf("oversize frame was accepted")
	}
}

// TestPageScriptsParse feeds every inline script and the service worker to
// `node --check`, so a typo in a fixture's JavaScript fails here instead of
// silently breaking a Chromium end-to-end test. Skipped when node is absent.
func TestPageScriptsParse(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping JS syntax check")
	}
	s := New()
	defer s.Close()

	pages := []string{
		"/spa", "/app", "/app?term=needle", "/storage", "/storage/read",
		"/ws", "/dialog", "/popup", "/perm", "/perm/geo", "/subresource",
		"/subresource/denied", "/",
	}
	dir := t.TempDir()
	n := 0
	for _, p := range pages {
		_, page := getText(t, s.client(), s.URL+p)
		for _, js := range inlineScripts(page) {
			n++
			file := filepath.Join(dir, fmt.Sprintf("page-%02d.js", n))
			if err := os.WriteFile(file, []byte(js), 0o600); err != nil {
				t.Fatalf("write %s: %v", file, err)
			}
			checkNode(t, nodeBin, file, p)
		}
	}
	_, sw := getText(t, s.client(), s.URL+"/sw.js")
	swFile := filepath.Join(dir, "sw.js")
	if err := os.WriteFile(swFile, []byte(sw), 0o600); err != nil {
		t.Fatalf("write sw.js: %v", err)
	}
	checkNode(t, nodeBin, swFile, "/sw.js")

	if n == 0 {
		t.Fatal("no inline scripts were extracted from any fixture page")
	}
	t.Logf("checked %d inline scripts plus the service worker", n)
}

// inlineScripts returns the bodies of every <script> element without a src.
func inlineScripts(page string) []string {
	var out []string
	rest := page
	for {
		i := strings.Index(rest, "<script")
		if i < 0 {
			return out
		}
		rest = rest[i+len("<script"):]
		gt := strings.Index(rest, ">")
		if gt < 0 {
			return out
		}
		openTag := rest[:gt]
		body := rest[gt+1:]
		end := strings.Index(body, "</script>")
		if end < 0 {
			return out
		}
		if !strings.Contains(openTag, "src=") {
			out = append(out, body[:end])
		}
		rest = body[end:]
	}
}

// checkNode runs `node --check` on one file and reports the parse error.
func checkNode(t *testing.T, nodeBin, file, origin string) {
	t.Helper()
	cmd := exec.Command(nodeBin, "--check", file)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("JS syntax error in %s script (%s): %v\n%s", origin, filepath.Base(file), err, out)
	}
}

// TestSubresourceScriptOrdering guards the bug found in real Chromium: /sub.js
// resolves #sub by id, so the paragraph must precede the script element in the
// document. Reversing them made the page silently stay at "waiting".
func TestSubresourceScriptOrdering(t *testing.T) {
	s := New()
	defer s.Close()

	_, page := getText(t, s.client(), s.URL+"/subresource")
	pIdx := strings.Index(page, `id=sub`)
	sIdx := strings.Index(page, `<script src="/sub.js">`)
	if pIdx < 0 || sIdx < 0 {
		t.Fatalf("/subresource missing #sub or the script tag:\n%s", page)
	}
	if pIdx > sIdx {
		t.Errorf("#sub (%d) must precede <script src=/sub.js> (%d)", pIdx, sIdx)
	}

	// The denied variant uses the same ordering rule.
	_, denied := getText(t, s.client(), s.URL+"/subresource/denied")
	if dp, ds := strings.Index(denied, `id=sub`), strings.Index(denied, "<script src="); dp < 0 || ds < 0 || dp > ds {
		t.Errorf("denied page ordering wrong: #sub=%d script=%d", dp, ds)
	}

	// The script must only touch an element it can actually find.
	if !strings.Contains(subJS, "getElementById('sub')") {
		t.Errorf("subJS no longer targets #sub: %s", subJS)
	}
}

// TestStorageCookieStable guards the second bug found in real Chromium: the
// cookie has to be minted once per profile and then reused, otherwise a later
// page load (or the service worker's precache fetch of /storage) rotates the
// value a test just captured and the persistence assertion becomes flaky.
func TestStorageCookieStable(t *testing.T) {
	s := New()
	defer s.Close()

	jar := newJar(t)
	c := &http.Client{Jar: jar, Timeout: 10 * time.Second}

	resp, err := c.Get(s.URL + "/storage")
	if err != nil {
		t.Fatalf("GET /storage: %v", err)
	}
	first := cookieValue(t, resp)
	if first == "" {
		t.Fatal("/storage set no sid")
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// Three more loads must all keep the same value.
	for i := 0; i < 3; i++ {
		resp, err := c.Get(s.URL + "/storage")
		if err != nil {
			t.Fatalf("reload %d: %v", i, err)
		}
		if got := cookieValue(t, resp); got != first {
			t.Errorf("load %d rotated sid: %q -> %q", i+2, first, got)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// Explicit rotation is still available, and the value must really change.
	resp, err = c.Get(s.URL + "/storage?rotate=1")
	if err != nil {
		t.Fatalf("GET /storage?rotate=1: %v", err)
	}
	rotated := cookieValue(t, resp)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if rotated == "" || rotated == first {
		t.Errorf("rotate=1 did not mint a new sid: %q -> %q", first, rotated)
	}
	if !regexp.MustCompile(`^[0-9a-f]{24}$`).MatchString(rotated) {
		t.Errorf("rotated sid = %q, want 24 hex chars", rotated)
	}
}

// cookieValue returns the value of the fixture sid cookie on resp, or "".
func cookieValue(t *testing.T, resp *http.Response) string {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == storageCookie {
			return c.Value
		}
	}
	return ""
}

// TestServiceWorkerScopeIsNarrow guards the third Chromium-found bug: a fetch
// handler registered at scope "/" that answers cache-first would replay cached
// /slow, /headers and /app responses, so the request never reaches the server
// and the hit log silently under-counts.
func TestServiceWorkerScopeIsNarrow(t *testing.T) {
	s := New()
	defer s.Close()

	_, js := getText(t, s.client(), s.URL+"/sw.js")
	if !strings.Contains(js, "PRECACHE.indexOf(url.pathname) < 0") {
		t.Errorf("sw.js lost its pathname allowlist guard:\n%s", js)
	}
	if !strings.Contains(js, "var PRECACHE = ['/storage']") {
		t.Errorf("sw.js precache list changed:\n%s", js)
	}
	// The guard must run before respondWith, or the cache wins anyway.
	guard := strings.Index(js, "PRECACHE.indexOf(url.pathname) < 0")
	respond := strings.Index(js, "event.respondWith")
	if guard < 0 || respond < 0 || guard > respond {
		t.Errorf("allowlist guard (at %d) must precede respondWith (at %d)", guard, respond)
	}
	// The pong must be able to reach a transferred MessagePort.
	if !strings.Contains(js, "event.ports[0].postMessage(reply)") {
		t.Errorf("sw.js pong does not use the transferred port:\n%s", js)
	}
}
