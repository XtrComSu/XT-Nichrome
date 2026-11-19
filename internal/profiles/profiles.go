// Package profiles owns persistent browser profiles on disk.
//
// Two guarantees:
//
//  1. One browser per profile. Chromium corrupts a profile that two processes
//     write at once, so a profile is guarded by an advisory lock for the whole
//     life of the session that owns it.
//  2. A profile name cannot escape its root. Names are validated against a
//     conservative pattern, then the resolved path is re-checked after symlink
//     evaluation, because a symlink planted inside the root would otherwise
//     redirect writes anywhere on the filesystem.
package profiles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// namePattern is deliberately narrow: lowercase alphanumerics, dash, underscore,
// starting and ending alphanumeric. A leading or trailing separator is refused
// because it is almost always a truncation or quoting bug in the caller, and a
// profile directory is a poor place to discover one.
var namePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]{0,62}[a-z0-9])?$`)

// Errors returned by the store.
var (
	ErrInvalidName = errors.New("profiles: invalid profile name")
	ErrBusy        = errors.New("profiles: profile is in use")
	ErrEscape      = errors.New("profiles: profile path escapes the profile root")
)

// Manager hands out locked profile directories.
type Manager struct {
	root string

	mu    sync.Mutex
	locks map[string]*os.File
}

// New creates a manager rooted at dir, creating it if needed.
func New(root string) (*Manager, error) {
	if root == "" {
		return nil, fmt.Errorf("profiles: root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("profiles: create root: %w", err)
	}
	// Resolve the root once so every later comparison is against the real path.
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("profiles: resolve root: %w", err)
	}
	return &Manager{root: real, locks: map[string]*os.File{}}, nil
}

// Root returns the resolved profile root.
func (m *Manager) Root() string { return m.root }

// ValidateName reports whether a name is usable.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%w: %q must match %s", ErrInvalidName, name, namePattern.String())
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return nil
}

// Acquire locks a profile and returns its directory.
//
// The lock is an exclusive flock on a sidecar file, taken before the directory
// is created for the browser. flock is released automatically if the process
// dies, which is exactly the failure mode a stale lockfile cannot handle.
func (m *Manager) Acquire(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	dir := filepath.Join(m.root, name)

	m.mu.Lock()
	if _, held := m.locks[name]; held {
		m.mu.Unlock()
		return "", fmt.Errorf("%w: %s is already open in this process", ErrBusy, name)
	}
	m.mu.Unlock()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("profiles: create %s: %w", name, err)
	}
	// Refuse a directory that resolves outside the root (symlink planted by an
	// earlier session or by the page via a download path).
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("profiles: resolve %s: %w", name, err)
	}
	if !within(m.root, realDir) {
		return "", fmt.Errorf("%w: %s resolves to %s", ErrEscape, name, realDir)
	}

	lockPath := filepath.Join(realDir, ".xt-nichrome.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", fmt.Errorf("profiles: open lock for %s: %w", name, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return "", fmt.Errorf("%w: %s is locked by another process", ErrBusy, name)
		}
		return "", fmt.Errorf("profiles: lock %s: %w", name, err)
	}
	// Record the holder for diagnostics; the lock, not the content, is
	// authoritative.
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("pid=%d\nsince=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))), 0)

	m.mu.Lock()
	m.locks[name] = f
	m.mu.Unlock()
	return realDir, nil
}

// Release unlocks a profile.
func (m *Manager) Release(name string) {
	m.mu.Lock()
	f, ok := m.locks[name]
	delete(m.locks, name)
	m.mu.Unlock()
	if !ok {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

// ReleaseAll unlocks everything (daemon shutdown).
func (m *Manager) ReleaseAll() {
	m.mu.Lock()
	held := m.locks
	m.locks = map[string]*os.File{}
	m.mu.Unlock()
	for _, f := range held {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}

// List returns the profile directories present under the root.
func (m *Manager) List() ([]string, error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if ValidateName(name) != nil {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

// Reset deletes a profile's contents. The lock must not be held.
func (m *Manager) Reset(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	m.mu.Lock()
	_, held := m.locks[name]
	m.mu.Unlock()
	if held {
		return fmt.Errorf("%w: %s is open", ErrBusy, name)
	}
	dir := filepath.Join(m.root, name)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !within(m.root, realDir) {
		return fmt.Errorf("%w: %s resolves to %s", ErrEscape, name, realDir)
	}
	if realDir == m.root {
		return fmt.Errorf("%w: refusing to remove the profile root", ErrInvalidName)
	}
	return os.RemoveAll(realDir)
}

// within reports whether p is inside root (or equal to it).
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
