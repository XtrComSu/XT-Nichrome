package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

// OpenSessionInput is the input for browser_open_session.
type OpenSessionInput struct {
	Mode      string `json:"mode,omitempty" jsonschema:"session mode: ephemeral (default, isolated context in pooled browser) or persistent (dedicated browser and on-disk profile)"`
	ProfileID string `json:"profileId,omitempty" jsonschema:"profile name (required when mode is persistent; lowercase alphanumeric, hyphens and underscores)"`
	Persona   string `json:"persona,omitempty" jsonschema:"optional stealth persona name (e.g. win11-chrome-141, win10-chrome-141, macos-chrome-141, linux-chrome-141)"`
}

// OpenSessionOutput is the output for browser_open_session.
type OpenSessionOutput struct {
	SessionID string `json:"sessionId" jsonschema:"created browser session ID (remembered as the active session for this MCP client)"`
	Mode      string `json:"mode" jsonschema:"session mode (ephemeral or persistent)"`
	ProfileID string `json:"profileId,omitempty" jsonschema:"profile ID for persistent sessions"`
	State     string `json:"state" jsonschema:"lifecycle state of the session"`
	Persona   string `json:"persona" jsonschema:"active stealth persona name"`
	CreatedAt string `json:"createdAt,omitempty" jsonschema:"RFC3339 creation timestamp"`
}

// CloseSessionInput is the input for browser_close_session.
type CloseSessionInput struct {
	SessionID string `json:"sessionId,omitempty" jsonschema:"session ID to close (defaults to the active session for this MCP client)"`
}

// CloseSessionOutput is the output for browser_close_session.
type CloseSessionOutput struct {
	SessionID string `json:"sessionId" jsonschema:"closed session ID"`
	Status    string `json:"status" jsonschema:"closure status (closed)"`
}

// ListSessionsInput is the input for browser_list_sessions.
type ListSessionsInput struct{}

// ListSessionsOutput is the output for browser_list_sessions.
type ListSessionsOutput struct {
	Sessions        []types.Session `json:"sessions" jsonschema:"currently open browser sessions"`
	ActiveSessionID string          `json:"activeSessionId,omitempty" jsonschema:"remembered default session ID for this MCP client"`
}

// OpenTabInput is the input for browser_open_tab.
type OpenTabInput struct {
	SessionID string `json:"sessionId,omitempty" jsonschema:"target session ID (defaults to the active session for this MCP client)"`
	URL       string `json:"url,omitempty" jsonschema:"optional allowlisted URL to navigate to immediately after opening the tab"`
}

// OpenTabOutput is the output for browser_open_tab.
type OpenTabOutput struct {
	SessionID  string `json:"sessionId" jsonschema:"session ID owning the tab"`
	TabID      string `json:"tabId" jsonschema:"created tab ID (remembered as the active tab for this session)"`
	State      string `json:"state" jsonschema:"tab lifecycle state"`
	CurrentURL string `json:"currentUrl,omitempty" jsonschema:"current committed URL"`
}

// CloseTabInput is the input for browser_close_tab.
type CloseTabInput struct {
	SessionID string `json:"sessionId,omitempty" jsonschema:"session ID owning the tab (defaults to active session)"`
	TabID     string `json:"tabId,omitempty" jsonschema:"tab ID to close (defaults to active tab)"`
}

// CloseTabOutput is the output for browser_close_tab.
type CloseTabOutput struct {
	SessionID string `json:"sessionId" jsonschema:"session ID"`
	TabID     string `json:"tabId" jsonschema:"closed tab ID"`
	Status    string `json:"status" jsonschema:"closure status (closed)"`
}

// ListTabsInput is the input for browser_list_tabs.
type ListTabsInput struct {
	SessionID string `json:"sessionId,omitempty" jsonschema:"session ID whose tabs to list (defaults to active session)"`
}

// ListTabsOutput is the output for browser_list_tabs.
type ListTabsOutput struct {
	SessionID   string          `json:"sessionId" jsonschema:"session ID"`
	Tabs        []types.TabInfo `json:"tabs" jsonschema:"open tabs in the session"`
	ActiveTabID string          `json:"activeTabId,omitempty" jsonschema:"remembered default tab ID for this session"`
}

// NavigateInput is the input for browser_navigate.
type NavigateInput struct {
	SessionID string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID     string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	URL       string `json:"url" jsonschema:"destination URL (must belong to an allowlisted origin)"`
	WaitUntil string `json:"waitUntil,omitempty" jsonschema:"navigation completion barrier: load (default) or commit"`
	TimeoutMS int    `json:"timeoutMs,omitempty" jsonschema:"optional navigation deadline in milliseconds (default 30000)"`
}

// NavigateOutput is the output for browser_navigate.
type NavigateOutput struct {
	SessionID             string `json:"sessionId" jsonschema:"session ID"`
	TabID                 string `json:"tabId" jsonschema:"tab ID"`
	URL                   string `json:"url" jsonschema:"final committed URL"`
	Generation            int64  `json:"generation" jsonschema:"document generation counter after navigation"`
	Title                 string `json:"title,omitempty" jsonschema:"document title"`
	FreshSnapshotRequired bool   `json:"freshSnapshotRequired" jsonschema:"always true after navigation; call browser_snapshot before acting"`
}

// SnapshotInput is the input for browser_snapshot.
type SnapshotInput struct {
	SessionID     string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID         string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	MaxChars      int    `json:"maxChars,omitempty" jsonschema:"maximum character budget for the rendered accessibility tree"`
	MaxRefs       int    `json:"maxRefs,omitempty" jsonschema:"maximum number of @eN element references to emit"`
	IncludeValues bool   `json:"includeValues,omitempty" jsonschema:"whether to include live non-password input field values"`
	Scope         string `json:"scope,omitempty" jsonschema:"snapshot scope: page (default)"`
}

// SnapshotOutput is the output for browser_snapshot.
type SnapshotOutput struct {
	SessionID         string   `json:"sessionId" jsonschema:"session ID"`
	TabID             string   `json:"tabId" jsonschema:"tab ID"`
	SnapshotID        string   `json:"snapshotId" jsonschema:"snapshot handle required by browser_click and browser_fill"`
	Generation        int64    `json:"generation" jsonschema:"document generation at snapshot time"`
	URL               string   `json:"url" jsonschema:"page URL"`
	Title             string   `json:"title,omitempty" jsonschema:"page title"`
	Text              string   `json:"text" jsonschema:"compact accessibility tree with @eN element references"`
	Truncated         bool     `json:"truncated" jsonschema:"true if the tree was truncated by maxChars or maxRefs"`
	RefCount          int      `json:"refCount" jsonschema:"number of actionable @eN references in this snapshot"`
	UnsupportedFrames []string `json:"unsupportedFrames,omitempty" jsonschema:"subframes that were not snapshotted"`
	OmittedUnnamed    int      `json:"omittedUnnamed,omitempty" jsonschema:"unnamed nodes omitted for compactness"`
}

// ClickInput is the input for browser_click.
type ClickInput struct {
	SessionID  string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID      string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	SnapshotID string `json:"snapshotId,omitempty" jsonschema:"snapshot ID from browser_snapshot (defaults to the most recent snapshot on this tab)"`
	Ref        string `json:"ref" jsonschema:"element reference from browser_snapshot, e.g. e1 or @e1"`
	Generation int64  `json:"generation,omitempty" jsonschema:"optional expected document generation"`
	Button     string `json:"button,omitempty" jsonschema:"mouse button: left (default), right, or middle"`
	ClickCount int    `json:"clickCount,omitempty" jsonschema:"number of clicks: 1 (default), 2, or 3"`
	TimeoutMS  int    `json:"timeoutMs,omitempty" jsonschema:"action deadline in milliseconds (default 15000)"`
}

// FillInput is the input for browser_fill.
type FillInput struct {
	SessionID     string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID         string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	SnapshotID    string `json:"snapshotId,omitempty" jsonschema:"snapshot ID from browser_snapshot (defaults to the most recent snapshot on this tab)"`
	Ref           string `json:"ref" jsonschema:"textbox/input reference from browser_snapshot, e.g. e1 or @e1"`
	Text          string `json:"text" jsonschema:"text to type into the referenced input element"`
	Generation    int64  `json:"generation,omitempty" jsonschema:"optional expected document generation"`
	ClearExisting *bool  `json:"clearExisting,omitempty" jsonschema:"clear existing field value before typing (default true)"`
	TimeoutMS     int    `json:"timeoutMs,omitempty" jsonschema:"action deadline in milliseconds (default 15000)"`
}

// ActionOutput is the output for browser_click and browser_fill.
type ActionOutput struct {
	SessionID             string `json:"sessionId" jsonschema:"session ID"`
	TabID                 string `json:"tabId" jsonschema:"tab ID"`
	Status                string `json:"status" jsonschema:"ok or unknown"`
	Outcome               string `json:"outcome" jsonschema:"completed or unknown"`
	FreshSnapshotRequired bool   `json:"freshSnapshotRequired" jsonschema:"true if the page navigated or the snapshot is no longer valid"`
	Generation            int64  `json:"generation" jsonschema:"document generation after the action"`
	URL                   string `json:"url,omitempty" jsonschema:"current page URL after the action"`
	VerifyBeforeRetry     bool   `json:"verifyBeforeRetry,omitempty" jsonschema:"true when outcome is unknown; inspect page with browser_snapshot before retrying"`
	Guidance              string `json:"guidance,omitempty" jsonschema:"next-step instruction for the agent"`
}

// ScreenshotInput is the input for browser_screenshot.
type ScreenshotInput struct {
	SessionID string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID     string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
}

// ScreenshotOutput is the structured metadata accompanying the PNG image block.
type ScreenshotOutput struct {
	SessionID string `json:"sessionId" jsonschema:"session ID"`
	TabID     string `json:"tabId" jsonschema:"tab ID"`
	MIMEType  string `json:"mimeType" jsonschema:"image MIME type (image/png)"`
	Bytes     int    `json:"bytes" jsonschema:"size of the captured PNG in bytes"`
}

// PersonaInput is the input for browser_persona.
type PersonaInput struct {
	SessionID string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID     string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
}

// ReadyInput is the input for browser_ready.
type ReadyInput struct{}

func (s *Server) registerTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "browser_open_session",
		Description: "Create a new browser session. Use mode='ephemeral' (default) for an isolated context in the pooled browser, " +
			"or mode='persistent' with a profileId for a dedicated browser that retains cookies and storage across runs. " +
			"The created sessionId becomes the default session for subsequent tool calls in this MCP session.",
	}, s.handleOpenSession)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_close_session",
		Description: "Close a browser session and all of its tabs, releasing its capacity slot and any persistent profile lock.",
	}, s.handleCloseSession)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_list_sessions",
		Description: "List all active browser sessions and report the remembered default sessionId for this MCP client.",
	}, s.handleListSessions)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "browser_open_tab",
		Description: "Open a new tab in a browser session (defaults to the active session) and verify its stealth persona. " +
			"Optionally navigate to an initial allowlisted URL. The new tabId becomes the default tab for this session.",
	}, s.handleOpenTab)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_close_tab",
		Description: "Close a browser tab and invalidate its snapshots, freeing a tab slot in the session.",
	}, s.handleCloseTab)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_list_tabs",
		Description: "List all open tabs in a browser session along with their current URLs, titles, and document generations.",
	}, s.handleListTabs)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "browser_navigate",
		Description: "Navigate a tab to an allowlisted URL and wait for 'load' (default) or 'commit'. " +
			"Every navigation increments the document generation and invalidates prior snapshots; call browser_snapshot afterwards.",
	}, s.handleNavigate)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "browser_snapshot",
		Description: "Capture a compact accessibility tree of the current page with stable @eN element references, " +
			"returning the tree as text alongside snapshotId and generation for use with browser_click and browser_fill.",
	}, s.handleSnapshot)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "browser_click",
		Description: "Click an actionable element referenced by its @eN handle from browser_snapshot using a humanized mouse trajectory. " +
			"If the click triggers a navigation or unknown outcome, check freshSnapshotRequired and re-snapshot before acting again.",
	}, s.handleClick)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "browser_fill",
		Description: "Focus and type text into a textbox, searchbox, combobox, or contentEditable element referenced by its @eN handle " +
			"from browser_snapshot using virtualized per-character keystrokes.",
	}, s.handleFill)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_screenshot",
		Description: "Capture a viewport PNG screenshot of the tab and return it as an MCP image/png content block.",
	}, s.handleScreenshot)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_persona",
		Description: "Inspect the live tab's observable stealth identity (UA, platform, WebGL renderer, screen/window geometry, timezone, plugins) and verify internal consistency.",
	}, s.handlePersona)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_ready",
		Description: "Report daemon readiness, active session/tab counts, configured capacity limits, and egress policy proxy statistics.",
	}, s.handleReady)

	s.registerExtendedTools()
}

func (s *Server) handleOpenSession(ctx context.Context, req *mcp.CallToolRequest, in OpenSessionInput) (*mcp.CallToolResult, OpenSessionOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var profPtr *string
	if in.ProfileID != "" {
		p := in.ProfileID
		profPtr = &p
	}
	sess, apiErr := s.cfg.Manager.Create(ctx, types.CreateSessionRequest{
		Mode:      in.Mode,
		ProfileID: profPtr,
		Persona:   in.Persona,
	})
	if apiErr != nil {
		return nil, OpenSessionOutput{}, FormatError(apiErr)
	}
	s.rememberSession(req, sess.ID)
	out := OpenSessionOutput{
		SessionID: sess.ID,
		Mode:      sess.Mode,
		ProfileID: sess.ProfileID,
		State:     sess.State,
		Persona:   sess.Persona,
		CreatedAt: sess.CreatedAt,
	}
	return nil, out, nil
}

func (s *Server) handleCloseSession(ctx context.Context, req *mcp.CallToolRequest, in CloseSessionInput) (*mcp.CallToolResult, CloseSessionOutput, error) {
	sid, apiErr := s.resolveSession(req, in.SessionID)
	if apiErr != nil {
		return nil, CloseSessionOutput{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if apiErr := s.cfg.Manager.Close(ctx, sid); apiErr != nil {
		return nil, CloseSessionOutput{}, FormatError(apiErr)
	}
	s.forgetSession(req, sid)
	return nil, CloseSessionOutput{SessionID: sid, Status: "closed"}, nil
}

func (s *Server) handleListSessions(_ context.Context, req *mcp.CallToolRequest, _ ListSessionsInput) (*mcp.CallToolResult, ListSessionsOutput, error) {
	list := s.cfg.Manager.List()
	st := s.stateFor(req)
	s.mu.Lock()
	active := st.sessionID
	s.mu.Unlock()
	return nil, ListSessionsOutput{Sessions: list, ActiveSessionID: active}, nil
}

func (s *Server) handleOpenTab(ctx context.Context, req *mcp.CallToolRequest, in OpenTabInput) (*mcp.CallToolResult, OpenTabOutput, error) {
	sid, apiErr := s.resolveSession(req, in.SessionID)
	if apiErr != nil {
		return nil, OpenTabOutput{}, FormatError(apiErr)
	}
	if apiErr := s.checkClientTabCap(req); apiErr != nil {
		return nil, OpenTabOutput{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	tab, apiErr := s.cfg.Manager.CreateTab(ctx, sid, types.CreateTabRequest{URL: in.URL})
	if apiErr != nil {
		return nil, OpenTabOutput{}, FormatError(apiErr)
	}
	s.rememberTab(req, sid, tab.TabID)
	return nil, OpenTabOutput{
		SessionID:  sid,
		TabID:      tab.TabID,
		State:      tab.State,
		CurrentURL: tab.CurrentURL,
	}, nil
}

func (s *Server) handleCloseTab(ctx context.Context, req *mcp.CallToolRequest, in CloseTabInput) (*mcp.CallToolResult, CloseTabOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, CloseTabOutput{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if apiErr := s.cfg.Manager.CloseTab(ctx, sid, tid); apiErr != nil {
		return nil, CloseTabOutput{}, FormatError(apiErr)
	}
	s.forgetTab(req, sid, tid)
	return nil, CloseTabOutput{SessionID: sid, TabID: tid, Status: "closed"}, nil
}

func (s *Server) handleListTabs(_ context.Context, req *mcp.CallToolRequest, in ListTabsInput) (*mcp.CallToolResult, ListTabsOutput, error) {
	sid, apiErr := s.resolveSession(req, in.SessionID)
	if apiErr != nil {
		return nil, ListTabsOutput{}, FormatError(apiErr)
	}
	tabs, apiErr := s.cfg.Manager.ListTabs(sid)
	if apiErr != nil {
		return nil, ListTabsOutput{}, FormatError(apiErr)
	}
	st := s.stateFor(req)
	s.mu.Lock()
	activeTab := st.tabBySession[sid]
	s.mu.Unlock()
	return nil, ListTabsOutput{SessionID: sid, Tabs: tabs, ActiveTabID: activeTab}, nil
}

func (s *Server) handleNavigate(ctx context.Context, req *mcp.CallToolRequest, in NavigateInput) (*mcp.CallToolResult, NavigateOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, NavigateOutput{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, apiErr := s.cfg.Manager.Navigate(ctx, sid, tid, types.NavigateRequest{
		URL:       in.URL,
		WaitUntil: in.WaitUntil,
		TimeoutMS: in.TimeoutMS,
	})
	if apiErr != nil {
		return nil, NavigateOutput{}, FormatError(apiErr)
	}
	s.rememberTab(req, sid, tid)
	return nil, NavigateOutput{
		SessionID:             sid,
		TabID:                 tid,
		URL:                   res.URL,
		Generation:            res.Generation,
		Title:                 res.Title,
		FreshSnapshotRequired: true,
	}, nil
}

func (s *Server) handleSnapshot(ctx context.Context, req *mcp.CallToolRequest, in SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, SnapshotOutput{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	snap, apiErr := s.cfg.Manager.Snapshot(ctx, sid, tid, types.SnapshotRequest{
		MaxChars:      in.MaxChars,
		MaxRefs:       in.MaxRefs,
		IncludeValues: in.IncludeValues,
		Scope:         in.Scope,
	})
	if apiErr != nil {
		return nil, SnapshotOutput{}, FormatError(apiErr)
	}
	s.rememberSnapshot(req, sid, tid, snap.SnapshotID)
	out := SnapshotOutput{
		SessionID:         sid,
		TabID:             tid,
		SnapshotID:        snap.SnapshotID,
		Generation:        snap.Generation,
		URL:               snap.URL,
		Title:             snap.Title,
		Text:              snap.Text,
		Truncated:         snap.Truncated,
		RefCount:          snap.RefCount,
		UnsupportedFrames: snap.UnsupportedFrames,
		OmittedUnnamed:    snap.OmittedUnnamed,
	}
	header := fmt.Sprintf("snapshotId: %s\ngeneration: %d\nurl: %s\nrefCount: %d\ntruncated: %v\n---\n%s",
		snap.SnapshotID, snap.Generation, snap.URL, snap.RefCount, snap.Truncated, snap.Text)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: header}},
	}, out, nil
}

func (s *Server) handleClick(ctx context.Context, req *mcp.CallToolRequest, in ClickInput) (*mcp.CallToolResult, ActionOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, ActionOutput{}, FormatError(apiErr)
	}
	snapID := s.resolveSnapshotID(req, tid, in.SnapshotID)
	return s.execAction(ctx, sid, tid, types.ActionRequest{
		Kind:       types.ActionClick,
		SnapshotID: snapID,
		Ref:        strings.TrimPrefix(strings.TrimSpace(in.Ref), "@"),
		Generation: in.Generation,
		Button:     in.Button,
		ClickCount: in.ClickCount,
		TimeoutMS:  in.TimeoutMS,
	})
}

func (s *Server) handleFill(ctx context.Context, req *mcp.CallToolRequest, in FillInput) (*mcp.CallToolResult, ActionOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, ActionOutput{}, FormatError(apiErr)
	}
	snapID := s.resolveSnapshotID(req, tid, in.SnapshotID)
	return s.execAction(ctx, sid, tid, types.ActionRequest{
		Kind:          types.ActionFill,
		SnapshotID:    snapID,
		Ref:           strings.TrimPrefix(strings.TrimSpace(in.Ref), "@"),
		Generation:    in.Generation,
		Text:          in.Text,
		ClearExisting: in.ClearExisting,
		TimeoutMS:     in.TimeoutMS,
	})
}

func (s *Server) execAction(ctx context.Context, sid, tid string, actReq types.ActionRequest) (*mcp.CallToolResult, ActionOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	res, apiErr := s.cfg.Manager.Act(ctx, sid, tid, actReq)
	if apiErr != nil {
		// Ambiguous outcome: the action was dispatched to the browser before the
		// result was lost. Surface as a non-failure warning so the agent verifies
		// the page state before retrying rather than double-submitting.
		if res != nil && (res.Outcome == types.OutcomeUnknown || apiErr.Code == types.CodeActionOutcomeUnknown) {
			msg := fmt.Sprintf("ACTION_OUTCOME_UNKNOWN: the %s action was dispatched and may have landed on the page (%s). "+
				"Do NOT blindly retry — call browser_snapshot to verify the page state before retrying.",
				actReq.Kind, apiErr.Message)
			out := ActionOutput{
				SessionID:             sid,
				TabID:                 tid,
				Status:                types.StatusUnknown,
				Outcome:               types.OutcomeUnknown,
				FreshSnapshotRequired: true,
				Generation:            res.Generation,
				URL:                   res.URL,
				VerifyBeforeRetry:     true,
				Guidance:              msg,
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: msg}},
			}, out, nil
		}
		return nil, ActionOutput{}, FormatError(apiErr)
	}
	out := ActionOutput{
		SessionID:             sid,
		TabID:                 tid,
		Status:                res.Status,
		Outcome:               res.Outcome,
		FreshSnapshotRequired: res.FreshSnapshotRequired,
		Generation:            res.Generation,
		URL:                   res.URL,
	}
	if res.FreshSnapshotRequired {
		out.Guidance = "The page navigated or changed generation; call browser_snapshot to obtain fresh @eN refs before your next action."
	}
	return nil, out, nil
}

func (s *Server) handleScreenshot(ctx context.Context, req *mcp.CallToolRequest, in ScreenshotInput) (*mcp.CallToolResult, ScreenshotOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, ScreenshotOutput{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	png, apiErr := s.cfg.Manager.Screenshot(ctx, sid, tid)
	if apiErr != nil {
		return nil, ScreenshotOutput{}, FormatError(apiErr)
	}
	out := ScreenshotOutput{
		SessionID: sid,
		TabID:     tid,
		MIMEType:  "image/png",
		Bytes:     len(png),
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.ImageContent{
				MIMEType: "image/png",
				Data:     png,
			},
		},
	}, out, nil
}

func (s *Server) handlePersona(ctx context.Context, req *mcp.CallToolRequest, in PersonaInput) (*mcp.CallToolResult, types.PersonaReport, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, types.PersonaReport{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rep, apiErr := s.cfg.Manager.PersonaReport(ctx, sid, tid)
	if apiErr != nil {
		return nil, types.PersonaReport{}, FormatError(apiErr)
	}
	return nil, *rep, nil
}

func (s *Server) handleReady(_ context.Context, _ *mcp.CallToolRequest, _ ReadyInput) (*mcp.CallToolResult, types.Ready, error) {
	count, tabs := s.cfg.Manager.Stats()
	limits := s.cfg.Manager.Limits()
	egress := s.cfg.Egress
	if s.cfg.EgressStats != nil {
		egress = s.cfg.EgressStats()
	}
	egress.Denials += s.cfg.Manager.DeniedAdmissions()
	ready := types.Ready{
		Ready:    count < limits.MaxEphemeralSessions+limits.MaxPersistentSessions,
		Browser:  s.cfg.Manager.SharedRevision(),
		Persona:  s.cfg.Manager.Persona().Name,
		Sessions: count,
		Tabs:     tabs,
		Capacity: types.Capacity{
			EphemeralContexts:  limits.MaxEphemeralSessions,
			PersistentBrowsers: limits.MaxPersistentSessions,
			TabsPerSession:     limits.MaxTabsPerSession,
			QueuedOpsPerTab:    limits.MaxQueuedOpsPerTab,
		},
		Egress: egress,
	}
	return nil, ready, nil
}
