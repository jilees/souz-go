package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withTestEndpoints points the package-level OAuth endpoint vars at ts for
// the duration of the test, restoring the originals on cleanup.
func withTestEndpoints(t *testing.T, ts *httptest.Server) {
	t.Helper()
	origUserCode, origTokenPoll, origOAuthToken := userCodeURL, tokenPollURL, oauthTokenURL
	userCodeURL = ts.URL + "/usercode"
	tokenPollURL = ts.URL + "/poll"
	oauthTokenURL = ts.URL + "/token"
	t.Cleanup(func() {
		userCodeURL, tokenPollURL, oauthTokenURL = origUserCode, origTokenPoll, origOAuthToken
	})
}

func TestStartDeviceFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["client_id"] != clientID {
			t.Errorf("unexpected client_id: %q", body["client_id"])
		}
		fmt.Fprint(w, `{"device_auth_id":"da-1","user_code":"ABCD-1234","interval":1}`)
	}))
	defer server.Close()
	withTestEndpoints(t, server)

	da, err := StartDeviceFlow(context.Background(), nil)
	if err != nil {
		t.Fatalf("StartDeviceFlow: %v", err)
	}
	if da.DeviceAuthID != "da-1" || da.UserCode != "ABCD-1234" || da.Interval != time.Second {
		t.Errorf("unexpected DeviceAuth: %+v", da)
	}
	if da.VerificationURL == "" {
		t.Error("expected a non-empty VerificationURL")
	}
}

func TestStartDeviceFlow_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"boom"}`)
	}))
	defer server.Close()
	withTestEndpoints(t, server)

	if _, err := StartDeviceFlow(context.Background(), nil); err == nil {
		t.Fatal("expected an error")
	}
}

func TestPollForAuthorization_SucceedsAfterPending(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/poll") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusBadRequest) // "authorization_pending"-style response
			return
		}
		fmt.Fprint(w, `{"authorization_code":"auth-code","code_verifier":"verifier-1"}`)
	}))
	defer server.Close()
	withTestEndpoints(t, server)

	da := &DeviceAuth{DeviceAuthID: "da-1", UserCode: "ABCD-1234", Interval: 5 * time.Millisecond}
	code, verifier, err := PollForAuthorization(context.Background(), nil, da)
	if err != nil {
		t.Fatalf("PollForAuthorization: %v", err)
	}
	if code != "auth-code" || verifier != "verifier-1" {
		t.Errorf("unexpected result: code=%q verifier=%q", code, verifier)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestPollForAuthorization_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	withTestEndpoints(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	da := &DeviceAuth{DeviceAuthID: "da-1", UserCode: "X", Interval: time.Millisecond}
	if _, _, err := PollForAuthorization(ctx, nil, da); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
}

func TestExchangeCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.PostForm.Get("grant_type") != "authorization_code" ||
			r.PostForm.Get("code") != "auth-code" ||
			r.PostForm.Get("code_verifier") != "verifier-1" {
			t.Errorf("unexpected form: %v", r.PostForm)
		}
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rt-1","expires_in":3600}`, testJWT(t, "acct-42"))
	}))
	defer server.Close()
	withTestEndpoints(t, server)

	tok, err := ExchangeCode(context.Background(), nil, "auth-code", "verifier-1")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if tok.RefreshToken != "rt-1" || tok.AccountID != "acct-42" {
		t.Errorf("unexpected token: %+v", tok)
	}
	if tok.ExpiresAt <= time.Now().Unix() {
		t.Errorf("expected ExpiresAt in the future, got %d", tok.ExpiresAt)
	}
}

func TestRefreshAccessToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != "old-rt" {
			t.Errorf("unexpected form: %v", r.PostForm)
		}
		fmt.Fprintf(w, `{"access_token":%q,"expires_in":"1800"}`, testJWT(t, "acct-7"))
	}))
	defer server.Close()
	withTestEndpoints(t, server)

	tok, err := RefreshAccessToken(context.Background(), nil, "old-rt")
	if err != nil {
		t.Fatalf("RefreshAccessToken: %v", err)
	}
	if tok.AccountID != "acct-7" {
		t.Errorf("unexpected account id: %q", tok.AccountID)
	}
}

func TestPostTokenForm_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer server.Close()
	withTestEndpoints(t, server)

	if _, err := RefreshAccessToken(context.Background(), nil, "bad-rt"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestExtractAccountID(t *testing.T) {
	jwt := testJWT(t, "acct-99")
	if got := extractAccountID(jwt); got != "acct-99" {
		t.Errorf("extractAccountID = %q, want %q", got, "acct-99")
	}
}

func TestExtractAccountID_NestedClaim(t *testing.T) {
	payload := map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-nested"},
	}
	jwt := "header." + base64.URLEncoding.EncodeToString(marshalJSON(t, payload)) + ".sig"
	if got := extractAccountID(jwt); got != "acct-nested" {
		t.Errorf("extractAccountID = %q, want %q", got, "acct-nested")
	}
}

func TestExtractAccountID_Malformed(t *testing.T) {
	if got := extractAccountID("not-a-jwt"); got != "" {
		t.Errorf("expected empty string for a malformed JWT, got %q", got)
	}
}

func TestLogin_EndToEnd(t *testing.T) {
	var polls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/usercode"):
			fmt.Fprint(w, `{"device_auth_id":"da-1","user_code":"CODE-1","interval":1}`)
		case strings.HasSuffix(r.URL.Path, "/poll"):
			polls++
			if polls < 2 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"authorization_code":"ac-1","code_verifier":"cv-1"}`)
		case strings.HasSuffix(r.URL.Path, "/token"):
			fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rt-1","expires_in":3600}`, testJWT(t, "acct-login"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	withTestEndpoints(t, server)

	store := NewTokenStore(t.TempDir() + "/token.json")
	var out strings.Builder
	if err := Login(context.Background(), nil, store, &out); err != nil {
		t.Fatalf("Login: %v", err)
	}

	if !strings.Contains(out.String(), "CODE-1") {
		t.Errorf("expected the user code printed, got %q", out.String())
	}
	tok, err := store.Load()
	if err != nil || tok == nil || tok.AccountID != "acct-login" {
		t.Fatalf("unexpected stored token: %+v, %v", tok, err)
	}
}

// testJWT builds a minimally-valid unsigned JWT string carrying
// chatgpt_account_id, for tests that need extractAccountID to succeed.
func testJWT(t *testing.T, accountID string) string {
	t.Helper()
	payload := marshalJSON(t, map[string]any{"chatgpt_account_id": accountID})
	return "header." + base64.URLEncoding.EncodeToString(payload) + ".sig"
}

func marshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
