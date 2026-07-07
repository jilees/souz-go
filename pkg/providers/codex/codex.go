package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"souz.ru/souz-go/pkg/providers"
)

const (
	originator = "codex_cli_rs"
	openaiBeta = "responses=experimental"
)

// responsesURL is a var, not a const, so tests can point it at an httptest
// server instead of the real Codex endpoint.
var responsesURL = "https://chatgpt.com/backend-api/codex/responses"

var _ providers.LLMProvider = (*Provider)(nil)

// Provider implements providers.LLMProvider against OpenAI's Responses API
// using a ChatGPT-subscription OAuth token pulled from Store, refreshed on
// demand. Unlike anthropic/openai_compat there is no static API key: run
// the device-authorization flow once (Login, or `souz-agent -codex-login`)
// to link an account before this provider can make requests.
type Provider struct {
	Store      *TokenStore
	HTTPClient *http.Client

	mu sync.Mutex // serializes refresh-then-save around concurrent requests
}

func (p *Provider) httpClient() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return http.DefaultClient
}

// ensureValidToken returns a currently-valid access token and account id,
// refreshing and persisting a new one if the cached token is missing or
// close to expiry (matching the original's REFRESH_BUFFER_SECONDS=300
// lazy-refresh-before-use trigger — no background scheduler needed).
func (p *Provider) ensureValidToken(ctx context.Context) (accessToken, accountID string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	tok, err := p.Store.Load()
	if err != nil {
		return "", "", err
	}
	if tok == nil || tok.AccessToken == "" {
		return "", "", fmt.Errorf("codex: not linked (run `souz-agent -codex-login` first)")
	}

	if time.Now().Unix() < tok.ExpiresAt-refreshBufferSecs {
		return tok.AccessToken, tok.AccountID, nil
	}
	if tok.RefreshToken == "" {
		// Can't refresh; use what we have and let the API reject it if truly expired.
		return tok.AccessToken, tok.AccountID, nil
	}

	refreshed, err := RefreshAccessToken(ctx, p.httpClient(), tok.RefreshToken)
	if err != nil {
		// Refresh failed transiently (network blip): fall back to the
		// existing token rather than hard-failing the turn, matching the
		// original's "warn and keep going" behavior.
		return tok.AccessToken, tok.AccountID, nil
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = tok.RefreshToken // some responses omit it on refresh
	}
	if refreshed.AccountID == "" {
		refreshed.AccountID = tok.AccountID
	}
	if err := p.Store.Save(refreshed); err != nil {
		return "", "", fmt.Errorf("codex: save refreshed token: %w", err)
	}
	return refreshed.AccessToken, refreshed.AccountID, nil
}

// Chat performs a non-streaming chat completion. The Responses API
// requires stream:true regardless — same as the original, which drains
// the stream internally and returns only the final accumulated response.
func (p *Provider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	return p.ChatStream(ctx, req, func(string) {})
}

// ChatStream performs the request. onChunk is never called: Codex's stream
// only reports whole output items becoming available and the response
// completing, not incremental text tokens — see readCodexStream's doc
// comment for why this matches the original rather than being a Go-side
// simplification.
func (p *Provider) ChatStream(ctx context.Context, req providers.ChatRequest, onChunk func(delta string)) (*providers.ChatResponse, error) {
	token, accountID, err := p.ensureValidToken(ctx)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(buildResponsesRequest(req))
	if err != nil {
		return nil, fmt.Errorf("codex: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, responsesURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("codex: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Chatgpt-Account-Id", accountID)
	httpReq.Header.Set("originator", originator)
	httpReq.Header.Set("OpenAI-Beta", openaiBeta)

	resp, err := p.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("codex: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("codex: status %d: %s", resp.StatusCode, string(data))
	}

	return readCodexStream(resp.Body)
}
