package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"souz.ru/souz-go/pkg/providers"
)

const maxSSELineBytes = 10 << 20

// codexSSEEnvelope covers every Responses API stream event this client
// acts on. Fields not relevant to a given event.Type are simply left zero.
type codexSSEEnvelope struct {
	Type     string          `json:"type"`
	Item     json.RawMessage `json:"item"`
	Response *struct {
		Usage *codexUsage     `json:"usage"`
		Error json.RawMessage `json:"error"`
	} `json:"response"`
	Error json.RawMessage `json:"error"`
}

type codexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type codexOutputItem struct {
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content"` // for type=message: a string or an array of {type,text}
	Text      string          `json:"text"`
	Name      string          `json:"name"`      // for type=function_call
	Arguments string          `json:"arguments"` // for type=function_call
	CallID    string          `json:"call_id"`
	ID        string          `json:"id"`
}

type codexContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// readCodexStream consumes a Codex Responses API SSE stream, collecting
// output items as they arrive and building the final ChatResponse once
// "response.completed" fires (or, if the stream ends without one but items
// did arrive, from whatever was collected — the same tolerance the
// original has for a stream that never sends a completion event).
//
// This provider does not surface token-level text deltas via onChunk: the
// Responses API events this client acts on (output_item.done,
// response.completed) only report whole items becoming available, not
// incremental text — matching the original CodexChatAPI, which has the
// same limitation despite being nominally "streaming."
func readCodexStream(body io.Reader) (*providers.ChatResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSELineBytes)

	var pendingEvent string
	var items []json.RawMessage
	var usage *codexUsage
	var streamErr error

	var dataLines []string
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if data == "" || data == "[DONE]" {
			pendingEvent = ""
			return
		}

		var evt codexSSEEnvelope
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			pendingEvent = ""
			return
		}
		eventType := pendingEvent
		if eventType == "" {
			eventType = evt.Type
		}

		switch eventType {
		case "response.output_item.done":
			if len(evt.Item) > 0 {
				items = append(items, evt.Item)
			} else {
				items = append(items, json.RawMessage(data))
			}
		case "response.completed":
			if evt.Response != nil {
				usage = evt.Response.Usage
			}
		case "response.failed", "response.incomplete":
			streamErr = fmt.Errorf("codex: stream error (%s): %s", eventType, codexErrorText(evt))
		}
		pendingEvent = ""
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "event:"):
			pendingEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("codex: read stream: %w", err)
	}
	if streamErr != nil {
		return nil, streamErr
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("codex: stream ended with no output")
	}

	return buildChatResponse(items, usage), nil
}

func codexErrorText(evt codexSSEEnvelope) string {
	raw := evt.Error
	if evt.Response != nil && len(evt.Response.Error) > 0 {
		raw = evt.Response.Error
	}
	if len(raw) == 0 {
		return "unknown error"
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	return string(raw)
}

func buildChatResponse(rawItems []json.RawMessage, usage *codexUsage) *providers.ChatResponse {
	var textParts []string
	var toolCalls []providers.ToolCall

	for _, raw := range rawItems {
		var item codexOutputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		switch item.Type {
		case "message":
			textParts = append(textParts, extractMessageText(item))
		case "function_call":
			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}
			args := item.Arguments
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, providers.ToolCall{ID: callID, Name: item.Name, Args: json.RawMessage(args)})
		}
	}

	resp := &providers.ChatResponse{
		Content:   strings.Join(textParts, ""),
		ToolCalls: toolCalls,
	}
	if len(toolCalls) > 0 {
		resp.FinishReason = providers.FinishToolUse
	} else {
		resp.FinishReason = providers.FinishStop
	}
	if usage != nil {
		resp.Usage = providers.Usage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens}
	}
	return resp
}

func extractMessageText(item codexOutputItem) string {
	if len(item.Content) == 0 {
		return item.Text
	}

	var asString string
	if json.Unmarshal(item.Content, &asString) == nil {
		return asString
	}

	var parts []codexContentPart
	if json.Unmarshal(item.Content, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			// Responses API uses "output_text"; Chat Completions-style
			// payloads (if ever mixed in) use "text" — accept both.
			if p.Type == "text" || p.Type == "output_text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}
