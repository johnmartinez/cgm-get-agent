package dexcom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnmartinez/cgm-get-agent/internal/crypto"
	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

// newTestClient creates a Client and OAuthHandler pair backed by a fresh token file.
// srv is an httptest.Server whose URL is used as both the OAuth and data base URL.
func newTestClient(t *testing.T, srv *httptest.Server) (*Client, *OAuthHandler) {
	t.Helper()
	tokenPath := filepath.Join(t.TempDir(), "tokens.enc")

	// Pre-load a fresh token so no refresh is triggered during client tests.
	tok := types.OAuthTokens{
		AccessToken:   "test-bearer-token",
		RefreshToken:  "test-refresh",
		ExpiresAt:     time.Now().UTC().Add(2 * time.Hour),
		LastRefreshed: time.Now().UTC(),
	}
	if err := crypto.SaveTokens(tokenPath, tok, testKey); err != nil {
		t.Fatalf("newTestClient: saving tokens: %v", err)
	}

	oauth := &OAuthHandler{
		clientID:     "cid",
		clientSecret: "csec",
		redirectURI:  "http://localhost/callback",
		baseURL:      srv.URL,
		tokenPath:    tokenPath,
		encKey:       testKey,
		httpClient:   srv.Client(),
	}
	client := &Client{
		baseURL:    srv.URL,
		oauth:      oauth,
		httpClient: srv.Client(),
	}
	return client, oauth
}

// sampleEGVs returns a minimal egvsResponse JSON body for use in mock handlers.
func sampleEGVsJSON(t *testing.T) string {
	t.Helper()
	base := time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC)
	resp := egvsResponse{EGVs: []apiEGV{
		{
			RecordID:              "rec-1",
			SystemTime:            base.Format(dexcomTimeFormat),
			DisplayTime:           base.Format(dexcomTimeFormat),
			TransmitterID:         "tx-abc",
			TransmitterTicks:      1000,
			Value:                 105,
			Trend:                 "flat",
			TrendRate:             0.1,
			Unit:                  "mg/dL",
			RateUnit:              "mg/dL/min",
			DisplayDevice:         "iOS",
			TransmitterGeneration: "g7",
			DisplayApp:            "G7",
		},
		{
			RecordID:    "rec-2",
			SystemTime:  base.Add(5 * time.Minute).Format(dexcomTimeFormat),
			DisplayTime: base.Add(5 * time.Minute).Format(dexcomTimeFormat),
			Value:       115,
			Trend:       "fortyFiveUp",
			TrendRate:   1.5,
			Unit:        "mg/dL",
			RateUnit:    "mg/dL/min",
		},
	}}
	b, _ := json.Marshal(resp)
	return string(b)
}

// --- GetEGVs ---

func TestGetEGVs_Success(t *testing.T) {
	var capturedAuthHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sampleEGVsJSON(t)))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv)
	start := time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)

	records, err := client.GetEGVs(context.Background(), start, end)
	if err != nil {
		t.Fatalf("GetEGVs: %v", err)
	}

	// Verify Bearer token sent.
	if capturedAuthHeader != "Bearer test-bearer-token" {
		t.Errorf("expected Bearer test-bearer-token, got %q", capturedAuthHeader)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0].RecordID != "rec-1" {
		t.Errorf("first record ID: got %q, want %q", records[0].RecordID, "rec-1")
	}
	if records[0].Value != 105 {
		t.Errorf("first record value: got %d, want 105", records[0].Value)
	}
	if records[0].Trend != types.TrendFlat {
		t.Errorf("first record trend: got %q, want flat", records[0].Trend)
	}
	if records[1].Trend != types.TrendFortyFiveUp {
		t.Errorf("second record trend: got %q", records[1].Trend)
	}
	if records[0].TransmitterGeneration != "g7" {
		t.Errorf("transmitter generation: got %q", records[0].TransmitterGeneration)
	}
}

func TestGetEGVs_QueryParamsFormat(t *testing.T) {
	var capturedURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(egvsResponse{})
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv)
	start := time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)

	_, _ = client.GetEGVs(context.Background(), start, end)

	if !strings.Contains(capturedURL, "startDate=2026-03-03T10%3A00%3A00") &&
		!strings.Contains(capturedURL, "startDate=2026-03-03T10:00:00") {
		t.Errorf("startDate not correctly formatted in URL: %q", capturedURL)
	}
}

func TestGetEGVs_WindowTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called for oversized window")
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv)
	start := time.Now()
	end := start.Add(31 * 24 * time.Hour) // 31 days

	_, err := client.GetEGVs(context.Background(), start, end)
	if err == nil {
		t.Fatal("expected WindowTooLargeError")
	}
	if _, ok := err.(*WindowTooLargeError); !ok {
		t.Errorf("expected *WindowTooLargeError, got %T: %v", err, err)
	}
}

func TestGetEGVs_ExactlyThirtyDays_Allowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(egvsResponse{})
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv)
	start := time.Now()
	end := start.Add(30 * 24 * time.Hour) // exactly 30 days — allowed

	_, err := client.GetEGVs(context.Background(), start, end)
	if err != nil {
		t.Errorf("30-day window should be allowed: %v", err)
	}
}

func TestGetEGVs_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv)
	_, err := client.GetEGVs(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if _, ok := err.(*AuthError); !ok {
		t.Errorf("expected *AuthError on 401, got %T: %v", err, err)
	}
}

func TestGetEGVs_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("temporarily unavailable"))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv)
	_, err := client.GetEGVs(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err == nil {
		t.Fatal("expected error on 503")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Errorf("expected *APIError on 503, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", apiErr.StatusCode)
	}
}

// --- GetEvents ---

func TestGetEvents_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC)
		carbVal := 70.0
		resp := eventsResponse{Events: []apiEvent{
			{
				RecordID:    "ev-1",
				SystemTime:  base.Format(dexcomTimeFormat),
				DisplayTime: base.Format(dexcomTimeFormat),
				EventType:   "carbs",
				Value:       &carbVal,
				Unit:        "grams",
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv)
	events, err := client.GetEvents(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventType != types.EventTypeCarbs {
		t.Errorf("EventType: got %q, want carbs", events[0].EventType)
	}
	if events[0].Value == nil || *events[0].Value != 70.0 {
		t.Errorf("event Value mismatch")
	}
}

// --- GetDataRange ---

func TestGetDataRange_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := dataRangeResponse{
			EGVs: timeRangeJSON{
				Start: "2026-03-01T00:00:00",
				End:   "2026-03-03T10:00:00",
			},
			Events: timeRangeJSON{
				Start: "2026-03-01T00:00:00",
				End:   "2026-03-03T09:00:00",
			},
			Calibrations: timeRangeJSON{
				Start: "2026-03-01T00:00:00",
				End:   "2026-03-02T00:00:00",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv)
	dr, err := client.GetDataRange(context.Background())
	if err != nil {
		t.Fatalf("GetDataRange: %v", err)
	}

	wantEnd := time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC)
	if !dr.EGVs.End.Equal(wantEnd) {
		t.Errorf("EGVs.End: got %v, want %v", dr.EGVs.End, wantEnd)
	}
}

// --- GetDevices ---

func TestGetDevices_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := devicesResponse{Devices: []DeviceRecord{
			{
				DeviceStatus:          "active",
				DisplayDevice:         "iOS",
				DisplayApp:            "G7",
				TransmitterGeneration: "g7",
				TransmitterID:         "tx-123",
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv)
	devices, err := client.GetDevices(context.Background())
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	if devices[0].TransmitterGeneration != "g7" {
		t.Errorf("TransmitterGeneration: got %q, want g7", devices[0].TransmitterGeneration)
	}
}

// --- Error type checks ---

func TestBaseURL(t *testing.T) {
	if BaseURL("production") != productionBaseURL {
		t.Errorf("production URL mismatch")
	}
	if BaseURL("sandbox") != sandboxBaseURL {
		t.Errorf("sandbox URL mismatch")
	}
	if BaseURL("anything-else") != sandboxBaseURL {
		t.Errorf("unknown env should default to sandbox")
	}
}

func TestWindowTooLargeError_Message(t *testing.T) {
	err := &WindowTooLargeError{RequestedDays: 45}
	if !strings.Contains(err.Error(), "45") {
		t.Errorf("error should mention requested days: %q", err.Error())
	}
}

func TestAPIError_Message(t *testing.T) {
	err := &APIError{StatusCode: 503, Body: "service unavailable"}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should mention status code: %q", err.Error())
	}
}
