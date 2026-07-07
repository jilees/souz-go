// Package validation runs the structural → static → LLM validation pipeline
// a skill bundle must pass before it is trusted, and caches verdicts on
// disk keyed by (skillId, policy version, bundle content hash).
package validation

import (
	"context"
	"time"

	"souz.ru/souz-go/pkg/providers"
	"souz.ru/souz-go/pkg/skills/bundle"
)

// Decision is the LLM validator's raw approve/reject call.
type Decision string

const (
	DecisionApprove Decision = "APPROVE"
	DecisionReject  Decision = "REJECT"
)

// Status is the final, cached outcome for a bundle.
type Status string

const (
	// StatusApproved means the bundle may be activated without re-running
	// the pipeline, as long as its content hash still matches.
	StatusApproved Status = "APPROVED"
	// StatusRejected means the bundle failed one or more stages; it is
	// never activated, and re-validating won't be attempted again for the
	// same (skillId, bundleHash, policyVersion).
	StatusRejected Status = "REJECTED"
	// StatusStale marks a previously APPROVED record superseded by a newer
	// bundle hash for the same skill; it is re-validated on next use.
	StatusStale Status = "STALE"
)

// Finding is one concrete problem surfaced by a validation stage.
type Finding struct {
	Stage   string `json:"stage"` // "structural" | "static" | "llm"
	File    string `json:"file,omitempty"`
	Message string `json:"message"`
}

// Record is the cached verdict for one (skillId, bundleHash, policyVersion).
type Record struct {
	SkillID       string    `json:"skillId"`
	BundleHash    string    `json:"bundleHash"`
	PolicyVersion int       `json:"policyVersion"`
	Status        Status    `json:"status"`
	Confidence    float64   `json:"confidence"`
	Reasons       []string  `json:"reasons,omitempty"`
	Findings      []Finding `json:"findings,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

// Approved reports whether this record allows the skill to be activated
// without re-validation.
func (r Record) Approved() bool { return r.Status == StatusApproved }

// Policy tunes the pipeline. Changing Version invalidates the on-disk cache
// (it's part of the cache key), so bump it whenever a rule change should
// force every skill to be re-validated.
type Policy struct {
	Version               int
	MinApprovalConfidence float64
	ExcerptCharsPerFile   int
	MaxSkillMarkdownChars int
}

// DefaultPolicy mirrors the original implementation's thresholds.
func DefaultPolicy() Policy {
	return Policy{
		Version:               1,
		MinApprovalConfidence: 0.66,
		ExcerptCharsPerFile:   2000,
		MaxSkillMarkdownChars: 8000,
	}
}

// Validate runs the full pipeline for a freshly-loaded bundle. It always
// returns a Record — validation failures (structural/static rejections, LLM
// errors, unparseable LLM output) produce a StatusRejected record rather
// than a Go error, so callers always have something cacheable. The only Go
// error path is ctx being done before the LLM stage could even start.
//
// Callers are responsible for the cache: check Store.Get first, and call
// Store.InvalidateOthers/Store.Save around this as appropriate.
func Validate(ctx context.Context, provider providers.LLMProvider, b *bundle.SkillBundle, policy Policy) Record {
	rec := Record{
		SkillID:       b.SkillID,
		BundleHash:    b.Hash(),
		PolicyVersion: policy.Version,
		CreatedAt:     time.Now().UTC(),
	}

	findings := Structural(b)
	findings = append(findings, Static(b)...)
	if len(findings) > 0 {
		rec.Status = StatusRejected
		rec.Findings = findings
		rec.Reasons = []string{"structural/static checks failed"}
		return rec
	}

	if err := ctx.Err(); err != nil {
		rec.Status = StatusRejected
		rec.Reasons = []string{"validation cancelled: " + err.Error()}
		return rec
	}

	verdict := ValidateWithLLM(ctx, provider, b, policy)
	rec.Confidence = verdict.Confidence
	rec.Reasons = verdict.Reasons
	if len(verdict.Findings) > 0 {
		rec.Findings = append(rec.Findings, verdict.Findings...)
	}
	if verdict.Decision == DecisionApprove && verdict.Confidence >= policy.MinApprovalConfidence {
		rec.Status = StatusApproved
	} else {
		rec.Status = StatusRejected
	}
	return rec
}
