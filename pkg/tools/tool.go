package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/providers"
)

// Tool is the interface all agent tools must implement.
// The Execute method receives arguments pre-parsed into a map of
// json.RawMessage values so each tool can unmarshal only what it needs.
type Tool interface {
	// Name is the identifier used in LLM tool calls. Must be unique within a session.
	Name() string
	// Description is shown to the LLM to help it decide when to call this tool.
	Description() string
	// Schema returns the JSON Schema for the tool's input object.
	// The schema is embedded verbatim into the LLM request.
	Schema() json.RawMessage
	// Execute runs the tool. Returns a plain-text result or an error string.
	Execute(ctx context.Context, args map[string]json.RawMessage, meta agent.InvocationMeta) (string, error)
}

// ToDefinition converts a Tool into a providers.ToolDefinition for LLM requests.
func ToDefinition(t Tool) providers.ToolDefinition {
	return providers.ToolDefinition{
		Name:        t.Name(),
		Description: t.Description(),
		InputSchema: t.Schema(),
	}
}

// ToDefinitions converts a tool registry into a slice of providers.ToolDefinition,
// sorted by name for deterministic LLM request payloads.
func ToDefinitions(registry map[string]Tool) []providers.ToolDefinition {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)

	defs := make([]providers.ToolDefinition, len(names))
	for i, name := range names {
		defs[i] = ToDefinition(registry[name])
	}
	return defs
}

// NewRegistry builds a name-keyed registry from a list of tools, as returned
// by each tool package's constructor (files.New, web.New, ...). Later tools
// win on a name collision.
func NewRegistry(lists ...[]Tool) map[string]Tool {
	registry := make(map[string]Tool)
	for _, list := range lists {
		for _, t := range list {
			registry[t.Name()] = t
		}
	}
	return registry
}

// ArgString reads a string argument, returning def if the key is absent.
func ArgString(args map[string]json.RawMessage, key, def string) (string, error) {
	raw, ok := args[key]
	if !ok {
		return def, nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("invalid %q argument: %w", key, err)
	}
	return v, nil
}

// ArgInt reads an integer argument, returning def if the key is absent.
func ArgInt(args map[string]json.RawMessage, key string, def int) (int, error) {
	raw, ok := args[key]
	if !ok {
		return def, nil
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, fmt.Errorf("invalid %q argument: %w", key, err)
	}
	return v, nil
}

// ArgBool reads a boolean argument, returning def if the key is absent.
func ArgBool(args map[string]json.RawMessage, key string, def bool) (bool, error) {
	raw, ok := args[key]
	if !ok {
		return def, nil
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, fmt.Errorf("invalid %q argument: %w", key, err)
	}
	return v, nil
}
