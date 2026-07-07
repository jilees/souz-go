package mcp

import (
	"context"
	"encoding/json"
)

// Transport is the minimal duplex message channel MCP's JSON-RPC runs over.
// Implementations: StdioTransport (subprocess stdin/stdout) and
// HTTPSSETransport (POST requests + a Server-Sent Events response stream).
type Transport interface {
	// Start connects the transport (spawns the subprocess / opens the SSE
	// stream). Recv only yields messages after Start returns successfully.
	Start(ctx context.Context) error
	// Send writes one JSON-RPC message (request or notification).
	Send(ctx context.Context, msg json.RawMessage) error
	// Recv returns the channel of incoming JSON-RPC messages. It is closed
	// when the transport's read side ends (process exit, connection drop).
	Recv() <-chan json.RawMessage
	// Close releases the transport's resources. Safe to call more than once.
	Close() error
}
