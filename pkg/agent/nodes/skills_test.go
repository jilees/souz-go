package nodes

import (
	"context"
	"errors"
	"strings"
	"testing"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/providers"
	"souz.ru/souz-go/pkg/skills/bundle"
	"souz.ru/souz-go/pkg/skills/registry"
	"souz.ru/souz-go/pkg/skills/validation"
)

// fakeActiveTools seeds a RunSkillCommand definition alongside an unrelated
// tool, so tests can assert the skills node rewrites/removes the former
// without disturbing the latter.
func fakeActiveTools() []providers.ToolDefinition {
	return []providers.ToolDefinition{
		{Name: "WebSearch", Description: "search the web"},
		{Name: runSkillCommandToolName, Description: "base description"},
	}
}

func findTool(tools []providers.ToolDefinition, name string) (providers.ToolDefinition, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return providers.ToolDefinition{}, false
}

func newSkillsTestConfig(t *testing.T, selectResp string) (SkillsConfig, *registry.Registry) {
	t.Helper()
	reg := registry.New(t.TempDir(), bundle.DefaultPolicy())
	store := validation.NewStore(t.TempDir())
	provider := &fakeProvider{resp: &providers.ChatResponse{Content: selectResp}}
	return SkillsConfig{
		Provider:        provider,
		Registry:        reg,
		ValidationStore: store,
		Policy:          validation.DefaultPolicy(),
	}, reg
}

func installSkill(t *testing.T, reg *registry.Registry, name, description, body string) registry.StoredSkill {
	t.Helper()
	b, err := bundle.FromFiles([]bundle.File{
		{Path: bundle.SkillMDPath, Content: []byte("---\nname: " + name + "\ndescription: " + description + "\n---\n" + body)},
	})
	if err != nil {
		t.Fatalf("FromFiles: %v", err)
	}
	stored, err := reg.SaveSkillBundle(b)
	if err != nil {
		t.Fatalf("SaveSkillBundle: %v", err)
	}
	return stored
}

func TestSkills_NoInstalledSkillsIsNoop(t *testing.T) {
	cfg, _ := newSkillsTestConfig(t, "")
	node := NewSkills(cfg)

	in := agent.AgentContext{Input: "hello", SystemPrompt: "base prompt"}
	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)
	if got.SystemPrompt != "base prompt" {
		t.Errorf("SystemPrompt = %q, want unchanged", got.SystemPrompt)
	}
}

func TestSkills_SelectedApprovedSkillInjectsContext(t *testing.T) {
	// The fake provider is used both for skill *selection* (first call) and,
	// if invoked, LLM *validation* (second call). This test relies on the
	// validation record already being APPROVED so only the selection call
	// happens.
	cfg, reg := newSkillsTestConfig(t, `{"selectedSkillIds":["weather-lookup"],"rationale":"weather asked"}`)
	stored := installSkill(t, reg, "Weather Lookup", "Looks up weather", "Use the weather API.")

	if err := cfg.ValidationStore.Save(validation.Record{
		SkillID:       stored.SkillID,
		BundleHash:    stored.BundleHash,
		PolicyVersion: cfg.Policy.Version,
		Status:        validation.StatusApproved,
		Confidence:    0.9,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	node := NewSkills(cfg)
	in := agent.AgentContext{Input: "what's the weather", SystemPrompt: "base prompt", ActiveTools: fakeActiveTools()}
	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)

	if !strings.Contains(got.SystemPrompt, "base prompt") {
		t.Errorf("expected base prompt preserved, got %q", got.SystemPrompt)
	}
	if !strings.Contains(got.SystemPrompt, skillsContextStart) || !strings.Contains(got.SystemPrompt, skillsContextEnd) {
		t.Errorf("expected skills context block, got %q", got.SystemPrompt)
	}
	if !strings.Contains(got.SystemPrompt, "Use the weather API.") {
		t.Errorf("expected skill body injected, got %q", got.SystemPrompt)
	}
	if len(got.InvocationMeta.ActiveSkillIDs) != 1 || got.InvocationMeta.ActiveSkillIDs[0] != stored.SkillID {
		t.Errorf("ActiveSkillIDs = %v, want [%s]", got.InvocationMeta.ActiveSkillIDs, stored.SkillID)
	}

	runSkill, ok := findTool(got.ActiveTools, runSkillCommandToolName)
	if !ok {
		t.Fatal("expected RunSkillCommand to remain in ActiveTools")
	}
	if !strings.Contains(runSkill.Description, "Active Skills for this turn") || !strings.Contains(runSkill.Description, stored.SkillID) {
		t.Errorf("expected RunSkillCommand description to list active skill ids, got %q", runSkill.Description)
	}
	if _, ok := findTool(got.ActiveTools, "WebSearch"); !ok {
		t.Error("expected unrelated tools left untouched in ActiveTools")
	}
}

func TestSkills_UnapprovedSkillIsNotInjected(t *testing.T) {
	cfg, reg := newSkillsTestConfig(t, `{"selectedSkillIds":["weather-lookup"],"rationale":"weather asked"}`)
	installSkill(t, reg, "Weather Lookup", "Looks up weather", "Use the weather API.")
	// No validation record saved: GetSkill exists, but nothing APPROVED.

	node := NewSkills(cfg)
	in := agent.AgentContext{Input: "what's the weather", SystemPrompt: "base prompt", ActiveTools: fakeActiveTools()}
	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)
	if strings.Contains(got.SystemPrompt, skillsContextStart) {
		t.Errorf("expected no skills context for an unapproved skill, got %q", got.SystemPrompt)
	}
	if len(got.InvocationMeta.ActiveSkillIDs) != 0 {
		t.Errorf("expected no ActiveSkillIDs for an unapproved skill, got %v", got.InvocationMeta.ActiveSkillIDs)
	}
	if _, ok := findTool(got.ActiveTools, runSkillCommandToolName); ok {
		t.Error("expected RunSkillCommand removed from ActiveTools when nothing is approved")
	}
	if _, ok := findTool(got.ActiveTools, "WebSearch"); !ok {
		t.Error("expected unrelated tools left untouched in ActiveTools")
	}
}

func TestSkills_StaleRecordTriggersRevalidation(t *testing.T) {
	cfg, reg := newSkillsTestConfig(t, "") // overridden below with a stateful fake
	stored := installSkill(t, reg, "Weather Lookup", "Looks up weather", "Use the weather API.")
	if err := cfg.ValidationStore.Save(validation.Record{
		SkillID:       stored.SkillID,
		BundleHash:    stored.BundleHash,
		PolicyVersion: cfg.Policy.Version,
		Status:        validation.StatusStale,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	seq := &sequenceProvider{responses: []*providers.ChatResponse{
		{Content: `{"selectedSkillIds":["weather-lookup"],"rationale":"weather asked"}`},          // selection
		{Content: `{"decision":"APPROVE","confidence":0.9,"riskLevel":"low","reasons":["fine"]}`}, // re-validation
	}}
	cfg.Provider = seq

	node := NewSkills(cfg)
	in := agent.AgentContext{Input: "what's the weather", ActiveTools: fakeActiveTools()}
	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)
	if !strings.Contains(got.SystemPrompt, "Use the weather API.") {
		t.Errorf("expected re-validated skill injected, got %q", got.SystemPrompt)
	}

	rec, err := cfg.ValidationStore.Get(stored.SkillID, cfg.Policy.Version, stored.BundleHash)
	if err != nil || rec == nil || rec.Status != validation.StatusApproved {
		t.Errorf("expected cache updated to APPROVED, got %+v, %v", rec, err)
	}
	if len(got.InvocationMeta.ActiveSkillIDs) != 1 || got.InvocationMeta.ActiveSkillIDs[0] != stored.SkillID {
		t.Errorf("ActiveSkillIDs = %v, want [%s]", got.InvocationMeta.ActiveSkillIDs, stored.SkillID)
	}
	if runSkill, ok := findTool(got.ActiveTools, runSkillCommandToolName); !ok || !strings.Contains(runSkill.Description, stored.SkillID) {
		t.Errorf("expected RunSkillCommand description to list %q, got %+v", stored.SkillID, runSkill)
	}
}

func TestSkills_NoSelectionStripsStaleContext(t *testing.T) {
	cfg, reg := newSkillsTestConfig(t, `{"selectedSkillIds":[],"rationale":"just chit-chat"}`)
	installSkill(t, reg, "Weather Lookup", "Looks up weather", "Use the weather API.")

	node := NewSkills(cfg)
	staleSystemPrompt := "base prompt\n\n" + skillsContextStart + "\nstale skill instructions\n" + skillsContextEnd
	in := agent.AgentContext{
		Input:          "how are you?",
		SystemPrompt:   staleSystemPrompt,
		InvocationMeta: agent.InvocationMeta{ActiveSkillIDs: []string{"weather-lookup"}},
		ActiveTools:    fakeActiveTools(),
	}
	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)
	if strings.Contains(got.SystemPrompt, skillsContextStart) {
		t.Errorf("expected stale skills context stripped, got %q", got.SystemPrompt)
	}
	if !strings.Contains(got.SystemPrompt, "base prompt") {
		t.Errorf("expected base prompt preserved, got %q", got.SystemPrompt)
	}
	if len(got.InvocationMeta.ActiveSkillIDs) != 0 {
		t.Errorf("expected stale ActiveSkillIDs cleared, got %v", got.InvocationMeta.ActiveSkillIDs)
	}
	if _, ok := findTool(got.ActiveTools, runSkillCommandToolName); ok {
		t.Error("expected RunSkillCommand removed from ActiveTools when nothing is selected")
	}
}

func TestSkills_ErrorPathHidesRunSkillCommand(t *testing.T) {
	cfg, reg := newSkillsTestConfig(t, "")
	installSkill(t, reg, "Weather Lookup", "Looks up weather", "Use the weather API.")
	cfg.Provider = &fakeProvider{err: errors.New("provider unavailable")}

	node := NewSkills(cfg)
	in := agent.AgentContext{
		Input:          "what's the weather",
		SystemPrompt:   "base prompt",
		InvocationMeta: agent.InvocationMeta{ActiveSkillIDs: []string{"weather-lookup"}},
		ActiveTools:    fakeActiveTools(),
	}
	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)
	if len(got.InvocationMeta.ActiveSkillIDs) != 0 {
		t.Errorf("expected ActiveSkillIDs cleared on error, got %v", got.InvocationMeta.ActiveSkillIDs)
	}
	if _, ok := findTool(got.ActiveTools, runSkillCommandToolName); ok {
		t.Error("expected RunSkillCommand removed from ActiveTools when selection errors")
	}
	if _, ok := findTool(got.ActiveTools, "WebSearch"); !ok {
		t.Error("expected unrelated tools left untouched in ActiveTools")
	}
}

func TestSkills_UnconfiguredIsNoop(t *testing.T) {
	node := NewSkills(SkillsConfig{})
	in := agent.AgentContext{Input: "hi", SystemPrompt: "base"}
	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.(agent.AgentContext).SystemPrompt != "base" {
		t.Errorf("expected unchanged context when unconfigured")
	}
}
