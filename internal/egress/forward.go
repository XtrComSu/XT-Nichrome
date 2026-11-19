package egress

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/policy"
)

// hopByHopHeaders are never forwarded (RFC 9110 §7.6.1). Connection and
// Upgrade are removed too, except for a protocol switch where they carry the
// handshake.
var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"TE",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// hopByHopSet builds the header-name skip set for one message, including the
// field names nominated by its Connection header.
func hopByHopSet(h http.Header) map[string]bool {
	skip := make(map[string]bool, len(hopByHopHeaders))
	for _, name := range hopByHopHeaders {
		skip[http.CanonicalHeaderKey(name)] = true
	}
	for _, v := range h.Values("Connection") {
		for _, field := range strings.Split(v, ",") {
			if name := http.CanonicalHeaderKey(strings.TrimSpace(field)); name != "" {
				skip[name] = true
			}
		}
	}
	return skip
}

// copyHeaders copies src into dst, skipping hop-by-hop names and the listed
// names.
func copyHeaders(dst, src http.Header, skip map[string]bool, drop ...string) {
	for name, vals := range src {
		canonical := http.CanonicalHeaderKey(name)
		if skip[canonical] {
			continue
		}
		dropped := false
		for _, d := range drop {
			if canonical == d {
				dropped = true
				break
			}
		}
		if dropped {
			continue
		}
		for _, v := range vals {
			dst.Add(canonical, v)
		}
	}
}

// hasToken reports whether a comma-separated header value contains token,
// case-insensitively.
func hasToken(value, token string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

// upgradeProto returns the requested protocol when the message is a protocol
// switch. A browser WebSocket handshake reaches a proxy as a plain HTTP request
// carrying Connection: Upgrade / Upgrade: websocket.
func upgradeProto(h http.Header) string {
	if !hasToken(h.Get("Connection"), "upgrade") {
		return ""
	}
	return strings.TrimSpace(h.Get("Upgrade"))
}

// hostHeader renders the outbound Host value: the origin name, with the port
// only when it is not the http default. IPv6 literals are bracketed.
func hostHeader(host string, port int) string {
	if port == policy.DefaultPort("http") {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// handleForward serves one absolute-URI plain HTTP request: origin policy on the
// name, resolve, check EVERY address, dial a checked IP (no re-resolution), then
// forward the request rewritten to origin-form with body streaming.
//
// The origin request is written by hand over the dialed conn rather than through
// http.Transport: the transport would re-resolve the name, which is exactly the
// DNS-rebinding window this component exists to close.
func (p *Proxy) handleForward(w http.ResponseWriter, r *http.Request, start time.Time) {
	ev := Event{Time: start, Kind: "http", Method: r.Method, Scheme: "http"}
	if r.URL == nil || r.URL.Host == "" {
		p.deny(w, ev, http.StatusBadRequest, "absolute-URI request without a host")
		return
	}
	switch strings.ToLower(r.URL.Scheme) {
	case "http", "ws":
		// Forwarded in origin-form below; ws:// arrives as Connection: Upgrade.
	case "https", "wss":
		p.deny(w, ev, http.StatusBadRequest, "https requests must use CONNECT")
		return
	default:
		p.deny(w, ev, http.StatusBadRequest, "unsupported request scheme")
		return
	}

	host, port, err := splitAuthority(r.URL.Host, policy.DefaultPort("http"), false)
	if err != nil || !portInRange(port) {
		ev.Host, ev.Port = host, port
		p.deny(w, ev, http.StatusBadRequest, "malformed request authority")
		return
	}
	ev.Host, ev.Port = host, port
	if strings.EqualFold(r.URL.Scheme, "ws") {
		ev.Scheme = "ws"
	}

	decision, ips, err := p.checkTarget(host, port)
	ev.IPs = ipStrings(ips)
	if err != nil {
		p.fail(w, ev, http.StatusBadGateway, "resolution failed for "+host)
		return
	}
	if !decision.Allowed {
		p.deny(w, ev, http.StatusForbidden, decision.Reason)
		return
	}

	upstream, _, err := p.dialChecked(port, ips)
	if err != nil {
		p.fail(w, ev, http.StatusBadGateway, "dial failed")
		return
	}
	handedOff := false
	defer func() {
		if !handedOff {
			_ = upstream.Close()
		}
	}()

	upgrade := upgradeProto(r.Header)
	skip := hopByHopSet(r.Header)
	if upgrade != "" {
		// Preserve the handshake headers for a protocol switch.
		delete(skip, "Upgrade")
		delete(skip, "Connection")
	}

	out := &http.Request{
		Method:     r.Method,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header, len(r.Header)+2),
		Host:       hostHeader(host, port),
		// Origin-form: scheme and host are absent on purpose, so the request
		// line carries only path+query and no second resolution can occur.
		URL: &url.URL{Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery},
		// Body is streamed straight through; -1 lets the writer chunk it.
		ContentLength: r.ContentLength,
	}
	if r.Body != nil && r.Body != http.NoBody {
		out.Body = &countingReadCloser{r: r.Body, counter: &p.bytesUp}
	}
	// Keep any Via the client already sent and append ours: the field is a
	// trace of proxies, not something to overwrite.
	copyHeaders(out.Header, r.Header, skip, "Host", "Content-Length")
	out.Header.Add("Via", viaToken)
	if upgrade != "" {
		out.Header.Set("Connection", "Upgrade")
		out.Header.Set("Upgrade", upgrade)
	} else {
		out.Close = true
	}

	br := bufio.NewReader(upstream)
	bw := bufio.NewWriter(upstream)
	if err := out.Write(bw); err != nil {
		p.fail(w, ev, http.StatusBadGateway, "request write failed")
		return
	}
	if err := bw.Flush(); err != nil {
		p.fail(w, ev, http.StatusBadGateway, "request write failed")
		return
	}

	resp, err := http.ReadResponse(br, out)
	if err != nil {
		p.fail(w, ev, http.StatusBadGateway, "invalid origin response")
		return
	}

	if resp.StatusCode == http.StatusSwitchingProtocols {
		// A 101 has no body framing: everything after the header block is raw
		// tunnel bytes, and br may already hold some of them.
		handedOff = true
		ev.Scheme = "ws"
		p.serveUpgrade(w, ev, start, resp, br, upstream)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	p.pumpResponse(w, ev, start, resp)
}

// pumpResponse forwards a normal response, streaming the body downstream.
func (p *Proxy) pumpResponse(w http.ResponseWriter, ev Event, start time.Time, resp *http.Response) {
	skip := hopByHopSet(resp.Header)
	copyHeaders(w.Header(), resp.Header, skip)
	switch {
	case resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified:
		// These statuses must not carry a body or a Content-Length.
		w.Header().Del("Content-Length")
		w.Header().Del("Transfer-Encoding")
	case resp.ContentLength >= 0:
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	default:
		w.Header().Del("Content-Length")
	}
	// Count and log the admission before streaming, so Stats() reflects admitted
	// requests rather than only those whose body finished cleanly (same rule as
	// the CONNECT path).
	p.allowed.Add(1)
	p.emitFinal(start, ev, resp.StatusCode, "http")

	w.WriteHeader(resp.StatusCode)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	_, _ = io.Copy(&countingWriter{w: w, counter: &p.bytesDown}, resp.Body)
}

// serveUpgrade completes a WebSocket-style protocol switch: replay the origin's
// 101 to the client verbatim, then splice raw bytes in both directions.
//
// Approximation: the 101 header block is re-serialised from the parsed response
// (values preserved, hop-by-hop fields except Upgrade/Connection dropped) rather
// than copied byte for byte. The tunnel payload itself is spliced as bytes, and
// any payload the origin sent immediately after its 101 is delivered from the
// buffered reader, not lost.
func (p *Proxy) serveUpgrade(w http.ResponseWriter, ev Event, start time.Time, resp *http.Response, br *bufio.Reader, upstream net.Conn) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		p.fail(w, ev, http.StatusInternalServerError, "hijack unsupported")
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		p.fail(w, ev, http.StatusInternalServerError, "hijack failed")
		return
	}

	var head bytes.Buffer
	head.WriteString("HTTP/1.1 101 " + http.StatusText(http.StatusSwitchingProtocols) + "\r\n")
	skip := hopByHopSet(resp.Header)
	delete(skip, "Upgrade")
	delete(skip, "Connection")
	for name, vals := range resp.Header {
		canonical := http.CanonicalHeaderKey(name)
		if skip[canonical] {
			continue
		}
		for _, v := range vals {
			head.WriteString(canonical + ": " + v + "\r\n")
		}
	}
	proto := resp.Header.Get("Upgrade")
	if proto == "" {
		proto = "websocket"
	}
	head.WriteString("Connection: Upgrade\r\n")
	head.WriteString("Upgrade: " + proto + "\r\n\r\n")
	if _, err := client.Write(head.Bytes()); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}

	p.allowed.Add(1)
	p.emitFinal(start, ev, http.StatusSwitchingProtocols, "http")
	// br may already hold payload the origin sent immediately behind its 101, so
	// the origin direction reads through it.
	p.splice(client, client, upstream, br)
}
