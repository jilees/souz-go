package http

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/graph"
	"souz.ru/souz-go/pkg/providers"
	"souz.ru/souz-go/pkg/storage"
)

// echoExecutor builds an Executor whose one node echoes the input back as
// the assistant reply, streaming a delta first — enough to exercise the
// HTTP layer without a real LLM provider.
func echoExecutor() *agent.Executor {
	echo := graph.NewNode("echo", func(_ context.Context, in agent.AgentContext) (agent.AgentContext, error) {
		reply := "echo: " + in.Input
		if in.EventSink != nil {
			in.EventSink.EmitDelta(reply)
		}
		return in.WithHistory(providers.Message{Role: providers.RoleAssistant, Content: reply}), nil
	})
	return agent.NewExecutor(graph.NewDefinition(), echo, graph.RetryPolicy{}, 10)
}

// blockingExecutor builds an Executor whose node blocks until ctx is
// cancelled or a fixed timeout elapses — for testing cancel-active.
func blockingExecutor(timeout time.Duration) *agent.Executor {
	blocker := graph.NewNode("blocker", func(ctx context.Context, in agent.AgentContext) (agent.AgentContext, error) {
		select {
		case <-ctx.Done():
			return in, ctx.Err()
		case <-time.After(timeout):
			return in, nil
		}
	})
	return agent.NewExecutor(graph.NewDefinition(), blocker, graph.RetryPolicy{}, 10)
}

func newTestServer(t *testing.T, executor *agent.Executor) (*httptest.Server, *storage.Store) {
	t.Helper()
	store := storage.New(t.TempDir())
	srv := NewServer(executor, store, agent.AgentSettings{Model: "test-model"}, "test system prompt")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, store
}

func doJSON(t *testing.T, method, url string, body any) *http.Response {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = strings.NewReader(string(b))
	} else {
		reader = strings.NewReader("")
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
}

func TestHealth(t *testing.T) {
	ts, _ := newTestServer(t, echoExecutor())
	resp := doJSON(t, http.MethodGet, ts.URL+"/health", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestCreateAndListChats(t *testing.T) {
	ts, _ := newTestServer(t, echoExecutor())

	resp := doJSON(t, http.MethodPost, ts.URL+"/v1/chats", createChatRequest{Title: "My chat"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created struct {
		Chat chatDTO `json:"chat"`
	}
	decodeBody(t, resp, &created)
	if created.Chat.Title != "My chat" || created.Chat.ID == "" {
		t.Fatalf("unexpected created chat: %+v", created.Chat)
	}

	listResp := doJSON(t, http.MethodGet, ts.URL+"/v1/chats", nil)
	var list struct {
		Items []chatDTO `json:"items"`
	}
	decodeBody(t, listResp, &list)
	if len(list.Items) != 1 || list.Items[0].ID != created.Chat.ID {
		t.Fatalf("unexpected chat list: %+v", list.Items)
	}
}

func TestSendMessage_RunsATurnAndPersists(t *testing.T) {
	ts, store := newTestServer(t, echoExecutor())

	createResp := doJSON(t, http.MethodPost, ts.URL+"/v1/chats", nil)
	var created struct {
		Chat chatDTO `json:"chat"`
	}
	decodeBody(t, createResp, &created)
	chatID := created.Chat.ID

	sendResp := doJSON(t, http.MethodPost, ts.URL+"/v1/chats/"+chatID+"/messages", createMessageRequest{Content: "hello"})
	if sendResp.StatusCode != http.StatusOK {
		t.Fatalf("send status = %d", sendResp.StatusCode)
	}
	var got createMessageResponse
	decodeBody(t, sendResp, &got)
	if got.Message.Content != "hello" {
		t.Errorf("unexpected user message: %+v", got.Message)
	}
	if got.AssistantMessage == nil || got.AssistantMessage.Content != "echo: hello" {
		t.Fatalf("unexpected assistant message: %+v", got.AssistantMessage)
	}
	if got.Execution.Status != "completed" {
		t.Errorf("unexpected execution status: %+v", got.Execution)
	}

	cs, err := store.Get(chatID)
	if err != nil || cs == nil {
		t.Fatalf("Get: %v, %+v", err, cs)
	}
	if len(cs.Messages) != 2 || len(cs.History) != 1 {
		t.Errorf("unexpected persisted state: messages=%+v history=%+v", cs.Messages, cs.History)
	}

	messagesResp := doJSON(t, http.MethodGet, ts.URL+"/v1/chats/"+chatID+"/messages", nil)
	var listed struct {
		Items []messageDTO `json:"items"`
	}
	decodeBody(t, messagesResp, &listed)
	if len(listed.Items) != 2 {
		t.Fatalf("expected 2 listed messages, got %d", len(listed.Items))
	}
}

func TestSendMessage_EmptyContentIsRejected(t *testing.T) {
	ts, _ := newTestServer(t, echoExecutor())
	createResp := doJSON(t, http.MethodPost, ts.URL+"/v1/chats", nil)
	var created struct {
		Chat chatDTO `json:"chat"`
	}
	decodeBody(t, createResp, &created)

	resp := doJSON(t, http.MethodPost, ts.URL+"/v1/chats/"+created.Chat.ID+"/messages", createMessageRequest{Content: "   "})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var envelope errorEnvelope
	decodeBody(t, resp, &envelope)
	if envelope.Error.Code != "invalid_request" {
		t.Errorf("unexpected error code: %+v", envelope.Error)
	}
}

func TestSendMessage_ToMissingChatIsLazilyCreated(t *testing.T) {
	// souz-go has no separate "chat must exist" requirement for sending —
	// unlike the Kotlin original, a fresh chatId is accepted on first send.
	ts, _ := newTestServer(t, echoExecutor())
	resp := doJSON(t, http.MethodPost, ts.URL+"/v1/chats/brand-new-chat/messages", createMessageRequest{Content: "hi"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestListMessages_UnknownChatIs404(t *testing.T) {
	ts, _ := newTestServer(t, echoExecutor())
	resp := doJSON(t, http.MethodGet, ts.URL+"/v1/chats/does-not-exist/messages", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var envelope errorEnvelope
	decodeBody(t, resp, &envelope)
	if envelope.Error.Code != "chat_not_found" {
		t.Errorf("unexpected error code: %+v", envelope.Error)
	}
}

func TestSendMessage_ConcurrentSendIsRejected(t *testing.T) {
	ts, _ := newTestServer(t, blockingExecutor(2*time.Second))
	chatID := "busy-chat"

	done := make(chan *http.Response, 1)
	go func() {
		done <- doJSON(t, http.MethodPost, ts.URL+"/v1/chats/"+chatID+"/messages", createMessageRequest{Content: "first"})
	}()
	time.Sleep(100 * time.Millisecond) // let the first request register as active

	second := doJSON(t, http.MethodPost, ts.URL+"/v1/chats/"+chatID+"/messages", createMessageRequest{Content: "second"})
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for a concurrent send, got %d", second.StatusCode)
	}

	<-done // let the background request finish so it doesn't leak past the test
}

func TestCancelActive(t *testing.T) {
	ts, _ := newTestServer(t, blockingExecutor(5*time.Second))
	chatID := "cancel-me"

	done := make(chan *http.Response, 1)
	go func() {
		done <- doJSON(t, http.MethodPost, ts.URL+"/v1/chats/"+chatID+"/messages", createMessageRequest{Content: "hi"})
	}()
	time.Sleep(100 * time.Millisecond)

	cancelResp := doJSON(t, http.MethodPost, ts.URL+"/v1/chats/"+chatID+"/cancel-active", nil)
	if cancelResp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d", cancelResp.StatusCode)
	}

	select {
	case resp := <-done:
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("expected the cancelled send to report 409, got %d", resp.StatusCode)
		}
		var envelope errorEnvelope
		decodeBody(t, resp, &envelope)
		if envelope.Error.Code != "agent_execution_cancelled" {
			t.Errorf("unexpected error code: %+v", envelope.Error)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the cancelled send to return")
	}
}

func TestCancelActive_NoActiveExecutionIs404(t *testing.T) {
	ts, _ := newTestServer(t, echoExecutor())
	resp := doJSON(t, http.MethodPost, ts.URL+"/v1/chats/idle-chat/cancel-active", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestEvents_StreamsDeltaAndExecutionEvents(t *testing.T) {
	ts, _ := newTestServer(t, echoExecutor())

	// The chat must exist before the events endpoint will accept a subscriber.
	createResp := doJSON(t, http.MethodPost, ts.URL+"/v1/chats", createChatRequest{})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("unexpected create status %d", createResp.StatusCode)
	}
	var created struct {
		Chat chatDTO `json:"chat"`
	}
	decodeBody(t, createResp, &created)
	chatID := created.Chat.ID

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/chats/"+chatID+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	sseResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", sseResp.StatusCode)
	}

	eventTypes := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(sseResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				eventTypes <- strings.TrimPrefix(line, "event: ")
			}
		}
	}()

	time.Sleep(50 * time.Millisecond) // let the subscription register
	sendResp := doJSON(t, http.MethodPost, ts.URL+"/v1/chats/"+chatID+"/messages", createMessageRequest{Content: "hi"})
	if sendResp.StatusCode != http.StatusOK {
		t.Fatalf("send status = %d", sendResp.StatusCode)
	}

	want := map[string]bool{"execution.started": false, "message.delta": false, "execution.finished": false}
	deadline := time.After(3 * time.Second)
	for {
		allSeen := true
		for _, seen := range want {
			if !seen {
				allSeen = false
			}
		}
		if allSeen {
			break
		}
		select {
		case et := <-eventTypes:
			if _, ok := want[et]; ok {
				want[et] = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for events, got so far: %+v", want)
		}
	}
}
