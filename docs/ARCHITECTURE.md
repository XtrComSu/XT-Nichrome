# XT-Nichrome Internal Architecture (`Ni` — 28)

XT-Nichrome is a zero-port CDP browser automation service, **37-tool Model Context Protocol (MCP) server** (+ 4 Resources + 3 Prompts), and **Wayland/Hyprland 2nd Companion Cursor (`Ni-Cursor`)**. Every layer is engineered so that security boundaries, stealth identity coherence, and action idempotency hold by construction rather than by convention.

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                        AI Agent / CLI / HTTP Client                          │
└───────────────┬──────────────────────────────────────────┬───────────────────┘
                │ Stdio JSON-RPC 2.0                       │ HTTP /v1/* & /mcp
                ▼                                          ▼
┌───────────────────────────────┐          ┌───────────────────────────────────┐
│   cmd/nichrome-mcp (Stdio)    │          │     cmd/nichrome (HTTP Daemon)    │
│ • 37 MCP Tools                │          │ • Constant-Time Bearer Auth       │
│ • 4 Live JSON Resources       │          │ • Strict JSON Decoder (No Dupes)  │
│ • 3 Guided Prompts            │          │ • Streamable HTTP /mcp Endpoint   │
└───────────────┬───────────────┘          └─────────────────┬─────────────────┘
                └─────────────────────┬──────────────────────┘
                                      ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                      internal/sessions (Session Manager)                     │
│ • Pooled Ephemeral Contexts & Flock-Guarded Persistent Profiles              │
│ • Per-Tab Serialized Operation Queue & Idle Lease Eviction                   │
│ • Generation-Locked & DOM-Fingerprinted @eN Snapshot Registry                │
│ • 3-State Action Outcome Machine (Failed-Pre-Dispatch | Completed | Unknown) │
└───────┬─────────────────────────────┬────────────────────────────────┬───────┘
        │ --remote-debugging-pipe     │ --proxy-server=127.0.0.1:PORT  │ stdin JSON
        ▼ (FD 3 / FD 4, 0 TCP ports)  ▼ (DNS blackholed in browser)    ▼
┌───────────────────────┐   ┌───────────────────────────┐   ┌──────────────────┐
│   Chromium 141.0.x    │   │   internal/egress Proxy   │   │    Ni-Cursor     │
│ • Win11 Stealth Patch │   │ • Origin Allowlist / Web  │   │ • wlr-layer-shell│
│ • Isolated Fontconfig │   │ • Post-DNS CheckIPs SSRF  │   │ • Shadow DOM     │
│ • Humanized Bézier    │   │   Guard (Blocks RFC1918,  │   │ • Primary Select │
│   Mouse & Keystrokes  │   │   Loopback & Metadata)    │   │   Live Analyzer  │
└───────────────────────┘   └───────────────────────────┘   └──────────────────┘
```

---

## 1. Zero-Port CDP Pipe Transport (`internal/cdp`)

Stock browser automation tools launch Chromium with `--remote-debugging-port=9222`, exposing an unauthenticated local WebSocket that any process on the host (or via DNS rebinding) can attach to.
- XT-Nichrome launches Chromium with `--remote-debugging-pipe`, attaching Go `os.Pipe` file descriptors directly to child FD `3` (read by Chromium) and FD `4` (written by Chromium).
- Framing is `\0`-terminated JSON messages multiplexed by `id` and `sessionId`.
- Zero listening ports are opened by the browser process.

---

## 2. Mandatory Egress Proxy & Post-DNS SSRF Boundary (`internal/policy`, `internal/egress`)

Chromium is launched with all internal name resolution disabled:
```text
--proxy-server=http://127.0.0.1:<PORT>
--proxy-bypass-list=<-loopback>
--host-resolver-rules=MAP * ~NOTFOUND,EXCLUDE 127.0.0.1
--disable-quic
--webrtc-ip-handling-policy=disable_non_proxied_udp
```
1. **Pre-Flight Admission (`Policy.AdmitURL`):** Validates URL syntax, rejects embedded `user:pass@` credentials, enforces `http`/`https` schemes, and checks the target origin against either the explicit `-allow` allowlist or `-allow-public` (standard ports `80`/`443`).
2. **Post-DNS Socket Verification (`Policy.CheckIPs`):** When the local egress proxy resolves a hostname for `HTTP`, `HTTPS CONNECT`, or `WebSocket`, it inspects **every** returned `A`/`AAAA` record (normalizing IPv4-mapped IPv6 `::ffff:x.x.x.x` to 4-byte IPv4) and blocks any connection if *any* resolved IP falls in:
   - Loopback (`127.0.0.0/8`, `::1/128`) unless `-allow-private` is explicitly enabled for local test fixtures
   - RFC1918 Private LANs (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`)
   - Cloud Metadata & Link-Local (`169.254.0.0/16`, `fe80::/10`)
   - CGNAT (`100.64.0.0/10`), Unique Local IPv6 (`fc00::/7`), and Multicast

---

## 3. Coherent Stealth Identity & Fontconfig Jail (`internal/stealth`)

Modern bot detectors do not check a single property in isolation; they cross-check pairs that cannot co-occur on real hardware.
1. **Cross-Validated Persona (`stealth.Persona.Validate`):** Asserts at startup that `navigator.userAgent`, `navigator.appVersion`, `navigator.platform`, `navigator.userAgentData` (low-entropy `brands` with major version `141` + deterministic GREASE brand vs. high-entropy `fullVersionList` with four-part `141.0.7390.37`), `screen.*`, `outerWidth/Height` (87 px desktop toolbar delta), `AudioContext.sampleRate` (`48000 Hz` on Windows), `Intl` locale/timezone, and ANGLE Direct3D11 WebGL strings are 100% mutually consistent.
2. **WeakMap-Backed `Function.prototype.toString` Masking (`internal/stealth/patch.js`):** Every patched prototype getter and method is registered in a closure-private `WeakMap` so `Function.prototype.toString.call(fn)` returns native `function ...() { [native code] }` without attaching an own `.toString` property to the function instance.
3. **Isolated Windows 11 Font Pack (`deploy/build-fonts.py`):** Generates 58 metrically distinct Windows TTF faces (`Segoe UI`, `Calibri`, `Cambria`, `Consolas`, `Tahoma`, `Verdana`, `Georgia`, `Impact`, etc.) and points Chromium's `FONTCONFIG_FILE` exclusively at that directory so zero Linux host fonts (`DejaVu Sans`, `Ubuntu`, `Cantarell`) ever leak through `document.fonts.check` or canvas `measureText`.

---

## 4. Humanized Input Virtualization (`internal/browser/human.go`)

Every pointer and keyboard action (`click`, `fill`, `hover`, `drag`, `scroll`, `press`, `select`, `check`, `upload`) synthesizes human motor control:
- **Interior Triangular Target Selection (`humanPickPoint`):** Samples a 2D triangular distribution inside the central 56% of the target's bounding box and verifies via `document.elementFromPoint(x, y)` that the point hits the target node (not a covering modal/tooltip).
- **Minimum-Jerk Cubic Bézier Trajectory (`humanMoveTo`):** Moves from the tab's current virtual cursor position along a cubic Bézier curve with a Flash & Hogan minimum-jerk velocity profile ($s(u) = 10u^3 - 15u^4 + 6u^5$), physiological micro-tremor, Fitts's Law step count (`14–28` steps), and desktop toolbar offset (`screenY = clientY + 87`).
- **Press-Hold Dwell & Digraph Typing (`humanTypeText`):** Holds `mousePressed` for `18–42 ms` before `mouseReleased`, and dispatches per-character US-QWERTY `keyDown`/`keyUp` events with authentic `windowsVirtualKeyCode`s and `ShiftLeft` (`modifiers: 8`) transitions.

---

## 5. Generation-Locked Snapshots & 3-State Action Idempotency (`internal/snapshots`, `internal/sessions`)

1. **Dual-Verified `@eN` References:** Every `@eN` handle in `browser_snapshot` records both the document `generation` counter and a DOM structural signature (`HTML[0]/BODY[1]/FORM[0]/BUTTON[2]|submit-btn`). Before dispatching any action, `resolveAndVerifyNode` re-computes the live DOM node's signature and rejects mismatches with `409 STALE_REF`.
2. **`ACTION_OUTCOME_UNKNOWN` Guard:** If a side-effecting action is sent over CDP and the tab times out or disconnects before the response arrives, XT-Nichrome returns `409 ACTION_OUTCOME_UNKNOWN` (`outcome: "unknown"`, `verifyBeforeRetry: true`) instead of a generic error—preventing agents from blindly double-submitting forms or payments.

---

## 6. Dual-Surface Companion Cursor (`internal/companion`, `cmd/nichrome-cursor`)

`Ni-Cursor` renders simultaneously on:
1. **Wayland / Hyprland Desktop (`internal/companion/wayland_overlay.c`):** A native C helper using `gtk-layer-shell` (`GTK_LAYER_SHELL_LAYER_OVERLAY`), Cairo/Pango rendering, 60 FPS minimum-jerk interpolation, and an empty `gdk_window_input_shape_combine_region` mask so 100% of clicks and keystrokes pass straight through.
2. **In-Browser Tabs (`Page.RenderCompanionOverlay`):** An isolated, `pointer-events: none`, `aria-hidden="true"` Shadow DOM overlay visible in browser windows and screenshots without polluting accessibility snapshots.
3. **Real-Time Selection Analyzer (`internal/companion/selection.go`):** Watches Wayland primary selection (`wl-paste --primary --no-newline`) in `-watch-selection -auto-hide 6s` mode, classifying chat tone, questions, URLs, dangerous shell commands, error traces, and base64/hex/timestamp payloads on the fly.
