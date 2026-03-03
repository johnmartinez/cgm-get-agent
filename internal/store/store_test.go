package store_test

import (
	"testing"
	"time"

	"github.com/johnmartinez/cgm-get-agent/internal/store"
	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ptr[T any](v T) *T { return &v }

// --- Migrations ---

func TestOpen_MigrationsIdempotent(t *testing.T) {
	s := testStore(t)
	// Ping confirms DB is still healthy after migrations.
	if err := s.Ping(); err != nil {
		t.Errorf("Ping after Open: %v", err)
	}
}

// --- Meal CRUD ---

func TestSaveMeal_GeneratesID(t *testing.T) {
	s := testStore(t)
	ts := time.Date(2026, 3, 3, 12, 15, 0, 0, time.UTC)
	m := types.Meal{Description: "tacos", Timestamp: ts}

	saved, err := s.SaveMeal(m)
	if err != nil {
		t.Fatalf("SaveMeal: %v", err)
	}
	if saved.ID != "m_20260303_1215" {
		t.Errorf("expected ID m_20260303_1215, got %q", saved.ID)
	}
	if saved.LoggedAt.IsZero() {
		t.Error("LoggedAt must be set on save")
	}
}

func TestGetMeal_RoundTrip(t *testing.T) {
	s := testStore(t)
	ts := time.Date(2026, 3, 3, 12, 15, 0, 0, time.UTC)
	m := types.Meal{
		Description: "carne asada tacos x3 with horchata",
		CarbsEst:    ptr(70.0),
		ProteinEst:  ptr(35.0),
		Notes:       ptr("street tacos from the truck"),
		Timestamp:   ts,
	}

	saved, err := s.SaveMeal(m)
	if err != nil {
		t.Fatalf("SaveMeal: %v", err)
	}

	got, err := s.GetMeal(saved.ID)
	if err != nil {
		t.Fatalf("GetMeal: %v", err)
	}

	if got.Description != m.Description {
		t.Errorf("Description: got %q, want %q", got.Description, m.Description)
	}
	if got.CarbsEst == nil || *got.CarbsEst != 70.0 {
		t.Errorf("CarbsEst mismatch")
	}
	if got.ProteinEst == nil || *got.ProteinEst != 35.0 {
		t.Errorf("ProteinEst mismatch")
	}
	if got.Notes == nil || *got.Notes != "street tacos from the truck" {
		t.Errorf("Notes mismatch")
	}
	if !got.Timestamp.Equal(ts) {
		t.Errorf("Timestamp: got %v, want %v", got.Timestamp, ts)
	}
}

func TestGetMeal_NotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.GetMeal("m_99991231_2359")
	if err == nil {
		t.Fatal("GetMeal on missing ID must return an error")
	}
}

func TestListMeals_TimeRange(t *testing.T) {
	s := testStore(t)
	base := time.Date(2026, 3, 3, 8, 0, 0, 0, time.UTC)

	meals := []types.Meal{
		{Description: "breakfast", Timestamp: base},
		{Description: "lunch", Timestamp: base.Add(4 * time.Hour)},
		{Description: "dinner", Timestamp: base.Add(10 * time.Hour)},
	}
	for _, m := range meals {
		if _, err := s.SaveMeal(m); err != nil {
			t.Fatalf("SaveMeal: %v", err)
		}
	}

	// Query only breakfast and lunch.
	got, err := s.ListMeals(base, base.Add(5*time.Hour))
	if err != nil {
		t.Fatalf("ListMeals: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 meals, got %d", len(got))
	}
	if got[0].Description != "breakfast" || got[1].Description != "lunch" {
		t.Errorf("unexpected order or content: %+v", got)
	}
}

// --- Exercise CRUD ---

func TestSaveExercise_GeneratesID(t *testing.T) {
	s := testStore(t)
	ts := time.Date(2026, 3, 3, 14, 30, 0, 0, time.UTC)
	e := types.Exercise{
		Type:        "run",
		DurationMin: 30,
		Intensity:   types.IntensityModerateHigh,
		Timestamp:   ts,
	}

	saved, err := s.SaveExercise(e)
	if err != nil {
		t.Fatalf("SaveExercise: %v", err)
	}
	if saved.ID != "e_20260303_1430" {
		t.Errorf("expected ID e_20260303_1430, got %q", saved.ID)
	}
}

func TestListExercise_RoundTrip(t *testing.T) {
	s := testStore(t)
	base := time.Date(2026, 3, 3, 6, 0, 0, 0, time.UTC)

	sessions := []types.Exercise{
		{Type: "kettlebell", DurationMin: 20, Intensity: types.IntensityHigh, Timestamp: base},
		{Type: "walk", DurationMin: 45, Intensity: types.IntensityLow, Timestamp: base.Add(8 * time.Hour)},
	}
	for _, e := range sessions {
		if _, err := s.SaveExercise(e); err != nil {
			t.Fatalf("SaveExercise: %v", err)
		}
	}

	got, err := s.ListExercise(base.Add(-time.Minute), base.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("ListExercise: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(got))
	}
	if got[0].Type != "kettlebell" {
		t.Errorf("expected kettlebell first, got %q", got[0].Type)
	}
	if got[1].Intensity != types.IntensityLow {
		t.Errorf("intensity mismatch: got %q", got[1].Intensity)
	}
}

func TestListExercise_Empty(t *testing.T) {
	s := testStore(t)
	got, err := s.ListExercise(time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("ListExercise on empty store: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d", len(got))
	}
}

// --- Glucose Cache ---

func TestCacheEGVs_Upsert(t *testing.T) {
	s := testStore(t)
	base := time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC)

	records := []types.EGVRecord{
		{RecordID: "rec-1", SystemTime: base, DisplayTime: base, Value: 105, Trend: types.TrendFlat, TrendRate: 0.1, Unit: "mg/dL"},
		{RecordID: "rec-2", SystemTime: base.Add(5 * time.Minute), DisplayTime: base.Add(5 * time.Minute), Value: 110, Trend: types.TrendFortyFiveUp, TrendRate: 1.2, Unit: "mg/dL"},
		{RecordID: "rec-3", SystemTime: base.Add(10 * time.Minute), DisplayTime: base.Add(10 * time.Minute), Value: 118, Trend: types.TrendSingleUp, TrendRate: 2.1, Unit: "mg/dL"},
	}

	n, err := s.CacheEGVs(records)
	if err != nil {
		t.Fatalf("CacheEGVs: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 cached, got %d", n)
	}

	// Upsert the same records — should not error or duplicate.
	n2, err := s.CacheEGVs(records)
	if err != nil {
		t.Fatalf("CacheEGVs upsert: %v", err)
	}
	if n2 != 3 {
		t.Errorf("upsert should still report 3, got %d", n2)
	}
}

func TestGetCachedEGVs_TimeRange(t *testing.T) {
	s := testStore(t)
	base := time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC)

	records := []types.EGVRecord{
		{RecordID: "r1", SystemTime: base, Value: 100, Trend: types.TrendFlat},
		{RecordID: "r2", SystemTime: base.Add(5 * time.Minute), Value: 110, Trend: types.TrendFlat},
		{RecordID: "r3", SystemTime: base.Add(10 * time.Minute), Value: 120, Trend: types.TrendFlat},
		{RecordID: "r4", SystemTime: base.Add(15 * time.Minute), Value: 130, Trend: types.TrendFlat},
	}
	if _, err := s.CacheEGVs(records); err != nil {
		t.Fatalf("CacheEGVs: %v", err)
	}

	// Request only r2 and r3.
	got, err := s.GetCachedEGVs(base.Add(4*time.Minute), base.Add(11*time.Minute))
	if err != nil {
		t.Fatalf("GetCachedEGVs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
	if got[0].RecordID != "r2" || got[1].RecordID != "r3" {
		t.Errorf("unexpected records: %v", got)
	}
	// Verify ascending order.
	if !got[0].SystemTime.Before(got[1].SystemTime) {
		t.Error("GetCachedEGVs must return records in ascending SystemTime order")
	}
}

func TestGetCachedEGVs_Empty(t *testing.T) {
	s := testStore(t)
	got, err := s.GetCachedEGVs(time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("GetCachedEGVs on empty cache: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d", len(got))
	}
}

func TestCacheEGVs_PreservesAllFields(t *testing.T) {
	s := testStore(t)
	base := time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC)

	r := types.EGVRecord{
		RecordID:              "full-record",
		SystemTime:            base,
		DisplayTime:           base.Add(time.Hour),
		TransmitterID:         "tx-abc",
		TransmitterTicks:      12345,
		Value:                 142,
		Trend:                 types.TrendFortyFiveDown,
		TrendRate:             -1.5,
		Unit:                  "mg/dL",
		RateUnit:              "mg/dL/min",
		DisplayDevice:         "iOS",
		TransmitterGeneration: "g7",
		DisplayApp:            "G7",
	}

	if _, err := s.CacheEGVs([]types.EGVRecord{r}); err != nil {
		t.Fatalf("CacheEGVs: %v", err)
	}

	got, err := s.GetCachedEGVs(base.Add(-time.Minute), base.Add(time.Minute))
	if err != nil {
		t.Fatalf("GetCachedEGVs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	g := got[0]
	if g.RecordID != r.RecordID {
		t.Errorf("RecordID: got %q", g.RecordID)
	}
	if g.Value != r.Value {
		t.Errorf("Value: got %d, want %d", g.Value, r.Value)
	}
	if g.Trend != r.Trend {
		t.Errorf("Trend: got %q, want %q", g.Trend, r.Trend)
	}
	if g.TransmitterGeneration != "g7" {
		t.Errorf("TransmitterGeneration: got %q", g.TransmitterGeneration)
	}
}
