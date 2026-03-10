# CGM Get Agent — Quick Start

Connect your Dexcom G7 CGM to Claude Desktop or ChatGPT Desktop in about 15 minutes.

---

## What This Does

CGM Get Agent runs as a local Docker container that exposes your Dexcom G7 data as MCP tools. Once connected, you can ask Claude or ChatGPT questions like:

- *"What's my glucose right now and where is it headed?"*
- *"I just had a bowl of oatmeal — log it and tell me what I'm starting from."*
- *"How did that burrito I had at lunch hit me?"*
- *"Did my CGM alarm go off last night?"*

---

## Prerequisites

| Requirement | Notes |
|---|---|
| macOS (Apple Silicon) | Intel Mac works with minor Dockerfile changes |
| [Colima](https://github.com/abiosoft/colima) + Docker CLI | `brew install colima docker docker-compose` |
| Dexcom G7 + iOS app | Active sensor and Dexcom cloud account required |
| [Dexcom developer account](https://developer.dexcom.com) | Free to register |
| Node.js | For Claude Desktop's `mcp-remote` bridge (`brew install node`) |
| Claude Desktop or ChatGPT Desktop | Any recent version with MCP support |

---

## Step 1 — Register a Dexcom Developer App

1. Sign in at [developer.dexcom.com](https://developer.dexcom.com).
2. Create a new application.
3. Set the **Redirect URI** to: `http://localhost:8090/callback`
   > **Using a different port?** The installer will ask for your port and set the redirect URI automatically. Make sure the Dexcom developer portal Redirect URI matches exactly.
4. Copy your **Client ID** and **Client Secret** — the installer will prompt for them.

> **Sandbox vs. Production**: The Dexcom sandbox provides simulated G7 data with no real CGM required. Start with sandbox to verify everything works, then switch to production for live readings.

---

## Step 2 — Install

```bash
git clone https://github.com/johnmartinez/cgm-get-agent.git
cd cgm-get-agent
make install
```

The installer will:
1. Check prerequisites (Docker, Colima, Node.js)
2. Ask for your environment (sandbox or production)
3. Prompt for your Dexcom Client ID and Client Secret
4. Auto-generate an AES-256 encryption key
5. Write your `.env` file
6. Create the data directory (`~/.cgm-get-agent/`)
7. Build and start the Docker container
8. Run a health check

When it finishes, follow the printed next steps.

### Manual Setup (fallback)

If you prefer not to use the installer, see the [Manual Setup section in README.md](README.md#manual-setup).

---

## Step 3 — Authorize Dexcom (One-Time)

```bash
make auth
```

Or open **http://localhost:8090/oauth/start** in your browser.

Log in to your Dexcom account and complete the HIPAA authorization screen. You'll be redirected back with a success message.

Verify authorization:

```bash
make health
```

Expected response:

```json
{"status": "ok", "dexcom_auth": "valid", "db_accessible": true, "uptime_seconds": 42}
```

OAuth tokens are encrypted with AES-256-GCM and stored at `~/.cgm-get-agent/tokens.enc`.

---

## Step 4 — Connect Claude Desktop

Claude Desktop only supports stdio transport in its local config — it cannot connect to an SSE endpoint directly. Use `mcp-remote` (via npx) as a stdio-to-SSE bridge. This is the primary and recommended method.

Edit Claude Desktop's MCP config file:

**`~/Library/Application Support/Claude/claude_desktop_config.json`**

```json
{
  "mcpServers": {
    "cgm-get-agent": {
      "command": "npx",
      "args": [
        "mcp-remote",
        "http://localhost:8090/sse",
        "--transport",
        "sse-only"
      ]
    }
  }
}
```

Restart Claude Desktop. You should see **cgm-get-agent** appear in the tools panel (the hammer icon).

> **Claude Code CLI (SSE, no bridge needed):**
> ```bash
> claude mcp add --transport sse cgm-get-agent http://localhost:8090/sse
> ```

> **stdio transport (experimental):** Runs a fresh server process per session with no HTTP layer. Lower latency but less tested:
> ```bash
> claude mcp add cgm-get-agent -- docker exec -e GA_MCP_TRANSPORT=stdio -i cgm-get-agent cgm-get-agent serve
> ```

---

## Step 5 — Connect ChatGPT Desktop

ChatGPT Desktop supports MCP via SSE. To add the server:

1. Open **ChatGPT Desktop** → **Settings** → **Connectors** (or **MCP Servers**).
2. Add a new server with URL: `http://localhost:8090/sse`
3. Name it `cgm-get-agent`.
4. Save and restart ChatGPT Desktop.

> If your version of ChatGPT Desktop uses a config file instead of the UI, create or edit:
> **`~/Library/Application Support/ChatGPT/mcp.json`**
> ```json
> {
>   "servers": [
>     {
>       "name": "cgm-get-agent",
>       "transport": "sse",
>       "url": "http://localhost:8090/sse"
>     }
>   ]
> }
> ```

---

## Test It

Try these prompts after connecting:

```
What's my glucose right now?
```

```
I just had two slices of sourdough toast with peanut butter. Log it and show me my current trend.
```

```
Did my Dexcom alarm go off at any point last night?
```

```
How did the meal I logged at lunch impact my glucose? Give it a rating.
```

---

## Upgrading

When new code is available:

```bash
cd cgm-get-agent
git pull
make upgrade
```

The upgrade preserves your data (`~/.cgm-get-agent/`), `.env` configuration, and OAuth tokens. Only the Docker image is rebuilt from the latest code.

### After Upgrading

1. `make status` — verify the container is running
2. `make health` — verify the server is healthy
3. Restart Claude Desktop to reconnect MCP
4. Test: ask Claude *"What Dexcom sensor am I wearing?"*

---

## Available Tools

| Tool | Description |
|---|---|
| `get_current_glucose` | Current reading + trend + optional history window |
| `get_glucose_history` | Historical EGVs for a date range (max 30 days) |
| `get_trend` | Trend arrow, rate of change, and glucose zone |
| `get_dexcom_events` | Events logged in the G7 app (carbs, insulin, exercise, health) |
| `get_calibrations` | Fingerstick calibration records |
| `get_alerts` | CGM alert history (high, low, urgent low, rise, fall, etc.) |
| `get_devices` | G7 transmitter and display device info |
| `get_data_range` | Earliest/latest timestamps for each data type |
| `log_meal` | Log a meal locally with optional macro estimates |
| `log_exercise` | Log an exercise session locally |
| `rate_meal_impact` | Analyze glucose impact of a logged meal (1–10 rating) |

---

## Troubleshooting

**"Tool result too large" error in Claude**
Use shorter time windows for `get_glucose_history` and `get_dexcom_events`. Instead of 30 days, try 1–3 days.

**SSE session drops / tools stop responding**
Restart Claude Desktop. The mcp-remote bridge reconnects automatically. If the issue persists, check `make logs` for SSE keepalive errors.

**OAuth token expired**
```bash
make auth
```
This opens the Dexcom authorization page. Re-authorize and your tokens will be refreshed.

**Container not starting**
```bash
colima status          # is Colima running?
colima start --arch aarch64 --vm-type vz   # start if needed
make logs              # check container logs for errors
```

**After upgrade, tools not responding**
Restart Claude Desktop to reconnect MCP. The mcp-remote bridge caches the old connection.

**Health shows `dexcom_auth: not_configured`**
Run `make auth` to complete the one-time Dexcom authorization.

**Health shows `dexcom_auth: expired`**
Your refresh token expired (rare). Run `make auth` to re-authorize.

**Tools show stale data notice**
Normal for US mobile users — the Dexcom cloud has a ~1 hour delay for G7 data uploaded via iOS. Data uploaded from a Dexcom receiver via USB arrives immediately.

**Port 8090 already in use**
Re-run `make install` with a different port, or manually edit `.env`:
```bash
GA_SERVER_PORT=9090
GA_DEXCOM_REDIRECT_URI=http://localhost:9090/callback
```
Update your Dexcom developer app's Redirect URI to match.

---

## Switching to Production

When you're ready to use live CGM data:

1. Register a **production** app at [developer.dexcom.com](https://developer.dexcom.com) with redirect URI matching `GA_DEXCOM_REDIRECT_URI`.
2. Update `.env`:
   ```bash
   GA_DEXCOM_CLIENT_ID=your-production-client-id
   GA_DEXCOM_CLIENT_SECRET=your-production-client-secret
   GA_DEXCOM_ENV=production
   ```
3. Rebuild and re-authorize:
   ```bash
   make rehup
   make auth
   ```
