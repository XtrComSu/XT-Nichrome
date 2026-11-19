// Package mcpserver exposes XT-Nichrome as a Model Context Protocol (MCP)
// server wrapping sessions.Manager directly.
//
// Design:
//   - Wraps *sessions.Manager in-process (no HTTP round-trip).
//   - Tracks per-MCP-client-session affinity (active sessionId, tabId, and
//     latest snapshotId per tab) so an agent can omit boilerplate IDs in
//     sequential tool calls while still allowing explicit overrides.
//   - Translates every types.APIError into an actionable tool error (IsError:
//     true via ToolHandlerFor) that tells the agent what to do next instead of
//     dumping a raw status code.
//   - Surfaces ACTION_OUTCOME_UNKNOWN as a structured non-failure warning
//     ("may have landed — verify with browser_snapshot before retrying") so an
//     agent never blindly double-submits a form.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xtrcom/xt-nichrome/internal/sessions"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

// DefaultMaxTabsPerClient bounds how many tabs one MCP client session may hold
// open concurrently across its sessions to stop runaway agent loops.
const DefaultMaxTabsPerClient = 8

// Config configures the MCP server wrapper.
type Config struct {
	Manager          *sessions.Manager
	Version          string
	Egress           types.EgressInfo
	EgressStats      func() types.EgressInfo
	MaxTabsPerClient int
}

// Server wraps sessions.Manager as an MCP server with per-client affinity.
type Server struct {
	cfg Config
	mcp *mcp.Server

	mu       sync.Mutex
	affinity map[*mcp.ServerSession]*clientAffinity
	fallback *clientAffinity
}

type clientAffinity struct {
	sessionID string
	tabID     string
	// tabBySession remembers the last active tab for each browser session.
	tabBySession map[string]string
	// snapByTab remembers the most recent snapshotId for each tab.
	snapByTab map[string]string
	// openTabs counts tabs opened by this MCP client that have not been closed.
	openTabs map[string]string // tabID -> sessionID
}

func newAffinity() *clientAffinity {
	return &clientAffinity{
		tabBySession: make(map[string]string),
		snapByTab:    make(map[string]string),
		openTabs:     make(map[string]string),
	}
}

// New constructs an MCP server with all browser tools registered.
func New(cfg Config) (*Server, error) {
	if cfg.Manager == nil {
		return nil, errors.New("mcpserver: sessions.Manager is required")
	}
	if cfg.Version == "" {
		cfg.Version = "1.0.0"
	}
	if cfg.MaxTabsPerClient <= 0 {
		cfg.MaxTabsPerClient = DefaultMaxTabsPerClient
	}
	impl := &mcp.Implementation{
		Name:    "xt-nichrome",
		Version: cfg.Version,
	}
	opts := &mcp.ServerOptions{
		Instructions: "XT-Nichrome stealth browser service. Typical workflow: " +
			"1) browser_open_session, 2) browser_open_tab (optionally with a URL), " +
			"3) browser_navigate, 4) browser_snapshot to obtain @eN element refs and a snapshotId, " +
			"5) browser_click or browser_fill using that snapshotId and ref. " +
			"Session, tab, and snapshot IDs are remembered automatically per MCP session when omitted.",
	}
	s := &Server{
		cfg:      cfg,
		mcp:      mcp.NewServer(impl, opts),
		affinity: make(map[*mcp.ServerSession]*clientAffinity),
		fallback: newAffinity(),
	}
	s.registerTools()
	return s, nil
}

// MCP returns the underlying *mcp.Server.
func (s *Server) MCP() *mcp.Server { return s.mcp }

// Run serves MCP over the given transport until ctx is cancelled or the
// transport closes.
func (s *Server) Run(ctx context.Context, t mcp.Transport) error {
	return s.mcp.Run(ctx, t)
}

// HTTPHandler returns a Streamable HTTP handler suitable for mounting at /mcp.
func (s *Server) HTTPHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		return s.mcp
	}, nil)
}

func (s *Server) stateFor(req *mcp.CallToolRequest) *clientAffinity {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req == nil || req.Session == nil {
		return s.fallback
	}
	st, ok := s.affinity[req.Session]
	if !ok {
		st = newAffinity()
		s.affinity[req.Session] = st
	}
	return st
}

func (s *Server) rememberSession(req *mcp.CallToolRequest, sessionID string) {
	st := s.stateFor(req)
	s.mu.Lock()
	defer s.mu.Unlock()
	st.sessionID = sessionID
	if tid := st.tabBySession[sessionID]; tid != "" {
		st.tabID = tid
	}
}

func (s *Server) forgetSession(req *mcp.CallToolRequest, sessionID string) {
	st := s.stateFor(req)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(st.tabBySession, sessionID)
	for tid, sid := range st.openTabs {
		if sid == sessionID {
			delete(st.openTabs, tid)
			delete(st.snapByTab, tid)
		}
	}
	if st.sessionID == sessionID {
		st.sessionID = ""
		st.tabID = ""
	}
}

func (s *Server) rememberTab(req *mcp.CallToolRequest, sessionID, tabID string) {
	st := s.stateFor(req)
	s.mu.Lock()
	defer s.mu.Unlock()
	st.sessionID = sessionID
	st.tabID = tabID
	st.tabBySession[sessionID] = tabID
	st.openTabs[tabID] = sessionID
}

func (s *Server) forgetTab(req *mcp.CallToolRequest, sessionID, tabID string) {
	st := s.stateFor(req)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(st.openTabs, tabID)
	delete(st.snapByTab, tabID)
	if st.tabID == tabID {
		st.tabID = ""
	}
	if st.tabBySession[sessionID] == tabID {
		delete(st.tabBySession, sessionID)
	}
}

func (s *Server) rememberSnapshot(req *mcp.CallToolRequest, sessionID, tabID, snapshotID string) {
	st := s.stateFor(req)
	s.mu.Lock()
	defer s.mu.Unlock()
	st.sessionID = sessionID
	st.tabID = tabID
	st.tabBySession[sessionID] = tabID
	st.snapByTab[tabID] = snapshotID
}

func (s *Server) resolveSession(req *mcp.CallToolRequest, explicit string) (string, *types.APIError) {
	if explicit != "" {
		return explicit, nil
	}
	st := s.stateFor(req)
	s.mu.Lock()
	sid := st.sessionID
	s.mu.Unlock()
	if sid == "" {
		return "", &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "sessionId was not provided and no active session is remembered for this MCP connection",
			Detail:  map[string]any{"field": "sessionId"},
		}
	}
	return sid, nil
}

func (s *Server) resolveSessionAndTab(req *mcp.CallToolRequest, explicitSession, explicitTab string) (string, string, *types.APIError) {
	sid, apiErr := s.resolveSession(req, explicitSession)
	if apiErr != nil {
		return "", "", apiErr
	}
	if explicitTab != "" {
		return sid, explicitTab, nil
	}
	st := s.stateFor(req)
	s.mu.Lock()
	tid := st.tabBySession[sid]
	if tid == "" && st.sessionID == sid {
		tid = st.tabID
	}
	s.mu.Unlock()
	if tid == "" {
		return "", "", &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "tabId was not provided and no active tab is remembered for session " + sid,
			Detail:  map[string]any{"field": "tabId", "sessionId": sid},
		}
	}
	return sid, tid, nil
}

func (s *Server) resolveSnapshotID(req *mcp.CallToolRequest, tabID, explicitSnap string) string {
	if explicitSnap != "" {
		return explicitSnap
	}
	st := s.stateFor(req)
	s.mu.Lock()
	defer s.mu.Unlock()
	return st.snapByTab[tabID]
}

func (s *Server) checkClientTabCap(req *mcp.CallToolRequest) *types.APIError {
	st := s.stateFor(req)
	s.mu.Lock()
	openCount := len(st.openTabs)
	s.mu.Unlock()
	if openCount >= s.cfg.MaxTabsPerClient {
		return &types.APIError{
			Code:    types.CodeCapacity,
			Message: fmt.Sprintf("MCP client tab cap reached (%d open tabs)", s.cfg.MaxTabsPerClient),
			Detail:  map[string]any{"limit": s.cfg.MaxTabsPerClient, "inUse": openCount},
		}
	}
	return nil
}

// FormatError translates a types.APIError into an agent-actionable error with
// next-step recovery instructions.
func FormatError(apiErr *types.APIError) error {
	if apiErr == nil {
		return nil
	}
	var guidance string
	switch apiErr.Code {
	case types.CodeStaleRef:
		guidance = "Next step: call browser_snapshot to capture a fresh accessibility tree and updated @eN refs before retrying the action."
	case types.CodeAmbiguousRef:
		guidance = "Next step: call browser_snapshot and pick a distinct element ref."
	case types.CodeElementNotActionable:
		guidance = "Next step: inspect the element's role and state with browser_snapshot. Use browser_click for buttons/checkboxes/links and browser_fill only for text inputs, or wait if the element is disabled/covered."
	case types.CodePolicyBlocked:
		guidance = "Next step: the target origin is blocked by the daemon's egress allowlist (-allow). Do not retry this URL; only navigate to allowlisted origins."
	case types.CodeCapacity:
		guidance = "Next step: call browser_list_tabs or browser_list_sessions and close unused tabs (browser_close_tab) or sessions (browser_close_session) to free capacity before opening new ones."
	case types.CodeBusyProfile:
		guidance = "Next step: the persistent profile or tab queue is busy. Wait for in-flight operations to finish, close the existing session holding the profile, or use an ephemeral session."
	case types.CodeNotFound:
		guidance = "Next step: call browser_list_sessions or browser_list_tabs to see live handles, or open a new session/tab with browser_open_session / browser_open_tab."
	case types.CodeTimeout:
		guidance = "Next step: the operation exceeded its deadline. Call browser_snapshot to inspect whether the page partially loaded before retrying."
	case types.CodeActionOutcomeUnknown:
		guidance = "Next step: the action was already dispatched and MAY have landed on the page. Do NOT blindly retry; call browser_snapshot first to verify whether the action took effect."
	case types.CodeBrowserFailed:
		guidance = "Next step: the underlying browser process exited or crashed. Open a fresh session with browser_open_session."
	case types.CodeInvalidRequest:
		guidance = "Next step: fix the invalid tool argument indicated in the error details and try again."
	default:
		guidance = "Next step: inspect service status with browser_ready or take a fresh snapshot with browser_snapshot."
	}
	var detailStr string
	if len(apiErr.Detail) > 0 {
		if b, err := json.Marshal(apiErr.Detail); err == nil {
			detailStr = " " + string(b)
		}
	}
	return fmt.Errorf("[%s] %s%s — %s", apiErr.Code, apiErr.Message, detailStr, guidance)
}
