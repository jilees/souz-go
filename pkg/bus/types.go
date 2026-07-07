package bus

// InboundMessage carries an incoming message from a channel to the agent.
type InboundMessage struct {
	Channel   string            // source channel name (e.g. "telegram", "sberboom")
	ChatID    string            // platform-specific chat/session ID
	SenderID  string            // platform-specific sender ID
	Text      string            // decoded text content
	MessageID string            // platform message ID for replies/reactions
	Metadata  map[string]string // optional channel-specific key-value pairs
}

// OutboundMessage carries a response from the agent back to a channel.
type OutboundMessage struct {
	Channel          string
	ChatID           string
	Text             string
	ReplyToMessageID string // set when the channel supports threaded replies
}
