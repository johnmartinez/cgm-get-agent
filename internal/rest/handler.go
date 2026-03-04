// Package rest implements HTTP endpoints for health checking and REST tool invocation.
package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/johnmartinez/cgm-get-agent/internal/dexcom"
	"github.com/johnmartinez/cgm-get-agent/internal/store"
)

// Handler holds the dependencies needed for the health and tool-invoke endpoints.
type Handler struct {
	oauth     *dexcom.OAuthHandler
	store     *store.Store
	startTime time.Time
}

// New creates a REST Handler.
func New(oauth *dexcom.OAuthHandler, st *store.Store, startTime time.Time) *Handler {
	return &Handler{
		oauth:     oauth,
		store:     st,
		startTime: startTime,
	}
}

// HandleHealth responds with a JSON health summary.
// GET /health
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	dexcomAuth := h.checkDexcomAuth(r.Context())
	dbAccessible := h.store.Ping() == nil

	status := "ok"
	switch {
	case !dbAccessible:
		status = "error"
	case dexcomAuth == "not_configured":
		status = "error"
	case dexcomAuth == "expired":
		status = "degraded"
	}

	resp := map[string]any{
		"status":         status,
		"dexcom_auth":    dexcomAuth,
		"db_accessible":  dbAccessible,
		"uptime_seconds": int(time.Since(h.startTime).Seconds()),
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}

// HandleToolInvoke is a REST shim for MCP tool invocation.
// POST /v1/tools/invoke
// Callers should prefer the MCP SSE transport (/sse) or stdio transport.
func (h *Handler) HandleToolInvoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	fmt.Fprintln(w, `{"error":"UsesMCPTransport","message":"Use the MCP SSE transport at /sse or set GA_MCP_TRANSPORT=stdio","retriable":false}`)
}

func (h *Handler) checkDexcomAuth(ctx context.Context) string {
	if !h.oauth.TokensExist() {
		return "not_configured"
	}
	if _, err := h.oauth.GetValidToken(ctx); err != nil {
		return "expired"
	}
	return "valid"
}
