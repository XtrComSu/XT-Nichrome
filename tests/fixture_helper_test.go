package tests

import (
	"regexp"
	"strings"
	"testing"

	"github.com/xtrcom/xt-nichrome/internal/fixtures"
)

// refPattern matches the reference sigil plus its label, e.g. "@e12".
var refPattern = regexp.MustCompile(`@e[0-9]+`)

// fixture wraps the deterministic test server.
type fixture struct {
	Server *fixtures.Server
	URL    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s := fixtures.New()
	t.Cleanup(s.Close)
	return &fixture{Server: s, URL: s.URL}
}

// findRef locates the reference for the first line carrying both role and name.
func findRef(t *testing.T, text, role, name string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(strings.ToLower(line), role) || !strings.Contains(line, name) {
			continue
		}
		if m := refPattern.FindString(line); m != "" {
			return m[1:] // drop the sigil, keep "e12"
		}
	}
	t.Fatalf("no ref for role %q name %q in snapshot:\n%s", role, name, text)
	return ""
}

type snapshotResult struct {
	Generation int64  `json:"generation"`
	SnapshotID string `json:"snapshotId"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	Truncated  bool   `json:"truncated"`
	RefCount   int    `json:"refCount"`
}

type actionResult struct {
	Status                string `json:"status"`
	Outcome               string `json:"outcome"`
	FreshSnapshotRequired bool   `json:"freshSnapshotRequired"`
	Generation            int64  `json:"generation"`
	URL                   string `json:"url"`
}

type apiError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail"`
}
