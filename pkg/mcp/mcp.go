// Package mcp implements a Model Context Protocol client: JSON-RPC 2.0
// request/response correlation over a pluggable Transport, with stdio and
// HTTP+SSE transport implementations.
//
// This package only talks to MCP servers (initialize, list tools, call
// tools); wiring discovered tools into the agent graph is Phase 4's
// pkg/agent/nodes/mcp.go, to keep this package agent-agnostic and
// independently testable.
package mcp

const protocolVersion = "2024-11-05"
