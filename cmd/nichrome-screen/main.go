// Command nichrome-screen captures the Wayland desktop when the user presses Alt.
// Images and context stay in a private local directory for the active agent to read.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type snapshot struct {
	Time        time.Time `json:"time"`
	Image       string    `json:"image"`
	CursorX     float64   `json:"cursorX"`
	CursorY     float64   `json:"cursorY"`
	CursorKnown bool      `json:"cursorKnown"`
	Selection   string    `json:"selection,omitempty"`
	WindowClass string    `json:"windowClass,omitempty"`
	WindowTitle string    `json:"windowTitle,omitempty"`
}

func capture(root string) (snapshot, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return snapshot{}, err
	}
	lock, err := os.OpenFile(filepath.Join(root, ".capture.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return snapshot{}, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return snapshot{}, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	image, err := os.CreateTemp(root, "screen-*.png")
	if err != nil {
		return snapshot{}, err
	}
	path := image.Name()
	if err := image.Close(); err != nil {
		return snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "grim", "-c", path).CombinedOutput(); err != nil {
		_ = os.Remove(path)
		return snapshot{}, fmt.Errorf("grim: %w: %s", err, strings.TrimSpace(string(out)))
	}
	s := snapshot{Time: time.Now().UTC(), Image: path}
	if out, err := run(500*time.Millisecond, "hyprctl", "cursorpos"); err == nil {
		parts := strings.Split(strings.TrimSpace(out), ",")
		if len(parts) == 2 {
			x, xErr := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
			y, yErr := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
			if xErr == nil && yErr == nil {
				s.CursorX, s.CursorY, s.CursorKnown = x, y, true
			}
		}
	}
	if out, err := run(500*time.Millisecond, "hyprctl", "activewindow", "-j"); err == nil {
		var window struct {
			Class string `json:"class"`
			Title string `json:"title"`
		}
		if json.Unmarshal([]byte(out), &window) == nil {
			s.WindowClass, s.WindowTitle = window.Class, window.Title
		}
	}
	if out, err := run(500*time.Millisecond, "wl-paste", "--primary", "--no-newline"); err == nil {
		s.Selection = strings.TrimSpace(out)
		if len(s.Selection) > 16384 {
			s.Selection = string([]rune(s.Selection)[:4096])
		}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return snapshot{}, err
	}
	meta, err := os.CreateTemp(root, "latest-*.json")
	if err != nil {
		return snapshot{}, err
	}
	defer os.Remove(meta.Name())
	if _, err = meta.Write(append(data, '\n')); err != nil {
		_ = meta.Close()
		return snapshot{}, err
	}
	if err = meta.Close(); err != nil {
		return snapshot{}, err
	}
	if err = os.Rename(meta.Name(), filepath.Join(root, "latest.json")); err != nil {
		return snapshot{}, err
	}
	if old, err := filepath.Glob(filepath.Join(root, "screen-*.png")); err == nil {
		for _, name := range old {
			if name != path {
				_ = os.Remove(name)
			}
		}
	}
	return s, nil
}

func run(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

func readLatest(root string) (snapshot, error) {
	data, err := os.ReadFile(filepath.Join(root, "latest.json"))
	if err != nil {
		return snapshot{}, err
	}
	var s snapshot
	err = json.Unmarshal(data, &s)
	return s, err
}

func main() {
	defaultRoot := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "xt-nichrome", "screen")
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		defaultRoot = filepath.Join(os.TempDir(), fmt.Sprintf("xt-nichrome-%d", os.Getuid()), "screen")
	}
	root := flag.String("dir", defaultRoot, "private screenshot directory")
	latest := flag.Bool("latest", false, "print the most recent capture without taking a screenshot")
	flag.Parse()
	var s snapshot
	var err error
	if *latest {
		s, err = readLatest(*root)
	} else {
		s, err = capture(*root)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nichrome-screen:", err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(s)
}
