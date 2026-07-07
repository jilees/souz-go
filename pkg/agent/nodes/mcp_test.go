package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/mcp"
	"souz.ru/souz-go/pkg/providers"
)

// fakeMCPTransport is an in-memory mcp.Transport controlled by the test,
// mirroring pkg/mcp's own internal test fake (unexported there, so it can't
// be reused directly from this package).
type fakeMCPTransport struct {
	sent chan json.RawMessage
	recv chan json.RawMessage
}

func newFakeMCPTransport() *fakeMCPTransport {
	return &fakeMCPTransport{sent: make(chan json.RawMessage, 16), recv: make(chan json.RawMessage, 16)}
}

func (f *fakeMCPTransport) Start(context.Context) error { return nil }
func (f *fakeMCPTransport) Send(_ context.Context, msg json.RawMessage) error {
	f.sent <- msg
	return nil
}
func (f *fakeMCPTransport) Recv() <-chan json.RawMessage { return f.recv }
func (f *fakeMCPTransport) Close() error {
	close(f.recv)
	return nil
}

func (f *fakeMCPTransport) nextRequest(t *testing.T) (id json.RawMessage, method string) {
	t.Helper()
	select {
	case raw := <-f.sent:
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("unmarshal sent request: %v", err)
		}
		return req.ID, req.Method
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for outgoing MCP request")
		return nil, ""
	}
}

func (f *fakeMCPTransport) respondResult(id json.RawMessage, result any) {
	resultJSON, _ := json.Marshal(result)
	f.recv <- json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, string(id), resultJSON))
}

func (f *fakeMCPTransport) respondError(id json.RawMessage, code int, message string) {
	f.recv <- json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":%q}}`, string(id), code, message))
}

func TestMCP_MergesToolDefinitions(t *testing.T) {
	transport := newFakeMCPTransport()
	client := mcp.NewClient(transport)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		id, method := transport.nextRequest(t)
		if method != "tools/list" {
			t.Errorf("expected tools/list, got %q", method)
		}
		transport.respondResult(id, map[string]any{
			"tools": []map[string]any{
				{"name": "echo", "description": "echoes input", "inputSchema": map[string]any{"type": "object"}},
			},
		})
	}()

	node := NewMCP(map[string]*mcp.Client{"myserver": client})
	in := agent.AgentContext{ActiveTools: []providers.ToolDefinition{{Name: "local_tool"}}}
	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)

	if len(got.ActiveTools) != 2 {
		t.Fatalf("expected 2 active tools (1 local + 1 mcp), got %d: %+v", len(got.ActiveTools), got.ActiveTools)
	}
	found := false
	for _, def := range got.ActiveTools {
		if def.Name == "myserver.echo" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a namespaced myserver.echo tool, got %+v", got.ActiveTools)
	}
}

func TestMCP_NoClientsIsNoop(t *testing.T) {
	node := NewMCP(nil)
	in := agent.AgentContext{Input: "hi"}
	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out.(agent.AgentContext).ActiveTools) != 0 {
		t.Errorf("expected no active tools, got %+v", out.(agent.AgentContext).ActiveTools)
	}
}

func TestMCP_FailingClientIsSkipped(t *testing.T) {
	transport := newFakeMCPTransport()
	client := mcp.NewClient(transport)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		id, _ := transport.nextRequest(t)
		transport.respondError(id, -32000, "server unavailable")
	}()

	node := NewMCP(map[string]*mcp.Client{"broken": client})
	out, err := node.Execute(context.Background(), agent.AgentContext{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out.(agent.AgentContext).ActiveTools) != 0 {
		t.Errorf("expected a failing client to contribute no tools, got %+v", out.(agent.AgentContext).ActiveTools)
	}
}

func TestToolLoop_DispatchesMCPToolCall(t *testing.T) {
	transport := newFakeMCPTransport()
	client := mcp.NewClient(transport)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		id, method := transport.nextRequest(t)
		if method != "tools/call" {
			t.Errorf("expected tools/call, got %q", method)
		}
		transport.respondResult(id, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "42"}},
			"isError": false,
		})
	}()

	node := NewToolLoop(nil, map[string]*mcp.Client{"myserver": client})
	in := agent.AgentContext{
		History: []providers.Message{
			{
				Role: providers.RoleAssistant,
				ToolCalls: []providers.ToolCall{
					{ID: "call_1", Name: "myserver.echo", Args: json.RawMessage(`{"x":1}`)},
				},
			},
		},
	}
	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)
	result := got.History[len(got.History)-1]
	if result.Role != providers.RoleTool || result.Content != "42" || result.ToolCallID != "call_1" {
		t.Errorf("unexpected result message: %+v", result)
	}
}

func TestToolLoop_UnknownMCPServerFallsBackToNoSuchTool(t *testing.T) {
	node := NewToolLoop(nil, map[string]*mcp.Client{})
	in := agent.AgentContext{
		History: []providers.Message{
			{
				Role: providers.RoleAssistant,
				ToolCalls: []providers.ToolCall{
					{ID: "call_1", Name: "ghostserver.echo", Args: json.RawMessage(`{}`)},
				},
			},
		},
	}
	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)
	result := got.History[len(got.History)-1]
	if result.Role != providers.RoleTool || result.Content == "" {
		t.Fatalf("expected a no-such-tool result, got %+v", result)
	}
}

func TestSplitMCPToolName(t *testing.T) {
	cases := []struct {
		in         string
		wantServer string
		wantTool   string
		wantOK     bool
	}{
		{"server.tool", "server", "tool", true},
		{"notool", "", "", false},
		{".tool", "", "", false},
		{"server.", "", "", false},
		{"server.tool.with.dots", "server", "tool.with.dots", true},
	}
	for _, tc := range cases {
		server, tool, ok := splitMCPToolName(tc.in)
		if server != tc.wantServer || tool != tc.wantTool || ok != tc.wantOK {
			t.Errorf("splitMCPToolName(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.in, server, tool, ok, tc.wantServer, tc.wantTool, tc.wantOK)
		}
	}
}
