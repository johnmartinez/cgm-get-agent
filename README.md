# CGM Get Agent

An MCP (Model Context Protocol) server that connects LLM chatbots to a Dexcom G7 continuous glucose monitor. Ask Claude or ChatGPT about your glucose and get personalized guidance on meals and exercise.

## What It Does

- Retrieves real-time glucose readings from the Dexcom G7 via the Dexcom Developer API v3
- Logs meals and exercise conversationally through the LLM
- Correlates meals against post-meal glucose response curves
- Rates meal impact on a 1-10 scale with actionable feedback

## Stack

- **Go** — single binary, single container
- **MCP** — primary protocol (SSE + stdio transports), with a REST shim for OpenAI function calling
- **Dexcom API v3** — OAuth2 + EGV/event data from G7
- **SQLite** — local storage for meals, exercise, and glucose cache
- **Docker** — runs on macOS/Apple Silicon via Colima

## Getting Started

See [SPEC.md](SPEC.md) for the full project specification. Architecture and workflow diagrams are in `docs/`.

## Status

Under active development. Spec-driven build using Claude Code.

## License

TBD
