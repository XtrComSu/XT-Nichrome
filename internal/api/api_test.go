package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xtrcom/xt-nichrome/internal/types"
)

func TestStrictJSONDecodingAndDuplicateKeyRejection(t *testing.T) {
	// 1. Valid JSON object decodes cleanly.
	r1 := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"mode":"ephemeral"}`))
	r1.Header.Set("Content-Type", "application/json")
	var req types.CreateSessionRequest
	if apiErr := decodeJSON(r1, &req, MaxBodyBytes, false); apiErr != nil {
		t.Fatalf("decodeJSON valid: %v", apiErr)
	}
	if req.Mode != "ephemeral" {
		t.Fatalf("got mode=%q, want ephemeral", req.Mode)
	}

	// 2. Duplicate JSON keys must be rejected.
	rDup := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"mode":"ephemeral","mode":"persistent"}`))
	rDup.Header.Set("Content-Type", "application/json")
	if apiErr := decodeJSON(rDup, &req, MaxBodyBytes, false); apiErr == nil || apiErr.Code != types.CodeInvalidRequest {
		t.Fatalf("expected duplicate JSON key rejection, got %v", apiErr)
	}

	// 3. Unknown JSON fields must be rejected.
	rUnk := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"mode":"ephemeral","bogus":123}`))
	rUnk.Header.Set("Content-Type", "application/json")
	if apiErr := decodeJSON(rUnk, &req, MaxBodyBytes, false); apiErr == nil || apiErr.Code != types.CodeInvalidRequest {
		t.Fatalf("expected unknown field rejection, got %v", apiErr)
	}

	// 4. Wrong Content-Type must be rejected.
	rCT := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"mode":"ephemeral"}`))
	rCT.Header.Set("Content-Type", "text/plain")
	if apiErr := decodeJSON(rCT, &req, MaxBodyBytes, false); apiErr == nil || apiErr.Code != types.CodeInvalidRequest {
		t.Fatalf("expected wrong content-type rejection, got %v", apiErr)
	}
}

func TestRequestIDAndSnapshotQueryValidation(t *testing.T) {
	if !validRequestID("req-123_ABC") || validRequestID("") || validRequestID("bad space") {
		t.Fatal("validRequestID validation failed")
	}

	reqOK := httptest.NewRequest(http.MethodGet, "/v1/sessions/s1/tabs/t1/snapshot?maxChars=4000&maxRefs=50&includeValues=true", nil)
	sq, apiErr := snapshotQuery(reqOK)
	if apiErr != nil || sq.MaxChars != 4000 || sq.MaxRefs != 50 || !sq.IncludeValues {
		t.Fatalf("snapshotQuery valid failed: err=%v sq=%+v", apiErr, sq)
	}

	reqBad := httptest.NewRequest(http.MethodGet, "/v1/sessions/s1/tabs/t1/snapshot?unknownParam=1", nil)
	if _, apiErr := snapshotQuery(reqBad); apiErr == nil || apiErr.Code != types.CodeInvalidRequest {
		t.Fatalf("expected unknown query parameter rejection, got %v", apiErr)
	}
}
