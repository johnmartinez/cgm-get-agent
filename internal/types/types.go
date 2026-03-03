// Package types defines all shared data structures used across packages.
// Both internal/dexcom and internal/store import from here to avoid circular dependencies.
package types

import "time"

// --- Dexcom API Types ---

// TrendArrow represents the direction of glucose change as reported by the G7.
type TrendArrow string

const (
	TrendDoubleUp       TrendArrow = "doubleUp"
	TrendSingleUp       TrendArrow = "singleUp"
	TrendFortyFiveUp    TrendArrow = "fortyFiveUp"
	TrendFlat           TrendArrow = "flat"
	TrendFortyFiveDown  TrendArrow = "fortyFiveDown"
	TrendSingleDown     TrendArrow = "singleDown"
	TrendDoubleDown     TrendArrow = "doubleDown"
	TrendNone           TrendArrow = "none"
	TrendNotComputable  TrendArrow = "notComputable"
	TrendRateOutOfRange TrendArrow = "rateOutOfRange"
)

// EGVRecord is a single estimated glucose value from the Dexcom API.
// Always use the Value field; do not use smoothed or realtime variants.
// SystemTime is device clock time and may drift from true UTC — use it for
// sequencing only, not wall-clock calculations.
type EGVRecord struct {
	RecordID              string     `json:"recordId"`
	SystemTime            time.Time  `json:"systemTime"`
	DisplayTime           time.Time  `json:"displayTime"`
	TransmitterID         string     `json:"transmitterId"`
	TransmitterTicks      int        `json:"transmitterTicks"`
	Value                 int        `json:"value"`
	Trend                 TrendArrow `json:"trend"`
	TrendRate             float64    `json:"trendRate"`
	Unit                  string     `json:"unit"`
	RateUnit              string     `json:"rateUnit"`
	DisplayDevice         string     `json:"displayDevice"`
	TransmitterGeneration string     `json:"transmitterGeneration"`
	DisplayApp            string     `json:"displayApp"`
}

// EventType categorizes Dexcom-logged events.
type EventType string

const (
	EventTypeCarbs    EventType = "carbs"
	EventTypeInsulin  EventType = "insulin"
	EventTypeExercise EventType = "exercise"
	EventTypeHealth   EventType = "health"
)

// DexcomEvent is a carbs/insulin/exercise/health event from the Dexcom API.
type DexcomEvent struct {
	RecordID     string    `json:"recordId"`
	SystemTime   time.Time `json:"systemTime"`
	DisplayTime  time.Time `json:"displayTime"`
	EventType    EventType `json:"eventType"`
	EventSubType *string   `json:"eventSubType,omitempty"`
	Value        *float64  `json:"value,omitempty"`
	Unit         string    `json:"unit"`
}

// TimeRange is a start/end timestamp pair from the Dexcom dataRange endpoint.
type TimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// DataRange holds the earliest/latest record timestamps per data type.
type DataRange struct {
	Calibrations TimeRange `json:"calibrations"`
	EGVs         TimeRange `json:"egvs"`
	Events       TimeRange `json:"events"`
}

// OAuthTokens holds Dexcom OAuth credentials.
// refresh_token is single-use: every successful token refresh issues a new one.
// The old refresh_token is immediately invalidated by Dexcom.
type OAuthTokens struct {
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token"`
	ExpiresAt     time.Time `json:"expires_at"`
	LastRefreshed time.Time `json:"last_refreshed"`
}

// --- Application Types ---

// ExerciseIntensity represents perceived exertion for an exercise session.
type ExerciseIntensity string

const (
	IntensityLow          ExerciseIntensity = "low"
	IntensityModerate     ExerciseIntensity = "moderate"
	IntensityModerateHigh ExerciseIntensity = "moderate_high"
	IntensityHigh         ExerciseIntensity = "high"
	IntensityMax          ExerciseIntensity = "max"
)

// Meal is a locally stored meal record.
type Meal struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	CarbsEst    *float64 `json:"carbs_est,omitempty"`
	ProteinEst  *float64 `json:"protein_est,omitempty"`
	FatEst      *float64 `json:"fat_est,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
	LoggedAt    time.Time `json:"logged_at"`
	Notes       *string  `json:"notes,omitempty"`
}

// Exercise is a locally stored exercise session record.
type Exercise struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	DurationMin int               `json:"duration_min"`
	Intensity   ExerciseIntensity `json:"intensity"`
	Timestamp   time.Time         `json:"timestamp"`
	LoggedAt    time.Time         `json:"logged_at"`
	Notes       *string           `json:"notes,omitempty"`
}

// GlucoseSnapshot is the structured response for get_current_glucose and get_glucose_history.
// History is always sorted ascending by SystemTime.
type GlucoseSnapshot struct {
	Current         EGVRecord   `json:"current"`
	Baseline        *EGVRecord  `json:"baseline,omitempty"`
	Peak            *EGVRecord  `json:"peak,omitempty"`
	Trough          *EGVRecord  `json:"trough,omitempty"`
	History         []EGVRecord `json:"history"`
	DataDelayNotice *string     `json:"data_delay_notice,omitempty"`
}

// ExerciseOffset describes glucose changes during exercise within the post-meal window.
type ExerciseOffset struct {
	Exercise       Exercise `json:"exercise"`
	GlucoseAtStart int      `json:"glucose_at_start"`
	GlucoseAtEnd   int      `json:"glucose_at_end"`
	Delta          int      `json:"delta"`
	Effectiveness  string   `json:"effectiveness"`
}

// MealImpactAssessment is the structured output from rate_meal_impact.
type MealImpactAssessment struct {
	Meal            Meal            `json:"meal"`
	PreMealGlucose  int             `json:"pre_meal_glucose"`
	PeakGlucose     int             `json:"peak_glucose"`
	SpikeDelta      int             `json:"spike_delta"`
	TimeToPeakMin   int             `json:"time_to_peak_min"`
	RecoveryGlucose int             `json:"recovery_glucose"`
	RecoveryTimeMin *int            `json:"recovery_time_min,omitempty"`
	ExerciseOffset  *ExerciseOffset `json:"exercise_offset,omitempty"`
	Rating          int             `json:"rating"`
	RatingRationale string          `json:"rating_rationale"`
}

// --- ID Helpers ---

// MealID generates a Meal ID from the meal's consumption timestamp.
// Format: m_YYYYMMDD_HHmm (UTC). Always derived from Timestamp, not LoggedAt.
func MealID(t time.Time) string {
	return "m_" + t.UTC().Format("20060102_1504")
}

// ExerciseID generates an Exercise ID from the session start timestamp.
// Format: e_YYYYMMDD_HHmm (UTC). Always derived from Timestamp, not LoggedAt.
func ExerciseID(t time.Time) string {
	return "e_" + t.UTC().Format("20060102_1504")
}
