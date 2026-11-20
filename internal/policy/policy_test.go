package policy

import (
	"net"
	"testing"

	"github.com/xtrcom/xt-nichrome/internal/types"
)

// TestProhibitedCoversPublicAddresses is the regression guard for a table bug
// that turned the policy into a blanket deny: a "::ffff:0:0/96" entry is parsed
// by net.ParseCIDR into the 16-byte network 0.0.0.0/0, which matches every IPv4
// address on earth.
func TestProhibitedCoversPublicAddresses(t *testing.T) {
	public := []string{
		"1.1.1.1", "8.8.8.8", "93.184.216.34", "142.250.72.14", "203.0.114.1",
		"2606:4700:4700::1111", "2001:4860:4860::8888",
	}
	for _, s := range public {
		if IsProhibited(net.ParseIP(s)) {
			t.Errorf("public address %s was reported prohibited", s)
		}
	}
	private := []string{
		"0.0.0.0", "10.0.0.5", "100.64.1.1", "127.0.0.1", "127.1.2.3",
		"169.254.1.1", "172.16.0.1", "172.31.255.255", "192.0.0.1",
		"192.0.2.5", "192.168.1.1", "198.18.0.1", "198.51.100.1",
		"203.0.113.9", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		"::", "::1", "64:ff9b::1", "100::1", "2001:db8::1",
		"fc00::1", "fd00::1", "fe80::1", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1",
	}
	for _, s := range private {
		if !IsProhibited(net.ParseIP(s)) {
			t.Errorf("private address %s was reported allowed", s)
		}
	}
	if !IsProhibited(nil) {
		t.Error("a nil address must be prohibited")
	}
}

// TestProhibitedTableHasNoBlanketEntry fails loudly if a future edit adds a CIDR
// that swallows a whole family.
func TestProhibitedTableHasNoBlanketEntry(t *testing.T) {
	for _, n := range prohibitedNetworks {
		ones, bits := n.Mask.Size()
		if bits == 0 {
			continue // malformed mask, caught by construction
		}
		if ones == 0 {
			t.Fatalf("prohibited network %s covers the entire address family", n)
		}
	}
}

func TestNormalizeIPCollapsesMappedForm(t *testing.T) {
	cases := map[string]string{
		"::ffff:127.0.0.1": "127.0.0.1",
		"::ffff:10.0.0.1":  "10.0.0.1",
		"::1":              "::1",
		"1.2.3.4":          "1.2.3.4",
	}
	for in, want := range cases {
		if got := NormalizeIP(net.ParseIP(in)).String(); got != want {
			t.Errorf("NormalizeIP(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestParseRule(t *testing.T) {
	valid := map[string]struct {
		scheme, host string
		port         int
	}{
		"https://example.test":     {"https", "example.test", 443},
		"http://example.test":      {"http", "example.test", 80},
		"http://example.test:8080": {"http", "example.test", 8080},
		"https://EXAMPLE.test":     {"https", "example.test", 443},
		"http://127.0.0.1:3000":    {"http", "127.0.0.1", 3000},
	}
	for in, want := range valid {
		r, err := ParseRule(in)
		if err != nil {
			t.Errorf("ParseRule(%q): %v", in, err)
			continue
		}
		if r.Scheme != want.scheme || r.Host != want.host || r.Port != want.port {
			t.Errorf("ParseRule(%q) = %+v, want %s %s %d", in, r, want.scheme, want.host, want.port)
		}
	}
	for _, bad := range []string{
		"", "example.test", "ftp://example.test", "file:///etc/passwd",
		"http://", "http://user:pass@example.test", "http://exa mple.test",
		"javascript:alert(1)",
		// A rule is an origin, not a URL prefix. Accepting a path would create
		// the false impression that the path is enforced.
		"https://example.test/ignored",
		// The wildcard scheme is not supported, and pretending otherwise would
		// mean silently matching every scheme.
		"*://example.test",
	} {
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("ParseRule(%q) accepted an invalid rule", bad)
		}
	}
}

func TestAdmitURL(t *testing.T) {
	p, err := New([]string{"https://example.test", "http://127.0.0.1:8080"}, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ok := []string{
		"https://example.test/",
		"https://example.test/a/b?c=d#e",
		"http://127.0.0.1:8080/form",
	}
	for _, u := range ok {
		if _, apiErr := p.AdmitURL(u); apiErr != nil {
			t.Errorf("AdmitURL(%q) rejected: %v", u, apiErr)
		}
	}
	rejected := map[string]string{
		"https://other.test/":            types.CodePolicyBlocked,
		"http://example.test/":           types.CodePolicyBlocked, // scheme mismatch
		"https://example.test:8443/":     types.CodePolicyBlocked, // port mismatch
		"http://127.0.0.1:9090/":         types.CodePolicyBlocked,
		"file:///etc/passwd":             types.CodeInvalidRequest,
		"javascript:alert(1)":            types.CodeInvalidRequest,
		"http://user:pass@example.test/": types.CodeInvalidRequest,
		"//example.test/":                types.CodeInvalidRequest,
		"":                               types.CodeInvalidRequest,
		"https://":                       types.CodeInvalidRequest,
	}
	for u, code := range rejected {
		_, apiErr := p.AdmitURL(u)
		if apiErr == nil {
			t.Errorf("AdmitURL(%q) was admitted", u)
			continue
		}
		if apiErr.Code != code {
			t.Errorf("AdmitURL(%q) = %s, want %s", u, apiErr.Code, code)
		}
	}
}

func TestAdmitURLIsOriginOnly(t *testing.T) {
	p, err := New([]string{"https://example.test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	// A subdomain is a different origin and must not be admitted by a suffix
	// match; that mistake is how an allowlist turns into a wildcard.
	if _, apiErr := p.AdmitURL("https://evil.example.test/"); apiErr == nil {
		t.Error("a subdomain of an allowed host was admitted")
	}
	// Credentials disguised in the userinfo must be rejected, not stripped.
	if _, apiErr := p.AdmitURL("https://example.test@evil.test/"); apiErr == nil {
		t.Error("a host-confusion URL was admitted")
	}
}

func TestCheckEgressDeniesUnlistedPorts(t *testing.T) {
	p, err := New([]string{"https://example.test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if d := p.CheckEgress("example.test", 443); !d.Allowed {
		t.Fatalf("the allowed origin was denied: %s", d.Reason)
	}
	if d := p.CheckEgress("example.test", 22); d.Allowed {
		t.Error("port 22 was allowed on an https-only origin")
	}
	if d := p.CheckEgress("other.test", 443); d.Allowed {
		t.Error("an unlisted host was allowed")
	}
}

func TestCheckIPsRequiresAllAddressesPublic(t *testing.T) {
	p, err := New([]string{"https://example.test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	// A name that answers with both a public and a private address is the
	// classic rebinding setup; allowing it because one answer is public is wrong.
	mixed := []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("127.0.0.1")}
	if d := p.CheckIPs(mixed); d.Allowed {
		t.Error("a mixed public/private answer set was allowed")
	}
	if d := p.CheckIPs([]net.IP{net.ParseIP("93.184.216.34")}); !d.Allowed {
		t.Errorf("a public answer was denied: %s", d.Reason)
	}
	if d := p.CheckIPs(nil); d.Allowed {
		t.Error("an empty answer set was allowed")
	}
}

func TestAllowPrivatePermitsLoopback(t *testing.T) {
	p, err := New([]string{"http://127.0.0.1:8080"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if d := p.CheckIPs([]net.IP{net.ParseIP("127.0.0.1")}); !d.Allowed {
		t.Fatalf("allowPrivate did not permit loopback: %s", d.Reason)
	}
	// Even with allowPrivate, an unrelated private range stays closed.
	if d := p.CheckIPs([]net.IP{net.ParseIP("10.1.2.3")}); d.Allowed {
		t.Error("allowPrivate also opened an unrelated private range")
	}
}

func TestAllowPublicWithSSRFProtection(t *testing.T) {
	p, err := New(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	p.SetAllowPublic(true)
	if !p.AllowPublic() {
		t.Fatal("expected AllowPublic() to be true")
	}
	if _, apiErr := p.AdmitURL("https://news.ycombinator.com/item?id=1"); apiErr != nil {
		t.Fatalf("expected public https URL to be admitted, got %v", apiErr)
	}
	if _, apiErr := p.AdmitURL("http://example.com/"); apiErr != nil {
		t.Fatalf("expected public http URL to be admitted, got %v", apiErr)
	}
	if _, apiErr := p.AdmitURL("https://example.com:8443/"); apiErr == nil {
		t.Fatal("expected non-standard port 8443 to be rejected without explicit rule")
	}
	if d := p.CheckEgress("news.ycombinator.com", 443); !d.Allowed {
		t.Fatalf("expected port 443 egress to be allowed: %s", d.Reason)
	}
	if d := p.CheckEgress("news.ycombinator.com", 22); d.Allowed {
		t.Fatal("expected port 22 egress to be denied")
	}
	// SSRF protection via CheckIPs stays strictly enforced.
	if d := p.CheckIPs([]net.IP{net.ParseIP("169.254.169.254")}); d.Allowed {
		t.Fatal("cloud metadata IP must remain blocked even when allowPublic is true")
	}
	if d := p.CheckIPs([]net.IP{net.ParseIP("127.0.0.1")}); d.Allowed {
		t.Fatal("loopback IP must remain blocked when allowPrivate is false")
	}
}
