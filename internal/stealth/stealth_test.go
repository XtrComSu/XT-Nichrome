package stealth

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPersonaIsInternallyConsistent is the property that matters: a detector
// does not check one value, it checks whether the values agree with each other.
func TestPersonaIsInternallyConsistent(t *testing.T) {
	for _, name := range Names() {
		p, err := Lookup(name)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", name, err)
		}
		if err := p.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDesktopChromeIsWindows11(t *testing.T) {
	p := DesktopChrome141()
	if p.Platform != "Win32" {
		t.Errorf("platform is %q", p.Platform)
	}
	if p.OSName != "Windows" {
		t.Errorf("UA-CH platform is %q", p.OSName)
	}
	if p.OSVersion != "15.0.0" {
		t.Errorf("platformVersion is %q; Windows 11 reports 15.0.0 and Windows 10 reports 1.x", p.OSVersion)
	}
	if !strings.Contains(p.UserAgent, "Windows NT 10.0; Win64; x64") {
		t.Errorf("user agent is %q", p.UserAgent)
	}
	if strings.Contains(p.UserAgent, "Headless") {
		t.Error("user agent mentions Headless")
	}
	// Chrome derives appVersion from userAgent by stripping "Mozilla/".
	if got := "Mozilla/" + p.AppVersion; got != p.UserAgent {
		t.Errorf("appVersion %q does not reconstruct userAgent %q", p.AppVersion, p.UserAgent)
	}
}

// TestBrandGreaseMatchesChrome checks UA-CH brand ordering, which is a
// well-known discriminator when it is merely plausible rather than correct.
func TestBrandGreaseMatchesChrome(t *testing.T) {
	p := DesktopChrome141()
	if len(p.Brands) != 3 {
		t.Fatalf("low-entropy brands has %d entries, want 3", len(p.Brands))
	}
	foundChromium := false
	foundGoogle := false
	foundGrease := false
	for _, b := range p.Brands {
		switch b.Brand {
		case "Chromium":
			foundChromium = true
			if b.Version != "141" {
				t.Errorf("Chromium brand version is %q", b.Version)
			}
		case "Google Chrome":
			foundGoogle = true
		}
		if strings.Contains(b.Brand, "Not") {
			foundGrease = true
		}
	}
	if !foundChromium || !foundGoogle || !foundGrease {
		t.Fatalf("brands are %+v", p.Brands)
	}
	if len(p.FullBrands) != len(p.Brands) {
		t.Errorf("fullVersionList has %d entries, brands has %d", len(p.FullBrands), len(p.Brands))
	}
	for _, b := range p.FullBrands {
		if strings.Contains(b.Brand, "Not") {
			// GREASE keeps the single-digit version in both lists.
			if b.Version != "8" {
				t.Errorf("grease entry %q has version %q", b.Brand, b.Version)
			}
			continue
		}
		if !strings.Contains(b.Version, ".") {
			t.Errorf("fullVersionList entry %q has version %q, want a four-part version", b.Brand, b.Version)
		}
	}
}

// TestScriptSubstitutesPersona proves the generated preload is valid JS-shaped
// source with the real values in it.
func TestScriptSubstitutesPersona(t *testing.T) {
	p := DesktopChrome141()
	src, err := Script(p)
	if err != nil {
		t.Fatalf("Script: %v", err)
	}
	if strings.Contains(src, "__PERSONA__") {
		t.Fatal("the placeholder survived substitution")
	}
	if !strings.Contains(src, `"userAgent":"`+p.UserAgent+`"`) {
		t.Error("the generated script does not carry the user agent")
	}
	// The payload must be a single JSON object literal.
	i := strings.Index(src, "const P = ")
	if i < 0 {
		t.Fatal("the patch does not declare the persona binding")
	}
	rest := src[i+len("const P = "):]
	j := strings.Index(rest, ";\n")
	if j < 0 {
		t.Fatal("the persona binding is not terminated")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(rest[:j]), &payload); err != nil {
		t.Fatalf("the persona payload is not valid JSON: %v", err)
	}
	for _, key := range []string{"userAgent", "platform", "screenWidth", "glUnmaskedRenderer", "audioSampleRate"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("the payload is missing %q", key)
		}
	}
}

// TestScriptIsDeterministic keeps the preload cacheable and makes a diff of the
// generated source meaningful.
func TestScriptIsDeterministic(t *testing.T) {
	p := DesktopChrome141()
	a, err := Script(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Script(p)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("two calls produced different sources")
	}
}

// TestSeedKeepsConsistency checks the per-session jitter never produces an
// impossible combination.
func TestSeedKeepsConsistency(t *testing.T) {
	base := DesktopChrome141()
	for _, id := range []string{"a", "b", "c", "session-1", "session-2", ""} {
		p := base.Seed(id)
		if err := p.Validate(); err != nil {
			t.Errorf("Seed(%q): %v", id, err)
		}
		if p.AvailWidth > p.ScreenWidth || p.AvailHeight > p.ScreenHeight {
			t.Errorf("Seed(%q): available area %dx%d exceeds the screen %dx%d",
				id, p.AvailWidth, p.AvailHeight, p.ScreenWidth, p.ScreenHeight)
		}
		if p.ViewportWidth > p.AvailWidth || p.ViewportHeight > p.AvailHeight {
			t.Errorf("Seed(%q): viewport %dx%d exceeds the available area %dx%d",
				id, p.ViewportWidth, p.ViewportHeight, p.AvailWidth, p.AvailHeight)
		}
	}
}

// TestValidateRejectsInconsistentPersonas proves Validate is not a rubber stamp.
func TestValidateRejectsInconsistentPersonas(t *testing.T) {
	cases := map[string]func(*Persona){
		"headless user agent":           func(p *Persona) { p.UserAgent = strings.Replace(p.UserAgent, "Chrome/", "HeadlessChrome/", 1) },
		"linux platform on windows":     func(p *Persona) { p.Platform = "Linux x86_64" },
		"windows platformVersion 1.0.0": func(p *Persona) { p.OSVersion = "1.0.0" },
		"empty user agent":              func(p *Persona) { p.UserAgent = "" },
		"viewport wider than screen":    func(p *Persona) { p.ViewportWidth = p.ScreenWidth + 100 },
		"zero screen":                   func(p *Persona) { p.ScreenWidth = 0 },
		"no languages":                  func(p *Persona) { p.Languages = nil },
		"llvmpipe renderer":             func(p *Persona) { p.GLUnmaskedRenderer = "ANGLE (Mesa, llvmpipe (LLVM 17.0.6, 256 bits), OpenGL 4.5)" },
		"audio 44100 on windows":        func(p *Persona) { p.AudioSampleRate = 44100 },
	}
	for name, mutate := range cases {
		p := DesktopChrome141()
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: Validate accepted an inconsistent persona", name)
		}
	}
}

// TestBuildFlagsKeepsSandboxAndPipe checks the two security-critical flags.
func TestBuildFlagsKeepsSandboxAndPipe(t *testing.T) {
	p := DesktopChrome141()
	cfg := BuildFlags(FlagsInput{
		Persona: p, ExecPath: "/usr/bin/chrome", ProfileDir: "/tmp/p",
		Headless: true, ProxyURL: "http://127.0.0.1:9999",
	})
	joined := strings.Join(cfg.Args, " ")
	for _, forbidden := range []string{"--no-sandbox", "--disable-web-security", "--remote-debugging-port"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("launch args contain %q", forbidden)
		}
	}
	if !strings.Contains(joined, "--headless=new") {
		t.Error("headless launch does not use the new headless mode")
	}
	// --no-proxy-server is presence-tested, so "--no-proxy-server=false" would
	// still disable the proxy and hand the browser a direct network path.
	if strings.Contains(joined, "--no-proxy-server") {
		t.Error("launch args contain a --no-proxy-server spelling")
	}
	if !strings.Contains(joined, "--proxy-bypass-list=<-loopback>") {
		t.Error("loopback is not routed through the proxy")
	}
	if !strings.Contains(joined, "--disable-quic") {
		t.Error("QUIC is enabled, which bypasses an HTTP proxy")
	}
}

// TestResolverRulesExcludeProxyHost is the regression guard for a bug that made
// every navigation fail: the MAP rule also applied to the proxy address.
func TestResolverRulesExcludeProxyHost(t *testing.T) {
	p := DesktopChrome141()
	cfg := BuildFlags(FlagsInput{
		Persona: p, ExecPath: "/usr/bin/chrome", ProfileDir: "/tmp/p",
		ProxyURL: "http://127.0.0.1:4321",
	})
	var rule string
	for _, a := range cfg.Args {
		if strings.HasPrefix(a, "--host-resolver-rules=") {
			rule = strings.TrimPrefix(a, "--host-resolver-rules=")
		}
	}
	if rule == "" {
		t.Fatal("no resolver rules were emitted")
	}
	if !strings.Contains(rule, "MAP * ~NOTFOUND") {
		t.Errorf("resolver rules do not block names: %q", rule)
	}
	if !strings.Contains(rule, "EXCLUDE 127.0.0.1") {
		t.Errorf("resolver rules do not exempt the proxy host, so the browser cannot reach it: %q", rule)
	}
}

func TestProxyHostParsing(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:8080":  "127.0.0.1",
		"http://127.0.0.1":       "127.0.0.1",
		"http://[::1]:8080":      "::1",
		"socks5://10.0.0.1:1080": "10.0.0.1",
		"":                       "127.0.0.1",
	}
	for in, want := range cases {
		if got := proxyHost(in); got != want {
			t.Errorf("proxyHost(%q) = %q, want %q", in, got, want)
		}
	}
}
