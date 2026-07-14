// Package skills implements RunSkillCommand: the one tool through which the
// LLM executes a skill's scripts. There is no Docker sandbox here (excluded
// per CLAUDE.md) — execution is a plain subprocess confined to the skill's
// bundle directory, with a timeout and capped output, which is the
// proportional amount of isolation for a trusted single-user embedded
// device. The security gate is two-layered: a skillId must have a cached
// APPROVED validation.Record for its current bundle hash (checked here, in
// authorize), *and* it must be in ctx.InvocationMeta.ActiveSkillIDs — the
// set the "skills" graph node selected as relevant to this specific turn
// (checked in Execute). The second check is what stops an approved-but-
// unrelated skillId from being reused as a standing license to run
// arbitrary commands for every later turn of the conversation.
package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/skills/registry"
	"souz.ru/souz-go/pkg/skills/validation"
	"souz.ru/souz-go/pkg/tools"
)

const (
	defaultTimeoutMillis = 60_000
	maxTimeoutMillis     = 300_000
	maxOutputChars       = 20_000
	maxCapturedBytes     = 4 << 20 // 4MB raw capture cap before truncation to maxOutputChars
)

var _ tools.Tool = (*RunSkillCommand)(nil)

// RunSkillCommand runs a script or command from an installed, approved
// skill's bundle.
type RunSkillCommand struct {
	Registry        *registry.Registry
	ValidationStore *validation.Store
	PolicyVersion   int
}

// New builds the tool against the given registry and validation cache.
func New(reg *registry.Registry, store *validation.Store, policyVersion int) *RunSkillCommand {
	return &RunSkillCommand{Registry: reg, ValidationStore: store, PolicyVersion: policyVersion}
}

func (t *RunSkillCommand) Name() string { return "RunSkillCommand" }

func (t *RunSkillCommand) Description() string {
	return "Runs a script or command from an installed skill's bundle, in a subprocess confined to that " +
		"skill's directory. Only skills that passed validation can be run."
}

func (t *RunSkillCommand) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"skillId": {"type": "string", "description": "Id of an installed, approved skill"},
			"runtime": {"type": "string", "enum": ["bash", "python", "node", "process"], "description": "How to run this command; defaults to bash. If bash isn't installed on this system, \"bash\" silently falls back to sh — avoid bash-only syntax (arrays, [[ ]], process substitution) unless bash is known to be present"},
			"script": {"type": "string", "description": "Inline script source, for runtime bash/python/node"},
			"command": {"type": "array", "items": {"type": "string"}, "description": "argv, for runtime process"},
			"workingDirectory": {"type": "string", "description": "Directory to run in, relative to the skill's bundle root; defaults to the root"},
			"environment": {"type": "object", "additionalProperties": {"type": "string"}, "description": "Extra environment variables"},
			"stdin": {"type": "string", "description": "Text piped to the command's stdin"},
			"timeoutMillis": {"type": "integer", "description": "Timeout in milliseconds; defaults to 60000, capped at 300000"}
		},
		"required": ["skillId"]
	}`)
}

func (t *RunSkillCommand) Execute(ctx context.Context, args map[string]json.RawMessage, meta agent.InvocationMeta) (string, error) {
	skillID, err := tools.ArgString(args, "skillId", "")
	if err != nil {
		return "", err
	}
	if skillID == "" {
		return "", fmt.Errorf("skillId is required")
	}
	if !activeThisTurn(meta, skillID) {
		return "", fmt.Errorf("skill %q is not active for this turn", skillID)
	}

	root, err := t.authorize(skillID)
	if err != nil {
		return "", err
	}

	runtimeName, err := tools.ArgString(args, "runtime", "bash")
	if err != nil {
		return "", err
	}
	script, err := tools.ArgString(args, "script", "")
	if err != nil {
		return "", err
	}
	command, err := argStringSlice(args, "command")
	if err != nil {
		return "", err
	}
	workingDirectory, err := tools.ArgString(args, "workingDirectory", "")
	if err != nil {
		return "", err
	}
	env, err := argStringMap(args, "environment")
	if err != nil {
		return "", err
	}
	stdin, err := tools.ArgString(args, "stdin", "")
	if err != nil {
		return "", err
	}
	timeoutMillis, err := tools.ArgInt(args, "timeoutMillis", defaultTimeoutMillis)
	if err != nil {
		return "", err
	}

	execDir, err := resolveWorkingDir(root, workingDirectory)
	if err != nil {
		return "", err
	}

	spec, err := buildRunSpec(runtime(strings.ToLower(runtimeName)), script, command, execDir)
	if err != nil {
		return "", err
	}
	spec.Env = append(spec.Env,
		"SOUZ_SKILL_ID="+skillID,
		"SOUZ_SKILL_ROOT="+root,
	)
	for k, v := range env {
		spec.Env = append(spec.Env, k+"="+v)
	}
	spec.Stdin = stdin

	timeout := clampTimeout(timeoutMillis)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := run(runCtx, spec)
	if err != nil {
		return "", fmt.Errorf("run skill %q: %w", skillID, err)
	}
	return formatResult(result), nil
}

// activeThisTurn reports whether skillID is one the "skills" graph node
// selected and approved for the current turn (see
// agent.InvocationMeta.ActiveSkillIDs). This is checked before authorize so
// a skillId that was approved at some point in the conversation, but isn't
// relevant to what the user just asked, can't be reused as a standing
// license to run arbitrary commands.
func activeThisTurn(meta agent.InvocationMeta, skillID string) bool {
	for _, id := range meta.ActiveSkillIDs {
		if id == skillID {
			return true
		}
	}
	return false
}

// authorize resolves skillID to its bundle root, requiring a cached
// APPROVED validation record for the skill's *current* bundle hash. This is
// the second half of the tool's security boundary — see activeThisTurn for
// the first half — and stays a separate check because a skill's approval
// can go stale (bundle hash changed, policy version bumped) independent of
// whether it was selected for this turn.
func (t *RunSkillCommand) authorize(skillID string) (root string, err error) {
	stored, err := t.Registry.GetSkill(skillID)
	if err != nil {
		return "", fmt.Errorf("skill %q: %w", skillID, err)
	}
	if stored == nil {
		return "", fmt.Errorf("skill %q is not installed", skillID)
	}

	rec, err := t.ValidationStore.Get(stored.SkillID, t.PolicyVersion, stored.BundleHash)
	if err != nil {
		return "", fmt.Errorf("skill %q: %w", skillID, err)
	}
	if rec == nil || !rec.Approved() {
		return "", fmt.Errorf("skill %q is not approved for execution", skillID)
	}

	return t.Registry.BundleRoot(stored.SkillID, stored.BundleHash)
}

// resolveWorkingDir confines rel to root: no absolute paths, no "..", and
// the result (after resolving symlinks, if it exists) must stay inside root.
func resolveWorkingDir(root, rel string) (string, error) {
	if rel == "" {
		return root, nil
	}
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workingDirectory %q must stay inside the skill's bundle root", rel)
	}

	joined := filepath.Join(root, clean)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolvedRoot = root
	}
	resolvedJoined, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("workingDirectory %q: %w", rel, err)
	}
	if resolvedJoined != resolvedRoot && !strings.HasPrefix(resolvedJoined, resolvedRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("workingDirectory %q escapes the skill's bundle root", rel)
	}
	info, err := os.Stat(resolvedJoined)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("workingDirectory %q is not a directory", rel)
	}
	return resolvedJoined, nil
}

func clampTimeout(millis int) time.Duration {
	if millis <= 0 {
		millis = defaultTimeoutMillis
	}
	if millis > maxTimeoutMillis {
		millis = maxTimeoutMillis
	}
	return time.Duration(millis) * time.Millisecond
}

func argStringSlice(args map[string]json.RawMessage, key string) ([]string, error) {
	raw, ok := args[key]
	if !ok {
		return nil, nil
	}
	var v []string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("invalid %q argument: %w", key, err)
	}
	return v, nil
}

func argStringMap(args map[string]json.RawMessage, key string) (map[string]string, error) {
	raw, ok := args[key]
	if !ok {
		return nil, nil
	}
	var v map[string]string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("invalid %q argument: %w", key, err)
	}
	return v, nil
}

func formatResult(r runResult) string {
	out, err := json.Marshal(struct {
		ExitCode int    `json:"exitCode"`
		TimedOut bool   `json:"timedOut"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}{
		ExitCode: r.ExitCode,
		TimedOut: r.TimedOut,
		Stdout:   truncate(r.Stdout, maxOutputChars),
		Stderr:   truncate(r.Stderr, maxOutputChars),
	})
	if err != nil {
		return fmt.Sprintf(`{"exitCode":%d,"timedOut":%v}`, r.ExitCode, r.TimedOut)
	}
	return string(out)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n[truncated: %d more characters]", len(s)-max)
}
