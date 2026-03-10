# CGM Get Agent — Makefile
# Run `make help` for a list of targets.

SHELL := /bin/bash

# Read port from .env if it exists, default to 8090
PORT := $(shell grep -E '^GA_SERVER_PORT=' .env 2>/dev/null | tail -1 | cut -d= -f2 | tr -d '[:space:]"'"'" || echo 8090)
ifeq ($(PORT),)
  PORT := 8090
endif

CONTAINER := cgm-get-agent
HEALTH_URL := http://localhost:$(PORT)/health
SSE_URL := http://localhost:$(PORT)/sse
OAUTH_URL := http://localhost:$(PORT)/oauth/start

.PHONY: install upgrade build start stop restart logs health auth rehup clean-warn status help

## install: Run interactive installer (auto-detects fresh vs upgrade)
install:
	@./install.sh

## upgrade: Upgrade existing installation (preserve data, rebuild container)
upgrade:
	@./install.sh --upgrade

## build: Build and start container (docker compose up --build -d)
build:
	docker compose up --build -d

## start: Start container without rebuilding (docker compose up -d)
start:
	docker compose up -d

## stop: Stop and remove container (docker compose down)
stop:
	docker compose down

## restart: Stop, rebuild, and start container
restart: stop build

## logs: Tail container logs (Ctrl-C to exit)
logs:
	docker logs -f $(CONTAINER)

## health: Check server health endpoint
health:
	@curl -sf $(HEALTH_URL) | python3 -m json.tool 2>/dev/null || curl -sf $(HEALTH_URL) || echo "Health check failed — is the container running?"

## auth: Open OAuth authorization page in browser
auth:
	@echo "Opening $(OAUTH_URL)..."
	@open $(OAUTH_URL)

## rehup: Quick rebuild cycle — stop, rebuild, start, health check
rehup: stop build
	@echo ""
	@echo "Waiting for health check..."
	@for i in $$(seq 1 15); do \
		if curl -sf $(HEALTH_URL) > /dev/null 2>&1; then \
			echo "Health: OK"; \
			curl -sf $(HEALTH_URL) | python3 -m json.tool 2>/dev/null || curl -sf $(HEALTH_URL); \
			exit 0; \
		fi; \
		sleep 2; \
	done; \
	echo "Health check did not pass within 30s — check: make logs"

## clean-warn: List artifacts that would need manual cleanup for a fresh install
clean-warn:
	@echo "Checking for existing artifacts..."
	@echo ""
	@found=0; \
	if [ -d "$$HOME/.cgm-get-agent" ]; then \
		echo "  [data dir]  $$HOME/.cgm-get-agent"; \
		echo "    rm -rf $$HOME/.cgm-get-agent"; \
		found=1; \
	fi; \
	if [ -f ".env" ]; then \
		echo "  [env file]  .env"; \
		echo "    rm .env"; \
		found=1; \
	fi; \
	if docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^$(CONTAINER)$$"; then \
		echo "  [container] $(CONTAINER) (running)"; \
		echo "    docker compose down"; \
		found=1; \
	fi; \
	if docker images --format '{{.Repository}}' 2>/dev/null | grep -q "^cgm-get-agent-cgm-get-agent$$"; then \
		echo "  [image]     cgm-get-agent-cgm-get-agent"; \
		echo "    docker rmi cgm-get-agent-cgm-get-agent"; \
		found=1; \
	fi; \
	if [ "$$found" = "0" ]; then \
		echo "  No artifacts found — ready for fresh install."; \
	fi; \
	echo ""

## status: Show container state, version, and health
status:
	@echo "─── CGM Get Agent Status ───"
	@echo ""
	@printf "  Container:  "; \
	if docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^$(CONTAINER)$$"; then \
		echo "running"; \
	elif docker ps -a --format '{{.Names}}' 2>/dev/null | grep -q "^$(CONTAINER)$$"; then \
		echo "stopped"; \
	else \
		echo "not found"; \
	fi
	@printf "  Port:       "; echo "$(PORT)"
	@printf "  Image:      "; docker inspect --format '{{.Image}}' $(CONTAINER) 2>/dev/null | cut -c8-19 || echo "n/a"
	@printf "  Version:    "; if [ -f .version ]; then cat .version | tr '\n' ' '; echo; else echo "n/a"; fi
	@printf "  Uptime:     "; docker inspect --format '{{.State.StartedAt}}' $(CONTAINER) 2>/dev/null || echo "n/a"
	@printf "  Health:     "; curl -sf $(HEALTH_URL) 2>/dev/null || echo "unreachable"
	@echo ""

## help: Show this help message
help:
	@echo "CGM Get Agent — Makefile Targets"
	@echo ""
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  make /' | sed 's/: /\t/'
	@echo ""
