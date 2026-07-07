package codex

import (
	"strings"
	"testing"

	"souz.ru/souz-go/pkg/providers"
)

func TestReadCodexStream_TextMessage(t *testing.T) {
	sse := "" +
		"event: response.output_item.done\n" +
		`data: {"item":{"type":"message","content":[{"type":"output_text","text":"Hello there"}]}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"response":{"usage":{"input_tokens":10,"output_tokens":5}}}` + "\n\n"

	resp, err := readCodexStream(strings.NewReader(sse))
	if err != nil {
		t.Fatalf("readCodexStream: %v", err)
	}
	if resp.Content != "Hello there" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.FinishReason != providers.FinishStop {
		t.Errorf("FinishReason = %q", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Errorf("unexpected usage: %+v", resp.Usage)
	}
}

func TestReadCodexStream_FunctionCall(t *testing.T) {
	sse := "" +
		"event: response.output_item.done\n" +
		`data: {"item":{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Tallinn\"}"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"response":{"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"

	resp, err := readCodexStream(strings.NewReader(sse))
	if err != nil {
		t.Fatalf("readCodexStream: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].Name != "get_weather" {
		t.Fatalf("unexpected tool calls: %+v", resp.ToolCalls)
	}
	if resp.FinishReason != providers.FinishToolUse {
		t.Errorf("FinishReason = %q", resp.FinishReason)
	}
}

func TestReadCodexStream_PlainStringContent(t *testing.T) {
	sse := "" +
		"event: response.output_item.done\n" +
		`data: {"item":{"type":"message","content":"just a string"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {}` + "\n\n"

	resp, err := readCodexStream(strings.NewReader(sse))
	if err != nil {
		t.Fatalf("readCodexStream: %v", err)
	}
	if resp.Content != "just a string" {
		t.Errorf("Content = %q", resp.Content)
	}
}

func TestReadCodexStream_FallbackWithoutCompletedEvent(t *testing.T) {
	sse := "" +
		"event: response.output_item.done\n" +
		`data: {"item":{"type":"message","content":[{"type":"output_text","text":"partial"}]}}` + "\n\n"

	resp, err := readCodexStream(strings.NewReader(sse))
	if err != nil {
		t.Fatalf("readCodexStream: %v", err)
	}
	if resp.Content != "partial" {
		t.Errorf("Content = %q", resp.Content)
	}
}

func TestReadCodexStream_ResponseFailedIsAnError(t *testing.T) {
	sse := "event: response.failed\n" + `data: {"response":{"error":"quota exceeded"}}` + "\n\n"
	if _, err := readCodexStream(strings.NewReader(sse)); err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("expected an error containing %q, got %v", "quota exceeded", err)
	}
}

func TestReadCodexStream_NoOutputIsAnError(t *testing.T) {
	sse := "data: [DONE]\n\n"
	if _, err := readCodexStream(strings.NewReader(sse)); err == nil {
		t.Fatal("expected an error for a stream with no output")
	}
}

func TestReadCodexStream_EventTypeFallsBackToJSONField(t *testing.T) {
	// No "event:" line; the type comes from the JSON body's own "type" field.
	sse := `data: {"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"no event line"}]}}` + "\n\n" +
		`data: {"type":"response.completed","response":{}}` + "\n\n"

	resp, err := readCodexStream(strings.NewReader(sse))
	if err != nil {
		t.Fatalf("readCodexStream: %v", err)
	}
	if resp.Content != "no event line" {
		t.Errorf("Content = %q", resp.Content)
	}
}
