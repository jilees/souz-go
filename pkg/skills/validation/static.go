package validation

import (
	"regexp"

	"souz.ru/souz-go/pkg/skills/bundle"
)

type staticRule struct {
	message string
	pattern *regexp.Regexp
}

// staticRules are deliberately coarse pattern matches, not a security
// guarantee — they catch the obvious/lazy cases (a skill bundled from an
// untrusted source with blatant prompt-injection or exfiltration text) so
// those never reach the more expensive LLM validation stage. Mirrors the
// seven categories the original implementation flagged.
var staticRules = []staticRule{
	{
		message: "possible prompt-injection phrasing",
		pattern: regexp.MustCompile(`(?i)ignore (all |any )?(previous|prior|above) instructions`),
	},
	{
		message: "possible hardcoded credential",
		pattern: regexp.MustCompile(`(?i)(api[_-]?key|secret|password|access[_-]?token)\s*[:=]\s*['"]?[A-Za-z0-9_\-]{16,}`),
	},
	{
		message: "references a private key path",
		pattern: regexp.MustCompile(`(?i)(id_rsa|id_ed25519|id_ecdsa|\.ssh/[a-z_]+|private[_-]?key\.pem)`),
	},
	{
		message: "dumps environment variables to output",
		pattern: regexp.MustCompile(`(?i)\b(printenv|process\.env|os\.environ)\b[^\n]{0,40}(\||>|curl|wget)`),
	},
	{
		message: "destructive shell command",
		pattern: regexp.MustCompile(`(?i)\brm\s+-rf\s+/(\s|$)|\bmkfs\.\w+|\bdd\s+[^\n]*of=/dev/`),
	},
	{
		message: "possible network exfiltration",
		pattern: regexp.MustCompile(`(?i)\b(curl|wget)\b[^\n]*(--upload-file|-T\s|-F\s)`),
	},
	{
		message: "obfuscated shell execution via base64 pipe",
		pattern: regexp.MustCompile(`(?i)base64\s+(-d|--decode)\s*\|\s*(sh|bash|zsh)\b`),
	},
}

// Static scans every file's raw content against staticRules.
func Static(b *bundle.SkillBundle) []Finding {
	var findings []Finding
	for _, f := range b.Files {
		text := string(f.Content)
		for _, rule := range staticRules {
			if rule.pattern.MatchString(text) {
				findings = append(findings, Finding{Stage: "static", File: f.Path, Message: rule.message})
			}
		}
	}
	return findings
}
