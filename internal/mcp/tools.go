package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/johnmartinez/cgm-get-agent/internal/analyzer"
	"github.com/johnmartinez/cgm-get-agent/internal/dexcom"
	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

// --- Input structs ---

type getCurrentGlucoseInput struct {
	IncludeTrend   bool `json:"include_trend,omitempty"`
	HistoryMinutes int  `json:"history_minutes,omitempty"`
}

// getDateRangeInput is shared by get_glucose_history, get_dexcom_events,
// get_calibrations, and get_alerts — all accept start_date and end_date.
type getDateRangeInput struct {
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
}

type logMealInput struct {
	Description string   `json:"description"`
	CarbsEst    *float64 `json:"carbs_est,omitempty"`
	ProteinEst  *float64 `json:"protein_est,omitempty"`
	FatEst      *float64 `json:"fat_est,omitempty"`
	Timestamp   string   `json:"timestamp,omitempty"`
	Notes       *string  `json:"notes,omitempty"`
}

type logExerciseInput struct {
	Type        string  `json:"type"`
	DurationMin int     `json:"duration_min"`
	Intensity   string  `json:"intensity"`
	Timestamp   string  `json:"timestamp,omitempty"`
	Notes       *string `json:"notes,omitempty"`
}

type rateMealImpactInput struct {
	MealID string `json:"meal_id"`
}

// --- Response helpers ---

type toolError struct {
	Error    string `json:"error"`
	Message  string `json:"message"`
	Retriable bool  `json:"retriable"`
}

func errResult(errType, msg string, retriable bool) (*sdkmcp.CallToolResult, any, error) {
	b, _ := json.Marshal(toolError{Error: errType, Message: msg, Retriable: retriable})
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(b)}},
		IsError: true,
	}, nil, nil
}

func jsonResult(v any) (*sdkmcp.CallToolResult, any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return errResult("InternalError", "failed to marshal response: "+err.Error(), false)
	}
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(b)}},
	}, nil, nil
}

// parseDate accepts RFC3339 or YYYY-MM-DD.
func parseDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid date %q: use RFC3339 or YYYY-MM-DD", s)
}

// classifyDexcomError maps dexcom package errors to structured tool error results.
func classifyDexcomError(err error) (*sdkmcp.CallToolResult, any, error) {
	var authErr *dexcom.AuthError
	if errors.As(err, &authErr) {
		return errResult("DexcomAuthError", authErr.Message, false)
	}
	var timeoutErr *dexcom.TimeoutError
	if errors.As(err, &timeoutErr) {
		return errResult("DexcomTimeoutError", timeoutErr.Error(), true)
	}
	var apiErr *dexcom.APIError
	if errors.As(err, &apiErr) {
		return errResult("DexcomAPIError", apiErr.Error(), apiErr.StatusCode >= 500)
	}
	var windowErr *dexcom.WindowTooLargeError
	if errors.As(err, &windowErr) {
		return errResult("WindowTooLargeError", windowErr.Error(), false)
	}
	return errResult("DexcomAPIError", err.Error(), true)
}

// --- Tool handlers ---

func (s *Server) handleGetCurrentGlucose(ctx context.Context, args getCurrentGlucoseInput) (*sdkmcp.CallToolResult, any, error) {
	slog.Error("TOOL HANDLER ENTERED", "tool", "get_current_glucose")
	defer func() {
		if r := recover(); r != nil {
			slog.Error("TOOL HANDLER PANIC", "tool", "get_current_glucose", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	histMinutes := args.HistoryMinutes
	if histMinutes <= 0 {
		histMinutes = 60
	}
	end := time.Now().UTC()
	start := end.Add(-time.Duration(histMinutes) * time.Minute)

	egvs, err := s.client.GetEGVs(ctx, start, end)
	if err != nil {
		// Graceful degradation: on 5xx or timeout, fall back to cached EGVs.
		var apiErr *dexcom.APIError
		var timeoutErr *dexcom.TimeoutError
		isServerErr := errors.As(err, &apiErr) && apiErr.StatusCode >= 500
		isTimeout := errors.As(err, &timeoutErr)
		if isServerErr || isTimeout {
			cached, cacheErr := s.store.GetCachedEGVs(start, end)
			if cacheErr == nil && len(cached) > 0 {
				snapshot, snapErr := analyzer.ComputeSnapshot(cached, s.cfg.GlucoseZones)
				if snapErr == nil {
					notice := "data from local cache (Dexcom API temporarily unavailable)"
					snapshot.DataDelayNotice = &notice
					return jsonResult(snapshot)
				}
			}
			return errResult("DexcomAPIError", "Dexcom API unavailable and no recent cached data — retry later", true)
		}
		return classifyDexcomError(err)
	}

	if len(egvs) == 0 {
		return errResult("NoDataError", "no glucose readings found in the requested window", true)
	}

	// Cache fresh EGVs for future graceful degradation.
	_, _ = s.store.CacheEGVs(egvs)

	snapshot, err := analyzer.ComputeSnapshot(egvs, s.cfg.GlucoseZones)
	if err != nil {
		return errResult("NoDataError", err.Error(), true)
	}
	return jsonResult(snapshot)
}

func (s *Server) handleGetGlucoseHistory(ctx context.Context, args getDateRangeInput) (*sdkmcp.CallToolResult, any, error) {
	slog.Error("TOOL HANDLER ENTERED", "tool", "get_glucose_history")
	defer func() {
		if r := recover(); r != nil {
			slog.Error("TOOL HANDLER PANIC", "tool", "get_glucose_history", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	start, err := parseDate(args.StartDate)
	if err != nil {
		return errResult("InvalidInput", "start_date: "+err.Error(), false)
	}
	end, err := parseDate(args.EndDate)
	if err != nil {
		return errResult("InvalidInput", "end_date: "+err.Error(), false)
	}

	egvs, err := s.client.GetEGVs(ctx, start, end)
	if err != nil {
		return classifyDexcomError(err)
	}
	return jsonResult(egvs)
}

func (s *Server) handleGetTrend(ctx context.Context) (*sdkmcp.CallToolResult, any, error) {
	slog.Error("TOOL HANDLER ENTERED", "tool", "get_trend")
	defer func() {
		if r := recover(); r != nil {
			slog.Error("TOOL HANDLER PANIC", "tool", "get_trend", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	end := time.Now().UTC()
	start := end.Add(-15 * time.Minute)

	egvs, err := s.client.GetEGVs(ctx, start, end)
	if err != nil {
		return classifyDexcomError(err)
	}
	if len(egvs) == 0 {
		return errResult("NoDataError", "no recent glucose readings available for trend", true)
	}

	latest := egvs[0]
	for _, r := range egvs[1:] {
		if r.SystemTime.After(latest.SystemTime) {
			latest = r
		}
	}

	zone := analyzer.ClassifyZone(latest.Value, s.cfg.GlucoseZones)
	return jsonResult(map[string]any{
		"trend":     latest.Trend,
		"trendRate": latest.TrendRate,
		"value":     latest.Value,
		"timestamp": latest.SystemTime,
		"zone":      zone,
	})
}

func (s *Server) handleGetDexcomEvents(ctx context.Context, args getDateRangeInput) (*sdkmcp.CallToolResult, any, error) {
	slog.Error("TOOL HANDLER ENTERED", "tool", "get_dexcom_events")
	defer func() {
		if r := recover(); r != nil {
			slog.Error("TOOL HANDLER PANIC", "tool", "get_dexcom_events", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	start, err := parseDate(args.StartDate)
	if err != nil {
		return errResult("InvalidInput", "start_date: "+err.Error(), false)
	}
	end, err := parseDate(args.EndDate)
	if err != nil {
		return errResult("InvalidInput", "end_date: "+err.Error(), false)
	}

	events, err := s.client.GetEvents(ctx, start, end)
	if err != nil {
		return classifyDexcomError(err)
	}
	return jsonResult(events)
}

func (s *Server) handleGetCalibrations(ctx context.Context, args getDateRangeInput) (*sdkmcp.CallToolResult, any, error) {
	slog.Error("TOOL HANDLER ENTERED", "tool", "get_calibrations")
	defer func() {
		if r := recover(); r != nil {
			slog.Error("TOOL HANDLER PANIC", "tool", "get_calibrations", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	start, err := parseDate(args.StartDate)
	if err != nil {
		return errResult("InvalidInput", "start_date: "+err.Error(), false)
	}
	end, err := parseDate(args.EndDate)
	if err != nil {
		return errResult("InvalidInput", "end_date: "+err.Error(), false)
	}

	calibrations, err := s.client.GetCalibrations(ctx, start, end)
	if err != nil {
		return classifyDexcomError(err)
	}
	return jsonResult(calibrations)
}

func (s *Server) handleGetAlerts(ctx context.Context, args getDateRangeInput) (*sdkmcp.CallToolResult, any, error) {
	slog.Error("TOOL HANDLER ENTERED", "tool", "get_alerts")
	defer func() {
		if r := recover(); r != nil {
			slog.Error("TOOL HANDLER PANIC", "tool", "get_alerts", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	start, err := parseDate(args.StartDate)
	if err != nil {
		return errResult("InvalidInput", "start_date: "+err.Error(), false)
	}
	end, err := parseDate(args.EndDate)
	if err != nil {
		return errResult("InvalidInput", "end_date: "+err.Error(), false)
	}

	alerts, err := s.client.GetAlerts(ctx, start, end)
	if err != nil {
		return classifyDexcomError(err)
	}
	return jsonResult(alerts)
}

func (s *Server) handleGetDevices(ctx context.Context) (*sdkmcp.CallToolResult, any, error) {
	slog.Error("TOOL HANDLER ENTERED", "tool", "get_devices")
	defer func() {
		if r := recover(); r != nil {
			slog.Error("TOOL HANDLER PANIC", "tool", "get_devices", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	devices, err := s.client.GetDevices(ctx)
	if err != nil {
		return classifyDexcomError(err)
	}
	return jsonResult(devices)
}

func (s *Server) handleGetDataRange(ctx context.Context) (*sdkmcp.CallToolResult, any, error) {
	slog.Error("TOOL HANDLER ENTERED", "tool", "get_data_range")
	defer func() {
		if r := recover(); r != nil {
			slog.Error("TOOL HANDLER PANIC", "tool", "get_data_range", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	dr, err := s.client.GetDataRange(ctx)
	if err != nil {
		return classifyDexcomError(err)
	}
	return jsonResult(dr)
}

func (s *Server) handleLogMeal(ctx context.Context, args logMealInput) (*sdkmcp.CallToolResult, any, error) {
	slog.Error("TOOL HANDLER ENTERED", "tool", "log_meal")
	defer func() {
		if r := recover(); r != nil {
			slog.Error("TOOL HANDLER PANIC", "tool", "log_meal", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if args.Description == "" {
		return errResult("InvalidInput", "description is required", false)
	}

	ts := time.Now().UTC()
	if args.Timestamp != "" {
		var err error
		ts, err = parseDate(args.Timestamp)
		if err != nil {
			return errResult("InvalidInput", "timestamp: "+err.Error(), false)
		}
	}

	meal := types.Meal{
		ID:          types.MealID(ts),
		Description: args.Description,
		CarbsEst:    args.CarbsEst,
		ProteinEst:  args.ProteinEst,
		FatEst:      args.FatEst,
		Timestamp:   ts,
		Notes:       args.Notes,
	}
	saved, err := s.store.SaveMeal(meal)
	if err != nil {
		return errResult("StorageError", "failed to save meal: "+err.Error(), false)
	}
	return jsonResult(saved)
}

func (s *Server) handleLogExercise(_ context.Context, args logExerciseInput) (*sdkmcp.CallToolResult, any, error) {
	slog.Error("TOOL HANDLER ENTERED", "tool", "log_exercise")
	defer func() {
		if r := recover(); r != nil {
			slog.Error("TOOL HANDLER PANIC", "tool", "log_exercise", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if args.Type == "" {
		return errResult("InvalidInput", "type is required", false)
	}
	if args.DurationMin <= 0 {
		return errResult("InvalidInput", "duration_min must be positive", false)
	}
	if args.Intensity == "" {
		return errResult("InvalidInput", "intensity is required (low, moderate, moderate_high, high, max)", false)
	}

	ts := time.Now().UTC()
	if args.Timestamp != "" {
		var err error
		ts, err = parseDate(args.Timestamp)
		if err != nil {
			return errResult("InvalidInput", "timestamp: "+err.Error(), false)
		}
	}

	exercise := types.Exercise{
		ID:          types.ExerciseID(ts),
		Type:        args.Type,
		DurationMin: args.DurationMin,
		Intensity:   types.ExerciseIntensity(args.Intensity),
		Timestamp:   ts,
		Notes:       args.Notes,
	}
	saved, err := s.store.SaveExercise(exercise)
	if err != nil {
		return errResult("StorageError", "failed to save exercise: "+err.Error(), false)
	}
	return jsonResult(saved)
}

func (s *Server) handleRateMealImpact(ctx context.Context, args rateMealImpactInput) (*sdkmcp.CallToolResult, any, error) {
	slog.Error("TOOL HANDLER ENTERED", "tool", "rate_meal_impact")
	defer func() {
		if r := recover(); r != nil {
			slog.Error("TOOL HANDLER PANIC", "tool", "rate_meal_impact", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if args.MealID == "" {
		return errResult("InvalidInput", "meal_id is required", false)
	}

	meal, err := s.store.GetMeal(args.MealID)
	if err != nil {
		return errResult("MealNotFoundError", fmt.Sprintf("meal %q not found", args.MealID), false)
	}

	// Fetch EGVs from 5 min before meal through 3h after (pre-meal baseline + full post-meal window).
	egvStart := meal.Timestamp.Add(-5 * time.Minute)
	egvEnd := meal.Timestamp.Add(180 * time.Minute)

	egvs, err := s.client.GetEGVs(ctx, egvStart, egvEnd)
	if err != nil {
		return classifyDexcomError(err)
	}
	if len(egvs) == 0 {
		return errResult("InsufficientDataError", "no glucose data available for the post-meal window", true)
	}

	exercises, _ := s.store.ListExercise(meal.Timestamp, egvEnd)

	assessment, err := analyzer.AssessMealImpact(meal, egvs, exercises)
	if err != nil {
		return errResult("InsufficientDataError", err.Error(), true)
	}
	return jsonResult(assessment)
}
