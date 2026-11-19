// Command nichrome-cursor drives the native Wayland/Hyprland 2nd companion
// cursor ("Ni-Cursor") directly from the terminal or Hyprland keybinds.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/companion"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

func main() {
	var (
		x        = flag.Float64("x", 680, "target screen X coordinate")
		y        = flag.Float64("y", 420, "target screen Y coordinate")
		say      = flag.String("say", "hey, you should click this to help me!", "chat bubble message")
		subtext  = flag.String("subtext", "Ni-28 assistant", "small guiding text near the cursor")
		mood     = flag.String("mood", "friendly", "companion mood: friendly|curious|warn|sad|excited|thinking")
		pulse    = flag.Bool("pulse", true, "render expanding crimson pulse ring at cursor tip")
		demo     = flag.Bool("demo", false, "run an interactive multi-step tour across the screen")
		duration = flag.Duration("duration", 5*time.Second, "how long to keep the overlay visible (0 = until Ctrl+C)")
	)
	flag.Parse()

	ctrl := companion.New()
	defer ctrl.Close()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

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
