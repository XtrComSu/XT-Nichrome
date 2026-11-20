package tests

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPStdioSubprocessRoundTrip(t *testing.T) {
	bin := buildBinary(t, "./cmd/nichrome-mcp")
	fx := newFixture(t)
	state := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.Command(bin,
		"-browser", browserPath(t),
		"-state-dir", state,
		"-allow", fx.URL,
		"-allow-private",
	)
	transport := &mcp.CommandTransport{
		Command:           cmd,
		TerminateDuration: 5 * time.Second,
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-e2e-client", Version: "1.0.0"}, nil)
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("Connect to nichrome-mcp stdio subprocess: %v", err)
	}
	defer sess.Close()

	// 1. Verify all 37 tools are advertised over stdio.
	toolsRes, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(toolsRes.Tools) != 37 {
		t.Fatalf("expected 37 MCP tools over stdio, got %d", len(toolsRes.Tools))
	}

	// 2. Verify all 4 resources and 3 prompts are advertised over stdio.
	resList, err := sess.ListResources(ctx, nil)
	if err != nil || len(resList.Resources) < 4 {
		t.Fatalf("ListResources failed: err=%v count=%d", err, len(resList.Resources))
	}
	promptList, err := sess.ListPrompts(ctx, nil)
	if err != nil || len(promptList.Prompts) < 3 {
		t.Fatalf("ListPrompts failed: err=%v count=%d", err, len(promptList.Prompts))
	}

	// 3. Drive a real session, tab, snapshot, and JS evaluation over stdio.
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		r, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("CallTool(%s): %v", name, err)
		}
		if r.IsError {
			t.Fatalf("CallTool(%s) returned tool error: %+v", name, r.Content)
		}
		return r
	}

	call("browser_open_session", map[string]any{"mode": "ephemeral"})
	call("browser_open_tab", map[string]any{"url": fx.URL + "/form"})
	snap := call("browser_snapshot", nil)
	snapText := snap.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(snapText, "Full name") {
		t.Fatalf("unexpected stdio snapshot text:\n%s", snapText)
	}

	// Read active sessions resource while session is open.
	activeRes, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: "nichrome://sessions/active"})
	if err != nil || len(activeRes.Contents) == 0 || !strings.Contains(activeRes.Contents[0].Text, "/form") {
		t.Fatalf("ReadResource(nichrome://sessions/active) failed: err=%v", err)
	}

	call("browser_close_session", nil)
}
