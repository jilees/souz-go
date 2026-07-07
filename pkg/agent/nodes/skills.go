package nodes

import (
	"context"
	"fmt"
	"strings"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/graph"
	"souz.ru/souz-go/pkg/providers"
	"souz.ru/souz-go/pkg/skills/bundle"
	"souz.ru/souz-go/pkg/skills/registry"
	"souz.ru/souz-go/pkg/skills/selection"
	"souz.ru/souz-go/pkg/skills/validation"
)

const (
	skillsContextStart = "<souz_skills_context>"
	skillsContextEnd   = "</souz_skills_context>"
)

// SkillsConfig wires the skills subsystem into the "skills" graph node.
type SkillsConfig struct {
	Provider        providers.LLMProvider
	Registry        *registry.Registry
	ValidationStore *validation.Store
	Policy          validation.Policy
}

// NewSkills builds the "skills" graph node: it selects which installed
// skills (if any) are relevant to this turn's input, ensures each selected
// skill has a current APPROVED validation record (re-validating a STALE or
// missing one), and injects the approved skills' instructions into
// ctx.SystemPrompt. Execution of a skill's scripts happens later, via the
// RunSkillCommand tool — this node only decides which skills' instructions
// the model gets to see.
//
// It fails open: any error (registry unreadable, selection/validation LLM
// call failing) leaves the turn unaffected, aside from clearing out any
// stale skills-context block from a previous turn. Skills are a best-effort
// enrichment, never a reason to fail the whole turn.
func NewSkills(cfg SkillsConfig) *graph.Node {
	return graph.NewNode("skills", func(ctx context.Context, in agent.AgentContext) (agent.AgentContext, error) {
		out, err := activateSkills(ctx, cfg, in)
		if err != nil {
			out = in
			out.SystemPrompt = stripSkillsContext(in.SystemPrompt)
		}
		return out, nil
	})
}

type activatedSkill struct {
	Name        string
	Description string
	Version     string
	Body        string
}

func activateSkills(ctx context.Context, cfg SkillsConfig, in agent.AgentContext) (agent.AgentContext, error) {
	if cfg.Registry == nil || cfg.Provider == nil || cfg.ValidationStore == nil {
		return in, nil
	}

	catalog, err := cfg.Registry.ListSkills()
	if err != nil {
		return agent.AgentContext{}, err
	}
	if len(catalog) == 0 {
		return withoutSkillsContext(in), nil
	}

	candidates := make([]selection.Candidate, len(catalog))
	byID := make(map[string]registry.StoredSkill, len(catalog))
	for i, s := range catalog {
		candidates[i] = selection.Candidate{
			SkillID:     s.SkillID,
			Name:        s.Manifest.Name,
			Description: s.Manifest.Description,
			Author:      s.Manifest.Author,
			Version:     s.Manifest.Version,
		}
		byID[s.SkillID] = s
	}

	result, err := selection.Select(ctx, cfg.Provider, in.Input, candidates)
	if err != nil {
		return agent.AgentContext{}, err
	}
	if len(result.SelectedSkillIDs) == 0 {
		return withoutSkillsContext(in), nil
	}

	var activated []activatedSkill
	for _, id := range result.SelectedSkillIDs {
		stored, ok := byID[id]
		if !ok {
			continue
		}
		b, ok := ensureApproved(ctx, cfg, stored)
		if !ok {
			continue
		}
		activated = append(activated, activatedSkill{
			Name:        b.Manifest.Name,
			Description: b.Manifest.Description,
			Version:     b.Manifest.Version,
			Body:        b.Body,
		})
	}

	if len(activated) == 0 {
		return withoutSkillsContext(in), nil
	}

	out := in
	out.SystemPrompt = appendSkillsContext(stripSkillsContext(in.SystemPrompt), activated)
	return out, nil
}

// ensureApproved makes sure stored has a current APPROVED validation
// record — re-validating if the cached one is missing or STALE — and
// returns its bundle if so. ok is false whenever the skill should not be
// activated this turn, for any reason (I/O error, rejected, unapproved).
func ensureApproved(ctx context.Context, cfg SkillsConfig, stored registry.StoredSkill) (b *bundle.SkillBundle, ok bool) {
	_ = cfg.ValidationStore.InvalidateOthers(stored.SkillID, cfg.Policy.Version, stored.BundleHash)

	rec, err := cfg.ValidationStore.Get(stored.SkillID, cfg.Policy.Version, stored.BundleHash)
	if err != nil {
		return nil, false
	}

	if rec == nil || rec.Status == validation.StatusStale {
		loaded, err := cfg.Registry.LoadSkillBundle(stored.SkillID, stored.BundleHash)
		if err != nil {
			return nil, false
		}
		fresh := validation.Validate(ctx, cfg.Provider, loaded, cfg.Policy)
		_ = cfg.ValidationStore.Save(fresh)
		if !fresh.Approved() {
			return nil, false
		}
		return loaded, true
	}

	if !rec.Approved() {
		return nil, false
	}
	loaded, err := cfg.Registry.LoadSkillBundle(stored.SkillID, stored.BundleHash)
	if err != nil {
		return nil, false
	}
	return loaded, true
}

func withoutSkillsContext(in agent.AgentContext) agent.AgentContext {
	stripped := stripSkillsContext(in.SystemPrompt)
	if stripped == in.SystemPrompt {
		return in
	}
	out := in
	out.SystemPrompt = stripped
	return out
}

func stripSkillsContext(systemPrompt string) string {
	start := strings.Index(systemPrompt, skillsContextStart)
	if start < 0 {
		return systemPrompt
	}
	end := strings.Index(systemPrompt, skillsContextEnd)
	if end < 0 {
		return systemPrompt
	}
	end += len(skillsContextEnd)
	return strings.TrimSpace(systemPrompt[:start] + systemPrompt[end:])
}

func appendSkillsContext(systemPrompt string, activated []activatedSkill) string {
	var b strings.Builder
	b.WriteString(skillsContextStart)
	b.WriteString("\nThe following skills were selected as relevant to the current request. ")
	b.WriteString("Follow each skill's instructions when performing related work; ignore skills that turn out not to apply.\n")
	for _, s := range activated {
		fmt.Fprintf(&b, "\n## %s", s.Name)
		if s.Version != "" {
			fmt.Fprintf(&b, " (v%s)", s.Version)
		}
		b.WriteString("\n")
		if s.Description != "" {
			fmt.Fprintf(&b, "%s\n\n", s.Description)
		}
		b.WriteString(s.Body)
		b.WriteString("\n")
	}
	b.WriteString(skillsContextEnd)

	if systemPrompt == "" {
		return b.String()
	}
	return systemPrompt + "\n\n" + b.String()
}
