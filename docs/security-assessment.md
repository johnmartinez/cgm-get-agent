# Security Assessment — CGM Get Agent

**Date:** 2026-03-04
**Scope:** Full source code review
**Reviewer:** Claude Code (claude-sonnet-4-6)
**Codebase:** `github.com/johnmartinez/cgm-get-agent`

---

## Executive Summary

The codebase demonstrates sound security fundamentals: AES-256-GCM token encryption, atomic file writes, CSRF-protected OAuth, parameterized SQL, and structured logging at INFO level. No critical or high-severity vulnerabilities were found. Four medium-severity issues and several low/informational findings are documented below. All medium findings have concrete, low-effort fixes.

A `govulncheck v1.1.4` symbol-level scan (source: `docs/govulncheck.json`, database 2026-02-27) produced **0 callable vulnerability findings** across 137 known OSV entries in the dependency graph. All dependency versions are past their respective CVE fix points.

**Overall risk rating: LOW-MEDIUM** (no production-blocking issues; medium findings should be remediated before internet-facing deployment).

---

## Finding Index

| ID | Severity | Title | File(s) |
|----|----------|-------|---------|
| SEC-01 | MEDIUM | CSRF state map grows without bound (DoS vector) | `internal/dexcom/oauth.go:65` |
| SEC-02 | MEDIUM | PHI stored unencrypted in SQLite | `internal/store/cache.go`, `store/meals.go` |
| SEC-03 | MEDIUM | MCP/OAuth endpoints exposed without authentication | `docker-compose.yaml:5`, `cmd/server/main.go:64-69` |
| SEC-04 | MEDIUM | `/callback` reflects raw query parameter into response body | `internal/dexcom/oauth.go:84-87` |
| SEC-05 | LOW | `GA_DEXCOM_ENV` accepts arbitrary values silently | `internal/config/config.go:105-107` |
| SEC-06 | LOW | Container runs as root | `Dockerfile:19-30` |
| SEC-07 | LOW | Alpine base image not digest-pinned | `Dockerfile:19` |
| SEC-08 | LOW | `history_minutes` has no upper bound | `internal/mcp/tools.go:109-114` |
| SEC-09 | LOW | `/callback` proceeds with empty authorization code | `internal/dexcom/oauth.go:95` |
| SEC-10 | LOW | `/health` discloses operational state unauthenticated | `internal/rest/handler.go:33-56` |
| SEC-11 | INFO | No HTTP request body size limit | `cmd/server/main.go:72-78` |
| SEC-12 | INFO | No rate limiting on OAuth endpoints | `cmd/server/main.go:66-67` |
| SEC-13 | INFO | `X-Content-Type-Options` header not set | `cmd/server/main.go:72-78` |

---

## Detailed Findings

---

### SEC-01 — MEDIUM: CSRF state map grows without bound

**File:** `internal/dexcom/oauth.go:65`
**Risk:** Memory exhaustion DoS if `/oauth/start` is called repeatedly without completing the OAuth flow.

**Vulnerable code:**
```go
// oauth.go:65 — state stored, never expired
h.csrfStates.Store(state, struct{}{})
```

States are consumed atomically via `LoadAndDelete` in `HandleCallback` (`oauth.go:90`), which is correct. However, if the user (or an attacker with access to port 8080) calls `/oauth/start` repeatedly without ever completing the callback, states accumulate in memory indefinitely. Each entry is ~40 bytes but with sustained hammering this becomes a heap-pressure DoS.

**Risk factors:**
- Port 8080 is published to all interfaces in `docker-compose.yaml` (see SEC-03)
- `/oauth/start` requires no authentication

**Fix — add a TTL-based cleanup goroutine:**
```go
// in OAuthHandler struct:
csrfStates sync.Map  // map[string]time.Time (store creation time, not struct{})

// in HandleStart — store creation timestamp:
h.csrfStates.Store(state, time.Now())

// in HandleCallback — check age before accepting:
if val, ok := h.csrfStates.LoadAndDelete(state); !ok {
    http.Error(w, "invalid or expired CSRF state", http.StatusBadRequest)
    return
}
if time.Since(val.(time.Time)) > 10*time.Minute {
    http.Error(w, "CSRF state expired", http.StatusBadRequest)
    return
}

// cleanup goroutine (start in NewOAuthHandler):
go func() {
    for range time.Tick(5 * time.Minute) {
        h.csrfStates.Range(func(k, v any) bool {
            if time.Since(v.(time.Time)) > 10*time.Minute {
                h.csrfStates.Delete(k)
            }
            return true
        })
    }
}()
```

---

### SEC-02 — MEDIUM: PHI stored unencrypted in SQLite

**Files:** `internal/store/cache.go:21-31`, `internal/store/meals.go:21-28`, `internal/store/sqlite.go:22-57`
**Risk:** Glucose readings, meal descriptions, and exercise sessions are stored in plaintext in the SQLite database at `~/.cgm-get-agent/data.db`.

**PHI in plaintext columns:**
- `glucose_cache.value` — numeric glucose reading (mg/dL)
- `glucose_cache.raw_json` — full EGVRecord JSON including value, trend rate, transmitter ID
- `meals.description` — free-text meal description (e.g., "80g carbs, took 6u Humalog")
- `meals.notes` — additional health notes
- `exercise.type`, `exercise.intensity` — activity PHI

The token file (`tokens.enc`) is correctly encrypted with AES-256-GCM, but the database containing the actual health data is not. This asymmetry means an attacker with filesystem read access (stolen laptop, Docker volume mount exposure) gets PHI without needing the encryption key.

**Mitigating factors:**
- The data directory is `~/.cgm-get-agent` — user-owned, not world-readable by default
- macOS FileVault (if enabled) provides OS-level encryption
- This is a single-user, local deployment

**Fix options (pick one):**

Option A — SQLite-level encryption using `sqlcipher` (replaces `mattn/go-sqlite3`):
```go
// go.mod: replace mattn/go-sqlite3 with mattn/go-sqlite3 + SQLITE_HAS_CODEC tag
// Open call:
db, err := sql.Open("sqlite3", path+"?_pragma_key="+encKeyHex+"&_journal_mode=WAL")
```

Option B — Encrypt `raw_json` at application layer (partial fix):
```go
// In CacheEGVs: encrypt raw before storing
encRaw, err := crypto.Encrypt(raw, s.encKey)
```

Option C — Document and require OS-level disk encryption:
```
# In QUICKSTART.md / README.md:
> Security note: Enable macOS FileVault or equivalent disk encryption.
> The SQLite database at ~/.cgm-get-agent/data.db stores glucose readings
> and meal logs in plaintext. Disk encryption is required for HIPAA compliance.
```

Option C is the fastest path to production. Option A provides the strongest guarantee.

---

### SEC-03 — MEDIUM: MCP/OAuth endpoints exposed without authentication

**Files:** `docker-compose.yaml:5`, `cmd/server/main.go:64-69`
**Risk:** All HTTP endpoints (`/sse`, `/oauth/start`, `/callback`, `/health`, `/v1/tools/invoke`) are accessible without any authentication from any host that can reach port 8080.

**docker-compose.yaml:**
```yaml
ports:
  - "8080:8080"   # publishes to 0.0.0.0 — network-accessible
```

**main.go:64-69:**
```go
mux.Handle("/sse", mcpServer.SSEHandler())       // MCP tools — no auth
mux.HandleFunc("/oauth/start", oauth.HandleStart) // initiates OAuth — no auth
mux.HandleFunc("/callback", oauth.HandleCallback) // token exchange — no auth
mux.HandleFunc("/health", restHandler.HandleHealth)
mux.HandleFunc("/v1/tools/invoke", restHandler.HandleToolInvoke)
```

The SPEC notes that Tailscale/WireGuard should be used for remote access, but the default config binds to all interfaces and the docker-compose publishes the port. Any host on the same network can:
1. Call MCP tools and receive glucose data
2. Trigger OAuth re-authorization flows
3. Read the health status (see SEC-10)

**Fix — bind to localhost only in docker-compose:**
```yaml
ports:
  - "127.0.0.1:8080:8080"   # localhost only
```

**Fix — add a shared-secret middleware for the SSE endpoint:**
```go
// middleware checks Authorization: Bearer <GA_MCP_SECRET> header
func requireToken(secret string, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if secret != "" {
            auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
            if auth != secret {
                http.Error(w, "unauthorized", http.StatusUnauthorized)
                return
            }
        }
        next.ServeHTTP(w, r)
    })
}

// main.go:
mux.Handle("/sse", requireToken(cfg.MCPSecret, mcpServer.SSEHandler()))
```

For a minimal fix, changing the docker-compose port binding to `127.0.0.1:8080:8080` is the immediate action required.

---

### SEC-04 — MEDIUM: `/callback` reflects raw query parameter into response body

**File:** `internal/dexcom/oauth.go:84-87`
**Risk:** Low-grade reflected injection. The `error` query parameter from the OAuth redirect is written directly to the HTTP response without sanitization.

**Vulnerable code:**
```go
if errParam := q.Get("error"); errParam != "" {
    http.Error(w, "Dexcom authorization failed: "+errParam, http.StatusBadRequest)
    return
}
```

`http.Error` sets `Content-Type: text/plain; charset=utf-8`, which prevents JavaScript execution in modern browsers. However:
- The CSRF state check occurs **after** this block, so no valid state is required to trigger the reflection
- The `X-Content-Type-Options: nosniff` header is not set (see SEC-13), allowing MIME sniffing on older clients
- The error appears verbatim in the response body

An attacker who can direct a browser to a crafted `/callback?error=<payload>` URL can inject arbitrary content into the response.

**Fix:**
```go
// Allowlist known Dexcom error codes instead of reflecting raw input
knownErrors := map[string]string{
    "access_denied":            "User denied access",
    "server_error":             "Dexcom server error",
    "temporarily_unavailable":  "Dexcom temporarily unavailable",
}
if errParam := q.Get("error"); errParam != "" {
    msg, ok := knownErrors[errParam]
    if !ok {
        msg = "authorization failed (unknown reason)"
    }
    http.Error(w, "Dexcom authorization failed: "+msg, http.StatusBadRequest)
    return
}
```

---

### SEC-05 — LOW: `GA_DEXCOM_ENV` accepts arbitrary values silently

**File:** `internal/config/config.go:105-107`
**Risk:** Any value other than `"production"` silently uses sandbox credentials, causing confusing behavior.

**Code:**
```go
if v := os.Getenv("GA_DEXCOM_ENV"); v != "" {
    cfg.Dexcom.Environment = v   // no validation
}
```

In `internal/dexcom/types.go`, `BaseURL()` uses sandbox for any value that isn't `"production"`, so a typo like `GA_DEXCOM_ENV=prod` silently routes all API calls to sandbox.

**Fix:**
```go
if v := os.Getenv("GA_DEXCOM_ENV"); v != "" {
    if v != "sandbox" && v != "production" {
        return nil, fmt.Errorf("config: GA_DEXCOM_ENV must be 'sandbox' or 'production', got %q", v)
    }
    cfg.Dexcom.Environment = v
}
```

---

### SEC-06 — LOW: Container runs as root

**File:** `Dockerfile:19-30`
**Risk:** Any remote code execution vulnerability in the binary gives root access within the container.

**Current Stage 2:**
```dockerfile
FROM --platform=linux/arm64 alpine:3.19
RUN apk add --no-cache ca-certificates sqlite-libs wget
COPY --from=builder /cgm-get-agent /usr/local/bin/cgm-get-agent
EXPOSE 8080
ENTRYPOINT ["cgm-get-agent", "serve"]
```

**Fix — add a non-root user before ENTRYPOINT:**
```dockerfile
FROM --platform=linux/arm64 alpine:3.19
RUN apk add --no-cache ca-certificates sqlite-libs wget \
    && addgroup -S cgm \
    && adduser -S cgm -G cgm
COPY --from=builder /cgm-get-agent /usr/local/bin/cgm-get-agent
USER cgm
EXPOSE 8080
ENTRYPOINT ["cgm-get-agent", "serve"]
```

Note: the `/data` volume (owned by the host `~/.cgm-get-agent` directory) must be writable by the `cgm` user. Add a `chown` step or adjust docker-compose volume permissions:
```yaml
volumes:
  - ~/.cgm-get-agent:/data:rw
```
and `chown -R <uid>:<gid> ~/.cgm-get-agent` on the host.

---

### SEC-07 — LOW: Alpine base image not digest-pinned

**File:** `Dockerfile:19`
**Risk:** `alpine:3.19` is a mutable tag. A compromised Docker Hub tag could silently deliver a malicious base image.

**Current:**
```dockerfile
FROM --platform=linux/arm64 alpine:3.19
```

**Fix — pin to a specific digest:**
```dockerfile
FROM --platform=linux/arm64 alpine:3.19@sha256:68bc7a67c00a02ef3b68946e87d0f3e2caefefe26d5ffc4b08a1db94c671b6fd
```

Retrieve the current digest with: `docker pull alpine:3.19 && docker inspect alpine:3.19 --format='{{index .RepoDigests 0}}'`

---

### SEC-08 — LOW: `history_minutes` has no upper bound

**File:** `internal/mcp/tools.go:109-114`
**Risk:** A caller can pass an arbitrarily large `history_minutes` value. The 30-day window check in `dexcom/client.go` catches this and returns a structured `WindowTooLargeError`, but the cap is enforced at the API layer rather than at the tool input layer, creating unnecessary round-trips.

**Code:**
```go
histMinutes := args.HistoryMinutes
if histMinutes <= 0 {
    histMinutes = 60
}
end := time.Now().UTC()
start := end.Add(-time.Duration(histMinutes) * time.Minute)
```

**Fix:**
```go
const maxHistoryMinutes = 30 * 24 * 60  // 30 days
histMinutes := args.HistoryMinutes
if histMinutes <= 0 {
    histMinutes = 60
}
if histMinutes > maxHistoryMinutes {
    return errResult("InvalidInput", "history_minutes cannot exceed 43200 (30 days)", false)
}
```

---

### SEC-09 — LOW: `/callback` proceeds with empty authorization code

**File:** `internal/dexcom/oauth.go:95`
**Risk:** If the OAuth redirect arrives with a valid CSRF state but no `code` parameter, `exchangeCode` is called with an empty string. Dexcom's token endpoint will reject it, but the error surfaces as a generic `"token exchange failed: ..."` message that could confuse users or mask configuration issues.

**Code:**
```go
tokens, err := h.exchangeCode(r.Context(), q.Get("code"))
```

**Fix:**
```go
code := q.Get("code")
if code == "" {
    http.Error(w, "authorization code missing from callback", http.StatusBadRequest)
    return
}
tokens, err := h.exchangeCode(r.Context(), code)
```

---

### SEC-10 — LOW: `/health` discloses operational state unauthenticated

**File:** `internal/rest/handler.go:33-56`
**Risk:** The `/health` endpoint is publicly accessible (no auth required) and reveals:
- Whether Dexcom OAuth is configured or expired
- Whether the database is accessible
- Server uptime in seconds

None of this is PHI, but it constitutes operational intelligence useful to an attacker mapping the system.

**Fix options:**
- Bind docker-compose to `127.0.0.1` (fixes SEC-03 and this finding simultaneously)
- Alternatively, restrict health disclosure to a boolean `{"status":"ok"}` without details for unauthenticated callers:
```go
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
    // Full details only if internal caller (e.g., Docker healthcheck via localhost)
    internal := r.Header.Get("X-Internal-Check") == os.Getenv("GA_HEALTH_SECRET")
    // ...
    if !internal {
        // Return minimal response for external callers
        json.NewEncoder(w).Encode(map[string]string{"status": status})
        return
    }
    // full response with dexcom_auth, db_accessible, uptime_seconds
}
```

---

### SEC-11 — INFO: No HTTP request body size limit

**File:** `cmd/server/main.go:72-78`
**Risk:** No `http.MaxBytesReader` middleware is applied. A slow-loris or large-body attack could hold goroutines. Mitigated by the 30s `ReadTimeout`, but defense-in-depth recommends an explicit body limit.

**Fix:** Add a middleware that wraps `r.Body`:
```go
func maxBodySize(limit int64, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        r.Body = http.MaxBytesReader(w, r.Body, limit)
        next.ServeHTTP(w, r)
    })
}
// wrap mux: srv.Handler = maxBodySize(1<<20, mux)  // 1MB
```

---

### SEC-12 — INFO: No rate limiting on OAuth endpoints

**File:** `cmd/server/main.go:66-67`
**Risk:** `/oauth/start` and `/callback` are not rate-limited. Combined with SEC-03 (network exposure) and SEC-01 (CSRF state accumulation), this enables cheap resource exhaustion.

**Fix:** Add a simple token-bucket middleware, or rely on the localhost-only binding from SEC-03 fix which eliminates external access.

---

### SEC-13 — INFO: Security headers not set

**File:** `cmd/server/main.go:72-78`
**Risk:** The HTTP server does not set `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, or `Content-Security-Policy`. These headers are a browser-side defense layer relevant to the `/callback` success page and error pages.

**Fix — add a middleware:**
```go
func secureHeaders(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("X-Content-Type-Options", "nosniff")
        w.Header().Set("X-Frame-Options", "DENY")
        w.Header().Set("Referrer-Policy", "no-referrer")
        next.ServeHTTP(w, r)
    })
}
// wrap mux: srv.Handler = secureHeaders(mux)
```

---

## Confirmed Strengths

The following were audited and found to be correctly implemented:

| Area | Detail | Location |
|------|--------|----------|
| **AES-256-GCM encryption** | Random 12-byte nonce per write, GCM tag verified on decrypt, base64 wire format | `internal/crypto/tokens.go` |
| **Atomic token writes** | Write to `.tmp`, `os.Rename` — POSIX atomic on same filesystem | `crypto/tokens.go:91-97` |
| **CSRF protection** | `crypto/rand` 16-byte hex state, `sync.Map` + `LoadAndDelete` (one-time use) | `oauth.go:60-93` |
| **Refresh token atomicity** | `sync.Mutex` serializes all refreshes; re-reads from disk inside lock to avoid double-refresh | `oauth.go:138-163` |
| **Single-use refresh token** | New `refresh_token` from every response is immediately saved before returning | `oauth.go:154-161` |
| **Parameterized SQL** | All queries use `?` placeholders; no string concatenation in queries | `store/meals.go`, `store/exercise.go`, `store/cache.go` |
| **Response body size limit** | `io.LimitReader(resp.Body, 4096)` on Dexcom error responses | `dexcom/client.go:182` |
| **No PHI in logs** | `slog` at INFO level; log calls use structured fields, not raw health data | `cmd/server/main.go:26` |
| **Encryption key validation** | Fails fast on startup if `GA_ENCRYPTION_KEY` is missing or not 64 hex chars (32 bytes) | `config/config.go:128-135` |
| **Read-only Dexcom API** | All client methods use `http.MethodGet`; no POST/DELETE for data endpoints | `dexcom/client.go:165` |
| **HTTP timeouts** | `ReadTimeout: 30s`, `WriteTimeout: 60s`, `IdleTimeout: 120s` | `cmd/server/main.go:75-77` |
| **Context propagation** | All HTTP requests use `NewRequestWithContext` for cancellation support | `dexcom/client.go:165`, `oauth.go:191` |
| **WAL mode + foreign keys** | SQLite opened with `?_journal_mode=WAL&_foreign_keys=on` | `store/sqlite.go:63` |
| **Dependency provenance** | All dependencies from first-party or well-known sources (golang.org/x, modelcontextprotocol) | `go.mod` |
| **Secrets never in YAML** | `EncryptionKey` field excluded from YAML tags; sourced only from `GA_ENCRYPTION_KEY` | `config/config.go:47` |

---

## Dependency Audit — govulncheck Results

**Tool:** `govulncheck v1.1.4`
**Source file:** `docs/govulncheck.json`
**Database:** `https://vuln.go.dev` (last modified 2026-02-27T02:17:00Z)
**Scan mode:** source / symbol-level
**Go version scanned:** go1.26.0

### Key Result: Zero Callable Vulnerabilities

govulncheck emits two distinct message types:

- **`osv`** — a vulnerability exists somewhere in the dependency graph's version history
- **`finding`** — a vulnerable symbol is actually *called* from the project's code

The scan produced **137 `osv` entries and 0 `finding` entries**. This means govulncheck traced every reachable call path through all 9 project packages and confirmed that **no vulnerable code path is callable at runtime**. All current dependency versions are past their respective fix points.

### SBOM — Scanned Modules

| Module | Version | Status |
|--------|---------|--------|
| `github.com/johnmartinez/cgm-get-agent` | (local) | — |
| `github.com/google/jsonschema-go` | v0.4.2 | ✅ No vulns |
| `github.com/mattn/go-sqlite3` | v1.14.34 | ✅ No vulns |
| `github.com/modelcontextprotocol/go-sdk` | v1.4.0 | ✅ No vulns |
| `github.com/segmentio/asm` | v1.1.3 | ✅ No vulns |
| `github.com/segmentio/encoding` | v0.5.3 | ✅ No vulns |
| `github.com/yosida95/uritemplate/v3` | v3.0.2 | ✅ No vulns |
| `golang.org/x/oauth2` | v0.34.0 | ✅ Past fix (see below) |
| `golang.org/x/sys` | v0.40.0 | ✅ Past fix (see below) |
| `gopkg.in/yaml.v3` | v3.0.1 | ✅ Past fix (see below) |
| `stdlib` | v1.26.0 | ✅ Past all fixes (see below) |

### Notable OSV Entries (Third-Party / Mixed)

These 3 entries affect packages in the SBOM. All are fixed in the versions currently in use.

#### GO-2025-3488 / CVE-2025-22868
**Package:** `golang.org/x/oauth2`
**Summary:** Unexpected memory consumption during token parsing
**Fixed:** v0.27.0 · **In use:** v0.34.0 ✅ patched
**govulncheck finding:** none — no call path to the vulnerable symbol

#### GO-2022-0603 / CVE-2022-28948
**Package:** `gopkg.in/yaml.v3`
**Summary:** Panic in `Unmarshal` on invalid input
**Fixed:** 3.0.0-20220521103104 · **In use:** v3.0.1 ✅ patched
**govulncheck finding:** none

#### GO-2022-0493 / CVE-2022-29526
**Package:** `stdlib` (`syscall`), `golang.org/x/sys/unix`
**Summary:** `Faccessat` incorrectly reports file accessibility when called with non-zero flags
**Fixed:** stdlib 1.18.2, golang.org/x/sys 0.0.0-20220412 · **In use:** stdlib v1.26.0, x/sys v0.40.0 ✅ patched
**govulncheck finding:** none

### Historical Stdlib OSV Entries (124 entries, all patched)

govulncheck logged 124 stdlib-only vulnerability records covering historical issues in `net/http`, `net/http2`, `crypto/tls`, `crypto/x509`, `encoding/*`, and other packages. All were fixed in Go releases predating v1.26.0. Representative entries by category:

| Category | Example IDs | Fixed in | Status |
|----------|-------------|----------|--------|
| HTTP/2 flood (rapid reset, CONTINUATION) | GO-2023-2102, GO-2024-2687 | go1.21.3, go1.22.2 | ✅ v1.26.0 |
| HTTP/2 memory growth | GO-2022-1144, GO-2022-0969 | go1.19.4, go1.19.1 | ✅ v1.26.0 |
| TLS/crypto panics | GO-2022-0229, GO-2021-0067 | go1.13.7, go1.15.x | ✅ v1.26.0 |
| Path/header injection | GO-2022-0535, GO-2023-1702 | go1.17.x, go1.20.x | ✅ v1.26.0 |
| Stack/resource exhaustion | GO-2022-0525, GO-2024-2598 | go1.17.x, go1.22.x | ✅ v1.26.0 |

The full list of all 137 OSV IDs is present in `docs/govulncheck.json`.

### Recommendation

No dependency updates are required. Add `govulncheck ./...` to CI to catch future regressions:
```yaml
# .github/workflows/security.yml (example)
- name: govulncheck
  run: go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

---

## Recommended Remediation Order

### Immediate (before any network-accessible deployment)

1. **SEC-03** — Change docker-compose to `127.0.0.1:8080:8080` (5 min)
2. **SEC-04** — Allowlist OAuth error codes in `/callback` (15 min)

### Short-term (within one sprint)

3. **SEC-01** — Add CSRF state TTL and cleanup goroutine (1 hour)
4. **SEC-05** — Validate `GA_DEXCOM_ENV` values (15 min)
5. **SEC-09** — Guard empty authorization code (5 min)
6. **SEC-08** — Cap `history_minutes` in tool handler (5 min)
7. **SEC-13** — Add security headers middleware (30 min)

### Before containerized production deployment

8. **SEC-06** — Add non-root user to Dockerfile (15 min)
9. **SEC-07** — Pin Alpine base image digest (5 min)

### Before HIPAA-covered deployment

10. **SEC-02** — Implement SQLite encryption or document disk encryption requirement (2-4 hours for SQLCipher; 30 min to document requirement)

---

*Assessment performed via static code review. No dynamic testing (fuzzing, penetration testing) was conducted. A follow-up dynamic assessment is recommended before production deployment.*
