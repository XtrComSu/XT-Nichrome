// Package types holds the wire contract shared by the HTTP API, the CLI and the
// application services. Nothing here performs I/O.
package types

import (
	"errors"
	"fmt"
	"net/http"
)

// Session modes.
const (
	ModeEphemeral  = "ephemeral"
	ModePersistent = "persistent"
)

// Session lifecycle states (brief §5).
const (
	StateStarting = "starting"
	StateReady    = "ready"
	StateClosing  = "closing"
	StateClosed   = "closed"
	StateFailed   = "failed"

	// Action result status.
	StatusOK      = "ok"
	StatusUnknown = "unknown"
	// Action outcomes for side-effecting operations.
	OutcomeCompleted = "completed"
	OutcomeUnknown   = "unknown"
)

// Error codes. Every non-2xx body carries one of these.
const (
	CodeInvalidRequest       = "INVALID_REQUEST"
	CodeUnauthorized         = "UNAUTHORIZED"
	CodeNotFound             = "NOT_FOUND"
	CodeStaleRef             = "STALE_REF"
	CodeAmbiguousRef         = "AMBIGUOUS_REF"
	CodeElementNotActionable = "ELEMENT_NOT_ACTIONABLE"
	CodePolicyBlocked        = "POLICY_BLOCKED"
	CodeCapacity             = "CAPACITY"
	CodeBusyProfile          = "BUSY_PROFILE"
	CodeTimeout              = "TIMEOUT"
	CodeActionOutcomeUnknown = "ACTION_OUTCOME_UNKNOWN"
	CodeBrowserFailed        = "BROWSER_FAILED"
	CodeUnsupported          = "UNSUPPORTED"
	CodeInternal             = "INTERNAL"
)

// APIError is the single error shape returned by the daemon.
type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId,omitempty"`
	// Detail is optional machine-readable context (never page content).
	Detail map[string]any `json:"detail,omitempty"`
}

func (e *APIError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// WithRequestID returns a copy carrying the request id.
func (e *APIError) WithRequestID(id string) *APIError {
	cp := *e
	if cp.RequestID == "" {
		cp.RequestID = id
	}
	return &cp
}

// NewError builds an APIError.
func NewError(code, msg string) *APIError { return &APIError{Code: code, Message: msg} }

// Status maps an error code to an HTTP status (brief §3).
func Status(code string) int {
	switch code {
	case CodeInvalidRequest:
		return http.StatusBadRequest
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeNotFound:
		return http.StatusNotFound
	// STALE_REF is a conflict, not a miss: the referenced snapshot existed, it is
	// simply no longer current. Returning 404 would make an agent treat a
	// superseded handle as "the page is gone" and stop instead of re-snapshotting.
	case CodeStaleRef, CodeAmbiguousRef, CodeBusyProfile, CodeElementNotActionable:
		return http.StatusConflict
	case CodePolicyBlocked:
		return http.StatusForbidden
	case CodeCapacity:
		return http.StatusTooManyRequests
	case CodeTimeout:
		return http.StatusGatewayTimeout
	case CodeActionOutcomeUnknown:
		return http.StatusConflict
	case CodeBrowserFailed:
		return http.StatusServiceUnavailable
	case CodeUnsupported:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// AsAPIError converts any error into the wire shape.
func AsAPIError(err error) *APIError {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae
	}
	return &APIError{Code: CodeInternal, Message: err.Error()}
}

// CreateSessionRequest is POST /v1/sessions.
type CreateSessionRequest struct {
	Mode      string  `json:"mode"`
	ProfileID *string `json:"profileId,omitempty"`
	// Persona optionally selects a stealth persona by name; defaults to the
	// daemon's configured persona.
	Persona string `json:"persona,omitempty"`
}

// Session is the create/describe response.
type Session struct {
	ID        string `json:"sessionId"`
	Mode      string `json:"mode"`
	ProfileID string `json:"profileId,omitempty"`
	State     string `json:"state"`
	Persona   string `json:"persona,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// CreateTabRequest is POST /v1/sessions/:id/tabs.
type CreateTabRequest struct {
	URL string `json:"url,omitempty"`
}

// Tab is the create response.
type Tab struct {
	TabID      string `json:"tabId"`
	State      string `json:"state"`
	CurrentURL string `json:"currentUrl,omitempty"`
}

// TabInfo is the list entry.
type TabInfo struct {
	TabID      string `json:"tabId"`
	CurrentURL string `json:"currentUrl,omitempty"`
	Title      string `json:"title,omitempty"`
	Generation int64  `json:"generation"`
}

// NavigateRequest is POST .../navigate.
type NavigateRequest struct {
	URL string `json:"url"`
	// WaitUntil selects the navigation barrier: "load" (default) or "commit".
	WaitUntil string `json:"waitUntil,omitempty"`
	TimeoutMS int    `json:"timeoutMs,omitempty"`
}

// NavigateResult is the navigate response.
type NavigateResult struct {
	URL        string `json:"url"`
	Generation int64  `json:"generation"`
	Title      string `json:"title,omitempty"`
}

// SnapshotRequest is the query for GET .../snapshot.
type SnapshotRequest struct {
	MaxChars int `json:"maxChars,omitempty"`
	MaxRefs  int `json:"maxRefs,omitempty"`
	// IncludeValues opts in to emitting live field values (never passwords).
	IncludeValues bool `json:"includeValues,omitempty"`
	// Scope is "page" (default). Frames beyond the top-level document are
	// reported as unsupported.
	Scope string `json:"scope,omitempty"`
}

// SnapshotResult is the snapshot response (brief §4).
type SnapshotResult struct {
	Generation int64  `json:"generation"`
	SnapshotID string `json:"snapshotId"`
	URL        string `json:"url"`
	Title      string `json:"title,omitempty"`
	Text       string `json:"text"`
	Truncated  bool   `json:"truncated"`
	RefCount   int    `json:"refCount"`
	// UnsupportedFrames lists frame scopes that were not snapshotted.
	UnsupportedFrames []string `json:"unsupportedFrames,omitempty"`
	// Elements intentionally omitted for budget reasons.
	OmittedUnnamed int `json:"omittedUnnamed,omitempty"`
}

// Action kinds.
const (
	ActionClick = "click"
	ActionFill  = "fill"
)

// ActionRequest is POST .../actions. Kind is the discriminator; fields for the
// other kind must be absent (strict decoding rejects unknown fields).
type ActionRequest struct {
	Kind       string `json:"kind"`
	SnapshotID string `json:"snapshotId"`
	// Ref is the opaque reference from the snapshot (e.g. "e12").
	Ref string `json:"ref"`
	// Generation is optional; when set it must match the live document
	// generation, otherwise the action is rejected as stale.
	Generation int64 `json:"generation,omitempty"`
	// Fill-only.
	Text string `json:"text,omitempty"`
	// Click-only.
	Button     string `json:"button,omitempty"` // left|right|middle
	ClickCount int    `json:"clickCount,omitempty"`
	// Deadline for the action in milliseconds.
	TimeoutMS int `json:"timeoutMs,omitempty"`
	// ClearExisting selects select-all-then-type for fill (default true).
	ClearExisting *bool `json:"clearExisting,omitempty"`
}

// PersonaReport is the observable identity of a tab, used by the privacy
// inventory endpoint and the startup self-check.
type PersonaReport struct {
	Persona             string    `json:"persona"`
	Consistent          bool      `json:"consistent"`
	Mismatches          []string  `json:"mismatches,omitempty"`
	UserAgent           string    `json:"userAgent"`
	Platform            string    `json:"platform"`
	Webdriver           bool      `json:"webdriver"`
	HardwareConcurrency int       `json:"hardwareConcurrency"`
	DeviceMemory        int       `json:"deviceMemory"`
	Screen              []int     `json:"screen"`
	Window              []float64 `json:"window"`
	UnmaskedRenderer    string    `json:"unmaskedRenderer"`
	Timezone            string    `json:"timezone"`
	PointerFine         bool      `json:"pointerFine"`
	ChromeObject        bool      `json:"chromeObject"`
	Plugins             int       `json:"plugins"`
}

// ActionResult is the action response.
type ActionResult struct {
	Status string `json:"status"`
	// FreshSnapshotRequired tells the agent its refs are no longer usable.
	FreshSnapshotRequired bool `json:"freshSnapshotRequired"`
	// Generation is the document generation at completion.
	Generation int64 `json:"generation"`
	// Outcome for side-effecting actions: "completed" | "unknown".
	Outcome string `json:"outcome"`
	URL     string `json:"url,omitempty"`
}

// Health responses.
type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// Ready describes readiness and current capacity.
type Ready struct {
	Ready    bool       `json:"ready"`
	Browser  string     `json:"browserRevision,omitempty"`
	Persona  string     `json:"persona,omitempty"`
	Sessions int        `json:"sessions"`
	Tabs     int        `json:"tabs"`
	Capacity Capacity   `json:"capacity"`
	Egress   EgressInfo `json:"egress"`
}

// Capacity reports configured limits.
type Capacity struct {
	EphemeralContexts  int `json:"ephemeralContexts"`
	PersistentBrowsers int `json:"persistentBrowsers"`
	TabsPerSession     int `json:"tabsPerSession"`
	QueuedOpsPerTab    int `json:"queuedOpsPerTab"`
}

// EgressInfo reports the policy boundary state.
type EgressInfo struct {
	Mode           string `json:"mode"` // "proxy" | "off"
	ProxyAddr      string `json:"proxyAddr,omitempty"`
	AllowedOrigins int    `json:"allowedOrigins"`
	GuardActive    bool   `json:"guardActive"`
	Denials        int64  `json:"denials"`
}
