// Package mattermost implements the Mattermost WebSocket real-time channel.
// There is no existing implementation in souz or picoclaw; this is net-new.
package mattermost

import (
	"context"
	"souz.ru/souz-go/pkg/bus"
	"souz.ru/souz-go/pkg/channels"
)

// TODO Phase 2: implement Mattermost WebSocket API + REST send.

var _ channels.Channel = (*Channel)(nil)

// Channel is the Mattermost channel.
type Channel struct {
	*channels.BaseChannel
	Config Config
}

// Config holds Mattermost channel configuration.
type Config struct {
	ServerURL string
	Token     string
	AllowFrom []string
}

// New creates a Mattermost channel.
func New(cfg Config, mb *bus.MessageBus) *Channel {
	return &Channel{
		BaseChannel: channels.NewBaseChannel("mattermost", mb, cfg.AllowFrom),
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
