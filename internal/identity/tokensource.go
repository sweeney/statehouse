package identity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TokenSource fetches and caches a client_credentials access token from the
// identity service. Token() is safe for concurrent use; only one in-flight
// fetch runs at a time. The cached token is refreshed automatically when it
// is within expiryBuffer of expiry.
type TokenSource struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client

	mu          sync.Mutex
	cachedToken string
	expiresAt   time.Time
}

// expiryBuffer is how early we proactively refresh before the token expires.
const expiryBuffer = 30 * time.Second

// Token returns a valid Bearer token, fetching a new one if the cache is
// empty or within expiryBuffer of expiry.
func (ts *TokenSource) Token() (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.cachedToken != "" && time.Now().Add(expiryBuffer).Before(ts.expiresAt) {
		return ts.cachedToken, nil
	}

	tok, expiresIn, err := ts.fetch()
	if err != nil {
		return "", err
	}

	ts.cachedToken = tok
	ts.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	return ts.cachedToken, nil
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type oauthError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (ts *TokenSource) fetch() (token string, expiresIn int, err error) {
	client := ts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	body := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {ts.ClientID},
		"client_secret": {ts.ClientSecret},
	}

	req, err := http.NewRequest(http.MethodPost, ts.BaseURL+"/oauth/token", strings.NewReader(body.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("identity: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("identity: token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var oerr oauthError
		if jsonErr := json.NewDecoder(resp.Body).Decode(&oerr); jsonErr == nil && oerr.Error != "" {
			return "", 0, fmt.Errorf("identity: %s: %s", oerr.Error, oerr.ErrorDescription)
		}
		return "", 0, fmt.Errorf("identity: unexpected status %d", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", 0, fmt.Errorf("identity: decode response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", 0, fmt.Errorf("identity: empty access_token in response")
	}
	if tr.ExpiresIn <= 0 {
		return "", 0, fmt.Errorf("identity: invalid expires_in %d in response", tr.ExpiresIn)
	}

	return tr.AccessToken, tr.ExpiresIn, nil
}
