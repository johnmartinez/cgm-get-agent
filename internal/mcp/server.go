// Package mcp implements the MCP server, tool registration, and transport selection
// for the CGM Get Agent. It wires all application dependencies into the 11 MCP tools
// defined in the Dexcom CGM spec.
package mcp

import (
	"context"
	"net/http"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/johnmartinez/cgm-get-agent/internal/config"
	"github.com/johnmartinez/cgm-get-agent/internal/dexcom"
	"github.com/johnmartinez/cgm-get-agent/internal/store"
)

// Server wraps the MCP SDK server with all application dependencies
// needed by the 11 tool handlers.
type Server struct {
	cfg       *config.Config
	store     *store.Store
	oauth     *dexcom.OAuthHandler
	client    *dexcom.Client
	mcpServer *sdkmcp.Server
	startTime time.Time
}

// New creates an MCP Server, registers all 11 tools, and returns it.
func New(cfg *config.Config, st *store.Store, oauth *dexcom.OAuthHandler, client *dexcom.Client) *Server {
	s := &Server{
		cfg:       cfg,
		store:     st,
		oauth:     oauth,
		client:    client,
		startTime: time.Now(),
	}
	s.mcpServer = sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:  "cgm-get-agent",
		Title: "CGM Get Agent",
	}, nil)
	s.registerTools()
	return s
}

// StartTime returns when the server was created (for uptime calculation).
func (s *Server) StartTime() time.Time {
	return s.startTime
}

// SSEHandler returns an HTTP handler that serves the MCP SSE transport.
// Mount at /sse in the HTTP mux.
func (s *Server) SSEHandler() http.Handler {
	return sdkmcp.NewSSEHandler(func(r *http.Request) *sdkmcp.Server {
		return s.mcpServer
	}, nil)
}

// RunStdio runs the MCP server on stdin/stdout (blocks until ctx is cancelled).
func (s *Server) RunStdio(ctx context.Context) error {
	return s.mcpServer.Run(ctx, &sdkmcp.StdioTransport{})
}

func (s *Server) registerTools() {
	sdkmcp.AddTool(s.mcpServer, &sdkmcp.Tool{
		Name:        "get_current_glucose",
		Description: "Get the current glucose reading with optional trend and history window. Falls back to cached data when the Dexcom API is temporarily unavailable.",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args getCurrentGlucoseInput) (*sdkmcp.CallToolResult, any, error) {
		return s.handleGetCurrentGlucose(ctx, args)
	})

	sdkmcp.AddTool(s.mcpServer, &sdkmcp.Tool{
		Name:        "get_glucose_history",
		Description: "Get historical glucose readings for a specified time window (max 30 days).",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args getDateRangeInput) (*sdkmcp.CallToolResult, any, error) {
		return s.handleGetGlucoseHistory(ctx, args)
	})

	sdkmcp.AddTool(s.mcpServer, &sdkmcp.Tool{
		Name:        "get_trend",
		Description: "Get the current glucose trend arrow, rate of change, and zone classification.",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args struct{}) (*sdkmcp.CallToolResult, any, error) {
		return s.handleGetTrend(ctx)
	})

	sdkmcp.AddTool(s.mcpServer, &sdkmcp.Tool{
		Name:        "get_dexcom_events",
		Description: "Get Dexcom-logged events (carbs, insulin, exercise, health) for a time window (max 30 days).",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args getDateRangeInput) (*sdkmcp.CallToolResult, any, error) {
		return s.handleGetDexcomEvents(ctx, args)
	})

	sdkmcp.AddTool(s.mcpServer, &sdkmcp.Tool{
		Name:        "get_calibrations",
		Description: "Get fingerstick calibration records for a time window (max 30 days).",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args getDateRangeInput) (*sdkmcp.CallToolResult, any, error) {
		return s.handleGetCalibrations(ctx, args)
	})

	sdkmcp.AddTool(s.mcpServer, &sdkmcp.Tool{
		Name:        "get_alerts",
		Description: "Get CGM alert events (high, low, urgentLow, rise, fall, etc.) for a time window (max 30 days).",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args getDateRangeInput) (*sdkmcp.CallToolResult, any, error) {
		return s.handleGetAlerts(ctx, args)
	})

	sdkmcp.AddTool(s.mcpServer, &sdkmcp.Tool{
		Name:        "get_devices",
		Description: "Get Dexcom G7 device and transmitter information for the authenticated user.",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args struct{}) (*sdkmcp.CallToolResult, any, error) {
		return s.handleGetDevices(ctx)
	})

	sdkmcp.AddTool(s.mcpServer, &sdkmcp.Tool{
		Name:        "get_data_range",
		Description: "Get the earliest and latest record timestamps for each data type (EGVs, events, calibrations).",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args struct{}) (*sdkmcp.CallToolResult, any, error) {
		return s.handleGetDataRange(ctx)
	})

	sdkmcp.AddTool(s.mcpServer, &sdkmcp.Tool{
		Name:        "log_meal",
		Description: "Log a meal to local storage with optional nutritional estimates (carbs, protein, fat in grams).",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args logMealInput) (*sdkmcp.CallToolResult, any, error) {
		return s.handleLogMeal(ctx, args)
	})

	sdkmcp.AddTool(s.mcpServer, &sdkmcp.Tool{
		Name:        "log_exercise",
		Description: "Log an exercise session to local storage. intensity: low, moderate, moderate_high, high, or max.",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args logExerciseInput) (*sdkmcp.CallToolResult, any, error) {
		return s.handleLogExercise(ctx, args)
	})

	sdkmcp.AddTool(s.mcpServer, &sdkmcp.Tool{
		Name:        "rate_meal_impact",
		Description: "Assess the glucose impact of a previously logged meal using post-meal CGM data. Returns a 1–10 rating.",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args rateMealImpactInput) (*sdkmcp.CallToolResult, any, error) {
		return s.handleRateMealImpact(ctx, args)
	})
}
