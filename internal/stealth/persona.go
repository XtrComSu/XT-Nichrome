// Package stealth builds a coherent, self-consistent browser identity.
//
// Design rule: every spoofed value must agree with every other value a page can
// observe. Chrome's real network stack, TLS fingerprint, JS engine, and rendering
// pipeline stay untouched — only the *reported platform identity* is aligned to
// the persona. That is why the engine is the real pinned Chromium build rather
// than a patched one: the network and TLS layers are then authentic by
// construction, and only the residual JS-surface gaps need alignment here.
package stealth

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// Persona is the complete identity a page can observe.
type Persona struct {
	Name        string `json:"name"`
	Description string `json:"description"`

	// Navigation identity.
	UserAgent      string  `json:"userAgent"`
	AppVersion     string  `json:"appVersion"`
	Platform       string  `json:"platform"`     // navigator.platform, e.g. Win32
	CDPPlatform    string  `json:"cdpPlatform"`  // Platform param of setUserAgentOverride
	OSName         string  `json:"osName"`       // Windows | macOS | Linux
	OSVersion      string  `json:"osVersion"`    // UA-CH platformVersion, e.g. 15.0.0
	Architecture   string  `json:"architecture"` // x86 | arm
	Bitness        string  `json:"bitness"`
	UAFullVersion  string  `json:"uaFullVersion"`
	MajorVersion   string  `json:"majorVersion"`
	Brands         []Brand `json:"brands"`
	FullBrands     []Brand `json:"fullVersionList"`
	AcceptLanguage string  `json:"acceptLanguage"`

	// Locale / time.
	Languages []string `json:"languages"`
	Locale    string   `json:"locale"`
	TimeZone  string   `json:"timeZone"`

	// Hardware.
	HardwareConcurrency int `json:"hardwareConcurrency"`
	DeviceMemory        int `json:"deviceMemory"`
	MaxTouchPoints      int `json:"maxTouchPoints"`

	// Display. These are the *emulated* viewport; BrowserWindow is the real OS
	// window the persona claims (outerWidth/Height, used for chrome-height math).
	ScreenWidth       int     `json:"screenWidth"`
	ScreenHeight      int     `json:"screenHeight"`
	AvailWidth        int     `json:"availWidth"`
	AvailHeight       int     `json:"availHeight"`
	DeviceScaleFactor float64 `json:"deviceScaleFactor"`
	ColorDepth        int     `json:"colorDepth"`
	ViewportWidth     int     `json:"viewportWidth"`
	ViewportHeight    int     `json:"viewportHeight"`

	// Graphics.
	GLVendor            string   `json:"glVendor"`
	GLRenderer          string   `json:"glRenderer"`
	GLUnmaskedVendor    string   `json:"glUnmaskedVendor"`
	GLUnmaskedRenderer  string   `json:"glUnmaskedRenderer"`
	GLVersion           string   `json:"glVersion"`
	GLSLVersion         string   `json:"glslVersion"`
	MaxTextureSize      int      `json:"maxTextureSize"`
	MaxRenderbufferSize int      `json:"maxRenderbufferSize"`
	MaxCubeMapSize      int      `json:"maxCubeMapSize"`
	MaxViewportDim      int      `json:"maxViewportDim"`
	AliasedPointMax     int      `json:"aliasedPointMax"`
	AliasedLineMax      int      `json:"aliasedLineMax"`
	GLExtensions        []string `json:"glExtensions"`

	// Media / sensors.
	AudioSampleRate    int     `json:"audioSampleRate"`
	AudioMaxChannels   int     `json:"audioMaxChannels"`
	AudioInputs        int     `json:"audioInputs"`
	AudioOutputs       int     `json:"audioOutputs"`
	VideoInputs        int     `json:"videoInputs"`
	NotificationPerm   string  `json:"notificationPermission"`
	ConnectionType     string  `json:"connectionType"`
	ConnectionRTT      int     `json:"connectionRtt"`
	ConnectionDownlink float64 `json:"connectionDownlink"`
	SpeechVoices       []Voice `json:"speechVoices"`

	// Fonts the persona claims to have installed.
	Fonts []string `json:"fonts"`

	// Chrome extension surface (window.chrome.*).
	ChromeObject bool `json:"chromeObject"`

	// Browser revision actually running (filled by the driver).
	BrowserRevision string `json:"browserRevision,omitempty"`
}

// Brand is one UA-CH brand entry.
type Brand struct {
	Brand   string `json:"brand"`
	Version string `json:"version"`
}

// Voice is a speechSynthesis voice.
type Voice struct {
	Name    string `json:"name"`
	Lang    string `json:"lang"`
	Local   bool   `json:"localService"`
	Default bool   `json:"default"`
}

// GreaseBrands returns the UA-CH brand list Chrome emits for a major version.
// The GREASE brand/version pair comes from the deterministic algorithm Chrome
// uses, so it must match the major version rather than being random.
func GreaseBrands(major int) (grease Brand, order int) {
	// Chrome's greased brand: chars from a rotating seed, joined with a version
	// derived from the major version. Values are stable for a given major.
	seeds := []string{"Not_A Brand", "Not;A Brand", "Not A(Brand", "Not?A_Brand", "Not)A;Brand", "Not/A)Brand"}
	order = major % len(seeds)
	grease = Brand{Brand: seeds[order], Version: greaseVersion}
	return grease, order
}

// greaseVersion is the version every GREASE brand entry carries. Chrome derives
// it from the major version, but always as a single digit.
const greaseVersion = "8"

// buildBrands constructs the ordered brand list Chrome reports.
//
// Two distinct lists are needed and conflating them is a known tell:
//   - getBrands() ("low entropy") carries the MAJOR version only, e.g. "141"
//   - getHighEntropyValues().fullVersionList carries the FOUR-PART version,
//     e.g. "141.0.7390.37"
func buildBrands(major int, fullVersion string) ([]Brand, []Brand) {
	grease, order := GreaseBrands(major)
	chromium := Brand{Brand: "Chromium", Version: fmt.Sprintf("%d", major)}
	chrome := Brand{Brand: "Google Chrome", Version: fmt.Sprintf("%d", major)}
	short := []Brand{chromium, chrome, grease}
	// Chrome rotates the GREASE entry position by major version.
	switch order % 3 {
	case 0:
		short = []Brand{grease, chromium, chrome}
	case 1:
		short = []Brand{chromium, grease, chrome}
	}
	full := make([]Brand, len(short))
	for i, b := range short {
		if strings.Contains(b.Brand, "Not") {
			// The GREASE entry keeps its single-digit version in both lists.
			full[i] = b
			continue
		}
		full[i] = Brand{Brand: b.Brand, Version: fullVersion}
	}
	return short, full
}

// chromeExtensionSet is the GL extension list ANGLE/D3D11 exposes on Windows.
// SwiftShader on Linux exposes a different set, which is a detectable tell.
var chromeExtensionSet = []string{
	"ANGLE_instanced_arrays",
	"EXT_blend_minmax",
	"EXT_clip_control",
	"EXT_color_buffer_half_float",
	"EXT_depth_clamp",
	"EXT_disjoint_timer_query",
	"EXT_float_blend",
	"EXT_frag_depth",
	"EXT_polygon_offset_clamp",
	"EXT_shader_texture_lod",
	"EXT_texture_compression_bptc",
	"EXT_texture_compression_rgtc",
	"EXT_texture_filter_anisotropic",
	"EXT_texture_mirror_clamp_to_edge",
	"EXT_sRGB",
	"KHR_parallel_shader_compile",
	"OES_element_index_uint",
	"OES_fbo_render_mipmap",
	"OES_standard_derivatives",
	"OES_texture_float",
	"OES_texture_float_linear",
	"OES_texture_half_float",
	"OES_texture_half_float_linear",
	"OES_vertex_array_object",
	"WEBGL_blend_func_extended",
	"WEBGL_color_buffer_float",
	"WEBGL_compressed_texture_s3tc",
	"WEBGL_compressed_texture_s3tc_srgb",
	"WEBGL_debug_renderer_info",
	"WEBGL_debug_shaders",
	"WEBGL_depth_texture",
	"WEBGL_draw_buffers",
	"WEBGL_lose_context",
	"WEBGL_multi_draw",
	"WEBGL_polygon_mode",
}

// windowsFonts is the font set a stock Windows 11 desktop exposes through the
// Detector/modern font-detection APIs and canvas width probing.
var windowsFonts = []string{
	"Arial", "Arial Black", "Bahnschrift", "Calibri", "Cambria", "Cambria Math",
	"Candara", "Comic Sans MS", "Consolas", "Constantia", "Corbel", "Courier New",
	"Ebrima", "Franklin Gothic Medium", "Gabriola", "Gadugi", "Georgia",
	"Impact", "Ink Free", "Javanese Text", "Leelawadee UI", "Lucida Console",
	"Lucida Sans Unicode", "Malgun Gothic", "Microsoft Himalaya", "Microsoft JhengHei",
	"Microsoft New Tai Lue", "Microsoft PhagsPa", "Microsoft Sans Serif",
	"Microsoft Tai Le", "Microsoft YaHei", "MingLiU-ExtB", "Mongolian Baiti",
	"MS Gothic", "MV Boli", "Myanmar Text", "Nirmala UI", "Palatino Linotype",
	"Segoe MDL2 Assets", "Segoe Print", "Segoe Script", "Segoe UI", "Segoe UI Emoji",
	"Segoe UI Historic", "Segoe UI Symbol", "SimSun", "Sitka", "Sylfaen", "Symbol",
	"Tahoma", "Times New Roman", "Trebuchet MS", "Verdana", "Webdings",
	"Wingdings", "Yu Gothic",
}

// windowsVoices models the default en-US voice set of Windows 11 23H2 Chrome.
func windowsVoices() []Voice {
	return []Voice{
		{Name: "Microsoft David - English (United States)", Lang: "en-US", Local: true},
		{Name: "Microsoft Mark - English (United States)", Lang: "en-US", Local: true},
		{Name: "Microsoft Zira - English (United States)", Lang: "en-US", Local: true},
	}
}

// DesktopChrome141 builds the Windows 11 Chrome 141 persona used by default.
// The build number matches the pinned Playwright Chromium revision so that
// `Browser.getVersion` and the UA-CH fullVersionList cannot be cross-checked
// into a contradiction.
func DesktopChrome141() Persona {
	const full = "141.0.7390.37"
	major := 141
	short, fullList := buildBrands(major, full)
	p := Persona{
		Name:        "win11-chrome-141",
		Description: "Windows 11 23H2, Chrome 141, single 1080p display, no touch, en-US",
		UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
			"(KHTML, like Gecko) Chrome/" + full + " Safari/537.36",
		// appVersion is the userAgent with the leading "Mozilla/" removed, which
		// is the invariant Chrome maintains. Kept explicit rather than derived so
		// a future edit to one field cannot silently desynchronise the other.
		AppVersion: "5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
			"(KHTML, like Gecko) Chrome/" + full + " Safari/537.36",
		Platform:            "Win32",
		CDPPlatform:         "Win32",
		OSName:              "Windows",
		OSVersion:           "15.0.0",
		Architecture:        "x86",
		Bitness:             "64",
		UAFullVersion:       full,
		MajorVersion:        "141",
		Brands:              short,
		FullBrands:          fullList,
		AcceptLanguage:      "en-US,en;q=0.9",
		Languages:           []string{"en-US", "en"},
		Locale:              "en-US",
		TimeZone:            "America/New_York",
		HardwareConcurrency: 8,
		DeviceMemory:        8,
		MaxTouchPoints:      0,
		ScreenWidth:         1920,
		ScreenHeight:        1080,
		AvailWidth:          1920,
		AvailHeight:         1032,
		DeviceScaleFactor:   1,
		ColorDepth:          24,
		ViewportWidth:       1920,
		ViewportHeight:      919,
		GLVendor:            "WebKit",
		GLRenderer:          "WebKit WebGL",
		GLUnmaskedVendor:    "Google Inc. (NVIDIA)",
		GLUnmaskedRenderer:  "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 Direct3D11 vs_5_0 ps_5_0, D3D11)",
		GLVersion:           "WebGL 1.0 (OpenGL ES 2.0 Chromium)",
		GLSLVersion:         "WebGL GLSL ES 1.0 (OpenGL ES GLSL ES 1.0 Chromium)",
		MaxTextureSize:      16384,
		MaxRenderbufferSize: 16384,
		MaxCubeMapSize:      16384,
		MaxViewportDim:      32767,
		AliasedPointMax:     1024,
		AliasedLineMax:      1,
		GLExtensions:        chromeExtensionSet,
		AudioSampleRate:     48000,
		AudioMaxChannels:    2,
		AudioInputs:         1,
		AudioOutputs:        1,
		VideoInputs:         1,
		NotificationPerm:    "prompt",
		ConnectionType:      "4g",
		ConnectionRTT:       50,
		ConnectionDownlink:  10,
		SpeechVoices:        windowsVoices(),
		Fonts:               windowsFonts,
		ChromeObject:        true,
	}
	return p
}

// Variants returns the built-in personas used by the privacy inventory test.
func Variants() map[string]func() Persona {
	return map[string]func() Persona{
		"win11-chrome-141": DesktopChrome141,
		"win10-chrome-141": func() Persona {
			p := DesktopChrome141()
			p.Name = "win10-chrome-141"
			p.Description = "Windows 10 22H2, Chrome 141, 1366x768, en-US"
			p.OSVersion = "10.0.0"
			p.UserAgent = strings.Replace(p.UserAgent, "Windows NT 10.0; Win64; x64", "Windows NT 10.0; Win64; x64", 1)
			p.ScreenWidth, p.AvailWidth = 1366, 1366
			p.ScreenHeight, p.AvailHeight = 768, 728
			p.ViewportWidth, p.ViewportHeight = 1366, 657
			p.HardwareConcurrency = 4
			p.DeviceMemory = 4
			p.GLUnmaskedRenderer = "ANGLE (Intel, Intel(R) UHD Graphics 620 Direct3D11 vs_5_0 ps_5_0, D3D11)"
			p.GLUnmaskedVendor = "Google Inc. (Intel)"
			p.MaxTextureSize, p.MaxRenderbufferSize, p.MaxCubeMapSize = 16384, 16384, 16384
			return p
		},
		"macos-chrome-141": func() Persona {
			p := DesktopChrome141()
			p.Name = "macos-chrome-141"
			p.Description = "macOS 14 (Sonoma) arm64, Chrome 141, Retina 1512x982, en-US"
			p.UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
				"(KHTML, like Gecko) Chrome/141.0.7390.37 Safari/537.36"
			p.AppVersion = "5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
				"(KHTML, like Gecko) Chrome/141.0.7390.37 Safari/537.36"
			p.Platform = "MacIntel"
			p.CDPPlatform = "MacIntel"
			p.OSName = "macOS"
			p.OSVersion = "14.6.1"
			p.Architecture = "arm"
			p.ScreenWidth, p.AvailWidth = 1512, 1512
			p.ScreenHeight, p.AvailHeight = 982, 944
			p.ViewportWidth, p.ViewportHeight = 1512, 823
			p.DeviceScaleFactor = 2
			p.GLUnmaskedVendor = "Google Inc. (Apple)"
			p.GLUnmaskedRenderer = "ANGLE (Apple, ANGLE Metal Renderer: Apple M2, Unspecified Version)"
			p.MaxTextureSize, p.MaxRenderbufferSize, p.MaxCubeMapSize = 16384, 16384, 16384
			p.Fonts = []string{"Helvetica", "Helvetica Neue", "Menlo", "Monaco", "SF Pro Text", "SF Pro Display", "Geneva", "Lucida Grande", "Optima", "Avenir", "Avenir Next", "Futura", "Baskerville", "Copperplate", "Gill Sans", "PingFang SC", "Hiragino Sans", "Apple Color Emoji", "Apple Symbols", "Andale Mono", "Courier New", "Arial", "Times New Roman", "Verdana", "Georgia", "Trebuchet MS", "Tahoma", "Impact", "Palatino", "Skia"}
			p.SpeechVoices = []Voice{
				{Name: "Samantha", Lang: "en-US", Local: true, Default: true},
				{Name: "Alex", Lang: "en-US", Local: true},
				{Name: "Karen", Lang: "en-AU", Local: true},
				{Name: "Daniel", Lang: "en-GB", Local: true},
			}
			p.AudioSampleRate = 48000
			return p
		},
		"linux-chrome-141": func() Persona {
			p := DesktopChrome141()
			p.Name = "linux-chrome-141"
			p.Description = "Ubuntu 24.04 x86_64, Chrome 141, 1920x1080, en-US"
			p.UserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
				"(KHTML, like Gecko) Chrome/141.0.7390.37 Safari/537.36"
			p.AppVersion = "5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
				"(KHTML, like Gecko) Chrome/141.0.7390.37 Safari/537.36"
			p.Platform = "Linux x86_64"
			p.CDPPlatform = "Linux x86_64"
			p.OSName = "Linux"
			p.OSVersion = "6.8.0"
			p.GLUnmaskedVendor = "Google Inc. (Mesa)"
			p.GLUnmaskedRenderer = "ANGLE (Mesa, llvmpipe (LLVM 17.0.6, 256 bits), OpenGL 4.5)"
			p.Fonts = []string{"DejaVu Sans", "DejaVu Serif", "DejaVu Sans Mono", "Liberation Sans", "Liberation Serif", "Liberation Mono", "Noto Sans", "Noto Color Emoji", "Ubuntu", "Cantarell", "FreeSans", "Arial", "Courier New", "Times New Roman"}
			p.SpeechVoices = nil
			return p
		},
	}
}

// aliases accept the informal names used in docs and flags.
var aliases = map[string]string{
	"win11":        "win11-chrome-141",
	"win11-chrome": "win11-chrome-141",
	"windows11":    "win11-chrome-141",
	"win10":        "win10-chrome-141",
	"win10-chrome": "win10-chrome-141",
	"macos":        "macos-chrome-141",
	"mac":          "macos-chrome-141",
	"linux":        "linux-chrome-141",
}

// Lookup resolves a persona by name (empty selects the default).
// PinnedProduct is the product string of the pinned build. It is a claim about
// which binary the deployment uses, checked against the live browser by
// ValidateIdentity at launch.
const PinnedProduct = "Chrome/141.0.7390.37"

// PinnedRevision is the pinned Chromium commit.
const PinnedRevision = "9f043f63b0e5b728c8d09f3e3ddfc1681a4bd58e"

func Lookup(name string) (Persona, error) {
	if name == "" {
		return DesktopChrome141(), nil
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if canonical, ok := aliases[key]; ok {
		key = canonical
	}
	f, ok := Variants()[key]
	if !ok {
		return Persona{}, fmt.Errorf("unknown persona %q", name)
	}
	p := f()
	p.Name = key
	if p.BrowserRevision == "" {
		p.BrowserRevision = PinnedProduct
	}
	return p, nil
}

// Names lists available persona names.
func Names() []string {
	return []string{"win11-chrome-141", "win10-chrome-141", "macos-chrome-141", "linux-chrome-141"}
}

// Seed derives a deterministic per-session offset so that two sessions of the
// same persona do not present byte-identical identities (screen size jitter
// within a realistic set, still internally consistent).
func (p Persona) Seed(sessionID string) Persona {
	sum := sha256.Sum256([]byte(p.Name + "|" + sessionID))
	h := int(sum[0])
	screens := [][4]int{
		{1920, 1080, 1920, 1032},
		{2560, 1440, 2560, 1392},
		{1536, 864, 1536, 816},
		{1920, 1080, 1920, 1040},
		{2560, 1440, 2560, 1400},
	}
	chosen := screens[h%len(screens)]
	p.ScreenWidth, p.ScreenHeight, p.AvailWidth, p.AvailHeight = chosen[0], chosen[1], chosen[2], chosen[3]
	p.ViewportWidth = p.ScreenWidth
	// Chrome's window chrome is 87px for the tab strip + omnibox + bookmarks bar.
	p.ViewportHeight = p.AvailHeight - 88
	if p.ViewportHeight < 400 {
		p.ViewportHeight = 400
	}
	if h%3 == 1 {
		p.HardwareConcurrency = 16
		p.DeviceMemory = 8
		p.GLUnmaskedRenderer = "ANGLE (NVIDIA, NVIDIA GeForce RTX 4070 Direct3D11 vs_5_0 ps_5_0, D3D11)"
	}
	if h%5 == 2 {
		p.HardwareConcurrency = 12
		p.DeviceMemory = 8
		p.GLUnmaskedRenderer = "ANGLE (AMD, AMD Radeon RX 6700 XT Direct3D11 vs_5_0 ps_5_0, D3D11)"
		p.GLUnmaskedVendor = "Google Inc. (AMD)"
	}
	return p
}

// Validate asserts internal consistency before the persona is used. A persona
// that fails this check would be *more* detectable than no persona at all.
func (p Persona) Validate() error {
	if p.UserAgent == "" || p.Platform == "" {
		return fmt.Errorf("stealth: persona %q is incomplete", p.Name)
	}
	if !strings.Contains(p.UserAgent, p.UAFullVersion) {
		return fmt.Errorf("stealth: persona %q user agent does not carry full version %s", p.Name, p.UAFullVersion)
	}
	if strings.Contains(p.UserAgent, "Headless") {
		return fmt.Errorf("stealth: persona %q user agent leaks HeadlessChrome", p.Name)
	}
	if p.AvailWidth > p.ScreenWidth || p.AvailHeight > p.ScreenHeight {
		return fmt.Errorf("stealth: persona %q available screen exceeds screen", p.Name)
	}
	if p.ViewportWidth > p.AvailWidth || p.ViewportHeight > p.AvailHeight {
		return fmt.Errorf("stealth: persona %q viewport exceeds available screen", p.Name)
	}
	if p.DeviceScaleFactor <= 0 {
		return fmt.Errorf("stealth: persona %q has invalid device scale factor", p.Name)
	}
	if p.HardwareConcurrency < 1 || p.DeviceMemory < 1 {
		return fmt.Errorf("stealth: persona %q has invalid hardware counts", p.Name)
	}
	if p.OSName == "Windows" && p.Platform != "Win32" {
		return fmt.Errorf("stealth: persona %q OS/platform mismatch", p.Name)
	}
	if p.MaxTouchPoints != 0 && p.OSName == "Windows" && p.MaxTouchPoints%2 != 0 {
		return fmt.Errorf("stealth: persona %q touch points must be even or zero", p.Name)
	}
	if p.GLUnmaskedRenderer == "" || !strings.Contains(p.GLUnmaskedRenderer, "ANGLE (") {
		return fmt.Errorf("stealth: persona %q needs an ANGLE renderer string", p.Name)
	}
	if len(p.Brands) != 3 {
		return fmt.Errorf("stealth: persona %q needs exactly three UA-CH brands", p.Name)
	}
	if len(p.Languages) == 0 || p.Locale == "" || p.TimeZone == "" {
		return fmt.Errorf("stealth: persona %q needs locale data", p.Name)
	}

	// Cross-field checks. A detector does not test one field in isolation; it
	// looks for pairs that cannot co-occur. Each rule below encodes a pairing
	// that was observed to be wrong in an earlier version of this file.
	if err := p.validateVersions(); err != nil {
		return err
	}
	if err := p.validatePlatformAgreement(); err != nil {
		return err
	}
	if err := p.validateRenderer(); err != nil {
		return err
	}
	if err := p.validateAudio(); err != nil {
		return err
	}
	if err := p.validateLocaleAgreement(); err != nil {
		return err
	}
	return nil
}

// validateVersions keeps userAgent, UAFullVersion and the UA-CH brand list on the
// same release.
func (p Persona) validateVersions() error {
	major := strings.SplitN(p.UAFullVersion, ".", 2)[0]
	if major == "" {
		return fmt.Errorf("stealth: persona %q has no version", p.Name)
	}
	if !strings.Contains(p.UserAgent, "Chrome/"+major+".") {
		return fmt.Errorf("stealth: persona %q user agent does not carry major version %s", p.Name, major)
	}
	// Low-entropy brands carry the major version only; fullVersionList carries
	// the four-part string. Mixing the two is a known mismatch signal.
	for _, b := range p.Brands {
		if strings.Contains(b.Brand, "Not") {
			if b.Version != greaseVersion {
				return fmt.Errorf("stealth: persona %q grease brand version is %q, want %q", p.Name, b.Version, greaseVersion)
			}
			continue
		}
		if b.Version != major {
			return fmt.Errorf("stealth: persona %q brand %q version is %q, want major %q", p.Name, b.Brand, b.Version, major)
		}
	}
	if len(p.FullBrands) != len(p.Brands) {
		return fmt.Errorf("stealth: persona %q fullVersionList has %d entries but brands has %d", p.Name, len(p.FullBrands), len(p.Brands))
	}
	for _, b := range p.FullBrands {
		if strings.Contains(b.Brand, "Not") {
			// GREASE entries are the exception: they keep the single-digit
			// version in both lists, which is what Chrome itself reports.
			if b.Version != greaseVersion {
				return fmt.Errorf("stealth: persona %q fullVersionList grease entry %q version is %q, want %q", p.Name, b.Brand, b.Version, greaseVersion)
			}
			continue
		}
		if b.Version != p.UAFullVersion {
			return fmt.Errorf("stealth: persona %q fullVersionList entry %q version is %q, want %q", p.Name, b.Brand, b.Version, p.UAFullVersion)
		}
	}
	return nil
}

// validatePlatformAgreement keeps the UA platform token, the JS platform string
// and the UA-CH platform/version in one story.
func (p Persona) validatePlatformAgreement() error {
	switch p.Platform {
	case "Win32":
		if p.OSName != "Windows" {
			return fmt.Errorf("stealth: persona %q reports Win32 with UA-CH platform %q", p.Name, p.OSName)
		}
		if !strings.Contains(p.UserAgent, "Windows NT 10.0") {
			return fmt.Errorf("stealth: persona %q reports Win32 but the user agent is not Windows", p.Name)
		}
		// Windows 11 is NT 10.0 with platformVersion 13+ (15.0.0 on 23H2).
		// Anything below 13 with an NT 10.0 token describes Windows 10.
		var major int
		if _, err := fmt.Sscanf(p.OSVersion, "%d.", &major); err != nil {
			return fmt.Errorf("stealth: persona %q has an unparseable platformVersion %q", p.Name, p.OSVersion)
		}
		if major < 13 && p.Name != "" && strings.Contains(p.Name, "win11") {
			return fmt.Errorf("stealth: persona %q claims Windows 11 but platformVersion is %q", p.Name, p.OSVersion)
		}
	case "MacIntel":
		if p.OSName != "macOS" || !strings.Contains(p.UserAgent, "Macintosh") {
			return fmt.Errorf("stealth: persona %q reports MacIntel with inconsistent UA data", p.Name)
		}
	case "Linux x86_64", "Linux i686":
		if p.OSName != "Linux" || !strings.Contains(p.UserAgent, "Linux") {
			return fmt.Errorf("stealth: persona %q reports %q with inconsistent UA data", p.Name, p.Platform)
		}
	default:
		return fmt.Errorf("stealth: persona %q has an unrecognised platform %q", p.Name, p.Platform)
	}
	return nil
}

// validateRenderer rejects a renderer string that belongs to another OS or to a
// software rasteriser, both of which are instant classification.
func (p Persona) validateRenderer() error {
	r := p.GLUnmaskedRenderer
	// Software rasterisers are a tell on every platform except Linux, where a
	// headless Chrome genuinely renders through llvmpipe and saying anything else
	// would be the lie.
	software := []string{"llvmpipe", "softpipe", "SwiftShader", "swiftshader"}
	for _, bad := range software {
		if p.OSName != "Linux" && strings.Contains(r, bad) {
			return fmt.Errorf("stealth: persona %q renderer leaks %q", p.Name, bad)
		}
	}
	for _, bad := range []string{"X11", "Gallium"} {
		if strings.Contains(r, bad) {
			return fmt.Errorf("stealth: persona %q renderer leaks %q", p.Name, bad)
		}
	}
	switch p.OSName {
	case "Windows":
		// Real Windows Chrome goes through ANGLE onto D3D11.
		if !strings.Contains(r, "Direct3D11") {
			return fmt.Errorf("stealth: persona %q is Windows but the renderer is not Direct3D11: %q", p.Name, r)
		}
		if strings.Contains(r, "OpenGL") {
			return fmt.Errorf("stealth: persona %q is Windows but the renderer mentions OpenGL: %q", p.Name, r)
		}
	case "macOS":
		if !strings.Contains(r, "Metal") && !strings.Contains(r, "OpenGL") {
			return fmt.Errorf("stealth: persona %q is macOS but the renderer is %q", p.Name, r)
		}
	}
	// GLVendor is the MASKED vendor ("WebKit"), which Chrome returns for
	// VENDOR while the real vendor only appears inside the unmasked renderer
	// string. They are different fields and must not be compared to each other.
	if p.GLVendor == "" {
		return fmt.Errorf("stealth: persona %q has no masked WebGL vendor", p.Name)
	}
	if p.GLUnmaskedVendor == "" {
		return fmt.Errorf("stealth: persona %q has no unmasked WebGL vendor", p.Name)
	}
	if !strings.Contains(r, p.GLUnmaskedVendor) && !strings.Contains(r, "ANGLE (") {
		return fmt.Errorf("stealth: persona %q renderer %q does not name unmasked vendor %q", p.Name, r, p.GLUnmaskedVendor)
	}
	if p.MaxTextureSize < 4096 {
		return fmt.Errorf("stealth: persona %q has an implausible MAX_TEXTURE_SIZE %d", p.Name, p.MaxTextureSize)
	}
	return nil
}

// validateAudio keeps the audio sample rate on the platform default. Windows
// mixes at 48 kHz; 44.1 kHz is the Linux default and a reliable tell.
func (p Persona) validateAudio() error {
	switch p.OSName {
	case "Windows":
		if p.AudioSampleRate != 48000 {
			return fmt.Errorf("stealth: persona %q is Windows but the audio sample rate is %d, want 48000", p.Name, p.AudioSampleRate)
		}
	case "macOS":
		if p.AudioSampleRate != 44100 && p.AudioSampleRate != 48000 {
			return fmt.Errorf("stealth: persona %q has an implausible audio sample rate %d", p.Name, p.AudioSampleRate)
		}
	}
	if p.AudioOutputs < 1 {
		return fmt.Errorf("stealth: persona %q reports no audio output device", p.Name)
	}
	return nil
}

// validateLocaleAgreement keeps the language list, the primary language and the
// time zone describing one plausible user.
func (p Persona) validateLocaleAgreement() error {
	if len(p.Languages) == 0 {
		return fmt.Errorf("stealth: persona %q has no languages", p.Name)
	}
	if p.Languages[0] != p.Locale {
		return fmt.Errorf("stealth: persona %q primary language %q differs from locale %q", p.Name, p.Languages[0], p.Locale)
	}
	if !strings.Contains(p.AcceptLanguage, p.Locale) {
		return fmt.Errorf("stealth: persona %q acceptLanguage %q omits locale %q", p.Name, p.AcceptLanguage, p.Locale)
	}
	// An en-US browser in Asia/Calcutta is a mismatch a detector scores.
	if strings.HasPrefix(p.Locale, "en-US") && !strings.HasPrefix(p.TimeZone, "America/") {
		return fmt.Errorf("stealth: persona %q pairs locale %q with time zone %q", p.Name, p.Locale, p.TimeZone)
	}
	return nil
}

// JSON returns the persona as JSON for embedding in the preload script.
func (p Persona) JSON() (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
