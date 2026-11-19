package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xtrcom/xt-nichrome/internal/browser"
	"github.com/xtrcom/xt-nichrome/internal/companion"
	"github.com/xtrcom/xt-nichrome/internal/egress"
	"github.com/xtrcom/xt-nichrome/internal/fixtures"
	"github.com/xtrcom/xt-nichrome/internal/policy"
	"github.com/xtrcom/xt-nichrome/internal/sessions"
	"github.com/xtrcom/xt-nichrome/internal/stealth"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

func testManager(t *testing.T, allowedOrigins []string) (*sessions.Manager, func()) {
	t.Helper()
	pol, err := policy.New(allowedOrigins, true)
	if err != nil {
		t.Fatalf("policy.New: %v", err)
	}
	proxy, err := egress.New(egress.Options{
		Policy: pol,
		Addr:   "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("egress.New: %v", err)
	}
	proxy.Start()

	stateDir := t.TempDir()
	fontConf := ""
	home, _ := os.UserHomeDir()
	fontsDir := filepath.Join(home, ".local", "share", "xt-nichrome", "fonts", "w11")
	if st, err := os.Stat(fontsDir); err == nil && st.IsDir() {
		fc := filepath.Join(stateDir, "fonts.conf")
		if err := browser.WriteFontConf(fc, fontsDir, filepath.Join(stateDir, "fontcache")); err == nil {
			fontConf = fc
		}
	}

	bPath := resolveBrowserForTest(t)
	mgr, err := sessions.New(sessions.Config{
		Limits:       sessions.DefaultLimits(),
		BrowserPath:  bPath,
		Headless:     true,
		ProfileRoot:  filepath.Join(stateDir, "profiles"),
		FontConfPath: fontConf,
		Persona:      stealth.DesktopChrome141(),
		ProxyURL:     proxy.URL(),
		Policy:       pol,
		TempRoot:     filepath.Join(stateDir, "tmp"),
	})
	if err != nil {
		proxy.Close()
		t.Fatalf("sessions.New: %v", err)
	}
	if ctrl, ok := mgr.CompanionController().(*companion.Controller); ok {
		ctrl.SetDisableExec(true)
	}
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		mgr.Shutdown(ctx)
		proxy.Close()
	}
	return mgr, cleanup
}

func resolveBrowserForTest(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("XT_NICHROME_BROWSER"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err == nil {
		base := filepath.Join(home, ".cache", "ms-playwright")
		if entries, err := os.ReadDir(base); err == nil {
			best, bestRev := "", -1
			for _, e := range entries {
				name := e.Name()
				if !strings.HasPrefix(name, "chromium-") || strings.Contains(name, "headless") {
					continue
				}
				rev, err := strconv.Atoi(strings.TrimPrefix(name, "chromium-"))
				if err != nil {
					continue
				}
				cand := filepath.Join(base, name, "chrome-linux", "chrome")
				if st, err := os.Stat(cand); err == nil && !st.IsDir() && rev > bestRev {
					best, bestRev = cand, rev
				}
			}
			if best != "" {
				return best
			}
		}
	}
	return "/bin/true"
}

func TestToolRegistrationAndSchemas(t *testing.T) {
	mgr, cleanup := testManager(t, []string{"https://example.test"})
	defer cleanup()

	srv, err := New(Config{
		Manager: mgr,
		Version: "1.0.0",
		Egress:  types.EgressInfo{Mode: "proxy", GuardActive: true, AllowedOrigins: 1},
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t1, t2 := mcp.NewInMemoryTransports()
	serverSess, err := srv.MCP().Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	defer serverSess.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	clientSess, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client Connect: %v", err)
	}
	defer clientSess.Close()

	res, err := clientSess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	wantTools := []string{
		"browser_open_session",
		"browser_close_session",
		"browser_list_sessions",
		"browser_open_tab",
		"browser_close_tab",
		"browser_list_tabs",
		"browser_navigate",
		"browser_snapshot",
		"browser_click",
		"browser_fill",
		"browser_screenshot",
		"browser_persona",
		"browser_ready",
		"browser_hover",
		"browser_scroll",
		"browser_press_key",
		"browser_select_option",
		"browser_check",
		"browser_uncheck",
		"browser_drag",
		"browser_navigate_back",
		"browser_navigate_forward",
		"browser_reload",
		"browser_wait_for",
		"browser_extract",
		"browser_cookies",
		"browser_telemetry",
		"browser_evaluate",
		"browser_pdf",
		"browser_storage",
		"browser_upload_file",
		"browser_dialogs",
		"companion_cursor_guide",
		"companion_cursor_control",
		"companion_analyze_selection",
	}
	got := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		got[tool.Name] = tool
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("tool %q has empty description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has nil InputSchema", tool.Name)
		}
	}
	for _, name := range wantTools {
		if _, ok := got[name]; !ok {
			t.Errorf("missing expected tool %q", name)
		}
	}
}

func TestFormatErrorMappings(t *testing.T) {
	cases := []struct {
		code       string
		wantSubstr string
	}{
		{types.CodeStaleRef, "browser_snapshot"},
		{types.CodeAmbiguousRef, "browser_snapshot"},
		{types.CodeElementNotActionable, "browser_snapshot"},
		{types.CodePolicyBlocked, "allowlist"},
		{types.CodeCapacity, "browser_close_tab"},
		{types.CodeBusyProfile, "busy"},
		{types.CodeNotFound, "browser_list_sessions"},
		{types.CodeTimeout, "browser_snapshot"},
		{types.CodeActionOutcomeUnknown, "Do NOT blindly retry"},
		{types.CodeBrowserFailed, "browser_open_session"},
		{types.CodeInvalidRequest, "invalid tool argument"},
		{types.CodeInternal, "browser_ready"},
	}
	for _, tc := range cases {
		err := FormatError(&types.APIError{
			Code:    tc.code,
			Message: "simulated error",
			Detail:  map[string]any{"field": "test"},
		})
		if err == nil {
			t.Fatalf("FormatError(%s) returned nil", tc.code)
		}
		s := err.Error()
		if !strings.Contains(s, tc.code) {
			t.Errorf("FormatError(%s) = %q, missing code", tc.code, s)
		}
		if !strings.Contains(s, tc.wantSubstr) {
			t.Errorf("FormatError(%s) = %q, missing guidance %q", tc.code, s, tc.wantSubstr)
		}
	}
}

func TestSessionAffinityAndClientTabCap(t *testing.T) {
	mgr, cleanup := testManager(t, []string{"https://example.test"})
	defer cleanup()

	srv, err := New(Config{
		Manager:          mgr,
		MaxTabsPerClient: 2,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Without any remembered session, resolveSession fails with INVALID_REQUEST.
	if _, apiErr := srv.resolveSession(nil, ""); apiErr == nil || apiErr.Code != types.CodeInvalidRequest {
		t.Fatalf("expected INVALID_REQUEST when no session remembered, got %v", apiErr)
	}

	srv.rememberSession(nil, "sess_1")
	sid, apiErr := srv.resolveSession(nil, "")
	if apiErr != nil || sid != "sess_1" {
		t.Fatalf("resolveSession got (%q, %v), want sess_1", sid, apiErr)
	}
	// Explicit override wins over remembered session.
	sidOverride, apiErr := srv.resolveSession(nil, "sess_override")
	if apiErr != nil || sidOverride != "sess_override" {
		t.Fatalf("resolveSession override got (%q, %v)", sidOverride, apiErr)
	}

	// Remember tab & snapshot.
	srv.rememberTab(nil, "sess_1", "tab_1")
	srv.rememberSnapshot(nil, "sess_1", "tab_1", "snap_abc")
	gotSid, gotTid, apiErr := srv.resolveSessionAndTab(nil, "", "")
	if apiErr != nil || gotSid != "sess_1" || gotTid != "tab_1" {
		t.Fatalf("resolveSessionAndTab got (%q, %q, %v)", gotSid, gotTid, apiErr)
	}
	if snap := srv.resolveSnapshotID(nil, "tab_1", ""); snap != "snap_abc" {
		t.Fatalf("resolveSnapshotID got %q, want snap_abc", snap)
	}

	// Second tab reaches MaxTabsPerClient=2.
	srv.rememberTab(nil, "sess_1", "tab_2")
	if capErr := srv.checkClientTabCap(nil); capErr == nil || capErr.Code != types.CodeCapacity {
		t.Fatalf("expected CAPACITY error at tab cap, got %v", capErr)
	}

	// Closing a tab frees the client tab cap slot.
	srv.forgetTab(nil, "sess_1", "tab_2")
	if capErr := srv.checkClientTabCap(nil); capErr != nil {
		t.Fatalf("unexpected error after forgetTab: %v", capErr)
	}

	// Forgetting the session clears remembered state.
	srv.forgetSession(nil, "sess_1")
	if _, apiErr := srv.resolveSession(nil, ""); apiErr == nil {
		t.Fatal("expected error after forgetSession")
	}
}

func TestProtocolRoundTripWithRealBrowser(t *testing.T) {
	bPath := resolveBrowserForTest(t)
	if bPath == "/bin/true" {
		t.Skip("no real Chromium binary available")
	}

	fxSrv := fixtures.New()
	ts := httptest.NewServer(fxSrv.Handler())
	defer ts.Close()

	mgr, cleanup := testManager(t, []string{ts.URL})
	defer cleanup()

	srv, err := New(Config{
		Manager: mgr,
		Version: "1.0.0",
		Egress:  types.EgressInfo{Mode: "proxy", GuardActive: true, AllowedOrigins: 1},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t1, t2 := mcp.NewInMemoryTransports()
	serverSess, err := srv.MCP().Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	defer serverSess.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-agent", Version: "1.0.0"}, nil)
	clientSess, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client Connect: %v", err)
	}
	defer clientSess.Close()

	callTool := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		res, err := clientSess.CallTool(ctx, &mcp.CallToolParams{
			Name:      name,
			Arguments: args,
		})
		if err != nil {
			t.Fatalf("CallTool(%s) protocol error: %v", name, err)
		}
		return res
	}

	// 1. browser_ready
	readyRes := callTool("browser_ready", nil)
	if readyRes.IsError {
		t.Fatalf("browser_ready returned tool error: %+v", readyRes.Content)
	}

	// 2. browser_open_session (affinity sets active sessionId)
	openSessRes := callTool("browser_open_session", map[string]any{"mode": "ephemeral"})
	if openSessRes.IsError {
		t.Fatalf("browser_open_session failed: %+v", openSessRes.Content)
	}

	// 3. browser_open_tab (omitting sessionId — uses affinity!)
	openTabRes := callTool("browser_open_tab", map[string]any{"url": ts.URL + "/form"})
	if openTabRes.IsError {
		t.Fatalf("browser_open_tab failed: %+v", openTabRes.Content)
	}

	// 4. browser_snapshot (omitting sessionId & tabId — uses affinity!)
	snapRes := callTool("browser_snapshot", nil)
	if snapRes.IsError {
		t.Fatalf("browser_snapshot failed: %+v", snapRes.Content)
	}
	snapText := snapRes.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(snapText, "Full name") || !strings.Contains(snapText, "@e") {
		t.Fatalf("unexpected snapshot text:\n%s", snapText)
	}

	// Find ref for Full name, Agree to terms, Role select, and Submit.
	nameRef := extractRefFromLines(t, snapText, "textbox", "Full name")
	checkRef := extractRefFromLines(t, snapText, "checkbox", "Agree to terms")
	selectRef := extractRefFromLines(t, snapText, "combobox", "Colour")
	submitRef := extractRefFromLines(t, snapText, "button", "Submit")

	// 5. browser_fill (omitting sessionId, tabId, and snapshotId — all resolved via affinity!)
	fillRes := callTool("browser_fill", map[string]any{
		"ref":  nameRef,
		"text": "Grace Hopper",
	})
	if fillRes.IsError {
		t.Fatalf("browser_fill failed: %+v", fillRes.Content)
	}

	// 5b. browser_hover, browser_check, browser_uncheck, browser_select_option, browser_scroll, browser_press_key
	if r := callTool("browser_hover", map[string]any{"ref": submitRef}); r.IsError {
		t.Fatalf("browser_hover failed: %+v", r.Content)
	}
	if r := callTool("browser_check", map[string]any{"ref": checkRef}); r.IsError {
		t.Fatalf("browser_check failed: %+v", r.Content)
	}
	if r := callTool("browser_uncheck", map[string]any{"ref": checkRef}); r.IsError {
		t.Fatalf("browser_uncheck failed: %+v", r.Content)
	}
	if r := callTool("browser_check", map[string]any{"ref": checkRef}); r.IsError {
		t.Fatalf("browser_check second call failed: %+v", r.Content)
	}
	if r := callTool("browser_select_option", map[string]any{"ref": selectRef, "value": "Green"}); r.IsError {
		t.Fatalf("browser_select_option failed: %+v", r.Content)
	}
	if r := callTool("browser_scroll", map[string]any{"direction": "down", "amount": 240}); r.IsError {
		t.Fatalf("browser_scroll failed: %+v", r.Content)
	}
	if r := callTool("browser_press_key", map[string]any{"key": "Tab"}); r.IsError {
		t.Fatalf("browser_press_key failed: %+v", r.Content)
	}

	// 5c. browser_extract, browser_wait_for, browser_cookies, browser_telemetry
	if r := callTool("browser_extract", map[string]any{"maxChars": 4000}); r.IsError {
		t.Fatalf("browser_extract failed: %+v", r.Content)
	}
	if r := callTool("browser_wait_for", map[string]any{"text": "Full name", "timeoutMs": 3000}); r.IsError {
		t.Fatalf("browser_wait_for failed: %+v", r.Content)
	}
	if r := callTool("browser_cookies", map[string]any{
		"action": "set",
		"cookies": []map[string]any{
			{"name": "xtn_session", "value": "ni28", "url": ts.URL + "/"},
		},
	}); r.IsError {
		t.Fatalf("browser_cookies set failed: %+v", r.Content)
	}
	if r := callTool("browser_cookies", map[string]any{"action": "get"}); r.IsError {
		t.Fatalf("browser_cookies get failed: %+v", r.Content)
	}
	if r := callTool("browser_telemetry", nil); r.IsError {
		t.Fatalf("browser_telemetry failed: %+v", r.Content)
	}

	// 5c-2. browser_evaluate, browser_storage, browser_dialogs, browser_pdf, companion_analyze_selection
	evalRes := callTool("browser_evaluate", map[string]any{
		"ref":        nameRef,
		"expression": "el => el.value",
	})
	if evalRes.IsError {
		t.Fatalf("browser_evaluate failed: %+v", evalRes.Content)
	}
	if r := callTool("browser_storage", map[string]any{
		"area":    "local",
		"action":  "set",
		"entries": map[string]string{"theme": "crimson", "element": "Ni-28"},
	}); r.IsError {
		t.Fatalf("browser_storage set failed: %+v", r.Content)
	}
	if r := callTool("browser_storage", map[string]any{"area": "local", "action": "get"}); r.IsError {
		t.Fatalf("browser_storage get failed: %+v", r.Content)
	}
	if r := callTool("browser_dialogs", map[string]any{"accept": true, "promptText": "confirmed"}); r.IsError {
		t.Fatalf("browser_dialogs failed: %+v", r.Content)
	}
	pdfPath := filepath.Join(t.TempDir(), "form.pdf")
	if r := callTool("browser_pdf", map[string]any{"outputPath": pdfPath}); r.IsError {
		t.Fatalf("browser_pdf failed: %+v", r.Content)
	}
	if st, err := os.Stat(pdfPath); err != nil || st.Size() < 100 {
		t.Fatalf("browser_pdf did not write valid PDF file: err=%v", err)
	}
	if r := callTool("companion_analyze_selection", map[string]any{
		"text":        "hey, this person isnt nice :(",
		"windowClass": "discord",
		"guide":       false,
	}); r.IsError {
		t.Fatalf("companion_analyze_selection failed: %+v", r.Content)
	}

	// 5d. companion_cursor_guide & companion_cursor_control (pointing at @eN with chat bubble!)
	if r := callTool("companion_cursor_guide", map[string]any{
		"ref":     submitRef,
		"mood":    "friendly",
		"subtext": "Submit form",
		"message": "hey, you should click this to help me!",
	}); r.IsError {
		t.Fatalf("companion_cursor_guide failed: %+v", r.Content)
	}
	if r := callTool("companion_cursor_control", map[string]any{"action": "status"}); r.IsError {
		t.Fatalf("companion_cursor_control status failed: %+v", r.Content)
	}
	if r := callTool("companion_cursor_control", map[string]any{"action": "hide"}); r.IsError {
		t.Fatalf("companion_cursor_control hide failed: %+v", r.Content)
	}

	// 6. browser_screenshot (verify ImageContent block with PNG signature)
	shotRes := callTool("browser_screenshot", nil)
	if shotRes.IsError {
		t.Fatalf("browser_screenshot failed: %+v", shotRes.Content)
	}
	if len(shotRes.Content) == 0 {
		t.Fatal("browser_screenshot returned no content blocks")
	}
	img, ok := shotRes.Content[0].(*mcp.ImageContent)
	if !ok {
		t.Fatalf("expected *mcp.ImageContent, got %T", shotRes.Content[0])
	}
	if img.MIMEType != "image/png" || !bytes.HasPrefix(img.Data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		t.Fatalf("screenshot is not a valid PNG (mime=%s, len=%d)", img.MIMEType, len(img.Data))
	}

	// 7. browser_persona
	personaRes := callTool("browser_persona", nil)
	if personaRes.IsError {
		t.Fatalf("browser_persona failed: %+v", personaRes.Content)
	}

	// 8. browser_click on Submit
	clickRes := callTool("browser_click", map[string]any{
		"ref": submitRef,
	})
	if clickRes.IsError {
		t.Fatalf("browser_click failed: %+v", clickRes.Content)
	}
	if fxSrv.Hit.MethodCount("POST", "form/submit") == 0 {
		t.Fatal("fixture server did not record POST /form/submit")
	}

	// 9. Acting again with the pre-navigation snapshot must return IsError=true with STALE_REF guidance.
	staleRes := callTool("browser_click", map[string]any{
		"ref": submitRef,
	})
	if !staleRes.IsError {
		t.Fatal("expected IsError=true when clicking with a stale post-navigation snapshot")
	}
	staleText := staleRes.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(staleText, "STALE_REF") || !strings.Contains(staleText, "browser_snapshot") {
		t.Fatalf("stale error missing STALE_REF or guidance: %q", staleText)
	}

	// 10. Navigating to a non-allowlisted origin must return IsError=true with POLICY_BLOCKED guidance.
	blockedRes := callTool("browser_navigate", map[string]any{
		"url": "https://denied.example.invalid/",
	})
	if !blockedRes.IsError {
		t.Fatal("expected IsError=true on blocked navigation")
	}
	blockedText := blockedRes.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(blockedText, "POLICY_BLOCKED") {
		t.Fatalf("blocked navigation missing POLICY_BLOCKED: %q", blockedText)
	}

	// 11. List tabs & sessions, then close tab & session.
	if r := callTool("browser_list_tabs", nil); r.IsError {
		t.Fatalf("browser_list_tabs failed: %+v", r.Content)
	}
	if r := callTool("browser_list_sessions", nil); r.IsError {
		t.Fatalf("browser_list_sessions failed: %+v", r.Content)
	}
	if r := callTool("browser_close_tab", nil); r.IsError {
		t.Fatalf("browser_close_tab failed: %+v", r.Content)
	}
	if r := callTool("browser_close_session", nil); r.IsError {
		t.Fatalf("browser_close_session failed: %+v", r.Content)
	}
	_ = json.Marshal
}

func extractRefFromLines(t *testing.T, text, role, name string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, role) || !strings.Contains(line, name) {
			continue
		}
		for _, tok := range strings.Fields(line) {
			tok = strings.Trim(tok, "[]()")
			if strings.HasPrefix(tok, "@e") {
				return strings.TrimPrefix(tok, "@")
			}
		}
	}
	t.Fatalf("could not find ref for role=%q name=%q in:\n%s", role, name, text)
	return ""
}
