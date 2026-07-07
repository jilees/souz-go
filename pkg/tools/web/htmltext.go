package web

import (
	"html"
	"regexp"
	"strings"
)

// Go's regexp (RE2) has no backreferences, so script/style tags are each
// stripped with their own pattern rather than one parameterized by tag name.
var (
	scriptRe  = regexp.MustCompile(`(?is)<script[^>]*>.*?</script\s*>`)
	styleRe   = regexp.MustCompile(`(?is)<style[^>]*>.*?</style\s*>`)
	commentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	tagRe     = regexp.MustCompile(`(?s)<[^>]+>`)
	wsRe      = regexp.MustCompile(`[ \t\r\n]+`)
)

// htmlToText renders a rough plain-text version of an HTML page: script,
// style, and comments are dropped, remaining tags are stripped, entities are
// decoded, and whitespace is collapsed. It is not a spec-compliant HTML
// parser — good enough for an LLM to read, not for rendering.
func htmlToText(document string) string {
	s := scriptRe.ReplaceAllString(document, " ")
	s = styleRe.ReplaceAllString(s, " ")
	s = commentRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = wsRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
