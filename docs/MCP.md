# XT-Nichrome Model Context Protocol (MCP) Server & Companion 2nd Cursor

XT-Nichrome ships a first-class MCP server (`internal/mcpserver`) built on the official Go SDK (`github.com/modelcontextprotocol/go-sdk v1.8.0`) exposing **35 typed tools** across session management, tab lifecycle, Playwright/Camofox+ humanized interactions, JavaScript evaluation, PDF rendering, Web Storage (`localStorage`/`sessionStorage`), file uploads, dialog handling, structured extraction, console/network telemetry, cookie state, and the **Ni-Cursor 2nd Companion Cursor** (native Wayland/Hyprland `wlr-layer-shell` overlay + in-browser Shadow DOM overlay + real-time highlight/selection responder).

## 1. Transports

### A. Standalone Stdio Server (`cmd/nichrome-mcp`)
Starts its own in-process session manager, egress policy proxy, and Wayland/Hyprland companion controller, speaking MCP JSON-RPC 2.0 over `stdin`/`stdout`.

```bash
go build -trimpath -o bin/nichrome-mcp ./cmd/nichrome-mcp
```

Example MCP client config (`mcp.json` / Claude Desktop / Cursor / Antigravity):

```json
{
  "mcpServers": {
    "xt-nichrome": {
      "command": "/home/xtrcom/projects/XT-Nichrome/bin/nichrome-mcp",
      "args": [
        "-allow", "https://example.com,https://news.ycombinator.com",
        "-persona", "win11-chrome-141"
      ]
    }
  }
}
```

### B. Streamable HTTP on the Daemon (`cmd/nichrome -mcp-http`)
Mounts `/mcp` on the `nichrome` HTTP daemon behind the same constant-time `Authorization: Bearer <token>` middleware as `/v1/*`:

```bash
./bin/nichrome -listen 127.0.0.1:7788 -allow https://example.com -mcp-http
```

## 2. Complete 35-Tool Inventory

### Session & Tab Lifecycle (6 Tools)
| Tool | Arguments | Description |
| --- | --- | --- |
| `browser_open_session` | `mode?` (`ephemeral`\|`persistent`), `profileId?`, `persona?` | Creates a browser session and remembers `sessionId` as the default for this MCP client. |
| `browser_close_session` | `sessionId?` | Closes the session and all of its tabs, releasing persistent profile locks. |
| `browser_list_sessions` | *(none)* | Lists active sessions and reports `activeSessionId`. |
| `browser_open_tab` | `sessionId?`, `url?` | Opens a tab, verifies the stealth persona, and remembers `tabId` as the default tab. |
| `browser_close_tab` | `sessionId?`, `tabId?` | Closes the tab, invalidates snapshots, and frees a per-client tab slot. |
| `browser_list_tabs` | `sessionId?` | Lists open tabs (`tabId`, `currentUrl`, `title`, `generation`) and `activeTabId`. |

### Navigation, History & Synchronization (6 Tools)
| Tool | Arguments | Description |
| --- | --- | --- |
| `browser_navigate` | `url`, `waitUntil?` (`load`\|`commit`), `timeoutMs?`, `sessionId?`, `tabId?` | Navigates to an allowlisted URL and increments the document generation. |
| `browser_navigate_back` | `timeoutMs?`, `sessionId?`, `tabId?` | Navigates backward one entry in `Page.getNavigationHistory`. |
| `browser_navigate_forward` | `timeoutMs?`, `sessionId?`, `tabId?` | Navigates forward one entry in `Page.getNavigationHistory`. |
| `browser_reload` | `timeoutMs?`, `sessionId?`, `tabId?` | Reloads the active document and waits for load settle. |
| `browser_wait_for` | `text?`, `textGone?`, `timeMs?`, `timeoutMs?`, `sessionId?`, `tabId?` | Waits for visible text to appear, `textGone` to disappear, or a fixed `timeMs` pause. |
| `browser_snapshot` | `maxChars?`, `maxRefs?`, `includeValues?`, `scope?`, `sessionId?`, `tabId?` | Captures a compact accessibility tree with `@eN` handles and remembers `snapshotId`. |

### Humanized Interactions (10 Tools — Playwright + Camofox+)
| Tool | Arguments | Description |
| --- | --- | --- |
| `browser_click` | `ref`, `button?`, `clickCount?`, `snapshotId?`, `generation?`, `timeoutMs?` | Moves along a minimum-jerk Bézier curve to an interior point of `@eN` and clicks with `18–42ms` press dwell. |
| `browser_fill` | `ref`, `text`, `clearExisting?`, `snapshotId?`, `generation?`, `timeoutMs?` | Clicks to focus `@eN`, clears with selection + `Backspace`, and types per-character US-QWERTY `keyDown`/`keyUp` events. |
| `browser_hover` | `ref`, `snapshotId?`, `generation?`, `timeoutMs?` | Glides onto `@eN` and hovers (`55–110ms`) to trigger CSS `:hover` and JS `mouseover` menus/tooltips. |
| `browser_scroll` | `direction?` (`down`\|`up`\|`left`\|`right`), `amount?`, `ref?`, `snapshotId?` | Scrolls the viewport (or `@eN` container) using 6 momentum-eased `mouseWheel` ticks. |
| `browser_press_key` | `key`, `ref?`, `snapshotId?`, `timeoutMs?` | Presses keys or chords (`Enter`, `Escape`, `Tab`, `ArrowDown`, `PageDown`, `Control+a`, `Shift+Tab`) with Windows virtual key codes. |
| `browser_select_option` | `ref`, `value`, `snapshotId?`, `timeoutMs?` | Selects an `<option>` inside a `<select>` (`@eN`) by value or visible label and dispatches `input`/`change`. |
| `browser_check` | `ref`, `snapshotId?`, `timeoutMs?` | Idempotently checks a checkbox, radio, or switch (`@eN`). |
| `browser_uncheck` | `ref`, `snapshotId?`, `timeoutMs?` | Idempotently unchecks a checkbox or switch (`@eN`). |
| `browser_drag` | `ref`, `targetRef`, `snapshotId?`, `timeoutMs?` | Humanized drag-and-drop from `@eN` (`ref`) onto `@eM` (`targetRef`) along a minimum-jerk curve with `buttons: 1` held. |
| `browser_upload_file` | `ref`, `files`, `snapshotId?`, `timeoutMs?` | Glides to an `<input type="file">` (`@eN`) and attaches local file paths via CDP `DOM.setFileInputFiles`. |

### Evaluation, Storage, PDF, Extraction, Cookies & Telemetry (10 Tools)
| Tool | Arguments | Description |
| --- | --- | --- |
| `browser_evaluate` | `expression`, `ref?`, `snapshotId?`, `timeoutMs?` | Evaluates a JS expression in the page context, or invokes `el => ...` on a verified `@eN` DOM element. |
| `browser_pdf` | `outputPath?`, `landscape?`, `printBackground?`, `scale?` | Renders the active tab to PDF via `Page.printToPDF`, saving to `outputPath` or returning base64 bytes. |
| `browser_storage` | `area?` (`local`\|`session`), `action?` (`get`\|`set`\|`clear`), `entries?` | Inspects, seeds, or clears `localStorage` or `sessionStorage` on the active origin. |
| `browser_dialogs` | `accept?`, `promptText?`, `clearLog?` | Returns captured JS `alert`/`confirm`/`prompt`/`beforeunload` dialogs and configures how the next dialog is handled. |
| `browser_extract` | `maxChars?`, `maxLinks?`, `sessionId?`, `tabId?` | Extracts clean visible text, `h1-h3` headings, deduplicated `[text, href]` links, and scroll geometry (`scrollY`, `scrollHeight`). |
| `browser_cookies` | `action?` (`get`\|`set`\|`clear`), `cookies?`, `sessionId?`, `tabId?` | Reads, seeds (validated against the origin allowlist), or clears browser context cookies. |
| `browser_telemetry` | `sessionId?`, `tabId?` | Returns the tab's ring-buffered console messages, network requests/responses, and JS dialogs. |
| `browser_screenshot` | `sessionId?`, `tabId?` | Returns a viewport PNG as an `mcp.ImageContent` (`image/png`) block. |
| `browser_persona` | `sessionId?`, `tabId?` | Verifies the live tab's stealth identity and reports `consistent` + any mismatches. |
| `browser_ready` | *(none)* | Reports daemon readiness, capacity, and egress policy denial counts. |

### Companion 2nd Cursor ("Ni-Cursor" — Wayland/Hyprland + In-Browser) (3 Tools)
| Tool | Arguments | Description |
| --- | --- | --- |
| `companion_cursor_guide` | `message`, `ref?`, `x?`, `y?`, `mood?`, `subtext?`, `pulse?`, `sessionId?`, `tabId?`, `snapshotId?` | Glides the 2nd mini-cursor onto `(x, y)` or a snapshot `@eN` element, sets its mood (`friendly`, `curious`, `warn`, `sad`, `excited`, `thinking`), and shows a floating chat bubble (e.g. `"hey, you should click this to help me!"` or `"hey, this person isnt nice :("`). |
| `companion_cursor_control` | `action` (`status`\|`show`\|`hide`\|`clear`), `followActions?` | Shows, hides, clears, or queries the companion cursor state, or toggles `followActions` auto-narration. |
| `companion_analyze_selection` | `text?`, `readPrimary?`, `windowClass?`, `windowTitle?`, `guide?`, `x?`, `y?` | Analyzes highlighted text (or reads Wayland primary selection via `wl-paste --primary`), classifies tone/intent (hostile chat, friendly chat, question, URL, error trace, dangerous command, or base64/hex/timestamp/JSON), and optionally pops up `Ni-Cursor` with a real-time reply. |

## 3. Native Wayland / Hyprland Layer-Shell Overlay (`cmd/nichrome-cursor`)

On Wayland compositors supporting `wlr-layer-shell` (such as Hyprland and Sway), XT-Nichrome renders the 2nd companion cursor directly on the desktop `OVERLAY` layer (`internal/companion/wayland_overlay.c` compiled into `bin/nichrome-cursor-wayland`):
- **100% Click-Through:** Applies an empty `cairo_region_t` input mask via `gdk_window_input_shape_combine_region` and `GTK_LAYER_SHELL_KEYBOARD_MODE_NONE` so the user's real mouse clicks, hovers, and keystrokes pass straight through to windows underneath.
- **60 FPS Minimum-Jerk Glide:** Animates smoothly between targets along a quadratic/cubic Bézier arc with a Flash & Hogan minimum-jerk velocity profile ($10u^3 - 15u^4 + 6u^5$), gentle idle bobbing, and expanding crimson attention rings.
- **On-Demand Selection-Triggered Activation (`-watch-selection -auto-hide 6s`):** Keeps the overlay completely hidden until the user highlights text in Discord, a browser, or a terminal (`wl-paste --primary`), then glides to the cursor, displays a real-time contextual reply, and auto-hides after 6 seconds.
- **Standalone CLI Usage:**
  ```bash
  make cursor
  ./bin/nichrome-cursor -demo
  # On-demand highlight/selection responder (stays hidden until you select text, auto-hides after 6s):
  ./bin/nichrome-cursor -listen 127.0.0.1:7798 -watch-selection -auto-hide 6s
  # Or guide to a specific spot on Hyprland:
  ./bin/nichrome-cursor -x 820 -y 440 -mood friendly -subtext "@e4 • Confirm" -say "hey, you should click this to help me!" -duration 5s
  ```
