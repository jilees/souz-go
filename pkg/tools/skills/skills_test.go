package skills

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/skills/bundle"
	"souz.ru/souz-go/pkg/skills/registry"
	"souz.ru/souz-go/pkg/skills/validation"
)

func rawArgs(t *testing.T, kv map[string]any) map[string]json.RawMessage {
	t.Helper()
	out := make(map[string]json.RawMessage, len(kv))
	for k, v := range kv {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %q: %v", k, err)
		}
		out[k] = b
	}
	return out
}

// setupApprovedSkill installs a skill with the given extra files and marks
// it APPROVED in a fresh validation store, returning both plus the tool.
func setupApprovedSkill(t *testing.T, files ...bundle.File) (*RunSkillCommand, string) {
	t.Helper()
	all := append([]bundle.File{
		{Path: bundle.SkillMDPath, Content: []byte("---\nname: Test Skill\ndescription: for tests\n---\nBody.")},
	}, files...)
	b, err := bundle.FromFiles(all)
	if err != nil {
		t.Fatalf("FromFiles: %v", err)
	}

	reg := registry.New(t.TempDir(), bundle.DefaultPolicy())
	stored, err := reg.SaveSkillBundle(b)
	if err != nil {
		t.Fatalf("SaveSkillBundle: %v", err)
	}

	store := validation.NewStore(t.TempDir())
	policyVersion := 1
	if err := store.Save(validation.Record{
		SkillID:       stored.SkillID,
		BundleHash:    stored.BundleHash,
		PolicyVersion: policyVersion,
		Status:        validation.StatusApproved,
		Confidence:    0.9,
	}); err != nil {
		t.Fatalf("Save validation record: %v", err)
	}

	return New(reg, store, policyVersion), stored.SkillID
}

func TestRunSkillCommand_BashScript(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	tool, skillID := setupApprovedSkill(t)

	got, err := tool.Execute(context.Background(), rawArgs(t, map[string]any{
		"skillId": skillID,
		"runtime": "bash",
		"script":  "echo hello",
	}), agent.InvocationMeta{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var result struct {
		ExitCode int
		TimedOut bool
		Stdout   string
		Stderr   string
	}
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal result: %v (%s)", err, got)
	}
	if result.ExitCode != 0 || result.TimedOut {
		t.Errorf("unexpected exit: %+v", result)
	}
	if result.Stdout != "hello\n" {
		t.Errorf("stdout = %q, want %q", result.Stdout, "hello\n")
	}
}

func TestRunSkillCommand_ProcessRuntime(t *testing.T) {
	if _, err := exec.LookPath("echo"); err != nil {
		t.Skip("echo not available")
	}
	tool, skillID := setupApprovedSkill(t)

	got, err := tool.Execute(context.Background(), rawArgs(t, map[string]any{
		"skillId": skillID,
		"runtime": "process",
		"command": []string{"echo", "argv works"},
	}), agent.InvocationMeta{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result struct{ Stdout string }
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Stdout != "argv works\n" {
		t.Errorf("stdout = %q", result.Stdout)
	}
}

func TestRunSkillCommand_EnvironmentAndSkillEnvVars(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	tool, skillID := setupApprovedSkill(t)

	got, err := tool.Execute(context.Background(), rawArgs(t, map[string]any{
		"skillId":     skillID,
		"runtime":     "bash",
		"script":      `echo "$SOUZ_SKILL_ID $CUSTOM_VAR"`,
		"environment": map[string]string{"CUSTOM_VAR": "custom-value"},
	}), agent.InvocationMeta{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var result struct{ Stdout string }
	json.Unmarshal([]byte(got), &result)
	want := skillID + " custom-value\n"
	if result.Stdout != want {
		t.Errorf("stdout = %q, want %q", result.Stdout, want)
	}
}

func TestRunSkillCommand_Timeout(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	tool, skillID := setupApprovedSkill(t)

	start := time.Now()
	got, err := tool.Execute(context.Background(), rawArgs(t, map[string]any{
		"skillId":       skillID,
		"runtime":       "bash",
		"script":        "sleep 5",
		"timeoutMillis": 200,
	}), agent.InvocationMeta{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("expected the timeout to cut execution short, took %v", elapsed)
	}
	var result struct{ TimedOut bool }
	json.Unmarshal([]byte(got), &result)
	if !result.TimedOut {
		t.Errorf("expected timedOut=true, got %s", got)
	}
}

func TestRunSkillCommand_RejectsUnapprovedSkill(t *testing.T) {
	b, err := bundle.FromFiles([]bundle.File{
		{Path: bundle.SkillMDPath, Content: []byte("---\nname: Unapproved\ndescription: d\n---\nbody")},
	})
	if err != nil {
		t.Fatalf("FromFiles: %v", err)
	}
	reg := registry.New(t.TempDir(), bundle.DefaultPolicy())
	stored, err := reg.SaveSkillBundle(b)
	if err != nil {
		t.Fatalf("SaveSkillBundle: %v", err)
	}
	store := validation.NewStore(t.TempDir()) // nothing saved: no record

	tool := New(reg, store, 1)
	_, err = tool.Execute(context.Background(), rawArgs(t, map[string]any{
		"skillId": stored.SkillID,
		"script":  "echo should-not-run",
	}), agent.InvocationMeta{})
	if err == nil {
		t.Fatal("expected error for an unapproved skill")
	}
}

func TestRunSkillCommand_RejectsUnknownSkill(t *testing.T) {
	reg := registry.New(t.TempDir(), bundle.DefaultPolicy())
	store := validation.NewStore(t.TempDir())
	tool := New(reg, store, 1)

	_, err := tool.Execute(context.Background(), rawArgs(t, map[string]any{"skillId": "ghost"}), agent.InvocationMeta{})
	if err == nil {
		t.Fatal("expected error for an unknown skill")
	}
}

func TestRunSkillCommand_RejectsWorkingDirectoryEscape(t *testing.T) {
	tool, skillID := setupApprovedSkill(t)

	_, err := tool.Execute(context.Background(), rawArgs(t, map[string]any{
		"skillId":          skillID,
		"script":           "echo hi",
		"workingDirectory": "../../../../etc",
	}), agent.InvocationMeta{})
	if err == nil {
		t.Fatal("expected error for a workingDirectory that escapes the bundle root")
	}
}

func TestRunSkillCommand_StaleValidationIsNotApproved(t *testing.T) {
	tool, skillID := setupApprovedSkill(t)
	// Overwrite the cached record with STALE, simulating a superseded bundle.
	rec, err := tool.ValidationStore.Get(skillID, tool.PolicyVersion, mustBundleHash(t, tool, skillID))
	if err != nil || rec == nil {
		t.Fatalf("Get: %v, %+v", err, rec)
	}
	rec.Status = validation.StatusStale
	if err := tool.ValidationStore.Save(*rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err = tool.Execute(context.Background(), rawArgs(t, map[string]any{
		"skillId": skillID,
		"script":  "echo hi",
	}), agent.InvocationMeta{})
	if err == nil {
		t.Fatal("expected error for a STALE (not APPROVED) skill")
	}
}

func mustBundleHash(t *testing.T, tool *RunSkillCommand, skillID string) string {
	t.Helper()
	stored, err := tool.Registry.GetSkill(skillID)
	if err != nil || stored == nil {
		t.Fatalf("GetSkill: %v, %+v", err, stored)
	}
	return stored.BundleHash
}
