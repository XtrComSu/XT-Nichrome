package egress

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/policy"
)

// errBadAuthority marks a host:port that cannot be parsed; the caller answers
// 400 instead of dialing anything.
var errBadAuthority = errors.New("malformed host:port")

// splitAuthority parses "[::1]:8080", "::1" (bare literal, last colon wins) and
// "host:8080" into a normalized host plus a port. requirePort is set for
// CONNECT, where the authority must carry one.
func splitAuthority(authority string, defaultPort int, requirePort bool) (string, int, error) {
	if authority == "" {
		return "", 0, errBadAuthority
	}
	if strings.ContainsAny(authority, "/\\?#@ \t") {
		return "", 0, errBadAuthority
	}
	host, portStr := authority, ""
	hasPort := false
	switch {
	case strings.HasPrefix(authority, "["):
		h, p, err := net.SplitHostPort(authority)
		if err != nil {
			if strings.HasSuffix(authority, "]") {
				host = authority // bare bracketed literal, no port
			} else {
				return "", 0, errBadAuthority
			}
		} else {
			host, portStr, hasPort = h, p, true
		}
	case strings.Count(authority, ":") > 1:
		host = authority // unbracketed IPv6 literal, no port
	default:
		if h, p, err := net.SplitHostPort(authority); err == nil {
			host, portStr, hasPort = h, p, true
		}
	}

	host = normalizeHost(host)
	if host == "" {
		return "", 0, errBadAuthority
	}
	if !hasPort {
		if requirePort {
			return "", 0, errBadAuthority
		}
		return host, defaultPort, nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, errBadAuthority
	}
	return host, port, nil
}

// portInRange reports whether a port may be dialed at all.
func portInRange(port int) bool { return port >= 1 && port <= 65535 }

// lookupIPs resolves host, or parses it as an IP literal (no resolver call, so
// an IP-literal target cannot be redirected by DNS). Resolution honors
// Options.Resolver and DialTimeout.
func (p *Proxy) lookupIPs(host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	ctx, cancel := contextWithTimeout(p.dialTimeout)
	defer cancel()
	addrs, err := p.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// checkTarget applies the full admission test for one destination: origin
// policy on the name, then range policy on every resolved address. A resolution
// failure is returned as err (an error, not a policy denial).
func (p *Proxy) checkTarget(host string, port int) (policy.EgressDecision, []net.IP, error) {
	if d := p.pol.CheckEgress(host, port); !d.Allowed {
		return d, nil, nil
	}
	ips, err := p.lookupIPs(host)
	if err != nil {
		return policy.EgressDecision{}, nil, err
	}
	d := p.pol.CheckIPs(ips)
	return d, ips, nil
}

// dialChecked dials one of the already-validated addresses, trying them in
// order. Addresses come from the checked slice verbatim, so the name is never
// resolved a second time.
func (p *Proxy) dialChecked(port int, ips []net.IP) (net.Conn, string, error) {
	var lastErr error
	for _, ip := range ips {
		addr := net.JoinHostPort(policy.NormalizeIP(ip).String(), strconv.Itoa(port))
		conn, err := p.dialTCP(addr)
		if err == nil {
			return conn, addr, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no address to dial")
	}
	return nil, "", lastErr
}

// ipStrings renders resolved addresses for the Event.
func ipStrings(ips []net.IP) []string {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if norm := policy.NormalizeIP(ip); norm != nil {
			out = append(out, norm.String())
		}
	}
	return out
}

// denialBody is the body sent with every 403.
func denialBody(reason string) string {
	if reason == "" {
		reason = "denied"
	}
	return "egress: " + reason
}

// deny answers a refusal without dialing and records it.
func (p *Proxy) deny(w http.ResponseWriter, ev Event, status int, reason string) {
	ev.Allowed = false
	ev.Status = status
	if status == http.StatusForbidden {
		ev.Kind = "denied"
		p.denied.Add(1)
	} else {
		ev.Kind = "error"
		p.errors.Add(1)
	}
	ev.Reason = reason
	if !ev.Time.IsZero() {
		ev.DurationMS = time.Since(ev.Time).Milliseconds()
	}
	p.emit(ev)
	p.writeStatus(w, status, denialBody(reason))
}

// fail answers an internal failure (resolution, dial, hijack) and records it.
func (p *Proxy) fail(w http.ResponseWriter, ev Event, status int, reason string) {
	ev.Allowed = false
	ev.Status = status
	ev.Kind = "error"
	ev.Reason = reason
	if !ev.Time.IsZero() {
		ev.DurationMS = time.Since(ev.Time).Milliseconds()
	}
	p.errors.Add(1)
	p.emit(ev)
	p.writeStatus(w, status, "egress: "+reason)
}

// writeStatus writes a plain-text response with an explicit Content-Length so
// the client (and the tests) can always read the body.
func (p *Proxy) writeStatus(w http.ResponseWriter, status int, body string) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = fmt.Fprint(w, body)
}

// handleConnect serves CONNECT (HTTPS and other tunnels).
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request, start time.Time) {
	ev := Event{Time: start, Kind: "connect", Scheme: "connect"}

	host, port, err := splitAuthority(r.Host, 0, true)
	if err != nil || !portInRange(port) {
		ev.Host, ev.Port = host, port
		p.deny(w, ev, http.StatusBadRequest, "malformed CONNECT authority")
		return
	}
	ev.Host, ev.Port, ev.Scheme = host, port, schemeForPort(port)

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

	hj, ok := w.(http.Hijacker)
	if !ok {
		p.fail(w, ev, http.StatusInternalServerError, "hijack unsupported")
		return
	}
	upstream, _, err := p.dialChecked(port, ips)
	if err != nil {
		p.fail(w, ev, http.StatusBadGateway, "dial failed")
		return
	}
	client, brw, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		p.fail(w, ev, http.StatusInternalServerError, "hijack failed")
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		p.fail(w, ev, http.StatusBadGateway, "handshake write failed")
		return
	}

	// Admitted: count and log now, before the (possibly long-lived) splice, so
	// Stats().Allowed reflects admissions rather than tunnel completions.
	p.allowed.Add(1)
	p.emitFinal(start, ev, http.StatusOK, "connect")

	// brw.Reader may already hold bytes the client pipelined behind the CONNECT
	// headers; reading the client direction through it keeps those bytes instead
	// of dropping them. The origin is read from the conn itself.
	p.splice(client, brw.Reader, upstream, upstream)
}

// emitFinal records a successful connection decision. Event.IPs stays a list of
// bare resolved addresses (the exact set the policy checked); the dialed one is
// among them, so a log line never needs a second, differently-shaped field.
func (p *Proxy) emitFinal(start time.Time, ev Event, status int, kind string) {
	ev.Allowed = true
	ev.Status = status
	ev.Kind = kind
	ev.DurationMS = time.Since(start).Milliseconds()
	p.emit(ev)
}
