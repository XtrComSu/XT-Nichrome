package companion

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xtrcom/xt-nichrome/internal/types"
)

// ReadPrimarySelection reads the current Wayland primary selection (highlighted
// text) via `wl-paste --primary --no-newline` with a tight 350ms timeout.
func ReadPrimarySelection() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "wl-paste", "--primary", "--no-newline").Output()
	if err != nil {
		return "", err
	}
	if !utf8.Valid(out) {
		return "", fmt.Errorf("primary selection is not valid UTF-8 text")
	}
	return strings.TrimSpace(string(out)), nil
}

// HandleSelection analyzes highlighted text (reading from Wayland primary
// selection when req.ReadPrimary is true and req.Text is empty) and optionally
// guides the companion cursor to react in real time.
func (c *Controller) HandleSelection(req types.SelectionRequest) (*types.SelectionAnalysis, *types.APIError) {
	raw := strings.TrimSpace(req.Text)
	if raw == "" && req.ReadPrimary {
		txt, err := ReadPrimarySelection()
		if err != nil {
			return nil, &types.APIError{
				Code:    types.CodeInvalidRequest,
				Message: "could not read Wayland primary selection: " + err.Error(),
			}
		}
		raw = txt
	}
	if raw == "" {
		return nil, &types.APIError{
			Code:    types.CodeInvalidRequest,
			Message: "text is required (or set readPrimary=true when text is highlighted)",
			Detail:  map[string]any{"field": "text"},
		}
	}

	analysis := AnalyzeSelection(raw, req.WindowClass, req.WindowTitle)

	c.mu.Lock()
	cpAnalysis := analysis
	c.state.LastSelection = &cpAnalysis
	c.mu.Unlock()

	if req.Guide {
		pls := true
		st, apiErr := c.Apply(types.CompanionRequest{
			Action:  "guide",
			X:       req.X,
			Y:       req.Y,
			Mood:    analysis.Mood,
			Subtext: analysis.Subtext,
			Message: analysis.Reply,
			Pulse:   &pls,
		})
		if apiErr != nil {
			return nil, apiErr
		}
		analysis.Guided = st.Visible
		analysis.X = st.X
		analysis.Y = st.Y
	}
	return &analysis, nil
}

// AnalyzeSelection inspects a highlighted string from Discord, a browser,
// terminal, or editor and returns a real-time companion reaction, mood, and
// optional suggested reply or decoded payload.
func AnalyzeSelection(text, windowClass, windowTitle string) types.SelectionAnalysis {
	trimmed := strings.TrimSpace(text)
	low := strings.ToLower(trimmed)
	winLabel := strings.TrimSpace(windowClass)
	if winLabel == "" {
		winLabel = "selection"
	}

	out := types.SelectionAnalysis{
		Text:        truncateStr(trimmed, 600),
		WindowClass: windowClass,
		WindowTitle: windowTitle,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}

	// 1. Dangerous shell commands.
	for _, hazard := range []struct {
		token string
		why   string
	}{
		{"rm -rf", "recursive force-delete"},
		{"mkfs.", "filesystem format"},
		{"dd if=", "raw block device write"},
		{"chmod 777", "world-writable permissions"},
		{"chmod -r 777", "recursive world-writable permissions"},
		{"git reset --hard", "discards uncommitted changes"},
		{"git push --force", "overwrites remote git history"},
		{"curl ", "piped remote script"},
		{":(){ :|:& };:", "fork bomb"},
	} {
		if strings.Contains(low, hazard.token) {
			if hazard.token == "curl " && !strings.Contains(low, "| sh") && !strings.Contains(low, "| bash") {
				continue
			}
			out.Category = "command_warning"
			out.Mood = "warn"
			out.Subtext = winLabel + " • command caution"
			out.Reply = fmt.Sprintf("careful! that command contains %s (%s) — double-check before running (•̀_•́)!", hazard.token, hazard.why)
			return out
		}
	}

	// 2. Hostile / rude / toxic chat messages (e.g. in Discord or social feeds).
	for _, rude := range []string{
		"you suck", "shut up", "idiot", "stupid", "moron", "trash",
		"loser", "hate you", "nobody asked", "get lost", "pathetic",
		"worthless", "dumbass", "clown", "kill yourself", "kys",
	} {
		if strings.Contains(low, rude) {
			out.Category = "hostile_chat"
			out.Mood = "sad"
			out.Subtext = winLabel + " • tone alert"
			out.Reply = "hey, this person isnt nice :( you don't owe them energy — want to ignore/mute or send a calm boundary?"
			out.SuggestedReply = "Let's keep this respectful, otherwise I'm going to step out of the conversation."
			return out
		}
	}

	// 3. Single URL or link inspection.
	if !strings.ContainsAny(trimmed, " \t\n") && (strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://")) {
		if u, err := url.Parse(trimmed); err == nil && u.Host != "" {
			host := u.Hostname()
			suspicious := false
			var reasons []string
			if u.User != nil {
				suspicious = true
				reasons = append(reasons, "embedded @ userinfo")
			}
			if net.ParseIP(host) != nil {
				suspicious = true
				reasons = append(reasons, "raw IP host")
			}
			if strings.Contains(strings.ToLower(host), "xn--") {
				suspicious = true
				reasons = append(reasons, "punycode domain")
			}
			if u.Scheme == "http" {
				reasons = append(reasons, "unencrypted HTTP")
			}
			if suspicious {
				out.Category = "url"
				out.Mood = "warn"
				out.Subtext = "URL • " + truncateStr(host, 28)
				out.Reply = fmt.Sprintf("heads up! this link (%s) uses %s — open it in an isolated XT-Nichrome ephemeral tab first!", host, strings.Join(reasons, ", "))
				return out
			}
			qCount := len(u.Query())
			out.Category = "url"
			out.Mood = "curious"
			out.Subtext = "URL • " + truncateStr(host, 28)
			out.Reply = fmt.Sprintf("link to %s%s (%d query params) — want me to open & snapshot it for you?", host, truncateStr(u.Path, 24), qCount)
			return out
		}
	}

	// 4. Stack traces, compiler diagnostics, or runtime errors.
	for _, errTok := range []string{
		"panic:", "traceback (most recent", "typeerror:", "referenceerror:",
		"syntaxerror:", "segmentation fault", "sigsegv", "econnrefused",
		"enoent", "nullpointerexception", "status 500", "status 404", "error:",
	} {
		if strings.Contains(low, errTok) {
			firstLine := strings.TrimSpace(strings.Split(trimmed, "\n")[0])
			out.Category = "error_trace"
			out.Mood = "warn"
			out.Subtext = winLabel + " • error trace"
			out.Reply = fmt.Sprintf("spotted an error (%s) — want me to trace the root cause and fix it?", truncateStr(firstLine, 72))
			return out
		}
	}

	// 5. Encoded payloads: Unix timestamp, Hex, Base64, or JSON object.
	if decoded, kind, ok := tryDecodeSelection(trimmed); ok {
		out.Category = "encoded_data"
		out.Mood = "thinking"
		out.Subtext = winLabel + " • decoded " + kind
		out.Decoded = decoded
		out.Reply = fmt.Sprintf("decoded %s → %s", kind, truncateStr(decoded, 110))
		return out
	}

	// 6. Friendly greetings or direct mentions of Ni.
	for _, greet := range []string{
		"hello ni", "hey ni", "hi ni", "good morning", "thank you", "tysm",
		"thanks!", "awesome", "great job", "love this", "nice!",
	} {
		if strings.Contains(low, greet) || low == "hello" || low == "hey" || low == "hi" || low == "gm" {
			out.Category = "friendly_chat"
			out.Mood = "excited"
			out.Subtext = winLabel + " • friendly message"
			if strings.Contains(low, "ni") {
				out.Reply = "heyyy! waving right back at you from Ni-28 (^‿^)!"
				out.SuggestedReply = "Ni-28 says hello back! (^‿^)"
			} else {
				out.Reply = "friendly vibes! (^‿^) want a quick upbeat reply?"
				out.SuggestedReply = "Hey! Hope you're having a great day!"
			}
			return out
		}
	}

	// 7. Questions in Discord / chat / docs.
	if strings.HasSuffix(trimmed, "?") || strings.HasPrefix(low, "can you ") ||
		strings.HasPrefix(low, "how do ") || strings.HasPrefix(low, "what is ") ||
		strings.HasPrefix(low, "why ") || strings.HasPrefix(low, "where ") {
		out.Category = "question_chat"
		out.Mood = "curious"
		out.Subtext = winLabel + " • question"
		out.Reply = fmt.Sprintf("they're asking: %q — want me to look up the answer or draft a reply?", truncateStr(trimmed, 68))
		out.SuggestedReply = "Good question — let me check on that and get right back to you!"
		return out
	}

	// 8. Multi-line code snippet vs general text.
	lines := strings.Count(trimmed, "\n") + 1
	words := len(strings.Fields(trimmed))
	if lines >= 2 && strings.ContainsAny(trimmed, "{};()=>[]") {
		out.Category = "code_snippet"
		out.Mood = "thinking"
		out.Subtext = fmt.Sprintf("%s • %d lines of code", winLabel, lines)
		out.Reply = fmt.Sprintf("selected %d lines (%d words) of code — want me to audit or explain it?", lines, words)
		return out
	}

	out.Category = "general"
	out.Mood = "friendly"
	out.Subtext = fmt.Sprintf("%s • %d words selected", winLabel, words)
	out.Reply = fmt.Sprintf("highlighted %q (%d words) — I'm right here if you want to reply or inspect it (^‿^)", truncateStr(trimmed, 64), words)
	return out
}

func tryDecodeSelection(s string) (decoded string, kind string, ok bool) {
	// Unix epoch seconds (10 digits) or milliseconds (13 digits).
	if len(s) == 10 || len(s) == 13 {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 1000000000 {
			var t time.Time
			if len(s) == 13 {
				t = time.UnixMilli(n).UTC()
			} else {
				t = time.Unix(n, 0).UTC()
			}
			return t.Format(time.RFC3339), "timestamp", true
		}
	}

	// Compact JSON object/array validation & key summary.
	if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) || (strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			if m, isMap := v.(map[string]any); isMap {
				keys := make([]string, 0, len(m))
				for k := range m {
					keys = append(keys, k)
				}
				return fmt.Sprintf("valid JSON object (%d keys: %s)", len(keys), strings.Join(keys, ", ")), "JSON", true
			}
			if arr, isArr := v.([]any); isArr {
				return fmt.Sprintf("valid JSON array (%d items)", len(arr)), "JSON", true
			}
		}
	}

	// Hex string (0x... or even-length >= 8 hex chars without spaces).
	hexCand := strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(hexCand) >= 8 && len(hexCand)%2 == 0 && !strings.ContainsAny(hexCand, " \t\n") {
		if raw, err := hex.DecodeString(hexCand); err == nil && isPrintableASCII(raw) {
			return string(raw), "hex", true
		}
	}

	// Base64 string (length >= 8, no whitespace, decodes to printable UTF-8).
	if len(s) >= 8 && len(s)%4 == 0 && !strings.ContainsAny(s, " \t\n") && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/' || r == '=' || r == '-' || r == '_')
	}) == -1 {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
			if raw, err := enc.DecodeString(s); err == nil && len(raw) >= 4 && isPrintableASCII(raw) {
				return string(raw), "base64", true
			}
		}
	}

	return "", "", false
}

func isPrintableASCII(b []byte) bool {
	if len(b) == 0 || !utf8.Valid(b) {
		return false
	}
	for _, c := range b {
		if c < 32 && c != '\n' && c != '\r' && c != '\t' {
			return false
		}
		if c > 126 {
			return false
		}
	}
	return true
}
