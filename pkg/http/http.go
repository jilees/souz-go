// Package http implements a scoped-down /v1/** HTTP API plus an SSE event
// sink, inspired by the KMP backend's BackendHttpRoutes.kt/BackendV1Dtos.kt
// contract (route naming, camelCase JSON fields, the error envelope shape,
// and the message.*/execution.*/tool.call.* event vocabulary).
//
// It is deliberately not a route-for-route port. The Kotlin backend is a
// multi-tenant server with Postgres/memory storage modes, an onboarding
// wizard, HTTP-managed settings and provider keys, Telegram-bot-binding
// routes, and a choice/option subsystem — none of which fit a single-user
// 256MB embedded device, and (confirmed via the Kotlin source) the existing
// KMP Compose desktop client doesn't call this API at all today; it runs
// the agent in-process. So there is no live consumer to stay
// byte-compatible with. This package keeps the recognizable shape
// (/v1/chats, camelCase DTOs, the event type vocabulary) for a future thin
// client, scoped to what a single-user embedded agent actually needs:
// create/list chats, send a message and run one turn, list its messages,
// watch it live over SSE, and cancel it.
//
// Transport for live events is real Server-Sent Events (text/event-stream),
// not the Kotlin backend's WebSocket — CLAUDE.md and docs/plan.md already
// call this an "SSE event sink," and SSE needs nothing beyond net/http
// (no WebSocket library, no extra dependency), which fits the project's
// zero-CGO/small-binary constraints better than a WS implementation would.
package http

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/storage"
)

// Server is the /v1/** HTTP API. One Server instance is safe for concurrent
// use across many chats; each chat's turns are still serialized (see
// tryBeginExecution) since AgentContext.History for a chat is only safe to
// advance one turn at a time.
type Server struct {
	Executor     *agent.Executor
	Store        *storage.Store
	Settings     agent.AgentSettings
	SystemPrompt string

	mux        *http.ServeMux
	httpServer *http.Server

	broadcastersMu sync.Mutex
	broadcasters   map[string]*chatBroadcaster

	activeMu sync.Mutex
	active   map[string]context.CancelFunc
}

// NewServer builds a Server. executor drives one agent turn per call;
// settings/systemPrompt are the defaults applied to every turn (souz-go has
// no per-chat settings override, unlike the Kotlin original's per-request
// BackendV1MessageOptionsRequest — a single embedded agent has one model
// configuration, set in config.yaml).
func NewServer(executor *agent.Executor, store *storage.Store, settings agent.AgentSettings, systemPrompt string) *Server {
	s := &Server{
		Executor:     executor,
		Store:        store,
		Settings:     settings,
		SystemPrompt: systemPrompt,
		broadcasters: make(map[string]*chatBroadcaster),
		active:       make(map[string]context.CancelFunc),
	}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /v1/chats", s.handleListChats)
	s.mux.HandleFunc("POST /v1/chats", s.handleCreateChat)
	s.mux.HandleFunc("GET /v1/chats/{chatId}/messages", s.handleListMessages)
	s.mux.HandleFunc("POST /v1/chats/{chatId}/messages", s.handleSendMessage)
	s.mux.HandleFunc("GET /v1/chats/{chatId}/events", s.handleEvents)
	s.mux.HandleFunc("POST /v1/chats/{chatId}/cancel-active", s.handleCancelActive)
	return s
}

// Handler returns the routed http.Handler, e.g. for httptest.NewServer.
func (s *Server) Handler() http.Handler { return s.mux }

// ListenAndServe blocks serving addr until Shutdown is called.
func (s *Server) ListenAndServe(addr string) error {
	s.httpServer = &http.Server{Addr: addr, Handler: s.mux}
	err := s.httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully stops the server started by ListenAndServe.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

// --- error envelope, matching BackendV1ErrorEnvelope's {"error":{"code","message"}} shape ---

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message}})
}

// decodeJSON decodes a possibly-empty JSON body; an empty body is not an
// error (callers get zero-value fields), but malformed JSON is.
func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

// --- DTOs ---

type chatDTO struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	Archived  bool   `json:"archived"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func toChatDTO(cs *storage.ChatState) chatDTO {
	return chatDTO{
		ID:        cs.ChatID,
		Title:     cs.Title,
		Archived:  cs.Archived,
		CreatedAt: cs.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: cs.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type messageDTO struct {
	ID        string `json:"id"`
	ChatID    string `json:"chatId"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
}

func toMessageDTO(chatID string, m storage.Message) messageDTO {
	return messageDTO{
		ID:        m.ID,
		ChatID:    chatID,
		Role:      m.Role,
		Content:   m.Content,
		CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// --- GET /health ---

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- GET /v1/chats, POST /v1/chats ---

func (s *Server) handleListChats(w http.ResponseWriter, _ *http.Request) {
	list, err := s.Store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	items := make([]chatDTO, len(list))
	for i, cs := range list {
		items[i] = toChatDTO(cs)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type createChatRequest struct {
	Title string `json:"title"`
}

func (s *Server) handleCreateChat(w http.ResponseWriter, r *http.Request) {
	var req createChatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	cs := &storage.ChatState{ChatID: newID(), Title: req.Title}
	if err := s.Store.Save(cs); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"chat": toChatDTO(cs)})
}

// --- GET /v1/chats/{chatId}/messages ---

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
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

	items := make([]messageDTO, len(cs.Messages))
	for i, m := range cs.Messages {
		items[i] = toMessageDTO(chatID, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// --- POST /v1/chats/{chatId}/messages ---

type createMessageRequest struct {
	Content string `json:"content"`
}

type createMessageResponse struct {
	Message          messageDTO   `json:"message"`
	AssistantMessage *messageDTO  `json:"assistantMessage"`
	Execution        executionDTO `json:"execution"`
}

type executionDTO struct {
	Status       string `json:"status"` // "completed" | "failed" | "cancelled"
	Model        string `json:"model,omitempty"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")

	var req createMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "content must not be empty")
		return
	}

	cs, err := s.Store.Get(chatID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if cs == nil {
		cs = &storage.ChatState{ChatID: chatID}
	}

	ctx, cancel := context.WithCancel(r.Context())
	if !s.tryBeginExecution(chatID, cancel) {
		cancel()
		writeError(w, http.StatusConflict, "chat_already_has_active_execution", "this chat already has a turn in progress")
		return
	}
	defer s.endExecution(chatID)
	defer cancel()

	userMsg := storage.Message{ID: newID(), Role: "user", Content: content, CreatedAt: time.Now().UTC()}

	broadcaster := s.broadcasterFor(chatID)
	sink := &sseEventSink{broadcaster: broadcaster}
	broadcaster.publish(sseEvent{eventType: "execution.started", payload: map[string]string{"model": s.Settings.Model}})

	seed := agent.AgentContext{
		Input:        content,
		History:      cs.History,
		SystemPrompt: s.SystemPrompt,
		Settings:     s.Settings,
		InvocationMeta: agent.InvocationMeta{
			UserID:         "default",
			ConversationID: chatID,
			RequestID:      newID(),
		},
		EventSink: sink,
	}

	result, execErr := s.Executor.Execute(ctx, seed)

	cs.Messages = append(cs.Messages, userMsg)

	if execErr != nil {
		_ = s.Store.Save(cs) // best-effort: keep the user's message even though the turn failed
		status, code := http.StatusInternalServerError, "agent_execution_failed"
		if errors.Is(execErr, context.Canceled) {
			status, code = http.StatusConflict, "agent_execution_cancelled"
		}
		sink.EmitError(code, execErr.Error())
		writeError(w, status, code, execErr.Error())
		return
	}

	assistantMsg := storage.Message{ID: newID(), Role: "assistant", Content: result.Output, CreatedAt: time.Now().UTC()}
	cs.Messages = append(cs.Messages, assistantMsg)
	cs.History = result.Context.History

	if err := s.Store.Save(cs); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	sink.Done()

	assistantDTO := toMessageDTO(chatID, assistantMsg)
	writeJSON(w, http.StatusOK, createMessageResponse{
		Message:          toMessageDTO(chatID, userMsg),
		AssistantMessage: &assistantDTO,
		Execution:        executionDTO{Status: "completed", Model: s.Settings.Model},
	})
}

// --- POST /v1/chats/{chatId}/cancel-active ---

func (s *Server) handleCancelActive(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	if !s.cancelActive(chatID) {
		writeError(w, http.StatusNotFound, "execution_not_found", "no active execution for this chat")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
}

// --- per-chat single-flight execution tracking ---

func (s *Server) tryBeginExecution(chatID string, cancel context.CancelFunc) bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if _, running := s.active[chatID]; running {
		return false
	}
	s.active[chatID] = cancel
	return true
}

func (s *Server) endExecution(chatID string) {
	s.activeMu.Lock()
	delete(s.active, chatID)
	s.activeMu.Unlock()
}

func (s *Server) cancelActive(chatID string) bool {
	s.activeMu.Lock()
	cancel, ok := s.active[chatID]
	s.activeMu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
