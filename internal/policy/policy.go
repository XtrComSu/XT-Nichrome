// Package policy owns origin admission rules and the address-range checks used
// by both the admission layer and the egress boundary.
package policy

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/xtrcom/xt-nichrome/internal/types"
)

// Rule is one authorized origin. Scheme/host/port are compared exactly; port 0
// means the scheme default.
type Rule struct {
	Scheme string
	Host   string
	Port   int
}

func (r Rule) String() string {
	s := r.Scheme + "://" + r.Host
	if r.Port != 0 && r.Port != DefaultPort(r.Scheme) {
		s += fmt.Sprintf(":%d", r.Port)
	}
	return s
}

// DefaultPort returns the scheme default port.
func DefaultPort(scheme string) int {
	switch strings.ToLower(scheme) {
	case "http", "ws":
		return 80
	case "https", "wss":
		return 443
	}
	return 0
}

// ParseRule parses an origin spec such as https://127.0.0.1:8080 or
// http://fixture.test. Wildcards are rejected: the boundary must be exact.
func ParseRule(spec string) (Rule, error) {
	u, err := url.Parse(strings.TrimSpace(spec))
	if err != nil {
		return Rule{}, fmt.Errorf("policy: bad origin %q: %w", spec, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return Rule{}, fmt.Errorf("policy: origin %q needs scheme and host", spec)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" && scheme != "ws" && scheme != "wss" {
		return Rule{}, fmt.Errorf("policy: origin %q uses unsupported scheme", spec)
	}
	if u.User != nil {
		return Rule{}, fmt.Errorf("policy: origin %q must not embed credentials", spec)
	}
	if u.Path != "" && u.Path != "/" {
		return Rule{}, fmt.Errorf("policy: origin %q must not contain a path", spec)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return Rule{}, fmt.Errorf("policy: origin %q must be a bare origin", spec)
	}
	host := strings.ToLower(u.Hostname())
	port := DefaultPort(scheme)
	if p := u.Port(); p != "" {
		n, err := net.LookupPort("tcp", p)
		if err != nil {
			return Rule{}, fmt.Errorf("policy: origin %q has bad port: %w", spec, err)
		}
		port = n
	}
	if strings.Contains(host, "*") {
		return Rule{}, fmt.Errorf("policy: origin %q must not use wildcards", spec)
	}
	return Rule{Scheme: scheme, Host: host, Port: port}, nil
}

// Policy is an immutable set of rules plus private-address and public-web switches.
type Policy struct {
	rules []Rule
	// allowPrivate is on for fixtures. Production deployments set it false so
	// the egress boundary refuses loopback/RFC1918 destinations.
	allowPrivate bool
	// allowPublic permits any public http/https origin on standard ports (80/443)
	// while still enforcing CheckIPs SSRF blocks against RFC1918, loopback,
	// link-local/metadata, and multicast ranges.
	allowPublic bool
}

// New builds a policy from origin specs.
func New(specs []string, allowPrivate bool) (*Policy, error) {
	p := &Policy{allowPrivate: allowPrivate}
	for _, s := range specs {
		r, err := ParseRule(s)
		if err != nil {
			return nil, err
		}
		p.rules = append(p.rules, r)
	}
	sort.Slice(p.rules, func(i, j int) bool {
		if p.rules[i].Host != p.rules[j].Host {
			return p.rules[i].Host < p.rules[j].Host
		}
		if p.rules[i].Scheme != p.rules[j].Scheme {
			return p.rules[i].Scheme < p.rules[j].Scheme
		}
		return p.rules[i].Port < p.rules[j].Port
	})
	return p, nil
}

// Rules returns the configured rules.
func (p *Policy) Rules() []Rule { return append([]Rule(nil), p.rules...) }

// AllowPrivate reports whether private destinations are permitted (fixtures).
func (p *Policy) AllowPrivate() bool { return p.allowPrivate }

// SetAllowPublic enables or disables public web origin admission (ports 80/443)
// while keeping CheckIPs SSRF protection active.
func (p *Policy) SetAllowPublic(v bool) { p.allowPublic = v }

// AllowPublic reports whether public web origins are admitted.
func (p *Policy) AllowPublic() bool { return p.allowPublic }

// AdmitURL validates a caller-supplied URL for admission (brief §6): a real
// URL parser, no embedded credentials, http(s) only, and an authorized origin.
func (p *Policy) AdmitURL(raw string) (*url.URL, *types.APIError) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return nil, types.NewError(types.CodeInvalidRequest, "url is not parseable")
	}
	if u.User != nil {
		return nil, types.NewError(types.CodeInvalidRequest, "url must not embed credentials")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, types.NewError(types.CodeInvalidRequest, "url scheme must be http or https")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil, types.NewError(types.CodeInvalidRequest, "url must include a host")
	}
	port := DefaultPort(scheme)
	if ps := u.Port(); ps != "" {
		n, err := net.LookupPort("tcp", ps)
		if err != nil {
			return nil, types.NewError(types.CodeInvalidRequest, "url has an invalid port")
		}
		port = n
	}
	if !p.originAllowed(scheme, host, port) {
		return nil, &types.APIError{
			Code:    types.CodePolicyBlocked,
			Message: "destination is outside the authorized origin list",
			Detail:  map[string]any{"origin": scheme + "://" + net.JoinHostPort(host, fmt.Sprint(port))},
		}
	}
	return u, nil
}

func (p *Policy) originAllowed(scheme, host string, port int) bool {
	if p.allowPublic && ((scheme == "http" && port == 80) || (scheme == "https" && port == 443)) {
		return true
	}
	for _, r := range p.rules {
		if r.Host != host {
			continue
		}
		if r.Port != port {
			continue
		}
		if schemeMatch(r.Scheme, scheme) {
			return true
		}
	}
	return false
}

func schemeMatch(ruleScheme, got string) bool {
	if ruleScheme == got {
		return true
	}
	// ws/wss ride on http/https origins.
	switch ruleScheme {
	case "http":
		return got == "ws" || got == "http"
	case "https":
		return got == "wss" || got == "https"
	}
	return false
}

// EgressDecision is returned by the socket-level check.
type EgressDecision struct {
	Allowed bool
	Reason  string
}

// CheckEgress validates a host:port that the egress component is about to
// connect to. The destination is judged after DNS resolution by the caller via
// CheckIPs; this function only handles origin policy.
func (p *Policy) CheckEgress(host string, port int) EgressDecision {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if p.allowPublic && (port == 80 || port == 443) {
		return EgressDecision{Allowed: true}
	}
	scheme := "http"
	if port == 443 {
		scheme = "https"
	}
	for _, r := range p.rules {
		if r.Host != host || r.Port != port {
			continue
		}
		if schemeMatch(r.Scheme, scheme) {
			return EgressDecision{Allowed: true}
		}
	}
	// A port-443 rule also authorizes CONNECT for https origins.
	return EgressDecision{Allowed: false, Reason: "origin not authorized"}
}

// CheckIPs enforces the address-range rules on every resolved address, covering
// IPv4, IPv6 and IPv4-mapped IPv6 (brief §6).
func (p *Policy) CheckIPs(ips []net.IP) EgressDecision {
	if len(ips) == 0 {
		return EgressDecision{Allowed: false, Reason: "no resolved address"}
	}
	for _, ip := range ips {
		if ip == nil {
			return EgressDecision{Allowed: false, Reason: "unresolved address"}
		}
		norm := NormalizeIP(ip)
		if norm.IsUnspecified() || norm.IsMulticast() {
			return EgressDecision{Allowed: false, Reason: "unspecified or multicast address"}
		}
		if IsProhibited(norm) {
			// allowPrivate exists so tests and local fixtures can reach the
			// loopback listener they themselves started. It is not a licence to
			// reach the LAN, cloud metadata or a neighbouring container: those
			// stay closed even in test mode, because "just for tests" is exactly
			// the setting that ends up in production.
			if !p.allowPrivate || !norm.IsLoopback() {
				return EgressDecision{Allowed: false, Reason: "prohibited address range " + norm.String()}
			}
		}
	}
	return EgressDecision{Allowed: true}
}

// NormalizeIP converts IPv4-mapped IPv6 (::ffff:127.0.0.1) to its IPv4 form so
// range checks cannot be bypassed with the 16-byte representation.
func NormalizeIP(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip.To16()
}

// prohibitedNetworks is the non-public address table.
//
// IPv4 entries are stored as 4-byte networks and IPv6 entries as 16-byte ones.
// This split is deliberate: net.ParseCIDR normalises an IPv4-mapped IPv6 prefix
// such as "::ffff:0:0/96" into the 16-byte network 0.0.0.0/0, which then
// matches EVERY IPv4 address and turns the policy into a blanket deny. The
// mapped form is not listed at all, because NormalizeIP collapses it to plain
// IPv4 before the range check.
var prohibitedNetworks = func() []*net.IPNet {
	v4 := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
		"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
	}
	v6 := []string{
		"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
		"2001::/32", "2001:db8::/32", "2001:10::/28", "2002::/16",
		"fc00::/7", "fe80::/10", "ff00::/8",
	}
	out := make([]*net.IPNet, 0, len(v4)+len(v6))
	add := func(c string, bits int) {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(fmt.Sprintf("policy: bad prohibited CIDR %q: %v", c, err))
		}
		ones, _ := n.Mask.Size()
		if ones > bits {
			panic(fmt.Sprintf("policy: prohibited CIDR %q has a %d-bit mask in a %d-bit family", c, ones, bits))
		}
		out = append(out, n)
	}
	for _, c := range v4 {
		add(c, 32)
	}
	for _, c := range v6 {
		add(c, 128)
	}
	return out
}()

// init guards the invariant that regressed once already: no IPv4 entry may be
// wide enough to swallow the entire public IPv4 space.
func init() {
	for _, n := range prohibitedNetworks {
		ones, bits := n.Mask.Size()
		if bits == 32 && ones == 0 {
			panic("policy: a prohibited network covers the whole IPv4 space")
		}
	}
}

// IsProhibited reports whether an address falls in a non-public range.
func IsProhibited(ip net.IP) bool {
	if ip == nil {
		return true
	}
	n := NormalizeIP(ip)
	if n == nil {
		return true
	}
	for _, net := range prohibitedNetworks {
		if net.Contains(n) {
			return true
		}
	}
	return false
}

// IsLoopback reports whether the address is loopback in either family.
func IsLoopback(ip net.IP) bool {
	n := NormalizeIP(ip)
	return n.IsLoopback()
}
