// Command nichrome-cli drives a running nichrome service from a shell or a
// script. It is a thin client: every decision about what is allowed lives in the
// daemon, so the CLI can never be more permissive than the API.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "nichrome-cli: %v\n", err)
		os.Exit(1)
	}
}

type client struct {
	base  string
	token string
	http  *http.Client
	json  bool
}

func run() error {
	var (
		base      = flag.String("url", envOr("XT_NICHROME_URL", "http://127.0.0.1:7788"), "service base URL")
		tokenFile = flag.String("token-file", "", "file holding the bearer token")
		stateDir  = flag.String("state-dir", defaultStateDir(), "state directory holding the token")
		timeout   = flag.Duration("timeout", 120*time.Second, "per-request timeout")
		asJSON    = flag.Bool("json", false, "print raw JSON")
	)
	flag.Usage = usage
	flag.Parse()

	if flag.NArg() == 0 {
		usage()
		return errors.New("a command is required")
	}
	token, err := loadToken(*tokenFile, *stateDir)
	if err != nil {
		return err
	}
	c := &client{
		base:  strings.TrimRight(*base, "/"),
		token: token,
		http:  &http.Client{Timeout: *timeout},
		json:  *asJSON,
	}
	return dispatch(c, flag.Arg(0), flag.Args()[1:])
}

// cmdFlags holds the flags every resource command accepts. Registering them per
// subcommand rather than only globally means "snapshot -json" works, which is
// how people actually type it.
type cmdFlags struct {
	fs       *flag.FlagSet
	session  string
	tab      string
	json     bool
	maxChars int
	maxRefs  int
	values   bool
	mode     string
	profile  string
	persona  string
}

func newCmd(cmd string, args []string, opts cmdOpts) (*cmdFlags, error) {
	cf := &cmdFlags{fs: flag.NewFlagSet(cmd, flag.ContinueOnError)}
	cf.fs.SetOutput(os.Stderr)
	cf.fs.BoolVar(&cf.json, "json", false, "print raw JSON")
	if opts.session || opts.tab {
		cf.fs.StringVar(&cf.session, "session", "", "session id")
	}
	if opts.tab {
		cf.fs.StringVar(&cf.tab, "tab", "", "tab id")
	}
	if opts.snapshot {
		cf.fs.IntVar(&cf.maxChars, "max-chars", 0, "character budget")
		cf.fs.IntVar(&cf.maxRefs, "max-refs", 0, "reference budget")
		cf.fs.BoolVar(&cf.values, "values", false, "include field values")
	}
	if opts.newSession {
		cf.fs.StringVar(&cf.mode, "mode", "ephemeral", "ephemeral or persistent")
		cf.fs.StringVar(&cf.profile, "profile", "", "profile id (persistent only)")
		cf.fs.StringVar(&cf.persona, "persona", "", "persona name")
	}
	if err := cf.fs.Parse(splitFlags(args)); err != nil {
		return nil, err
	}
	if cf.fs.NArg() > 0 && !opts.wantArgs {
		return nil, fmt.Errorf("%s takes no positional arguments, got %q", cmd, cf.fs.Args())
	}
	if (opts.session || opts.tab) && cf.session == "" {
		return nil, errors.New("-session is required")
	}
	if opts.tab && cf.tab == "" {
		return nil, errors.New("-tab is required")
	}
	return cf, nil
}

// splitFlags moves flags ahead of positional arguments.
//
// Go's flag package stops at the first non-flag argument, so "go URL -json"
// would silently treat -json as a second URL. Reordering here keeps the natural
// "verb then object then options" order working. A literal argument that begins
// with "-" is still expressible by putting it after "--".
func splitFlags(args []string) []string {
	var flags, positional []string
	terminated := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if terminated {
			positional = append(positional, a)
			continue
		}
		if a == "--" {
			terminated = true
			continue
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			// A flag written as "-name value" needs its value kept adjacent.
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				if knownValueFlag(strings.TrimLeft(a, "-")) {
					flags = append(flags, args[i+1])
					i++
				}
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

// knownValueFlag reports whether a flag takes a separate value, so the value is
// not mistaken for a positional argument.
func knownValueFlag(name string) bool {
	switch name {
	case "session", "tab", "max-chars", "max-refs", "mode", "profile", "persona",
		"url", "token-file", "state-dir", "timeout":
		return true
	}
	return false
}

type cmdOpts struct {
	session, tab, snapshot, newSession, wantArgs bool
}

func dispatch(c *client, cmd string, args []string) error {
	switch cmd {
	case "health", "ready", "sessions":
		cf, err := newCmd(cmd, args, cmdOpts{})
		if err != nil {
			return err
		}
		c.json = c.json || cf.json
		return c.get(map[string]string{
			"health": "/healthz", "ready": "/readyz", "sessions": "/v1/sessions",
		}[cmd])

	case "new-session":
		cf, err := newCmd(cmd, args, cmdOpts{newSession: true})
		if err != nil {
			return err
		}
		body := map[string]any{"mode": cf.mode}
		if cf.profile != "" {
			body["profileId"] = cf.profile
		}
		if cf.persona != "" {
			body["persona"] = cf.persona
		}
		c.json = c.json || cf.json
		return c.post("/v1/sessions", body)

	case "kill-session":
		cf, err := newCmd(cmd, args, cmdOpts{session: true})
		if err != nil {
			return err
		}
		c.json = c.json || cf.json
		return c.delete("/v1/sessions/" + cf.session)

	case "tabs":
		cf, err := newCmd(cmd, args, cmdOpts{session: true})
		if err != nil {
			return err
		}
		c.json = c.json || cf.json
		return c.get("/v1/sessions/" + cf.session + "/tabs")

	case "new-tab":
		cf, err := newCmd(cmd, args, cmdOpts{session: true, wantArgs: true})
		if err != nil {
			return err
		}
		body := map[string]any{}
		if rest := cf.fs.Args(); len(rest) > 0 {
			body["url"] = rest[0]
		}
		c.json = c.json || cf.json
		return c.post("/v1/sessions/"+cf.session+"/tabs", body)

	case "snapshot":
		cf, err := newCmd(cmd, args, cmdOpts{session: true, tab: true, snapshot: true})
		if err != nil {
			return err
		}
		var q []string
		if cf.maxChars > 0 {
			q = append(q, "maxChars="+strconv.Itoa(cf.maxChars))
		}
		if cf.maxRefs > 0 {
			q = append(q, "maxRefs="+strconv.Itoa(cf.maxRefs))
		}
		if cf.values {
			q = append(q, "includeValues=true")
		}
		path := tabPath(cf) + "/snapshot"
		if len(q) > 0 {
			path += "?" + strings.Join(q, "&")
		}
		c.json = c.json || cf.json
		return c.get(path)

	case "close-tab", "persona", "screenshot", "go", "click", "fill":
		cf, err := newCmd(cmd, args, cmdOpts{session: true, tab: true, wantArgs: true})
		if err != nil {
			return err
		}
		c.json = c.json || cf.json
		rest := cf.fs.Args()
		base := tabPath(cf)
		switch cmd {
		case "close-tab":
			if len(rest) != 0 {
				return errors.New("close-tab takes no positional arguments")
			}
			return c.delete(base)
		case "persona":
			return c.get(base + "/persona")
		case "screenshot":
			out := "screenshot.png"
			if len(rest) > 0 {
				out = rest[0]
			}
			return c.screenshot(base+"/screenshot", out)
		case "go":
			if len(rest) != 1 {
				return errors.New("usage: go -session S -tab T URL")
			}
			return c.post(base+"/navigate", map[string]any{"url": rest[0]})
		case "click":
			if len(rest) != 2 {
				return errors.New("usage: click -session S -tab T SNAPSHOT_ID REF")
			}
			return c.post(base+"/actions", map[string]any{
				"kind": "click", "snapshotId": rest[0], "ref": rest[1],
			})
		case "fill":
			if len(rest) != 3 {
				return errors.New("usage: fill -session S -tab T SNAPSHOT_ID REF TEXT")
			}
			return c.post(base+"/actions", map[string]any{
				"kind": "fill", "snapshotId": rest[0], "ref": rest[1], "text": rest[2],
			})
		}
	}
	usage()
	return fmt.Errorf("unknown command %q", cmd)
}

func tabPath(cf *cmdFlags) string {
	return "/v1/sessions/" + cf.session + "/tabs/" + cf.tab
}

func (c *client) do(method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", c.base, err)
	}
	return resp, nil
}

// fail turns a non-2xx response into an error carrying the service's own code,
// so a script can branch on the reason instead of parsing prose.
func fail(resp *http.Response, data []byte) error {
	var e struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Detail  map[string]any `json:"detail"`
	}
	if err := json.Unmarshal(data, &e); err == nil && e.Code != "" {
		msg := e.Code + ": " + e.Message
		if len(e.Detail) > 0 {
			if b, err := json.Marshal(e.Detail); err == nil {
				msg += " " + string(b)
			}
		}
		return fmt.Errorf("%s (HTTP %d)", msg, resp.StatusCode)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
}

// roundTrip is the shared body handling for the JSON verbs.
func (c *client) roundTrip(method, path string, body any, expectBody bool) error {
	resp, err := c.do(method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fail(resp, data)
	}
	if !expectBody || len(bytes.TrimSpace(data)) == 0 {
		fmt.Println("ok")
		return nil
	}
	return c.printBody(data)
}

func (c *client) get(path string) error { return c.roundTrip("GET", path, nil, true) }
func (c *client) post(path string, b any) error {
	return c.roundTrip("POST", path, b, true)
}
func (c *client) delete(path string) error { return c.roundTrip("DELETE", path, nil, false) }

func (c *client) screenshot(path, out string) error {
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fail(resp, data)
	}
	if err := os.WriteFile(out, data, 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d bytes)\n", out, len(data))
	return nil
}

// printBody renders JSON readably. A snapshot's text is the point of the call,
// so it is printed as text rather than escaped through a JSON encoder.
func (c *client) printBody(data []byte) error {
	if c.json {
		fmt.Println(strings.TrimSpace(string(data)))
		return nil
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		fmt.Println(strings.TrimSpace(string(data)))
		return nil
	}
	if m, ok := generic.(map[string]any); ok {
		if text, ok := m["text"].(string); ok {
			for _, key := range []string{"snapshotId", "generation", "url", "refCount", "truncated"} {
				if v, ok := m[key]; ok {
					fmt.Printf("%s: %v\n", key, v)
				}
			}
			if v, ok := m["unsupportedFrames"]; ok {
				fmt.Printf("unsupportedFrames: %v\n", v)
			}
			fmt.Println("---")
			fmt.Print(text)
			if !strings.HasSuffix(text, "\n") {
				fmt.Println()
			}
			return nil
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(generic)
}

func loadToken(tokenFile, stateDir string) (string, error) {
	if v := os.Getenv("XT_NICHROME_TOKEN"); v != "" {
		return v, nil
	}
	if tokenFile == "" {
		tokenFile = filepath.Join(stateDir, "token")
	}
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return "", fmt.Errorf("no bearer token: set XT_NICHROME_TOKEN or pass -token-file (%w)", err)
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("%s is empty", tokenFile)
	}
	return tok, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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

func usage() {
	fmt.Fprintf(os.Stderr, `nichrome-cli - drive a running nichrome browser service

Usage:
  nichrome-cli [global flags] COMMAND [flags] [args]

Global flags (accepted before or after the command):
  -url URL           service base URL (default %s)
  -token-file PATH   bearer token file
  -state-dir DIR     state directory holding the token
  -timeout DURATION  per-request timeout
  -json              print raw JSON

Commands:
  health                                   liveness
  ready                                    readiness, capacity and egress state
  sessions                                 list sessions
  new-session [-mode m] [-profile p]       create a session
  kill-session -session S                  destroy a session
  tabs -session S                          list tabs
  new-tab -session S [URL]                 open a tab
  close-tab -session S -tab T              close a tab
  go -session S -tab T URL                 navigate
  snapshot -session S -tab T [-max-chars n] [-max-refs n] [-values]
  click -session S -tab T SNAPSHOT REF     click a referenced element
  fill -session S -tab T SNAPSHOT REF TXT  fill a referenced field
  persona -session S -tab T                report the observed identity
  screenshot -session S -tab T [FILE]      save a PNG

Environment:
  XT_NICHROME_URL      base URL
  XT_NICHROME_TOKEN    bearer token
  XT_NICHROME_STATE    state directory

Example:
  nichrome-cli new-session -mode persistent -profile agent-a
  nichrome-cli go -session sess_1 -tab tab_1 https://example.test/form
  nichrome-cli snapshot -session sess_1 -tab tab_1
  nichrome-cli fill -session sess_1 -tab tab_1 s1a2b3c4d5 e3 "Ada Lovelace"
`, envOr("XT_NICHROME_URL", "http://127.0.0.1:7788"))
}
