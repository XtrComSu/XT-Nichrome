# XT-Nichrome MCP Server

XT-Nichrome ships with a first-class Model Context Protocol (MCP) server built on `github.com/modelcontextprotocol/go-sdk` v1.8.0. It wraps `sessions.Manager` directly in-process — no intermediate HTTP client hop — and supports both **stdio** (`cmd/nichrome-mcp`) and **Streamable HTTP** (`nichrome -mcp-http` mounted at `/mcp`).

## Transports

### 1. Stdio (`bin/nichrome-mcp`)

Launch `nichrome-mcp` directly from any MCP host (Claude Desktop, Cursor, Antigravity, or custom MCP clients). It starts the local egress policy proxy and manages the Chromium pool for the lifetime of the stdio connection:

```json
{
  "mcpServers": {
    "xt-nichrome": {
      "command": "/path/to/bin/nichrome-mcp",
      "args": [
        "-allow", "https://example.com,https://api.example.com",
        "-persona", "win11-chrome-141"
      ]
    }
  }
}
```

### 2. Streamable HTTP (`bin/nichrome -mcp-http`)

Pass `-mcp-http` to the main `nichrome` daemon to mount the MCP Streamable HTTP endpoint at `/mcp` alongside `/v1/*`. Requests to `/mcp` require the same `Authorization: Bearer <token>` header as the REST routes:

```sh
./bin/nichrome -listen 127.0.0.1:7788 -allow https://example.com -mcp-http
```

## Tools (13)

| Tool | Required Inputs | Optional Inputs | Purpose |
| --- | --- | --- | --- |
| `browser_open_session` | — | `mode`, `profileId`, `persona` | Open an `ephemeral` or `persistent` browser session; sets the active `sessionId` for this MCP client. |
| `browser_close_session` | — | `sessionId` | Tear down a session, close its tabs, and release its capacity slot / profile lock. |
| `browser_list_sessions` | — | — | List active sessions and report the remembered `activeSessionId`. |
| `browser_open_tab` | — | `sessionId`, `url` | Open a tab, verify the stealth persona, optionally navigate to `url`, and set the active `tabId`. |
| `browser_close_tab` | — | `sessionId`, `tabId` | Close a tab and invalidate its snapshots. |
| `browser_list_tabs` | — | `sessionId` | List open tabs in a session with URLs, titles, and document generations. |
| `browser_navigate` | `url` | `sessionId`, `tabId`, `waitUntil`, `timeoutMs` | Navigate a tab to an allowlisted URL (`load` or `commit`), invalidating previous snapshots. |
| `browser_snapshot` | — | `sessionId`, `tabId`, `maxChars`, `maxRefs`, `includeValues`, `scope` | Render a compact accessibility tree with `@eN` refs, returning `snapshotId` and `generation`. |
| `browser_click` | `ref` | `sessionId`, `tabId`, `snapshotId`, `generation`, `button`, `clickCount`, `timeoutMs` | Click an `@eN` ref using a humanized Bézier mouse trajectory and realistic press dwell. |
| `browser_fill` | `ref`, `text` | `sessionId`, `tabId`, `snapshotId`, `generation`, `clearExisting`, `timeoutMs` | Click to focus an `@eN` input and type `text` via virtualized per-character keystrokes. |
| `browser_screenshot` | — | `sessionId`, `tabId` | Capture a viewport PNG returned as an MCP `image/png` content block. |
| `browser_persona` | — | `sessionId`, `tabId` | Inspect the live tab's observable stealth surface and consistency report. |
| `browser_ready` | — | — | Report readiness, active sessions/tabs, capacity limits, and egress denial counts. |

## Session, Tab & Snapshot Affinity

Every connected MCP client session tracks its own remembered `sessionId`, `tabId` (per session), and `snapshotId` (per tab):
- Calling `browser_open_session` sets the default `sessionId`.
- Calling `browser_open_tab` sets the default `tabId` for that session.
- Calling `browser_snapshot` sets the default `snapshotId` for that tab.
- Subsequent calls to `browser_navigate`, `browser_snapshot`, `browser_click`, `browser_fill`, `browser_screenshot`, and `browser_persona` can omit `sessionId`, `tabId`, and `snapshotId` entirely, or pass explicit IDs to target a different tab or session.
- Runaway agents are bounded by a per-MCP-client open-tab cap (`DefaultMaxTabsPerClient = 8`) in addition to the daemon's per-session tab quota.

## Actionable Error Mapping & Ambiguous Outcomes

Every `types.APIError` code is mapped to an MCP tool error (`IsError: true`) that tells the agent what to do next:
- `STALE_REF`: Instructs the agent to call `browser_snapshot` for fresh `@eN` handles before retrying.
- `ELEMENT_NOT_ACTIONABLE`: Explains role/action mismatch (e.g., use `browser_click` on checkboxes, `browser_fill` on textboxes) or element occlusion.
- `POLICY_BLOCKED`: Explains that the destination origin is outside `-allow` and should not be retried.
- `CAPACITY` / `BUSY_PROFILE`: Instructs the agent to close unused tabs/sessions or wait for in-flight operations.
- `ACTION_OUTCOME_UNKNOWN`: When a click or fill is dispatched to the browser and the result is lost before completion, the tool returns a **non-failure warning** (`status: "unknown"`, `outcome: "unknown"`, `verifyBeforeRetry: true`, `freshSnapshotRequired: true`) instructing the agent to inspect the page with `browser_snapshot` before retrying so it never blindly double-submits a form.
