package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"souz.ru/souz-go/pkg/agent"
)

// sseEvent is one event queued for a chat's SSE subscribers.
type sseEvent struct {
	eventType string
	payload   any
}

// chatBroadcaster fans a chat's events out to every currently-connected SSE
// subscriber. A slow or absent subscriber never blocks the turn producing
// events — publish drops on a full subscriber channel rather than waiting.
type chatBroadcaster struct {
	mu   sync.Mutex
	subs map[chan sseEvent]struct{}
}

func newChatBroadcaster() *chatBroadcaster {
	return &chatBroadcaster{subs: make(map[chan sseEvent]struct{})}
}

const subscriberBufferSize = 64

func (b *chatBroadcaster) subscribe() (<-chan sseEvent, func()) {
	ch := make(chan sseEvent, subscriberBufferSize)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	unsubscribe := func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
	return ch, unsubscribe
}

func (b *chatBroadcaster) publish(evt sseEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- evt:
		default:
			// Subscriber isn't keeping up; drop the event rather than
			// block the agent turn that's producing it.
		}
	}
}

// broadcasterFor returns (creating if needed) the broadcaster for a chat.
// Broadcasters are never removed — a chat only ever accumulates one
// long-lived, cheap (a mutex + a map) broadcaster for the life of the
// process, which is fine at embedded-device chat volumes.
func (s *Server) broadcasterFor(chatID string) *chatBroadcaster {
	s.broadcastersMu.Lock()
	defer s.broadcastersMu.Unlock()
	b, ok := s.broadcasters[chatID]
	if !ok {
		b = newChatBroadcaster()
		s.broadcasters[chatID] = b
	}
	return b
}

// sseEventSink implements agent.EventSink by publishing to a chat's
// broadcaster, translating each callback into the message.*/tool.call.*/
// execution.* event vocabulary documented in CLAUDE.md.
type sseEventSink struct {
	broadcaster *chatBroadcaster
}

var _ agent.EventSink = (*sseEventSink)(nil)

func (s *sseEventSink) EmitDelta(delta string) {
	s.broadcaster.publish(sseEvent{eventType: "message.delta", payload: map[string]string{"delta": delta}})
}

func (s *sseEventSink) EmitToolCall(name, argsJSON string) {
	s.broadcaster.publish(sseEvent{
		eventType: "tool.call.started",
		payload:   map[string]string{"name": name, "argumentsPreview": argsJSON},
	})
}

func (s *sseEventSink) EmitToolResult(name, result string, isError bool) {
	eventType := "tool.call.finished"
	if isError {
		eventType = "tool.call.failed"
	}
	s.broadcaster.publish(sseEvent{
		eventType: eventType,
		payload:   map[string]any{"name": name, "resultPreview": result, "isError": isError},
	})
}

func (s *sseEventSink) EmitError(code, message string) {
	s.broadcaster.publish(sseEvent{
		eventType: "execution.failed",
		payload:   map[string]string{"errorCode": code, "errorMessage": message},
	})
}

func (s *sseEventSink) Done() {
	s.broadcaster.publish(sseEvent{eventType: "execution.finished", payload: map[string]string{}})
}

// --- GET /v1/chats/{chatId}/events ---

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	cs, err := s.Store.Get(chatID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if cs == nil {
		writeError(w, http.StatusNotFound, "chat_not_found", fmt.Sprintf("chat %q not found", chatID))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "streaming not supported")
		return
	}

	sub, unsubscribe := s.broadcasterFor(chatID).subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case evt, ok := <-sub:
			if !ok {
				return
			}
			data, err := json.Marshal(evt.payload)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.eventType, data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
