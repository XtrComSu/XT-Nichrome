package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentTokenCreation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "token")
	const workers = 64

	var wg sync.WaitGroup
	start := make(chan struct{})
	tokens := make(chan string, workers)
	for range workers {
		wg.Go(func() {
			<-start
			token, err := loadOrCreateToken(dir, path, "")
			if err != nil {
				t.Errorf("loadOrCreateToken: %v", err)
				return
			}
			tokens <- token
		})
	}
	close(start)
	wg.Wait()
	close(tokens)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	persisted := strings.TrimSpace(string(data))
	for token := range tokens {
		if token != persisted {
			t.Fatal("a caller received a token different from the persisted token")
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("token permissions = %04o, want 0600", mode)
	}
	if files, err := os.ReadDir(filepath.Dir(path)); err != nil || len(files) != 1 {
		t.Fatalf("expected only the published token file, found %v (%v)", files, err)
	}
}

func TestMalformedTokenIsNotReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	const original = "too-short\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateToken(dir, "", ""); err == nil {
		t.Fatal("accepted malformed token file")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != original {
		t.Fatal("malformed token file was modified")
	}
}
