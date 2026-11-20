// Package api is the HTTP surface. It is intentionally strict: unknown fields,
// wrong content types and duplicated JSON keys are rejected, because a silently
// ignored typo turns into a wrong action against a live page.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/sessions"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

// MaxBodyBytes bounds request bodies.
const MaxBodyBytes = 1 << 20

// Config configures the server.
type Config struct {
	Manager      *sessions.Manager
	Token        string
	Logger       *slog.Logger
	MaxBodyBytes int64
	// Version is reported by healthz.
	Version string
	// Egress describes the policy boundary for readiness reporting.
	Egress types.EgressInfo
	// EgressStats, when set, is read on every /readyz so the denial counter is
	// live. A value captured at startup would always report zero, which is worse
	// than omitting the field: it looks like nothing is ever blocked.
	EgressStats func() types.EgressInfo
	// MCPHandler, when non-nil, is mounted at /mcp behind bearer authentication.
	MCPHandler http.Handler
}

// Server is the HTTP API.
type Server struct {
	cfg  Config
	log  *slog.Logger
	mux  *http.ServeMux
	reqs atomic.Int64
}

// New builds a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Manager == nil {
		return nil, fmt.Errorf("api: manager is required")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("api: a bearer token is required; refusing to start an unauthenticated service")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = MaxBodyBytes
	}
	s := &Server{cfg: cfg, log: cfg.Logger, mux: http.NewServeMux()}
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /readyz", s.handleReady)

	s.mux.HandleFunc("POST /v1/sessions", s.auth(s.handleCreateSession))
	s.mux.HandleFunc("GET /v1/sessions", s.auth(s.handleListSessions))
	s.mux.HandleFunc("GET /v1/sessions/{id}", s.auth(s.handleGetSession))
	s.mux.HandleFunc("DELETE /v1/sessions/{id}", s.auth(s.handleDeleteSession))

	s.mux.HandleFunc("POST /v1/sessions/{id}/tabs", s.auth(s.handleCreateTab))
	s.mux.HandleFunc("GET /v1/sessions/{id}/tabs", s.auth(s.handleListTabs))
	s.mux.HandleFunc("DELETE /v1/sessions/{id}/tabs/{tab}", s.auth(s.handleDeleteTab))

	s.mux.HandleFunc("POST /v1/sessions/{id}/tabs/{tab}/navigate", s.auth(s.handleNavigate))
	s.mux.HandleFunc("POST /v1/sessions/{id}/tabs/{tab}/history", s.auth(s.handleHistory))
	s.mux.HandleFunc("POST /v1/sessions/{id}/tabs/{tab}/wait", s.auth(s.handleWait))
	s.mux.HandleFunc("GET /v1/sessions/{id}/tabs/{tab}/snapshot", s.auth(s.handleSnapshot))
	s.mux.HandleFunc("GET /v1/sessions/{id}/tabs/{tab}/extract", s.auth(s.handleExtract))
	s.mux.HandleFunc("POST /v1/sessions/{id}/tabs/{tab}/actions", s.auth(s.handleAction))
	s.mux.HandleFunc("GET /v1/sessions/{id}/tabs/{tab}/screenshot", s.auth(s.handleScreenshot))
	s.mux.HandleFunc("GET /v1/sessions/{id}/tabs/{tab}/persona", s.auth(s.handlePersona))
	s.mux.HandleFunc("GET /v1/sessions/{id}/tabs/{tab}/telemetry", s.auth(s.handleTelemetry))
	s.mux.HandleFunc("GET /v1/sessions/{id}/tabs/{tab}/cookies", s.auth(s.handleGetCookies))
	s.mux.HandleFunc("POST /v1/sessions/{id}/tabs/{tab}/cookies", s.auth(s.handleSetCookies))
	s.mux.HandleFunc("DELETE /v1/sessions/{id}/tabs/{tab}/cookies", s.auth(s.handleClearCookies))
	s.mux.HandleFunc("POST /v1/sessions/{id}/tabs/{tab}/evaluate", s.auth(s.handleEvaluate))
	s.mux.HandleFunc("GET /v1/sessions/{id}/tabs/{tab}/pdf", s.auth(s.handlePDF))
	s.mux.HandleFunc("POST /v1/sessions/{id}/tabs/{tab}/pdf", s.auth(s.handlePDF))
	s.mux.HandleFunc("GET /v1/sessions/{id}/tabs/{tab}/storage", s.auth(s.handleGetStorage))
	s.mux.HandleFunc("POST /v1/sessions/{id}/tabs/{tab}/storage", s.auth(s.handlePostStorage))
	s.mux.HandleFunc("DELETE /v1/sessions/{id}/tabs/{tab}/storage", s.auth(s.handleClearStorage))
	s.mux.HandleFunc("GET /v1/sessions/{id}/tabs/{tab}/dialogs", s.auth(s.handleGetDialogs))
	s.mux.HandleFunc("POST /v1/sessions/{id}/tabs/{tab}/dialogs", s.auth(s.handlePostDialogs))
	s.mux.HandleFunc("POST /v1/sessions/{id}/tabs/{tab}/find", s.auth(s.handleFind))
	s.mux.HandleFunc("POST /v1/sessions/{id}/tabs/{tab}/emulate", s.auth(s.handleEmulate))

	s.mux.HandleFunc("GET /v1/companion", s.auth(s.handleGetCompanion))
	s.mux.HandleFunc("POST /v1/companion", s.auth(s.handlePostCompanion))
	s.mux.HandleFunc("POST /v1/companion/selection", s.auth(s.handleCompanionSelection))

	if s.cfg.MCPHandler != nil {
		s.mux.HandleFunc("/mcp", s.auth(s.cfg.MCPHandler.ServeHTTP))
	}
}

// Handler returns the root handler with recovery and request ids.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID(r)
		w.Header().Set("X-Request-Id", id)
		r = r.WithContext(withRequestID(r.Context(), id))
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic while handling request", "requestId", id, "path", r.URL.Path, "panic", fmt.Sprint(rec))
				writeError(w, &types.APIError{Code: types.CodeInternal, Message: "internal error", RequestID: id})
			}
		}()
		s.reqs.Add(1)
		s.mux.ServeHTTP(w, r)
	})
}

type ctxKey string

const requestIDKey ctxKey = "requestId"

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func requestIDOf(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

func requestID(r *http.Request) string {
	if v := r.Header.Get("X-Request-Id"); validRequestID(v) {
		return v
	}
	return newRequestID()
}

func validRequestID(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

var idSeq atomic.Int64

func newRequestID() string {
	n := idSeq.Add(1)
	return fmt.Sprintf("req-%d-%d", time.Now().UnixNano()%1e9, n)
}

// auth enforces the bearer token in constant time and rejects anything else.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	want := []byte("Bearer " + s.cfg.Token)
	return func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="xt-nichrome"`)
			writeError(w, &types.APIError{
				Code: types.CodeUnauthorized, Message: "a valid bearer token is required",
				RequestID: requestIDOf(r.Context()),
			})
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, types.Health{Status: "ok", Version: s.cfg.Version})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	count, tabs := s.cfg.Manager.Stats()
	ready := types.Ready{
		Ready:    true,
		Browser:  s.cfg.Manager.SharedRevision(),
		Persona:  s.cfg.Manager.Persona().Name,
		Sessions: count,
		Tabs:     tabs,
		Capacity: types.Capacity{
			EphemeralContexts:  s.cfg.Manager.Limits().MaxEphemeralSessions,
			PersistentBrowsers: s.cfg.Manager.Limits().MaxPersistentSessions,
			TabsPerSession:     s.cfg.Manager.Limits().MaxTabsPerSession,
			QueuedOpsPerTab:    s.cfg.Manager.Limits().MaxQueuedOpsPerTab,
		},
		Egress: s.cfg.Egress,
	}
	if s.cfg.EgressStats != nil {
		ready.Egress = s.cfg.EgressStats()
	}
	// Admission refusals happen before the proxy is consulted, so they are
	// counted separately and folded in here: an operator needs one number for
	// "how much has the policy stopped", not one per enforcement layer.
	ready.Egress.Denials += s.cfg.Manager.DeniedAdmissions()
	// Ready means "can accept work now", not "process is up".
	if count >= s.cfg.Manager.Limits().MaxEphemeralSessions+s.cfg.Manager.Limits().MaxPersistentSessions {
		ready.Ready = false
	}
	status := http.StatusOK
	if !ready.Ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, ready)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req types.CreateSessionRequest
	if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, true); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.Create(ctx, req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	list := s.cfg.Manager.List()
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	out, apiErr := s.cfg.Manager.Describe(r.PathValue("id"))
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if apiErr := s.cfg.Manager.Close(ctx, r.PathValue("id")); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateTab(w http.ResponseWriter, r *http.Request) {
	var req types.CreateTabRequest
	if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, true); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.CreateTab(ctx, r.PathValue("id"), req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleListTabs(w http.ResponseWriter, r *http.Request) {
	tabs, apiErr := s.cfg.Manager.ListTabs(r.PathValue("id"))
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tabs": tabs})
}

func (s *Server) handleDeleteTab(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if apiErr := s.cfg.Manager.CloseTab(ctx, r.PathValue("id"), r.PathValue("tab")); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNavigate(w http.ResponseWriter, r *http.Request) {
	var req types.NavigateRequest
	if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, false); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.Navigate(ctx, r.PathValue("id"), r.PathValue("tab"), req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	req, apiErr := snapshotQuery(r)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.Snapshot(ctx, r.PathValue("id"), r.PathValue("tab"), req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	var req types.ActionRequest
	if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, false); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.Act(ctx, r.PathValue("id"), r.PathValue("tab"), req)
	if apiErr != nil {
		if out != nil {
			// Ambiguous outcome: the body still describes what is known.
			writeJSON(w, types.Status(apiErr.Code), struct {
				types.ActionResult
				Error *types.APIError `json:"error"`
			}{ActionResult: *out, Error: apiErr.WithRequestID(requestIDOf(r.Context()))})
			return
		}
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	png, apiErr := s.cfg.Manager.Screenshot(ctx, r.PathValue("id"), r.PathValue("tab"))
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", fmt.Sprint(len(png)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

func (s *Server) handlePersona(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.PersonaReport(ctx, r.PathValue("id"), r.PathValue("tab"))
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	var req types.HistoryRequest
	if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, false); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.History(ctx, r.PathValue("id"), r.PathValue("tab"), req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleWait(w http.ResponseWriter, r *http.Request) {
	var req types.WaitRequest
	if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, false); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 65*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.WaitFor(ctx, r.PathValue("id"), r.PathValue("tab"), req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleExtract(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var req types.ExtractRequest
	if v := q.Get("maxChars"); v != "" {
		n, err := parseInt(v)
		if err != nil || n < 0 {
			writeError(w, (&types.APIError{Code: types.CodeInvalidRequest, Message: "maxChars must be a non-negative integer"}).WithRequestID(requestIDOf(r.Context())))
			return
		}
		req.MaxChars = n
	}
	if v := q.Get("maxLinks"); v != "" {
		n, err := parseInt(v)
		if err != nil || n < 0 {
			writeError(w, (&types.APIError{Code: types.CodeInvalidRequest, Message: "maxLinks must be a non-negative integer"}).WithRequestID(requestIDOf(r.Context())))
			return
		}
		req.MaxLinks = n
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.Extract(ctx, r.PathValue("id"), r.PathValue("tab"), req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	out, apiErr := s.cfg.Manager.Telemetry(r.Context(), r.PathValue("id"), r.PathValue("tab"))
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetCookies(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.GetCookies(ctx, r.PathValue("id"), r.PathValue("tab"))
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSetCookies(w http.ResponseWriter, r *http.Request) {
	var req types.SetCookiesRequest
	if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, false); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.SetCookies(ctx, r.PathValue("id"), r.PathValue("tab"), req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleClearCookies(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if apiErr := s.cfg.Manager.ClearCookies(ctx, r.PathValue("id"), r.PathValue("tab")); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetCompanion(w http.ResponseWriter, r *http.Request) {
	out, apiErr := s.cfg.Manager.Companion(r.Context(), types.CompanionRequest{Action: "status"})
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePostCompanion(w http.ResponseWriter, r *http.Request) {
	var req types.CompanionRequest
	if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, true); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.Companion(ctx, req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCompanionSelection(w http.ResponseWriter, r *http.Request) {
	var req types.SelectionRequest
	if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, false); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.CompanionSelection(ctx, req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleEvaluate(w http.ResponseWriter, r *http.Request) {
	var req types.EvaluateRequest
	if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, false); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.Evaluate(ctx, r.PathValue("id"), r.PathValue("tab"), req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePDF(w http.ResponseWriter, r *http.Request) {
	var req types.PDFRequest
	if r.Method == http.MethodPost {
		if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, true); apiErr != nil {
			writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	pdf, apiErr := s.cfg.Manager.PDF(ctx, r.PathValue("id"), r.PathValue("tab"), req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Length", fmt.Sprint(len(pdf)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

func (s *Server) handleGetStorage(w http.ResponseWriter, r *http.Request) {
	area := r.URL.Query().Get("area")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.Storage(ctx, r.PathValue("id"), r.PathValue("tab"), types.StorageRequest{
		Area:   area,
		Action: "get",
	})
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePostStorage(w http.ResponseWriter, r *http.Request) {
	var req types.StorageRequest
	if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, false); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.Storage(ctx, r.PathValue("id"), r.PathValue("tab"), req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleClearStorage(w http.ResponseWriter, r *http.Request) {
	area := r.URL.Query().Get("area")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, apiErr := s.cfg.Manager.Storage(ctx, r.PathValue("id"), r.PathValue("tab"), types.StorageRequest{
		Area:   area,
		Action: "clear",
	}); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetDialogs(w http.ResponseWriter, r *http.Request) {
	out, apiErr := s.cfg.Manager.Dialogs(r.Context(), r.PathValue("id"), r.PathValue("tab"), nil)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePostDialogs(w http.ResponseWriter, r *http.Request) {
	var req types.DialogPolicyRequest
	if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, true); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	out, apiErr := s.cfg.Manager.Dialogs(r.Context(), r.PathValue("id"), r.PathValue("tab"), &req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleFind(w http.ResponseWriter, r *http.Request) {
	var req types.FindRequest
	if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, true); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.Find(ctx, r.PathValue("id"), r.PathValue("tab"), req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleEmulate(w http.ResponseWriter, r *http.Request) {
	var req types.EmulateRequest
	if apiErr := decodeJSON(r, &req, s.cfg.MaxBodyBytes, false); apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out, apiErr := s.cfg.Manager.Emulate(ctx, r.PathValue("id"), r.PathValue("tab"), req)
	if apiErr != nil {
		writeError(w, apiErr.WithRequestID(requestIDOf(r.Context())))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// decodeJSON reads one JSON value with strict semantics.
func decodeJSON(r *http.Request, dst any, maxBytes int64, allowEmpty bool) *types.APIError {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		base := ct
		if i := strings.IndexByte(base, ';'); i >= 0 {
			base = base[:i]
		}
		if strings.TrimSpace(strings.ToLower(base)) != "application/json" {
			return &types.APIError{
				Code:    types.CodeInvalidRequest,
				Message: "content type must be application/json",
				Detail:  map[string]any{"contentType": base},
			}
		}
	}
	body := http.MaxBytesReader(nil, r.Body, maxBytes)
	data, err := io.ReadAll(body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "request body is too large", Detail: map[string]any{"limit": maxBytes}}
		}
		return &types.APIError{Code: types.CodeInvalidRequest, Message: "could not read the request body"}
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		if allowEmpty {
			return nil
		}
		return &types.APIError{Code: types.CodeInvalidRequest, Message: "a JSON body is required"}
	}
	if apiErr := rejectDuplicateKeys(data); apiErr != nil {
		return apiErr
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return &types.APIError{
				Code:    types.CodeInvalidRequest,
				Message: fmt.Sprintf("field %q has the wrong type", typeErr.Field),
				Detail:  map[string]any{"field": typeErr.Field, "want": typeErr.Type.String()},
			}
		}
		return &types.APIError{Code: types.CodeInvalidRequest, Message: cleanJSONError(err)}
	}
	// Trailing data means the client sent something we did not consume.
	if dec.More() {
		return &types.APIError{Code: types.CodeInvalidRequest, Message: "the request body must contain exactly one JSON value"}
	}
	return nil
}

// rejectDuplicateKeys walks the raw token stream. encoding/json silently keeps
// the last duplicate, which lets a request smuggle a second value past a
// reviewer's reading of it.
func rejectDuplicateKeys(data []byte) *types.APIError {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	type frame struct {
		isObject  bool
		seen      map[string]bool
		keys      []string
		expectKey bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return &types.APIError{Code: types.CodeInvalidRequest, Message: "malformed JSON: " + cleanJSONError(err)}
		}
		top := len(stack) - 1
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				f := &frame{isObject: true, seen: map[string]bool{}, expectKey: true}
				stack = append(stack, f)
			case '[':
				stack = append(stack, &frame{})
			case '}', ']':
				if top >= 0 {
					stack = stack[:top]
				}
				if top-1 >= 0 {
					stack[top-1].expectKey = stack[top-1].isObject
				}
			}
			continue
		}
		if top >= 0 && stack[top].isObject && stack[top].expectKey {
			key, ok := tok.(string)
			if !ok {
				return &types.APIError{Code: types.CodeInvalidRequest, Message: "malformed JSON object key"}
			}
			if stack[top].seen[key] {
				return &types.APIError{
					Code:    types.CodeInvalidRequest,
					Message: "duplicate JSON key",
					Detail:  map[string]any{"key": key, "path": strings.Join(stack[top].keys, ".")},
				}
			}
			stack[top].seen[key] = true
			stack[top].keys = append(stack[top].keys, key)
			stack[top].expectKey = false
			continue
		}
		if top >= 0 && stack[top].isObject {
			stack[top].expectKey = true
		}
	}
}

func cleanJSONError(err error) string {
	msg := err.Error()
	// Keep the position information, drop Go's internal type names.
	msg = strings.ReplaceAll(msg, "json: ", "")
	if i := strings.Index(msg, "; error found in"); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

func snapshotQuery(r *http.Request) (types.SnapshotRequest, *types.APIError) {
	q := r.URL.Query()
	var req types.SnapshotRequest
	for key := range q {
		switch key {
		case "maxChars", "maxRefs", "includeValues", "scope":
		default:
			return req, &types.APIError{
				Code:    types.CodeInvalidRequest,
				Message: "unknown query parameter",
				Detail:  map[string]any{"parameter": key, "supported": []string{"maxChars", "maxRefs", "includeValues", "scope"}},
			}
		}
	}
	if v := q.Get("maxChars"); v != "" {
		n, err := parseInt(v)
		if err != nil || n < 0 {
			return req, &types.APIError{Code: types.CodeInvalidRequest, Message: "maxChars must be a non-negative integer", Detail: map[string]any{"field": "maxChars"}}
		}
		req.MaxChars = n
	}
	if v := q.Get("maxRefs"); v != "" {
		n, err := parseInt(v)
		if err != nil || n < 0 {
			return req, &types.APIError{Code: types.CodeInvalidRequest, Message: "maxRefs must be a non-negative integer", Detail: map[string]any{"field": "maxRefs"}}
		}
		req.MaxRefs = n
	}
	if v := q.Get("includeValues"); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes":
			req.IncludeValues = true
		case "0", "false", "no":
			req.IncludeValues = false
		default:
			return req, &types.APIError{Code: types.CodeInvalidRequest, Message: "includeValues must be a boolean", Detail: map[string]any{"field": "includeValues"}}
		}
	}
	req.Scope = q.Get("scope")
	return req, nil
}

func parseInt(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(r-'0')
		if n > 1<<30 {
			return 0, errors.New("too large")
		}
	}
	return n, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		writeError(w, &types.APIError{Code: types.CodeInternal, Message: "could not encode the response"})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

func writeError(w http.ResponseWriter, e *types.APIError) {
	if e == nil {
		e = &types.APIError{Code: types.CodeInternal, Message: "internal error"}
	}
	writeJSON(w, types.Status(e.Code), e)
}
