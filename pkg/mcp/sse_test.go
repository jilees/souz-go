package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newSSEServer(t *testing.T, received chan<- string, pushCh <-chan string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support flushing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "event: endpoint\ndata: /messages\n\n")
		flusher.Flush()

		for {
			select {
			case msg, ok := <-pushCh:
				if !ok {
					return
				}
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- string(body)
		w.WriteHeader(http.StatusAccepted)
	})
	return httptest.NewServer(mux)
}

func TestHTTPSSETransport_RoundTrip(t *testing.T) {
	received := make(chan string, 1)
	pushCh := make(chan string, 1)
	server := newSSEServer(t, received, pushCh)
	defer server.Close()
	defer close(pushCh)

	transport := NewHTTPSSETransport(server.URL + "/sse")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := transport.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer transport.Close()

	req := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if err := transport.Send(ctx, req); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case got := <-received:
		if got != string(req) {
			t.Errorf("server received %q, want %q", got, req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the POST to arrive")
	}

	pushCh <- `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`
	select {
	case msg := <-transport.Recv():
		var decoded map[string]any
		if err := json.Unmarshal(msg, &decoded); err != nil {
			t.Fatalf("unmarshal: %v (%s)", err, msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the pushed SSE message")
	}
}

func TestHTTPSSETransport_SendBeforeReadyFails(t *testing.T) {
	transport := NewHTTPSSETransport("http://127.0.0.1:1/sse") // never started
	err := transport.Send(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected an error sending before Start/endpoint discovery")
	}
}

func TestHTTPSSETransport_StartFailsOnNonSSEServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	transport := NewHTTPSSETransport(server.URL)
	transport.EndpointTimeout = 200 * time.Millisecond
	if err := transport.Start(context.Background()); err == nil {
		t.Fatal("expected an error connecting to a non-SSE endpoint")
	}
}
