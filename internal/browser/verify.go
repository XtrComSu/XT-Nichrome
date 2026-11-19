package browser

import (
	"context"
	"fmt"
	"strings"

	"github.com/xtrcom/xt-nichrome/internal/types"
)

// VerifyPersona checks, from inside the page, that the persona actually took
// effect and that no section of the patch failed. A partial persona is worse
// than none: the mismatch between a Windows UA and a Linux platform string is a
// stronger signal than either value alone.
func (p *Page) VerifyPersona(ctx context.Context) (*PersonaReport, *types.APIError) {
	expr := `(() => {
		const rep = window.__xtn_report__ || null;
		const g = (() => { try { return document.createElement('canvas').getContext('webgl'); } catch (e) { return null; } })();
		const dbg = g && g.getExtension('WEBGL_debug_renderer_info');
		return {
			patchInstalled: !!rep,
			patchFailures: rep ? rep.failures : [],
			persona: rep ? rep.persona : null,
			userAgent: navigator.userAgent,
			platform: navigator.platform,
			webdriver: navigator.webdriver,
			hardwareConcurrency: navigator.hardwareConcurrency,
			deviceMemory: navigator.deviceMemory,
			languages: navigator.languages,
			language: navigator.language,
			screen: [screen.width, screen.height, screen.availWidth, screen.availHeight, screen.colorDepth],
			window: [innerWidth, innerHeight, outerWidth, outerHeight, devicePixelRatio],
			glVendor: g ? g.getParameter(g.VENDOR) : null,
			glRenderer: g ? g.getParameter(g.RENDERER) : null,
			unmaskedVendor: dbg ? g.getParameter(dbg.UNMASKED_VENDOR_WEBGL) : null,
			unmaskedRenderer: dbg ? g.getParameter(dbg.UNMASKED_RENDERER_WEBGL) : null,
			timezone: (() => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone; } catch (e) { return null; } })(),
			pointerFine: matchMedia('(pointer: fine)').matches,
			hover: matchMedia('(hover: hover)').matches,
			chromeObject: typeof window.chrome === 'object' && window.chrome !== null,
			plugins: navigator.plugins.length,
			notificationPermission: (() => { try { return Notification.permission; } catch (e) { return null; } })(),
		};
	})()`
	var raw struct {
		PatchInstalled         bool      `json:"patchInstalled"`
		PatchFailures          []string  `json:"patchFailures"`
		Persona                string    `json:"persona"`
		UserAgent              string    `json:"userAgent"`
		Platform               string    `json:"platform"`
		Webdriver              bool      `json:"webdriver"`
		HardwareConcurrency    int       `json:"hardwareConcurrency"`
		DeviceMemory           int       `json:"deviceMemory"`
		Languages              []string  `json:"languages"`
		Language               string    `json:"language"`
		Screen                 []int     `json:"screen"`
		Window                 []float64 `json:"window"`
		GLVendor               string    `json:"glVendor"`
		GLRenderer             string    `json:"glRenderer"`
		UnmaskedVendor         string    `json:"unmaskedVendor"`
		UnmaskedRenderer       string    `json:"unmaskedRenderer"`
		Timezone               string    `json:"timezone"`
		PointerFine            bool      `json:"pointerFine"`
		Hover                  bool      `json:"hover"`
		ChromeObject           bool      `json:"chromeObject"`
		Plugins                int       `json:"plugins"`
		NotificationPermission string    `json:"notificationPermission"`
	}
	if apiErr := p.Evaluate(ctx, expr, &raw); apiErr != nil {
		return nil, apiErr
	}
	rep := &PersonaReport{
		PatchInstalled:         raw.PatchInstalled,
		PatchFailures:          raw.PatchFailures,
		Persona:                raw.Persona,
		UserAgent:              raw.UserAgent,
		Platform:               raw.Platform,
		Webdriver:              raw.Webdriver,
		HardwareConcurrency:    raw.HardwareConcurrency,
		DeviceMemory:           raw.DeviceMemory,
		Languages:              raw.Languages,
		Screen:                 raw.Screen,
		Window:                 raw.Window,
		GLVendor:               raw.GLVendor,
		GLRenderer:             raw.GLRenderer,
		UnmaskedVendor:         raw.UnmaskedVendor,
		UnmaskedRenderer:       raw.UnmaskedRenderer,
		Timezone:               raw.Timezone,
		PointerFine:            raw.PointerFine,
		Hover:                  raw.Hover,
		ChromeObject:           raw.ChromeObject,
		Plugins:                raw.Plugins,
		NotificationPermission: raw.NotificationPermission,
	}
	rep.Mismatches = p.checkConsistency(rep)
	return rep, nil
}

// PersonaReport is the observable state of one page, plus consistency findings.
type PersonaReport struct {
	PatchInstalled         bool      `json:"patchInstalled"`
	PatchFailures          []string  `json:"patchFailures,omitempty"`
	Persona                string    `json:"persona,omitempty"`
	UserAgent              string    `json:"userAgent"`
	Platform               string    `json:"platform"`
	Webdriver              bool      `json:"webdriver"`
	HardwareConcurrency    int       `json:"hardwareConcurrency"`
	DeviceMemory           int       `json:"deviceMemory"`
	Languages              []string  `json:"languages"`
	Screen                 []int     `json:"screen"`
	Window                 []float64 `json:"window"`
	GLVendor               string    `json:"glVendor"`
	GLRenderer             string    `json:"glRenderer"`
	UnmaskedVendor         string    `json:"unmaskedVendor"`
	UnmaskedRenderer       string    `json:"unmaskedRenderer"`
	Timezone               string    `json:"timezone"`
	PointerFine            bool      `json:"pointerFine"`
	Hover                  bool      `json:"hover"`
	ChromeObject           bool      `json:"chromeObject"`
	Plugins                int       `json:"plugins"`
	NotificationPermission string    `json:"notificationPermission"`
	Mismatches             []string  `json:"mismatches"`
}

// Consistent reports whether the persona is fully applied and self-consistent.
func (r *PersonaReport) Consistent() bool {
	return r.PatchInstalled && len(r.PatchFailures) == 0 && len(r.Mismatches) == 0
}

func (p *Page) checkConsistency(r *PersonaReport) []string {
	var out []string
	per := p.proc.persona
	add := func(f string, args ...any) { out = append(out, sprintf(f, args...)) }

	if !r.PatchInstalled {
		add("preload patch did not install")
	}
	for _, f := range r.PatchFailures {
		add("patch section failed: %s", f)
	}
	if r.UserAgent != per.UserAgent {
		add("userAgent mismatch: got %q want %q", r.UserAgent, per.UserAgent)
	}
	if strings.Contains(r.UserAgent, "Headless") {
		add("userAgent leaks HeadlessChrome")
	}
	if r.Platform != per.Platform {
		add("platform mismatch: got %q want %q", r.Platform, per.Platform)
	}
	if r.Webdriver {
		add("navigator.webdriver is true")
	}
	if r.HardwareConcurrency != per.HardwareConcurrency {
		add("hardwareConcurrency mismatch: got %d want %d", r.HardwareConcurrency, per.HardwareConcurrency)
	}
	if per.OSName == "Windows" && len(r.Screen) == 5 {
		if r.Screen[0] != per.ScreenWidth || r.Screen[1] != per.ScreenHeight {
			add("screen mismatch: got %v want %dx%d", r.Screen, per.ScreenWidth, per.ScreenHeight)
		}
	}
	if len(r.Window) == 5 && r.Window[2] == 0 {
		add("outerWidth is 0 (headless geometry leak)")
	}
	if r.UnmaskedRenderer == "" || !strings.Contains(r.UnmaskedRenderer, "ANGLE (") {
		add("unmasked WebGL renderer is not an ANGLE string: %q", r.UnmaskedRenderer)
	}
	if r.Timezone != per.TimeZone {
		add("timezone mismatch: got %q want %q", r.Timezone, per.TimeZone)
	}
	if !r.PointerFine || !r.Hover {
		add("pointer/hover media queries report a non-desktop input device")
	}
	if per.ChromeObject && !r.ChromeObject {
		add("window.chrome is missing")
	}
	if r.Plugins == 0 {
		add("navigator.plugins is empty")
	}
	if per.OSName == "Windows" && r.NotificationPermission == "denied" {
		add("Notification.permission is denied (no-display leak)")
	}
	return out
}

func sprintf(f string, args ...any) string { return fmt.Sprintf(f, args...) }
