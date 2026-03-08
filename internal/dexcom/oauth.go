package dexcom

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/johnmartinez/cgm-get-agent/internal/config"
	"github.com/johnmartinez/cgm-get-agent/internal/crypto"
	"github.com/johnmartinez/cgm-get-agent/internal/types"
)

// OAuthHandler manages the Dexcom OAuth2 token lifecycle.
//
//   - HandleStart / HandleCallback: HTTP endpoints for the authorization code flow.
//   - GetValidToken: called by Client before every Dexcom API request; refreshes
//     transparently when the access_token is within 5 minutes of expiry.
//
// Token refresh is serialized by an internal mutex. If two goroutines both detect
// impending expiry, the second re-reads the (already refreshed) tokens from disk
// after the first goroutine releases the lock and returns without calling Dexcom again.
type OAuthHandler struct {
	clientID     string
	clientSecret string
	redirectURI  string
	baseURL      string
	tokenPath    string
	encKey       []byte
	httpClient   *http.Client

	csrfStates sync.Map  // map[string]struct{}; values are disposable
	mu         sync.Mutex // serializes refreshIfNeeded
}

// NewOAuthHandler creates an OAuthHandler from application config.
func NewOAuthHandler(cfg *config.Config) *OAuthHandler {
	return &OAuthHandler{
		clientID:     cfg.Dexcom.ClientID,
		clientSecret: cfg.Dexcom.ClientSecret,
		redirectURI:  cfg.Dexcom.RedirectURI,
		baseURL:      BaseURL(cfg.Dexcom.Environment),
		tokenPath:    cfg.Storage.TokenPath,
		encKey:       cfg.EncryptionKey,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// HandleStart generates a CSRF state token, stores it server-side, and redirects
// the user's browser to the Dexcom OAuth2 authorization page.
//
// GET /oauth/start
func (h *OAuthHandler) HandleStart(w http.ResponseWriter, r *http.Request) {
	state, err := generateCSRFState()
	if err != nil {
		http.Error(w, "failed to generate CSRF state", http.StatusInternalServerError)
		return
	}
	h.csrfStates.Store(state, struct{}{})

	params := url.Values{
		"client_id":     {h.clientID},
		"redirect_uri":  {h.redirectURI},
		"response_type": {"code"},
		"scope":         {"offline_access"},
		"state":         {state},
	}
	slog.Error("oauth start redirect",
		"redirect_uri_in_auth_request", h.redirectURI,
		"base_url", h.baseURL,
	)
	http.Redirect(w, r, h.baseURL+"/v3/oauth2/login?"+params.Encode(), http.StatusFound)
}

// HandleCallback validates the CSRF state, exchanges the authorization code for
// tokens, encrypts, and stores them. Writes a success or error page.
//
// GET /callback?code=...&state=...
func (h *OAuthHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	callbackTime := time.Now().UTC()
	q := r.URL.Query()
	slog.Error("oauth callback received",
		"query", r.URL.RawQuery,
		"callback_time", callbackTime.Format(time.RFC3339Nano),
	)

	if errParam := q.Get("error"); errParam != "" {
		slog.Error("oauth callback error from Dexcom",
			"error", errParam,
			"error_description", q.Get("error_description"),
		)
		http.Error(w, "Dexcom authorization failed: "+errParam, http.StatusBadRequest)
		return
	}

	state := q.Get("state")
	if _, ok := h.csrfStates.LoadAndDelete(state); !ok {
		http.Error(w, "invalid or expired CSRF state", http.StatusBadRequest)
		return
	}

	tokens, err := h.exchangeCode(r.Context(), q.Get("code"))
	if err != nil {
		slog.Error("oauth token exchange failed", "error", err.Error())
		http.Error(w, "token exchange failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := crypto.SaveTokens(h.tokenPath, tokens, h.encKey); err != nil {
		http.Error(w, "failed to store tokens", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<html><body>
<h2>Authorization complete.</h2>
<p>CGM Get Agent is now connected to Dexcom. You may close this tab.</p>
</body></html>`)
}

// GetValidToken returns a valid Dexcom access token, refreshing transparently if needed.
// Safe for concurrent use.
func (h *OAuthHandler) GetValidToken(ctx context.Context) (string, error) {
	slog.Error("DEBUG GetValidToken: entering")
	tokens, err := h.refreshIfNeeded(ctx)
	if err != nil {
		slog.Error("DEBUG GetValidToken: refreshIfNeeded failed", "error", err)
		return "", err
	}
	slog.Error("DEBUG GetValidToken: returning valid token")
	return tokens.AccessToken, nil
}

// TokensExist reports whether a readable, decryptable token file exists.
// Used by the health check endpoint.
func (h *OAuthHandler) TokensExist() bool {
	_, err := crypto.LoadTokens(h.tokenPath, h.encKey)
	return err == nil
}

// LoadTokens returns the current stored tokens (without refreshing).
// Used by the health check to inspect expiry without side effects.
func (h *OAuthHandler) LoadTokens() (types.OAuthTokens, error) {
	return crypto.LoadTokens(h.tokenPath, h.encKey)
}

// refreshIfNeeded loads tokens from disk, refreshes if expiring within 5 minutes,
// and returns valid tokens. The mutex ensures only one refresh happens concurrently.
func (h *OAuthHandler) refreshIfNeeded(ctx context.Context) (types.OAuthTokens, error) {
	slog.Error("DEBUG refreshIfNeeded: acquiring mutex")
	h.mu.Lock()
	slog.Error("DEBUG refreshIfNeeded: mutex acquired")
	defer h.mu.Unlock()

	slog.Error("DEBUG refreshIfNeeded: loading tokens from disk", "path", h.tokenPath)
	// Always re-read from disk inside the lock: another goroutine may have already refreshed.
	tokens, err := crypto.LoadTokens(h.tokenPath, h.encKey)
	if err != nil {
		slog.Error("DEBUG refreshIfNeeded: LoadTokens failed", "error", err)
		return types.OAuthTokens{}, &AuthError{
			Message: "no tokens found — visit /oauth/start to authorize",
		}
	}
	slog.Error("DEBUG refreshIfNeeded: tokens loaded", "expires_at", tokens.ExpiresAt, "time_until_expiry", time.Until(tokens.ExpiresAt).String())

	if time.Until(tokens.ExpiresAt) > 5*time.Minute {
		slog.Error("DEBUG refreshIfNeeded: tokens still fresh, returning")
		return tokens, nil // still fresh
	}

	slog.Error("DEBUG refreshIfNeeded: tokens expiring soon, refreshing")
	refreshed, err := h.doRefresh(ctx, tokens.RefreshToken)
	if err != nil {
		slog.Error("DEBUG refreshIfNeeded: doRefresh failed", "error", err)
		return types.OAuthTokens{}, fmt.Errorf("dexcom: refreshing token: %w", err)
	}

	slog.Error("DEBUG refreshIfNeeded: saving refreshed tokens")
	if err := crypto.SaveTokens(h.tokenPath, refreshed, h.encKey); err != nil {
		slog.Error("DEBUG refreshIfNeeded: SaveTokens failed", "error", err)
		return types.OAuthTokens{}, fmt.Errorf("dexcom: saving refreshed tokens: %w", err)
	}
	slog.Error("DEBUG refreshIfNeeded: refresh complete")
	return refreshed, nil
}

// doRefresh exchanges a refresh_token for a new access_token + refresh_token pair.
// CRITICAL: refresh_token is single-use. The new refresh_token in the response
// must be saved before this function returns; the old one is now invalid on Dexcom's side.
func (h *OAuthHandler) doRefresh(ctx context.Context, refreshToken string) (types.OAuthTokens, error) {
	return h.doTokenRequest(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {h.clientID},
		"client_secret": {h.clientSecret},
		"redirect_uri":  {h.redirectURI},
	})
}

// exchangeCode performs the authorization_code grant: code → access_token + refresh_token.
func (h *OAuthHandler) exchangeCode(ctx context.Context, code string) (types.OAuthTokens, error) {
	codePreview := code
	if len(codePreview) > 8 {
		codePreview = codePreview[:8] + "..."
	}
	slog.Error("oauth exchangeCode starting",
		"code_preview", codePreview,
		"redirect_uri", h.redirectURI,
	)
	return h.doTokenRequest(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {h.clientID},
		"client_secret": {h.clientSecret},
		"redirect_uri":  {h.redirectURI},
	})
}

// doTokenRequest POSTs form-encoded params to the Dexcom token endpoint.
func (h *OAuthHandler) doTokenRequest(ctx context.Context, params url.Values) (types.OAuthTokens, error) {
	tokenURL := h.baseURL + "/v3/oauth2/token"

	// Log request details (mask client_secret).
	maskedSecret := params.Get("client_secret")
	if len(maskedSecret) > 4 {
		maskedSecret = maskedSecret[:4] + "****"
	}
	slog.Error("oauth token request",
		"url", tokenURL,
		"grant_type", params.Get("grant_type"),
		"client_id", params.Get("client_id"),
		"client_secret_masked", maskedSecret,
		"redirect_uri", params.Get("redirect_uri"),
		"has_code", params.Get("code") != "",
		"has_refresh_token", params.Get("refresh_token") != "",
		"request_time", time.Now().UTC().Format(time.RFC3339Nano),
	)

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		tokenURL,
		strings.NewReader(params.Encode()),
	)
	if err != nil {
		return types.OAuthTokens{}, fmt.Errorf("dexcom: building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	startTime := time.Now()
	resp, err := h.httpClient.Do(req)
	elapsed := time.Since(startTime)
	if err != nil {
		slog.Error("oauth token request failed",
			"error", err.Error(),
			"elapsed_ms", elapsed.Milliseconds(),
		)
		return types.OAuthTokens{}, fmt.Errorf("dexcom: token request: %w", err)
	}
	defer resp.Body.Close()

	slog.Error("oauth token response",
		"status", resp.StatusCode,
		"elapsed_ms", elapsed.Milliseconds(),
		"response_time", time.Now().UTC().Format(time.RFC3339Nano),
	)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		slog.Error("oauth token endpoint error",
			"status", resp.StatusCode,
			"response_body", string(body),
		)
		return types.OAuthTokens{}, &AuthError{
			Message: fmt.Sprintf("token endpoint returned HTTP %d: %s", resp.StatusCode, string(body)),
		}
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return types.OAuthTokens{}, fmt.Errorf("dexcom: decoding token response: %w", err)
	}
	if tr.AccessToken == "" || tr.RefreshToken == "" {
		return types.OAuthTokens{}, &AuthError{Message: "token response missing access_token or refresh_token"}
	}

	now := time.Now().UTC()
	return types.OAuthTokens{
		AccessToken:   tr.AccessToken,
		RefreshToken:  tr.RefreshToken,
		ExpiresAt:     now.Add(time.Duration(tr.ExpiresIn) * time.Second),
		LastRefreshed: now,
	}, nil
}

// generateCSRFState returns a cryptographically random 16-byte hex string.
func generateCSRFState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
