package rest_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/johnmartinez/cgm-get-agent/internal/crypto"
	"github.com/johnmartinez/cgm-get-agent/internal/dexcom"
	"github.com/johnmartinez/cgm-get-agent/internal/rest"
	"github.com/johnmartinez/cgm-get-agent/internal/store"
	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

var testKey = bytes.Repeat([]byte{0xCD}, 32)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("testStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newRestHandler creates a rest.Handler with a dexcom oauth backed by tokenPath.
// dexcomSrv is used as the base URL (needed if GetValidToken triggers a refresh).
func newRestHandler(t *testing.T, tokenPath string, dexcomSrv *httptest.Server) *rest.Handler {
	t.Helper()
	var httpClient *http.Client
	var baseURL string
	if dexcomSrv != nil {
		httpClient = dexcomSrv.Client()
		baseURL = dexcomSrv.URL
	} else {
		httpClient = &http.Client{}
		baseURL = "http://127.0.0.1:0" // unreachable; token should not need refresh
	}
	_, oauth := dexcom.NewClientForTest(baseURL, tokenPath, testKey, httpClient)
	return rest.New(oauth, testStore(t), time.Now())
}

// --- HandleHealth ---

func TestHandleHealth_NoDexcomTokens(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "tokens.enc") // no file written
	h := newRestHandler(t, tokenPath, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.HandleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["dexcom_auth"] != "not_configured" {
		t.Errorf("dexcom_auth: got %v, want not_configured", resp["dexcom_auth"])
	}
	if resp["status"] != "error" {
		t.Errorf("status: got %v, want error", resp["status"])
	}
}

func TestHandleHealth_ValidTokens(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "tokens.enc")
	tok := types.OAuthTokens{
		AccessToken:   "fresh-token",
		RefreshToken:  "fresh-refresh",
		ExpiresAt:     time.Now().UTC().Add(2 * time.Hour),
		LastRefreshed: time.Now().UTC(),
	}
	if err := crypto.SaveTokens(tokenPath, tok, testKey); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}

	h := newRestHandler(t, tokenPath, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.HandleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["dexcom_auth"] != "valid" {
		t.Errorf("dexcom_auth: got %v, want valid", resp["dexcom_auth"])
	}
	if resp["status"] != "ok" {
		t.Errorf("status: got %v, want ok", resp["status"])
	}
	if resp["db_accessible"] != true {
		t.Errorf("db_accessible: got %v, want true", resp["db_accessible"])
	}
	if _, ok := resp["uptime_seconds"]; !ok {
		t.Error("uptime_seconds must be present in health response")
	}
}

func TestHandleHealth_ExpiredTokens_RefreshFails(t *testing.T) {
	// Token exists but is expired; mock server returns 401 on refresh → "expired".
	tokenPath := filepath.Join(t.TempDir(), "tokens.enc")
	tok := types.OAuthTokens{
		AccessToken:   "stale-token",
		RefreshToken:  "stale-refresh",
		ExpiresAt:     time.Now().UTC().Add(-10 * time.Minute), // already expired
		LastRefreshed: time.Now().UTC().Add(-1 * time.Hour),
	}
	if err := crypto.SaveTokens(tokenPath, tok, testKey); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}

	// Mock token endpoint returns 401 (refresh token revoked).
	dexcomSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer dexcomSrv.Close()

	h := newRestHandler(t, tokenPath, dexcomSrv)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.HandleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp["dexcom_auth"] != "expired" {
		t.Errorf("dexcom_auth: got %v, want expired", resp["dexcom_auth"])
	}
	if resp["status"] != "degraded" {
		t.Errorf("status: got %v, want degraded", resp["status"])
	}
}

func TestHandleHealth_ContentTypeJSON(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "tokens.enc")
	h := newRestHandler(t, tokenPath, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.HandleHealth(rec, req)

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
}

// --- HandleToolInvoke ---

func TestHandleToolInvoke_ReturnsNotImplemented(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "tokens.enc")
	h := newRestHandler(t, tokenPath, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/tools/invoke", nil)
	rec := httptest.NewRecorder()
	h.HandleToolInvoke(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("UsesMCPTransport")) {
		t.Errorf("body should mention UsesMCPTransport, got: %s", rec.Body.String())
	}
}

func TestHandleToolInvoke_RejectsNonPOST(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "tokens.enc")
	h := newRestHandler(t, tokenPath, nil)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/v1/tools/invoke", nil)
		rec := httptest.NewRecorder()
		h.HandleToolInvoke(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected 405, got %d", method, rec.Code)
		}
	}
}
