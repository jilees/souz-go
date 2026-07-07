package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

const maxLineBytes = 10 << 20 // 10MB; generous enough for large tool payloads

var _ Transport = (*StdioTransport)(nil)

// StdioTransport runs an MCP server as a subprocess, exchanging
// newline-delimited JSON-RPC messages over its stdin/stdout — the transport
// MCP servers distributed as local binaries/scripts use.
type StdioTransport struct {
	Command string
	Args    []string
	// Env holds extra "KEY=VALUE" entries appended to the subprocess
	// environment (which otherwise inherits os.Environ()).
	Env []string
	// Stderr receives the subprocess's stderr; defaults to os.Stderr when nil.
	Stderr io.Writer

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	recvCh chan json.RawMessage

	writeMu sync.Mutex
}

// NewStdioTransport builds a transport that runs command with args.
func NewStdioTransport(command string, args ...string) *StdioTransport {
	return &StdioTransport{Command: command, Args: args}
}

func (t *StdioTransport) Start(ctx context.Context) error {
	t.cmd = exec.CommandContext(ctx, t.Command, t.Args...)
	if len(t.Env) > 0 {
		t.cmd.Env = append(os.Environ(), t.Env...)
	}
	if t.Stderr != nil {
		t.cmd.Stderr = t.Stderr
	} else {
		t.cmd.Stderr = os.Stderr
	}

	stdin, err := t.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("mcp: stdin pipe: %w", err)
	}
	stdout, err := t.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("mcp: stdout pipe: %w", err)
	}
	if err := t.cmd.Start(); err != nil {
		return fmt.Errorf("mcp: start %s: %w", t.Command, err)
	}

	t.stdin = stdin
	t.recvCh = make(chan json.RawMessage, 16)
	go t.readLoop(stdout)
	return nil
}

func (t *StdioTransport) readLoop(stdout io.Reader) {
	defer close(t.recvCh)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		msg := make(json.RawMessage, len(line))
		copy(msg, line)
		t.recvCh <- msg
	}
}

func (t *StdioTransport) Send(_ context.Context, msg json.RawMessage) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	if _, err := t.stdin.Write(msg); err != nil {
		return fmt.Errorf("mcp: write to subprocess: %w", err)
	}
	if _, err := t.stdin.Write([]byte("\n")); err != nil {
		return fmt.Errorf("mcp: write to subprocess: %w", err)
	}
	return nil
}

func (t *StdioTransport) Recv() <-chan json.RawMessage { return t.recvCh }

func (t *StdioTransport) Close() error {
	if t.stdin != nil {
		t.stdin.Close()
	}
	if t.cmd == nil || t.cmd.Process == nil {
		return nil
	}
	// Give the server a chance to exit on its own after stdin closes, but
	// don't let Close block forever on a server that ignores EOF.
	_ = t.cmd.Process.Kill()
	_ = t.cmd.Wait()
	return nil
}
