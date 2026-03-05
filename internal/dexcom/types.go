// Package dexcom provides an OAuth2-authenticated client for the Dexcom Developer API v3
// and HTTP handlers for the OAuth2 authorization code flow.
package dexcom

import (
	"encoding/json"
	"fmt"
)

// API base URLs.
const (
	sandboxBaseURL    = "https://sandbox-api.dexcom.com"
	productionBaseURL = "https://api.dexcom.com"
)

// dexcomTimeFormat is the timestamp format used by the Dexcom v3 API
// for both query parameters and response fields. It has no timezone component;
// treat all values as UTC for sequencing purposes only.
const dexcomTimeFormat = "2006-01-02T15:04:05"

// maxWindowDays is the Dexcom API's hard limit on date range queries.
const maxWindowDays = 30

// BaseURL returns the Dexcom API base URL for the given environment string.
// Any value other than "production" resolves to the sandbox.
func BaseURL(env string) string {
	if env == "production" {
		return productionBaseURL
	}
	return sandboxBaseURL
}

// --- Sentinel error types ---

// AuthError indicates that OAuth tokens are missing, expired beyond refresh,
// or rejected by the Dexcom API. Not retriable without re-authorization.
type AuthError struct {
	Message string
}

func (e *AuthError) Error() string { return "dexcom: auth: " + e.Message }

// APIError indicates a non-2xx response from the Dexcom API.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("dexcom: API error %d: %s", e.StatusCode, e.Body)
}

// WindowTooLargeError is returned when a requested date range exceeds 30 days.
type WindowTooLargeError struct {
	RequestedDays int
}

func (e *WindowTooLargeError) Error() string {
	return fmt.Sprintf("dexcom: date window %d days exceeds %d-day maximum", e.RequestedDays, maxWindowDays)
}

// --- Public types ---

// DeviceRecord contains information about a Dexcom G7 transmitter and display device.
// Returned by GetDevices.
type DeviceRecord struct {
	DeviceStatus          string        `json:"deviceStatus"`
	DisplayDevice         string        `json:"displayDevice"`
	DisplayApp            string        `json:"displayApp"`
	LastUploadDate        string        `json:"lastUploadDate"`
	TransmitterGeneration string        `json:"transmitterGeneration"`
	TransmitterID         string        `json:"transmitterId"`
	AlertSettings         []interface{} `json:"alertScheduleList,omitempty"`
}

// --- Internal API response envelopes ---

// egvsResponse is the JSON envelope from GET /v3/users/self/egvs.
type egvsResponse struct {
	EGVs []apiEGV `json:"egvs"`
}

// apiEGV is the raw JSON shape of a single EGV record from Dexcom.
// Timestamps are strings; converted to time.Time by convertEGV.
type apiEGV struct {
	RecordID              string  `json:"recordId"`
	SystemTime            string  `json:"systemTime"`
	DisplayTime           string  `json:"displayTime"`
	TransmitterID         string  `json:"transmitterId"`
	TransmitterTicks      int     `json:"transmitterTicks"`
	Value                 int     `json:"value"`
	Trend                 string  `json:"trend"`
	TrendRate             float64 `json:"trendRate"`
	Unit                  string  `json:"unit"`
	RateUnit              string  `json:"rateUnit"`
	DisplayDevice         string  `json:"displayDevice"`
	TransmitterGeneration string  `json:"transmitterGeneration"`
	DisplayApp            string  `json:"displayApp"`
}

// eventsResponse is the JSON envelope from GET /v3/users/self/events.
type eventsResponse struct {
	Events []apiEvent `json:"events"`
}

// apiEvent is the raw JSON shape of a single event record from Dexcom.
type apiEvent struct {
	RecordID     string   `json:"recordId"`
	SystemTime   string   `json:"systemTime"`
	DisplayTime  string   `json:"displayTime"`
	EventType    string   `json:"eventType"`
	EventSubType *string  `json:"eventSubType,omitempty"`
	Value        *float64 `json:"value,omitempty"`
	Unit         string   `json:"unit"`
}

// calibrationsResponse is the JSON envelope from GET /v3/users/self/calibrations.
type calibrationsResponse struct {
	Calibrations []apiCalibration `json:"calibrations"`
}

// apiCalibration is the raw JSON shape of a single calibration record from Dexcom.
type apiCalibration struct {
	RecordID              string  `json:"recordId"`
	SystemTime            string  `json:"systemTime"`
	DisplayTime           string  `json:"displayTime"`
	Value                 int     `json:"value"`
	Unit                  string  `json:"unit"`
	TransmitterID         string  `json:"transmitterId"`
	TransmitterGeneration string  `json:"transmitterGeneration"`
	DisplayDevice         string  `json:"displayDevice"`
	DisplayApp            string  `json:"displayApp"`
}

// alertsResponse is the JSON envelope from GET /v3/users/self/alerts.
type alertsResponse struct {
	Alerts []apiAlert `json:"alerts"`
}

// apiAlert is the raw JSON shape of a single alert event from Dexcom.
type apiAlert struct {
	RecordID    string `json:"recordId"`
	SystemTime  string `json:"systemTime"`
	DisplayTime string `json:"displayTime"`
	AlertName   string `json:"alertName"`
	AlertState  string `json:"alertState"`
}

// dataRangeResponse is the JSON envelope from GET /v3/users/self/dataRange.
type dataRangeResponse struct {
	Calibrations timeRangeJSON `json:"calibrations"`
	EGVs         timeRangeJSON `json:"egvs"`
	Events       timeRangeJSON `json:"events"`
}

// flexString unmarshals a JSON string, null, or any non-string value (object,
// number, etc.) into a Go string. Non-string values — including null and objects
// returned by some Dexcom sandbox users — are silently mapped to "".
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	// Non-string JSON value (object, array, number) — treat as no data.
	*f = ""
	return nil
}

type timeRangeJSON struct {
	Start flexString `json:"start"`
	End   flexString `json:"end"`
}

// devicesResponse is the JSON envelope from GET /v3/users/self/devices.
type devicesResponse struct {
	Devices []DeviceRecord `json:"devices"`
}

// tokenResponse is the JSON body from the Dexcom token endpoint.
// refresh_token here is always new and single-use — the old one is immediately invalidated.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"` // seconds until access_token expiry
	TokenType    string `json:"token_type"`
}
