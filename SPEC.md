# CGM Get Agent — NLSpec

## 1. Overview

CGM Get Agent is an MCP (Model Context Protocol) server that bridges LLM chatbots (Claude, ChatGPT) to a Dexcom G7 continuous glucose monitor via the Dexcom Developer API v3. It retrieves real-time glucose data, logs meals and exercise, correlates meals against glucose response curves, and provides structured data that LLMs use to generate personalized health guidance.

The system runs as a single Go binary inside a Docker container on macOS (Apple Silicon / arm64), managed via Docker Compose with Colima as the container runtime.

### 1.1 Design Principles

- **MCP-first**: The primary protocol is MCP (SSE transport for remote clients, stdio for local/Claude Code). A thin REST shim provides OpenAI function-calling compatibility.
- **Single binary, single container**: No microservices. One Go binary, one Docker image, one compose service.
- **Spec-driven development**: This document is the source of truth. Claude Code implements against it.
- **Health data sensitivity**: All Dexcom OAuth tokens encrypted at rest. No PHI leaves the local machine unless explicitly configured.
- **Offline-tolerant**: Meal and exercise logging works without Dexcom connectivity. Glucose fetches degrade gracefully with cached data.

### 1.2 Constraints

- Dexcom API v3 imposes a **1-hour data delay** for US mobile app users (G7 via iOS). Data uploaded from a receiver via USB is immediate. All tool responses must include the `data_delay_notice` field when the most recent EGV is older than 10 minutes.
- Dexcom API v3 query windows are capped at **30 days** maximum. Requests exceeding this return HTTP 400.
- Each `refresh_token` is **single-use**. Upon exchange, a new refresh_token is issued and the old one is invalidated. The token lifecycle manager must handle this atomically.
- The G7 transmits an EGV reading every **5 minutes**.

---

## 2. Data Structures

### 2.1 Dexcom API Types (read-only, from Dexcom v3)

```
RECORD EGVRecord:
    recordId        : String              -- UUID from Dexcom
    systemTime      : Timestamp           -- UTC time per device clock (not true UTC, subject to drift)
    displayTime     : Timestamp           -- Local time shown on device (may include UTC offset for mobile)
    transmitterId   : String              -- Hashed transmitter ID
    transmitterTicks: Int                 -- Transmitter tick count
    value           : Int                 -- Estimated glucose value in mg/dL (use this field, not smoothed/realtime)
    trend           : TrendArrow          -- Directional trend
    trendRate       : Float               -- Rate of change in mg/dL/min
    unit            : String              -- "mg/dL" or "mmol/L"
    rateUnit        : String              -- "mg/dL/min" or "mmol/L/min"
    displayDevice   : String              -- "iOS", "android", or "receiver"
    transmitterGeneration : String        -- "g7" for Dexcom G7
    displayApp      : String              -- "G7"

ENUM TrendArrow:
    doubleUp       -- rising > 3 mg/dL/min
    singleUp       -- rising 2-3 mg/dL/min
    fortyFiveUp    -- rising 1-2 mg/dL/min
    flat           -- stable (-1 to 1 mg/dL/min)
    fortyFiveDown  -- falling 1-2 mg/dL/min
    singleDown     -- falling 2-3 mg/dL/min
    doubleDown     -- falling > 3 mg/dL/min
    none           -- no trend available
    notComputable  -- insufficient data
    rateOutOfRange -- rate exceeds sensor capability

RECORD DexcomEvent:
    recordId    : String
    systemTime  : Timestamp
    displayTime : Timestamp
    eventType   : EventType
    eventSubType: String | None
    value       : Float | None          -- carbs in grams, exercise in minutes, insulin in units
    unit        : String                -- "grams", "minutes", "units", "mg/dL", "unknown"

ENUM EventType:
    carbs
    insulin
    exercise
    health

RECORD DataRange:
    calibrations : TimeRange
    egvs         : TimeRange
    events       : TimeRange

RECORD TimeRange:
    start : Timestamp
    end   : Timestamp
```

### 2.2 Application Types (local, read-write)

```
RECORD Meal:
    id          : String              -- generated: "m_YYYYMMDD_HHmm"
    description : String              -- free text from user via LLM, e.g. "carne asada tacos x3 with horchata"
    carbs_est   : Float | None        -- estimated grams, may be LLM-estimated
    protein_est : Float | None        -- estimated grams
    fat_est     : Float | None        -- estimated grams
    timestamp   : Timestamp           -- when the meal was consumed (may be approximate)
    logged_at   : Timestamp           -- when the record was created
    notes       : String | None       -- additional context

RECORD Exercise:
    id          : String              -- generated: "e_YYYYMMDD_HHmm"
    type        : String              -- free text: "kettlebell", "run", "walk", "cycling", etc.
    duration_min: Int                 -- duration in minutes
    intensity   : ExerciseIntensity
    timestamp   : Timestamp           -- when the exercise started
    logged_at   : Timestamp           -- when the record was created
    notes       : String | None

ENUM ExerciseIntensity:
    low
    moderate
    moderate_high
    high
    max

RECORD GlucoseSnapshot:
    current     : EGVRecord           -- most recent reading
    baseline    : EGVRecord | None    -- reading before meal/exercise window
    peak        : EGVRecord | None    -- highest reading in window
    trough      : EGVRecord | None    -- lowest reading in window
    history     : List<EGVRecord>     -- time-ordered series for the requested window
    data_delay_notice : String | None -- non-nil if most recent EGV is older than 10 minutes

RECORD MealImpactAssessment:
    meal            : Meal
    pre_meal_glucose: Int             -- baseline mg/dL before meal
    peak_glucose    : Int             -- highest mg/dL post-meal
    spike_delta     : Int             -- peak - baseline
    time_to_peak_min: Int             -- minutes from meal to peak
    recovery_glucose: Int             -- current or lowest post-peak mg/dL
    recovery_time_min: Int | None     -- minutes from peak to return to baseline (None if still elevated)
    exercise_offset : ExerciseOffset | None
    rating          : Int             -- 1-10 scale
    rating_rationale: String          -- explanation of rating

RECORD ExerciseOffset:
    exercise        : Exercise
    glucose_at_start: Int             -- mg/dL when exercise began
    glucose_at_end  : Int             -- mg/dL when exercise ended
    delta           : Int             -- change during exercise
    effectiveness   : String          -- "strong", "moderate", "minimal", "none"

RECORD OAuthTokens:
    access_token    : String          -- encrypted at rest
    refresh_token   : String          -- encrypted at rest, single-use
    expires_at      : Timestamp       -- access_token expiry
    last_refreshed  : Timestamp
```

### 2.3 Configuration

```
RECORD Config:
    dexcom:
        client_id       : String      -- from env: GA_DEXCOM_CLIENT_ID
        client_secret   : String      -- from env: GA_DEXCOM_CLIENT_SECRET
        environment     : DexcomEnv   -- from env: GA_DEXCOM_ENV
        redirect_uri    : String      -- default: "http://localhost:8080/callback"
    server:
        port            : Int         -- default: 8080
        host            : String      -- default: "0.0.0.0"
    storage:
        db_path         : String      -- from env: GA_DB_PATH, default: "/data/data.db"
        token_path      : String      -- from env: GA_TOKEN_PATH, default: "/data/tokens.enc"
    encryption:
        key             : Bytes       -- from env: GA_ENCRYPTION_KEY (32 bytes, hex-encoded)
    glucose_zones:                    -- user-configurable target ranges
        low             : Int         -- default: 70
        target_low      : Int         -- default: 80
        target_high     : Int         -- default: 120
        elevated        : Int         -- default: 140
        high            : Int         -- default: 180

ENUM DexcomEnv:
    sandbox             -- https://sandbox-api.dexcom.com
    production          -- https://api.dexcom.com
```

---

## 3. Interfaces

### 3.1 MCP Tool Definitions

The MCP server exposes six tools. Each tool has a name, description, input schema (JSON Schema), and returns structured JSON as `TextContent` in the MCP `CallToolResult`.

```
INTERFACE MCPTools:

    TOOL get_current_glucose:
        description: "Get the current glucose reading from the Dexcom G7, including trend direction, rate of change, and optional recent history."
        input:
            include_trend    : Boolean  -- default: true
            history_minutes  : Int      -- default: 30, max: 1440 (24 hours)
        output: GlucoseSnapshot (JSON)
        errors:
            - DexcomAuthError: OAuth tokens invalid or expired beyond refresh
            - DexcomAPIError: Dexcom API returned non-200
            - NoDataError: No EGV records in requested window

    TOOL get_glucose_history:
        description: "Get glucose readings for an arbitrary date range. Max window is 30 days per Dexcom API constraint."
        input:
            start_date : Timestamp     -- ISO 8601
            end_date   : Timestamp     -- ISO 8601
        output: List<EGVRecord> (JSON)
        errors:
            - WindowTooLargeError: window exceeds 30 days
            - DexcomAuthError
            - DexcomAPIError

    TOOL get_trend:
        description: "Get the current trend arrow and rate of change. Lightweight call for quick status checks."
        input: (none)
        output:
            trend      : TrendArrow
            trendRate  : Float
            value      : Int
            timestamp  : Timestamp
            zone       : String        -- "low", "target", "elevated", "high" based on config

    TOOL log_meal:
        description: "Log a meal with description and optional estimated macros. The LLM typically estimates carbs/protein/fat from the user's description."
        input:
            description : String       -- required, free text
            carbs_est   : Float | None
            protein_est : Float | None
            fat_est     : Float | None
            timestamp   : Timestamp | None  -- default: now
            notes       : String | None
        output: Meal (JSON, the stored record)

    TOOL log_exercise:
        description: "Log an exercise session with type, duration, and intensity."
        input:
            type         : String      -- required, free text
            duration_min : Int         -- required
            intensity    : ExerciseIntensity -- required
            timestamp    : Timestamp | None  -- default: now
            notes        : String | None
        output: Exercise (JSON, the stored record)

    TOOL rate_meal_impact:
        description: "Analyze the glucose impact of a previously logged meal. Correlates the meal timestamp against the post-meal glucose curve from Dexcom, calculates spike magnitude, time-to-peak, recovery, and assigns a 1-10 rating. If exercise occurred in the post-meal window, its offset effect is included."
        input:
            meal_id     : String       -- required, references a stored Meal
        output: MealImpactAssessment (JSON)
        errors:
            - MealNotFoundError: meal_id does not exist in store
            - InsufficientDataError: not enough post-meal EGV data yet (meal too recent)
```

### 3.2 REST Shim (OpenAI Adapter)

A thin HTTP adapter that translates OpenAI-style function calling into the same tool router used by MCP.

```
INTERFACE RESTShim:

    ENDPOINT POST /v1/tools/invoke:
        request:
            tool   : String            -- tool name (must match MCP tool name)
            params : Map<String, Any>  -- tool input parameters
        response:
            result : Any               -- tool output (same JSON as MCP tool_result)
            error  : String | None

    ENDPOINT GET /health:
        response:
            status        : String     -- "ok" | "degraded" | "error"
            dexcom_auth   : String     -- "valid" | "expired" | "not_configured"
            db_accessible : Boolean
            uptime_seconds: Int
```

### 3.3 OAuth2 Lifecycle

```
INTERFACE OAuthHandler:

    ENDPOINT GET /oauth/start:
        -- Redirects browser to Dexcom authorization URL.
        -- Includes client_id, redirect_uri, response_type=code, scope=offline_access, state=CSRF_token.
        -- Target: {dexcom_base}/v3/oauth2/login

    ENDPOINT GET /callback:
        -- Receives authorization code from Dexcom redirect.
        -- Validates CSRF state parameter.
        -- Exchanges code for access_token + refresh_token via POST to {dexcom_base}/v3/oauth2/token.
        -- Encrypts and stores tokens.
        -- Returns success page to browser.

    FUNCTION refresh_token_if_needed() -> OAuthTokens:
        -- Check if access_token is within 5 minutes of expiry.
        -- If so, POST refresh_token to {dexcom_base}/v3/oauth2/token.
        -- CRITICAL: refresh_token is single-use. Capture new refresh_token from response.
        -- Atomically update encrypted token store.
        -- Return valid tokens.

    FUNCTION get_valid_token() -> String:
        -- Calls refresh_token_if_needed().
        -- Returns access_token string for use in Authorization header.
```

### 3.4 Dexcom Client

```
INTERFACE DexcomClient:

    FUNCTION get_egvs(start: Timestamp, end: Timestamp) -> List<EGVRecord>:
        -- GET {base}/v3/users/self/egvs?startDate={start}&endDate={end}
        -- Authorization: Bearer {access_token}
        -- Validates window <= 30 days.
        -- Parses response.records array.
        -- Queries against systemTime field.

    FUNCTION get_events(start: Timestamp, end: Timestamp) -> List<DexcomEvent>:
        -- GET {base}/v3/users/self/events?startDate={start}&endDate={end}
        -- Returns carb intake, insulin, exercise, health events logged in Dexcom app.

    FUNCTION get_data_range() -> DataRange:
        -- GET {base}/v3/users/self/dataRange
        -- Returns earliest/latest timestamps for each record type.
        -- Useful for determining if new data is available.

    FUNCTION get_devices() -> List<DeviceRecord>:
        -- GET {base}/v3/users/self/devices
        -- Returns G7 device info, alerts, settings.
        -- No date parameters required.
```

### 3.5 Store (SQLite)

```
INTERFACE Store:

    -- Schema migrations run on startup. Use a migrations table for versioning.

    TABLE meals:
        id          TEXT PRIMARY KEY
        description TEXT NOT NULL
        carbs_est   REAL
        protein_est REAL
        fat_est     REAL
        timestamp   TEXT NOT NULL       -- ISO 8601
        logged_at   TEXT NOT NULL       -- ISO 8601
        notes       TEXT

    TABLE exercise:
        id           TEXT PRIMARY KEY
        type         TEXT NOT NULL
        duration_min INTEGER NOT NULL
        intensity    TEXT NOT NULL       -- enum value as string
        timestamp    TEXT NOT NULL
        logged_at    TEXT NOT NULL
        notes        TEXT

    TABLE glucose_cache:
        record_id    TEXT PRIMARY KEY   -- Dexcom recordId (dedup key)
        system_time  TEXT NOT NULL
        display_time TEXT NOT NULL
        value        INTEGER NOT NULL
        trend        TEXT NOT NULL
        trend_rate   REAL
        raw_json     TEXT NOT NULL      -- full Dexcom record for future use

    TABLE migrations:
        version      INTEGER PRIMARY KEY
        applied_at   TEXT NOT NULL

    FUNCTION save_meal(m: Meal) -> Meal
    FUNCTION get_meal(id: String) -> Meal | None
    FUNCTION list_meals(start: Timestamp, end: Timestamp) -> List<Meal>

    FUNCTION save_exercise(e: Exercise) -> Exercise
    FUNCTION list_exercise(start: Timestamp, end: Timestamp) -> List<Exercise>

    FUNCTION cache_egvs(records: List<EGVRecord>) -> Int  -- returns count cached
    FUNCTION get_cached_egvs(start: Timestamp, end: Timestamp) -> List<EGVRecord>
```

### 3.6 Glucose Analyzer

```
INTERFACE GlucoseAnalyzer:

    FUNCTION classify_zone(value: Int, config: GlucoseZones) -> String:
        -- Returns "low" | "low_normal" | "target" | "elevated" | "high"
        -- Thresholds from config.glucose_zones.

    FUNCTION compute_snapshot(egvs: List<EGVRecord>, config: GlucoseZones) -> GlucoseSnapshot:
        -- Identifies current, baseline, peak, trough from EGV series.
        -- Sets data_delay_notice if most recent EGV systemTime > 10 minutes old.

    FUNCTION assess_meal_impact(meal: Meal, egvs: List<EGVRecord>, exercises: List<Exercise>) -> MealImpactAssessment:
        -- Finds pre-meal baseline (last EGV before meal timestamp).
        -- Finds post-meal peak (highest EGV in 30-180 min window after meal).
        -- Calculates spike_delta = peak - baseline.
        -- Calculates time_to_peak_min.
        -- Determines recovery: has glucose returned to within 10 mg/dL of baseline?
        -- If exercise occurred in the post-meal window, calculates ExerciseOffset.
        -- Assigns rating 1-10:
            10: spike_delta <= 20
            9:  spike_delta <= 30
            8:  spike_delta <= 40
            7:  spike_delta <= 50
            6:  spike_delta <= 60
            5:  spike_delta <= 70
            4:  spike_delta <= 80
            3:  spike_delta <= 100
            2:  spike_delta <= 120
            1:  spike_delta > 120
        -- Generates rating_rationale string explaining the score factors.
```

---

## 4. MCP Server Implementation

### 4.1 SDK Choice

Use the **official Go MCP SDK**: `github.com/modelcontextprotocol/go-sdk/mcp`. This is maintained in collaboration with Google and tracks the MCP spec. Import path: `github.com/modelcontextprotocol/go-sdk/mcp`.

The server must support both **SSE transport** (for remote MCP clients like claude.ai) and **stdio transport** (for Claude Code local usage). Use a CLI flag or env var to select transport mode: `GA_MCP_TRANSPORT=sse|stdio` (default: `sse`).

### 4.2 Tool Registration Pattern

Each tool is registered using the SDK's typed tool handler pattern:

```
mcp.AddTool(server, &mcp.Tool{
    Name:        "get_current_glucose",
    Description: "Get the current glucose reading...",
}, handler_function)
```

Input structs use `json` and `jsonschema` struct tags for automatic JSON Schema generation.

### 4.3 Error Handling

Tool errors are returned as `mcp.CallToolResult` with `IsError: true` and a `TextContent` block containing a structured JSON error:

```
{
    "error": "DexcomAuthError",
    "message": "OAuth tokens expired. Re-authorize at http://localhost:8080/oauth/start",
    "retriable": false
}
```

---

## 5. Docker Configuration

### 5.1 Dockerfile (multi-stage, arm64-native)

```
Stage 1: golang:1.22-alpine AS builder
    - Install: gcc, musl-dev, sqlite-dev (CGO required for mattn/go-sqlite3)
    - WORKDIR /src
    - Copy go.mod, go.sum → go mod download
    - Copy source → CGO_ENABLED=1 GOOS=linux go build -o /cgm-get-agent ./cmd/server

Stage 2: alpine:3.19
    - Install: ca-certificates, sqlite-libs, wget (for healthcheck)
    - Copy binary from builder
    - EXPOSE 8080
    - ENTRYPOINT ["cgm-get-agent", "serve"]
```

### 5.2 Docker Compose

```yaml
services:
  cgm-get-agent:
    build: .
    container_name: cgm-get-agent
    ports:
      - "8080:8080"
    volumes:
      - ~/.cgm-get-agent:/data
    env_file:
      - .env
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://localhost:8080/health"]
      interval: 30s
      timeout: 5s
      retries: 3
    networks:
      - cgm-get-agent-net

networks:
  cgm-get-agent-net:
    driver: bridge
```

### 5.3 Environment Variables

```
GA_DEXCOM_CLIENT_ID       -- Dexcom developer app client ID
GA_DEXCOM_CLIENT_SECRET   -- Dexcom developer app client secret
GA_DEXCOM_ENV             -- "sandbox" or "production" (default: sandbox)
GA_ENCRYPTION_KEY         -- 32-byte hex-encoded AES-256 key for token encryption
GA_DB_PATH                -- SQLite database path (default: /data/data.db)
GA_TOKEN_PATH             -- Encrypted token file path (default: /data/tokens.enc)
GA_CONFIG_PATH            -- YAML config file path (default: /data/config.yaml)
GA_MCP_TRANSPORT          -- "sse" or "stdio" (default: sse)
GA_SERVER_PORT            -- HTTP port (default: 8080)
```

---

## 6. Project Structure

```
cgm-get-agent/
├── cmd/
│   └── server/
│       └── main.go                  -- entrypoint: parse flags, wire dependencies, start server
├── internal/
│   ├── mcp/
│   │   ├── server.go                -- MCP server setup, transport selection, tool registration
│   │   └── tools.go                 -- tool handler functions (one per tool)
│   ├── rest/
│   │   └── handler.go               -- REST shim: /v1/tools/invoke, /health
│   ├── dexcom/
│   │   ├── client.go                -- Dexcom API client (get_egvs, get_events, get_data_range, get_devices)
│   │   ├── oauth.go                 -- OAuth2 lifecycle (start, callback, refresh, token management)
│   │   └── types.go                 -- EGVRecord, DexcomEvent, DataRange, TrendArrow, etc.
│   ├── store/
│   │   ├── sqlite.go                -- SQLite connection, migrations
│   │   ├── meals.go                 -- Meal CRUD
│   │   ├── exercise.go              -- Exercise CRUD
│   │   └── cache.go                 -- Glucose cache operations
│   ├── analyzer/
│   │   └── glucose.go               -- GlucoseAnalyzer: zone classification, snapshot, meal impact
│   ├── crypto/
│   │   └── tokens.go                -- AES-256-GCM encrypt/decrypt for OAuth tokens
│   └── config/
│       └── config.go                -- YAML + env var config loading
├── docs/
│   ├── architecture.mermaid         -- system architecture diagram
│   ├── workflow.mermaid             -- runtime workflow sequence diagram
│   └── cgm-get-agent-spec.md       -- this file
├── Dockerfile
├── docker-compose.yaml
├── .env.example
├── .gitignore
├── go.mod
├── go.sum
└── README.md
```

---

## 7. Scenarios

Scenarios are behavioral test cases that validate the system end-to-end. Each scenario describes a user intent, the expected tool invocations, and the expected structured output. These are used both for manual validation and as the basis for automated integration tests.

### Scenario 1: Simple Glucose Check

```
USER: "What's my glucose right now?"

EXPECTED TOOL CALLS:
    1. get_current_glucose(include_trend=true, history_minutes=30)

EXPECTED BEHAVIOR:
    - Agent calls Dexcom GET /v3/users/self/egvs with 30-min window.
    - Returns GlucoseSnapshot with current reading, trend, and short history.
    - If data is >10 min stale, includes data_delay_notice.

VALIDATION:
    - Response contains valid EGV value (40-400 mg/dL range).
    - Trend is a valid TrendArrow enum value.
    - history array is ordered by systemTime ascending.
    - If sandbox: value comes from sandbox test data.
```

### Scenario 2: Meal Logging + Glucose Context

```
USER: "I just had three carne asada tacos with a horchata."

EXPECTED TOOL CALLS:
    1. log_meal(description="three carne asada tacos with a horchata", carbs_est=~70, ...)
    2. get_current_glucose(include_trend=true, history_minutes=30)

EXPECTED BEHAVIOR:
    - Meal is stored in SQLite with LLM-estimated macros.
    - Current glucose is fetched for context.
    - LLM uses both results to provide guidance on what to expect.

VALIDATION:
    - Meal record exists in meals table with matching description.
    - meal.id follows "m_YYYYMMDD_HHmm" format.
    - Glucose data is returned and non-empty.
```

### Scenario 3: Meal Impact Rating

```
USER: "How did that burrito I had at lunch hit me?"

EXPECTED TOOL CALLS:
    1. (LLM resolves "that burrito at lunch" to a meal_id, possibly via list_meals or context)
    2. rate_meal_impact(meal_id="m_20260303_1215")

EXPECTED BEHAVIOR:
    - Agent reads meal from SQLite.
    - Queries Dexcom for 3-hour post-meal EGV window.
    - Queries SQLite for exercise in the same window.
    - Computes MealImpactAssessment.

VALIDATION:
    - spike_delta = peak_glucose - pre_meal_glucose.
    - rating is 1-10 and consistent with spike_delta table in Section 3.6.
    - If exercise occurred, exercise_offset is populated with non-None values.
    - rating_rationale is a non-empty string explaining factors.
```

### Scenario 4: Exercise + Glucose Correlation

```
USER: "I just did a 30-minute run. How's my glucose responding?"

EXPECTED TOOL CALLS:
    1. log_exercise(type="run", duration_min=30, intensity="moderate_high", ...)
    2. get_current_glucose(include_trend=true, history_minutes=60)

EXPECTED BEHAVIOR:
    - Exercise logged.
    - 60-min history shows pre- and during-exercise glucose trajectory.
    - LLM interprets the trend and exercise effect.

VALIDATION:
    - Exercise record exists in exercise table.
    - History window covers at least the exercise duration.
```

### Scenario 5: OAuth Token Refresh (Background)

```
TRIGGER: Any tool call when access_token is within 5 minutes of expiry.

EXPECTED BEHAVIOR:
    - refresh_token_if_needed() detects imminent expiry.
    - POSTs refresh_token to Dexcom /v3/oauth2/token.
    - Captures NEW refresh_token from response (old one is now invalid).
    - Atomically writes encrypted tokens to /data/tokens.enc.
    - Retries the original Dexcom API call with new access_token.

VALIDATION:
    - tokens.enc file is updated with new encrypted payload.
    - Old refresh_token is no longer present in decrypted store.
    - Original tool call succeeds transparently.
```

### Scenario 6: Dexcom API Unavailable (Graceful Degradation)

```
TRIGGER: Dexcom API returns 5xx or connection timeout during get_current_glucose.

EXPECTED BEHAVIOR:
    - Agent checks glucose_cache table for most recent cached EGVs.
    - If cache has data within last 30 minutes, returns cached data with a stale_data_notice.
    - If cache is older than 30 minutes, returns error with suggestion to try again.
    - Meal and exercise logging still works (SQLite-only path).

VALIDATION:
    - Tool does not crash or hang.
    - Response includes either cached data with notice or a clear error message.
    - SQLite operations are unaffected.
```

### Scenario 7: First-Time Setup (OAuth Authorization)

```
USER: Starts the container for the first time, no tokens exist.

EXPECTED BEHAVIOR:
    - GET /health returns: dexcom_auth="not_configured"
    - User visits http://localhost:8080/oauth/start in browser.
    - Redirected to Dexcom login with correct parameters.
    - After Dexcom auth + HIPAA consent, redirected to /callback with code.
    - Agent exchanges code for tokens, encrypts, stores.
    - GET /health now returns: dexcom_auth="valid"

VALIDATION:
    - /data/tokens.enc file exists and is non-empty.
    - Decrypted tokens contain both access_token and refresh_token.
    - Subsequent tool calls succeed.
```

---

## 8. Build & Run

### 8.1 Development Workflow

```bash
# Clone and enter repo
cd ~/git/cgm-get-agent

# Create environment file from example
cp .env.example .env
# Edit .env with Dexcom developer portal credentials + encryption key

# Generate encryption key
openssl rand -hex 32  # paste into .env as GA_ENCRYPTION_KEY

# Build and start
docker compose up --build

# One-time OAuth setup (open in browser)
open http://localhost:8080/oauth/start

# Connect Claude Code (stdio mode)
# For local dev, run the binary directly with GA_MCP_TRANSPORT=stdio
claude mcp add cgm-get-agent -- docker exec -i cgm-get-agent cgm-get-agent serve --transport stdio

# Or connect Claude Code to the SSE endpoint
claude mcp add --transport sse cgm-get-agent http://localhost:8080/mcp
```

### 8.2 Testing with Dexcom Sandbox

The Dexcom sandbox (sandbox-api.dexcom.com) provides simulated G7 data. No real CGM is needed for development. Sandbox login does not require a password. Set `GA_DEXCOM_ENV=sandbox` in .env.

### 8.3 Production Cutover

```bash
# In .env, change:
GA_DEXCOM_ENV=production

# Rebuild and re-authorize
docker compose up --build
open http://localhost:8080/oauth/start
# This time, real Dexcom credentials + HIPAA consent required
```

---

## 9. Security Considerations

- **Token encryption**: All Dexcom OAuth tokens are AES-256-GCM encrypted at rest in /data/tokens.enc. The encryption key is provided via environment variable, never baked into the image.
- **Volume permissions**: ~/.cgm-get-agent on host should be mode 0700. Contains PHI-adjacent health data.
- **No inbound internet**: Container binds to localhost:8080 only. For remote access (e.g., iPad), use Tailscale, WireGuard, or mTLS reverse proxy. Never expose MCP endpoint raw.
- **Dexcom HIPAA**: The Dexcom OAuth flow includes a HIPAA authorization screen. User must consent. This consent is per-authorization; revoking access is done at dexcom.com account settings.
- **No PHI in logs**: Do not log glucose values, meal descriptions, or any health data at INFO level. DEBUG level may include EGV values for troubleshooting but should be disabled in normal operation.
- **CSRF protection**: The /oauth/start endpoint generates a random state parameter stored server-side and validated in /callback.
- **Container isolation**: Single process, no shell needed in production image. Consider distroless base for hardened deployment.

---

## 10. Future Extensions (Out of Scope for v1)

These are documented for architectural awareness but NOT part of the initial implementation:

- **Apple Health / HealthKit integration**: Pull exercise and meal data from Apple Health via a companion iOS shortcut or app.
- **Nightscout bridge**: For users who also run Nightscout, pull glucose data from Nightscout API as an alternative to Dexcom direct.
- **Multi-user support**: Current architecture is single-user. Multi-user would require per-user token storage, user identification in MCP sessions, and tenant isolation.
- **Insulin tracking**: Dexcom events include insulin doses. A future tool could correlate insulin + meal + exercise + glucose for comprehensive analysis.
- **Pattern recognition**: Weekly/monthly trend analysis, time-in-range reports, dawn phenomenon detection.
- **Notification/alerting**: Proactive alerts when glucose enters danger zones (would require a persistent polling loop or webhook).
- **LLM system prompt distribution**: Serve the glucose zone definitions and dietary context as an MCP resource, so the LLM can read them dynamically instead of requiring them in the system prompt.
