// Package telegram implements the Telegram long-polling channel using the
// Bot API directly over net/http (no third-party SDK, keeps the binary
// small for the embedded target).
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"souz.ru/souz-go/pkg/bus"
	"souz.ru/souz-go/pkg/channels"
)

const (
	defaultBaseURL         = "https://api.telegram.org"
	longPollTimeoutSeconds = 30
	httpClientTimeout      = 40 * time.Second
	pollErrorBackoff       = 2 * time.Second

	// maxTypingDuration caps how long the indicator can run if stop is
	// never called (e.g. the LLM call hangs), so a stuck turn doesn't
	// leave the user staring at "typing…" forever.
	maxTypingDuration = 5 * time.Minute
)

// typingRepeatInterval must be shorter than Telegram's ~5s typing indicator
// expiry so the "печатает…" status stays up continuously while the agent is
// still thinking. A var (not const) so tests can shorten it.
var typingRepeatInterval = 4 * time.Second

var (
	_ channels.Channel       = (*Channel)(nil)
	_ channels.TypingCapable = (*Channel)(nil)
)

// Config holds Telegram channel configuration.
type Config struct {
	Token     string
	AllowFrom []string
	// BaseURL overrides the Telegram API origin; empty uses the real API.
	// Intended for tests.
	BaseURL string
}

// Channel is the Telegram long-polling channel.
type Channel struct {
	*channels.BaseChannel
	Config     Config
	HTTPClient *http.Client
}

// New creates a Telegram channel.
func New(cfg Config, mb *bus.MessageBus) *Channel {
	return &Channel{
		BaseChannel: channels.NewBaseChannel("telegram", mb, cfg.AllowFrom),
		Config:      cfg,
	}
}

func (c *Channel) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: httpClientTimeout}
}

func (c *Channel) baseURL() string {
	if c.Config.BaseURL != "" {
		return c.Config.BaseURL
	}
	return defaultBaseURL
}

func (c *Channel) apiURL(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", c.baseURL(), c.Config.Token, method)
}

// Start begins long-polling for updates. It blocks until ctx is cancelled
// or the bus is closed.
func (c *Channel) Start(ctx context.Context) error {
	if c.Config.Token == "" {
		return fmt.Errorf("telegram: token is required")
	}

	c.SetRunning(true)
	defer c.SetRunning(false)

	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		updates, err := c.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pollErrorBackoff):
			}
			continue
		}

		for _, u := range updates {
			offset = u.UpdateID + 1
			msg, ok := toInboundMessage(u)
			if !ok {
				continue
			}
			if err := c.HandleInbound(ctx, msg); err != nil {
				return err
			}
		}
	}
}

// Send delivers a text response to the given chat.
func (c *Channel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	values := url.Values{}
	values.Set("chat_id", msg.ChatID)
	values.Set("text", msg.Text)
	if msg.ReplyToMessageID != "" {
		values.Set("reply_to_message_id", msg.ReplyToMessageID)
	}
	_, err := call[json.RawMessage](ctx, c.httpClient(), c.apiURL("sendMessage"), values)
	return err
}

// StartTyping implements channels.TypingCapable. It sends the "typing" chat
// action immediately and repeats it every typingRepeatInterval — shorter
// than Telegram's own expiry — until the returned stop func is called, the
// context is cancelled, or maxTypingDuration elapses. The stop func is
// idempotent.
func (c *Channel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	if err := c.sendChatAction(ctx, chatID); err != nil {
		return func() {}, err
	}

	typingCtx, cancel := context.WithTimeout(ctx, maxTypingDuration)
	go func() {
		defer cancel()
		ticker := time.NewTicker(typingRepeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-typingCtx.Done():
				return
			case <-ticker.C:
				_ = c.sendChatAction(typingCtx, chatID)
			}
		}
	}()

	return cancel, nil
}

func (c *Channel) sendChatAction(ctx context.Context, chatID string) error {
	values := url.Values{}
	values.Set("chat_id", chatID)
	values.Set("action", "typing")
	_, err := call[json.RawMessage](ctx, c.httpClient(), c.apiURL("sendChatAction"), values)
	return err
}

func (c *Channel) getUpdates(ctx context.Context, offset int64) ([]tgUpdate, error) {
	values := url.Values{}
	values.Set("timeout", strconv.Itoa(longPollTimeoutSeconds))
	values.Set("offset", strconv.FormatInt(offset, 10))
	values.Set("allowed_updates", `["message"]`)
	return call[[]tgUpdate](ctx, c.httpClient(), c.apiURL("getUpdates"), values)
}

func toInboundMessage(u tgUpdate) (bus.InboundMessage, bool) {
	if u.Message == nil || u.Message.Text == "" {
		return bus.InboundMessage{}, false
	}
	senderID := ""
	if u.Message.From != nil {
		senderID = strconv.FormatInt(u.Message.From.ID, 10)
	}
	return bus.InboundMessage{
		Channel:   "telegram",
		ChatID:    strconv.FormatInt(u.Message.Chat.ID, 10),
		SenderID:  senderID,
		Text:      u.Message.Text,
		MessageID: strconv.FormatInt(u.Message.MessageID, 10),
	}, true
}

// --- Telegram Bot API DTOs ---

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	MessageID int64   `json:"message_id"`
	From      *tgUser `json:"from"`
	Chat      tgChat  `json:"chat"`
	Text      string  `json:"text"`
}

type tgUser struct {
	ID int64 `json:"id"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

func call[T any](ctx context.Context, client *http.Client, apiURL string, values url.Values) (T, error) {
	var zero T
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(values.Encode()))
	if err != nil {
		return zero, fmt.Errorf("telegram: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return zero, fmt.Errorf("telegram: request failed: %w", err)
	}
	defer resp.Body.Close()

	var envelope struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      T      `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return zero, fmt.Errorf("telegram: decode response: %w", err)
	}
	if !envelope.OK {
		return zero, fmt.Errorf("telegram: api error: %s", envelope.Description)
	}
	return envelope.Result, nil
}
