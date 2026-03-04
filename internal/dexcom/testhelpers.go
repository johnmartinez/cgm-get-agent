package dexcom

import "net/http"

// NewClientForTest creates a Client and OAuthHandler backed by the given base URL,
// token file path, encryption key, and HTTP client.
//
// Intended exclusively for integration tests in other packages that need to wire
// up the full Dexcom stack against an httptest.Server.
func NewClientForTest(baseURL, tokenPath string, encKey []byte, httpClient *http.Client) (*Client, *OAuthHandler) {
	oauth := &OAuthHandler{
		clientID:     "test-client-id",
		clientSecret: "test-secret",
		redirectURI:  "http://localhost/callback",
		baseURL:      baseURL,
		tokenPath:    tokenPath,
		encKey:       encKey,
		httpClient:   httpClient,
	}
	client := &Client{
		baseURL:    baseURL,
		oauth:      oauth,
		httpClient: httpClient,
	}
	return client, oauth
}
