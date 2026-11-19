// Package egress implements the single outbound network path for browser
// traffic (brief §6).
//
// The browser is launched with this proxy as its only configured egress: no
// bypass list, and every name resolution inside the browser is poisoned
// (see ChromiumFlags). Chromium request hooks (Fetch/Network domains) are
// diagnostics only — they are advisory, race the network stack and can be
// detached or missed; they are never the enforcement point. Enforcement lives
// here, below the browser:
//
//   - The proxy refuses a destination whose host:port is not an authorized
//     origin (policy.CheckEgress).
//   - The proxy resolves the name itself, then refuses when ANY resolved
//     address is prohibited (policy.CheckIPs). A mixed answer is refused as a
//     whole; a permitted address is never cherry-picked out of it.
//   - The proxy dials the already-checked address rather than re-resolving the
//     name, so a DNS-rebinding answer cannot swap the target between the check
//     and the connect.
//   - Loopback/RFC1918 destinations are refused unless the policy was built
//     with allowPrivate (fixtures only), so "resolves to 127.0.0.1" is not a
//     way around the origin list.
//
// Denials never open a socket. They are answered with 403 and a body of the
// form "egress: <reason>" for CONNECT as well, so the browser surfaces a proxy
// error instead of a bare reset.
//
// # Residual bypasses
//
// Two bypass classes are closed by the launch flags in ChromiumFlags: name
// resolution inside the browser (--host-resolver-rules=MAP * ~NOTFOUND) and
// WebRTC UDP egress (--force-webrtc-ip-handling-policy /
// --webrtc-ip-handling-policy=disable_non_proxied_udp), plus QUIC
// (--disable-quic) so nothing rides UDP/443 around an HTTP CONNECT.
//
// An IP-literal direct connection still requires an OS/deployment-level
// boundary: this proxy can only enforce traffic that is handed to it, so the
// browser process must be confined by a network namespace, nftables/iptables
// owner match, or equivalent so that a connection which ignores proxy settings
// has no route. The flags make the browser's own resolution-based paths fail
// closed; the namespace is what makes an IP-literal path impossible. Note also
// that --host-resolver-rules cannot resolve the proxy address itself, so the
// listen address handed to the browser must be an IP literal (the default
// 127.0.0.1:0 satisfies this).
//
// Chromium bypasses localhost implicitly in some versions; the empty
// --proxy-bypass-list= is the contract this package emits, and where the browser
// still honours an implicit loopback exemption the deployment should add the
// "<-loopback>" token to that same flag. Either way the proxy refuses loopback
// origins whenever AllowPrivate() is false, so an escaped loopback request is
// still denied at the boundary.
package egress

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/policy"
)

// Event is one connection decision. It is deliberately narrow: it never
// carries headers, cookies, bodies or query strings, so it is safe to log.
//
// Kind is one of "connect" (CONNECT tunnel admitted), "http" (forwarded
// request), "denied" (refused before any dial) or "error" (malformed request,
// resolution failure or dial failure).
type Event struct {
	Time       time.Time
	Kind       string
	Method     string // for http
	Host       string // hostname only, never the query string
	Port       int
	Scheme     string
	Allowed    bool
	Reason     string
	IPs        []string
	Status     int
	DurationMS int64
}

// Stats is a point-in-time snapshot of proxy counters.
type Stats struct {
	Allowed     int64
	Denied      int64
	Errors      int64
	ActiveConns int64
	BytesUp     int64
	BytesDown   int64
}

// Options configures a Proxy.
type Options struct {
	// Policy is required: it is the only authority on what may be reached.
	Policy *policy.Policy
	// Addr is the listen address, e.g. "127.0.0.1:0". Empty = "127.0.0.1:0".
	Addr string
	// Log receives one sanitized line per decision. Optional. It may be called
	// concurrently from several connection goroutines.
	Log func(Event)
	// DialTimeout bounds resolution and connection establishment. Default 10s.
	DialTimeout time.Duration
	// Resolver resolves names for the proxy. Optional; defaults to
	// net.DefaultResolver. Injected in tests to simulate hostile DNS answers.
	Resolver *net.Resolver
}

const (
	defaultAddr     = "127.0.0.1:0"
	defaultDialTime = 10 * time.Second

	// viaToken is the proxy's Via pseudonym.
	viaToken = "1.1 xt-nichrome"
)

// Proxy is the enforcing HTTP/1.1 forward proxy. A Proxy is safe for
// concurrent use.
type Proxy struct {
	pol         *policy.Policy
	srv         *http.Server
	ln          net.Listener
	addr        string
	log         func(Event)
	dialTimeout time.Duration
	resolver    *net.Resolver

	startOnce sync.Once
	closeOnce sync.Once

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool

	allowed     atomic.Int64
	denied      atomic.Int64
	errors      atomic.Int64
	activeConns atomic.Int64
	bytesUp     atomic.Int64
	bytesDown   atomic.Int64
}

// New binds the listener and returns a Proxy. Nothing is served until Start.
func New(opts Options) (*Proxy, error) {
	if opts.Policy == nil {
		return nil, fmt.Errorf("egress: options.Policy is required")
	}
	addr := opts.Addr
	if addr == "" {
		addr = defaultAddr
	}
	timeout := opts.DialTimeout
	if timeout <= 0 {
		timeout = defaultDialTime
	}
	res := opts.Resolver
	if res == nil {
		res = net.DefaultResolver
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("egress: listen %s: %w", addr, err)
	}
	p := &Proxy{
		pol:         opts.Policy,
		ln:          ln,
		addr:        ln.Addr().String(),
		log:         opts.Log,
		dialTimeout: timeout,
		resolver:    res,
		conns:       make(map[net.Conn]struct{}),
	}
	p.srv = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	return p, nil
}

// Start serves in a background goroutine. It is safe to call once; later calls
// are no-ops.
func (p *Proxy) Start() {
	p.startOnce.Do(func() {
		go func() { _ = p.srv.Serve(&trackedListener{p: p, ln: p.ln}) }()
	})
}

// Addr returns the actual listen address as host:port.
func (p *Proxy) Addr() string { return p.addr }

// URL returns the proxy URL the browser should be launched with.
func (p *Proxy) URL() string { return "http://" + p.addr }

// Close stops accepting, terminates active connections (including hijacked
// tunnels) and unblocks every copy loop. It is idempotent and returns nil.
func (p *Proxy) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		victims := make([]net.Conn, 0, len(p.conns))
		for c := range p.conns {
			victims = append(victims, c)
		}
		p.conns = make(map[net.Conn]struct{})
		p.mu.Unlock()

		// Closes the listener (ending Accept) and every non-hijacked conn.
		_ = p.srv.Close()
		// srv.Close only closes listeners that Serve registered; when Start was
		// never called the listener is still open and must be closed here.
		_ = p.ln.Close()
		// Hijacked conns (tunnels, WebSocket splices) are invisible to
		// http.Server, so they are closed from our own registry.
		for _, c := range victims {
			_ = c.Close()
		}
	})
	return nil
}

// Stats returns a snapshot of the counters.
func (p *Proxy) Stats() Stats {
	return Stats{
		Allowed:     p.allowed.Load(),
		Denied:      p.denied.Load(),
		Errors:      p.errors.Load(),
		ActiveConns: p.activeConns.Load(),
		BytesUp:     p.bytesUp.Load(),
		BytesDown:   p.bytesDown.Load(),
	}
}

// ServeHTTP dispatches CONNECT tunnels and absolute-URI forwarded requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() {
		if rec := recover(); rec != nil {
			// A malformed request must never take the daemon down.
			p.errors.Add(1)
			p.emit(Event{Time: start, Kind: "error", Reason: "handler panic"})
			p.writeStatus(w, http.StatusInternalServerError, "internal proxy error")
		}
	}()
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r, start)
		return
	}
	p.handleForward(w, r, start)
}

// ChromiumFlags returns the launch flags that make p the browser's only egress
// path. extraDeny adds browser-side network features to switch off (background
// networking, component updates, DNS-based reporting, prefetch, pings,
// client-side phishing lookups); none of them duplicate the mandatory flags.
//
// The flags close DNS-based and WebRTC-based bypasses. They cannot close a raw
// IP-literal connect, which needs an OS boundary (netns/nftables) — see the
// package comment.
func ChromiumFlags(p *Proxy, extraDeny bool) []string {
	addr := "[::1]:0"
	if p != nil {
		if got := p.Addr(); got != "" {
			addr = got
		}
	}
	flags := []string{
		"--proxy-server=http://" + addr,
		// Empty bypass list: nothing, loopback included, may skip the proxy.
		"--proxy-bypass-list=",
		// Any name the browser resolves itself fails, so the proxy is the only
		// component that performs DNS.
		"--host-resolver-rules=MAP * ~NOTFOUND",
		// QUIC is UDP/443 and would bypass an HTTP CONNECT proxy.
		"--disable-quic",
		// Keep WebRTC from opening non-proxied UDP sockets.
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--webrtc-ip-handling-policy=disable_non_proxied_udp",
	}
	if extraDeny {
		flags = append(flags,
			"--disable-background-networking",
			"--disable-component-update",
			"--disable-domain-reliability",
			"--disable-client-side-phishing-detection",
			"--dns-prefetch-disable",
			"--no-pings",
		)
	}
	return flags
}

// emit records a decision and forwards it to the optional logger.
func (p *Proxy) emit(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	if p.log != nil {
		p.log(ev)
	}
}

// track registers a connection for teardown. It reports false when the proxy is
// already closed (the caller must then close the conn itself).
func (p *Proxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *Proxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
}

// trackedListener counts accepted connections and makes them killable on Close.
type trackedListener struct {
	p  *Proxy
	ln net.Listener
}

func (l *trackedListener) Accept() (net.Conn, error) {
	c, err := l.ln.Accept()
	if err != nil {
		return nil, err
	}
	tc := &managedConn{Conn: c, p: l.p, counts: true}
	if !l.p.track(tc) {
		_ = c.Close()
		return nil, net.ErrClosed
	}
	l.p.activeConns.Add(1)
	return tc, nil
}

func (l *trackedListener) Close() error   { return l.ln.Close() }
func (l *trackedListener) Addr() net.Addr { return l.ln.Addr() }

// managedConn is a conn registered in the proxy's teardown set. Close
// unregisters it exactly once; conns that came from the client listener also
// decrement the active-connection gauge (upstream dials do not).
//
// Registration is what makes Close() able to terminate hijacked tunnels, which
// http.Server no longer tracks, and unregistration on Close is what keeps the
// registry from growing for the life of the proxy.
type managedConn struct {
	net.Conn
	p      *Proxy
	counts bool
	once   sync.Once
}

func (c *managedConn) Close() error {
	c.once.Do(func() {
		c.p.untrack(c)
		if c.counts {
			c.p.activeConns.Add(-1)
		}
	})
	return c.Conn.Close()
}

// CloseWrite lets the splice loop half-close instead of tearing the peer down.
func (c *managedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

// connCount reports how many conns are registered for teardown; used by tests to
// prove the registry does not grow.
func (p *Proxy) connCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

// dialTCP connects to an already-checked IP literal (never a name, so no second
// resolution can redirect the connection).
func (p *Proxy) dialTCP(addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: p.dialTimeout}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	mc := &managedConn{Conn: conn, p: p}
	if !p.track(mc) {
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	return mc, nil
}

// schemeForPort names the scheme of a CONNECT target for logging purposes.
func schemeForPort(port int) string {
	switch port {
	case 443:
		return "https"
	case 80:
		return "http"
	}
	return "tcp"
}

// normalizeHost lowercases a hostname and drops a trailing root dot.
func normalizeHost(host string) string {
	host = strings.Trim(host, "[]")
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host
}
