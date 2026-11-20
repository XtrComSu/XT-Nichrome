package types

import (
	"errors"
	"net/http"
	"testing"
)

func TestStatusMappingsAndAPIError(t *testing.T) {
	cases := map[string]int{
		CodeInvalidRequest:       http.StatusBadRequest,
		CodeUnauthorized:         http.StatusUnauthorized,
		CodeNotFound:             http.StatusNotFound,
		CodeStaleRef:             http.StatusConflict,
		CodeAmbiguousRef:         http.StatusConflict,
		CodeBusyProfile:          http.StatusConflict,
		CodeElementNotActionable: http.StatusConflict,
		CodeActionOutcomeUnknown: http.StatusConflict,
		CodePolicyBlocked:        http.StatusForbidden,
		CodeCapacity:             http.StatusTooManyRequests,
		CodeTimeout:              http.StatusGatewayTimeout,
		CodeBrowserFailed:        http.StatusServiceUnavailable,
		CodeUnsupported:          http.StatusUnprocessableEntity,
		CodeInternal:             http.StatusInternalServerError,
		"UNKNOWN_CODE":           http.StatusInternalServerError,
	}
	for code, want := range cases {
		if got := Status(code); got != want {
			t.Errorf("Status(%q) = %d, want %d", code, got, want)
		}
	}

	var nilErr *APIError
	if nilErr.Error() != "<nil>" {
		t.Fatalf("nil APIError.Error() = %q, want <nil>", nilErr.Error())
	}

	e := NewError(CodeStaleRef, "snapshot expired").WithRequestID("req-123")
	if e.RequestID != "req-123" || e.Error() != "STALE_REF: snapshot expired" {
		t.Fatalf("unexpected APIError: %+v (%s)", e, e.Error())
	}
	// WithRequestID does not overwrite an existing RequestID.
	e2 := e.WithRequestID("req-456")
	if e2.RequestID != "req-123" {
		t.Fatalf("WithRequestID overwrote existing ID: %q", e2.RequestID)
	}

	if got := AsAPIError(e); got.Code != CodeStaleRef {
		t.Fatalf("AsAPIError(APIError) = %+v", got)
	}
	if got := AsAPIError(errors.New("plain failure")); got.Code != CodeInternal || got.Message != "plain failure" {
		t.Fatalf("AsAPIError(plain) = %+v", got)
	}
}
