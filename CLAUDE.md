# CLAUDE.md — Implementation Instructions for CGM Get Agent

## Project Identity

**CGM Get Agent** — a Go MCP server that bridges LLMs (Claude, ChatGPT) to the Dexcom G7 CGM via Dexcom Developer API v3. Single binary, single Docker container, runs on macOS/Apple Silicon via Colima.

Canonical spec: `SPEC.md`. This file is the source of truth. Never deviate from it without noting the deviation here.

---

## Absolute Rules

### Security (non-negotiable)
- **Never** hardcode credentials, tokens, or secrets in source code.
- **Never** commit `.env` (only `.env.example` with placeholder values).
- **Never** commit `tokens.enc`, `data.db`, `config.yaml`, or any file under `~/.cgm-get-agent/`.
- All Dexcom OAuth tokens are AES-256-GCM encrypted at rest (`internal/crypto/tokens.go`).
- Encryption key comes from `GA_ENCRYPTION_KEY` env var only (32-byte hex-encoded).
- No PHI (glucose values, meal descriptions, health data) in INFO-level logs.
- CSRF state token required for `/oauth/start` → `/callback` flow.

### Git Workflow
- Always branch from `main` before any code changes.
- Branch naming: `feat/<feature-name>`, `fix/<issue>`, `test/<scope>`.
- Commit message format: `<type>(<scope>): <summary>` (conventional commits).
- Push every branch to origin before opening PR or merging.
- Never force-push `main`.

### Code Quality
- All packages must have `_test.go` files covering core logic.
- `go vet ./...` must pass before any commit.
- No `panic()` in production paths — return errors.
- Handle the single-use refresh_token atomically (see Scenario 5 in SPEC.md).

---

## Implementation Order (dependency-safe, bottom-up)

### Phase 1 — Scaffolding (branch: `feat/scaffolding`)
Files to create:
- `go.mod` — module `github.com/yourusername/cgm-get-agent`, Go 1.22
- `.gitignore` — see Security Gitignore section below
- `.env.example` — placeholder values only
- `Dockerfile` — multi-stage, CGO enabled, arm64-native
- `docker-compose.yaml` — exactly as in SPEC §5.2
- `docs/` — already exists (mermaid diagrams)

Estimated tokens: ~3,000

### Phase 2 — Core Packages (branch: `feat/core-packages`)
Build order within phase:
1. `internal/config/config.go` — Config struct, YAML+env loading (viper or manual)
2. `internal/crypto/tokens.go` — AES-256-GCM encrypt/decrypt for OAuthTokens struct
3. `internal/store/sqlite.go` — SQLite open, migrations runner
4. `internal/store/meals.go` — Meal CRUD
5. `internal/store/exercise.go` — Exercise CRUD
6. `internal/store/cache.go` — EGV cache insert/query
7. Unit tests for all of the above

Dependencies: `mattn/go-sqlite3` (CGO), `gopkg.in/yaml.v3`

Estimated tokens: ~10,000

### Phase 3 — Dexcom Integration (branch: `feat/dexcom`)
Build order:
1. `internal/dexcom/types.go` — all types from SPEC §2.1
2. `internal/dexcom/oauth.go` — OAuth2 lifecycle: `/oauth/start`, `/callback`, `refresh_token_if_needed`, `get_valid_token`
3. `internal/dexcom/client.go` — `get_egvs`, `get_events`, `get_data_range`, `get_devices`
4. Unit tests with httptest mock server for Dexcom API

Key constraints:
- refresh_token is single-use — capture new one from every refresh response
- Token file write must be atomic (write temp file, rename)
- 30-day max window validation in `get_egvs`
- CSRF state stored in server-side map (sync.Map), validated in callback

Estimated tokens: ~8,000

### Phase 4 — Glucose Analyzer (branch: `feat/analyzer`)
File: `internal/analyzer/glucose.go`
- `classify_zone(value, config) -> string`
- `compute_snapshot(egvs, config) -> GlucoseSnapshot`
- `assess_meal_impact(meal, egvs, exercises) -> MealImpactAssessment`
- Rating table: spike_delta ≤20→10, ≤30→9, ≤40→8, ≤50→7, ≤60→6, ≤70→5, ≤80→4, ≤100→3, ≤120→2, >120→1
- data_delay_notice set when most recent EGV systemTime > 10 minutes old
- Unit tests covering all rating tiers and edge cases

Estimated tokens: ~5,000

### Phase 5 — MCP Server + REST Shim + Entrypoint (branch: `feat/mcp-server`)
Files:
1. `internal/mcp/server.go` — MCP server setup, transport selection (SSE vs stdio), tool registration
2. `internal/mcp/tools.go` — 6 tool handler functions wiring everything together
3. `internal/rest/handler.go` — POST /v1/tools/invoke, GET /health
4. `cmd/server/main.go` — parse flags/env, wire all dependencies, start server

MCP SDK: `github.com/modelcontextprotocol/go-sdk/mcp`
Transport: `GA_MCP_TRANSPORT=sse|stdio` (default: sse)

Tool error format:
```json
{"error": "DexcomAuthError", "message": "...", "retriable": false}
```

Estimated tokens: ~8,000

### Phase 6 — Test Harnesses (branch: `feat/tests`)
Scenario-based integration tests from SPEC §7:
1. `Scenario 1: Simple Glucose Check` — mock Dexcom, verify GlucoseSnapshot shape
2. `Scenario 2: Meal Logging + Glucose Context` — verify SQLite insert + glucose fetch
3. `Scenario 3: Meal Impact Rating` — verify MealImpactAssessment with known EGV curve
4. `Scenario 4: Exercise + Glucose Correlation` — verify exercise logging + history
5. `Scenario 5: OAuth Token Refresh` — verify atomic token swap, old token gone
6. `Scenario 6: Graceful Degradation` — mock Dexcom 503, verify cache fallback
7. `Scenario 7: First-Time OAuth Setup` — full flow with httptest

Estimated tokens: ~8,000

---

## Token Budget Summary

| Phase | Branch | Est. Tokens |
|-------|--------|-------------|
| 1 Scaffolding | feat/scaffolding | 3,000 |
| 2 Core Packages | feat/core-packages | 10,000 |
| 3 Dexcom Integration | feat/dexcom | 8,000 |
| 4 Glucose Analyzer | feat/analyzer | 5,000 |
| 5 MCP Server | feat/mcp-server | 8,000 |
| 6 Test Harnesses | feat/tests | 8,000 |
| CLAUDE.md + README | main | 2,000 |
| **Total** | | **~44,000** |

Context window budget per phase: keep each branch under ~30k output tokens.

---

## Security Gitignore

The `.gitignore` must include:
```
.env
*.enc
data.db
data.db-shm
data.db-wal
config.yaml
~/.cgm-get-agent/
*.pem
*.key
```

---

## Key Dependencies

```
github.com/modelcontextprotocol/go-sdk/mcp   # MCP server (official Go SDK)
github.com/mattn/go-sqlite3                   # SQLite (CGO required)
gopkg.in/yaml.v3                              # Config file parsing
golang.org/x/crypto                           # AES-GCM crypto primitives
github.com/google/uuid                        # UUID generation for IDs
```

---

## Configuration Loading Order

1. Load `GA_CONFIG_PATH` (default: `/data/config.yaml`) if file exists
2. Override all fields with environment variables (`GA_*`)
3. Apply defaults for any unset fields
4. Validate required fields (GA_DEXCOM_CLIENT_ID, GA_DEXCOM_CLIENT_SECRET, GA_ENCRYPTION_KEY)
5. Fail fast on startup if required config missing

---

## Token Encryption Spec

Algorithm: AES-256-GCM
Key: 32 bytes from GA_ENCRYPTION_KEY (hex-decoded)
Nonce: 12-byte random, prepended to ciphertext
Format: `base64(nonce || ciphertext)` in JSON
File: `/data/tokens.enc` — JSON file containing `OAuthTokens` struct fields encrypted

Atomic write pattern:
1. Marshal + encrypt tokens to bytes
2. Write to `tokens.enc.tmp`
3. `os.Rename(tokens.enc.tmp, tokens.enc)` — atomic on POSIX

---

## Dexcom API Base URLs

- Sandbox: `https://sandbox-api.dexcom.com`
- Production: `https://api.dexcom.com`

OAuth endpoints (both envs):
- Auth: `{base}/v3/oauth2/login`
- Token: `{base}/v3/oauth2/token`

Data endpoints:
- EGVs: `{base}/v3/users/self/egvs`
- Events: `{base}/v3/users/self/events`
- Data range: `{base}/v3/users/self/dataRange`
- Devices: `{base}/v3/users/self/devices`

---

## Go Module Name

`github.com/jmverleger/cgm-get-agent` (or match the actual GitHub repo URL — update if different)

---

## MCP Tool Registration Pattern

```go
mcp.AddTool(server, &mcp.Tool{
    Name:        "get_current_glucose",
    Description: "Get the current glucose reading...",
}, handleGetCurrentGlucose)
```

Input structs must use `json` and `jsonschema` tags. Return `*mcp.CallToolResult` with `IsError: true` on error.

---

## Database Migration Strategy

Use a `migrations` table. On startup:
1. Open SQLite
2. Read current version from `migrations` table (or 0 if table doesn't exist)
3. Apply migrations in order until current
4. Each migration is a hardcoded SQL string in `sqlite.go`

---

## Graceful Degradation (Scenario 6)

In `get_current_glucose` tool handler:
1. Try Dexcom API
2. On 5xx or timeout: query `glucose_cache` for records within last 30 minutes
3. If cache hit: return GlucoseSnapshot with `stale_data_notice` field set
4. If cache miss (older than 30 min): return error JSON with suggestion

---

## Health Check Endpoint

`GET /health` returns:
```json
{
  "status": "ok|degraded|error",
  "dexcom_auth": "valid|expired|not_configured",
  "db_accessible": true,
  "uptime_seconds": 123
}
```
- `not_configured`: no tokens.enc file
- `expired`: tokens exist but access_token expired and refresh failed
- `valid`: tokens exist and access_token valid (or refreshable)

---

## Notes & Gotchas

- Dexcom systemTime ≠ true UTC — it's device clock time. Use it for sequencing, not wall-clock calculations.
- G7 EGV interval is 5 minutes. Gaps > 10 min in history suggest sensor dropout.
- The `value` field in EGVRecord is the canonical glucose value (not `smoothed` or `realtime`).
- history in GlucoseSnapshot must be sorted ascending by systemTime.
- meal IDs: `m_YYYYMMDD_HHmm` — use the meal's `timestamp`, not `logged_at`.
- exercise IDs: `e_YYYYMMDD_HHmm` — same rule.
- The MCP server should support both SSE (for claude.ai) and stdio (for Claude Code CLI) in the same binary.
- Docker healthcheck uses `wget` (installed in alpine base), not `curl`.
