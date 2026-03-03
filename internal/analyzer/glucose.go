// Package analyzer implements pure business logic for glucose zone classification,
// snapshot construction, and meal impact assessment. It has no I/O side effects
// and no external dependencies beyond the project's own types and config packages.
package analyzer

import (
	"fmt"
	"sort"
	"time"

	"github.com/johnmartinez/cgm-get-agent/internal/config"
	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

// dataDelayThreshold is the maximum age of the most-recent EGV before a
// data_delay_notice is included in the snapshot.
const dataDelayThreshold = 10 * time.Minute

// postMealWindowStart is the minimum number of minutes after meal time before
// the post-meal glucose window begins.
const postMealWindowStart = 30 * time.Minute

// postMealWindowEnd is the maximum number of minutes after meal time before
// the post-meal glucose window ends.
const postMealWindowEnd = 180 * time.Minute

// ClassifyZone maps a glucose value (mg/dL) to a named zone using the provided
// threshold configuration. Zones are:
//
//	"low"        — below Low threshold
//	"low_normal" — at or above Low, below TargetLow
//	"target"     — at or above TargetLow, below or equal to TargetHigh
//	"elevated"   — above TargetHigh, below High
//	"high"       — at or above High
func ClassifyZone(value int, zones config.GlucoseZones) string {
	switch {
	case value < zones.Low:
		return "low"
	case value < zones.TargetLow:
		return "low_normal"
	case value <= zones.TargetHigh:
		return "target"
	case value < zones.High:
		return "elevated"
	default:
		return "high"
	}
}

// ComputeSnapshot builds a GlucoseSnapshot from a slice of EGV records.
// It returns an error if egvs is empty.
//
// Behavior:
//   - History is sorted ascending by SystemTime.
//   - Current = last record (most recent after sort).
//   - Baseline = first record (oldest after sort).
//   - Peak = record with the highest Value.
//   - Trough = record with the lowest Value.
//   - DataDelayNotice is set when the most recent EGV's SystemTime is more
//     than 10 minutes before now (using time.Now().UTC()).
func ComputeSnapshot(egvs []types.EGVRecord, zones config.GlucoseZones) (types.GlucoseSnapshot, error) {
	if len(egvs) == 0 {
		return types.GlucoseSnapshot{}, fmt.Errorf("analyzer: no EGV records provided")
	}

	sorted := make([]types.EGVRecord, len(egvs))
	copy(sorted, egvs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].SystemTime.Before(sorted[j].SystemTime)
	})

	current := sorted[len(sorted)-1]
	baseline := sorted[0]

	peak := sorted[0]
	trough := sorted[0]
	for _, r := range sorted[1:] {
		if r.Value > peak.Value {
			peak = r
		}
		if r.Value < trough.Value {
			trough = r
		}
	}

	snap := types.GlucoseSnapshot{
		Current:  current,
		Baseline: &baseline,
		Peak:     &peak,
		Trough:   &trough,
		History:  sorted,
	}

	age := time.Now().UTC().Sub(current.SystemTime)
	if age > dataDelayThreshold {
		msg := fmt.Sprintf("most recent reading is %.0f minutes old; CGM data may be delayed", age.Minutes())
		snap.DataDelayNotice = &msg
	}

	return snap, nil
}

// AssessMealImpact computes a MealImpactAssessment for a logged meal using
// the EGV history surrounding the meal timestamp.
//
// Post-meal window: 30–180 minutes after meal.Timestamp.
// Pre-meal baseline: the EGV record closest to (and at or before) meal.Timestamp.
//
// Returns an error when:
//   - No EGVs are provided.
//   - No pre-meal baseline EGV is found at or before meal.Timestamp.
//   - No post-meal EGVs are found in the 30–180 minute window.
func AssessMealImpact(meal types.Meal, egvs []types.EGVRecord, exercises []types.Exercise) (types.MealImpactAssessment, error) {
	if len(egvs) == 0 {
		return types.MealImpactAssessment{}, fmt.Errorf("analyzer: no EGV records provided for meal impact assessment")
	}

	sorted := make([]types.EGVRecord, len(egvs))
	copy(sorted, egvs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].SystemTime.Before(sorted[j].SystemTime)
	})

	mealTime := meal.Timestamp.UTC()
	windowStart := mealTime.Add(postMealWindowStart)
	windowEnd := mealTime.Add(postMealWindowEnd)

	// Find the pre-meal baseline: the latest EGV at or before meal time.
	preMealIdx := -1
	for i, r := range sorted {
		if !r.SystemTime.After(mealTime) {
			preMealIdx = i
		}
	}
	if preMealIdx == -1 {
		return types.MealImpactAssessment{}, fmt.Errorf("analyzer: no pre-meal EGV found at or before meal time %v", mealTime)
	}
	preMealRecord := sorted[preMealIdx]

	// Collect post-meal EGVs.
	var postMealEGVs []types.EGVRecord
	for _, r := range sorted {
		t := r.SystemTime
		if !t.Before(windowStart) && !t.After(windowEnd) {
			postMealEGVs = append(postMealEGVs, r)
		}
	}
	if len(postMealEGVs) == 0 {
		return types.MealImpactAssessment{}, fmt.Errorf("analyzer: no post-meal EGVs found in 30–180 minute window after %v", mealTime)
	}

	// Find peak and recovery (last value in window) from post-meal EGVs.
	peakRecord := postMealEGVs[0]
	for _, r := range postMealEGVs[1:] {
		if r.Value > peakRecord.Value {
			peakRecord = r
		}
	}
	recoveryRecord := postMealEGVs[len(postMealEGVs)-1]

	spikeDelta := peakRecord.Value - preMealRecord.Value
	if spikeDelta < 0 {
		spikeDelta = 0
	}

	timeToPeakMin := int(peakRecord.SystemTime.Sub(mealTime).Minutes())

	rating := rateSpike(spikeDelta)
	rationale := buildRationale(spikeDelta, rating, preMealRecord.Value, peakRecord.Value)

	assessment := types.MealImpactAssessment{
		Meal:            meal,
		PreMealGlucose:  preMealRecord.Value,
		PeakGlucose:     peakRecord.Value,
		SpikeDelta:      spikeDelta,
		TimeToPeakMin:   timeToPeakMin,
		RecoveryGlucose: recoveryRecord.Value,
		Rating:          rating,
		RatingRationale: rationale,
	}

	// Compute recovery time: first post-meal EGV that returns to pre-meal level.
	for _, r := range postMealEGVs {
		if r.Value <= preMealRecord.Value {
			mins := int(r.SystemTime.Sub(mealTime).Minutes())
			assessment.RecoveryTimeMin = &mins
			break
		}
	}

	// Check for exercise in the post-meal window.
	offset := findExerciseOffset(exercises, mealTime, windowEnd, sorted)
	if offset != nil {
		assessment.ExerciseOffset = offset
	}

	return assessment, nil
}

// rateSpike maps spike_delta (mg/dL) to a 1–10 rating per the spec table.
func rateSpike(delta int) int {
	switch {
	case delta <= 20:
		return 10
	case delta <= 30:
		return 9
	case delta <= 40:
		return 8
	case delta <= 50:
		return 7
	case delta <= 60:
		return 6
	case delta <= 70:
		return 5
	case delta <= 80:
		return 4
	case delta <= 100:
		return 3
	case delta <= 120:
		return 2
	default:
		return 1
	}
}

// buildRationale constructs a human-readable rating explanation.
func buildRationale(spikeDelta, rating, preMeal, peak int) string {
	return fmt.Sprintf(
		"glucose rose %d mg/dL (from %d to %d); spike rating %d/10 (%s)",
		spikeDelta, preMeal, peak, rating, ratingLabel(rating),
	)
}

func ratingLabel(rating int) string {
	switch {
	case rating >= 9:
		return "excellent"
	case rating >= 7:
		return "good"
	case rating >= 5:
		return "moderate"
	case rating >= 3:
		return "high"
	default:
		return "very high"
	}
}

// findExerciseOffset returns an ExerciseOffset if any exercise session overlaps
// the post-meal window (mealTime to windowEnd). It picks the EGV records closest
// to exercise start and end to compute the glucose delta during exercise.
func findExerciseOffset(exercises []types.Exercise, mealTime, windowEnd time.Time, sortedEGVs []types.EGVRecord) *types.ExerciseOffset {
	for _, ex := range exercises {
		exStart := ex.Timestamp.UTC()
		exEnd := exStart.Add(time.Duration(ex.DurationMin) * time.Minute)

		// Exercise must overlap the post-meal window.
		if exEnd.Before(mealTime) || exStart.After(windowEnd) {
			continue
		}

		glucoseAtStart := closestEGVValue(sortedEGVs, exStart)
		glucoseAtEnd := closestEGVValue(sortedEGVs, exEnd)
		if glucoseAtStart == 0 || glucoseAtEnd == 0 {
			continue
		}

		delta := glucoseAtEnd - glucoseAtStart
		effectiveness := exerciseEffectiveness(delta)

		return &types.ExerciseOffset{
			Exercise:       ex,
			GlucoseAtStart: glucoseAtStart,
			GlucoseAtEnd:   glucoseAtEnd,
			Delta:          delta,
			Effectiveness:  effectiveness,
		}
	}
	return nil
}

// closestEGVValue returns the Value of the EGV record whose SystemTime is
// nearest to t. Returns 0 if sortedEGVs is empty.
func closestEGVValue(sortedEGVs []types.EGVRecord, t time.Time) int {
	if len(sortedEGVs) == 0 {
		return 0
	}
	best := sortedEGVs[0]
	bestDiff := absDuration(sortedEGVs[0].SystemTime.Sub(t))
	for _, r := range sortedEGVs[1:] {
		d := absDuration(r.SystemTime.Sub(t))
		if d < bestDiff {
			bestDiff = d
			best = r
		}
	}
	return best.Value
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func exerciseEffectiveness(delta int) string {
	switch {
	case delta < -20:
		return "strong_reduction"
	case delta < 0:
		return "mild_reduction"
	case delta == 0:
		return "neutral"
	default:
		return "no_reduction"
	}
}
