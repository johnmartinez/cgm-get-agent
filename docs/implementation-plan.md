# CGM Get Agent — Implementation Plan

This document memorializes the phased build plan for the `cgm-get-agent` project. It is the canonical reference for tracking implementation progress. Update the status column as phases complete.

---

## Ground Rules

| Rule | Detail |
|---|---|
| **Git flow** | Branch → code → commit → push per phase. Branch naming: `feat/<name>`, `fix/<name>`, `test/<name>`. |
| **Commits** | Conventional commits: `feat(scope): summary` |
| **Security** | No static credentials. No `.env`, `*.enc`, `data.db`, or `config.yaml` in git. |
| **Tests** | Every package ships with `_test.go` covering core logic. Integration tests cover all 7 SPEC scenarios. |
| **Code quality** | `go vet ./...` must pass before each commit. No `panic()` in production paths. |
| **PHI** | No glucose values, meal descriptions, or health data at INFO log level. |

---

## Token Budget

| Phase | Branch | Est. Output Tokens | Status |
|---|---|---|---|
| CLAUDE.md + README.md | `main` | ~2,000 | ✅ Complete — PR #1 |
| 1 — Scaffolding | `feat/scaffolding` | ~3,000 | ✅ Complete — PR #2 |
| 2 — Core Packages | `feat/core-packages` | ~10,000 | ✅ Complete — PR #3 |
| 3 — Dexcom Integration | `feat/dexcom` | ~8,000 | ✅ Complete — PR #5 |
| 4 — Glucose Analyzer | `feat/analyzer` | ~5,000 | ⬜ Pending |
| 5 — MCP Server + REST + Entrypoint | `feat/mcp-server` | ~8,000 | ⬜ Pending |
| 6 — Test Harnesses | `feat/tests` | ~8,000 | ⬜ Pending |
| **Total** | | **~44,000** | |

---

## Phase 1 — Scaffolding

**Branch:** `feat/scaffolding`

**Goal:** Establish the full project skeleton so all subsequent phases have a clean, working base.

| File | Purpose |
|---|---|
| `go.mod` | Go module declaration, Go 1.22, dependency stubs |
| `go.sum` | Generated on `go mod tidy` |
| `.gitignore` | Excludes `.env`, `*.enc`, `data.db`, `config.yaml`, `~/.cgm-get-agent/` |
| `.env.example` | Placeholder values for all `GA_*` env vars — safe to commit |
| `Dockerfile` | Multi-stage build: `golang:1.22-alpine` builder → `alpine:3.19` runtime; CGO enabled; arm64-native |
| `docker-compose.yaml` | Service `cgm-get-agent`, volume `~/.cgm-get-agent:/data`, `env_file: .env`, healthcheck |

**Acceptance criteria:**
- `docker compose build` succeeds (even with stub `main.go`)
- `.gitignore` covers all credential and data files
- `.env.example` contains every `GA_*` variable documented in SPEC §5.3

---

## Phase 2 — Core Packages

**Branch:** `feat/core-packages`

**Goal:** All internal packages that have zero external (Dexcom) dependencies. Can be fully unit-tested in isolation.

### Files

| File | Key Responsibilities |
|---|---|
| `internal/config/config.go` | Load `GA_CONFIG_PATH` YAML → override with env vars → apply defaults → validate required fields → fail fast |
| `internal/crypto/tokens.go` | AES-256-GCM encrypt/decrypt; atomic file write (`write tmp → rename`); marshal/unmarshal `OAuthTokens` |
| `internal/store/sqlite.go` | Open SQLite (`mattn/go-sqlite3`); run versioned migrations; expose `*sql.DB` |
| `internal/store/meals.go` | `SaveMeal`, `GetMeal`, `ListMeals` |
| `internal/store/exercise.go` | `SaveExercise`, `ListExercise` |
| `internal/store/cache.go` | `CacheEGVs`, `GetCachedEGVs` |

### Migration schema (v1)

```sql
CREATE TABLE IF NOT EXISTS migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);

CREATE TABLE IF NOT EXISTS meals (
    id TEXT PRIMARY KEY, description TEXT NOT NULL,
    carbs_est REAL, protein_est REAL, fat_est REAL,
    timestamp TEXT NOT NULL, logged_at TEXT NOT NULL, notes TEXT
);

CREATE TABLE IF NOT EXISTS exercise (
    id TEXT PRIMARY KEY, type TEXT NOT NULL,
    duration_min INTEGER NOT NULL, intensity TEXT NOT NULL,
    timestamp TEXT NOT NULL, logged_at TEXT NOT NULL, notes TEXT
);

CREATE TABLE IF NOT EXISTS glucose_cache (
    record_id TEXT PRIMARY KEY, system_time TEXT NOT NULL,
    display_time TEXT NOT NULL, value INTEGER NOT NULL,
    trend TEXT NOT NULL, trend_rate REAL, raw_json TEXT NOT NULL
);
```

### Unit test coverage

- `crypto`: encrypt→decrypt round-trip; wrong key returns error; atomic write verified via temp file
- `store`: meal/exercise CRUD; cache dedup on `record_id`; migration idempotency
- `config`: env override wins over YAML; missing required field returns error

**Acceptance criteria:** `go test ./internal/config/... ./internal/crypto/... ./internal/store/...` passes.

---

## Phase 3 — Dexcom Integration

**Branch:** `feat/dexcom`

**Goal:** Full Dexcom API client + OAuth2 lifecycle. All network calls mockable via `httptest`.

### Files

| File | Key Responsibilities |
|---|---|
| `internal/dexcom/types.go` | `EGVRecord`, `TrendArrow` (enum), `DexcomEvent`, `EventType`, `DataRange`, `TimeRange`, `OAuthTokens` |
| `internal/dexcom/oauth.go` | HTTP handlers for `/oauth/start` and `/callback`; `refreshTokenIfNeeded`; `GetValidToken`; CSRF state map (`sync.Map`) |
| `internal/dexcom/client.go` | `GetEGVs`, `GetEvents`, `GetDataRange`, `GetDevices`; calls `GetValidToken` before each request; 30-day window validation |

### Critical constraints

- **Single-use refresh_token**: every successful refresh response issues a new refresh_token. Capture it. Write atomically before returning.
- **Atomic token write**: use `crypto.SaveTokens` which writes via temp file + rename.
- **CSRF**: `/oauth/start` generates `crypto/rand` state, stores in `sync.Map`; `/callback` validates and deletes it.
- **30-day cap**: `GetEGVs` must return `WindowTooLargeError` if `end - start > 30 days`.

### Unit test coverage

- `oauth`: mock Dexcom token endpoint; verify new refresh_token captured; verify CSRF mismatch rejected
- `client`: mock EGV endpoint; verify date formatting; verify 30-day window error; verify Bearer header sent

**Acceptance criteria:** `go test ./internal/dexcom/...` passes with no live network calls.

---

## Phase 4 — Glucose Analyzer

**Branch:** `feat/analyzer`

**Goal:** Pure business logic for glucose zone classification, snapshot construction, and meal impact assessment.

### File

`internal/analyzer/glucose.go`

### Functions

| Function | Behavior |
|---|---|
| `ClassifyZone(value int, cfg GlucoseZones) string` | Returns `"low"` / `"low_normal"` / `"target"` / `"elevated"` / `"high"` per config thresholds |
| `ComputeSnapshot(egvs []EGVRecord, cfg GlucoseZones) GlucoseSnapshot` | current=last, baseline=first, peak=max, trough=min; sets `DataDelayNotice` if last EGV > 10 min old; history sorted ascending |
| `AssessMealImpact(meal Meal, egvs []EGVRecord, exercises []Exercise) MealImpactAssessment` | See rating table below |

### Meal impact rating table

| spike_delta (mg/dL) | Rating |
|---|---|
| ≤ 20 | 10 |
| ≤ 30 | 9 |
| ≤ 40 | 8 |
| ≤ 50 | 7 |
| ≤ 60 | 6 |
| ≤ 70 | 5 |
| ≤ 80 | 4 |
| ≤ 100 | 3 |
| ≤ 120 | 2 |
| > 120 | 1 |

Post-meal window: 30–180 minutes. Exercise in the same window produces an `ExerciseOffset`.

### Unit test coverage

- All 10 rating tiers with synthetic EGV sequences
- `data_delay_notice` triggered at exactly 10 min + 1 sec
- Exercise offset: exercise during post-meal window populates `ExerciseOffset`
- `ClassifyZone` boundary values for all thresholds

**Acceptance criteria:** `go test ./internal/analyzer/...` passes.

---

## Phase 5 — MCP Server + REST Shim + Entrypoint

**Branch:** `feat/mcp-server`

**Goal:** Runnable server. All 6 MCP tools registered. REST shim operational. Health endpoint live.

### Files

| File | Key Responsibilities |
|---|---|
| `internal/mcp/server.go` | Create MCP server; register all 6 tools; select SSE vs stdio transport from `GA_MCP_TRANSPORT` |
| `internal/mcp/tools.go` | 6 handler functions: `handleGetCurrentGlucose`, `handleGetGlucoseHistory`, `handleGetTrend`, `handleLogMeal`, `handleLogExercise`, `handleRateMealImpact` |
| `internal/rest/handler.go` | `POST /v1/tools/invoke` (routes to same tool functions); `GET /health` |
| `cmd/server/main.go` | Parse env/flags; construct Config; open Store; create DexcomClient; create MCP server; register OAuth routes; start HTTP listener |

### MCP SDK pattern

```go
// github.com/modelcontextprotocol/go-sdk/mcp
mcp.AddTool(server, &mcp.Tool{
    Name:        "get_current_glucose",
    Description: "...",
}, handleGetCurrentGlucose)
```

### Tool error format

```json
{"error": "DexcomAuthError", "message": "...", "retriable": false}
```

Returned as `*mcp.CallToolResult` with `IsError: true`.

### Health endpoint

```json
{
  "status": "ok|degraded|error",
  "dexcom_auth": "valid|expired|not_configured",
  "db_accessible": true,
  "uptime_seconds": 42
}
```

### Graceful degradation (Scenario 6)

In `handleGetCurrentGlucose`:
1. Attempt Dexcom API call
2. On 5xx or timeout → query `glucose_cache` for records within 30 min
3. Cache hit → return snapshot with `stale_data_notice` populated
4. Cache miss → return structured error with retry suggestion

**Acceptance criteria:**
- `docker compose up --build` succeeds
- `GET /health` returns 200
- All 6 tools visible in Claude MCP tool list

---

## Phase 6 — Test Harnesses

**Branch:** `feat/tests`

**Goal:** Integration tests covering all 7 SPEC scenarios. No live network calls. Dexcom API mocked with `httptest`.

### Test scenarios

| # | Name | Validates |
|---|---|---|
| 1 | Simple Glucose Check | `GlucoseSnapshot` shape; EGV value in 40–400 range; history sorted ascending; `data_delay_notice` when stale |
| 2 | Meal Logging + Glucose Context | Meal in SQLite; `m_YYYYMMDD_HHmm` ID format; glucose data non-empty |
| 3 | Meal Impact Rating | `spike_delta = peak - baseline`; rating matches table; `exercise_offset` populated when exercise in window; `rating_rationale` non-empty |
| 4 | Exercise + Correlation | Exercise in SQLite; `e_YYYYMMDD_HHmm` ID format; history window ≥ exercise duration |
| 5 | OAuth Token Refresh | Atomic token swap; new refresh_token captured; old one gone from decrypted store; original tool call succeeds |
| 6 | Graceful Degradation | On mock 503: cached data returned with `stale_data_notice`; SQLite unaffected; no crash/hang |
| 7 | First-Time OAuth Setup | `dexcom_auth` transitions `not_configured → valid`; `tokens.enc` created; subsequent tool calls succeed |

### Test utilities

- `internal/testutil/mockdexcom.go` — `httptest.Server` with configurable EGV/token responses
- `internal/testutil/fixtures.go` — synthetic `EGVRecord` slices for all test scenarios

**Acceptance criteria:** `go test ./...` passes with `-race` flag.

---

## Key Dependencies

```
github.com/modelcontextprotocol/go-sdk/mcp  # MCP server (official Go SDK)
github.com/mattn/go-sqlite3                 # SQLite (CGO required)
gopkg.in/yaml.v3                            # Config file parsing
golang.org/x/crypto                         # AES-GCM
github.com/google/uuid                      # UUID generation
```

CGO required for SQLite. Docker build must set `CGO_ENABLED=1` with `gcc` and `musl-dev` in builder stage.

---

## Docs in This Directory

| File | Contents |
|---|---|
| `architecture.mermaid` | System architecture: clients → transport → tools → core services → Dexcom |
| `workflow.mermaid` | Runtime sequence: user query → LLM → tool calls → Dexcom → response |
| `implementation-plan.md` | This file — phased build plan with acceptance criteria |
