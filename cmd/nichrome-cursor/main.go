// Command nichrome-cursor drives the native Wayland/Hyprland 2nd companion
// cursor ("Ni-Cursor") directly from the terminal, Hyprland keybinds, or a
// live local HTTP control endpoint (-listen 127.0.0.1:7798).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/companion"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

func main() {
	var (
		x              = flag.Float64("x", 680, "target screen X coordinate")
		y              = flag.Float64("y", 420, "target screen Y coordinate")
		say            = flag.String("say", "hey, you should click this to help me!", "chat bubble message")
		subtext        = flag.String("subtext", "Ni-28 assistant", "small guiding text near the cursor")
		mood           = flag.String("mood", "friendly", "companion mood: friendly|curious|warn|sad|excited|thinking")
		pulse          = flag.Bool("pulse", true, "render expanding crimson pulse ring at cursor tip")
		demo           = flag.Bool("demo", false, "run an interactive multi-step tour across the screen")
		listen         = flag.String("listen", "", "run as a live HTTP daemon (e.g. 127.0.0.1:7798) keeping the overlay open")
		followUser     = flag.Bool("follow-user", false, "gently shadow the user's Hyprland cursor and active window when not pinned")
		watchSelection = flag.Bool("watch-selection", false, "activate automatically when the user highlights/selects text on Wayland and reply in real time")
		autoHide       = flag.Duration("auto-hide", 6*time.Second, "auto-hide the overlay after selection reply when follow-user is false (0 disables)")
		duration       = flag.Duration("duration", 5*time.Second, "how long to keep the overlay visible (0 = until Ctrl+C)")
	)
	flag.Parse()

	ctrl := companion.New()
	defer ctrl.Close()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if *listen != "" {
		runDaemon(ctrl, *listen, *followUser, *watchSelection, *autoHide, *x, *y, *mood, *subtext, *say, *pulse, sigCh)
		return
	}

	if *demo {
		steps := []struct {
			x, y    float64
			mood    string
			subtext string
			msg     string
			wait    time.Duration
		}{
			{480, 300, "friendly", "Ni-28 • online", "hey! I'm your 2nd cursor on Hyprland (^‿^)", 2200 * time.Millisecond},
			{960, 440, "excited", "@e4 • primary action", "hey, you should click this to help me!", 2400 * time.Millisecond},
			{1280, 620, "sad", "@e9 • warning", "hey, this person isnt nice :(", 2400 * time.Millisecond},
			{740, 520, "curious", "inspecting • DOM", "let's check what's further down this page...", 2000 * time.Millisecond},
		}
		for _, s := range steps {
			tx, ty := s.x, s.y
			pls := true
			st, apiErr := ctrl.Apply(types.CompanionRequest{
				Action:  "guide",
				X:       &tx,
				Y:       &ty,
				Mood:    s.mood,
				Subtext: s.subtext,
				Message: s.msg,
				Pulse:   &pls,
			})
			if apiErr != nil {
				fmt.Fprintf(os.Stderr, "nichrome-cursor: %v\n", apiErr)
				os.Exit(1)
			}
			b, _ := json.Marshal(st)
			fmt.Println(string(b))
			select {
			case <-sigCh:
				return
			case <-time.After(s.wait):
			}
		}
		return
	}

	st, apiErr := ctrl.Apply(types.CompanionRequest{
		Action:  "guide",
		X:       x,
		Y:       y,
		Mood:    *mood,
		Subtext: *subtext,
		Message: *say,
		Pulse:   pulse,
	})
	if apiErr != nil {
		fmt.Fprintf(os.Stderr, "nichrome-cursor: %v\n", apiErr)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	fmt.Println(string(b))

	if *duration <= 0 {
		<-sigCh
		return
	}
	select {
	case <-sigCh:
	case <-time.After(*duration):
	}
}

func runDaemon(ctrl *companion.Controller, addr string, followInitial, watchSelInitial bool, autoHide time.Duration, initX, initY float64, mood, subtext, say string, pulse bool, sigCh <-chan os.Signal) {
	_ = os.Setenv("XT_NICHROME_CURSOR_DAEMON", "1")
	var (
		mu             sync.Mutex
		followUser     = followInitial
		watchSelection = watchSelInitial
		pinUntil       = time.Now().Add(6 * time.Second)
		msgUntil       = time.Now().Add(6 * time.Second)
		hideAfter      time.Time
		lastWinKey     string
		lastSelection  string
	)

	// Seed lastSelection with whatever is already in primary selection at launch
	// so we only trigger when the user highlights NEW text.
	if initSel, err := companion.ReadPrimarySelection(); err == nil {
		lastSelection = initSel
	}

	if followInitial || !watchSelInitial {
		_, _ = ctrl.Apply(types.CompanionRequest{
			Action:  "guide",
			X:       &initX,
			Y:       &initY,
			Mood:    mood,
			Subtext: subtext,
			Message: say,
			Pulse:   &pulse,
		})
		if autoHide > 0 && !followInitial {
			hideAfter = time.Now().Add(autoHide)
		}
	}

	stopCh := make(chan struct{})
	defer close(stopCh)

	go func() {
		ticker := time.NewTicker(350 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				now := time.Now()
				mu.Lock()
				doWatchSel := watchSelection
				prevSel := lastSelection
				mu.Unlock()

				if doWatchSel {
					if sel, err := companion.ReadPrimarySelection(); err == nil && sel != "" && sel != prevSel && len([]rune(sel)) >= 2 {
						cx, cy, ok := readHyprCursor()
						winClass, winTitle := readHyprActiveWindow()
						var tx, ty *float64
						if ok {
							px := math.Max(40, math.Min(1860, cx+40))
							py := math.Max(40, math.Min(1020, cy-36))
							tx, ty = &px, &py
						}
						if _, apiErr := ctrl.HandleSelection(types.SelectionRequest{
							Text:        sel,
							WindowClass: winClass,
							WindowTitle: winTitle,
							Guide:       true,
							X:           tx,
							Y:           ty,
						}); apiErr == nil {
							hold := autoHide
							if hold <= 0 {
								hold = 6 * time.Second
							}
							mu.Lock()
							lastSelection = sel
							pinUntil = now.Add(hold)
							msgUntil = now.Add(hold)
							if !followUser && autoHide > 0 {
								hideAfter = now.Add(hold)
							}
							mu.Unlock()
							continue
						}
					}
				}

				mu.Lock()
				shouldHide := !followUser && !hideAfter.IsZero() && now.After(hideAfter)
				if shouldHide {
					hideAfter = time.Time{}
				}
				doFollow := followUser && now.After(pinUntil)
				clearMsg := followUser && now.After(msgUntil)
				prevKey := lastWinKey
				mu.Unlock()

				if shouldHide && ctrl.State().Visible {
					_, _ = ctrl.Apply(types.CompanionRequest{Action: "hide"})
					continue
				}
				if !doFollow {
					continue
				}
				cx, cy, ok := readHyprCursor()
				winClass, winTitle := readHyprActiveWindow()
				winKey := winClass + "|" + winTitle
				switched := winClass != "" && winKey != prevKey
				if switched {
					mu.Lock()
					lastWinKey = winKey
					msgUntil = now.Add(4500 * time.Millisecond)
					clearMsg = false
					mu.Unlock()
				}
				if ok {
					tx := math.Max(40, math.Min(1860, cx+44))
					ty := math.Max(40, math.Min(1020, cy-32))
					req := types.CompanionRequest{
						Action: "move",
						X:      &tx,
						Y:      &ty,
					}
					if switched {
						shortTitle := winTitle
						if len([]rune(shortTitle)) > 38 {
							shortTitle = string([]rune(shortTitle)[:38]) + "…"
						}
						req.Subtext = fmt.Sprintf("%s • %s", winClass, shortTitle)
						req.Mood, req.Message = contextualReaction(winClass, winTitle)
						req.Action = "say"
					} else if clearMsg && ctrl.State().Message != "" {
						req.Action = "say"
						req.Message = ""
						if winClass != "" {
							req.Subtext = "watching • " + winClass
						}
					}
					_, _ = ctrl.Apply(req)
				}
			}
		}
	}()

	handleUpdate := func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			types.CompanionRequest
			FollowUser     *bool `json:"followUser,omitempty"`
			WatchSelection *bool `json:"watchSelection,omitempty"`
			PinSeconds     int   `json:"pinSeconds,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		now := time.Now()
		mu.Lock()
		if body.FollowUser != nil {
			followUser = *body.FollowUser
		}
		if body.WatchSelection != nil {
			watchSelection = *body.WatchSelection
		}
		pinSec := body.PinSeconds
		if pinSec <= 0 {
			pinSec = 8
		}
		pinUntil = now.Add(time.Duration(pinSec) * time.Second)
		msgUntil = now.Add(time.Duration(pinSec) * time.Second)
		if !followUser && autoHide > 0 {
			hideAfter = now.Add(time.Duration(pinSec) * time.Second)
		}
		mu.Unlock()
		st, apiErr := ctrl.Apply(body.CompanionRequest)
		if apiErr != nil {
			http.Error(w, apiErr.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	}

	handleSelection := func(w http.ResponseWriter, r *http.Request) {
		var req types.SelectionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.WindowClass == "" && req.WindowTitle == "" {
			req.WindowClass, req.WindowTitle = readHyprActiveWindow()
		}
		if req.Guide && req.X == nil && req.Y == nil {
			if cx, cy, ok := readHyprCursor(); ok {
				px := math.Max(40, math.Min(1860, cx+40))
				py := math.Max(40, math.Min(1020, cy-36))
				req.X, req.Y = &px, &py
			}
		}
		res, apiErr := ctrl.HandleSelection(req)
		if apiErr != nil {
			http.Error(w, apiErr.Error(), types.Status(apiErr.Code))
			return
		}
		if req.Guide {
			now := time.Now()
			mu.Lock()
			lastSelection = res.Text
			pinUntil = now.Add(6 * time.Second)
			msgUntil = now.Add(6 * time.Second)
			if !followUser && autoHide > 0 {
				hideAfter = now.Add(autoHide)
			}
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ctrl.State())
	})
	mux.HandleFunc("POST /guide", handleUpdate)
	mux.HandleFunc("POST /overlay", handleUpdate)
	mux.HandleFunc("POST /selection", handleSelection)

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		fmt.Fprintf(os.Stderr, "nichrome-cursor listening on http://%s\n", addr)
		_ = srv.ListenAndServe()
	}()
	<-sigCh
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func contextualReaction(class, title string) (string, string) {
	lowC := strings.ToLower(class)
	lowT := strings.ToLower(title)
	switch {
	case strings.Contains(lowC, "discord") || strings.Contains(lowC, "vesktop"):
		if strings.Contains(lowT, "general") {
			return "friendly", "back in #general! let me know if you want me to point at any message (^‿^)"
		}
		return "curious", "checking Discord: " + truncateRunes(title, 44)
	case strings.Contains(lowC, "chrome") || strings.Contains(lowC, "chromium") || strings.Contains(lowC, "firefox") || strings.Contains(lowC, "brave"):
		return "excited", "browser focused! I can point at any @eN element or guide you here (✦‿✦)"
	case strings.Contains(lowC, "kitty") || strings.Contains(lowC, "alacritty") || strings.Contains(lowC, "foot") || strings.Contains(lowC, "wezterm"):
		return "thinking", "terminal active — ready for commands!"
	case strings.Contains(lowC, "code") || strings.Contains(lowC, "antigravity") || strings.Contains(lowC, "zed"):
		return "friendly", "coding workspace focused — I'm right beside you!"
	default:
		return "friendly", "watching " + truncateRunes(title, 48)
	}
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func readHyprCursor() (float64, float64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "hyprctl", "cursorpos").Output()
	if err != nil {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) != 2 {
		return 0, 0, false
	}
	x, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	y, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return x, y, true
}

func readHyprActiveWindow() (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "hyprctl", "activewindow", "-j").Output()
	if err != nil {
		return "", ""
	}
	var w struct {
		Class string `json:"class"`
		Title string `json:"title"`
	}
	if json.Unmarshal(out, &w) != nil {
		return "", ""
	}
	return w.Class, w.Title
}
