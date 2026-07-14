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
// those never reach the more expensive LLM validation stage. Ported
// verbatim (same seven categories, same regex bodies translated from
// Kotlin's java.util.regex to Go's RE2 syntax — both engines support
// everything used here, so it's a direct transliteration) from the KMP
// original's SkillStaticValidator.kt, so a bundle gets the same static
// verdict regardless of which runtime validates it.
var staticRules = []staticRule{
	{
		message: "possible prompt-injection phrasing",
		pattern: regexp.MustCompile(`(?i)ignore\b.{0,80}\b(previous|prior|system|developer)\b.{0,40}\binstructions?\b`),
	},
	{
		message: "possible credential exfiltration",
		pattern: regexp.MustCompile(`(?i)(api[_ -]?key|token|secret|password).{0,80}(send|upload|exfiltrat|post|curl|wget)`),
	},
	{
		message: "references a private key path",
		pattern: regexp.MustCompile(`(?i)(\.ssh|id_rsa|id_ed25519|known_hosts)`),
	},
	{
		message: "possible environment-variable dumping",
		pattern: regexp.MustCompile(`(?i)(\bprintenv\b|\benv\b|/proc/self/environ|System\.getenv|process\.env)`),
	},
	{
		message: "destructive shell command",
		pattern: regexp.MustCompile(`(?i)rm\s+-rf|chmod\s+-R\s+777|mkfs\b|dd\s+if=.*\s+of=/dev/`),
	},
	{
		message: "possible network exfiltration",
		pattern: regexp.MustCompile(`(?i)(curl|wget).{0,120}(--data|--data-binary|--upload-file|-T\s|\b-F\b|--form|PUT\s+https?://|POST\s+https?://)`),
	},
	{
		message: "obfuscated shell execution via decode/eval pipe",
		pattern: regexp.MustCompile(`(?i)(base64\s+-d|openssl\s+enc|python\s+-c).{0,120}\|\s*(sh|bash|zsh)`),
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
