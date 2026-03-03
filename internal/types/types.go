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

// EventSubType qualifies an EventType with a more specific category.
// Not all events have a subtype; the field is omitempty in the API response.
type EventSubType string

const (
	// Carbs subtypes
	EventSubTypeCarbsLiquid EventSubType = "liquid"
	EventSubTypeCarbsSolid  EventSubType = "solid"

	// Insulin subtypes
	EventSubTypeInsulinRapidActing EventSubType = "rapidActing"
	EventSubTypeInsulinShortActing EventSubType = "shortActing"
	EventSubTypeInsulinLongActing  EventSubType = "longActing"
	EventSubTypeInsulinCombination EventSubType = "combination"

	// Exercise subtypes
	EventSubTypeExerciseCardiovascular EventSubType = "cardiovascular"
	EventSubTypeExerciseStrength       EventSubType = "strength"
	EventSubTypeExerciseMixed          EventSubType = "mixed"

	// Health subtypes
	EventSubTypeHealthIllness     EventSubType = "illness"
	EventSubTypeHealthStress      EventSubType = "stress"
	EventSubTypeHealthHighSymptom EventSubType = "highSymptoms"
	EventSubTypeHealthLowSymptom  EventSubType = "lowSymptoms"
	EventSubTypeHealthCycle       EventSubType = "cycle"

	// Shared fallbacks
	EventSubTypeOther   EventSubType = "other"
	EventSubTypeUnknown EventSubType = "unknown"
)

// DexcomEvent is a carbs/insulin/exercise/health event from the Dexcom API.
// These are events logged by the user in the Dexcom G7 mobile app; the API is read-only.
type DexcomEvent struct {
	RecordID     string        `json:"recordId"`
	SystemTime   time.Time     `json:"systemTime"`
	DisplayTime  time.Time     `json:"displayTime"`
	EventType    EventType     `json:"eventType"`
	EventSubType *EventSubType `json:"eventSubType,omitempty"`
	Value        *float64      `json:"value,omitempty"`
	Unit         string        `json:"unit"`
}

// CalibrationRecord is a single fingerstick blood glucose calibration entry
// as returned by GET /v3/users/self/calibrations. Read-only via the API.
type CalibrationRecord struct {
	RecordID              string    `json:"recordId"`
	SystemTime            time.Time `json:"systemTime"`
	DisplayTime           time.Time `json:"displayTime"`
	Value                 int       `json:"value"` // mg/dL fingerstick reading
	Unit                  string    `json:"unit"`
	TransmitterID         string    `json:"transmitterId"`
	TransmitterGeneration string    `json:"transmitterGeneration"`
	DisplayDevice         string    `json:"displayDevice"`
	DisplayApp            string    `json:"displayApp"`
}

// AlertType identifies the kind of alert fired by the G7.
type AlertType string

const (
	AlertTypeHigh         AlertType = "high"
	AlertTypeLow          AlertType = "low"
	AlertTypeUrgentLow    AlertType = "urgentLow"
	AlertTypeUrgentLowSoon AlertType = "urgentLowSoon"
	AlertTypeRise         AlertType = "rise"
	AlertTypeFall         AlertType = "fall"
	AlertTypeOutOfRange   AlertType = "outOfRange"
	AlertTypeNoReadings   AlertType = "noReadings"
)

// AlertState tracks the lifecycle of an alert event.
type AlertState string

const (
	AlertStateTriggered    AlertState = "triggered"
	AlertStateAcknowledged AlertState = "acknowledged"
	AlertStateCleared      AlertState = "cleared"
)

// AlertRecord is a single alert event as returned by GET /v3/users/self/alerts.
// Alerts are fired by the G7 when glucose enters a configured danger zone.
type AlertRecord struct {
	RecordID    string     `json:"recordId"`
	SystemTime  time.Time  `json:"systemTime"`
	DisplayTime time.Time  `json:"displayTime"`
	AlertName   AlertType  `json:"alertName"`
	AlertState  AlertState `json:"alertState"`
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
