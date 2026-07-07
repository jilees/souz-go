package mcp

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

// TestStdioTransport_RoundTrip uses `cat` as a stand-in MCP server: it
// echoes stdin to stdout unchanged, which is enough to verify the
// newline-delimited framing and process lifecycle without a real server.
func TestStdioTransport_RoundTrip(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat not available on this system")
	}

	transport := NewStdioTransport("cat")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := transport.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer transport.Close()

	msg := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if err := transport.Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case got := <-transport.Recv():
		if string(got) != string(msg) {
			t.Errorf("got %q, want %q", got, msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for echoed message")
	}
}

func TestStdioTransport_CloseTerminatesProcess(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat not available on this system")
	}

	transport := NewStdioTransport("cat")
	ctx := context.Background()
	if err := transport.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case _, ok := <-transport.Recv():
		if ok {
			t.Error("expected Recv channel to be closed with no further messages")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Recv channel to close")
	}
}

func TestStdioTransport_StartFailsForMissingCommand(t *testing.T) {
	transport := NewStdioTransport("souz-go-definitely-not-a-real-command")
	if err := transport.Start(context.Background()); err == nil {
		t.Fatal("expected an error starting a nonexistent command")
	}
}
