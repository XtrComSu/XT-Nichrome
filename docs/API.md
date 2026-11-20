# XT-Nichrome HTTP REST API Reference (`/v1/*`)

The `nichrome` daemon exposes a strict JSON REST API over HTTP (`default: 127.0.0.1:7788`).
- **Authentication:** Every `/v1/*` and `/mcp` route requires `Authorization: Bearer <token>` (compared in constant time via `crypto/subtle.ConstantTimeCompare`). Only `GET /healthz` and `GET /readyz` are unauthenticated.
- **Strict JSON Decoding:** Request bodies are capped at `1 MiB`, require `Content-Type: application/json`, and reject unknown fields, duplicate JSON keys, and trailing tokens with `400 INVALID_REQUEST`.

---

## 1. Health & Readiness

### `GET /healthz`
Returns process liveness and version.
```json
{
  "status": "ok",
  "version": "1.0.0"
}
```

### `GET /readyz`
Returns readiness, active session/tab counts, capacity limits, and egress policy denial counts (`200 OK` when capacity is available, `503 Service Unavailable` when full).
```json
{
  "ready": true,
  "browserRevision": "Chrome/141.0.7390.37",
  "persona": "win11-chrome-141",
  "sessions": 1,
  "tabs": 1,
  "capacity": {
    "ephemeralContexts": 4,
    "persistentBrowsers": 2,
    "tabsPerSession": 4,
    "queuedOpsPerTab": 8
  },
  "egress": {
    "mode": "proxy",
    "proxyAddr": "127.0.0.1:41923",
    "allowedOrigins": 2,
    "guardActive": true,
    "denials": 0
  }
}
```

---

## 2. Sessions (`/v1/sessions`)

### `POST /v1/sessions`
Creates an `ephemeral` (pooled browser, isolated context) or `persistent` (dedicated browser + `flock`-locked profile directory) session.
```json
{
  "mode": "ephemeral",
  "persona": "win11-chrome-141"
}
```
Response (`201 Created`):
```json
{
  "sessionId": "sess_a1b2c3d4",
  "mode": "ephemeral",
  "state": "ready",
  "persona": "win11-chrome-141",
  "createdAt": "2026-11-19T18:00:00Z"
}
```

### `GET /v1/sessions`
Lists all active sessions.

### `GET /v1/sessions/{id}`
Describes a single session.

### `DELETE /v1/sessions/{id}`
Closes the session and all of its tabs, releasing its capacity slot and any persistent profile lock (`204 No Content`).

---

## 3. Tabs (`/v1/sessions/{id}/tabs`)

### `POST /v1/sessions/{id}/tabs`
Opens a new tab, runs the startup stealth persona self-check gate, and optionally navigates to an initial URL.
```json
{
  "url": "https://example.com"
}
```

### `GET /v1/sessions/{id}/tabs`
Lists all open tabs (`tabId`, `currentUrl`, `title`, `generation`) in the session.

### `DELETE /v1/sessions/{id}/tabs/{tab}`
Closes the tab and invalidates its snapshots (`204 No Content`).

---

## 4. Navigation, History & Synchronization

### `POST /v1/sessions/{id}/tabs/{tab}/navigate`
Navigates to an authorized URL and waits for `"load"` (default) or `"commit"`.
```json
{
  "url": "https://example.com/form",
  "waitUntil": "load",
  "timeoutMs": 30000
}
```

### `POST /v1/sessions/{id}/tabs/{tab}/history`
Navigates session history (`"back"`, `"forward"`, or `"reload"`).
```json
{
  "action": "back"
}
```

### `POST /v1/sessions/{id}/tabs/{tab}/wait`
Waits for visible `text` to appear, `textGone` to disappear, or a fixed `timeMs` delay.
```json
{
  "text": "Order Confirmed",
  "timeoutMs": 10000
}
```

---

## 5. Accessibility Snapshots, Locator Search & Extraction

### `GET /v1/sessions/{id}/tabs/{tab}/snapshot`
Query parameters: `maxChars` (`default 16000`), `maxRefs` (`default 400`), `includeValues` (`true`|`false`), `scope` (`page`).
```json
{
  "generation": 2,
  "snapshotId": "s8f9e0d1c2b3a4",
  "url": "https://example.com/form",
  "title": "Example Form",
  "text": "[document] \"Example Form\"\n  [textbox] \"Full name\" @e1\n  [button] \"Submit\" @e2\n",
  "truncated": false,
  "refCount": 2
}
```

### `POST /v1/sessions/{id}/tabs/{tab}/find`
Searches the tab's accessibility snapshot for `@eN` elements matching `query` and/or `role` (auto-snapshotting if needed).
```json
{
  "query": "Submit",
  "role": "button",
  "actionableOnly": true,
  "limit": 10
}
```

### `GET /v1/sessions/{id}/tabs/{tab}/extract`
Query parameters: `maxChars` (`default 12000`), `maxLinks` (`default 50`). Returns clean visible text, `h1-h3` headings, deduplicated `[text, href]` links, and scroll geometry.

---

## 6. Humanized Actions (`POST /v1/sessions/{id}/tabs/{tab}/actions`)

`kind` discriminates the action (`click`, `fill`, `hover`, `scroll`, `press`, `select`, `check`, `uncheck`, `drag`, `upload`):

```json
{
  "kind": "fill",
  "snapshotId": "s8f9e0d1c2b3a4",
  "ref": "e1",
  "text": "Ada Lovelace",
  "clearExisting": true
}
```

| `kind` | Required Fields | Optional Fields |
| --- | --- | --- |
| `click` | `snapshotId`, `ref` | `button` (`left`\|`right`\|`middle`), `clickCount` (`1..3`), `generation`, `timeoutMs` |
| `fill` | `snapshotId`, `ref`, `text` | `clearExisting` (`bool`), `generation`, `timeoutMs` |
| `hover` | `snapshotId`, `ref` | `generation`, `timeoutMs` |
| `scroll` | *(none)* | `direction` (`down`\|`up`\|`left`\|`right`), `amount` (`1..10000`), `snapshotId`, `ref` |
| `press` | `key` (e.g. `Enter`, `Control+a`) | `snapshotId`, `ref`, `timeoutMs` |
| `select` | `snapshotId`, `ref`, `value` | `timeoutMs` |
| `check` / `uncheck` | `snapshotId`, `ref` | `timeoutMs` |
| `drag` | `snapshotId`, `ref`, `targetRef` | `timeoutMs` |
| `upload` | `snapshotId`, `ref`, `files` (`[]string`) | `timeoutMs` |

---

## 7. Evaluation, Storage, Cookies, Dialogs, Emulation, PDF & Telemetry

- **`POST /v1/sessions/{id}/tabs/{tab}/evaluate`**: `{"expression": "el => el.value", "snapshotId": "s...", "ref": "e1"}`
- **`GET/POST/DELETE /v1/sessions/{id}/tabs/{tab}/storage`**: Inspect, seed (`{"area": "local", "action": "set", "entries": {"k": "v"}}`), or clear `localStorage` / `sessionStorage`.
- **`GET/POST/DELETE /v1/sessions/{id}/tabs/{tab}/cookies`**: Inspect, seed (origin-checked against policy), or clear browser cookies.
- **`GET/POST /v1/sessions/{id}/tabs/{tab}/dialogs`**: Inspect captured JS `alert`/`confirm`/`prompt` dialogs or configure the next dialog handler (`{"accept": true, "promptText": "yes"}`).
- **`POST /v1/sessions/{id}/tabs/{tab}/emulate`**: Dynamically update viewport dimensions, `colorScheme` (`"dark"`\|`"light"`), `reducedMotion`, or `offline`.
- **`GET /v1/sessions/{id}/tabs/{tab}/screenshot`**: Returns raw `image/png` viewport screenshot bytes.
- **`GET/POST /v1/sessions/{id}/tabs/{tab}/pdf`**: Returns raw `application/pdf` bytes (`{"landscape": false, "printBackground": true, "scale": 1.0}`).
- **`GET /v1/sessions/{id}/tabs/{tab}/telemetry`**: Returns ring-buffered `console`, `network`, and `dialogs` arrays.
- **`GET /v1/sessions/{id}/tabs/{tab}/persona`**: Returns the live `PersonaReport` (`consistent: true`, `mismatches: []`).

---

## 8. Companion 2nd Cursor (`/v1/companion`)

- **`GET /v1/companion`**: Returns live `CompanionState` (`visible`, `x`, `y`, `mood`, `message`, `subtext`, `lastSelection`).
- **`POST /v1/companion`**: Guides, moves, shows, hides, or clears `Ni-Cursor` on Wayland/Hyprland and/or inside a browser tab.
- **`POST /v1/companion/selection`**: Analyzes highlighted text (`{"text": "...", "windowClass": "discord", "guide": true}`) or reads live Wayland primary selection (`{"readPrimary": true}`).
