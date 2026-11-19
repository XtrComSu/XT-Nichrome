package stealth

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

//go:embed patch.js
var patchSource string

var (
	scriptOnce sync.Once
	scriptSrc  string
	scriptErr  error
)

// Script returns the preload source with the persona substituted.
//
// The persona is injected as a JSON literal; the patch never reads it from the
// DOM, storage or a global the page can reach first, because a page script that
// runs before the patch could otherwise poison the values.
func Script(p Persona) (string, error) {
	if p.Name == "" {
		return "", fmt.Errorf("stealth: persona has no name")
	}
	if err := p.Validate(); err != nil {
		return "", err
	}
	scriptOnce.Do(func() {
		if !strings.Contains(patchSource, "__PERSONA__") {
			scriptErr = fmt.Errorf("stealth: patch.js is missing the __PERSONA__ placeholder")
			return
		}
		scriptSrc = patchSource
	})
	if scriptErr != nil {
		return "", scriptErr
	}
	payload, err := json.Marshal(payloadFor(p))
	if err != nil {
		return "", err
	}
	out := strings.Replace(scriptSrc, "__PERSONA__", string(payload), 1)
	// Guard against HTML-context breakouts if the source is ever inlined.
	if strings.Contains(out, "__PERSONA__") {
		return "", fmt.Errorf("stealth: persona placeholder appears more than once")
	}
	return out, nil
}

// payload is the exact object the patch reads. Kept explicit so a typo in a
// field name fails here rather than silently producing an undefined getter.
type payload struct {
	Name                string   `json:"name"`
	UserAgent           string   `json:"userAgent"`
	AppVersion          string   `json:"appVersion"`
	Platform            string   `json:"platform"`
	OSName              string   `json:"osName"`
	OSVersion           string   `json:"osVersion"`
	Architecture        string   `json:"architecture"`
	Bitness             string   `json:"bitness"`
	UAFullVersion       string   `json:"uaFullVersion"`
	Brands              []Brand  `json:"brands"`
	FullBrands          []Brand  `json:"fullBrands"`
	Languages           []string `json:"languages"`
	Locale              string   `json:"locale"`
	TimeZone            string   `json:"timeZone"`
	HardwareConcurrency int      `json:"hardwareConcurrency"`
	DeviceMemory        int      `json:"deviceMemory"`
	MaxTouchPoints      int      `json:"maxTouchPoints"`
	ScreenWidth         int      `json:"screenWidth"`
	ScreenHeight        int      `json:"screenHeight"`
	AvailWidth          int      `json:"availWidth"`
	AvailHeight         int      `json:"availHeight"`
	DeviceScaleFactor   float64  `json:"deviceScaleFactor"`
	ColorDepth          int      `json:"colorDepth"`
	ViewportWidth       int      `json:"viewportWidth"`
	ViewportHeight      int      `json:"viewportHeight"`
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
	AudioSampleRate     int      `json:"audioSampleRate"`
	AudioInputs         int      `json:"audioInputs"`
	AudioOutputs        int      `json:"audioOutputs"`
	VideoInputs         int      `json:"videoInputs"`
	NotificationPerm    string   `json:"notificationPermission"`
	ConnectionType      string   `json:"connectionType"`
	ConnectionRTT       int      `json:"connectionRtt"`
	ConnectionDownlink  float64  `json:"connectionDownlink"`
	SpeechVoices        []Voice  `json:"speechVoices"`
	Fonts               []string `json:"fonts"`
	ChromeObject        bool     `json:"chromeObject"`
}

func payloadFor(p Persona) payload {
	return payload{
		Name:                p.Name,
		UserAgent:           p.UserAgent,
		AppVersion:          p.AppVersion,
		Platform:            p.Platform,
		OSName:              p.OSName,
		OSVersion:           p.OSVersion,
		Architecture:        p.Architecture,
		Bitness:             p.Bitness,
		UAFullVersion:       p.UAFullVersion,
		Brands:              p.Brands,
		FullBrands:          p.FullBrands,
		Languages:           p.Languages,
		Locale:              p.Locale,
		TimeZone:            p.TimeZone,
		HardwareConcurrency: p.HardwareConcurrency,
		DeviceMemory:        p.DeviceMemory,
		MaxTouchPoints:      p.MaxTouchPoints,
		ScreenWidth:         p.ScreenWidth,
		ScreenHeight:        p.ScreenHeight,
		AvailWidth:          p.AvailWidth,
		AvailHeight:         p.AvailHeight,
		DeviceScaleFactor:   p.DeviceScaleFactor,
		ColorDepth:          p.ColorDepth,
		ViewportWidth:       p.ViewportWidth,
		ViewportHeight:      p.ViewportHeight,
		GLVendor:            p.GLVendor,
		GLRenderer:          p.GLRenderer,
		GLUnmaskedVendor:    p.GLUnmaskedVendor,
		GLUnmaskedRenderer:  p.GLUnmaskedRenderer,
		GLVersion:           p.GLVersion,
		GLSLVersion:         p.GLSLVersion,
		MaxTextureSize:      p.MaxTextureSize,
		MaxRenderbufferSize: p.MaxRenderbufferSize,
		MaxCubeMapSize:      p.MaxCubeMapSize,
		MaxViewportDim:      p.MaxViewportDim,
		AliasedPointMax:     p.AliasedPointMax,
		AliasedLineMax:      p.AliasedLineMax,
		GLExtensions:        p.GLExtensions,
		AudioSampleRate:     p.AudioSampleRate,
		AudioInputs:         p.AudioInputs,
		AudioOutputs:        p.AudioOutputs,
		VideoInputs:         p.VideoInputs,
		NotificationPerm:    p.NotificationPerm,
		ConnectionType:      p.ConnectionType,
		ConnectionRTT:       p.ConnectionRTT,
		ConnectionDownlink:  p.ConnectionDownlink,
		SpeechVoices:        p.SpeechVoices,
		Fonts:               p.Fonts,
		ChromeObject:        p.ChromeObject,
	}
}

// PatchSource exposes the raw template (tests assert it parses).
func PatchSource() string { return patchSource }
