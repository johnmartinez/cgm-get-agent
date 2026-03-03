package dexcom

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

	"github.com/johnmartinez/cgm-get-agent/internal/crypto"
	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

var testKey = bytes.Repeat([]byte{0x01}, 32)

// newTestHandler returns a minimal OAuthHandler wired to a temp token file.
// Point h.baseURL at a test server before use.
func newTestHandler(t *testing.T) *OAuthHandler {
	t.Helper()
	return &OAuthHandler{
		clientID:     "test-client-id",
		clientSecret: "test-secret",
		redirectURI:  "http://localhost:8080/callback",
		tokenPath:    filepath.Join(t.TempDir(), "tokens.enc"),
		encKey:       testKey,
		httpClient:   &http.Client{Timeout: 5 * time.Second},
	}
}

// writeFreshTokens saves tokens with ExpiresAt far in the future.
func writeFreshTokens(t *testing.T, h *OAuthHandler) types.OAuthTokens {
	t.Helper()
	tok := types.OAuthTokens{
		AccessToken:   "fresh-access-token",
		RefreshToken:  "fresh-refresh-token",
		ExpiresAt:     time.Now().UTC().Add(2 * time.Hour),
		LastRefreshed: time.Now().UTC(),
	}
	if err := crypto.SaveTokens(h.tokenPath, tok, h.encKey); err != nil {
		t.Fatalf("writeFreshTokens: %v", err)
	}
	return tok
}

// writeExpiredTokens saves tokens whose access_token has already expired.
func writeExpiredTokens(t *testing.T, h *OAuthHandler) {
	t.Helper()
	tok := types.OAuthTokens{
		AccessToken:   "expired-access-token",
		RefreshToken:  "old-refresh-token",
		ExpiresAt:     time.Now().UTC().Add(-5 * time.Minute), // already expired
		LastRefreshed: time.Now().UTC().Add(-1 * time.Hour),
	}
	if err := crypto.SaveTokens(h.tokenPath, tok, h.encKey); err != nil {
		t.Fatalf("writeExpiredTokens: %v", err)
	}
}

// mockTokenServer starts an httptest server that serves POST /v3/oauth2/token.
func mockTokenServer(t *testing.T, statusCode int, body tokenResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/oauth2/token" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(body)
	}))
}

// --- CSRF ---

func TestHandleStart_SetsCSRFAndRedirects(t *testing.T) {
	h := newTestHandler(t)
	h.baseURL = "https://sandbox-api.dexcom.com"

	req := httptest.NewRequest(http.MethodGet, "/oauth/start", nil)
	rec := httptest.NewRecorder()
	h.HandleStart(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/v3/oauth2/login") {
		t.Errorf("redirect should target Dexcom login, got %q", loc)
	}
	if !strings.Contains(loc, "client_id=test-client-id") {
		t.Errorf("redirect should include client_id, got %q", loc)
	}
	if !strings.Contains(loc, "response_type=code") {
		t.Errorf("redirect should include response_type=code, got %q", loc)
	}
	if !strings.Contains(loc, "scope=offline_access") {
		t.Errorf("redirect should include scope=offline_access, got %q", loc)
	}
	// State param must be present and stored.
	if !strings.Contains(loc, "state=") {
		t.Errorf("redirect should include CSRF state, got %q", loc)
	}
}

func TestHandleCallback_InvalidCSRF(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/callback?code=abc&state=bogus-state", nil)
	rec := httptest.NewRecorder()
	h.HandleCallback(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid CSRF state, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "CSRF") {
		t.Errorf("error body should mention CSRF, got %q", rec.Body.String())
	}
}

func TestHandleCallback_DexcomError(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/callback?error=access_denied", nil)
	rec := httptest.NewRecorder()
	h.HandleCallback(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for Dexcom error param, got %d", rec.Code)
	}
}

func TestHandleCallback_ValidFlow(t *testing.T) {
	srv := mockTokenServer(t, http.StatusOK, tokenResponse{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresIn:    3600,
	})
	defer srv.Close()

	h := newTestHandler(t)
	h.baseURL = srv.URL

	// Simulate a valid CSRF state already stored.
	state := "valid-csrf-state-1234"
	h.csrfStates.Store(state, struct{}{})

	req := httptest.NewRequest(http.MethodGet, "/callback?code=authcode&state="+state, nil)
	rec := httptest.NewRecorder()
	h.HandleCallback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid callback, got %d: %s", rec.Code, rec.Body.String())
	}

	// CSRF state must be consumed (LoadAndDelete).
	if _, ok := h.csrfStates.Load(state); ok {
		t.Error("CSRF state must be deleted after use")
	}

	// Tokens must be stored and decryptable.
	stored, err := crypto.LoadTokens(h.tokenPath, testKey)
	if err != nil {
		t.Fatalf("tokens not stored after callback: %v", err)
	}
	if stored.AccessToken != "new-access" {
		t.Errorf("stored access_token: got %q, want %q", stored.AccessToken, "new-access")
	}
	if stored.RefreshToken != "new-refresh" {
		t.Errorf("stored refresh_token: got %q, want %q", stored.RefreshToken, "new-refresh")
	}
}

// --- GetValidToken / refresh ---

func TestGetValidToken_FreshTokens_NoRefresh(t *testing.T) {
	// No mock server — if a network call is made, the test will fail because
	// h.baseURL is set to an unreachable address.
	h := newTestHandler(t)
	h.baseURL = "http://127.0.0.1:0" // unreachable; refresh must NOT be called
	writeFreshTokens(t, h)

	token, err := h.GetValidToken(context.Background())
	if err != nil {
		t.Fatalf("GetValidToken with fresh tokens: %v", err)
	}
	if token != "fresh-access-token" {
		t.Errorf("expected fresh-access-token, got %q", token)
	}
}

func TestGetValidToken_ExpiredTokens_TriggersRefresh(t *testing.T) {
	refreshCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalled = true
		// Verify the request uses refresh_token grant.
		r.ParseForm()
		if r.FormValue("grant_type") != "refresh_token" {
			t.Errorf("expected grant_type=refresh_token, got %q", r.FormValue("grant_type"))
		}
		if r.FormValue("refresh_token") != "old-refresh-token" {
			t.Errorf("expected old-refresh-token, got %q", r.FormValue("refresh_token"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken:  "refreshed-access",
			RefreshToken: "new-refresh-token",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	h := newTestHandler(t)
	h.baseURL = srv.URL
	writeExpiredTokens(t, h)

	token, err := h.GetValidToken(context.Background())
	if err != nil {
		t.Fatalf("GetValidToken with expired tokens: %v", err)
	}
	if !refreshCalled {
		t.Error("expected token refresh to be called for expired tokens")
	}
	if token != "refreshed-access" {
		t.Errorf("expected refreshed-access, got %q", token)
	}

	// Verify new refresh_token is persisted (old one must be gone).
	stored, err := crypto.LoadTokens(h.tokenPath, testKey)
	if err != nil {
		t.Fatalf("loading stored tokens after refresh: %v", err)
	}
	if stored.RefreshToken == "old-refresh-token" {
		t.Error("old refresh_token must not persist after refresh — single-use violation")
	}
	if stored.RefreshToken != "new-refresh-token" {
		t.Errorf("new refresh_token not stored: got %q", stored.RefreshToken)
	}
}

func TestGetValidToken_NoTokenFile_ReturnsAuthError(t *testing.T) {
	h := newTestHandler(t)
	// tokenPath points to a non-existent file by default.
	_, err := h.GetValidToken(context.Background())
	if err == nil {
		t.Fatal("expected error when no token file exists")
	}
	var authErr *AuthError
	if !isAuthError(err, &authErr) {
		t.Errorf("expected AuthError, got %T: %v", err, err)
	}
}

func TestGetValidToken_RefreshFails_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // simulate revoked refresh_token
	}))
	defer srv.Close()

	h := newTestHandler(t)
	h.baseURL = srv.URL
	writeExpiredTokens(t, h)

	_, err := h.GetValidToken(context.Background())
	if err == nil {
		t.Fatal("expected error when token refresh fails")
	}
}

func TestTokensExist(t *testing.T) {
	h := newTestHandler(t)
	if h.TokensExist() {
		t.Error("TokensExist should be false before any tokens are stored")
	}
	writeFreshTokens(t, h)
	if !h.TokensExist() {
		t.Error("TokensExist should be true after storing tokens")
	}
}

func TestGenerateCSRFState_Unique(t *testing.T) {
	s1, err1 := generateCSRFState()
	s2, err2 := generateCSRFState()
	if err1 != nil || err2 != nil {
		t.Fatalf("generateCSRFState errors: %v %v", err1, err2)
	}
	if s1 == s2 {
		t.Error("two CSRF states must be unique")
	}
	if len(s1) != 32 { // 16 bytes → 32 hex chars
		t.Errorf("CSRF state should be 32 hex chars, got %d", len(s1))
	}
}

// isAuthError is a type-assertion helper for *AuthError wrapped in fmt.Errorf chains.
func isAuthError(err error, target **AuthError) bool {
	if ae, ok := err.(*AuthError); ok {
		*target = ae
		return true
	}
	// Unwrap one level (fmt.Errorf %w).
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok {
		return isAuthError(u.Unwrap(), target)
	}
	return false
}
