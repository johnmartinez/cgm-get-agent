# CGM Get Agent

An MCP (Model Context Protocol) server that connects LLMs (Claude, ChatGPT) to a Dexcom G7 continuous glucose monitor. Ask Claude or ChatGPT about your glucose in natural language and get personalized health guidance on meals and exercise.

## What It Does

- Exposes all six Dexcom API v3 read endpoints as MCP tools (EGVs, events, calibrations, alerts, devices, data range)
- Logs meals and exercise locally with LLM-estimated macros
- Correlates meals against post-meal glucose response curves and rates impact 1–10
- Works with Claude (via MCP/SSE or stdio) and ChatGPT (via REST shim)
- Degrades gracefully when Dexcom is unavailable using a local glucose cache

## Quick Start

```bash
git clone https://github.com/johnmartinez/cgm-get-agent
cd cgm-get-agent
make install
```

The installer will walk you through prerequisites, Dexcom credentials, and container setup. When it finishes, follow the printed instructions to authorize Dexcom and connect Claude.

See [QUICKSTART.md](QUICKSTART.md) for the full step-by-step guide.

## Upgrading

```bash
cd cgm-get-agent
git pull
make upgrade
```

Your data (`~/.cgm-get-agent/`), `.env` configuration, and OAuth tokens are preserved. Only the Docker image is rebuilt. Restart Claude Desktop after upgrading to reconnect MCP.

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
- Node.js (for Claude Desktop's `mcp-remote` bridge — `brew install node`)

## Makefile Targets

| Target | Description |
|---|---|
| `make install` | Run interactive installer (auto-detects fresh vs upgrade) |
| `make upgrade` | Upgrade existing install (preserve data, rebuild container) |
| `make build` | Build and start container (`docker compose up --build -d`) |
| `make start` | Start container without rebuilding |
| `make stop` | Stop and remove container |
| `make restart` | Stop, rebuild, and start |
| `make logs` | Tail container logs |
| `make health` | Check server health endpoint |
| `make auth` | Open OAuth authorization page in browser |
| `make rehup` | Quick rebuild: stop, rebuild, start, health check |
| `make clean-warn` | List artifacts that need manual cleanup |
| `make status` | Show container state, version, health |
| `make help` | List all targets |

## Development Workflow

For active development with Claude Code, use `make rehup` after merging PRs to quickly rebuild and test without the full install interaction:

```bash
# After merging a PR
git checkout main && git pull
make rehup
```

## Manual Setup

If you prefer not to use the installer:

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
mkdir -p ~/.cgm-get-agent && chmod 700 ~/.cgm-get-agent
colima start --arch aarch64 --vm-type vz  # start Colima (first time)
docker compose up --build -d
```

The server starts on `http://localhost:8090`.

### 3. Authorize Dexcom (one-time)

Open in your browser:

```
http://localhost:8090/oauth/start
```

Verify auth status:

```bash
curl http://localhost:8090/health
```

### 4. Connect to Claude

**Claude Code CLI (SSE, recommended):**

```bash
claude mcp add --transport sse cgm-get-agent http://localhost:8090/sse
```

**Claude Desktop (requires mcp-remote bridge):**

Add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

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

**stdio (lowest latency, no HTTP):**

```bash
claude mcp add cgm-get-agent -- docker exec -i cgm-get-agent cgm-get-agent serve --transport stdio
```

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `GA_DEXCOM_CLIENT_ID` | **Yes** | — | Dexcom developer app client ID |
| `GA_DEXCOM_CLIENT_SECRET` | **Yes** | — | Dexcom developer app client secret |
| `GA_ENCRYPTION_KEY` | **Yes** | — | 32-byte hex-encoded AES-256 key |
| `GA_DEXCOM_ENV` | No | `sandbox` | `sandbox` or `production` |
| `GA_DEXCOM_REDIRECT_URI` | No | `http://localhost:8090/callback` | OAuth redirect URI — must match `GA_SERVER_PORT` |
| `GA_MCP_TRANSPORT` | No | `sse` | `sse` or `stdio` |
| `GA_SERVER_PORT` | No | `8090` | HTTP listen port |
| `GA_DB_PATH` | No | `/data/data.db` | SQLite database path |
| `GA_TOKEN_PATH` | No | `/data/tokens.enc` | Encrypted token file path |
| `GA_LOG_LEVEL` | No | `2` | Log verbosity: 1=ERROR, 2=INFO, 3=DEBUG |
| `GA_CONFIG_PATH` | No | `/data/config.yaml` | Optional YAML config override |

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

## Data & Privacy

- All health data stays on your local machine.
- OAuth tokens are AES-256-GCM encrypted in `~/.cgm-get-agent/tokens.enc`.
- The host volume `~/.cgm-get-agent` should be `chmod 700`.
- No data is transmitted to any third party other than the Dexcom API.
- Never expose port 8090 directly to the internet. Use Tailscale or WireGuard for remote access.

## Architecture

See `docs/architecture.mermaid` and `docs/workflow.mermaid` for system and sequence diagrams.

Full specification: [SPEC.md](SPEC.md)

Implementation instructions: [CLAUDE.md](CLAUDE.md)

## Project Status

Active development — spec-driven build. See `CLAUDE.md` for the phased implementation plan and progress.

## License

TBD
