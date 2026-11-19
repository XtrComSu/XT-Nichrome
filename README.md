<p align="center">
  <img src="assets/logo-word.svg" alt="XT-Nichrome — Element 28 (Nickel)" width="860">
</p>

# XT-Nichrome (`Ni` — 28)

A hardened headless Chromium service and Model Context Protocol (MCP) server for authorized agent workflows. It drives a real Chromium over the DevTools pipe (`--remote-debugging-pipe`), exposes both a strict REST API and a 13-tool MCP interface for sessions, tabs, accessibility snapshots, and ref-based actions, virtualizes human mouse trajectories and per-keystroke cadence, and presents a self-consistent Windows 11 desktop Chrome identity.

```
agent ──HTTP / MCP──▶ nichrome / nichrome-mcp ──CDP pipe──▶ Chromium (win11-chrome-141)
                              │
                              └──▶ enforcing policy proxy ──▶ network
```

## Why a browser service

An agent that shells out to a raw headless browser gets an unpredictable process, a DOM-shaped string, and immediate bot-detection flags. An agent that talks to XT-Nichrome gets:

- **A lease.** Sessions are bounded, tabs are bounded, and one persistent profile cannot be opened twice (`flock`). Capacity is reported at `/readyz` (or `browser_ready`) before you ask for work.
- **References, not selectors.** `snapshot` (`browser_snapshot`) returns a compact accessibility tree with stable `@eN` handles. Acting on a handle that belongs to a superseded document is refused (`409 STALE_REF`) rather than silently retargeted, so an agent cannot click a button on the wrong page.
- **Humanized input virtualization.** `click` and `fill` never teleport to exact bounding-box midpoints or dump raw strings without key events. Every click selects a 2D triangular interior point (verified via `document.elementFromPoint`), moves the virtual cursor along a cubic Bézier curve with a Flash & Hogan minimum-jerk velocity profile (`10u³ - 15u⁴ + 6u⁵`) and micro-tremor, aligns `MouseEvent.screenY` with the desktop toolbar offset, and holds `mousedown` for a realistic `18–42 ms` dwell before `mouseup`. Every `fill` focuses the target with a humanized click, clears existing content via selection + `Backspace`, and dispatches per-character US-QWERTY `keyDown`/`keyUp` events with `windowsVirtualKeyCode`, `ShiftLeft` (`modifiers: 8`) transitions, and human digraph cadence.
- **One egress door.** The browser is launched with name resolution disabled and every connection pointed at a local proxy that enforces an origin allowlist and blocks RFC1918/loopback/link-local/metadata ranges by default.

## Quick start

```sh
make build cli mcp fixture detect   # bin/nichrome, bin/nichrome-cli, bin/nichrome-mcp, bin/fixture-server, bin/detect-server
make fonts                          # build the Windows-shaped font pack (once)

./bin/fixture-server -listen 127.0.0.1:7789 &
./bin/nichrome -listen 127.0.0.1:7788 -allow http://127.0.0.1:7789 -allow-private -mcp-http &
export XT_NICHROME_STATE=~/.local/share/xt-nichrome

SID=$(./bin/nichrome-cli new-session -json | jq -r .sessionId)
TID=$(./bin/nichrome-cli new-tab -session "$SID" -json | jq -r .tabId)
./bin/nichrome-cli go       -session "$SID" -tab "$TID" http://127.0.0.1:7789/form
./bin/nichrome-cli snapshot -session "$SID" -tab "$TID"
./bin/nichrome-cli fill     -session "$SID" -tab "$TID" SNAPSHOT e1 "Ada Lovelace"
./bin/nichrome-cli click    -session "$SID" -tab "$TID" SNAPSHOT e9
```

The bearer token is created on first run under `<state-dir>/token` with mode `0600`. Pass `-token` or `-token-file` to supply your own.

## Model Context Protocol (MCP) Server

XT-Nichrome ships a first-class MCP server (`internal/mcpserver`) built on the official Go SDK (`github.com/modelcontextprotocol/go-sdk`) with two transports:

1. **Standalone Stdio Binary (`bin/nichrome-mcp`):** Launches its own in-process session manager and speaks JSON-RPC 2.0 over stdin/stdout (zero network listeners or bearer tokens required).
2. **Streamable HTTP on Daemon (`bin/nichrome -mcp-http`):** Mounts `/mcp` on the daemon listener behind the same constant-time `Authorization: Bearer <token>` middleware as `/v1/*`.

All 13 typed tools (`browser_open_session`, `browser_close_session`, `browser_list_sessions`, `browser_open_tab`, `browser_close_tab`, `browser_list_tabs`, `browser_navigate`, `browser_snapshot`, `browser_click`, `browser_fill`, `browser_screenshot`, `browser_persona`, `browser_ready`) maintain per-MCP-session affinity (`sessionId`, `tabId`, `snapshotId`), enforce a per-client tab cap, and translate every service error code into actionable next-step guidance. See **[docs/MCP.md](docs/MCP.md)** for full tool schemas and client configuration.

## REST API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` | process liveness |
| `GET` | `/readyz` | readiness, capacity, egress state, browser revision |
| `POST` | `/v1/sessions` | create a session (`ephemeral` or `persistent`) |
| `GET` | `/v1/sessions` | list sessions |
| `GET` | `/v1/sessions/{id}` | describe one session |
| `DELETE` | `/v1/sessions/{id}` | destroy a session |
| `POST` | `/v1/sessions/{id}/tabs` | open a tab |
| `GET` | `/v1/sessions/{id}/tabs` | list tabs |
| `DELETE` | `/v1/sessions/{id}/tabs/{tab}` | close a tab |
| `POST` | `/v1/sessions/{id}/tabs/{tab}/navigate` | navigate |
| `GET` | `/v1/sessions/{id}/tabs/{tab}/snapshot` | accessibility tree + refs |
| `POST` | `/v1/sessions/{id}/tabs/{tab}/actions` | `click` or `fill` a ref |
| `GET` | `/v1/sessions/{id}/tabs/{tab}/screenshot` | PNG of the viewport |
| `GET` | `/v1/sessions/{id}/tabs/{tab}/persona` | observed identity report |
| `POST/GET` | `/mcp` | Streamable HTTP MCP endpoint (when `-mcp-http` is enabled) |

Errors are `{"code","message","requestId","detail"}`.

| Code | Status | Meaning |
| --- | --- | --- |
| `INVALID_REQUEST` | 400 | malformed body, unknown field, bad flag |
| `UNAUTHORIZED` | 401 | missing or wrong bearer token |
| `NOT_FOUND` | 404 | unknown session, tab or snapshot |
| `POLICY_BLOCKED` | 403 | destination outside the allowlist |
| `STALE_REF` | 409 | snapshot superseded or invalidated by a navigation |
| `AMBIGUOUS_REF` | 409 | the reference matches more than one element |
| `ELEMENT_NOT_ACTIONABLE` | 409 | right element, wrong verb (filling a checkbox) |
| `BUSY_PROFILE` | 409 | profile already open, or the tab has queued work |
| `CAPACITY` | 429 | session or tab quota reached |
| `TIMEOUT` | 504 | operation exceeded its deadline |
| `ACTION_OUTCOME_UNKNOWN` | 409 | the action was dispatched, the result was lost |

`ACTION_OUTCOME_UNKNOWN` is the important one. When a click is dispatched and the connection dies before the result is read, the service reports `status: "unknown"` rather than claiming failure — retrying a submit that may have succeeded is worse than telling the caller the truth.

## Stealth & Adversarial Detection Suite

The browser is a real Chromium 141.0.7390.37. Its TLS stack, HTTP/2 framing, JS engine, and rendering are untouched; the reported identity and input telemetry are aligned to a human Windows 11 desktop Chrome user. See **[docs/DETECTION.md](docs/DETECTION.md)** for the complete 28-probe adversarial benchmark (`cmd/detect-server` and `tests/detect_test.go`).

| Surface / Telemetry | Stock Headless on Linux (`--headless=new`) | XT-Nichrome (`win11-chrome-141`) |
| --- | --- | --- |
| **Composite Suspicion Score** | `62 / 100` (`HEADLESS_LINUX_BOT`) | `0 / 100` (`HUMAN_DESKTOP_WIN11`) |
| `navigator.userAgent` | `HeadlessChrome/141.0.7390.37` (Linux x86_64) | `Chrome/141.0.7390.37` on `Windows NT 10.0; Win64; x64` |
| `navigator.platform` | `Linux x86_64` | `Win32` |
| `navigator.webdriver` | `true` | `false` (native-masked prototype getter) |
| `screen` / `outerWidth×Height` | `800×600` / `800×600` | `1920×1080` (`1920×1040` avail) / `1920×919` |
| `(pointer: fine)` & `(hover: hover)` | `false` | `true` |
| `Notification.permission` | `denied` | `prompt` |
| `AudioContext.sampleRate` | `44100` / host default | `48000 Hz` |
| WebGL1 + WebGL2 (84 params) | `Google SwiftShader` | `ANGLE (Intel, Intel(R) UHD Graphics 630 Direct3D11 ...)` |
| Font families (`FONTCONFIG_FILE`) | 883 Linux families (`DejaVu Sans`, etc.) | 58 Windows families (`Segoe UI`, `Calibri`, `Consolas`, etc.); 0 Linux leaks |
| Pointer & Keystroke Telemetry | 0 `mousemove`, 0ms dwell, `(0.5, 0.5)` center | Minimum-jerk Bézier trajectory, `18–42ms` dwell, interior triangular sampling, `screenY` chrome offset, per-key `keyDown`/`keyUp` |

## Egress

The browser cannot reach the network except through the policy proxy:

```
--proxy-server=http://127.0.0.1:PORT
--proxy-bypass-list=<-loopback>          # loopback is proxied too
--host-resolver-rules=MAP * ~NOTFOUND,EXCLUDE 127.0.0.1
--disable-quic                            # QUIC would skip an HTTP proxy
```

Name resolution is disabled so a direct connection cannot resolve; the `EXCLUDE` is mandatory because the rule would otherwise also block the proxy's own address. WebRTC non-proxied UDP is disabled so a page cannot leak the host address around the proxy.

## Sessions

| Mode | Browser | Storage | Use |
| --- | --- | --- | --- |
| `ephemeral` | shared process, isolated context | discarded | parallel tasks, no state |
| `persistent` | dedicated process | kept in `profiles/{id}` | logins that must survive |

Default limits: 4 ephemeral sessions, 2 persistent, 4 tabs each, 8 queued operations per tab, 10-minute idle eviction.

## Testing & Benchmarks

```sh
make unit    # unit + MCP server protocol tests
make e2e     # end-to-end + adversarial detection suite against real Chromium
make race    # race detector
make bench   # latency against direct CDP
```

Measured on this host (8 cores, Chromium 141, headless new mode), 30 runs each, same fixture page, `median` / `p95`:

| Operation | Direct CDP | Through the service |
| --- | --- | --- |
| cold start to first answer | 145 ms | 439 ms (session + tab + persona gate) |
| navigate + load | 13.2 ms / 23.7 ms | 19.5 ms / 31.0 ms |
| tree fetch vs snapshot render | 3.1 ms / 4.3 ms | 2.4 ms / 7.0 ms |
| RSS | 203 MB (browser) | 15 MB (daemon) + browser |

## Layout

```
assets/               Ni (Element 28) periodic-table SVG/PNG brand assets
cmd/nichrome          HTTP + optional /mcp daemon
cmd/nichrome-mcp      standalone stdio MCP server
cmd/nichrome-cli      REST client
cmd/fixture-server    deterministic test pages
cmd/detect-server     standalone adversarial detection & behavioral telemetry server
internal/cdp          CDP over --remote-debugging-pipe, stdlib only
internal/browser      process lifecycle, pages, humanized input virtualization, persona gate
internal/stealth      persona definitions, launch flags, preload patch
internal/sessions     session and tab lifecycle, action pipeline
internal/snapshots    accessibility tree to refs
internal/policy       origin allowlist, prohibited address ranges
internal/egress       enforcing proxy (CONNECT, HTTP, WebSocket)
internal/profiles     on-disk profile leases
internal/fixtures     deterministic test pages
internal/detect       adversarial fingerprinting & behavioral probe suite
internal/mcpserver    13-tool MCP server (stdio + Streamable HTTP)
internal/api          HTTP surface
tests/                end-to-end & adversarial detection tests against real Chromium
bench/                latency comparison
deploy/               font pack builder and fontconfig template
docs/                 MCP and detection documentation
```

## Security posture

- Bearer token required on every `/v1` and `/mcp` HTTP route, compared in constant time.
- The browser is never started with a remote debugging port; CDP runs over a pipe. Nothing else on the host can attach.
- Profile directories are `0700` and the token file is held at `0600`.
- Profile names are validated against `^[a-z0-9](?:[a-z0-9_-]{0,62}[a-z0-9])?$` and resolved through `EvalSymlinks` before use.
- Request bodies are capped at 1 MiB and decoded strictly (no unknown fields, no duplicate keys, no trailing data).
- The browser renderer sandbox stays enabled (`--no-sandbox` is never used).
