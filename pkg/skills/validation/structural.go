package validation

import (
	"strings"

	"souz.ru/souz-go/pkg/skills/bundle"
)

const maxDescriptionChars = 2000

// Structural re-checks the invariants bundle.Load already enforces at parse
// time (missing SKILL.md, blank name/description, duplicate paths). It's
// intentionally leaner than the original Kotlin stage, which ran before its
// bundle loader had fully absorbed these checks — here, a *bundle.SkillBundle
// literally cannot exist without them already holding for a freshly-loaded
// bundle. This stage stays as defense-in-depth for bundles reconstructed
// from other sources (e.g. the registry's stored copy), and adds the few
// checks bundle.Load doesn't cover (description length, non-empty body).
func Structural(b *bundle.SkillBundle) []Finding {
	var findings []Finding

	if strings.TrimSpace(b.Manifest.Name) == "" {
		findings = append(findings, Finding{Stage: "structural", Message: "manifest name is empty"})
	}
	if strings.TrimSpace(b.Manifest.Description) == "" {
		findings = append(findings, Finding{Stage: "structural", Message: "manifest description is empty"})
	}
	if len(b.Manifest.Description) > maxDescriptionChars {
		findings = append(findings, Finding{Stage: "structural", Message: "description exceeds the maximum length"})
	}

	seen := make(map[string]bool, len(b.Files))
	hasSkillMD := false
	for _, f := range b.Files {
		if seen[f.Path] {
			findings = append(findings, Finding{Stage: "structural", File: f.Path, Message: "duplicate file path"})
		}
		seen[f.Path] = true
		if f.Path == bundle.SkillMDPath {
			hasSkillMD = true
		}
	}
	if !hasSkillMD {
		findings = append(findings, Finding{Stage: "structural", Message: "missing " + bundle.SkillMDPath})
	}
	if strings.TrimSpace(b.Body) == "" {
		findings = append(findings, Finding{Stage: "structural", File: bundle.SkillMDPath, Message: "SKILL.md has no instructions body"})
	}

	return findings
}
