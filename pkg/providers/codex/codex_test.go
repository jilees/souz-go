package codex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"souz.ru/souz-go/pkg/providers"
)

func responsesSSE(text string) string {
	return "event: response.output_item.done\n" +
		fmt.Sprintf(`data: {"item":{"type":"message","content":[{"type":"output_text","text":%q}]}}`, text) + "\n\n" +
		"event: response.completed\n" +
		`data: {"response":{"usage":{"input_tokens":3,"output_tokens":2}}}` + "\n\n"
}

func TestProvider_Chat_UsesStoredToken(t *testing.T) {
	var gotAuth, gotAccount string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("Chatgpt-Account-Id")
		fmt.Fprint(w, responsesSSE("hello from codex"))
	}))
	defer server.Close()
	origResponsesURL := responsesURL
	responsesURL = server.URL
	t.Cleanup(func() { responsesURL = origResponsesURL })

	store := NewTokenStore(filepath.Join(t.TempDir(), "token.json"))
	if err := store.Save(&Token{
		AccessToken: "at-1", RefreshToken: "rt-1", AccountID: "acct-1",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	provider := &Provider{Store: store}
	resp, err := provider.Chat(context.Background(), providers.ChatRequest{
		Model:    "gpt-5-codex",
		Messages: []providers.Message{{Role: providers.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "hello from codex" {
		t.Errorf("Content = %q", resp.Content)
	}
	if gotAuth != "Bearer at-1" {
		t.Errorf("Authorization header = %q", gotAuth)
	}
	if gotAccount != "acct-1" {
		t.Errorf("Chatgpt-Account-Id header = %q", gotAccount)
	}
}

func TestProvider_Chat_RefreshesExpiringToken(t *testing.T) {
	var refreshCalled bool
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalled = true
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"new-rt","expires_in":3600}`, testJWT(t, "acct-fresh"))
	}))
	defer tokenServer.Close()
	origOAuthTokenURL := oauthTokenURL
	oauthTokenURL = tokenServer.URL
	t.Cleanup(func() { oauthTokenURL = origOAuthTokenURL })

	var gotAuth string
	responsesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, responsesSSE("ok"))
	}))
	defer responsesServer.Close()
	origResponsesURL := responsesURL
	responsesURL = responsesServer.URL
	t.Cleanup(func() { responsesURL = origResponsesURL })

	store := NewTokenStore(filepath.Join(t.TempDir(), "token.json"))
	if err := store.Save(&Token{
		AccessToken: "stale-at", RefreshToken: "old-rt", AccountID: "acct-old",
		ExpiresAt: time.Now().Add(10 * time.Second).Unix(), // inside the 300s refresh buffer
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	provider := &Provider{Store: store}
	if _, err := provider.Chat(context.Background(), providers.ChatRequest{
		Messages: []providers.Message{{Role: providers.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if !refreshCalled {
		t.Fatal("expected a token refresh")
	}
	if gotAuth != "Bearer "+testJWT(t, "acct-fresh") {
		t.Errorf("Authorization header = %q", gotAuth)
	}

	saved, err := store.Load()
	if err != nil || saved == nil || saved.AccountID != "acct-fresh" {
		t.Fatalf("expected the refreshed token persisted, got %+v, %v", saved, err)
	}
}

func TestProvider_Chat_NotLinkedIsAnError(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "token.json")) // never saved
	provider := &Provider{Store: store}
	_, err := provider.Chat(context.Background(), providers.ChatRequest{
		Messages: []providers.Message{{Role: providers.RoleUser, Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "codex-login") {
		t.Fatalf("expected a not-linked error mentioning codex-login, got %v", err)
	}
}

func TestProvider_ChatStream_DoesNotCallOnChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, responsesSSE("streamed but not chunked"))
	}))
	defer server.Close()
	origResponsesURL := responsesURL
	responsesURL = server.URL
	t.Cleanup(func() { responsesURL = origResponsesURL })

	store := NewTokenStore(filepath.Join(t.TempDir(), "token.json"))
	store.Save(&Token{AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour).Unix()})

	provider := &Provider{Store: store}
	var chunks []string
	resp, err := provider.ChatStream(context.Background(), providers.ChatRequest{
		Messages: []providers.Message{{Role: providers.RoleUser, Content: "hi"}},
	}, func(delta string) { chunks = append(chunks, delta) })
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected no onChunk calls (Codex doesn't emit text deltas), got %v", chunks)
	}
	if resp.Content != "streamed but not chunked" {
		t.Errorf("Content = %q", resp.Content)
	}
}

func TestProvider_Chat_PropagatesHTTPErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "forbidden")
	}))
	defer server.Close()
	origResponsesURL := responsesURL
	responsesURL = server.URL
	t.Cleanup(func() { responsesURL = origResponsesURL })

	store := NewTokenStore(filepath.Join(t.TempDir(), "token.json"))
	store.Save(&Token{AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour).Unix()})

	provider := &Provider{Store: store}
	_, err := provider.Chat(context.Background(), providers.ChatRequest{
		Messages: []providers.Message{{Role: providers.RoleUser, Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected an error mentioning status 403, got %v", err)
	}
}
