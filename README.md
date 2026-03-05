# CGM Get Agent

An MCP (Model Context Protocol) server that connects LLMs (Claude, ChatGPT) to a Dexcom G7 continuous glucose monitor. Ask Claude or ChatGPT about your glucose in natural language and get personalized health guidance on meals and exercise.

## What It Does

- Exposes all six Dexcom API v3 read endpoints as MCP tools (EGVs, events, calibrations, alerts, devices, data range)
- Logs meals and exercise locally with LLM-estimated macros
- Correlates meals against post-meal glucose response curves and rates impact 1–10
- Works with Claude (via MCP/SSE or stdio) and ChatGPT (via REST shim)
- Degrades gracefully when Dexcom is unavailable using a local glucose cache

## Stack

- **Go 1.24** — single binary, CGO for SQLite
- **MCP** — primary protocol (`github.com/modelcontextprotocol/go-sdk/mcp`), SSE + stdio transports
- **REST shim** — OpenAI function-calling compatibility at `/v1/tools/invoke`
- **Dexcom API v3** — OAuth2, EGV/event data from Dexcom G7
- **SQLite** — local storage for meals, exercise, and glucose cache (`mattn/go-sqlite3`)
- **AES-256-GCM** — OAuth tokens encrypted at rest
- **Docker + Colima** — arm64-native, runs on macOS/Apple Silicon

## Prerequisites

- macOS (Apple Silicon recommended)
- [Colima](https://github.com/abiosoft/colima) + Docker CLI (`brew install colima docker docker-compose`)
- A [Dexcom Developer account](https://developer.dexcom.com/) — create an app to get `client_id` and `client_secret`
- Go 1.24+ (for local development only; not needed for Docker builds)

## Quick Start

### 1. Clone and configure

```bash
git clone https://github.com/johnmartinez/cgm-get-agent
cd cgm-get-agent

cp .env.example .env
```

Edit `.env` with your Dexcom credentials and a generated encryption key:

```bash
# Generate a 32-byte AES encryption key
openssl rand -hex 32
# Paste the output into .env as GA_ENCRYPTION_KEY
```

### 2. Start with Docker Compose

```bash
colima start --arch aarch64 --vm-type vz  # start Colima (first time)
docker compose up --build
```

The server starts on `http://localhost:8080`.

### 3. Authorize Dexcom (one-time)

Open in your browser:

```
http://localhost:8080/oauth/start
```

You'll be redirected to Dexcom to log in and grant HIPAA consent. After completing the flow, tokens are encrypted and stored locally. You will not need to repeat this unless tokens are revoked.

Verify auth status:

```bash
curl http://localhost:8080/health
# {"status":"ok","dexcom_auth":"valid","db_accessible":true,"uptime_seconds":14}
```

### 4. Connect to Claude

**Option A — SSE (recommended for claude.ai or Claude desktop):**

```bash
claude mcp add --transport sse cgm-get-agent http://localhost:8080/mcp
```

**Option B — stdio (for Claude Code CLI, lower latency):**

```bash
claude mcp add cgm-get-agent -- docker exec -i cgm-get-agent cgm-get-agent serve --transport stdio
```

Now ask Claude: *"What's my glucose right now?"*

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `GA_DEXCOM_CLIENT_ID` | **Yes** | — | Dexcom developer app client ID |
| `GA_DEXCOM_CLIENT_SECRET` | **Yes** | — | Dexcom developer app client secret |
| `GA_ENCRYPTION_KEY` | **Yes** | — | 32-byte hex-encoded AES-256 key |
| `GA_DEXCOM_ENV` | No | `sandbox` | `sandbox` or `production` |
| `GA_DEXCOM_REDIRECT_URI` | No | `http://localhost:8080/callback` | OAuth redirect URI — change if port 8080 is in use |
| `GA_MCP_TRANSPORT` | No | `sse` | `sse` or `stdio` |
| `GA_SERVER_PORT` | No | `8080` | HTTP listen port |
| `GA_DB_PATH` | No | `/data/data.db` | SQLite database path |
| `GA_TOKEN_PATH` | No | `/data/tokens.enc` | Encrypted token file path |
| `GA_CONFIG_PATH` | No | `/data/config.yaml` | Optional YAML config override |

> **Port conflict?** If port 8080 is already in use, set both `GA_SERVER_PORT` and `GA_DEXCOM_REDIRECT_URI` together in `.env` and update your Dexcom developer app's Redirect URI to match:
> ```bash
> GA_SERVER_PORT=8090
> GA_DEXCOM_REDIRECT_URI=http://localhost:8090/callback
> ```

See `.env.example` for a template.

## Available MCP Tools

### Dexcom Read Tools (live data from Dexcom API v3 — read-only)

| Tool | Description |
|---|---|
| `get_current_glucose` | Current reading + trend + optional history window |
| `get_glucose_history` | EGV records for a date range (max 30-day window) |
| `get_trend` | Lightweight trend arrow and rate of change |
| `get_dexcom_events` | Carbs, insulin, exercise, health events logged in the G7 app |
| `get_calibrations` | Fingerstick calibration records from the G7 |
| `get_alerts` | Alert history: high, low, urgent low, rise, fall, out-of-range |
| `get_devices` | G7 transmitter and display device info |
| `get_data_range` | Earliest/latest timestamps for each Dexcom data type |

### Local Tools (SQLite-backed, work offline)

| Tool | Description |
|---|---|
| `log_meal` | Log a meal locally with description and estimated macros |
| `log_exercise` | Log an exercise session with type, duration, intensity |
| `rate_meal_impact` | Analyze glucose impact of a logged meal; 1–10 rating |

## Dexcom Sandbox (No CGM Required)

For development, use `GA_DEXCOM_ENV=sandbox` (default). The Dexcom sandbox provides simulated G7 data. Sandbox login does not require a real Dexcom account password.

## Production Switch

```bash
# In .env:
GA_DEXCOM_ENV=production

docker compose up --build
open http://localhost:8080/oauth/start   # re-authorize with real credentials
```

## Data & Privacy

- All health data stays on your local machine.
- OAuth tokens are AES-256-GCM encrypted in `~/.cgm-get-agent/tokens.enc`.
- The host volume `~/.cgm-get-agent` should be `chmod 700`.
- No data is transmitted to any third party other than the Dexcom API.
- Never expose port 8080 directly to the internet. Use Tailscale or WireGuard for remote access.

## Development

```bash
# Run tests
go test ./...

# Run locally (no Docker)
GA_DEXCOM_ENV=sandbox \
GA_ENCRYPTION_KEY=$(openssl rand -hex 32) \
GA_MCP_TRANSPORT=sse \
go run ./cmd/server

# Build binary
CGO_ENABLED=1 go build -o cgm-get-agent ./cmd/server
```

## Architecture

See `docs/architecture.mermaid` and `docs/workflow.mermaid` for system and sequence diagrams.

Full specification: [SPEC.md](SPEC.md)

Implementation instructions: [CLAUDE.md](CLAUDE.md)

## Project Status

Active development — spec-driven build. See `CLAUDE.md` for the phased implementation plan and progress.

## License

TBD
