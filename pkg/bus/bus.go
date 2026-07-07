package bus

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// ErrBusClosed is returned when publishing to a closed MessageBus.
var ErrBusClosed = errors.New("bus: closed")

const defaultBufferSize = 64

// MessageBus routes messages between channels and the agent executor.
// It is safe for concurrent use. Close must be called exactly once.
type MessageBus struct {
	inbound  chan InboundMessage
	outbound chan OutboundMessage

	done      chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
	wg        sync.WaitGroup
}

// New creates a MessageBus with default buffer sizes.
func New() *MessageBus {
	return &MessageBus{
		inbound:  make(chan InboundMessage, defaultBufferSize),
		outbound: make(chan OutboundMessage, defaultBufferSize),
		done:     make(chan struct{}),
	}
}

// PublishInbound sends an inbound message to the bus.
// Returns ErrBusClosed if the bus is closed, or ctx.Err() if cancelled.
func (mb *MessageBus) PublishInbound(ctx context.Context, msg InboundMessage) error {
	if mb.closed.Load() {
		return ErrBusClosed
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-mb.done:
		return ErrBusClosed
	default:
	}

	mb.wg.Add(1)
	defer mb.wg.Done()

	select {
	case mb.inbound <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-mb.done:
		return ErrBusClosed
	}
}

// InboundChan returns the read-only inbound message stream.
func (mb *MessageBus) InboundChan() <-chan InboundMessage {
	return mb.inbound
}

// PublishOutbound sends an outbound message to the bus.
func (mb *MessageBus) PublishOutbound(ctx context.Context, msg OutboundMessage) error {
	if mb.closed.Load() {
		return ErrBusClosed
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-mb.done:
		return ErrBusClosed
	default:
	}

	mb.wg.Add(1)
	defer mb.wg.Done()

	select {
	case mb.outbound <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-mb.done:
		return ErrBusClosed
	}
}

// OutboundChan returns the read-only outbound message stream.
func (mb *MessageBus) OutboundChan() <-chan OutboundMessage {
	return mb.outbound
}

// Close shuts down the bus. Safe to call from multiple goroutines;
// only the first call takes effect. Waits for in-flight publishes to finish
// before closing and draining internal channels.
func (mb *MessageBus) Close() {
	mb.closeOnce.Do(func() {
		close(mb.done)
		mb.closed.Store(true)
		mb.wg.Wait()
		close(mb.inbound)
		close(mb.outbound)
		for range mb.inbound {
		}
		for range mb.outbound {
		}
	})
}
