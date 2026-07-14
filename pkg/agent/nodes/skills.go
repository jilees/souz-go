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

	// runSkillCommandToolName must match skills.RunSkillCommand.Name() —
	// duplicated here rather than imported to avoid pkg/agent/nodes depending
	// on pkg/tools/skills for a single string constant.
	runSkillCommandToolName = "RunSkillCommand"
)

// SkillsConfig wires the skills subsystem into the "skills" graph node.
type SkillsConfig struct {
	Provider        providers.LLMProvider
	Registry        *registry.Registry
	ValidationStore *validation.Store
	Policy          validation.Policy
	// Model is the same chat model configured for the agent's normal turns.
	// Selection and validation both make their own LLM calls independent of
	// any live turn's AgentSettings, so this is the only way they learn
	// which model to use — see selection.Select's doc comment for why an
	// empty Model is not a safe default.
	Model string
}

// NewSkills builds the "skills" graph node: it selects which installed
// skills (if any) are relevant to this turn's input, ensures each selected
// skill has a current APPROVED validation record (re-validating a STALE or
// missing one), injects the approved skills' instructions into
// ctx.SystemPrompt, records their ids in ctx.InvocationMeta.ActiveSkillIDs,
// and rewrites the RunSkillCommand tool definition in ctx.ActiveTools to
// match — dropping it entirely when nothing is active this turn, or
// appending the list of active skill ids to its description when something
// is. Execution of a skill's scripts happens later, via the RunSkillCommand
// tool, which checks a call's skillId against ActiveSkillIDs — this node is
// what decides which skills' instructions the model gets to see, whether it
// even sees RunSkillCommand as callable, and which skillIds it will accept
// this turn. Ported from the KMP original's per-turn tool-description
// rewrite (NodesSkills.kt's withSkillTools) so the model learns what's
// actually usable *before* deciding to call the tool, not just after.
//
// It fails open on the system-prompt enrichment (any error — registry
// unreadable, selection/validation LLM call failing — leaves the turn
// unaffected, aside from clearing out any stale skills-context block from a
// previous turn) but always fails closed on ActiveSkillIDs and tool
// visibility: any error path here leaves ActiveSkillIDs empty and
// RunSkillCommand out of ActiveTools, so a turn where skill selection
// couldn't run grants no RunSkillCommand access rather than silently
// keeping a prior grant.
func NewSkills(cfg SkillsConfig) *graph.Node {
	return graph.NewNode("skills", func(ctx context.Context, in agent.AgentContext) (agent.AgentContext, error) {
		out, err := activateSkills(ctx, cfg, in)
		if err != nil {
			out = in
			out.SystemPrompt = stripSkillsContext(in.SystemPrompt)
			out.InvocationMeta.ActiveSkillIDs = nil
			out.ActiveTools = withoutRunSkillCommand(in.ActiveTools)
		}
		return out, nil
	})
}

type activatedSkill struct {
	SkillID     string
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

	result, err := selection.Select(ctx, cfg.Provider, in.Input, cfg.Model, candidates)
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
			SkillID:     stored.SkillID,
			Name:        b.Manifest.Name,
			Description: b.Manifest.Description,
			Version:     b.Manifest.Version,
			Body:        b.Body,
		})
	}

	if len(activated) == 0 {
		return withoutSkillsContext(in), nil
	}

	activeIDs := make([]string, len(activated))
	for i, s := range activated {
		activeIDs[i] = s.SkillID
	}

	out := in
	out.SystemPrompt = appendSkillsContext(stripSkillsContext(in.SystemPrompt), activated)
	out.InvocationMeta.ActiveSkillIDs = activeIDs
	out.ActiveTools = withActiveSkillsDescription(in.ActiveTools, activated)
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
		fresh := validation.Validate(ctx, cfg.Provider, loaded, cfg.Policy, cfg.Model)
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
	strippedTools := withoutRunSkillCommand(in.ActiveTools)
	if stripped == in.SystemPrompt && in.InvocationMeta.ActiveSkillIDs == nil && len(strippedTools) == len(in.ActiveTools) {
		return in
	}
	out := in
	out.SystemPrompt = stripped
	out.InvocationMeta.ActiveSkillIDs = nil
	out.ActiveTools = strippedTools
	return out
}

// withoutRunSkillCommand drops the RunSkillCommand definition from tools
// entirely — matching KMP's withDynamicSkillTools when no skill is
// activated — rather than leaving it visible with nothing it's authorized
// to run. A model that never sees the tool can't attempt (and fail) a call.
func withoutRunSkillCommand(toolsIn []providers.ToolDefinition) []providers.ToolDefinition {
	out := make([]providers.ToolDefinition, 0, len(toolsIn))
	for _, t := range toolsIn {
		if t.Name == runSkillCommandToolName {
			continue
		}
		out = append(out, t)
	}
	return out
}

// withActiveSkillsDescription rewrites RunSkillCommand's Description (if
// present in toolsIn) to append this turn's activated skill ids, mirroring
// KMP's NodesSkills.kt buildSkillCommandDescription — the model reads which
// skillIds are actually callable as part of deciding whether to call the
// tool at all, rather than learning it only from a runtime rejection.
func withActiveSkillsDescription(toolsIn []providers.ToolDefinition, activated []activatedSkill) []providers.ToolDefinition {
	out := make([]providers.ToolDefinition, len(toolsIn))
	copy(out, toolsIn)
	for i, t := range out {
		if t.Name != runSkillCommandToolName {
			continue
		}
		var b strings.Builder
		b.WriteString(t.Description)
		b.WriteString("\n\nActive Skills for this turn:\n")
		for _, s := range activated {
			fmt.Fprintf(&b, "- %s\n", s.SkillID)
		}
		b.WriteString("Do not use this tool merely to list or inspect the skill bundle files. ")
		b.WriteString("Call it only when an active skill instruction requires executing a bundled script or command. ")
		b.WriteString("For instruction-only/template-only skills, follow the injected skill instructions without calling this tool.")
		out[i].Description = b.String()
	}
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
