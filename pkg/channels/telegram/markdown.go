package telegram

import (
	"html"
	"regexp"
)

// Telegram's Bot API doesn't render Markdown unless parse_mode is set, and
// LLM output is unreliable Markdown (unescaped punctuation breaks
// MarkdownV2's strict parser). HTML mode only requires escaping &, <, > so
// we convert the small subset of Markdown the LLM actually produces to it.
var (
	mdBold = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mdCode = regexp.MustCompile("`([^`]+)`")
	mdLink = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
)

// mdToHTML converts **bold**, `code`, and [text](url) Markdown to
// Telegram-flavored HTML. Everything else is escaped as plain text.
func mdToHTML(s string) string {
	s = html.EscapeString(s)
	s = mdLink.ReplaceAllString(s, `<a href="$2">$1</a>`)
	s = mdCode.ReplaceAllString(s, `<code>$1</code>`)
	s = mdBold.ReplaceAllString(s, `<b>$1</b>`)
	return s
}
