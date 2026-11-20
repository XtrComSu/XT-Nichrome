package main

import (
	"errors"
	"flag"
	"os"
	"strings"
	"testing"
)

func TestPublicProbesWithoutToken(t *testing.T) {
	t.Setenv("XT_NICHROME_TOKEN", "")
	t.Setenv("XT_NICHROME_STATE", t.TempDir())

	for _, command := range []string{"health", "ready"} {
		t.Run(command, func(t *testing.T) {
			err := run(flag.NewFlagSet("nichrome-cli", flag.ContinueOnError), []string{"-url", "://bad-url", command})
			// An invalid URL lets us verify that the CLI reached request
			// construction without needing a listener or outbound network.
			if err == nil || !strings.Contains(err.Error(), "missing protocol scheme") {
				t.Fatalf("expected URL error, got %v", err)
			}
		})
	}
}

func TestProtectedCommandRequiresToken(t *testing.T) {
	t.Setenv("XT_NICHROME_TOKEN", "")
	t.Setenv("XT_NICHROME_STATE", t.TempDir())

	if err := run(flag.NewFlagSet("nichrome-cli", flag.ContinueOnError), []string{"sessions"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing token error, got %v", err)
	}
}
