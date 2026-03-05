# CGM Get Agent — NLSpec

## 1. Overview

CGM Get Agent is an MCP (Model Context Protocol) server that bridges LLM chatbots (Claude, ChatGPT) to a Dexcom G7 continuous glucose monitor via the Dexcom Developer API v3. It exposes all six Dexcom read endpoints as MCP tools, plus local meal and exercise logging with glucose correlation and impact rating. The LLM uses this structured data to provide personalized health guidance.

The system runs as a single Go binary inside a Docker container on macOS (Apple Silicon / arm64), managed via Docker Compose with Colima as the container runtime.

### 1.1 Design Principles

- **MCP-first**: The primary protocol is MCP (SSE transport for remote clients, stdio for local/Claude Code). A thin REST shim provides OpenAI function-calling compatibility.
- **Single binary, single container**: No microservices. One Go binary, one Docker image, one compose service.
- **Spec-driven development**: This document is the source of truth. Claude Code implements against it.
- **Health data sensitivity**: All Dexcom OAuth tokens encrypted at rest. No PHI leaves the local machine unless explicitly configured.
- **Offline-tolerant**: Meal and exercise logging works without Dexcom connectivity. Glucose fetches degrade gracefully with cached data.
- **Read-only Dexcom API**: The Dexcom API v3 is entirely read-only. Carbs, insulin, exercise, and health events logged in the Dexcom G7 app are readable via the API but cannot be created or deleted through it. Local meal and exercise logging fills this gap.

### 1.2 Constraints

- Dexcom API v3 imposes a **1-hour data delay** for US mobile app users (G7 via iOS). Data uploaded from a receiver via USB is immediate. All tool responses must include the `data_delay_notice` field when the most recent EGV is older than 10 minutes.
- Dexcom API v3 query windows are capped at **30 days** maximum. Requests exceeding this return a `WindowTooLargeError`.
- Each `refresh_token` is **single-use**. Upon exchange, a new refresh_token is issued and the old one is invalidated. The token lifecycle manager must handle this atomically.
- The G7 transmits an EGV reading every **5 minutes**.
- Rate limit: 60,000 API calls per app per hour (HTTP 429 if exceeded).

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
    recordId     : String
    systemTime   : Timestamp
    displayTime  : Timestamp
    eventType    : EventType
    eventSubType : EventSubType | None
    value        : Float | None     -- carbs in grams, exercise in minutes, insulin in units
    unit         : String           -- "grams", "minutes", "units", "mg/dL", "unknown"

ENUM EventType:
    carbs
    insulin
    exercise
    health

ENUM EventSubType:
    -- carbs subtypes
    liquid | solid
    -- insulin subtypes
    rapidActing | shortActing | longActing | combination
    -- exercise subtypes
    cardiovascular | strength | mixed
    -- health subtypes
    illness | stress | highSymptoms | lowSymptoms | cycle
    -- shared
    other | unknown

RECORD CalibrationRecord:
    recordId              : String    -- UUID from Dexcom
    systemTime            : Timestamp
    displayTime           : Timestamp
    value                 : Int       -- fingerstick glucose reading in mg/dL
    unit                  : String    -- "mg/dL"
    transmitterId         : String
    transmitterGeneration : String
    displayDevice         : String
    displayApp            : String

RECORD AlertRecord:
    recordId    : String
    systemTime  : Timestamp
    displayTime : Timestamp
    alertName   : AlertType
    alertState  : AlertState

ENUM AlertType:
    high | low | urgentLow | urgentLowSoon | rise | fall | outOfRange | noReadings

ENUM AlertState:
    triggered | acknowledged | cleared

RECORD DataRange:
    calibrations : TimeRange
    egvs         : TimeRange
    events       : TimeRange

RECORD TimeRange:
    start : Timestamp
    end   : Timestamp

RECORD DeviceRecord:
    deviceStatus          : String
    displayDevice         : String
    displayApp            : String
    lastUploadDate        : String
    transmitterGeneration : String
    transmitterId         : String
    alertScheduleList     : List<Any> | None
```

### 2.2 Application Types (local, read-write)

```
RECORD Meal:
    id          : String              -- generated: "m_YYYYMMDD_HHmm"
    description : String              -- free text from user via LLM
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
    low | moderate | moderate_high | high | max

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
    effectiveness   : String          -- "strong_reduction", "mild_reduction", "neutral", "no_reduction"

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

The MCP server exposes eleven tools. The first six fetch data from Dexcom; the final five manage local data and analysis. Each tool returns structured JSON as `TextContent` in the MCP `CallToolResult`.

```
INTERFACE MCPTools:

    -- ── Dexcom Read Tools (all read-only, backed by Dexcom API v3) ─────────

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
            zone       : String        -- "low", "low_normal", "target", "elevated", "high" per config

    TOOL get_dexcom_events:
        description: "Get events logged in the Dexcom G7 app (carbs, insulin, exercise, health) for a date range. Read-only; the Dexcom API does not support creating events."
        input:
            start_date   : Timestamp   -- ISO 8601
            end_date     : Timestamp   -- ISO 8601
        output: List<DexcomEvent> (JSON)
        errors:
            - WindowTooLargeError
            - DexcomAuthError
            - DexcomAPIError

    TOOL get_calibrations:
        description: "Get fingerstick blood glucose calibration records from the Dexcom G7 for a date range."
        input:
            start_date : Timestamp     -- ISO 8601
            end_date   : Timestamp     -- ISO 8601
        output: List<CalibrationRecord> (JSON)
        errors:
            - WindowTooLargeError
            - DexcomAuthError
            - DexcomAPIError

    TOOL get_alerts:
        description: "Get glucose alert events (high, low, urgent low, rising fast, falling fast) fired by the G7 sensor for a date range."
        input:
            start_date : Timestamp     -- ISO 8601
            end_date   : Timestamp     -- ISO 8601
        output: List<AlertRecord> (JSON)
        errors:
            - WindowTooLargeError
            - DexcomAuthError
            - DexcomAPIError

    TOOL get_devices:
        description: "Get Dexcom G7 transmitter and display device information for the authenticated user."
        input: (none)
        output: List<DeviceRecord> (JSON)
        errors:
            - DexcomAuthError
            - DexcomAPIError

    TOOL get_data_range:
        description: "Get the earliest and latest timestamps for each Dexcom data type (EGVs, events, calibrations). Useful for determining what data is available before making range queries."
        input: (none)
        output: DataRange (JSON)
        errors:
            - DexcomAuthError
            - DexcomAPIError

    -- ── Local Tools (SQLite-backed, work without Dexcom connectivity) ───────

    TOOL log_meal:
        description: "Log a meal locally with description and optional estimated macros. The LLM typically estimates carbs/protein/fat from the user's description. Use get_dexcom_events to read meals logged in the G7 app."
        input:
            description : String       -- required, free text
            carbs_est   : Float | None
            protein_est : Float | None
            fat_est     : Float | None
            timestamp   : Timestamp | None  -- default: now
            notes       : String | None
        output: Meal (JSON, the stored record)

    TOOL log_exercise:
        description: "Log an exercise session locally with type, duration, and intensity."
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

All six Dexcom data endpoints are read-only. The API does not support creating, updating, or deleting any records.

```
INTERFACE DexcomClient:

    FUNCTION get_egvs(start: Timestamp, end: Timestamp) -> List<EGVRecord>:
        -- GET {base}/v3/users/self/egvs?startDate={start}&endDate={end}
        -- Validates window <= 30 days.

    FUNCTION get_events(start: Timestamp, end: Timestamp) -> List<DexcomEvent>:
        -- GET {base}/v3/users/self/events?startDate={start}&endDate={end}
        -- Returns carb intake, insulin, exercise, health events logged in Dexcom app.

    FUNCTION get_calibrations(start: Timestamp, end: Timestamp) -> List<CalibrationRecord>:
        -- GET {base}/v3/users/self/calibrations?startDate={start}&endDate={end}
        -- Returns fingerstick calibration records. Empty for Dexcom ONE/ONE+.

    FUNCTION get_alerts(start: Timestamp, end: Timestamp) -> List<AlertRecord>:
        -- GET {base}/v3/users/self/alerts?startDate={start}&endDate={end}
        -- Returns high/low/urgentLow/rise/fall/outOfRange/noReadings alert events.

    FUNCTION get_data_range() -> DataRange:
        -- GET {base}/v3/users/self/dataRange
        -- No date parameters. Returns earliest/latest timestamps for all data types.

    FUNCTION get_devices() -> List<DeviceRecord>:
        -- GET {base}/v3/users/self/devices
        -- No date parameters. Returns G7 device info, transmitter, alerts config.
```

### 3.5 Store (SQLite)

```
INTERFACE Store:

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
        intensity    TEXT NOT NULL
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

    FUNCTION cache_egvs(records: List<EGVRecord>) -> Int
    FUNCTION get_cached_egvs(start: Timestamp, end: Timestamp) -> List<EGVRecord>
```

### 3.6 Glucose Analyzer

```
INTERFACE GlucoseAnalyzer:

    FUNCTION classify_zone(value: Int, config: GlucoseZones) -> String:
        -- Returns "low" | "low_normal" | "target" | "elevated" | "high"

    FUNCTION compute_snapshot(egvs: List<EGVRecord>, config: GlucoseZones) -> GlucoseSnapshot:
        -- Identifies current, baseline, peak, trough from EGV series.
        -- Sets data_delay_notice if most recent EGV systemTime > 10 minutes old.

    FUNCTION assess_meal_impact(meal: Meal, egvs: List<EGVRecord>, exercises: List<Exercise>) -> MealImpactAssessment:
        -- Post-meal window: 30-180 minutes after meal.timestamp.
        -- spike_delta = peak - baseline.
        -- Rating 1-10:
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
```

---

## 4. MCP Server Implementation

### 4.1 SDK Choice

Use the **official Go MCP SDK**: `github.com/modelcontextprotocol/go-sdk/mcp`. Import path: `github.com/modelcontextprotocol/go-sdk/mcp`.

The server must support both **SSE transport** (for remote MCP clients like claude.ai) and **stdio transport** (for Claude Code local usage). Use env var to select: `GA_MCP_TRANSPORT=sse|stdio` (default: `sse`).

### 4.2 Tool Registration Pattern

```go
mcp.AddTool(server, &mcp.Tool{
    Name:        "get_current_glucose",
    Description: "Get the current glucose reading...",
}, handler_function)
```

Input structs use `json` and `jsonschema` struct tags for automatic JSON Schema generation.

### 4.3 Error Handling

Tool errors are returned as `mcp.CallToolResult` with `IsError: true` and a `TextContent` block:

```json
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
Stage 1: golang:1.24-alpine AS builder
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
│   │   └── tools.go                 -- 11 tool handler functions
│   ├── rest/
│   │   └── handler.go               -- REST shim: /v1/tools/invoke, /health
│   ├── dexcom/
│   │   ├── client.go                -- Dexcom API client (all 6 endpoints)
│   │   ├── oauth.go                 -- OAuth2 lifecycle
│   │   └── types.go                 -- API response envelopes and error types
│   ├── store/
│   │   ├── sqlite.go                -- SQLite connection, migrations
│   │   ├── meals.go                 -- Meal CRUD
│   │   ├── exercise.go              -- Exercise CRUD
│   │   └── cache.go                 -- Glucose cache operations
│   ├── analyzer/
│   │   └── glucose.go               -- GlucoseAnalyzer: zone classification, snapshot, meal impact
│   ├── crypto/
│   │   └── tokens.go                -- AES-256-GCM encrypt/decrypt for OAuth tokens
│   ├── config/
│   │   └── config.go                -- YAML + env var config loading
│   └── types/
│       └── types.go                 -- All shared types across packages
├── docs/
│   ├── architecture.mermaid
│   ├── workflow.mermaid
│   └── implementation-plan.md
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

### Scenario 1: Simple Glucose Check

```
USER: "What's my glucose right now?"

EXPECTED TOOL CALLS:
    1. get_current_glucose(include_trend=true, history_minutes=30)

EXPECTED BEHAVIOR:
    - Returns GlucoseSnapshot with current reading, trend, and short history.
    - If data is >10 min stale, includes data_delay_notice.

VALIDATION:
    - Response contains valid EGV value (40-400 mg/dL range).
    - Trend is a valid TrendArrow enum value.
    - history array is ordered by systemTime ascending.
```

### Scenario 2: Meal Logging + Glucose Context

```
USER: "I just had three carne asada tacos with a horchata."

EXPECTED TOOL CALLS:
    1. log_meal(description="three carne asada tacos with a horchata", carbs_est=~70, ...)
    2. get_current_glucose(include_trend=true, history_minutes=30)

VALIDATION:
    - Meal record exists in SQLite with matching description.
    - meal.id follows "m_YYYYMMDD_HHmm" format.
    - Glucose data is returned and non-empty.
```

### Scenario 3: Meal Impact Rating

```
USER: "How did that burrito I had at lunch hit me?"

EXPECTED TOOL CALLS:
    1. rate_meal_impact(meal_id="m_20260303_1215")

VALIDATION:
    - spike_delta = peak_glucose - pre_meal_glucose.
    - rating is 1-10 consistent with spike_delta table.
    - exercise_offset populated if exercise occurred in post-meal window.
    - rating_rationale is non-empty.
```

### Scenario 4: Exercise + Glucose Correlation

```
USER: "I just did a 30-minute run. How's my glucose responding?"

EXPECTED TOOL CALLS:
    1. log_exercise(type="run", duration_min=30, intensity="moderate_high", ...)
    2. get_current_glucose(include_trend=true, history_minutes=60)

VALIDATION:
    - Exercise record exists in SQLite.
    - History window covers at least the exercise duration.
```

### Scenario 5: Reading Dexcom App Events

```
USER: "What did I log in my Dexcom app today?"

EXPECTED TOOL CALLS:
    1. get_dexcom_events(start_date="2026-03-03T00:00:00", end_date="2026-03-03T23:59:59")

VALIDATION:
    - Returns list of DexcomEvent records (carbs, insulin, exercise, health).
    - Each event has valid eventType and optional eventSubType.
    - Value and unit consistent with eventType.
```

### Scenario 6: Alert History Review

```
USER: "Did my Dexcom alarm go off last night?"

EXPECTED TOOL CALLS:
    1. get_alerts(start_date="2026-03-02T22:00:00", end_date="2026-03-03T06:00:00")

VALIDATION:
    - Returns list of AlertRecord objects.
    - alertName is a valid AlertType enum value.
    - alertState reflects triggered/acknowledged/cleared lifecycle.
```

### Scenario 7: Fingerstick Calibration Review

```
USER: "When did I last calibrate my CGM?"

EXPECTED TOOL CALLS:
    1. get_calibrations(start_date="2026-02-25T00:00:00", end_date="2026-03-03T23:59:59")

VALIDATION:
    - Returns list of CalibrationRecord objects.
    - value is a plausible fingerstick reading (40-400 mg/dL).
```

### Scenario 8: OAuth Token Refresh (Background)

```
TRIGGER: Any tool call when access_token is within 5 minutes of expiry.

EXPECTED BEHAVIOR:
    - refresh_token_if_needed() detects imminent expiry.
    - POSTs refresh_token to Dexcom /v3/oauth2/token.
    - Captures NEW refresh_token from response (old one is now invalid).
    - Atomically writes encrypted tokens to /data/tokens.enc.
    - Original tool call succeeds transparently.

VALIDATION:
    - tokens.enc updated with new encrypted payload.
    - Old refresh_token no longer present in decrypted store.
```

### Scenario 9: Dexcom API Unavailable (Graceful Degradation)

```
TRIGGER: Dexcom API returns 5xx or connection timeout during get_current_glucose.

EXPECTED BEHAVIOR:
    - Agent checks glucose_cache table for records within last 30 minutes.
    - Cache hit → return cached GlucoseSnapshot with stale_data_notice.
    - Cache miss → return structured error with retry suggestion.
    - Local meal/exercise logging unaffected.

VALIDATION:
    - Tool does not crash or hang.
    - Response includes cached data with notice, or clear error.
```

### Scenario 10: First-Time Setup (OAuth Authorization)

```
USER: Starts container for first time; no tokens exist.

EXPECTED BEHAVIOR:
    - GET /health returns: dexcom_auth="not_configured"
    - User visits http://localhost:8080/oauth/start in browser.
    - After Dexcom auth + HIPAA consent, redirected to /callback.
    - Tokens encrypted and stored; GET /health returns dexcom_auth="valid".

VALIDATION:
    - /data/tokens.enc exists and is non-empty after flow.
    - Subsequent tool calls succeed.
```

---

## 8. Build & Run

### 8.1 Development Workflow

```bash
cd ~/git/cgm-get-agent
cp .env.example .env
openssl rand -hex 32  # paste into .env as GA_ENCRYPTION_KEY

docker compose up --build
open http://localhost:8080/oauth/start

# Claude Code stdio
claude mcp add cgm-get-agent -- docker exec -i cgm-get-agent cgm-get-agent serve --transport stdio

# Or SSE
claude mcp add --transport sse cgm-get-agent http://localhost:8080/mcp
```

### 8.2 Testing with Dexcom Sandbox

Set `GA_DEXCOM_ENV=sandbox` (default). No real CGM required for development.

### 8.3 Production Cutover

```bash
GA_DEXCOM_ENV=production
docker compose up --build
open http://localhost:8080/oauth/start
```

---

## 9. Security Considerations

- **Token encryption**: AES-256-GCM at rest in `/data/tokens.enc`. Key from env var only.
- **Volume permissions**: `~/.cgm-get-agent` should be `chmod 700`.
- **No inbound internet**: Container binds to localhost:8080. Use Tailscale or WireGuard for remote access.
- **Dexcom HIPAA**: OAuth flow includes HIPAA authorization screen. Consent required.
- **No PHI in logs**: Glucose values, meal descriptions, and health data must not appear at INFO level.
- **CSRF protection**: `/oauth/start` generates a random state stored server-side and validated in `/callback`.

---

## 10. Future Extensions (Out of Scope for v1)

- Apple Health / HealthKit integration
- Nightscout bridge
- Multi-user support
- Insulin tracking and dose correlation
- Pattern recognition (weekly/monthly trends, time-in-range, dawn phenomenon)
- Notification/alerting (persistent polling loop or webhook)
- LLM system prompt distribution via MCP resources
