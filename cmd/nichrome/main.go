// Command nichrome runs the browser service.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/api"
	"github.com/xtrcom/xt-nichrome/internal/browser"
	"github.com/xtrcom/xt-nichrome/internal/egress"
	"github.com/xtrcom/xt-nichrome/internal/mcpserver"
	"github.com/xtrcom/xt-nichrome/internal/policy"
	"github.com/xtrcom/xt-nichrome/internal/sessions"
	"github.com/xtrcom/xt-nichrome/internal/stealth"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

// Version is reported by /healthz.
const Version = "1.0.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "nichrome: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	listen       string
	browser      string
	headless     bool
	persona      string
	stateDir     string
	allow        string
	allowPublic  bool
	allowPrivate bool
	token        string
	tokenFile    string
	logLevel     string
	idle         time.Duration
	ephemeral    int
	persistent   int
	tabs         int
	printToken   bool
	fontsDir     string
	mcpHTTP      bool
}

func run() error {
	var opt options
	flag.StringVar(&opt.listen, "listen", "127.0.0.1:7788", "listen address")
	flag.StringVar(&opt.browser, "browser", defaultBrowserPath(), "path to the Chromium binary")
	flag.BoolVar(&opt.headless, "headless", true, "run the browser headless (new headless mode)")
	flag.StringVar(&opt.persona, "persona", "win11-chrome-141", "stealth persona name")
	flag.StringVar(&opt.stateDir, "state-dir", defaultStateDir(), "state directory (profiles, font config)")
	flag.StringVar(&opt.allow, "allow", "", "comma-separated allowed origins, e.g. https://a.test,https://b.test (or '*' for public web)")
	flag.BoolVar(&opt.allowPublic, "allow-public", false, "permit any public http/https origin on ports 80/443 while blocking private/loopback/metadata IPs")
	flag.BoolVar(&opt.allowPrivate, "allow-private", false, "permit requests to private/loopback addresses")
	flag.StringVar(&opt.token, "token", "", "bearer token (default: read or create in the state dir)")
	flag.StringVar(&opt.tokenFile, "token-file", "", "file holding the bearer token")
	flag.StringVar(&opt.logLevel, "log-level", "info", "debug|info|warn|error")
	flag.DurationVar(&opt.idle, "idle-timeout", 10*time.Minute, "evict sessions idle for this long (0 disables)")
	flag.IntVar(&opt.ephemeral, "max-ephemeral", 4, "maximum concurrent ephemeral sessions")
	flag.IntVar(&opt.persistent, "max-persistent", 2, "maximum concurrent persistent sessions")
	flag.IntVar(&opt.tabs, "max-tabs", 4, "maximum tabs per session")
	flag.BoolVar(&opt.printToken, "print-token", false, "print the bearer token to stdout and exit")
	flag.StringVar(&opt.fontsDir, "fonts-dir", defaultFontsDir(), "directory holding the Windows-shaped font pack")
	flag.BoolVar(&opt.mcpHTTP, "mcp-http", false, "mount the Streamable HTTP MCP server at /mcp")
	flag.Parse()

	logger := newLogger(opt.logLevel)

	if opt.printToken {
		tok, err := loadOrCreateToken(opt.stateDir, opt.tokenFile, opt.token)
		if err != nil {
			return err
		}
		fmt.Println(tok)
		return nil
	}

	persona, err := stealth.Lookup(opt.persona)
	if err != nil {
		return fmt.Errorf("%w (known: %s)", err, strings.Join(stealth.Names(), ", "))
	}

	if opt.browser == "" {
		return errors.New("no Chromium binary found; pass -browser")
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
	// MkdirAll leaves an existing directory's mode alone. The state dir holds
	// profiles (cookies) and the bearer token, so if it was created by an
	// earlier version or by hand with a permissive umask, tighten it here.
	if err := tightenDir(stateDir); err != nil {
		return err
	}

	token, err := loadOrCreateToken(stateDir, opt.tokenFile, opt.token)
	if err != nil {
		return err
	}

	// Font surface: generated once, then reused. Chromium is pointed at it via
	// FONTCONFIG_FILE so the host's Linux families never appear in a probe.
	fontConf := filepath.Join(stateDir, "fonts.conf")
	fontCache := filepath.Join(stateDir, "fontcache")
	if err := writeFontConfIfNeeded(fontConf, fontCache, opt.fontsDir); err != nil {
		return err
	}
	if err := verifyFontPack(opt.fontsDir, logger); err != nil {
		return err
	}

	// Egress policy. The proxy is not optional: the browser is launched with all
	// name resolution disabled and every connection pointed at this listener, so
	// there is no configuration in which it reaches the network on its own. The
	// session manager enforces this too, which is why there is no "off" switch to
	// get wrong.
	pol, err := buildPolicy(opt.allow, opt.allowPublic, opt.allowPrivate)
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
	proxyURL := proxy.URL()
	egressInfo := types.EgressInfo{
		Mode: "proxy", ProxyAddr: proxy.Addr(),
		AllowedOrigins: len(pol.Rules()), GuardActive: true,
	}
	// Denials are read from the proxy on demand: the number is the operator's
	// primary signal that the allowlist is doing something.
	egressStats := func() types.EgressInfo {
		info := egressInfo
		info.Denials = proxy.Stats().Denied
		return info
	}
	logger.Info("egress policy active",
		"proxy", proxy.Addr(), "origins", len(pol.Rules()), "allowPrivate", opt.allowPrivate)

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
		ProxyURL:     proxyURL,
		Policy:       pol,
		TempRoot:     tempRoot,
	})
	if err != nil {
		return err
	}

	var mcpHandler http.Handler
	if opt.mcpHTTP {
		ms, err := mcpserver.New(mcpserver.Config{
			Manager:     mgr,
			Version:     Version,
			Egress:      egressInfo,
			EgressStats: egressStats,
		})
		if err != nil {
			mgr.Shutdown(context.Background())
			return err
		}
		mcpHandler = ms.HTTPHandler()
	}

	srv, err := api.New(api.Config{
		Manager: mgr, Token: token, Logger: logger, Version: Version,
		Egress: egressInfo, EgressStats: egressStats, MCPHandler: mcpHandler,
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", opt.listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", opt.listen, err)
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", ln.Addr().String(), "persona", persona.Name, "browser", opt.browser, "version", Version)
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		mgr.Shutdown(context.Background())
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	mgr.Shutdown(shutdownCtx)
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}

// loadOrCreateToken resolves the bearer token: explicit flag, then file, then a
// persisted generated value. The token is never logged.
func loadOrCreateToken(stateDir, tokenFile, explicit string) (string, error) {
	if explicit != "" {
		if len(explicit) < 16 {
			return "", errors.New("the bearer token must be at least 16 characters")
		}
		return explicit, nil
	}
	if tokenFile == "" {
		tokenFile = filepath.Join(stateDir, "token")
	}
	token, err := readToken(tokenFile)
	if !errors.Is(err, os.ErrNotExist) {
		return token, err
	}

	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	token = hex.EncodeToString(entropy[:])
	dir := filepath.Dir(tokenFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(dir, ".nichrome-token-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(temp.Name())

	_, writeErr := temp.WriteString(token + "\n")
	if err := errors.Join(writeErr, temp.Close()); err != nil {
		return "", err
	}
	// Publish the completed file without replacing a token installed by
	// another daemon or a simultaneous -print-token invocation.
	if err := os.Link(temp.Name(), tokenFile); err != nil {
		if errors.Is(err, os.ErrExist) {
			return readToken(tokenFile)
		}
		return "", err
	}
	return token, nil
}

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 16 {
		return "", fmt.Errorf("token file %s does not contain a usable token", path)
	}
	return token, nil
}

// tightenDir removes group and other permissions from an existing directory.
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

func buildPolicy(allow string, allowPublic, allowPrivate bool) (*policy.Policy, error) {
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
	if allowPublic || wildcard {
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

func verifyFontPack(dir string, logger *slog.Logger) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".ttf") || strings.HasSuffix(e.Name(), ".otf")) {
			n++
		}
	}
	if n < 10 {
		return fmt.Errorf("font pack %s has only %d faces; the persona would not be credible", dir, n)
	}
	logger.Debug("font pack ready", "dir", dir, "faces", n)
	return nil
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

// defaultBrowserPath finds the newest pinned Playwright Chromium, then falls back
// to a system chrome. It never returns a headless_shell, which has a reduced
// fingerprint surface.
func defaultBrowserPath() string {
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
