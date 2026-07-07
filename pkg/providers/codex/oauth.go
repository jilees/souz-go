package codex

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	clientID        = "app_EMoamEEZ73f0CkXaXp7hrann"
	issuer          = "https://auth.openai.com"
	redirectURI     = issuer + "/deviceauth/callback"
	verificationURL = "https://auth.openai.com/codex/device"

	maxPollAttempts     = 60
	defaultPollInterval = 5 * time.Second
	refreshBufferSecs   = 300
)

// userCodeURL/tokenPollURL/oauthTokenURL are vars, not consts, so tests can
// point them at an httptest server instead of the real OpenAI endpoints.
var (
	userCodeURL   = issuer + "/api/accounts/deviceauth/usercode"
	tokenPollURL  = issuer + "/api/accounts/deviceauth/token"
	oauthTokenURL = issuer + "/oauth/token"
)

// DeviceAuth is the result of starting a device-authorization flow: show
// UserCode to the person at VerificationURL, then poll.
type DeviceAuth struct {
	DeviceAuthID    string
	UserCode        string
	VerificationURL string
	Interval        time.Duration
}

func httpClientOrDefault(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// StartDeviceFlow requests a user code to begin linking a ChatGPT account.
func StartDeviceFlow(ctx context.Context, client *http.Client) (*DeviceAuth, error) {
	client = httpClientOrDefault(client)

	payload, _ := json.Marshal(map[string]string{"client_id": clientID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, userCodeURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("codex: build device flow request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex: start device flow: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("codex: read device flow response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("codex: start device flow: status %d: %s", resp.StatusCode, string(body))
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("codex: decode device flow response: %w", err)
	}
	deviceAuthID, _ := data["device_auth_id"].(string)
	userCode, _ := data["user_code"].(string)
	if deviceAuthID == "" || userCode == "" {
		return nil, fmt.Errorf("codex: device flow response missing device_auth_id/user_code")
	}
	interval := defaultPollInterval
	if v, ok := toInt64(data["interval"]); ok && v > 0 {
		interval = time.Duration(v) * time.Second
	}

	return &DeviceAuth{
		DeviceAuthID:    deviceAuthID,
		UserCode:        userCode,
		VerificationURL: verificationURL,
		Interval:        interval,
	}, nil
}

// PollForAuthorization polls until the person has entered UserCode at
// VerificationURL, or the flow times out after maxPollAttempts.
func PollForAuthorization(ctx context.Context, client *http.Client, da *DeviceAuth) (authorizationCode, codeVerifier string, err error) {
	client = httpClientOrDefault(client)

	for attempt := 0; attempt < maxPollAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(da.Interval):
		}

		authCode, verifier, ok, err := pollOnce(ctx, client, da)
		if err != nil {
			return "", "", err
		}
		if ok {
			return authCode, verifier, nil
		}
		// Not authorized yet; keep polling.
	}
	return "", "", fmt.Errorf("codex: timed out waiting for authorization")
}

func pollOnce(ctx context.Context, client *http.Client, da *DeviceAuth) (authorizationCode, codeVerifier string, ok bool, err error) {
	payload, _ := json.Marshal(map[string]string{"device_auth_id": da.DeviceAuthID, "user_code": da.UserCode})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenPollURL, bytes.NewReader(payload))
	if err != nil {
		return "", "", false, fmt.Errorf("codex: build poll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", false, fmt.Errorf("codex: poll: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", false, fmt.Errorf("codex: read poll response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return "", "", false, nil
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return "", "", false, nil
	}
	authCode, _ := data["authorization_code"].(string)
	verifier, _ := data["code_verifier"].(string)
	if authCode == "" || verifier == "" {
		return "", "", false, nil
	}
	return authCode, verifier, true, nil
}

// ExchangeCode trades an authorization code for a Token.
func ExchangeCode(ctx context.Context, client *http.Client, authorizationCode, codeVerifier string) (*Token, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authorizationCode},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {codeVerifier},
	}
	return postTokenForm(ctx, client, form)
}

// RefreshAccessToken trades a refresh token for a new Token.
func RefreshAccessToken(ctx context.Context, client *http.Client, refreshToken string) (*Token, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"scope":         {"openid profile email"},
	}
	return postTokenForm(ctx, client, form)
}

func postTokenForm(ctx context.Context, client *http.Client, form url.Values) (*Token, error) {
	client = httpClientOrDefault(client)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("codex: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex: token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("codex: read token response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("codex: token request: status %d: %s", resp.StatusCode, string(body))
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("codex: decode token response: %w", err)
	}
	accessToken, _ := data["access_token"].(string)
	if accessToken == "" {
		return nil, fmt.Errorf("codex: token response missing access_token")
	}
	refreshToken, _ := data["refresh_token"].(string)
	expiresIn, ok := toInt64(data["expires_in"])
	if !ok {
		expiresIn = 3600
	}

	return &Token{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		AccountID:    extractAccountID(accessToken),
		ExpiresAt:    time.Now().Unix() + expiresIn,
	}, nil
}

// extractAccountID pulls the ChatGPT account id out of the access token's
// JWT payload. The signature is not verified — same as the original, which
// only reads claims for display/header purposes; the API call itself is
// what actually authenticates, via the bearer token.
func extractAccountID(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return ""
	}
	payload := parts[1]
	if m := len(payload) % 4; m != 0 {
		payload += strings.Repeat("=", 4-m)
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return ""
	}
	if id, ok := claims["chatgpt_account_id"].(string); ok && id != "" {
		return id
	}
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if id, ok := auth["chatgpt_account_id"].(string); ok {
			return id
		}
	}
	if id, ok := claims["https://api.openai.com/auth.chatgpt_account_id"].(string); ok {
		return id
	}
	return ""
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	default:
		return 0, false
	}
}

// Login runs the interactive device-authorization flow end to end: prints
// the user code and verification URL to out, polls until the person
// authorizes, exchanges the code, and saves the resulting Token to store.
// Intended for a one-off CLI invocation (e.g. `souz-agent -codex-login`),
// not something the agent runs on its own.
func Login(ctx context.Context, client *http.Client, store *TokenStore, out io.Writer) error {
	da, err := StartDeviceFlow(ctx, client)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "To link your ChatGPT account, visit:\n\n  %s\n\nand enter this code:\n\n  %s\n\nWaiting for authorization...\n",
		da.VerificationURL, da.UserCode)

	authCode, codeVerifier, err := PollForAuthorization(ctx, client, da)
	if err != nil {
		return err
	}
	tok, err := ExchangeCode(ctx, client, authCode, codeVerifier)
	if err != nil {
		return err
	}
	if err := store.Save(tok); err != nil {
		return err
	}
	fmt.Fprintf(out, "Codex linked (account %s).\n", tok.AccountID)
	return nil
}
