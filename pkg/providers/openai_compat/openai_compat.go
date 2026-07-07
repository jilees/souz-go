// Package openai_compat implements LLMProvider for OpenAI-compatible chat
// completion APIs (OpenAI, Qwen, AiTunnel, Codex).
package openai_compat

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
	defaultBaseURL = "https://api.openai.com/v1"
	defaultModel   = "gpt-5-nano"
)

var _ providers.LLMProvider = (*Provider)(nil)

// Provider is the OpenAI-compatible LLMProvider.
type Provider struct {
	APIKey     string
	BaseURL    string       // defaults to defaultBaseURL; point at Qwen/AiTunnel/Codex endpoints as needed
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
		return nil, fmt.Errorf("openai_compat: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL()+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("openai_compat: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
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
		return nil, fmt.Errorf("openai_compat: request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai_compat: read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openai_compat: status %d: %s", resp.StatusCode, string(data))
	}

	var parsed chatResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("openai_compat: decode response: %w", err)
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
		return nil, fmt.Errorf("openai_compat: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai_compat: status %d: %s", resp.StatusCode, string(data))
	}

	return readStream(resp.Body, onChunk)
}

// --- request DTOs ---

type chatRequest struct {
	Model               string        `json:"model"`
	Messages            []chatMessage `json:"messages"`
	Stream              bool          `json:"stream"`
	MaxCompletionTokens int           `json:"max_completion_tokens,omitempty"`
	Temperature         *float64      `json:"temperature,omitempty"`
	Tools               []chatTool    `json:"tools,omitempty"`
	ToolChoice          string        `json:"tool_choice,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatToolCallFunc `json:"function"`
}

type chatToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatToolFunc `json:"function"`
}

type chatToolFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func buildRequest(req providers.ChatRequest, stream bool) chatRequest {
	model := req.Model
	if model == "" {
		model = defaultModel
	}
	out := chatRequest{
		Model:    model,
		Messages: buildMessages(req),
		Stream:   stream,
	}
	if req.MaxTokens > 0 {
		out.MaxCompletionTokens = req.MaxTokens
	}
	if req.Temperature != 0 {
		t := req.Temperature
		out.Temperature = &t
	}
	if len(req.Tools) > 0 {
		out.Tools = buildTools(req.Tools)
		out.ToolChoice = "auto"
	}
	return out
}

func buildMessages(req providers.ChatRequest) []chatMessage {
	out := make([]chatMessage, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		out = append(out, chatMessage{Role: "system", Content: req.SystemPrompt})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case providers.RoleSystem:
			out = append(out, chatMessage{Role: "system", Content: m.Content})
		case providers.RoleTool:
			out = append(out, chatMessage{Role: "tool", Content: m.Content, ToolCallID: m.ToolCallID})
		case providers.RoleAssistant:
			out = append(out, buildAssistantMessage(m))
		default:
			out = append(out, chatMessage{Role: "user", Content: m.Content, Name: m.Name})
		}
	}
	return out
}

func buildAssistantMessage(m providers.Message) chatMessage {
	cm := chatMessage{Role: "assistant", Content: m.Content}
	if len(m.ToolCalls) == 0 {
		return cm
	}
	cm.ToolCalls = make([]chatToolCall, len(m.ToolCalls))
	for i, tc := range m.ToolCalls {
		cm.ToolCalls[i] = chatToolCall{
			ID:   tc.ID,
			Type: "function",
			Function: chatToolCallFunc{
				Name:      tc.Name,
				Arguments: string(tc.Args),
			},
		}
	}
	return cm
}

func buildTools(defs []providers.ToolDefinition) []chatTool {
	out := make([]chatTool, len(defs))
	for i, d := range defs {
		out[i] = chatTool{
			Type: "function",
			Function: chatToolFunc{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  d.InputSchema,
			},
		}
	}
	return out
}

// --- non-streaming response ---

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (r chatResponse) toChatResponse() *providers.ChatResponse {
	out := &providers.ChatResponse{
		Usage: providers.Usage{
			InputTokens:  r.Usage.PromptTokens,
			OutputTokens: r.Usage.CompletionTokens,
		},
	}
	if len(r.Choices) == 0 {
		return out
	}
	choice := r.Choices[0]
	out.Content = choice.Message.Content
	out.FinishReason = toFinishReason(choice.FinishReason)
	for _, tc := range choice.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, providers.ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: toRawArgs(tc.Function.Arguments),
		})
	}
	return out
}

func toFinishReason(reason string) providers.FinishReason {
	switch reason {
	case "tool_calls":
		return providers.FinishToolUse
	case "length":
		return providers.FinishLength
	default:
		return providers.FinishStop
	}
}

func toRawArgs(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}

// --- streaming response ---

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   *string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type toolCallAccumulator struct {
	id   string
	name string
	args strings.Builder
}

func readStream(body io.Reader, onChunk func(string)) (*providers.ChatResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var textBuilder strings.Builder
	toolCalls := map[int]*toolCallAccumulator{}
	var toolOrder []int
	finish := providers.FinishStop
	usage := providers.Usage{}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // skip malformed chunk
		}
		if chunk.Usage != nil {
			usage.InputTokens = chunk.Usage.PromptTokens
			usage.OutputTokens = chunk.Usage.CompletionTokens
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				textBuilder.WriteString(*choice.Delta.Content)
				onChunk(*choice.Delta.Content)
			}
			for _, tc := range choice.Delta.ToolCalls {
				acc, ok := toolCalls[tc.Index]
				if !ok {
					acc = &toolCallAccumulator{}
					toolCalls[tc.Index] = acc
					toolOrder = append(toolOrder, tc.Index)
				}
				if tc.ID != "" {
					acc.id = tc.ID
				}
				if tc.Function.Name != "" {
					acc.name = tc.Function.Name
				}
				acc.args.WriteString(tc.Function.Arguments)
			}
			if choice.FinishReason != nil {
				finish = toFinishReason(*choice.FinishReason)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("openai_compat: read stream: %w", err)
	}

	var calls []providers.ToolCall
	for _, idx := range toolOrder {
		acc := toolCalls[idx]
		calls = append(calls, providers.ToolCall{ID: acc.id, Name: acc.name, Args: toRawArgs(acc.args.String())})
	}

	return &providers.ChatResponse{
		Content:      textBuilder.String(),
		ToolCalls:    calls,
		FinishReason: finish,
		Usage:        usage,
	}, nil
}
