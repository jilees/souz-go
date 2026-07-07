package nodes

import (
	"context"
	"fmt"
	"strings"
	"time"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/graph"
	"souz.ru/souz-go/pkg/providers"
)

const contextMarker = "<context>"

// NewEnrich builds the "enrich" graph node: it strips any stale <context>
// block left over from a previous turn, injects a fresh one carrying the
// current date/time (and locale, if known), and appends the turn's raw
// input as a user message. A blank Input leaves History untouched (besides
// dropping stale context), matching a turn with nothing new to enrich.
//
// now defaults to time.Now when nil; tests inject a fixed clock.
func NewEnrich(now func() time.Time) *graph.Node {
	if now == nil {
		now = time.Now
	}
	return graph.NewNode("enrich", func(_ context.Context, in agent.AgentContext) (agent.AgentContext, error) {
		history := stripContextMessages(in.History)
		if in.Input != "" {
			history = append(history, buildContextMessage(now(), in.InvocationMeta))
			history = append(history, providers.Message{Role: providers.RoleUser, Content: in.Input})
		}
		out := in
		out.History = history
		return out, nil
	})
}

func stripContextMessages(msgs []providers.Message) []providers.Message {
	out := make([]providers.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == providers.RoleUser && strings.Contains(m.Content, contextMarker) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func buildContextMessage(now time.Time, meta agent.InvocationMeta) providers.Message {
	loc := time.UTC
	if meta.TimeZone != "" {
		if l, err := time.LoadLocation(meta.TimeZone); err == nil {
			loc = l
		}
	}
	var b strings.Builder
	b.WriteString(contextMarker + "\n")
	b.WriteString("Background information. Use ONLY if strictly relevant to the user query; otherwise ignore. Do not reference this block in your reply.\n")
	b.WriteString("---\n")
	fmt.Fprintf(&b, "- Current date and time: %s\n", now.In(loc).Format("Monday, 2006-01-02 15:04:05 MST"))
	if meta.Locale != "" {
		fmt.Fprintf(&b, "- Locale: %s\n", meta.Locale)
	}
	b.WriteString("</context>")
	return providers.Message{Role: providers.RoleUser, Content: b.String()}
}
