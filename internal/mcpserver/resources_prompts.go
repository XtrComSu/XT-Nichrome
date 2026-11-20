package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xtrcom/xt-nichrome/internal/stealth"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

func (s *Server) registerResourcesAndPrompts() {
	// 1. Live Readiness & Egress Policy Resource
	s.mcp.AddResource(&mcp.Resource{
		URI:         "nichrome://status/ready",
		Name:        "status_ready",
		Title:       "XT-Nichrome Readiness, Capacity & Egress Policy State",
		Description: "Live readiness, active session/tab counts, configured capacity limits, and egress policy proxy statistics.",
		MIMEType:    "application/json",
	}, s.handleResourceReady)

	// 2. Active Sessions & Tabs Inventory Resource
	s.mcp.AddResource(&mcp.Resource{
		URI:         "nichrome://sessions/active",
		Name:        "sessions_active",
		Title:       "Active Browser Sessions & Open Tabs",
		Description: "JSON snapshot of all currently open browser sessions and their tabs (URLs, titles, and document generations).",
		MIMEType:    "application/json",
	}, s.handleResourceSessions)

	// 3. Built-in Stealth Personas Catalog Resource
	s.mcp.AddResource(&mcp.Resource{
		URI:         "nichrome://personas/catalog",
		Name:        "personas_catalog",
		Title:       "Stealth Personas Catalog",
		Description: "Catalog of all built-in coherent stealth personas (win11-chrome-141, win10-chrome-141, macos-chrome-141, linux-chrome-141).",
		MIMEType:    "application/json",
	}, s.handleResourcePersonas)

	// 4. Companion 2nd Cursor ("Ni-Cursor") Live State Resource
	s.mcp.AddResource(&mcp.Resource{
		URI:         "nichrome://companion/state",
		Name:        "companion_state",
		Title:       "Ni-Cursor 2nd Companion State & Latest Highlighted Selection",
		Description: "Live state of the Wayland/Hyprland & in-browser 2nd companion cursor, including coordinates, mood, chat bubble, and last highlighted text analysis.",
		MIMEType:    "application/json",
	}, s.handleResourceCompanion)

	// Prompt 1: Stealth Browse & Extract
	s.mcp.AddPrompt(&mcp.Prompt{
		Name:        "stealth_browse_and_extract",
		Title:       "Stealth Browse & Extract Structured Content",
		Description: "Open an isolated stealth session, navigate to a target URL, verify persona coherence, and extract clean readable text, headings, and links.",
		Arguments: []*mcp.PromptArgument{
			{Name: "url", Description: "Target URL to open and extract", Required: true},
			{Name: "persona", Description: "Optional stealth persona (e.g. win11-chrome-141, macos-chrome-141)", Required: false},
			{Name: "goal", Description: "Specific information or question to answer from the page", Required: false},
		},
	}, s.handlePromptBrowseAndExtract)

	// Prompt 2: Interactive Form Workflow
	s.mcp.AddPrompt(&mcp.Prompt{
		Name:        "interactive_form_workflow",
		Title:       "Humanized Form Fill & Safe Submission",
		Description: "Snapshot a form page, fill inputs with humanized keystrokes, select/check options, and submit with stale-ref and idempotency safeguards.",
		Arguments: []*mcp.PromptArgument{
			{Name: "url", Description: "URL of the form page", Required: true},
			{Name: "fields", Description: "Description or JSON map of form fields and values to populate", Required: true},
		},
	}, s.handlePromptFormWorkflow)

	// Prompt 3: Companion Screen Guide ("Ni-Cursor")
	s.mcp.AddPrompt(&mcp.Prompt{
		Name:        "companion_screen_guide",
		Title:       "Ni-Cursor Visual Assistant & Selection Responder",
		Description: "Use the 2nd companion cursor (Ni-Cursor) to point at elements, react to highlighted text, and guide the user with friendly chat bubbles and mood expressions.",
		Arguments: []*mcp.PromptArgument{
			{Name: "context", Description: "What the user is currently viewing or highlighting", Required: true},
			{Name: "mood", Description: "Initial companion mood (friendly, curious, warn, sad, excited, thinking)", Required: false},
		},
	}, s.handlePromptCompanionGuide)
}

func (s *Server) handleResourceReady(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	_, ready, err := s.handleReady(ctx, nil, ReadyInput{})
	if err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(ready, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(b),
			},
		},
	}, nil
}

func (s *Server) handleResourceSessions(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	type sessionWithTabs struct {
		types.Session
		Tabs []types.TabInfo `json:"tabs"`
	}
	sessions := s.cfg.Manager.List()
	out := make([]sessionWithTabs, 0, len(sessions))
	for _, sess := range sessions {
		tabs, _ := s.cfg.Manager.ListTabs(sess.ID)
		out = append(out, sessionWithTabs{
			Session: sess,
			Tabs:    tabs,
		})
	}
	b, err := json.MarshalIndent(map[string]any{"sessions": out}, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(b),
			},
		},
	}, nil
}

func (s *Server) handleResourcePersonas(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	names := stealth.Names()
	list := make([]stealth.Persona, 0, len(names))
	for _, name := range names {
		if p, err := stealth.Lookup(name); err == nil {
			list = append(list, p)
		}
	}
	b, err := json.MarshalIndent(map[string]any{"personas": list}, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(b),
			},
		},
	}, nil
}

func (s *Server) handleResourceCompanion(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	st, apiErr := s.cfg.Manager.Companion(ctx, types.CompanionRequest{Action: "status"})
	if apiErr != nil {
		return nil, FormatError(apiErr)
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(b),
			},
		},
	}, nil
}

func (s *Server) handlePromptBrowseAndExtract(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	args := req.Params.Arguments
	targetURL := args["url"]
	persona := args["persona"]
	if persona == "" {
		persona = "win11-chrome-141"
	}
	goal := args["goal"]
	if goal == "" {
		goal = "Summarize the main content, headings, and key links on the page."
	}
	text := fmt.Sprintf(
		"Use XT-Nichrome to browse %q with stealth persona %q.\n\n"+
			"Steps:\n"+
			"1. Call `browser_open_session` with `mode='ephemeral'` and `persona=%q`.\n"+
			"2. Call `browser_open_tab` with `url=%q`.\n"+
			"3. Call `browser_extract` to retrieve clean readable text, headings, and hyperlinks, and `browser_snapshot` if interactive `@eN` element references are needed.\n"+
			"4. Goal: %s\n"+
			"5. When finished, call `browser_close_session` to release the browser lease.",
		targetURL, persona, persona, targetURL, goal,
	)
	return &mcp.GetPromptResult{
		Description: "Stealth browse and extract workflow for " + targetURL,
		Messages: []*mcp.PromptMessage{
			{
				Role:    "user",
				Content: &mcp.TextContent{Text: text},
			},
		},
	}, nil
}

func (s *Server) handlePromptFormWorkflow(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	args := req.Params.Arguments
	targetURL := args["url"]
	fields := args["fields"]
	text := fmt.Sprintf(
		"Complete and submit the form at %q using XT-Nichrome's humanized input tools.\n\n"+
			"Target fields: %s\n\n"+
			"Rules:\n"+
			"1. Open a session (`browser_open_session`) and tab (`browser_open_tab` with `url=%q`).\n"+
			"2. Call `browser_snapshot` to inspect the accessibility tree and locate `@eN` handles for each input, select, checkbox, or button.\n"+
			"3. Populate textboxes with `browser_fill`, dropdowns with `browser_select_option`, and checkboxes with `browser_check`/`browser_uncheck`.\n"+
			"4. Submit with `browser_click`. If `ACTION_OUTCOME_UNKNOWN` or `freshSnapshotRequired: true` is returned, NEVER blindly re-click submit — call `browser_snapshot` first to verify whether the submission already succeeded.",
		targetURL, fields, targetURL,
	)
	return &mcp.GetPromptResult{
		Description: "Humanized form interaction workflow for " + targetURL,
		Messages: []*mcp.PromptMessage{
			{
				Role:    "user",
				Content: &mcp.TextContent{Text: text},
			},
		},
	}, nil
}

func (s *Server) handlePromptCompanionGuide(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	args := req.Params.Arguments
	ctxDesc := args["context"]
	mood := args["mood"]
	if mood == "" {
		mood = "friendly"
	}
	text := fmt.Sprintf(
		"Act as Ni-28 (`Ni-Cursor`), the user's friendly 2nd cursor companion.\n\n"+
			"Current user context: %s\n\n"+
			"Available companion tools:\n"+
			"- `companion_analyze_selection`: Analyze highlighted text (or set `readPrimary=true` to read Wayland primary selection) and classify tone/intent or decode base64/hex/timestamps.\n"+
			"- `companion_cursor_guide`: Glide the 2nd cursor to `(x, y)` or a snapshot `@eN` element with mood=%q (`friendly`, `curious`, `warn`, `sad`, `excited`, `thinking`) and a concise, conversational chat bubble.\n"+
			"- `companion_cursor_control`: Show, hide, or clear the overlay when done so it never obstructs the user's screen.",
		ctxDesc, mood,
	)
	return &mcp.GetPromptResult{
		Description: "Ni-Cursor companion guide workflow",
		Messages: []*mcp.PromptMessage{
			{
				Role:    "user",
				Content: &mcp.TextContent{Text: text},
			},
		},
	}, nil
}
