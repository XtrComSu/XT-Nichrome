package mcpserver

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

// HoverInput is the input for browser_hover.
type HoverInput struct {
	SessionID  string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID      string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	SnapshotID string `json:"snapshotId,omitempty" jsonschema:"snapshot ID from browser_snapshot (defaults to latest snapshot)"`
	Ref        string `json:"ref" jsonschema:"element reference from browser_snapshot, e.g. e1 or @e1"`
	Generation int64  `json:"generation,omitempty" jsonschema:"optional expected document generation"`
	TimeoutMS  int    `json:"timeoutMs,omitempty" jsonschema:"action deadline in milliseconds (default 15000)"`
}

// ScrollInput is the input for browser_scroll.
type ScrollInput struct {
	SessionID  string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID      string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	SnapshotID string `json:"snapshotId,omitempty" jsonschema:"snapshot ID (required only when scrolling a specific @eN container)"`
	Ref        string `json:"ref,omitempty" jsonschema:"optional scrollable container @eN reference; omit to scroll the viewport"`
	Direction  string `json:"direction,omitempty" jsonschema:"scroll direction: down (default), up, left, or right"`
	Amount     int    `json:"amount,omitempty" jsonschema:"scroll distance in pixels (default 480, max 10000)"`
	TimeoutMS  int    `json:"timeoutMs,omitempty" jsonschema:"action deadline in milliseconds"`
}

// PressKeyInput is the input for browser_press_key.
type PressKeyInput struct {
	SessionID  string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID      string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	SnapshotID string `json:"snapshotId,omitempty" jsonschema:"snapshot ID (required only when focusing @eN before pressing the key)"`
	Ref        string `json:"ref,omitempty" jsonschema:"optional @eN element to click/focus before pressing the key"`
	Key        string `json:"key" jsonschema:"key or modifier chord to press, e.g. Enter, Escape, Tab, ArrowDown, PageDown, Control+a, Shift+Tab"`
	TimeoutMS  int    `json:"timeoutMs,omitempty" jsonschema:"action deadline in milliseconds"`
}

// SelectOptionInput is the input for browser_select_option.
type SelectOptionInput struct {
	SessionID  string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID      string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	SnapshotID string `json:"snapshotId,omitempty" jsonschema:"snapshot ID from browser_snapshot (defaults to latest snapshot)"`
	Ref        string `json:"ref" jsonschema:"combobox/select @eN reference from browser_snapshot"`
	Value      string `json:"value" jsonschema:"option value attribute or visible option label/text to select"`
	TimeoutMS  int    `json:"timeoutMs,omitempty" jsonschema:"action deadline in milliseconds"`
}

// CheckInput is the input for browser_check and browser_uncheck.
type CheckInput struct {
	SessionID  string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID      string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	SnapshotID string `json:"snapshotId,omitempty" jsonschema:"snapshot ID from browser_snapshot (defaults to latest snapshot)"`
	Ref        string `json:"ref" jsonschema:"checkbox, radio, or switch @eN reference from browser_snapshot"`
	TimeoutMS  int    `json:"timeoutMs,omitempty" jsonschema:"action deadline in milliseconds"`
}

// DragInput is the input for browser_drag.
type DragInput struct {
	SessionID  string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID      string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	SnapshotID string `json:"snapshotId,omitempty" jsonschema:"snapshot ID from browser_snapshot (defaults to latest snapshot)"`
	Ref        string `json:"ref" jsonschema:"source element @eN reference to drag from"`
	TargetRef  string `json:"targetRef" jsonschema:"destination element @eM reference to drop onto"`
	TimeoutMS  int    `json:"timeoutMs,omitempty" jsonschema:"action deadline in milliseconds"`
}

// HistoryInput is the input for browser_navigate_back, browser_navigate_forward, and browser_reload.
type HistoryInput struct {
	SessionID string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID     string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	TimeoutMS int    `json:"timeoutMs,omitempty" jsonschema:"navigation deadline in milliseconds"`
}

// WaitForInput is the input for browser_wait_for.
type WaitForInput struct {
	SessionID string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID     string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	Text      string `json:"text,omitempty" jsonschema:"wait until this substring appears in visible page text"`
	TextGone  string `json:"textGone,omitempty" jsonschema:"wait until this substring disappears from visible page text"`
	TimeMS    int    `json:"timeMs,omitempty" jsonschema:"wait a fixed duration in milliseconds (up to 30000)"`
	TimeoutMS int    `json:"timeoutMs,omitempty" jsonschema:"maximum time to poll for text/textGone in milliseconds (default 10000)"`
}

// ExtractInput is the input for browser_extract.
type ExtractInput struct {
	SessionID string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID     string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	MaxChars  int    `json:"maxChars,omitempty" jsonschema:"maximum character budget for extracted visible text (default 12000)"`
	MaxLinks  int    `json:"maxLinks,omitempty" jsonschema:"maximum number of hyperlinks to extract (default 50)"`
}

// CookiesInput is the input for browser_cookies.
type CookiesInput struct {
	SessionID string         `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID     string         `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	Action    string         `json:"action,omitempty" jsonschema:"cookie operation: get (default), set, or clear"`
	Cookies   []types.Cookie `json:"cookies,omitempty" jsonschema:"cookies to seed when action is 'set' (must belong to policy-allowed origins)"`
}

// TelemetryInput is the input for browser_telemetry.
type TelemetryInput struct {
	SessionID string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID     string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
}

// CompanionGuideInput is the input for companion_cursor_guide.
type CompanionGuideInput struct {
	SessionID  string   `json:"sessionId,omitempty" jsonschema:"optional browser session ID when pointing at a tab element"`
	TabID      string   `json:"tabId,omitempty" jsonschema:"optional browser tab ID when pointing at a tab element"`
	SnapshotID string   `json:"snapshotId,omitempty" jsonschema:"optional snapshot ID when pointing at @eN"`
	Ref        string   `json:"ref,omitempty" jsonschema:"optional @eN element reference from browser_snapshot to point the 2nd cursor at"`
	X          *float64 `json:"x,omitempty" jsonschema:"optional screen/viewport X coordinate for the 2nd companion cursor"`
	Y          *float64 `json:"y,omitempty" jsonschema:"optional screen/viewport Y coordinate for the 2nd companion cursor"`
	Message    string   `json:"message" jsonschema:"chat bubble message around the 2nd cursor (e.g. 'hey, you should click this to help me!' or 'hey, this person isnt nice :(')"`
	Subtext    string   `json:"subtext,omitempty" jsonschema:"small guiding label next to the mini cursor (e.g. 'click here • @e4')"`
	Mood       string   `json:"mood,omitempty" jsonschema:"companion expression: friendly (default), curious, warn, sad, excited, or thinking"`
	Pulse      *bool    `json:"pulse,omitempty" jsonschema:"show expanding attention ring at the cursor tip (default true)"`
}

// CompanionControlInput is the input for companion_cursor_control.
type CompanionControlInput struct {
	Action        string `json:"action" jsonschema:"control action: status, show, hide, or clear"`
	FollowActions *bool  `json:"followActions,omitempty" jsonschema:"when true, the 2nd cursor automatically glides to and narrates every browser action"`
}

// EvaluateInput is the input for browser_evaluate.
type EvaluateInput struct {
	SessionID  string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID      string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	SnapshotID string `json:"snapshotId,omitempty" jsonschema:"optional snapshot ID when evaluating on an @eN element"`
	Ref        string `json:"ref,omitempty" jsonschema:"optional @eN element reference; when set, expression can be a function (el => ...)"`
	Expression string `json:"expression" jsonschema:"JavaScript expression or (el => ...) function to evaluate in the page context"`
	TimeoutMS  int    `json:"timeoutMs,omitempty" jsonschema:"evaluation deadline in milliseconds (default 15000)"`
}

// PDFInput is the input for browser_pdf.
type PDFInput struct {
	SessionID       string  `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID           string  `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	Landscape       bool    `json:"landscape,omitempty" jsonschema:"render pages in landscape orientation"`
	PrintBackground bool    `json:"printBackground,omitempty" jsonschema:"include CSS background graphics"`
	Scale           float64 `json:"scale,omitempty" jsonschema:"page rendering scale between 0.1 and 2.0 (default 1.0)"`
	OutputPath      string  `json:"outputPath,omitempty" jsonschema:"optional file path to write the rendered PDF bytes to"`
}

// PDFOutput is the output for browser_pdf.
type PDFOutput struct {
	SessionID  string `json:"sessionId" jsonschema:"session ID"`
	TabID      string `json:"tabId" jsonschema:"tab ID"`
	Bytes      int    `json:"bytes" jsonschema:"size of the rendered PDF in bytes"`
	OutputPath string `json:"outputPath,omitempty" jsonschema:"file path where PDF was saved, if requested"`
	DataBase64 string `json:"dataBase64,omitempty" jsonschema:"base64-encoded PDF when outputPath is omitted"`
}

// StorageInput is the input for browser_storage.
type StorageInput struct {
	SessionID string            `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID     string            `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	Area      string            `json:"area,omitempty" jsonschema:"storage area: local (default, localStorage) or session (sessionStorage)"`
	Action    string            `json:"action,omitempty" jsonschema:"operation: get (default), set, or clear"`
	Entries   map[string]string `json:"entries,omitempty" jsonschema:"key/value pairs to store when action is 'set'"`
}

// UploadFileInput is the input for browser_upload_file.
type UploadFileInput struct {
	SessionID  string   `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID      string   `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	SnapshotID string   `json:"snapshotId,omitempty" jsonschema:"snapshot ID from browser_snapshot (defaults to latest snapshot)"`
	Ref        string   `json:"ref" jsonschema:"file input element @eN reference"`
	Files      []string `json:"files" jsonschema:"local file paths to attach to the <input type='file'> element"`
	TimeoutMS  int      `json:"timeoutMs,omitempty" jsonschema:"action deadline in milliseconds"`
}

// DialogsInput is the input for browser_dialogs.
type DialogsInput struct {
	SessionID  string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID      string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	Accept     *bool  `json:"accept,omitempty" jsonschema:"optional policy for the next JavaScript dialog: true to accept (default), false to dismiss"`
	PromptText string `json:"promptText,omitempty" jsonschema:"optional prompt text to supply when accepting the next window.prompt() dialog"`
	ClearLog   bool   `json:"clearLog,omitempty" jsonschema:"when true, clear the recorded dialog history"`
}

// CompanionSelectionInput is the input for companion_analyze_selection.
type CompanionSelectionInput struct {
	Text        string   `json:"text,omitempty" jsonschema:"highlighted/selected text to analyze (omit and set readPrimary=true to read live Wayland primary selection)"`
	ReadPrimary bool     `json:"readPrimary,omitempty" jsonschema:"read the currently highlighted text from Wayland primary selection (wl-paste --primary)"`
	WindowClass string   `json:"windowClass,omitempty" jsonschema:"optional active window class (e.g. discord, firefox, kitty)"`
	WindowTitle string   `json:"windowTitle,omitempty" jsonschema:"optional active window title"`
	Guide       bool     `json:"guide,omitempty" jsonschema:"when true, pop up the 2nd companion cursor ('Ni-Cursor') with the real-time reply"`
	X           *float64 `json:"x,omitempty" jsonschema:"optional target X coordinate when guide=true"`
	Y           *float64 `json:"y,omitempty" jsonschema:"optional target Y coordinate when guide=true"`
}

// FindInput is the input for browser_find.
type FindInput struct {
	SessionID      string `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID          string `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	Query          string `json:"query,omitempty" jsonschema:"case-insensitive substring to match against element name, role, tag, or @eN ref"`
	Role           string `json:"role,omitempty" jsonschema:"optional accessibility role filter, e.g. button, textbox, link, combobox, checkbox"`
	ActionableOnly bool   `json:"actionableOnly,omitempty" jsonschema:"when true, return only actionable elements"`
	Limit          int    `json:"limit,omitempty" jsonschema:"maximum number of matched @eN refs to return (default 25, max 200)"`
}

// EmulateInput is the input for browser_emulate.
type EmulateInput struct {
	SessionID         string  `json:"sessionId,omitempty" jsonschema:"session ID (defaults to active session)"`
	TabID             string  `json:"tabId,omitempty" jsonschema:"tab ID (defaults to active tab)"`
	ViewportWidth     int     `json:"viewportWidth,omitempty" jsonschema:"optional viewport width in CSS pixels (200..7680)"`
	ViewportHeight    int     `json:"viewportHeight,omitempty" jsonschema:"optional viewport height in CSS pixels (200..4320)"`
	DeviceScaleFactor float64 `json:"deviceScaleFactor,omitempty" jsonschema:"optional device pixel ratio (0.25..4.0)"`
	ColorScheme       string  `json:"colorScheme,omitempty" jsonschema:"optional prefers-color-scheme media override: dark, light, or no-preference"`
	ReducedMotion     string  `json:"reducedMotion,omitempty" jsonschema:"optional prefers-reduced-motion media override: reduce or no-preference"`
	Offline           *bool   `json:"offline,omitempty" jsonschema:"optional network offline emulation toggle"`
}

func (s *Server) registerExtendedTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_hover",
		Description: "Move the humanized cursor along a minimum-jerk Bézier trajectory onto an @eN element and hover over it to trigger CSS :hover or JS mouseover menus/tooltips.",
	}, s.handleHover)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "browser_scroll",
		Description: "Scroll the page or a specific @eN scrollable container using humanized momentum mouse-wheel ticks in direction 'down' (default), 'up', 'left', or 'right'. " +
			"Omit ref to scroll the active viewport.",
	}, s.handleScroll)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "browser_press_key",
		Description: "Press a keyboard key or modifier chord (e.g. 'Enter', 'Escape', 'Tab', 'Backspace', 'ArrowDown', 'PageDown', 'Control+a', 'Shift+Tab') " +
			"with authentic Windows virtual key codes. Optionally pass ref to focus an @eN element first.",
	}, s.handlePressKey)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_select_option",
		Description: "Select an option inside a <select> / combobox element (@eN) by its value attribute or visible label text, dispatching input and change events.",
	}, s.handleSelectOption)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_check",
		Description: "Idempotently check a checkbox, radio button, or switch (@eN). If already checked, it hovers without toggling it off.",
	}, s.handleCheck)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_uncheck",
		Description: "Idempotently uncheck a checkbox or switch (@eN). If already unchecked, it hovers without toggling it on.",
	}, s.handleUncheck)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_drag",
		Description: "Drag from source element @eN (ref) and drop onto destination element @eM (targetRef) along a humanized minimum-jerk Bézier curve.",
	}, s.handleDrag)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_navigate_back",
		Description: "Navigate backward one entry in the tab's session history, invalidating prior snapshots.",
	}, s.handleNavigateBack)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_navigate_forward",
		Description: "Navigate forward one entry in the tab's session history, invalidating prior snapshots.",
	}, s.handleNavigateForward)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_reload",
		Description: "Reload the current tab and wait for load settle, invalidating prior snapshots.",
	}, s.handleReload)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_wait_for",
		Description: "Wait for a substring (text) to appear in visible page text, a substring (textGone) to disappear, or a fixed timeMs delay.",
	}, s.handleWaitFor)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_extract",
		Description: "Extract clean readable text, headings, hyperlinks, and scroll geometry (scrollY, scrollHeight, viewportHeight) from the active page without raw HTML noise.",
	}, s.handleExtract)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_cookies",
		Description: "Inspect (action='get'), seed (action='set', policy-checked against allowlist), or clear (action='clear') cookies in the tab's browser context.",
	}, s.handleCookies)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_telemetry",
		Description: "Return the ring-buffered console messages (console.log/warn/error, runtime exceptions), network requests/responses, and JavaScript dialogs observed on the active tab.",
	}, s.handleTelemetry)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_evaluate",
		Description: "Evaluate a JavaScript expression in the active tab (or pass ref='@eN' to invoke a function el => ... on a verified DOM element) and return its JSON value.",
	}, s.handleEvaluate)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_pdf",
		Description: "Render the active tab to a PDF document via CDP Page.printToPDF and optionally save it to outputPath.",
	}, s.handlePDF)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_storage",
		Description: "Inspect (action='get'), seed (action='set'), or clear (action='clear') localStorage (area='local') or sessionStorage (area='session') on the active tab.",
	}, s.handleStorage)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_upload_file",
		Description: "Move the humanized cursor to an <input type='file'> element (@eN) and attach local file paths via CDP DOM.setFileInputFiles.",
	}, s.handleUploadFile)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_dialogs",
		Description: "Inspect captured JavaScript alert/confirm/prompt/beforeunload dialogs on the tab, or configure how the next dialog should be handled (accept/dismiss and promptText).",
	}, s.handleDialogs)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "companion_cursor_guide",
		Description: "Drive the 2nd companion mini-cursor ('Ni-Cursor') on both the user's native Wayland/Hyprland desktop overlay and inside the browser viewport. " +
			"Move it to (x, y) or point it at a snapshot @eN ref, set its mood ('friendly', 'curious', 'warn', 'sad', 'excited', 'thinking'), " +
			"and display a friendly chat bubble (e.g. 'hey, you should click this to help me!' or 'hey, this person isnt nice :(').",
	}, s.handleCompanionGuide)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "companion_cursor_control",
		Description: "Show, hide, clear, or query the status of the Wayland/Hyprland & in-browser 2nd companion cursor, or toggle followActions auto-narration.",
	}, s.handleCompanionControl)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "companion_analyze_selection",
		Description: "Analyze highlighted/selected text (or read live Wayland primary selection via readPrimary=true) from Discord, browser, or terminal, " +
			"classify tone/intent or decode base64/hex/timestamps, and optionally pop up Ni-Cursor with a real-time reply.",
	}, s.handleCompanionSelection)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "browser_find",
		Description: "Search the active tab's accessibility snapshot for @eN elements by substring query and/or role (e.g. 'button', 'textbox', 'link'), " +
			"automatically capturing a fresh snapshot if needed and remembering snapshotId for immediate interaction.",
	}, s.handleFind)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "browser_emulate",
		Description: "Dynamically adjust viewport dimensions (width, height, deviceScaleFactor), prefers-color-scheme ('dark'|'light'), prefers-reduced-motion, or offline network state on the active tab.",
	}, s.handleEmulate)
}

func (s *Server) handleHover(ctx context.Context, req *mcp.CallToolRequest, in HoverInput) (*mcp.CallToolResult, ActionOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, ActionOutput{}, FormatError(apiErr)
	}
	snapID := s.resolveSnapshotID(req, tid, in.SnapshotID)
	return s.execAction(ctx, sid, tid, types.ActionRequest{
		Kind:       types.ActionHover,
		SnapshotID: snapID,
		Ref:        strings.TrimPrefix(strings.TrimSpace(in.Ref), "@"),
		Generation: in.Generation,
		TimeoutMS:  in.TimeoutMS,
	})
}

func (s *Server) handleScroll(ctx context.Context, req *mcp.CallToolRequest, in ScrollInput) (*mcp.CallToolResult, ActionOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, ActionOutput{}, FormatError(apiErr)
	}
	ref := strings.TrimPrefix(strings.TrimSpace(in.Ref), "@")
	snapID := ""
	if ref != "" {
		snapID = s.resolveSnapshotID(req, tid, in.SnapshotID)
	}
	return s.execAction(ctx, sid, tid, types.ActionRequest{
		Kind:       types.ActionScroll,
		SnapshotID: snapID,
		Ref:        ref,
		Direction:  in.Direction,
		Amount:     in.Amount,
		TimeoutMS:  in.TimeoutMS,
	})
}

func (s *Server) handlePressKey(ctx context.Context, req *mcp.CallToolRequest, in PressKeyInput) (*mcp.CallToolResult, ActionOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, ActionOutput{}, FormatError(apiErr)
	}
	ref := strings.TrimPrefix(strings.TrimSpace(in.Ref), "@")
	snapID := ""
	if ref != "" {
		snapID = s.resolveSnapshotID(req, tid, in.SnapshotID)
	}
	return s.execAction(ctx, sid, tid, types.ActionRequest{
		Kind:       types.ActionPress,
		SnapshotID: snapID,
		Ref:        ref,
		Key:        in.Key,
		TimeoutMS:  in.TimeoutMS,
	})
}

func (s *Server) handleSelectOption(ctx context.Context, req *mcp.CallToolRequest, in SelectOptionInput) (*mcp.CallToolResult, ActionOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, ActionOutput{}, FormatError(apiErr)
	}
	snapID := s.resolveSnapshotID(req, tid, in.SnapshotID)
	return s.execAction(ctx, sid, tid, types.ActionRequest{
		Kind:       types.ActionSelect,
		SnapshotID: snapID,
		Ref:        strings.TrimPrefix(strings.TrimSpace(in.Ref), "@"),
		Value:      in.Value,
		TimeoutMS:  in.TimeoutMS,
	})
}

func (s *Server) handleCheck(ctx context.Context, req *mcp.CallToolRequest, in CheckInput) (*mcp.CallToolResult, ActionOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, ActionOutput{}, FormatError(apiErr)
	}
	snapID := s.resolveSnapshotID(req, tid, in.SnapshotID)
	return s.execAction(ctx, sid, tid, types.ActionRequest{
		Kind:       types.ActionCheck,
		SnapshotID: snapID,
		Ref:        strings.TrimPrefix(strings.TrimSpace(in.Ref), "@"),
		TimeoutMS:  in.TimeoutMS,
	})
}

func (s *Server) handleUncheck(ctx context.Context, req *mcp.CallToolRequest, in CheckInput) (*mcp.CallToolResult, ActionOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, ActionOutput{}, FormatError(apiErr)
	}
	snapID := s.resolveSnapshotID(req, tid, in.SnapshotID)
	return s.execAction(ctx, sid, tid, types.ActionRequest{
		Kind:       types.ActionUncheck,
		SnapshotID: snapID,
		Ref:        strings.TrimPrefix(strings.TrimSpace(in.Ref), "@"),
		TimeoutMS:  in.TimeoutMS,
	})
}

func (s *Server) handleDrag(ctx context.Context, req *mcp.CallToolRequest, in DragInput) (*mcp.CallToolResult, ActionOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, ActionOutput{}, FormatError(apiErr)
	}
	snapID := s.resolveSnapshotID(req, tid, in.SnapshotID)
	return s.execAction(ctx, sid, tid, types.ActionRequest{
		Kind:       types.ActionDrag,
		SnapshotID: snapID,
		Ref:        strings.TrimPrefix(strings.TrimSpace(in.Ref), "@"),
		TargetRef:  strings.TrimPrefix(strings.TrimSpace(in.TargetRef), "@"),
		TimeoutMS:  in.TimeoutMS,
	})
}

func (s *Server) handleNavigateBack(ctx context.Context, req *mcp.CallToolRequest, in HistoryInput) (*mcp.CallToolResult, NavigateOutput, error) {
	return s.execHistory(ctx, req, in, "back")
}

func (s *Server) handleNavigateForward(ctx context.Context, req *mcp.CallToolRequest, in HistoryInput) (*mcp.CallToolResult, NavigateOutput, error) {
	return s.execHistory(ctx, req, in, "forward")
}

func (s *Server) handleReload(ctx context.Context, req *mcp.CallToolRequest, in HistoryInput) (*mcp.CallToolResult, NavigateOutput, error) {
	return s.execHistory(ctx, req, in, "reload")
}

func (s *Server) execHistory(ctx context.Context, req *mcp.CallToolRequest, in HistoryInput, action string) (*mcp.CallToolResult, NavigateOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, NavigateOutput{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, apiErr := s.cfg.Manager.History(ctx, sid, tid, types.HistoryRequest{
		Action:    action,
		TimeoutMS: in.TimeoutMS,
	})
	if apiErr != nil {
		return nil, NavigateOutput{}, FormatError(apiErr)
	}
	return nil, NavigateOutput{
		SessionID:             sid,
		TabID:                 tid,
		URL:                   res.URL,
		Generation:            res.Generation,
		Title:                 res.Title,
		FreshSnapshotRequired: true,
	}, nil
}

func (s *Server) handleWaitFor(ctx context.Context, req *mcp.CallToolRequest, in WaitForInput) (*mcp.CallToolResult, types.WaitResult, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, types.WaitResult{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 65*time.Second)
	defer cancel()
	res, apiErr := s.cfg.Manager.WaitFor(ctx, sid, tid, types.WaitRequest{
		Text:      in.Text,
		TextGone:  in.TextGone,
		TimeMS:    in.TimeMS,
		TimeoutMS: in.TimeoutMS,
	})
	if apiErr != nil {
		return nil, types.WaitResult{}, FormatError(apiErr)
	}
	return nil, *res, nil
}

func (s *Server) handleExtract(ctx context.Context, req *mcp.CallToolRequest, in ExtractInput) (*mcp.CallToolResult, types.ExtractResult, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, types.ExtractResult{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, apiErr := s.cfg.Manager.Extract(ctx, sid, tid, types.ExtractRequest{
		MaxChars: in.MaxChars,
		MaxLinks: in.MaxLinks,
	})
	if apiErr != nil {
		return nil, types.ExtractResult{}, FormatError(apiErr)
	}
	return nil, *res, nil
}

func (s *Server) handleCookies(ctx context.Context, req *mcp.CallToolRequest, in CookiesInput) (*mcp.CallToolResult, types.CookiesResult, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, types.CookiesResult{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	act := strings.ToLower(strings.TrimSpace(in.Action))
	if act == "" {
		act = "get"
	}
	switch act {
	case "get":
		res, apiErr := s.cfg.Manager.GetCookies(ctx, sid, tid)
		if apiErr != nil {
			return nil, types.CookiesResult{}, FormatError(apiErr)
		}
		return nil, *res, nil
	case "set":
		res, apiErr := s.cfg.Manager.SetCookies(ctx, sid, tid, types.SetCookiesRequest{Cookies: in.Cookies})
		if apiErr != nil {
			return nil, types.CookiesResult{}, FormatError(apiErr)
		}
		return nil, *res, nil
	case "clear":
		if apiErr := s.cfg.Manager.ClearCookies(ctx, sid, tid); apiErr != nil {
			return nil, types.CookiesResult{}, FormatError(apiErr)
		}
		return nil, types.CookiesResult{Cookies: []types.Cookie{}}, nil
	default:
		return nil, types.CookiesResult{}, FormatError(&types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "action must be get, set, or clear",
		})
	}
}

func (s *Server) handleTelemetry(ctx context.Context, req *mcp.CallToolRequest, in TelemetryInput) (*mcp.CallToolResult, types.TelemetryResult, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, types.TelemetryResult{}, FormatError(apiErr)
	}
	res, apiErr := s.cfg.Manager.Telemetry(ctx, sid, tid)
	if apiErr != nil {
		return nil, types.TelemetryResult{}, FormatError(apiErr)
	}
	return nil, *res, nil
}

func (s *Server) handleCompanionGuide(ctx context.Context, req *mcp.CallToolRequest, in CompanionGuideInput) (*mcp.CallToolResult, types.CompanionState, error) {
	sid := in.SessionID
	tid := in.TabID
	snapID := in.SnapshotID
	ref := strings.TrimPrefix(strings.TrimSpace(in.Ref), "@")
	// If pointing at an element ref or if an active session/tab exists, resolve
	// it so the in-browser companion overlay updates alongside Wayland/Hyprland.
	if sid == "" || tid == "" {
		if rSid, rTid, err := s.resolveSessionAndTab(req, sid, tid); err == nil {
			sid, tid = rSid, rTid
		}
	}
	if ref != "" && snapID == "" && tid != "" {
		snapID = s.resolveSnapshotID(req, tid, "")
	}
	st, apiErr := s.cfg.Manager.Companion(ctx, types.CompanionRequest{
		Action:     "guide",
		SessionID:  sid,
		TabID:      tid,
		SnapshotID: snapID,
		Ref:        ref,
		X:          in.X,
		Y:          in.Y,
		Message:    in.Message,
		Subtext:    in.Subtext,
		Mood:       in.Mood,
		Pulse:      in.Pulse,
	})
	if apiErr != nil {
		return nil, types.CompanionState{}, FormatError(apiErr)
	}
	return nil, *st, nil
}

func (s *Server) handleCompanionControl(ctx context.Context, req *mcp.CallToolRequest, in CompanionControlInput) (*mcp.CallToolResult, types.CompanionState, error) {
	var sid, tid string
	if rSid, rTid, err := s.resolveSessionAndTab(req, "", ""); err == nil {
		sid, tid = rSid, rTid
	}
	st, apiErr := s.cfg.Manager.Companion(ctx, types.CompanionRequest{
		Action:        in.Action,
		SessionID:     sid,
		TabID:         tid,
		FollowActions: in.FollowActions,
	})
	if apiErr != nil {
		return nil, types.CompanionState{}, FormatError(apiErr)
	}
	return nil, *st, nil
}

func (s *Server) handleEvaluate(ctx context.Context, req *mcp.CallToolRequest, in EvaluateInput) (*mcp.CallToolResult, types.EvaluateResult, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, types.EvaluateResult{}, FormatError(apiErr)
	}
	ref := strings.TrimPrefix(strings.TrimSpace(in.Ref), "@")
	snapID := in.SnapshotID
	if ref != "" && snapID == "" {
		snapID = s.resolveSnapshotID(req, tid, "")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, apiErr := s.cfg.Manager.Evaluate(ctx, sid, tid, types.EvaluateRequest{
		Expression: in.Expression,
		SnapshotID: snapID,
		Ref:        ref,
		TimeoutMS:  in.TimeoutMS,
	})
	if apiErr != nil {
		return nil, types.EvaluateResult{}, FormatError(apiErr)
	}
	return nil, *res, nil
}

func (s *Server) handlePDF(ctx context.Context, req *mcp.CallToolRequest, in PDFInput) (*mcp.CallToolResult, PDFOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, PDFOutput{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	pdf, apiErr := s.cfg.Manager.PDF(ctx, sid, tid, types.PDFRequest{
		Landscape:       in.Landscape,
		PrintBackground: in.PrintBackground,
		Scale:           in.Scale,
	})
	if apiErr != nil {
		return nil, PDFOutput{}, FormatError(apiErr)
	}
	out := PDFOutput{
		SessionID: sid,
		TabID:     tid,
		Bytes:     len(pdf),
	}
	if in.OutputPath != "" {
		if err := os.WriteFile(in.OutputPath, pdf, 0o600); err != nil {
			return nil, PDFOutput{}, FormatError(&types.APIError{
				Code:    types.CodeInvalidRequest,
				Message: "could not write PDF file: " + err.Error(),
			})
		}
		out.OutputPath = in.OutputPath
	} else {
		out.DataBase64 = base64.StdEncoding.EncodeToString(pdf)
	}
	return nil, out, nil
}

func (s *Server) handleStorage(ctx context.Context, req *mcp.CallToolRequest, in StorageInput) (*mcp.CallToolResult, types.StorageResult, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, types.StorageResult{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, apiErr := s.cfg.Manager.Storage(ctx, sid, tid, types.StorageRequest{
		Area:    in.Area,
		Action:  in.Action,
		Entries: in.Entries,
	})
	if apiErr != nil {
		return nil, types.StorageResult{}, FormatError(apiErr)
	}
	return nil, *res, nil
}

func (s *Server) handleUploadFile(ctx context.Context, req *mcp.CallToolRequest, in UploadFileInput) (*mcp.CallToolResult, ActionOutput, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, ActionOutput{}, FormatError(apiErr)
	}
	snapID := s.resolveSnapshotID(req, tid, in.SnapshotID)
	return s.execAction(ctx, sid, tid, types.ActionRequest{
		Kind:       types.ActionUpload,
		SnapshotID: snapID,
		Ref:        strings.TrimPrefix(strings.TrimSpace(in.Ref), "@"),
		Files:      in.Files,
		TimeoutMS:  in.TimeoutMS,
	})
}

func (s *Server) handleDialogs(ctx context.Context, req *mcp.CallToolRequest, in DialogsInput) (*mcp.CallToolResult, types.DialogsResult, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, types.DialogsResult{}, FormatError(apiErr)
	}
	var pol *types.DialogPolicyRequest
	if in.Accept != nil || in.PromptText != "" || in.ClearLog {
		pol = &types.DialogPolicyRequest{
			Accept:     in.Accept,
			PromptText: in.PromptText,
			ClearLog:   in.ClearLog,
		}
	}
	res, apiErr := s.cfg.Manager.Dialogs(ctx, sid, tid, pol)
	if apiErr != nil {
		return nil, types.DialogsResult{}, FormatError(apiErr)
	}
	return nil, *res, nil
}

func (s *Server) handleCompanionSelection(ctx context.Context, _ *mcp.CallToolRequest, in CompanionSelectionInput) (*mcp.CallToolResult, types.SelectionAnalysis, error) {
	res, apiErr := s.cfg.Manager.CompanionSelection(ctx, types.SelectionRequest{
		Text:        in.Text,
		ReadPrimary: in.ReadPrimary,
		WindowClass: in.WindowClass,
		WindowTitle: in.WindowTitle,
		Guide:       in.Guide,
		X:           in.X,
		Y:           in.Y,
	})
	if apiErr != nil {
		return nil, types.SelectionAnalysis{}, FormatError(apiErr)
	}
	return nil, *res, nil
}

func (s *Server) handleFind(ctx context.Context, req *mcp.CallToolRequest, in FindInput) (*mcp.CallToolResult, types.FindResult, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, types.FindResult{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, apiErr := s.cfg.Manager.Find(ctx, sid, tid, types.FindRequest{
		Query:          in.Query,
		Role:           in.Role,
		ActionableOnly: in.ActionableOnly,
		Limit:          in.Limit,
	})
	if apiErr != nil {
		return nil, types.FindResult{}, FormatError(apiErr)
	}
	s.rememberSnapshot(req, sid, tid, res.SnapshotID)
	return nil, *res, nil
}

func (s *Server) handleEmulate(ctx context.Context, req *mcp.CallToolRequest, in EmulateInput) (*mcp.CallToolResult, types.EmulateResult, error) {
	sid, tid, apiErr := s.resolveSessionAndTab(req, in.SessionID, in.TabID)
	if apiErr != nil {
		return nil, types.EmulateResult{}, FormatError(apiErr)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, apiErr := s.cfg.Manager.Emulate(ctx, sid, tid, types.EmulateRequest{
		ViewportWidth:     in.ViewportWidth,
		ViewportHeight:    in.ViewportHeight,
		DeviceScaleFactor: in.DeviceScaleFactor,
		ColorScheme:       in.ColorScheme,
		ReducedMotion:     in.ReducedMotion,
		Offline:           in.Offline,
	})
	if apiErr != nil {
		return nil, types.EmulateResult{}, FormatError(apiErr)
	}
	return nil, *res, nil
}
