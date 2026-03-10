#!/usr/bin/env bash
# install.sh — Interactive installer for CGM Get Agent
# Usage: ./install.sh [--upgrade]
set -euo pipefail

# ─── Colors ──────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m' # No Color

info()  { printf "${BLUE}[info]${NC}  %s\n" "$*"; }
ok()    { printf "${GREEN}[ok]${NC}    %s\n" "$*"; }
warn()  { printf "${YELLOW}[warn]${NC}  %s\n" "$*"; }
err()   { printf "${RED}[error]${NC} %s\n" "$*"; }
bold()  { printf "${BOLD}%s${NC}\n" "$*"; }

# ─── Constants ───────────────────────────────────────────────────────────────
REPO_ROOT="$(cd "$(dirname "$0")" && pwd)"
DATA_DIR="$HOME/.cgm-get-agent"
ENV_FILE="$REPO_ROOT/.env"
VERSION_FILE="$REPO_ROOT/.version"
CONTAINER_NAME="cgm-get-agent"
IMAGE_NAME="cgm-get-agent-cgm-get-agent"
DEFAULT_PORT=8090
DEFAULT_LOG_LEVEL=2

# Required env vars for a complete install
REQUIRED_VARS=(GA_DEXCOM_CLIENT_ID GA_DEXCOM_CLIENT_SECRET GA_ENCRYPTION_KEY GA_DEXCOM_ENV GA_SERVER_PORT GA_MCP_TRANSPORT GA_DEXCOM_REDIRECT_URI)

# ─── Helpers ─────────────────────────────────────────────────────────────────

prompt_value() {
    local varname="$1" prompt="$2" default="${3:-}"
    if [[ -n "$default" ]]; then
        printf "${BOLD}%s${NC} [%s]: " "$prompt" "$default"
    else
        printf "${BOLD}%s${NC}: " "$prompt"
    fi
    read -r value
    value="${value:-$default}"
    if [[ -z "$value" ]]; then
        err "$varname is required."
        exit 1
    fi
    eval "$varname=\"$value\""
}

prompt_choice() {
    local prompt="$1"
    shift
    printf "${BOLD}%s${NC}\n" "$prompt"
    local i=1
    for opt in "$@"; do
        printf "  [%d] %s\n" "$i" "$opt"
        ((i++))
    done
    printf "Choice: "
    read -r choice
    echo "$choice"
}

check_command() {
    if command -v "$1" &>/dev/null; then
        ok "$1 found: $(command -v "$1")"
        return 0
    else
        err "$1 not found"
        return 1
    fi
}

check_go_version() {
    if ! command -v go &>/dev/null; then
        warn "Go not found (only needed for local development, not Docker)"
        return 0
    fi
    local ver
    ver=$(go version | grep -oE 'go[0-9]+\.[0-9]+' | head -1 | sed 's/go//')
    local major minor
    major=$(echo "$ver" | cut -d. -f1)
    minor=$(echo "$ver" | cut -d. -f2)
    if (( major > 1 || (major == 1 && minor >= 24) )); then
        ok "Go $ver (>= 1.24)"
    else
        warn "Go $ver found but 1.24+ recommended"
    fi
}

detect_artifacts() {
    local found=0
    if [[ -d "$DATA_DIR" ]]; then
        info "Found data directory: $DATA_DIR"
        ((found++))
    fi
    if [[ -f "$ENV_FILE" ]]; then
        info "Found .env file: $ENV_FILE"
        ((found++))
    fi
    if docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^${CONTAINER_NAME}$"; then
        info "Found running container: $CONTAINER_NAME"
        ((found++))
    fi
    if docker images --format '{{.Repository}}' 2>/dev/null | grep -q "^${IMAGE_NAME}$"; then
        info "Found Docker image: $IMAGE_NAME"
        ((found++))
    fi
    return $found
}

wait_for_health() {
    local port="$1" max_wait=30 elapsed=0
    info "Waiting for health check (up to ${max_wait}s)..."
    while (( elapsed < max_wait )); do
        if curl -sf "http://localhost:${port}/health" &>/dev/null; then
            ok "Health check passed"
            return 0
        fi
        sleep 2
        ((elapsed += 2))
    done
    warn "Health check did not pass within ${max_wait}s — container may still be starting"
    return 1
}

read_port_from_env() {
    if [[ -f "$ENV_FILE" ]]; then
        local port
        port=$(grep -E '^GA_SERVER_PORT=' "$ENV_FILE" 2>/dev/null | tail -1 | cut -d= -f2 | tr -d '[:space:]"'"'" || true)
        echo "${port:-$DEFAULT_PORT}"
    else
        echo "$DEFAULT_PORT"
    fi
}

write_version_file() {
    local sha timestamp
    sha=$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo "unknown")
    timestamp=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    cat > "$VERSION_FILE" <<EOF
sha=$sha
timestamp=$timestamp
EOF
    ok "Version recorded: $sha ($timestamp)"
}

read_version_file() {
    if [[ -f "$VERSION_FILE" ]]; then
        grep -E '^sha=' "$VERSION_FILE" | cut -d= -f2
    else
        echo "unknown"
    fi
}

# ─── Prerequisites ───────────────────────────────────────────────────────────

check_prerequisites() {
    bold "Checking prerequisites..."
    echo
    local missing=0

    check_go_version

    check_command docker || ((missing++))
    check_command docker-compose || docker compose version &>/dev/null && ok "docker compose (plugin) found" || ((missing++))

    if command -v colima &>/dev/null; then
        ok "Colima found"
        if colima status &>/dev/null; then
            ok "Colima is running"
        else
            warn "Colima is installed but not running — start with: colima start --arch aarch64 --vm-type vz"
        fi
    else
        warn "Colima not found — install with: brew install colima"
    fi

    if command -v node &>/dev/null; then
        ok "Node.js found (needed for mcp-remote bridge)"
    else
        warn "Node.js not found — needed for Claude Desktop MCP bridge (brew install node)"
    fi

    if (( missing > 0 )); then
        err "Missing required tools. Install them and re-run."
        exit 1
    fi
    echo
}

# ─── Fresh Install ───────────────────────────────────────────────────────────

fresh_install() {
    bold "─── Fresh Install ───"
    echo

    # Check for existing artifacts
    local artifact_count=0
    detect_artifacts || artifact_count=$?

    if (( artifact_count > 0 )); then
        echo
        err "Existing artifacts detected. A fresh install requires a clean slate."
        echo
        bold "To clean up manually, remove:"
        [[ -d "$DATA_DIR" ]] && echo "  rm -rf $DATA_DIR"
        [[ -f "$ENV_FILE" ]] && echo "  rm $ENV_FILE"
        if docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^${CONTAINER_NAME}$"; then
            echo "  docker compose down"
        fi
        if docker images --format '{{.Repository}}' 2>/dev/null | grep -q "^${IMAGE_NAME}$"; then
            echo "  docker rmi $IMAGE_NAME"
        fi
        echo
        echo "Then re-run: ./install.sh"
        exit 1
    fi

    # Environment selection
    echo
    local env_choice
    env_choice=$(prompt_choice "Environment?" "Sandbox (simulated data, no real CGM needed)" "Production (live CGM data)")
    local dexcom_env
    case "$env_choice" in
        1) dexcom_env="sandbox" ;;
        2) dexcom_env="production" ;;
        *) err "Invalid choice"; exit 1 ;;
    esac
    echo

    # Prompt for required values
    local client_id client_secret server_port log_level
    prompt_value client_id "Dexcom Client ID"
    prompt_value client_secret "Dexcom Client Secret"
    prompt_value server_port "Server port" "$DEFAULT_PORT"
    prompt_value log_level "Log level (1=ERROR, 2=INFO, 3=DEBUG)" "$DEFAULT_LOG_LEVEL"
    echo

    # Auto-generate encryption key
    info "Generating AES-256-GCM encryption key..."
    local encryption_key
    encryption_key=$(openssl rand -hex 32)
    ok "Encryption key generated"

    # Derive redirect URI
    local redirect_uri="http://localhost:${server_port}/callback"

    # Write .env
    info "Writing .env..."
    cat > "$ENV_FILE" <<EOF
# CGM Get Agent — Environment Configuration
# Generated by install.sh on $(date -u +"%Y-%m-%dT%H:%M:%SZ")
# This file is gitignored and must never be committed.

# Dexcom Developer App Credentials
# Register at https://developer.dexcom.com to obtain these values.
GA_DEXCOM_CLIENT_ID=$client_id
GA_DEXCOM_CLIENT_SECRET=$client_secret

# Dexcom environment: "sandbox" for simulated data, "production" for live CGM.
GA_DEXCOM_ENV=$dexcom_env

# AES-256-GCM key for encrypting OAuth tokens at rest (32 bytes, hex-encoded).
# Auto-generated — do not change unless you also delete tokens.enc.
GA_ENCRYPTION_KEY=$encryption_key

# Server port. Must match the Redirect URI in your Dexcom developer app.
GA_SERVER_PORT=$server_port

# OAuth redirect URI — derived from server port. Must match Dexcom app config.
GA_DEXCOM_REDIRECT_URI=$redirect_uri

# MCP transport: "sse" for Claude Desktop/ChatGPT, "stdio" for CLI.
GA_MCP_TRANSPORT=sse

# Log verbosity: 1=ERROR (quiet), 2=INFO (default), 3=DEBUG (diagnostic).
GA_LOG_LEVEL=$log_level
EOF
    ok ".env written"

    # Create data directory
    info "Creating data directory: $DATA_DIR"
    mkdir -p "$DATA_DIR"
    chmod 700 "$DATA_DIR"
    ok "Data directory created with mode 700"

    # Build and start
    echo
    info "Building and starting container..."
    (cd "$REPO_ROOT" && docker compose up --build -d)
    echo

    # Health check
    wait_for_health "$server_port" || true

    # Write version file
    write_version_file

    # Next steps
    echo
    bold "─── Setup Complete ───"
    echo
    bold "Next steps:"
    echo
    echo "  1. Authorize Dexcom (one-time):"
    echo "     open http://localhost:${server_port}/oauth/start"
    echo
    echo "  2. Connect Claude Desktop — add to ~/Library/Application Support/Claude/claude_desktop_config.json:"
    cat <<EOF

     {
       "mcpServers": {
         "cgm-get-agent": {
           "command": "npx",
           "args": [
             "mcp-remote",
             "http://localhost:${server_port}/sse",
             "--transport",
             "sse-only"
           ]
         }
       }
     }

EOF
    echo "  3. Connect Claude Code CLI:"
    echo "     claude mcp add --transport sse cgm-get-agent http://localhost:${server_port}/sse"
    echo
    echo "  4. Test it — ask Claude: \"What's my glucose right now?\""
    echo
}

# ─── Upgrade ─────────────────────────────────────────────────────────────────

upgrade_install() {
    bold "─── Upgrade ───"
    echo

    local old_sha
    old_sha=$(read_version_file)
    local new_sha
    new_sha=$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo "unknown")

    if [[ "$old_sha" != "unknown" ]]; then
        info "Upgrading from $old_sha to $new_sha"
    else
        info "Installing version $new_sha"
    fi
    echo

    # Validate .env exists
    if [[ ! -f "$ENV_FILE" ]]; then
        err ".env file not found. Cannot upgrade without existing configuration."
        echo "Run ./install.sh without --upgrade for a fresh install."
        exit 1
    fi

    # Check for missing required vars and prompt for them
    local missing_vars=()
    for var in "${REQUIRED_VARS[@]}"; do
        if ! grep -qE "^${var}=" "$ENV_FILE" 2>/dev/null; then
            missing_vars+=("$var")
        fi
    done

    if (( ${#missing_vars[@]} > 0 )); then
        warn "Missing vars in .env: ${missing_vars[*]}"
        echo "These may have been added in a recent update. Please provide values:"
        echo
        for var in "${missing_vars[@]}"; do
            local value
            prompt_value value "$var"
            echo "" >> "$ENV_FILE"
            echo "# Added during upgrade on $(date -u +"%Y-%m-%dT%H:%M:%SZ")" >> "$ENV_FILE"
            echo "${var}=$value" >> "$ENV_FILE"
            ok "Added $var to .env"
        done
        echo
    fi

    local port
    port=$(read_port_from_env)

    # Confirmation
    bold "Upgrade will:"
    echo "  - Rebuild Docker image from current code"
    echo "  - Restart the container"
    echo "  - Preserve your data ($DATA_DIR)"
    echo "  - Preserve your .env configuration"
    echo "  - Your OAuth tokens remain valid (no re-auth needed unless tokens have expired)"
    echo
    printf "Continue? [y/N]: "
    read -r confirm
    if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then
        info "Upgrade cancelled."
        exit 0
    fi
    echo

    # Stop, rebuild, start
    info "Stopping container..."
    (cd "$REPO_ROOT" && docker compose down)
    echo

    info "Rebuilding and starting container..."
    (cd "$REPO_ROOT" && docker compose up --build -d)
    echo

    # Health check
    wait_for_health "$port" || true

    # Write version file
    write_version_file

    # Check OAuth status
    local health_response auth_status
    health_response=$(curl -sf "http://localhost:${port}/health" 2>/dev/null || echo '{}')
    auth_status=$(echo "$health_response" | grep -oE '"dexcom_auth":"[^"]*"' | cut -d'"' -f4 || echo "unknown")

    # Status report
    echo
    bold "─── Upgrade Complete ───"
    echo
    echo "  Container:  running"
    echo "  Health:     $(echo "$health_response" | grep -oE '"status":"[^"]*"' | cut -d'"' -f4 || echo "checking...")"
    echo "  Data dir:   preserved ($DATA_DIR)"

    case "$auth_status" in
        valid)
            echo "  OAuth:      ${GREEN}valid${NC}"
            ;;
        expired)
            printf "  OAuth:      ${YELLOW}expired${NC} — run: open http://localhost:${port}/oauth/start\n"
            ;;
        not_configured)
            printf "  OAuth:      ${YELLOW}not configured${NC} — run: open http://localhost:${port}/oauth/start\n"
            ;;
        *)
            echo "  OAuth:      $auth_status"
            ;;
    esac

    echo
    echo "  Restart Claude Desktop to reconnect MCP."
    echo
}

# ─── Main ────────────────────────────────────────────────────────────────────

main() {
    bold "CGM Get Agent Installer"
    echo

    check_prerequisites

    # If --upgrade flag, go straight to upgrade
    if [[ "${1:-}" == "--upgrade" ]]; then
        # Verify existing install
        local artifact_count=0
        detect_artifacts || artifact_count=$?
        if (( artifact_count == 0 )); then
            err "No existing installation detected. Run ./install.sh for a fresh install."
            exit 1
        fi
        upgrade_install
        return
    fi

    # Auto-detect mode
    local artifact_count=0
    detect_artifacts || artifact_count=$?

    if (( artifact_count == 0 )); then
        # No artifacts — fresh install
        fresh_install
    else
        # Artifacts found — ask user
        echo
        local mode_choice
        mode_choice=$(prompt_choice "Existing installation detected. What would you like to do?" "Upgrade (preserve data and config)" "Fresh install (requires manual cleanup first)")
        echo
        case "$mode_choice" in
            1) upgrade_install ;;
            2) fresh_install ;;
            *) err "Invalid choice"; exit 1 ;;
        esac
    fi
}

main "$@"
