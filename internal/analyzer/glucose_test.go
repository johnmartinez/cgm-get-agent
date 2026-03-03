package analyzer

import (
	"strings"
	"testing"
	"time"

	"github.com/johnmartinez/cgm-get-agent/internal/config"
	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

// defaultZones returns the same defaults used by config.Load().
func defaultZones() config.GlucoseZones {
	return config.GlucoseZones{
		Low:        70,
		TargetLow:  80,
		TargetHigh: 120,
		Elevated:   140,
		High:       180,
	}
}

// makeEGV builds a minimal EGVRecord at the given offset from base.
func makeEGV(base time.Time, offsetMin int, value int) types.EGVRecord {
	t := base.Add(time.Duration(offsetMin) * time.Minute)
	return types.EGVRecord{
		RecordID:   strings.ReplaceAll(t.Format("T150405"), ":", ""),
		SystemTime: t,
		Value:      value,
		Trend:      types.TrendFlat,
	}
}

// --- ClassifyZone ---

func TestClassifyZone_AllZones(t *testing.T) {
	zones := defaultZones()
	cases := []struct {
		value int
		want  string
	}{
		{40, "low"},
		{69, "low"},
		{70, "low_normal"}, // equal to Low — first non-low zone
		{79, "low_normal"},
		{80, "target"},    // equal to TargetLow
		{100, "target"},
		{120, "target"},   // equal to TargetHigh — still target
		{121, "elevated"},
		{139, "elevated"},
		{179, "elevated"}, // just below High
		{180, "high"},     // equal to High
		{250, "high"},
	}
	for _, tc := range cases {
		got := ClassifyZone(tc.value, zones)
		if got != tc.want {
			t.Errorf("ClassifyZone(%d) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

func TestClassifyZone_BoundaryValues(t *testing.T) {
	zones := defaultZones()

	// One below each threshold boundary.
	if got := ClassifyZone(zones.Low-1, zones); got != "low" {
		t.Errorf("one below Low: got %q, want low", got)
	}
	if got := ClassifyZone(zones.TargetLow-1, zones); got != "low_normal" {
		t.Errorf("one below TargetLow: got %q, want low_normal", got)
	}
	if got := ClassifyZone(zones.TargetHigh, zones); got != "target" {
		t.Errorf("TargetHigh itself: got %q, want target", got)
	}
	if got := ClassifyZone(zones.TargetHigh+1, zones); got != "elevated" {
		t.Errorf("one above TargetHigh: got %q, want elevated", got)
	}
	if got := ClassifyZone(zones.High-1, zones); got != "elevated" {
		t.Errorf("one below High: got %q, want elevated", got)
	}
	if got := ClassifyZone(zones.High, zones); got != "high" {
		t.Errorf("High itself: got %q, want high", got)
	}
}

// --- ComputeSnapshot ---

func TestComputeSnapshot_Empty(t *testing.T) {
	_, err := ComputeSnapshot(nil, defaultZones())
	if err == nil {
		t.Fatal("expected error for empty EGV slice")
	}
}

func TestComputeSnapshot_SingleRecord(t *testing.T) {
	base := time.Now().UTC().Add(-5 * time.Minute) // recent enough — no delay notice
	egvs := []types.EGVRecord{makeEGV(base, 0, 105)}

	snap, err := ComputeSnapshot(egvs, defaultZones())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Current.Value != 105 {
		t.Errorf("Current.Value = %d, want 105", snap.Current.Value)
	}
	if snap.Baseline.Value != 105 {
		t.Errorf("Baseline.Value = %d, want 105", snap.Baseline.Value)
	}
	if snap.Peak.Value != 105 {
		t.Errorf("Peak.Value = %d, want 105", snap.Peak.Value)
	}
	if snap.Trough.Value != 105 {
		t.Errorf("Trough.Value = %d, want 105", snap.Trough.Value)
	}
	if snap.DataDelayNotice != nil {
		t.Errorf("unexpected DataDelayNotice for recent EGV: %q", *snap.DataDelayNotice)
	}
}

func TestComputeSnapshot_HistorySortedAscending(t *testing.T) {
	base := time.Now().UTC().Add(-20 * time.Minute)
	// Provide out-of-order records.
	egvs := []types.EGVRecord{
		makeEGV(base, 10, 115),
		makeEGV(base, 0, 100),
		makeEGV(base, 5, 110),
	}
	snap, err := ComputeSnapshot(egvs, defaultZones())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 1; i < len(snap.History); i++ {
		if snap.History[i].SystemTime.Before(snap.History[i-1].SystemTime) {
			t.Errorf("history not sorted ascending at index %d", i)
		}
	}
	if snap.Current.Value != 115 {
		t.Errorf("Current should be most recent (115), got %d", snap.Current.Value)
	}
	if snap.Baseline.Value != 100 {
		t.Errorf("Baseline should be oldest (100), got %d", snap.Baseline.Value)
	}
}

func TestComputeSnapshot_PeakAndTrough(t *testing.T) {
	base := time.Now().UTC().Add(-60 * time.Minute)
	egvs := []types.EGVRecord{
		makeEGV(base, 0, 90),
		makeEGV(base, 5, 160),
		makeEGV(base, 10, 70),
		makeEGV(base, 15, 110),
	}
	snap, err := ComputeSnapshot(egvs, defaultZones())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Peak.Value != 160 {
		t.Errorf("Peak = %d, want 160", snap.Peak.Value)
	}
	if snap.Trough.Value != 70 {
		t.Errorf("Trough = %d, want 70", snap.Trough.Value)
	}
}

func TestComputeSnapshot_DataDelayNotice_TriggeredAtTenMinPlusOne(t *testing.T) {
	// EGV is exactly 10 min + 1 sec old — must trigger notice.
	staleTime := time.Now().UTC().Add(-(dataDelayThreshold + time.Second))
	egvs := []types.EGVRecord{
		{RecordID: "stale", SystemTime: staleTime, Value: 100},
	}
	snap, err := ComputeSnapshot(egvs, defaultZones())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.DataDelayNotice == nil {
		t.Error("expected DataDelayNotice for EGV older than 10 minutes")
	}
}

func TestComputeSnapshot_DataDelayNotice_NotTriggeredBeforeTenMin(t *testing.T) {
	// EGV is 9 min 59 sec old — must NOT trigger notice.
	recentTime := time.Now().UTC().Add(-(dataDelayThreshold - time.Second))
	egvs := []types.EGVRecord{
		{RecordID: "recent", SystemTime: recentTime, Value: 100},
	}
	snap, err := ComputeSnapshot(egvs, defaultZones())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.DataDelayNotice != nil {
		t.Errorf("DataDelayNotice should not fire before 10 min threshold: %q", *snap.DataDelayNotice)
	}
}

// --- AssessMealImpact ---

// buildMealEGVs creates a synthetic EGV curve:
// - 4 records before the meal (t-20 to t-5 min)
// - preMeal value at t-5
// - rising curve in post-meal window
// - peak at +peakOffsetMin
// - flat recovery at recoveryValue
func buildMealEGVs(mealTime time.Time, preMeal, peak, recovery, peakOffsetMin int) []types.EGVRecord {
	egvs := []types.EGVRecord{
		makeEGV(mealTime, -20, preMeal-5),
		makeEGV(mealTime, -15, preMeal-3),
		makeEGV(mealTime, -10, preMeal-1),
		makeEGV(mealTime, -5, preMeal),
		// post-meal: rising to peak then recovery
		makeEGV(mealTime, 30, preMeal+10),
		makeEGV(mealTime, peakOffsetMin, peak),
		makeEGV(mealTime, 120, recovery),
		makeEGV(mealTime, 150, recovery),
		makeEGV(mealTime, 180, recovery),
	}
	return egvs
}

func makeMeal(t time.Time) types.Meal {
	return types.Meal{
		ID:          types.MealID(t),
		Description: "test meal",
		Timestamp:   t,
		LoggedAt:    t,
	}
}

// TestAssessMealImpact_AllRatingTiers verifies each tier of the spike rating table.
func TestAssessMealImpact_AllRatingTiers(t *testing.T) {
	mealTime := time.Now().UTC().Add(-4 * time.Hour) // well in the past

	cases := []struct {
		spikeDelta int
		wantRating int
	}{
		{20, 10},
		{21, 9}, {30, 9},
		{31, 8}, {40, 8},
		{41, 7}, {50, 7},
		{51, 6}, {60, 6},
		{61, 5}, {70, 5},
		{71, 4}, {80, 4},
		{81, 3}, {100, 3},
		{101, 2}, {120, 2},
		{121, 1}, {200, 1},
	}

	for _, tc := range cases {
		preMeal := 100
		peak := preMeal + tc.spikeDelta
		egvs := buildMealEGVs(mealTime, preMeal, peak, preMeal, 60)
		meal := makeMeal(mealTime)

		got, err := AssessMealImpact(meal, egvs, nil)
		if err != nil {
			t.Errorf("spike=%d: unexpected error: %v", tc.spikeDelta, err)
			continue
		}
		if got.Rating != tc.wantRating {
			t.Errorf("spike=%d: rating=%d, want %d", tc.spikeDelta, got.Rating, tc.wantRating)
		}
		if got.SpikeDelta != tc.spikeDelta {
			t.Errorf("spike=%d: SpikeDelta=%d, want %d", tc.spikeDelta, got.SpikeDelta, tc.spikeDelta)
		}
	}
}

func TestAssessMealImpact_NoEGVs(t *testing.T) {
	meal := makeMeal(time.Now().Add(-2 * time.Hour))
	_, err := AssessMealImpact(meal, nil, nil)
	if err == nil {
		t.Fatal("expected error when no EGVs provided")
	}
}

func TestAssessMealImpact_NoPreMealEGV(t *testing.T) {
	mealTime := time.Now().UTC().Add(-3 * time.Hour)
	// All EGVs are after the meal.
	egvs := []types.EGVRecord{
		makeEGV(mealTime, 35, 120),
		makeEGV(mealTime, 60, 140),
	}
	meal := makeMeal(mealTime)
	_, err := AssessMealImpact(meal, egvs, nil)
	if err == nil {
		t.Fatal("expected error when no pre-meal EGV found")
	}
}

func TestAssessMealImpact_NoPostMealEGVs(t *testing.T) {
	mealTime := time.Now().UTC().Add(-5 * time.Hour)
	// EGVs only before the meal or outside the 30–180 min window.
	egvs := []types.EGVRecord{
		makeEGV(mealTime, -10, 100),
		makeEGV(mealTime, 5, 105),   // within 30 min — not post-meal window
		makeEGV(mealTime, 200, 110), // beyond 180 min
	}
	meal := makeMeal(mealTime)
	_, err := AssessMealImpact(meal, egvs, nil)
	if err == nil {
		t.Fatal("expected error when no post-meal EGVs in window")
	}
}

func TestAssessMealImpact_ExerciseOffset_InWindow(t *testing.T) {
	mealTime := time.Now().UTC().Add(-5 * time.Hour)
	preMeal := 100
	peak := 150
	egvs := buildMealEGVs(mealTime, preMeal, peak, preMeal, 60)

	// Exercise starting 45 min after meal (within 30–180 min window).
	exTime := mealTime.Add(45 * time.Minute)
	exercises := []types.Exercise{
		{
			ID:          types.ExerciseID(exTime),
			Type:        "walk",
			DurationMin: 30,
			Intensity:   types.IntensityModerate,
			Timestamp:   exTime,
			LoggedAt:    exTime,
		},
	}

	meal := makeMeal(mealTime)
	got, err := AssessMealImpact(meal, egvs, exercises)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ExerciseOffset == nil {
		t.Error("expected ExerciseOffset to be populated for exercise in post-meal window")
	}
}

func TestAssessMealImpact_ExerciseOffset_OutsideWindow(t *testing.T) {
	mealTime := time.Now().UTC().Add(-8 * time.Hour)
	preMeal := 100
	peak := 130
	egvs := buildMealEGVs(mealTime, preMeal, peak, preMeal, 60)

	// Exercise 4 hours after meal — outside the 30–180 min post-meal window.
	exTime := mealTime.Add(4 * time.Hour)
	exercises := []types.Exercise{
		{
			ID:          types.ExerciseID(exTime),
			Type:        "run",
			DurationMin: 30,
			Intensity:   types.IntensityHigh,
			Timestamp:   exTime,
			LoggedAt:    exTime,
		},
	}

	meal := makeMeal(mealTime)
	got, err := AssessMealImpact(meal, egvs, exercises)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ExerciseOffset != nil {
		t.Error("ExerciseOffset should be nil for exercise outside post-meal window")
	}
}

func TestAssessMealImpact_RatingRationale_NonEmpty(t *testing.T) {
	mealTime := time.Now().UTC().Add(-4 * time.Hour)
	egvs := buildMealEGVs(mealTime, 100, 140, 100, 60)
	meal := makeMeal(mealTime)

	got, err := AssessMealImpact(meal, egvs, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.RatingRationale == "" {
		t.Error("RatingRationale must not be empty")
	}
}

func TestAssessMealImpact_NegativeSpikeClampsToZero(t *testing.T) {
	// If glucose drops after a meal (e.g. insulin), SpikeDelta must be 0, not negative.
	mealTime := time.Now().UTC().Add(-4 * time.Hour)
	egvs := []types.EGVRecord{
		makeEGV(mealTime, -5, 120),
		makeEGV(mealTime, 35, 100), // lower than pre-meal
		makeEGV(mealTime, 60, 90),
		makeEGV(mealTime, 90, 95),
	}
	meal := makeMeal(mealTime)

	got, err := AssessMealImpact(meal, egvs, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SpikeDelta < 0 {
		t.Errorf("SpikeDelta must not be negative, got %d", got.SpikeDelta)
	}
}

// --- rateSpike (internal) ---

func TestRateSpike_AllTiers(t *testing.T) {
	cases := []struct{ delta, want int }{
		{0, 10}, {20, 10},
		{21, 9}, {30, 9},
		{31, 8}, {40, 8},
		{41, 7}, {50, 7},
		{51, 6}, {60, 6},
		{61, 5}, {70, 5},
		{71, 4}, {80, 4},
		{81, 3}, {100, 3},
		{101, 2}, {120, 2},
		{121, 1}, {500, 1},
	}
	for _, tc := range cases {
		if got := rateSpike(tc.delta); got != tc.want {
			t.Errorf("rateSpike(%d) = %d, want %d", tc.delta, got, tc.want)
		}
	}
}
