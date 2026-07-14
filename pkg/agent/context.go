package agent

import (
	"souz.ru/souz-go/pkg/providers"
)

// AgentSettings controls LLM behaviour for a single turn.
type AgentSettings struct {
	Model       string
	Temperature float64
	MaxTokens   int
	// ContextSize is the maximum number of history messages passed to the LLM.
	// 0 means unlimited.
	ContextSize int
}

// InvocationMeta carries request-scoped metadata through the graph.
type InvocationMeta struct {
	UserID         string
	ConversationID string
	RequestID      string
	Locale         string
	TimeZone       string
	// ActiveSkillIDs lists the skills the "skills" node selected and
	// approved for this turn (see nodes.NewSkills). RunSkillCommand checks
	// a call's skillId against this list — an approved-but-not-selected
	// skill's id is rejected, so a skill can't be invoked just because it
	// was ever approved at some point in the conversation.
	ActiveSkillIDs []string
}

// EventSink receives real-time execution events while a turn is in progress.
// The HTTP layer implements this interface to write SSE events; channels that
// don't support streaming use NoopEventSink.
type EventSink interface {
	EmitDelta(delta string)
	EmitToolCall(name string, argsJSON string)
	EmitToolResult(name string, result string, isError bool)
	EmitError(code, message string)
	// Done signals that the turn has completed (success or failure already emitted).
	Done()
}

// NoopEventSink discards all events. Used when no streaming consumer is attached.
type NoopEventSink struct{}

func (NoopEventSink) EmitDelta(string)                    {}
func (NoopEventSink) EmitToolCall(string, string)         {}
func (NoopEventSink) EmitToolResult(string, string, bool) {}
func (NoopEventSink) EmitError(string, string)            {}
func (NoopEventSink) Done()                               {}

// AgentContext is the immutable state snapshot passed through every graph node.
// Nodes receive a value, modify a copy, and return the copy — never mutate in place.
type AgentContext struct {
	Input          string
	History        []providers.Message
	ActiveTools    []providers.ToolDefinition
	SystemPrompt   string
	Settings       AgentSettings
	InvocationMeta InvocationMeta
	EventSink      EventSink
}

// WithHistory returns a copy of ctx with msgs appended to the history.
func (ctx AgentContext) WithHistory(msgs ...providers.Message) AgentContext {
	updated := make([]providers.Message, len(ctx.History)+len(msgs))
	copy(updated, ctx.History)
	copy(updated[len(ctx.History):], msgs)
	ctx.History = updated
	return ctx
}

// WithTools returns a copy of ctx with tools replacing the current ActiveTools.
func (ctx AgentContext) WithTools(tools []providers.ToolDefinition) AgentContext {
	ctx.ActiveTools = tools
	return ctx
}

// WithSystemPrompt returns a copy of ctx with a new system prompt.
func (ctx AgentContext) WithSystemPrompt(prompt string) AgentContext {
	ctx.SystemPrompt = prompt
	return ctx
}
