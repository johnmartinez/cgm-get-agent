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

// ToolInvoker dispatches a named tool call and returns its JSON result.
// isError is true when the tool itself signalled an error (not a transport failure).
// Implemented by *mcp.Server.
type ToolInvoker interface {
	InvokeTool(ctx context.Context, name string, params json.RawMessage) (json.RawMessage, bool, error)
}

// invokeRequest is the JSON body accepted by POST /v1/tools/invoke.
type invokeRequest struct {
	Tool   string          `json:"tool"`
	Params json.RawMessage `json:"params"`
}

// Handler holds the dependencies needed for the health and tool-invoke endpoints.
type Handler struct {
	oauth     *dexcom.OAuthHandler
	store     *store.Store
	startTime time.Time
	invoker   ToolInvoker // nil when running in stdio-only mode
}

// New creates a REST Handler. Pass a non-nil invoker to enable POST /v1/tools/invoke.
func New(oauth *dexcom.OAuthHandler, st *store.Store, startTime time.Time, invoker ToolInvoker) *Handler {
	return &Handler{
		oauth:     oauth,
		store:     st,
		startTime: startTime,
		invoker:   invoker,
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
// POST /v1/tools/invoke  —  body: {"tool":"<name>","params":{...}}
// Returns: {"tool":"<name>","result":{...},"is_error":false}
// When no invoker is configured (stdio-only mode), returns 501.
func (h *Handler) HandleToolInvoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	if h.invoker == nil {
		w.WriteHeader(http.StatusNotImplemented)
		fmt.Fprintln(w, `{"error":"UsesMCPTransport","message":"Use the MCP SSE transport at /sse or set GA_MCP_TRANSPORT=stdio","retriable":false}`)
		return
	}

	var req invokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintln(w, `{"error":"InvalidRequest","message":"invalid JSON body"}`)
		return
	}
	if req.Tool == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintln(w, `{"error":"InvalidRequest","message":"tool name is required"}`)
		return
	}
	if req.Params == nil {
		req.Params = json.RawMessage(`{}`)
	}

	result, isError, err := h.invoker.InvokeTool(r.Context(), req.Tool, req.Params)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		b, _ := json.Marshal(map[string]any{"error": "ToolError", "message": err.Error()})
		w.Write(b) //nolint:errcheck
		return
	}

	resp := map[string]any{
		"tool":     req.Tool,
		"result":   json.RawMessage(result),
		"is_error": isError,
	}
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
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
