package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// Client is an MCP client speaking JSON-RPC 2.0 over a Transport. Create one
// per server connection; call Start, then Initialize, then ListTools/CallTool
// as needed. Close shuts the transport down.
type Client struct {
	transport Transport

	nextID int64

	mu      sync.Mutex
	pending map[int64]chan rpcMessage
	closed  bool
}

// NewClient wraps a not-yet-started Transport.
func NewClient(t Transport) *Client {
	return &Client{transport: t, pending: make(map[int64]chan rpcMessage)}
}

// Start connects the transport and begins pumping incoming messages.
func (c *Client) Start(ctx context.Context) error {
	if err := c.transport.Start(ctx); err != nil {
		return fmt.Errorf("mcp: start transport: %w", err)
	}
	go c.pump()
	return nil
}

// Close shuts down the underlying transport and fails any calls still
// awaiting a response.
func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	return c.transport.Close()
}

// pump reads incoming messages for the lifetime of the transport, routing
// responses to their waiting caller and dropping anything else (server-
// initiated requests/notifications aren't needed for tool discovery/calls).
func (c *Client) pump() {
	for raw := range c.transport.Recv() {
		var msg rpcMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if !msg.isResponse() {
			continue
		}
		var id int64
		if err := json.Unmarshal(msg.ID, &id); err != nil {
			continue
		}

		c.mu.Lock()
		ch, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.mu.Unlock()

		if ok {
			ch <- msg
			close(ch)
		}
	}

	// Transport closed: unblock anyone still waiting.
	c.mu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	id := atomic.AddInt64(&c.nextID, 1)
	payload, err := newRequest(id, method, params)
	if err != nil {
		return fmt.Errorf("mcp: encode %s request: %w", method, err)
	}

	ch := make(chan rpcMessage, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("mcp: client is closed")
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.transport.Send(ctx, payload); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("mcp: send %s request: %w", method, err)
	}

	select {
	case msg, ok := <-ch:
		if !ok {
			return fmt.Errorf("mcp: %s: connection closed before a response arrived", method)
		}
		if msg.Error != nil {
			return fmt.Errorf("mcp: %s: %w (code %d)", method, msg.Error, msg.Error.Code)
		}
		if result != nil {
			if err := json.Unmarshal(msg.Result, result); err != nil {
				return fmt.Errorf("mcp: %s: decode result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	payload, err := newNotification(method, params)
	if err != nil {
		return fmt.Errorf("mcp: encode %s notification: %w", method, err)
	}
	return c.transport.Send(ctx, payload)
}

// InitializeResult is the server's reply to the initialize handshake.
type InitializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	ServerInfo      ServerInfo      `json:"serverInfo"`
	Capabilities    json.RawMessage `json:"capabilities"`
}

// ServerInfo identifies the connected MCP server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Initialize performs the MCP handshake: sends `initialize`, then the
// `notifications/initialized` follow-up the spec requires before any other
// request. Must be called once, right after Start.
func (c *Client) Initialize(ctx context.Context, clientName, clientVersion string) (*InitializeResult, error) {
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": clientName, "version": clientVersion},
	}
	var result InitializeResult
	if err := c.call(ctx, "initialize", params, &result); err != nil {
		return nil, err
	}
	if err := c.notify(ctx, "notifications/initialized", nil); err != nil {
		return nil, fmt.Errorf("mcp: send initialized notification: %w", err)
	}
	return &result, nil
}

// ToolInfo describes one tool a server advertises via tools/list.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ListTools fetches the server's tool catalog.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	var result struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := c.call(ctx, "tools/list", map[string]any{}, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// CallTool invokes a tool by name with the given JSON arguments object and
// returns its concatenated text content, plus whether the server flagged
// the result as an error (a normal MCP outcome — it's still surfaced as
// tool output to the LLM, not a Go error).
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage) (text string, isError bool, err error) {
	if len(arguments) == 0 {
		arguments = json.RawMessage("{}")
	}
	params := map[string]any{"name": name, "arguments": json.RawMessage(arguments)}

	var result struct {
		Content []contentBlock `json:"content"`
		IsError bool           `json:"isError"`
	}
	if err := c.call(ctx, "tools/call", params, &result); err != nil {
		return "", false, err
	}

	var b strings.Builder
	for _, block := range result.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String(), result.IsError, nil
}
