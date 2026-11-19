package profiles

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestValidateName(t *testing.T) {
	valid := []string{"a", "agent-a", "agent_a", "a1", "0abc", "x" + string(make([]byte, 0))}
	valid = append(valid, string(make([]byte, 0))+"a234567890123456789012345678901234567890123456789012345678901234")
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) rejected a valid name: %v", name, err)
		}
	}
	invalid := []string{
		"", "..", "../x", "a/b", "/abs", "A", "with space",
		"a.b", "-lead", "_lead", "trail-",
		"a2345678901234567890123456789012345678901234567890123456789012345", // 65 chars
	}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) accepted an invalid name", name)
		}
	}
}

func TestAcquireCreatesAndLocks(t *testing.T) {
	root := t.TempDir()
	m, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dir, err := m.Acquire("agent-a")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("profile dir was not created: %v", err)
	}
	if perm := mustStat(t, dir).Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("profile dir mode is %o", perm)
	}
	// A second acquire of the same name in this process must fail rather than
	// hand two browsers one profile.
	if _, err := m.Acquire("agent-a"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Acquire returned %v, want ErrBusy", err)
	}
	m.Release("agent-a")
	if dir2, err := m.Acquire("agent-a"); err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	} else if dir2 != dir {
		t.Errorf("Acquire returned %q, want %q", dir2, dir)
	}
	m.ReleaseAll()
}

func TestAcquireIsRaceFree(t *testing.T) {
	root := t.TempDir()
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Acquire("contended"); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d goroutines acquired the same profile, want exactly 1", wins)
	}
	m.ReleaseAll()
}

// TestSymlinkEscapeRefused is the security property: a symlink planted inside
// the root must not redirect a profile outside it.
func TestSymlinkEscapeRefused(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := m.Acquire("escape"); !errors.Is(err, ErrEscape) {
		t.Fatalf("Acquire through a symlink returned %v, want ErrEscape", err)
	}
	// Reset must refuse it too, not follow the link and delete the victim.
	if err := m.Reset("escape"); !errors.Is(err, ErrEscape) {
		t.Fatalf("Reset through a symlink returned %v, want ErrEscape", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("the symlink target was damaged: %v", err)
	}
}

func TestResetRefusesOpenProfile(t *testing.T) {
	root := t.TempDir()
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire("open"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reset("open"); !errors.Is(err, ErrBusy) {
		t.Fatalf("Reset on an open profile returned %v, want ErrBusy", err)
	}
	m.Release("open")
	if err := m.Reset("open"); err != nil {
		t.Fatalf("Reset after Release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "open")); !os.IsNotExist(err) {
		t.Error("Reset left the directory behind")
	}
}

func TestListSkipsInvalidNames(t *testing.T) {
	root := t.TempDir()
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire("good"); err != nil {
		t.Fatal(err)
	}
	m.Release("good")
	// Something a human dropped in by hand.
	if err := os.MkdirAll(filepath.Join(root, "Bad Name"), 0o700); err != nil {
		t.Fatal(err)
	}
	list, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0] != "good" {
		t.Fatalf("List returned %v", list)
	}
}

func mustStat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	return st
}
