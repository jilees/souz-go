package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var _ Transport = (*HTTPSSETransport)(nil)

// HTTPSSETransport implements MCP's HTTP+SSE transport: a GET request opens
// a Server-Sent Events stream; the server's first event ("endpoint") gives
// the URL the client then POSTs JSON-RPC messages to, with the corresponding
// JSON-RPC responses arriving asynchronously as further SSE "message" events.
type HTTPSSETransport struct {
	// URL is the SSE endpoint to GET, e.g. "http://localhost:8000/sse".
	URL        string
	HTTPClient *http.Client
	// EndpointTimeout bounds how long Start waits for the server's initial
	// "endpoint" event; defaults to 10s.
	EndpointTimeout time.Duration

	body   io.ReadCloser
	recvCh chan json.RawMessage
	ready  chan struct{}

	mu      sync.Mutex
	postURL string
}

// NewHTTPSSETransport builds a transport that connects to the given SSE URL.
func NewHTTPSSETransport(sseURL string) *HTTPSSETransport {
	return &HTTPSSETransport{URL: sseURL}
}

func (t *HTTPSSETransport) httpClient() *http.Client {
	if t.HTTPClient != nil {
		return t.HTTPClient
	}
	return http.DefaultClient
}

func (t *HTTPSSETransport) Start(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		return fmt.Errorf("mcp: build sse request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := t.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("mcp: connect sse: %w", err)
	}
	if resp.StatusCode >= 300 {
		resp.Body.Close()
		return fmt.Errorf("mcp: connect sse: status %d", resp.StatusCode)
	}

	t.body = resp.Body
	t.recvCh = make(chan json.RawMessage, 16)
	t.ready = make(chan struct{})
	go t.readLoop()

	timeout := t.EndpointTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	select {
	case <-t.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(timeout):
		return fmt.Errorf("mcp: timed out waiting for the server's endpoint event")
	}
}

func (t *HTTPSSETransport) readLoop() {
	defer close(t.recvCh)
	defer t.body.Close()

	scanner := bufio.NewScanner(t.body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	var eventType string
	var dataLines []string
	flush := func() {
		if len(dataLines) == 0 {
			eventType = ""
			return
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		switch eventType {
		case "endpoint":
			t.setPostURL(data)
		case "message", "":
			t.recvCh <- json.RawMessage([]byte(data))
		}
		eventType = ""
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// ignore "id:", "retry:", comments, etc.
		}
	}
	flush()
}

func (t *HTTPSSETransport) setPostURL(raw string) {
	resolved := raw
	if base, err := url.Parse(t.URL); err == nil {
		if ref, err := url.Parse(raw); err == nil {
			resolved = base.ResolveReference(ref).String()
		}
	}
	t.mu.Lock()
	alreadySet := t.postURL != ""
	t.postURL = resolved
	t.mu.Unlock()
	if !alreadySet {
		close(t.ready)
	}
}

func (t *HTTPSSETransport) Send(ctx context.Context, msg json.RawMessage) error {
	t.mu.Lock()
	postURL := t.postURL
	t.mu.Unlock()
	if postURL == "" {
		return fmt.Errorf("mcp: transport not ready: no endpoint from the server yet")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, bytes.NewReader(msg))
	if err != nil {
		return fmt.Errorf("mcp: build post request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("mcp: post message: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mcp: post message: status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (t *HTTPSSETransport) Recv() <-chan json.RawMessage { return t.recvCh }

func (t *HTTPSSETransport) Close() error {
	if t.body != nil {
		return t.body.Close()
	}
	return nil
}
