package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// fakeTransport is an in-memory Transport controlled entirely by the test:
// Send records outgoing messages on sent, Recv yields whatever the test
// pushes onto recv.
type fakeTransport struct {
	sent chan json.RawMessage
	recv chan json.RawMessage
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{sent: make(chan json.RawMessage, 16), recv: make(chan json.RawMessage, 16)}
}

func (f *fakeTransport) Start(context.Context) error { return nil }
func (f *fakeTransport) Send(_ context.Context, msg json.RawMessage) error {
	f.sent <- msg
	return nil
}
func (f *fakeTransport) Recv() <-chan json.RawMessage { return f.recv }
func (f *fakeTransport) Close() error {
	close(f.recv)
	return nil
}

func (f *fakeTransport) expectRequest(t *testing.T, wantMethod string) rpcMessage {
	t.Helper()
	select {
	case raw := <-f.sent:
		var m rpcMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal sent message: %v", err)
		}
		if m.Method != wantMethod {
			t.Fatalf("expected method %q, got %q", wantMethod, m.Method)
		}
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for outgoing message")
		return rpcMessage{}
	}
}

func TestClient_Initialize(t *testing.T) {
	ft := newFakeTransport()
	c := NewClient(ft)
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		req := ft.expectRequest(t, "initialize")
		ft.recv <- json.RawMessage(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"test-server","version":"1.0"},"capabilities":{}}}`,
			string(req.ID)))
		ft.expectRequest(t, "notifications/initialized")
	}()

	result, err := c.Initialize(ctx, "souz-go", "0.1")
	<-done
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if result.ServerInfo.Name != "test-server" || result.ProtocolVersion != "2024-11-05" {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestClient_ListTools(t *testing.T) {
	ft := newFakeTransport()
	c := NewClient(ft)
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		req := ft.expectRequest(t, "tools/list")
		ft.recv <- json.RawMessage(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo","description":"echoes input","inputSchema":{"type":"object"}}]}}`,
			string(req.ID)))
	}()

	toolsList, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(toolsList) != 1 || toolsList[0].Name != "echo" {
		t.Errorf("unexpected tools: %+v", toolsList)
	}
}

func TestClient_CallTool(t *testing.T) {
	ft := newFakeTransport()
	c := NewClient(ft)
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		req := ft.expectRequest(t, "tools/call")
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Errorf("unmarshal params: %v", err)
		}
		if params.Name != "echo" {
			t.Errorf("expected tool name echo, got %q", params.Name)
		}
		ft.recv <- json.RawMessage(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"hello"}],"isError":false}}`,
			string(req.ID)))
	}()

	text, isError, err := c.CallTool(ctx, "echo", json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if isError {
		t.Error("expected isError=false")
	}
	if text != "hello" {
		t.Errorf("text = %q, want %q", text, "hello")
	}
}

func TestClient_CallTool_ServerReportedError(t *testing.T) {
	ft := newFakeTransport()
	c := NewClient(ft)
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		req := ft.expectRequest(t, "tools/call")
		ft.recv <- json.RawMessage(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"boom"}],"isError":true}}`,
			string(req.ID)))
	}()

	text, isError, err := c.CallTool(ctx, "boom", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !isError || text != "boom" {
		t.Errorf("text=%q isError=%v, want %q true", text, isError, "boom")
	}
}

func TestClient_RPCErrorIsReturnedAsGoError(t *testing.T) {
	ft := newFakeTransport()
	c := NewClient(ft)
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		req := ft.expectRequest(t, "tools/list")
		ft.recv <- json.RawMessage(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"method not found"}}`,
			string(req.ID)))
	}()

	if _, err := c.ListTools(ctx); err == nil {
		t.Fatal("expected an error")
	}
}

func TestClient_Close_UnblocksPendingCalls(t *testing.T) {
	ft := newFakeTransport()
	c := NewClient(ft)
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := c.ListTools(ctx)
		errCh <- err
	}()

	ft.expectRequest(t, "tools/list")
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("expected an error after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the pending call to unblock")
	}
}
