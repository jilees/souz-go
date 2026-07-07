package registry

import (
	"os"
	"path/filepath"
	"testing"

	"souz.ru/souz-go/pkg/skills/bundle"
)

func demoBundle(t *testing.T) *bundle.SkillBundle {
	t.Helper()
	b, err := bundle.FromFiles([]bundle.File{
		{Path: bundle.SkillMDPath, Content: []byte("---\nname: Demo Skill\ndescription: does demo things\n---\nInstructions here.")},
		{Path: "helper.py", Content: []byte("print('hi')\n")},
	})
	if err != nil {
		t.Fatalf("FromFiles: %v", err)
	}
	return b
}

func TestSaveAndListSkills(t *testing.T) {
	dir := t.TempDir()
	reg := New(dir, bundle.DefaultPolicy())
	b := demoBundle(t)

	stored, err := reg.SaveSkillBundle(b)
	if err != nil {
		t.Fatalf("SaveSkillBundle: %v", err)
	}
	if stored.SkillID != "demo-skill" || stored.BundleHash != b.Hash() {
		t.Errorf("unexpected stored skill: %+v", stored)
	}

	list, err := reg.ListSkills()
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(list) != 1 || list[0].SkillID != "demo-skill" || list[0].Loose {
		t.Errorf("unexpected list: %+v", list)
	}
}

func TestGetSkillAndGetSkillByName(t *testing.T) {
	dir := t.TempDir()
	reg := New(dir, bundle.DefaultPolicy())
	b := demoBundle(t)
	if _, err := reg.SaveSkillBundle(b); err != nil {
		t.Fatalf("SaveSkillBundle: %v", err)
	}

	byID, err := reg.GetSkill("demo-skill")
	if err != nil || byID == nil {
		t.Fatalf("GetSkill: %v, %+v", err, byID)
	}
	byName, err := reg.GetSkillByName("Demo Skill")
	if err != nil || byName == nil {
		t.Fatalf("GetSkillByName: %v, %+v", err, byName)
	}
	if byID.SkillID != byName.SkillID {
		t.Errorf("GetSkill/GetSkillByName mismatch: %+v vs %+v", byID, byName)
	}

	missing, err := reg.GetSkill("does-not-exist")
	if err != nil || missing != nil {
		t.Fatalf("expected nil, nil for missing skill, got %v, %+v", err, missing)
	}
}

func TestLoadSkillBundle_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	reg := New(dir, bundle.DefaultPolicy())
	b := demoBundle(t)
	stored, err := reg.SaveSkillBundle(b)
	if err != nil {
		t.Fatalf("SaveSkillBundle: %v", err)
	}

	loaded, err := reg.LoadSkillBundle(stored.SkillID, stored.BundleHash)
	if err != nil {
		t.Fatalf("LoadSkillBundle: %v", err)
	}
	if loaded.Hash() != b.Hash() || len(loaded.Files) != len(b.Files) {
		t.Errorf("loaded bundle mismatch: %+v", loaded)
	}
}

func TestSaveSkillBundle_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	reg := New(dir, bundle.DefaultPolicy())
	b := demoBundle(t)

	if _, err := reg.SaveSkillBundle(b); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if _, err := reg.SaveSkillBundle(b); err != nil {
		t.Fatalf("second save: %v", err)
	}

	list, err := reg.ListSkills()
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected exactly 1 entry after repeated saves, got %d", len(list))
	}
}

func TestLooseSkillDiscovery(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "loose-one")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\nname: Loose One\ndescription: dropped in directly\n---\nDo the loose thing."
	if err := os.WriteFile(filepath.Join(skillDir, bundle.SkillMDPath), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	reg := New(dir, bundle.DefaultPolicy())
	list, err := reg.ListSkills()
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(list) != 1 || !list[0].Loose || list[0].Manifest.Name != "Loose One" {
		t.Fatalf("unexpected loose skill listing: %+v", list)
	}

	root, err := reg.BundleRoot(list[0].SkillID, list[0].BundleHash)
	if err != nil {
		t.Fatalf("BundleRoot: %v", err)
	}
	if root != skillDir {
		t.Errorf("BundleRoot = %q, want %q", root, skillDir)
	}

	loaded, err := reg.LoadSkillBundle(list[0].SkillID, list[0].BundleHash)
	if err != nil {
		t.Fatalf("LoadSkillBundle (loose): %v", err)
	}
	if loaded.Manifest.Name != "Loose One" {
		t.Errorf("unexpected loaded loose bundle: %+v", loaded.Manifest)
	}
}

func TestListSkills_EmptyRegistryDirDoesNotError(t *testing.T) {
	reg := New(filepath.Join(t.TempDir(), "does-not-exist-yet"), bundle.DefaultPolicy())
	list, err := reg.ListSkills()
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %+v", list)
	}
}

func TestLoadSkillBundle_UnknownSkillErrors(t *testing.T) {
	reg := New(t.TempDir(), bundle.DefaultPolicy())
	if _, err := reg.LoadSkillBundle("nope", "deadbeef"); err == nil {
		t.Fatal("expected error for unknown skill")
	}
}
