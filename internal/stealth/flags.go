package stealth

import (
	"runtime"
	"strings"
)

// LaunchConfig is the browser command line plus the environment overrides that
// make the persona coherent.
type LaunchConfig struct {
	Args []string
	Env  []string
}

// FlagsInput describes what the caller knows about the target environment.
type FlagsInput struct {
	Persona    Persona
	ExecPath   string
	ProfileDir string
	// Headless selects the new headless mode. Headed mode needs a display
	// server; the persona claims a desktop either way.
	Headless bool
	// ProxyURL is the policy-enforcing egress proxy.
	ProxyURL string
	// FontConfPath points Chromium at the Windows-metric fontconfig file.
	FontConfPath string
	// ProxyResolverRules, when empty, blocks all direct name resolution.
	ProxyResolverRules string
	// Languages for --lang / --accept-lang.
	Languages string
	// ExtraArgs are appended verbatim (tests use this).
	ExtraArgs []string
}

// BaseArgs are the non-negotiable sandbox/integrity flags.
func BaseArgs() []string {
	return []string{
		// Keep the renderer sandbox on (brief §1). Never add --no-sandbox here.
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--disable-client-side-phishing-detection",
		"--disable-default-apps",
		"--disable-domain-reliability",
		"--disable-component-update",
		"--disable-breakpad",
		"--disable-crash-reporter",
		"--disable-dev-shm-usage",
		"--metrics-recording-only",
		"--no-service-autorun",
		"--password-store=basic",
		"--use-mock-keychain",
		// Do not announce automation in the UA or via the automation infobar.
		"--disable-blink-features=AutomationControlled",
		"--disable-features=Translate,OptimizationHints,MediaRouter,CalculateNativeWinOcclusion,InterestFeedContentSuggestions,AutomationControlled,IsolateOrigins,site-per-process",
		"--enable-features=NetworkService,NetworkServiceInProcess",
		"--exclude-switches=enable-automation",
		"--disable-ipc-flooding-protection",
		"--disable-hang-monitor",
		"--disable-prompt-on-repost",
		"--disable-sync",
		"--disable-extensions",
		"--disable-component-extensions-with-background-pages",
		"--disable-search-engine-choice-screen",
		"--no-pings",
		"--disable-logging",
		// Media: no fake-device filling; keep default enumerated devices off
		// until permissions are granted explicitly.
		"--use-fake-ui-for-media-stream=false",
		"--autoplay-policy=no-user-gesture-required",
		// Downloads off (brief §6, first release).
		"--disable-features=DownloadBubble,DownloadBubbleV2",
	}
}

// BuildFlags assembles the full launch configuration.
func BuildFlags(in FlagsInput) LaunchConfig {
	args := BaseArgs()

	if in.Headless {
		// "new" headless is the same browser code path as headed Chrome, which
		// is why the JS surface is complete. The old headless shell is a
		// different binary with a much smaller surface.
		args = append(args, "--headless=new")
		args = append(args, "--hide-scrollbars", "--mute-audio")
	} else {
		args = append(args,
			"--start-maximized",
			"--window-position=0,0",
			"--window-size="+itoa(in.Persona.ScreenWidth)+","+itoa(in.Persona.ScreenHeight),
		)
	}

	if in.ProfileDir != "" {
		args = append(args, "--user-data-dir="+in.ProfileDir)
	}
	if in.Languages != "" {
		args = append(args, "--lang="+in.Languages, "--accept-lang="+in.Languages)
	}

	// Persona identity at the process level. The authoritative identity is set
	// per-context through Network.setUserAgentOverride; these keep the process
	// from advertising Linux before any context exists.
	args = append(args,
		"--user-agent="+in.Persona.UserAgent,
		"--window-size="+itoa(in.Persona.ViewportWidth)+","+itoa(in.Persona.ViewportHeight),
	)

	// Graphics: SwiftShader is the only reliable renderer in a container, but its
	// strings are a fingerprint. We keep the real renderer (so rendering, and
	// therefore canvas/WebGL pixel output, is authentic) and align the reported
	// strings through getParameter. --use-angle selects the backend.
	if runtime.GOOS == "linux" {
		args = append(args,
			"--use-gl=angle",
			"--use-angle=swiftshader",
			"--enable-unsafe-swiftshader",
			"--disable-gpu-driver-bug-workarounds",
		)
	}

	// Privacy / retention: no network prediction, no prefetch, no preconnect.
	// Note: do NOT pass --no-proxy-server=false. Chromium bool switches are
	// presence-tested, so that spelling *enables* --no-proxy-server and silently
	// disables the policy proxy, leaving the browser to resolve names itself.
	args = append(args,
		"--disable-features=NetworkPrediction,PreconnectToSearch,SpeculationRulesPrefetch",
	)

	if in.FontConfPath != "" {
		// Applied via env (FONTCONFIG_FILE); kept here for callers that log it.
	}

	if in.ProxyURL != "" {
		args = append(args,
			"--proxy-server="+in.ProxyURL,
			// <-loopback> cancels Chromium's implicit localhost bypass. Without
			// it, every request to 127.0.0.1 goes direct and skips the policy
			// boundary entirely - which is exactly the traffic a test suite uses.
			"--proxy-bypass-list=<-loopback>",
		)
		rules := in.ProxyResolverRules
		if rules == "" {
			// Every name fails to resolve inside the browser; the proxy is the
			// only component that resolves. Closes the DNS-rebinding window.
			//
			// EXCLUDE is mandatory, not cosmetic: the resolver rules apply to
			// the proxy's own address too, so a bare "MAP * ~NOTFOUND" makes the
			// browser unable to resolve the proxy and every navigation fails
			// with ERR_PROXY_CONNECTION_FAILED before a single request is sent.
			rules = "MAP * ~NOTFOUND,EXCLUDE " + proxyHost(in.ProxyURL)
		}
		args = append(args, "--host-resolver-rules="+rules)
		// QUIC bypasses HTTP proxies; WebRTC can send UDP directly.
		args = append(args,
			"--disable-quic",
			"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
			"--webrtc-ip-handling-policy=disable_non_proxied_udp",
			"--disable-features=WebRtcHideLocalIpsWithMdns",
		)
	}

	args = append(args, in.ExtraArgs...)

	env := []string{
		// Keep the environment a Windows desktop would have, minus anything
		// pointing at the Linux host's identity.
		"TZ=" + in.Persona.TimeZone,
		"LANG=" + strings.ReplaceAll(in.Persona.Locale, "-", "_") + ".UTF-8",
		"LC_ALL=" + strings.ReplaceAll(in.Persona.Locale, "-", "_") + ".UTF-8",
		"LANGUAGE=" + in.Persona.Languages[0],
	}
	if in.FontConfPath != "" {
		env = append(env, "FONTCONFIG_FILE="+in.FontConfPath)
	}
	return LaunchConfig{Args: args, Env: env}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// proxyHost extracts the host from a proxy URL for the resolver EXCLUDE clause.
// The proxy listens on a loopback IP literal (a hostname could not be resolved
// once the MAP rule is in force), so this is normally "127.0.0.1".
func proxyHost(proxyURL string) string {
	h := proxyURL
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if strings.HasPrefix(h, "[") {
		if j := strings.Index(h, "]"); j >= 0 {
			return h[1:j]
		}
	}
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	if h == "" {
		return "127.0.0.1"
	}
	return h
}
