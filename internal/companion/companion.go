// Package companion manages the "Ni-Cursor" 2nd companion cursor across both
// the native Wayland/Hyprland layer-shell desktop overlay and in-browser tabs.
package companion

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/types"
)

//go:embed wayland_overlay.c
var waylandOverlayC []byte

var validMoods = map[string]bool{
	"friendly": true,
	"curious":  true,
	"warn":     true,
	"sad":      true,
	"excited":  true,
	"thinking": true,
}

// Controller owns the companion cursor state and optional Wayland layer-shell
// child process.
type Controller struct {
	mu          sync.Mutex
	state       types.CompanionState
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	disableExec bool
}

// New creates a companion cursor controller.
func New() *Controller {
	c := &Controller{
		state: types.CompanionState{
			Visible:        false,
			WaylandDisplay: os.Getenv("WAYLAND_DISPLAY"),
			HyprlandActive: os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "",
			ScreenWidth:    1920,
			ScreenHeight:   1080,
			X:              640,
			Y:              360,
			Mood:           "friendly",
			Pulse:          true,
			FollowActions:  true,
			UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
		},
	}
	c.probeHyprlandMonitor()
	return c
}

// SetDisableExec disables spawning the native Wayland helper (used in unit tests).
func (c *Controller) SetDisableExec(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disableExec = v
}

func (c *Controller) probeHyprlandMonitor() {
	if !c.state.HyprlandActive {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "hyprctl", "monitors", "-j").Output()
	if err != nil {
		return
	}
	var mons []struct {
		Name    string `json:"name"`
		Width   int    `json:"width"`
		Height  int    `json:"height"`
		Focused bool   `json:"focused"`
	}
	if json.Unmarshal(out, &mons) != nil || len(mons) == 0 {
		return
	}
	chosen := mons[0]
	for _, m := range mons {
		if m.Focused {
			chosen = m
			break
		}
	}
	c.state.Monitor = chosen.Name
	if chosen.Width > 0 && chosen.Height > 0 {
		c.state.ScreenWidth = chosen.Width
		c.state.ScreenHeight = chosen.Height
	}
}

// State returns a snapshot of the current companion cursor state.
func (c *Controller) State() types.CompanionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Apply updates the companion cursor state and synchronises the native Wayland
// layer-shell overlay if running on a Wayland session.
func (c *Controller) Apply(req types.CompanionRequest) (types.CompanionState, *types.APIError) {
	c.mu.Lock()
	defer c.mu.Unlock()

	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "guide"
	}
	switch action {
	case "status":
		return c.state, nil
	case "hide":
		c.state.Visible = false
	case "show":
		c.state.Visible = true
	case "clear":
		c.state.Message = ""
		c.state.Subtext = ""
		c.state.TargetRef = ""
	case "guide", "move", "say", "point_ref":
		if req.Visible == nil {
			c.state.Visible = true
		}
	default:
		return c.state, &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "action must be guide, move, say, point_ref, show, hide, clear, or status",
			Detail:  map[string]any{"field": "action"},
		}
	}

	if req.Visible != nil {
		c.state.Visible = *req.Visible
	}
	if req.Pulse != nil {
		c.state.Pulse = *req.Pulse
	}
	if req.FollowActions != nil {
		c.state.FollowActions = *req.FollowActions
	}
	if req.X != nil {
		c.state.X = clamp(*req.X, 4, float64(c.state.ScreenWidth-16))
	}
	if req.Y != nil {
		c.state.Y = clamp(*req.Y, 4, float64(c.state.ScreenHeight-16))
	}
	if req.Message != "" || action == "say" {
		c.state.Message = truncateStr(req.Message, 480)
	}
	if req.Subtext != "" {
		c.state.Subtext = truncateStr(req.Subtext, 140)
	}
	if req.Ref != "" {
		c.state.TargetRef = req.Ref
		if c.state.Subtext == "" {
			c.state.Subtext = "@" + strings.TrimPrefix(req.Ref, "@")
		}
	}
	if req.Mood != "" {
		m := strings.ToLower(strings.TrimSpace(req.Mood))
		if !validMoods[m] {
			return c.state, &types.APIError{
				Code:    types.CodeInvalidRequest,
				Message: "mood must be friendly, curious, warn, sad, excited, or thinking",
				Detail:  map[string]any{"field": "mood", "supported": []string{"friendly", "curious", "warn", "sad", "excited", "thinking"}},
			}
		}
		c.state.Mood = m
	}
	c.state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	c.syncWaylandLocked()
	return c.state, nil
}

// NotifyAction updates the companion cursor automatically when FollowActions is
// enabled and the companion cursor is visible.
func (c *Controller) NotifyAction(x, y float64, ref, kind, detail string) (types.CompanionState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.Visible || !c.state.FollowActions {
		return c.state, false
	}
	if x > 0 && y > 0 {
		c.state.X = clamp(x, 4, float64(c.state.ScreenWidth-16))
		c.state.Y = clamp(y, 4, float64(c.state.ScreenHeight-16))
	}
	if ref != "" {
		c.state.TargetRef = ref
		c.state.Subtext = fmt.Sprintf("%s • @%s", kind, strings.TrimPrefix(ref, "@"))
	} else {
		c.state.Subtext = kind
	}
	if detail != "" {
		c.state.Message = truncateStr(detail, 240)
	}
	c.state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	c.syncWaylandLocked()
	return c.state, true
}

func (c *Controller) syncWaylandLocked() {
	if c.disableExec || c.state.WaylandDisplay == "" {
		return
	}
	if c.state.Visible && c.stdin == nil {
		c.startWaylandHelperLocked()
	}
	if c.stdin == nil {
		return
	}
	wire := map[string]any{
		"visible": c.state.Visible,
		"pulse":   c.state.Pulse,
		"x":       c.state.X,
		"y":       c.state.Y,
		"message": c.state.Message,
		"subtext": c.state.Subtext,
		"mood":    c.state.Mood,
	}
	line, err := json.Marshal(wire)
	if err != nil {
		return
	}
	line = append(line, '\n')
	if _, err := c.stdin.Write(line); err != nil {
		_ = c.stdin.Close()
		c.stdin = nil
		c.cmd = nil
		c.state.WaylandActive = false
	} else {
		c.state.WaylandActive = true
	}
}

func (c *Controller) startWaylandHelperLocked() {
	bin, err := EnsureWaylandBinary()
	if err != nil {
		return
	}
	cmd := exec.Command(bin)
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return
	}
	c.cmd = cmd
	c.stdin = stdin
	c.state.WaylandActive = true
	go func(proc *exec.Cmd) {
		_ = proc.Wait()
		c.mu.Lock()
		if c.cmd == proc {
			c.cmd = nil
			c.stdin = nil
			c.state.WaylandActive = false
		}
		c.mu.Unlock()
	}(cmd)
}

// Close shuts down the Wayland overlay process if one is running.
func (c *Controller) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdin != nil {
		_, _ = c.stdin.Write([]byte(`{"quit":true}` + "\n"))
		_ = c.stdin.Close()
		c.stdin = nil
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		c.cmd = nil
	}
	c.state.WaylandActive = false
}

// EnsureWaylandBinary returns the path to the compiled Wayland layer-shell
// overlay binary, building it on the fly into /tmp/xt-nichrome-build if needed.
func EnsureWaylandBinary() (string, error) {
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "nichrome-cursor-wayland")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	dir := filepath.Join(os.TempDir(), "xt-nichrome-build")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	binPath := filepath.Join(dir, "nichrome-cursor-wayland")
	if st, err := os.Stat(binPath); err == nil && !st.IsDir() {
		return binPath, nil
	}
	srcPath := filepath.Join(dir, "wayland_overlay.c")
	if err := os.WriteFile(srcPath, waylandOverlayC, 0o644); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	flagsOut, err := exec.CommandContext(ctx, "pkg-config", "--cflags", "--libs", "gtk+-3.0", "gtk-layer-shell-0", "cairo", "pangocairo").Output()
	if err != nil {
		return "", fmt.Errorf("pkg-config gtk-layer-shell: %w", err)
	}
	args := []string{"-O2", srcPath, "-o", binPath}
	args = append(args, strings.Fields(string(flagsOut))...)
	args = append(args, "-lm")
	if out, err := exec.CommandContext(ctx, "gcc", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("compile wayland overlay: %v (%s)", err, string(out))
	}
	return binPath, nil
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func truncateStr(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
