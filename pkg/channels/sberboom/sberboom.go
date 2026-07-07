// Package sberboom implements the SberBoom WebSocket channel.
// It connects as a WebSocket CLIENT to the Sber OS bridge (BackendURL).
// Reference: picoclaw/pkg/channels/sberboom/sberboom.go
package sberboom

import (
	"context"
	"souz.ru/souz-go/pkg/bus"
	"souz.ru/souz-go/pkg/channels"
)

// TODO Phase 2: implement WebSocket reconnect loop, readLoop, pingLoop, TTS via /vendor/staros/box.

var _ channels.Channel = (*Channel)(nil)

// Channel is the SberBoom WebSocket channel.
type Channel struct {
	*channels.BaseChannel
	Config Config
}

// Config holds SberBoom channel configuration.
type Config struct {
	BackendURL string
	AllowFrom  []string
}

// New creates a SberBoom channel.
func New(cfg Config, mb *bus.MessageBus) *Channel {
	return &Channel{
		BaseChannel: channels.NewBaseChannel("sberboom", mb, cfg.AllowFrom),
		Config:      cfg,
	}
}

func (c *Channel) Start(ctx context.Context) error {
	c.SetRunning(true)
	defer c.SetRunning(false)
	<-ctx.Done()
	return ctx.Err()
}

func (c *Channel) Send(_ context.Context, _ bus.OutboundMessage) error { return nil }
