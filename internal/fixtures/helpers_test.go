package fixtures

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"
)

// client returns an HTTP client that trusts the fixture's self-signed
// certificate (needed only for NewTLS) and keeps no state between calls.
func (s *Server) client() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			// The TLS fixture uses httptest's self-signed certificate.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 15 * time.Second,
	}
}

// tlsClient is an explicit alias for readability at TLS call sites.
func (s *Server) tlsClient() *http.Client { return s.client() }

// newJar returns a cookie jar so tests can prove cookie persistence across
// page loads the way a real browser would.
func newJar(t *testing.T) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return jar
}

// decodeJSON reads and decodes a response body into v.
func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode json: %v", err)
	}
}

// drainBody reads and closes a response body, returning it as a string.
func drainBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}
