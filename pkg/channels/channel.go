package channels

import (
	"context"
	"sync/atomic"

	"souz.ru/souz-go/pkg/bus"
)

// Channel handles one communication channel (SberBoom, Telegram, Mattermost, …).
type Channel interface {
	// Name returns the unique identifier used in bus messages (e.g. "telegram").
	Name() string
	// Start connects to the platform and begins dispatching messages.
	// It blocks until the context is cancelled or a fatal error occurs.
	Start(ctx context.Context) error
	// Send delivers a text response to the given chatID.
	Send(ctx context.Context, msg bus.OutboundMessage) error
	// IsRunning returns true while the channel is connected.
	IsRunning() bool
}

// TypingCapable is implemented by channels that can show a "typing"/"thinking"
// indicator to the user while the agent is processing a turn. StartTyping
// begins the indicator and returns a stop function; the stop function must
// be idempotent and safe to call multiple times (including never, if
// StartTyping itself failed).
type TypingCapable interface {
	StartTyping(ctx context.Context, chatID string) (stop func(), err error)
}

// BaseChannel provides the common allow-list check and bus-publish logic
// that every concrete channel embeds.
type BaseChannel struct {
	name      string
	allowList []string
	mb        *bus.MessageBus
	running   atomic.Bool
}

// NewBaseChannel creates a BaseChannel.
// allowList may contain user IDs; an empty list allows everyone.
func NewBaseChannel(name string, mb *bus.MessageBus, allowList []string) *BaseChannel {
	return &BaseChannel{name: name, mb: mb, allowList: allowList}
}

// Name returns the channel identifier.
func (b *BaseChannel) Name() string { return b.name }

// IsRunning returns the current running state.
func (b *BaseChannel) IsRunning() bool { return b.running.Load() }

// SetRunning updates the running flag.
func (b *BaseChannel) SetRunning(v bool) { b.running.Store(v) }

// IsAllowed returns true if senderID is permitted by the allow-list.
// An empty allow-list permits everyone. Use "*" to explicitly allow all.
func (b *BaseChannel) IsAllowed(senderID string) bool {
	if len(b.allowList) == 0 {
		return true
	}
	for _, a := range b.allowList {
		if a == "*" || a == senderID {
			return true
		}
	}
	return false
}

// HandleInbound publishes a message to the bus after checking the allow-list.
func (b *BaseChannel) HandleInbound(ctx context.Context, msg bus.InboundMessage) error {
	if !b.IsAllowed(msg.SenderID) {
		return nil
	}
	return b.mb.PublishInbound(ctx, msg)
}
