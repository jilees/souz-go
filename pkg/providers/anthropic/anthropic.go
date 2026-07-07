// Package anthropic implements LLMProvider for the Anthropic Messages API.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"souz.ru/souz-go/pkg/providers"
)

const (
	defaultBaseURL    = "https://api.anthropic.com"
	defaultAPIVersion = "2023-06-01"
	defaultModel      = "claude-haiku-4-5-20251001"
	defaultMaxTokens  = 4096
)

var _ providers.LLMProvider = (*Provider)(nil)

// Provider is the Anthropic LLMProvider implementation.
type Provider struct {
	APIKey     string
	BaseURL    string       // defaults to defaultBaseURL
	HTTPClient *http.Client // defaults to http.DefaultClient
}

func (p *Provider) baseURL() string {
	if p.BaseURL != "" {
		return p.BaseURL
	}
	return defaultBaseURL
}

func (p *Provider) httpClient() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return http.DefaultClient
}

func (p *Provider) newRequest(ctx context.Context, body any) (*http.Request, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL()+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("anthropic-version", defaultAPIVersion)
	return req, nil
}

// Chat performs a non-streaming chat completion.
func (p *Provider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	httpReq, err := p.newRequest(ctx, buildRequest(req, false))
	if err != nil {
		return nil, err
	}

	resp, err := p.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, string(data))
	}

	var parsed messagesResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}
	return parsed.toChatResponse(), nil
}

// ChatStream performs a streaming chat completion, invoking onChunk for each text delta.
func (p *Provider) ChatStream(ctx context.Context, req providers.ChatRequest, onChunk func(delta string)) (*providers.ChatResponse, error) {
	httpReq, err := p.newRequest(ctx, buildRequest(req, true))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, string(data))
	}

	return readStream(resp.Body, onChunk)
}

// --- request DTOs ---

type messagesRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	Stream      bool               `json:"stream"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	ToolChoice  map[string]string  `json:"tool_choice,omitempty"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

// anthropicContentBlock covers text, tool_use and tool_result blocks;
// only the fields relevant to each block's "type" are populated.
type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`    // tool_use id
	Name      string          `json:"name,omitempty"`  // tool_use name
	Input     json.RawMessage `json:"input,omitempty"` // tool_use args
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"` // tool_result content
	IsError   bool            `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func buildRequest(req providers.ChatRequest, stream bool) messagesRequest {
	model := req.Model
	if model == "" {
		model = defaultModel
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	out := messagesRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  buildMessages(req.Messages),
		System:    req.SystemPrompt,
		Stream:    stream,
	}
	if req.Temperature != 0 {
		t := req.Temperature
		out.Temperature = &t
	}
	if len(req.Tools) > 0 {
		out.Tools = buildTools(req.Tools)
		out.ToolChoice = map[string]string{"type": "auto"}
	}
	return out
}

func buildMessages(msgs []providers.Message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case providers.RoleSystem:
			continue // carried via top-level "system" instead
		case providers.RoleAssistant:
			out = append(out, anthropicMessage{Role: "assistant", Content: buildAssistantBlocks(m)})
		default: // user, tool
			out = append(out, anthropicMessage{Role: "user", Content: buildContentBlocks(m)})
		}
	}
	return out
}

func buildAssistantBlocks(m providers.Message) []anthropicContentBlock {
	var blocks []anthropicContentBlock
	if m.Content != "" {
		blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
	}
	for _, tc := range m.ToolCalls {
		blocks = append(blocks, anthropicContentBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: tc.Args})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, anthropicContentBlock{Type: "text", Text: ""})
	}
	return blocks
}

func buildContentBlocks(m providers.Message) []anthropicContentBlock {
	if len(m.Parts) > 0 {
		blocks := make([]anthropicContentBlock, 0, len(m.Parts))
		for _, part := range m.Parts {
			if part.Type == "tool_result" {
				blocks = append(blocks, anthropicContentBlock{
					Type: "tool_result", ToolUseID: part.ToolCallID, Content: part.Content, IsError: part.IsError,
				})
			} else {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: part.Text})
			}
		}
		return blocks
	}
	if m.Role == providers.RoleTool {
		return []anthropicContentBlock{{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content}}
	}
	return []anthropicContentBlock{{Type: "text", Text: m.Content}}
}

func buildTools(defs []providers.ToolDefinition) []anthropicTool {
	out := make([]anthropicTool, len(defs))
	for i, d := range defs {
		out[i] = anthropicTool{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema}
	}
	return out
}

// --- non-streaming response ---

type messagesResponse struct {
	StopReason string                  `json:"stop_reason"`
	Content    []anthropicContentBlock `json:"content"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (r messagesResponse) toChatResponse() *providers.ChatResponse {
	var text strings.Builder
	var toolCalls []providers.ToolCall
	for _, block := range r.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			toolCalls = append(toolCalls, providers.ToolCall{ID: block.ID, Name: block.Name, Args: block.Input})
		}
	}
	return &providers.ChatResponse{
		Content:      text.String(),
		ToolCalls:    toolCalls,
		FinishReason: toFinishReason(r.StopReason),
		Usage: providers.Usage{
			InputTokens:  r.Usage.InputTokens,
			OutputTokens: r.Usage.OutputTokens,
		},
	}
}

func toFinishReason(stopReason string) providers.FinishReason {
	switch stopReason {
	case "tool_use":
		return providers.FinishToolUse
	case "max_tokens":
		return providers.FinishLength
	default:
		return providers.FinishStop
	}
}

// --- streaming response ---

type sseEnvelope struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	ContentBlock *anthropicContentBlock `json:"content_block"`
	Delta        *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type toolBlockState struct {
	id   string
	name string
	args strings.Builder
}

func readStream(body io.Reader, onChunk func(string)) (*providers.ChatResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var textBuilder strings.Builder
	toolBlocks := map[int]*toolBlockState{}
	var toolCalls []providers.ToolCall
	finish := providers.FinishStop
	usage := providers.Usage{}

	var dataLines []string
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if data == "" || data == "[DONE]" {
			return
		}
		var evt sseEnvelope
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			return // skip malformed chunk
		}
		switch evt.Type {
		case "message_start":
			if evt.Message != nil {
				usage.InputTokens = evt.Message.Usage.InputTokens
			}
		case "content_block_start":
			if evt.ContentBlock != nil && evt.ContentBlock.Type == "tool_use" {
				toolBlocks[evt.Index] = &toolBlockState{id: evt.ContentBlock.ID, name: evt.ContentBlock.Name}
			}
		case "content_block_delta":
			if evt.Delta == nil {
				return
			}
			switch evt.Delta.Type {
			case "text_delta":
				if evt.Delta.Text != "" {
					textBuilder.WriteString(evt.Delta.Text)
					onChunk(evt.Delta.Text)
				}
			case "input_json_delta":
				if tb, ok := toolBlocks[evt.Index]; ok {
					tb.args.WriteString(evt.Delta.PartialJSON)
				}
			}
		case "content_block_stop":
			if tb, ok := toolBlocks[evt.Index]; ok {
				delete(toolBlocks, evt.Index)
				args := tb.args.String()
				if args == "" {
					args = "{}"
				}
				toolCalls = append(toolCalls, providers.ToolCall{ID: tb.id, Name: tb.name, Args: json.RawMessage(args)})
			}
		case "message_delta":
			if evt.Delta != nil && evt.Delta.StopReason != "" {
				finish = toFinishReason(evt.Delta.StopReason)
			}
			if evt.Usage != nil {
				usage.OutputTokens = evt.Usage.OutputTokens
			}
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// ignore "event:" and other SSE fields
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("anthropic: read stream: %w", err)
	}

	if len(toolCalls) > 0 {
		finish = providers.FinishToolUse
	}

	return &providers.ChatResponse{
		Content:      textBuilder.String(),
		ToolCalls:    toolCalls,
		FinishReason: finish,
		Usage:        usage,
	}, nil
}
