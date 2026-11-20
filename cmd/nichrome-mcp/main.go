// Command nichrome-mcp runs XT-Nichrome as a standalone Model Context Protocol
// server over stdio, launching the local egress policy proxy and sessions
// manager in-process.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xtrcom/xt-nichrome/internal/browser"
	"github.com/xtrcom/xt-nichrome/internal/egress"
	"github.com/xtrcom/xt-nichrome/internal/mcpserver"
	"github.com/xtrcom/xt-nichrome/internal/policy"
	"github.com/xtrcom/xt-nichrome/internal/sessions"
	"github.com/xtrcom/xt-nichrome/internal/stealth"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

// Version is advertised in the MCP server implementation handshake.
const Version = "1.0.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "nichrome-mcp: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	browser      string
	headless     bool
	persona      string
	stateDir     string
	allow        string
	allowPublic  bool
	strictAllow  bool
	allowPrivate bool
	logLevel     string
	idle         time.Duration
	ephemeral    int
	persistent   int
	tabs         int
	fontsDir     string
}

func run() error {
	var opt options
	flag.StringVar(&opt.browser, "browser", defaultBrowserPath(), "path to the Chromium binary")
	flag.BoolVar(&opt.headless, "headless", true, "run the browser headless (new headless mode)")
	flag.StringVar(&opt.persona, "persona", "win11-chrome-141", "stealth persona name")
	flag.StringVar(&opt.stateDir, "state-dir", defaultStateDir(), "state directory (profiles, font config)")
	flag.StringVar(&opt.allow, "allow", os.Getenv("XT_NICHROME_ALLOW"), "comma-separated allowed origins (or '*' for public web; omit for public web with SSRF guard)")
	flag.BoolVar(&opt.allowPublic, "allow-public", os.Getenv("XT_NICHROME_ALLOW_PUBLIC") == "1", "permit any public http/https origin on ports 80/443 while blocking private/loopback/metadata IPs")
	flag.BoolVar(&opt.strictAllow, "strict-allow", false, "require explicit -allow origins even when -allow is empty")
	flag.BoolVar(&opt.allowPrivate, "allow-private", false, "permit requests to loopback addresses")
	flag.StringVar(&opt.logLevel, "log-level", "warn", "debug|info|warn|error (logged to stderr)")
	flag.DurationVar(&opt.idle, "idle-timeout", 10*time.Minute, "evict sessions idle for this long (0 disables)")
	flag.IntVar(&opt.ephemeral, "max-ephemeral", 4, "maximum concurrent ephemeral sessions")
	flag.IntVar(&opt.persistent, "max-persistent", 2, "maximum concurrent persistent sessions")
	flag.IntVar(&opt.tabs, "max-tabs", 4, "maximum tabs per session")
	flag.StringVar(&opt.fontsDir, "fonts-dir", defaultFontsDir(), "directory holding the Windows-shaped font pack")
	flag.Parse()

	logger := newLogger(opt.logLevel)

	persona, err := stealth.Lookup(opt.persona)
	if err != nil {
		return fmt.Errorf("%w (known: %s)", err, strings.Join(stealth.Names(), ", "))
	}

	if opt.browser == "" {
		return errors.New("no Chromium binary found; pass -browser or set XT_NICHROME_BROWSER")
	}
	if st, err := os.Stat(opt.browser); err != nil || st.IsDir() {
		return fmt.Errorf("browser binary %s is not usable", opt.browser)
	}

	stateDir, err := filepath.Abs(opt.stateDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if err := tightenDir(stateDir); err != nil {
		return err
	}

	fontConf := filepath.Join(stateDir, "fonts.conf")
	fontCache := filepath.Join(stateDir, "fontcache")
	if err := writeFontConfIfNeeded(fontConf, fontCache, opt.fontsDir); err != nil {
		logger.Warn("font pack not ready; starting with system fontconfig fallback (run 'make fonts' for full Windows font isolation)", "err", err)
		fontConf = ""
	}

	pol, err := buildPolicy(opt.allow, opt.allowPublic, opt.strictAllow, opt.allowPrivate)
	if err != nil {
		return err
	}
	proxy, err := egress.New(egress.Options{
		Policy: pol,
		Addr:   "127.0.0.1:0",
		Log: func(e egress.Event) {
			if e.Allowed && e.Kind != "denied" {
				logger.Debug("egress", "kind", e.Kind, "host", e.Host, "port", e.Port, "ms", e.DurationMS)
				return
			}
			logger.Info("egress denied", "kind", e.Kind, "host", e.Host, "port", e.Port, "reason", e.Reason)
		},
	})
	if err != nil {
		return fmt.Errorf("egress proxy: %w", err)
	}
	proxy.Start()
	defer proxy.Close()

	egressInfo := types.EgressInfo{
		Mode:           "proxy",
		ProxyAddr:      proxy.Addr(),
		AllowedOrigins: len(pol.Rules()),
		GuardActive:    true,
	}
	egressStats := func() types.EgressInfo {
		info := egressInfo
		info.Denials = proxy.Stats().Denied
		return info
	}

	tempRoot := filepath.Join(stateDir, "tmp")
	if err := os.MkdirAll(tempRoot, 0o700); err != nil {
		return err
	}

	mgr, err := sessions.New(sessions.Config{
		Limits: sessions.Limits{
			MaxEphemeralSessions:  opt.ephemeral,
			MaxPersistentSessions: opt.persistent,
			MaxTabsPerSession:     opt.tabs,
			MaxQueuedOpsPerTab:    8,
			IdleTimeout:           opt.idle,
			OperationTimeout:      45 * time.Second,
		},
		BrowserPath:  opt.browser,
		Headless:     opt.headless,
		ProfileRoot:  filepath.Join(stateDir, "profiles"),
		FontConfPath: fontConf,
		Persona:      persona,
		ProxyURL:     proxy.URL(),
		Policy:       pol,
		TempRoot:     tempRoot,
	})
	if err != nil {
		return err
	}

	srv, err := mcpserver.New(mcpserver.Config{
		Manager:     mgr,
		Version:     Version,
		Egress:      egressInfo,
		EgressStats: egressStats,
	})
	if err != nil {
		mgr.Shutdown(context.Background())
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runErr := srv.Run(ctx, &mcp.StdioTransport{})

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mgr.Shutdown(shutdownCtx)
	return runErr
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelWarn
	}
	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}

func tightenDir(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return os.Chmod(path, 0o700)
}

func buildPolicy(allow string, allowPublic, strictAllow, allowPrivate bool) (*policy.Policy, error) {
	var specs []string
	wildcard := false
	for _, part := range strings.Split(allow, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if p == "*" {
			wildcard = true
			continue
		}
		specs = append(specs, p)
	}
	pol, err := policy.New(specs, allowPrivate)
	if err != nil {
		return nil, err
	}
	if allowPublic || wildcard || (len(specs) == 0 && !strictAllow) {
		pol.SetAllowPublic(true)
	}
	return pol, nil
}

func writeFontConfIfNeeded(path, cacheDir, fontsDir string) error {
	if _, err := os.Stat(fontsDir); err != nil {
		return fmt.Errorf("font pack %s is missing; run deploy/build-fonts.py: %w", fontsDir, err)
	}
	return browser.WriteFontConf(path, fontsDir, cacheDir)
}

func defaultStateDir() string {
	if v := os.Getenv("XT_NICHROME_STATE"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".xt-nichrome"
	}
	return filepath.Join(home, ".local", "share", "xt-nichrome")
}

func defaultFontsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "xt-nichrome", "fonts", "w11")
}

func defaultBrowserPath() string {
	if v := os.Getenv("XT_NICHROME_BROWSER"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err == nil {
		base := filepath.Join(home, ".cache", "ms-playwright")
		if entries, err := os.ReadDir(base); err == nil {
			best := ""
			bestRev := -1
			for _, e := range entries {
				name := e.Name()
				if !strings.HasPrefix(name, "chromium-") {
					continue
				}
				rev, err := strconv.Atoi(strings.TrimPrefix(name, "chromium-"))
				if err != nil {
					continue
				}
				cand := filepath.Join(base, name, "chrome-linux", "chrome")
				if _, err := os.Stat(cand); err != nil {
					continue
				}
				if rev > bestRev {
					best, bestRev = cand, rev
				}
			}
			if best != "" {
				return best
			}
		}
	}
	for _, cand := range []string{
		"/usr/bin/google-chrome-stable", "/usr/bin/google-chrome",
		"/usr/bin/chromium", "/usr/bin/chromium-browser",
		"/snap/bin/chromium",
	} {
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand
		}
	}
	return ""
}
