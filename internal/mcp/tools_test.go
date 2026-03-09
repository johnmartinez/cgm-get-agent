// Scenario-based integration tests for the MCP tool handlers.
// Each test maps to one of the 10 scenarios from SPEC §7 / CLAUDE.md Phase 6.
// Tests are in package mcp to access unexported handler methods and struct fields.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/johnmartinez/cgm-get-agent/internal/config"
	"github.com/johnmartinez/cgm-get-agent/internal/crypto"
	"github.com/johnmartinez/cgm-get-agent/internal/dexcom"
	"github.com/johnmartinez/cgm-get-agent/internal/store"
	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

// dexcomFmt is the Dexcom API timestamp format used in JSON responses.
const dexcomFmt = "2006-01-02T15:04:05"

// testEncKey is a deterministic 32-byte key for encrypting tokens in tests.
var testEncKey = bytes.Repeat([]byte{0xAB}, 32)

// testZones returns default glucose zone thresholds.
func testZones() config.GlucoseZones {
	return config.GlucoseZones{Low: 70, TargetLow: 80, TargetHigh: 120, Elevated: 140, High: 180}
}

// newTestServer builds a *Server wired to the given httptest.Server.
// A fresh access token is pre-seeded so no OAuth refresh is triggered.
func newTestServer(t *testing.T, srv *httptest.Server) *Server {
	t.Helper()
	tokenPath := filepath.Join(t.TempDir(), "tokens.enc")

	tok := types.OAuthTokens{
		AccessToken:   "test-bearer",
		RefreshToken:  "test-refresh",
		ExpiresAt:     time.Now().UTC().Add(2 * time.Hour),
		LastRefreshed: time.Now().UTC(),
	}
	if err := crypto.SaveTokens(tokenPath, tok, testEncKey); err != nil {
		t.Fatalf("newTestServer: saving tokens: %v", err)
	}

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("newTestServer: opening store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	client, oauth := dexcom.NewClientForTest(srv.URL, tokenPath, testEncKey, srv.Client())

	cfg := &config.Config{}
	cfg.GlucoseZones = testZones()

	return &Server{
		cfg:    cfg,
		store:  st,
		oauth:  oauth,
		client: client,
	}
}

// textContent extracts the text from the first Content element of a tool result.
func textContent(t *testing.T, result *sdkmcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty Content in tool result")
	}
	tc, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("expected *sdkmcp.TextContent, got %T", result.Content[0])
	}
	return tc.Text
}

// ptr is a generic helper to take the address of a value.
func ptr[T any](v T) *T { return &v }

// --- Mock response helpers ---

// mockEGVsJSON returns a JSON string with count EGV records ending at baseTime.
// Records are spaced 5 minutes apart, ascending.
func mockEGVsJSON(baseTime time.Time, values []int) string {
	type egv struct {
		RecordID    string  `json:"recordId"`
		SystemTime  string  `json:"systemTime"`
		DisplayTime string  `json:"displayTime"`
		Value       int     `json:"value"`
		Trend       string  `json:"trend"`
		TrendRate   float64 `json:"trendRate"`
		Unit        string  `json:"unit"`
		RateUnit    string  `json:"rateUnit"`
	}
	type envelope struct {
		EGVs []egv `json:"records"`
	}

	env := envelope{}
	for i, v := range values {
		t := baseTime.Add(time.Duration(i) * 5 * time.Minute)
		env.EGVs = append(env.EGVs, egv{
			RecordID:    "r-" + t.Format("150405"),
			SystemTime:  t.Format(dexcomFmt),
			DisplayTime: t.Format(dexcomFmt),
			Value:       v,
			Trend:       "flat",
			Unit:        "mg/dL",
			RateUnit:    "mg/dL/min",
		})
	}
	b, _ := json.Marshal(env)
	return string(b)
}

// --- Scenario 1: Simple Glucose Check ---
// Mock Dexcom returns two EGVs; verify GlucoseSnapshot shape.

func TestScenario1_SimpleGlucoseCheck(t *testing.T) {
	now := time.Now().UTC()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockEGVsJSON(now.Add(-10*time.Minute), []int{95, 100})))
	}))
	defer srv.Close()

	s := newTestServer(t, srv)

	result, _, err := s.handleGetCurrentGlucose(context.Background(), getCurrentGlucoseInput{HistoryMinutes: 60})
	if err != nil {
		t.Fatalf("handleGetCurrentGlucose: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", textContent(t, result))
	}

	var snap types.GlucoseSnapshot
	if err := json.Unmarshal([]byte(textContent(t, result)), &snap); err != nil {
		t.Fatalf("unmarshaling snapshot: %v", err)
	}

	if snap.Current.Value == 0 {
		t.Error("current glucose must be non-zero")
	}
	if len(snap.History) < 2 {
		t.Errorf("expected at least 2 history records, got %d", len(snap.History))
	}
	// History must be sorted ascending by SystemTime.
	for i := 1; i < len(snap.History); i++ {
		if snap.History[i].SystemTime.Before(snap.History[i-1].SystemTime) {
			t.Error("history is not sorted ascending by SystemTime")
			break
		}
	}
	if snap.Peak == nil || snap.Trough == nil || snap.Baseline == nil {
		t.Error("snapshot must have Peak, Trough, and Baseline set")
	}
}

// --- Scenario 2: Meal Logging + Glucose Context ---
// Verify SQLite insert via handleLogMeal; verify glucose fetch via handleGetGlucoseHistory.

func TestScenario2_MealLoggingAndGlucoseContext(t *testing.T) {
	now := time.Now().UTC()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockEGVsJSON(now.Add(-30*time.Minute), []int{90, 92, 95})))
	}))
	defer srv.Close()

	s := newTestServer(t, srv)

	// Log a meal.
	mealResult, _, err := s.handleLogMeal(context.Background(), logMealInput{
		Description: "oatmeal with berries",
		CarbsEst:    ptr(55.0),
		ProteinEst:  ptr(8.0),
	})
	if err != nil {
		t.Fatalf("handleLogMeal: %v", err)
	}
	if mealResult.IsError {
		t.Fatalf("handleLogMeal error: %s", textContent(t, mealResult))
	}

	var meal types.Meal
	if err := json.Unmarshal([]byte(textContent(t, mealResult)), &meal); err != nil {
		t.Fatalf("unmarshaling meal: %v", err)
	}
	if !strings.HasPrefix(meal.ID, "m_") {
		t.Errorf("meal ID must start with m_, got %q", meal.ID)
	}
	if meal.Description != "oatmeal with berries" {
		t.Errorf("meal description mismatch: got %q", meal.Description)
	}
	if meal.CarbsEst == nil || *meal.CarbsEst != 55.0 {
		t.Error("carbs_est not persisted")
	}

	// Fetch glucose history for the same window.
	start := now.Add(-time.Hour).Format("2006-01-02")
	end := now.Format("2006-01-02")
	histResult, _, err := s.handleGetGlucoseHistory(context.Background(), getDateRangeInput{
		StartDate: start,
		EndDate:   end,
	})
	if err != nil {
		t.Fatalf("handleGetGlucoseHistory: %v", err)
	}
	if histResult.IsError {
		t.Fatalf("handleGetGlucoseHistory error: %s", textContent(t, histResult))
	}

	var egvs []types.EGVRecord
	if err := json.Unmarshal([]byte(textContent(t, histResult)), &egvs); err != nil {
		t.Fatalf("unmarshaling EGVs: %v", err)
	}
	if len(egvs) == 0 {
		t.Error("expected at least one EGV record")
	}
}

// --- Scenario 3: Meal Impact Rating ---
// Known EGV curve: baseline 90, spike to 140, recovery to 95 → spike_delta 50 → rating 7.

func TestScenario3_MealImpactRating(t *testing.T) {
	mealTime := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	type egv struct {
		RecordID    string `json:"recordId"`
		SystemTime  string `json:"systemTime"`
		DisplayTime string `json:"displayTime"`
		Value       int    `json:"value"`
		Trend       string `json:"trend"`
		Unit        string `json:"unit"`
		RateUnit    string `json:"rateUnit"`
	}
	egvs := []egv{
		{RecordID: "pre", SystemTime: mealTime.Format(dexcomFmt), DisplayTime: mealTime.Format(dexcomFmt), Value: 90, Trend: "flat", Unit: "mg/dL", RateUnit: "mg/dL/min"},
		{RecordID: "peak", SystemTime: mealTime.Add(45 * time.Minute).Format(dexcomFmt), DisplayTime: mealTime.Add(45 * time.Minute).Format(dexcomFmt), Value: 140, Trend: "singleUp", Unit: "mg/dL", RateUnit: "mg/dL/min"},
		{RecordID: "rec", SystemTime: mealTime.Add(120 * time.Minute).Format(dexcomFmt), DisplayTime: mealTime.Add(120 * time.Minute).Format(dexcomFmt), Value: 95, Trend: "flat", Unit: "mg/dL", RateUnit: "mg/dL/min"},
	}
	body, _ := json.Marshal(map[string]any{"records": egvs})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	s := newTestServer(t, srv)

	// Save the meal directly to the store.
	savedMeal, err := s.store.SaveMeal(types.Meal{
		ID:          types.MealID(mealTime),
		Description: "pasta bolognese",
		Timestamp:   mealTime,
	})
	if err != nil {
		t.Fatalf("SaveMeal: %v", err)
	}

	result, _, err := s.handleRateMealImpact(context.Background(), rateMealImpactInput{MealID: savedMeal.ID})
	if err != nil {
		t.Fatalf("handleRateMealImpact: %v", err)
	}
	if result.IsError {
		t.Fatalf("handleRateMealImpact error: %s", textContent(t, result))
	}

	var assessment types.MealImpactAssessment
	if err := json.Unmarshal([]byte(textContent(t, result)), &assessment); err != nil {
		t.Fatalf("unmarshaling assessment: %v", err)
	}

	if assessment.SpikeDelta != 50 {
		t.Errorf("spike_delta: got %d, want 50", assessment.SpikeDelta)
	}
	if assessment.Rating != 7 {
		t.Errorf("rating: got %d, want 7 (spike_delta ≤50)", assessment.Rating)
	}
	if assessment.PreMealGlucose != 90 {
		t.Errorf("pre_meal_glucose: got %d, want 90", assessment.PreMealGlucose)
	}
	if assessment.PeakGlucose != 140 {
		t.Errorf("peak_glucose: got %d, want 140", assessment.PeakGlucose)
	}
	if assessment.TimeToPeakMin != 45 {
		t.Errorf("time_to_peak_min: got %d, want 45", assessment.TimeToPeakMin)
	}
}

// --- Scenario 4: Exercise + Glucose Correlation ---
// Log an exercise session; verify saved with correct ID; fetch history.

func TestScenario4_ExerciseLogging(t *testing.T) {
	now := time.Now().UTC()
	exerciseTime := now.Add(-45 * time.Minute)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockEGVsJSON(now.Add(-60*time.Minute), []int{105, 100, 95, 98})))
	}))
	defer srv.Close()

	s := newTestServer(t, srv)

	exResult, _, err := s.handleLogExercise(context.Background(), logExerciseInput{
		Type:        "running",
		DurationMin: 30,
		Intensity:   "moderate",
		Timestamp:   exerciseTime.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("handleLogExercise: %v", err)
	}
	if exResult.IsError {
		t.Fatalf("handleLogExercise error: %s", textContent(t, exResult))
	}

	var ex types.Exercise
	if err := json.Unmarshal([]byte(textContent(t, exResult)), &ex); err != nil {
		t.Fatalf("unmarshaling exercise: %v", err)
	}
	if !strings.HasPrefix(ex.ID, "e_") {
		t.Errorf("exercise ID must start with e_, got %q", ex.ID)
	}
	if ex.Type != "running" {
		t.Errorf("type: got %q, want running", ex.Type)
	}
	if ex.DurationMin != 30 {
		t.Errorf("duration_min: got %d, want 30", ex.DurationMin)
	}
	if string(ex.Intensity) != "moderate" {
		t.Errorf("intensity: got %q, want moderate", ex.Intensity)
	}

	// Confirm glucose history fetch succeeds alongside the exercise record.
	start := now.Add(-2 * time.Hour).Format("2006-01-02")
	end := now.Format("2006-01-02")
	histResult, _, err := s.handleGetGlucoseHistory(context.Background(), getDateRangeInput{StartDate: start, EndDate: end})
	if err != nil {
		t.Fatalf("handleGetGlucoseHistory: %v", err)
	}
	if histResult.IsError {
		t.Fatalf("handleGetGlucoseHistory error: %s", textContent(t, histResult))
	}
}

// --- Scenario 5: Reading Dexcom App Events ---
// Mock events endpoint returns carbs + insulin events; verify DexcomEvent list.

func TestScenario5_DexcomAppEvents(t *testing.T) {
	base := time.Date(2026, 3, 4, 8, 0, 0, 0, time.UTC)
	carbVal := 60.0
	insulinVal := 3.5
	subType := "rapidActing"

	type apiEvt struct {
		RecordID     string   `json:"recordId"`
		SystemTime   string   `json:"systemTime"`
		DisplayTime  string   `json:"displayTime"`
		EventType    string   `json:"eventType"`
		EventSubType *string  `json:"eventSubType,omitempty"`
		Value        *float64 `json:"value,omitempty"`
		Unit         string   `json:"unit"`
	}
	resp := map[string]any{"records": []apiEvt{
		{RecordID: "ev-1", SystemTime: base.Format(dexcomFmt), DisplayTime: base.Format(dexcomFmt), EventType: "carbs", Value: &carbVal, Unit: "grams"},
		{RecordID: "ev-2", SystemTime: base.Add(5 * time.Minute).Format(dexcomFmt), DisplayTime: base.Add(5 * time.Minute).Format(dexcomFmt), EventType: "insulin", EventSubType: &subType, Value: &insulinVal, Unit: "units"},
	}}
	body, _ := json.Marshal(resp)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	s := newTestServer(t, srv)

	result, _, err := s.handleGetDexcomEvents(context.Background(), getDateRangeInput{
		StartDate: "2026-03-04",
		EndDate:   "2026-03-05",
	})
	if err != nil {
		t.Fatalf("handleGetDexcomEvents: %v", err)
	}
	if result.IsError {
		t.Fatalf("handleGetDexcomEvents error: %s", textContent(t, result))
	}

	var events []types.DexcomEvent
	if err := json.Unmarshal([]byte(textContent(t, result)), &events); err != nil {
		t.Fatalf("unmarshaling events: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].EventType != types.EventTypeCarbs {
		t.Errorf("event[0] type: got %q, want carbs", events[0].EventType)
	}
	if events[1].EventType != types.EventTypeInsulin {
		t.Errorf("event[1] type: got %q, want insulin", events[1].EventType)
	}
	if events[1].EventSubType == nil || *events[1].EventSubType != types.EventSubTypeInsulinRapidActing {
		t.Error("event[1] EventSubType must be rapidActing")
	}
}

// --- Scenario 6: Alert History Review ---
// Mock alerts endpoint returns high + urgentLow alerts; verify AlertRecord list.

func TestScenario6_AlertHistoryReview(t *testing.T) {
	base := time.Date(2026, 3, 4, 3, 0, 0, 0, time.UTC)

	type apiAlert struct {
		RecordID    string `json:"recordId"`
		SystemTime  string `json:"systemTime"`
		DisplayTime string `json:"displayTime"`
		AlertName   string `json:"alertName"`
		AlertState  string `json:"alertState"`
	}
	resp := map[string]any{"records": []apiAlert{
		{RecordID: "al-1", SystemTime: base.Format(dexcomFmt), DisplayTime: base.Format(dexcomFmt), AlertName: "high", AlertState: "triggered"},
		{RecordID: "al-2", SystemTime: base.Add(30 * time.Minute).Format(dexcomFmt), DisplayTime: base.Add(30 * time.Minute).Format(dexcomFmt), AlertName: "urgentLow", AlertState: "triggered"},
	}}
	body, _ := json.Marshal(resp)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	s := newTestServer(t, srv)

	result, _, err := s.handleGetAlerts(context.Background(), getDateRangeInput{
		StartDate: "2026-03-04",
		EndDate:   "2026-03-05",
	})
	if err != nil {
		t.Fatalf("handleGetAlerts: %v", err)
	}
	if result.IsError {
		t.Fatalf("handleGetAlerts error: %s", textContent(t, result))
	}

	var alerts []types.AlertRecord
	if err := json.Unmarshal([]byte(textContent(t, result)), &alerts); err != nil {
		t.Fatalf("unmarshaling alerts: %v", err)
	}

	if len(alerts) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(alerts))
	}
	if alerts[0].AlertName != types.AlertTypeHigh {
		t.Errorf("alert[0] name: got %q, want high", alerts[0].AlertName)
	}
	if alerts[1].AlertName != types.AlertTypeUrgentLow {
		t.Errorf("alert[1] name: got %q, want urgentLow", alerts[1].AlertName)
	}
	if alerts[0].AlertState != types.AlertStateTriggered {
		t.Errorf("alert[0] state: got %q, want triggered", alerts[0].AlertState)
	}
}

// --- Scenario 7: Fingerstick Calibration Review ---
// Mock calibrations endpoint returns two records; verify CalibrationRecord list.

func TestScenario7_CalibrationReview(t *testing.T) {
	base := time.Date(2026, 3, 4, 7, 0, 0, 0, time.UTC)

	type apiCal struct {
		RecordID              string `json:"recordId"`
		SystemTime            string `json:"systemTime"`
		DisplayTime           string `json:"displayTime"`
		Value                 int    `json:"value"`
		Unit                  string `json:"unit"`
		TransmitterID         string `json:"transmitterId"`
		TransmitterGeneration string `json:"transmitterGeneration"`
		DisplayDevice         string `json:"displayDevice"`
		DisplayApp            string `json:"displayApp"`
	}
	resp := map[string]any{"records": []apiCal{
		{RecordID: "cal-1", SystemTime: base.Format(dexcomFmt), DisplayTime: base.Format(dexcomFmt), Value: 108, Unit: "mg/dL", TransmitterID: "tx-1", TransmitterGeneration: "g7", DisplayDevice: "iOS", DisplayApp: "G7"},
		{RecordID: "cal-2", SystemTime: base.Add(8 * time.Hour).Format(dexcomFmt), DisplayTime: base.Add(8 * time.Hour).Format(dexcomFmt), Value: 112, Unit: "mg/dL", TransmitterID: "tx-1", TransmitterGeneration: "g7", DisplayDevice: "iOS", DisplayApp: "G7"},
	}}
	body, _ := json.Marshal(resp)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	s := newTestServer(t, srv)

	result, _, err := s.handleGetCalibrations(context.Background(), getDateRangeInput{
		StartDate: "2026-03-04",
		EndDate:   "2026-03-05",
	})
	if err != nil {
		t.Fatalf("handleGetCalibrations: %v", err)
	}
	if result.IsError {
		t.Fatalf("handleGetCalibrations error: %s", textContent(t, result))
	}

	var cals []types.CalibrationRecord
	if err := json.Unmarshal([]byte(textContent(t, result)), &cals); err != nil {
		t.Fatalf("unmarshaling calibrations: %v", err)
	}

	if len(cals) != 2 {
		t.Fatalf("expected 2 calibrations, got %d", len(cals))
	}
	if cals[0].Value != 108 {
		t.Errorf("cal[0] value: got %d, want 108", cals[0].Value)
	}
	if cals[1].Value != 112 {
		t.Errorf("cal[1] value: got %d, want 112", cals[1].Value)
	}
	if cals[0].TransmitterGeneration != "g7" {
		t.Errorf("cal[0] transmitter generation: got %q, want g7", cals[0].TransmitterGeneration)
	}
}

// --- Scenario 9: Graceful Degradation ---
// Mock Dexcom returns 503; verify cache fallback with stale_data_notice.

func TestScenario9_GracefulDegradation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	s := newTestServer(t, srv)

	// Pre-seed the glucose cache with recent EGVs (within the 60-min window).
	now := time.Now().UTC()
	cached := []types.EGVRecord{
		{RecordID: "cached-1", SystemTime: now.Add(-8 * time.Minute), Value: 115, Trend: types.TrendFlat, Unit: "mg/dL"},
		{RecordID: "cached-2", SystemTime: now.Add(-3 * time.Minute), Value: 118, Trend: types.TrendFlat, Unit: "mg/dL"},
	}
	if _, err := s.store.CacheEGVs(cached); err != nil {
		t.Fatalf("CacheEGVs: %v", err)
	}

	result, _, err := s.handleGetCurrentGlucose(context.Background(), getCurrentGlucoseInput{HistoryMinutes: 60})
	if err != nil {
		t.Fatalf("handleGetCurrentGlucose: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected successful cache fallback, got error: %s", textContent(t, result))
	}

	var snap types.GlucoseSnapshot
	if err := json.Unmarshal([]byte(textContent(t, result)), &snap); err != nil {
		t.Fatalf("unmarshaling snapshot: %v", err)
	}

	if snap.DataDelayNotice == nil {
		t.Fatal("expected DataDelayNotice to be set for cache fallback")
	}
	if !strings.Contains(*snap.DataDelayNotice, "cache") {
		t.Errorf("DataDelayNotice should mention cache, got %q", *snap.DataDelayNotice)
	}
	if snap.Current.Value != 118 {
		t.Errorf("current value from cache: got %d, want 118", snap.Current.Value)
	}
}

// --- Additional tool handler edge cases ---

func TestLogMeal_MissingDescription_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	s := newTestServer(t, srv)
	result, _, err := s.handleLogMeal(context.Background(), logMealInput{Description: ""})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing description")
	}
}

func TestLogExercise_InvalidIntensity_Accepted(t *testing.T) {
	// Intensity is a string; the handler stores whatever value is passed.
	// Validation of known intensity values is the client's responsibility.
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	s := newTestServer(t, srv)
	result, _, err := s.handleLogExercise(context.Background(), logExerciseInput{
		Type:        "yoga",
		DurationMin: 45,
		Intensity:   "low",
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected tool error: %s", textContent(t, result))
	}
}

func TestRateMealImpact_MealNotFound_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	s := newTestServer(t, srv)
	result, _, err := s.handleRateMealImpact(context.Background(), rateMealImpactInput{MealID: "m_99999999_0000"})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for non-existent meal")
	}
	if !strings.Contains(textContent(t, result), "MealNotFoundError") {
		t.Errorf("expected MealNotFoundError, got: %s", textContent(t, result))
	}
}

func TestGetTrend_NoData_ReturnsError(t *testing.T) {
	// Mock returns empty EGV list.
	type envelope struct {
		EGVs []struct{} `json:"records"`
	}
	body, _ := json.Marshal(envelope{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	s := newTestServer(t, srv)
	result, _, err := s.handleGetTrend(context.Background())
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true when no EGVs available")
	}
}

func TestParseDate_AcceptsRFC3339AndYYYYMMDD(t *testing.T) {
	cases := []struct {
		input   string
		wantErr bool
	}{
		{"2026-03-04T12:00:00Z", false},
		{"2026-03-04", false},
		{"not-a-date", true},
		{"", true},
	}
	for _, tc := range cases {
		_, err := parseDate(tc.input)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseDate(%q): wantErr=%v, got %v", tc.input, tc.wantErr, err)
		}
	}
}
