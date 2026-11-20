# XT-Nichrome Installation & MCP Setup Guide

This guide covers building XT-Nichrome from source, generating the isolated Windows 11 font pack, compiling the optional Wayland/Hyprland 2nd companion cursor (`Ni-Cursor`), and connecting XT-Nichrome as a Model Context Protocol (MCP) server in **Antigravity**, **Claude Desktop**, **Cursor**, **Windsurf**, **VS Code / Cline**, or over **Streamable HTTP (`/mcp`)**.

---

## 1. System Prerequisites

| Component | Requirement | Purpose |
| --- | --- | --- |
| **OS** | Linux (`x86_64` or `arm64`) | Host platform for the daemon, policy proxy, and Chromium |
| **Go** | Go `1.25+` | Builds `nichrome`, `nichrome-mcp`, `nichrome-cli`, `nichrome-cursor`, `fixture-server`, and `detect-server` |
| **Browser** | Pinned Playwright Chromium (`chromium-1194` / Chrome `141.0.7390.37`) or system `google-chrome-stable` / `chromium` | Driven over `--remote-debugging-pipe` (never exposes a TCP debugging port) |
| **Python 3** | `python3` + `fonttools` (`pip install fonttools`) | Generates the 58-face Windows 11 metrically compatible font pack (`make fonts`) |
| **Wayland / Hyprland (Optional)** | `gcc`, `pkg-config`, `gtk+-3.0`, `gtk-layer-shell-0`, `cairo`, `pangocairo`, `wl-clipboard` (`wl-paste`), `hyprctl` | Compiles and drives the click-through desktop 2nd companion cursor (`bin/nichrome-cursor-wayland`) |

### Installing Prerequisites

#### Arch Linux / Hyprland
```bash
sudo pacman -S --needed go python python-fonttools gtk3 gtk-layer-shell cairo pango wl-clipboard
npx playwright@1.56.1 install chromium
```

#### Ubuntu / Debian (24.04+)
```bash
sudo apt-get update
sudo apt-get install -y golang-go python3 python3-fonttools fontconfig \
  build-essential pkg-config libgtk-3-dev libgtk-layer-shell-dev libcairo2-dev libpango1.0-dev wl-clipboard
npx playwright@1.56.1 install chromium
```

---

## 2. Building All Binaries & Font Pack

From the repository root (`/home/xtrcom/projects/XT-Nichrome`):

```bash
# 1. Build all binaries into ./bin/
make build cli mcp cursor fixture detect

# 2. Generate the isolated Windows 11 font pack (58 TTF faces) into ~/.local/share/xt-nichrome/fonts/w11
make fonts
```

This produces:
- `bin/nichrome` — Hardened REST + Streamable HTTP (`/mcp`) browser daemon
- `bin/nichrome-mcp` — Standalone `stdio` MCP server (35 tools, 4 resources, 3 prompts)
- `bin/nichrome-cli` — Shell/script CLI client for driving `bin/nichrome`
- `bin/nichrome-cursor` & `bin/nichrome-cursor-wayland` — Native Wayland/Hyprland 2nd companion cursor (`Ni-Cursor`) with real-time highlight/selection responses
- `bin/fixture-server` — Deterministic local test fixture server
- `bin/detect-server` — 28-probe adversarial bot-detection & behavioral telemetry benchmark server

Verify your build and installation:
```bash
make unit
make e2e
```

---

## 3. Configuring XT-Nichrome as an MCP Server

XT-Nichrome supports two MCP transports:
1. **Stdio Transport (`bin/nichrome-mcp`) — Recommended for local IDE & desktop agents:** Launches its own in-process session manager and mandatory egress proxy, speaking JSON-RPC 2.0 over `stdin`/`stdout`.
   - **Out-of-the-Box Public Web Mode:** When `-allow` is omitted (or set to `"*"`), `nichrome-mcp` permits any public `http`/`https` website on ports `80`/`443` while **still enforcing full DNS/IP SSRF protection** (blocking loopback `127.0.0.0/8`, RFC1918 private LANs, and cloud metadata `169.254.169.254`).
   - **Strict Origin Allowlist Mode:** Pass `-allow "https://example.com,https://github.com"` (or `-strict-allow`) to restrict navigation exclusively to listed origins.
2. **Streamable HTTP Transport (`bin/nichrome -mcp-http`) — Recommended for shared or remote daemons:** Mounts `/mcp` on the HTTP daemon behind constant-time `Authorization: Bearer <token>` authentication.

### A. Antigravity / Claude Desktop / Cursor / Windsurf (`mcp.json`)

Add the following entry to your MCP configuration file:

#### Option 1: General Public Web Browsing (SSRF-Protected)
```json
{
  "mcpServers": {
    "xt-nichrome": {
      "command": "/home/xtrcom/projects/XT-Nichrome/bin/nichrome-mcp",
      "args": [
        "-persona", "win11-chrome-141"
      ]
    }
  }
}
```

#### Option 2: Strict Origin Allowlist Mode
```json
{
  "mcpServers": {
    "xt-nichrome": {
      "command": "/home/xtrcom/projects/XT-Nichrome/bin/nichrome-mcp",
      "args": [
        "-persona", "win11-chrome-141",
        "-allow", "https://example.com,https://news.ycombinator.com,https://github.com"
      ]
    }
  }
}
```

#### Option 3: Local Development (Allowing Loopback Dev Servers)
```json
{
  "mcpServers": {
    "xt-nichrome": {
      "command": "/home/xtrcom/projects/XT-Nichrome/bin/nichrome-mcp",
      "args": [
        "-persona", "win11-chrome-141",
        "-allow-public",
        "-allow", "http://127.0.0.1:3000,http://localhost:5173",
        "-allow-private"
      ]
    }
  }
}
```

### B. Streamable HTTP MCP Endpoint (`/mcp`)

1. Start the daemon with `-mcp-http`:
   ```bash
   ./bin/nichrome -listen 127.0.0.1:7788 -allow-public -mcp-http
   ```
2. Read the generated bearer token:
   ```bash
   ./bin/nichrome -print-token
   ```
3. Point any Streamable HTTP MCP client at `http://127.0.0.1:7788/mcp` with header:
   ```http
   Authorization: Bearer <YOUR_TOKEN>
   ```

---

## 4. Optional: Wayland / Hyprland 2nd Companion Cursor (`Ni-Cursor`)

On Wayland compositors supporting `wlr-layer-shell` (Hyprland, Sway), `bin/nichrome-cursor` renders a 100% click-through 2nd mini-cursor with floating chat bubbles and mood expressions (`friendly`, `curious`, `warn`, `sad`, `excited`, `thinking`).

### On-Demand Highlight / Selection Mode (Recommended)
Stays **completely hidden** until you highlight text in Discord, a browser, or a terminal (`wl-paste --primary`), then glides to your cursor, replies in real time, and **automatically hides after 6 seconds**:

```bash
./bin/nichrome-cursor -listen 127.0.0.1:7798 -watch-selection -auto-hide 6s
```

### Hyprland Keybind Integration (`~/.config/hypr/hyprland.conf`)
Bind a key (e.g. `SUPER + N`) to analyze whatever text you currently have highlighted on screen without running a continuous background watcher:

```ini
# Pop up Ni-Cursor for 5 seconds to analyze or reply to the currently highlighted text:
bind = $mainMod, N, exec, /home/xtrcom/projects/XT-Nichrome/bin/nichrome-cursor -duration 5s -say "$(wl-paste --primary --no-newline | head -c 140)"
```

---

## 5. Environment Variables Reference

| Variable | Default | Description |
| --- | --- | --- |
| `XT_NICHROME_STATE` | `~/.local/share/xt-nichrome` | State directory holding `token`, `profiles/`, `fonts.conf`, and `fontcache/` |
| `XT_NICHROME_BROWSER` | Auto-detected Playwright Chromium | Explicit path to the Chromium binary |
| `XT_NICHROME_ALLOW` | `""` | Comma-separated allowed origins (or `*` for public web) |
| `XT_NICHROME_ALLOW_PUBLIC` | `0` (`1` when `nichrome-mcp` has empty `-allow`) | Permit any public `http`/`https` origin on ports `80`/`443` with SSRF protection |
| `XT_NICHROME_URL` | `http://127.0.0.1:7788` | Default daemon URL for `nichrome-cli` |
| `XT_NICHROME_TOKEN` | Read from `$XT_NICHROME_STATE/token` | Bearer token for `nichrome-cli` |
