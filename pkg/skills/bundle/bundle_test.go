package bundle

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

const sampleSkillMD = `---
name: Weather Lookup
description: Looks up current weather for a city.
author: souz
version: 1.0.0
metadata:
  category: utility
  requires_network: "true"
---
# Weather Lookup

Use this skill to fetch weather data.
`

func TestLoad_ParsesManifestAndBody(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "SKILL.md", sampleSkillMD)
	writeFile(t, root, "helper.py", "print('hi')\n")

	b, err := Load(root, DefaultPolicy())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if b.Manifest.Name != "Weather Lookup" || b.Manifest.Description != "Looks up current weather for a city." {
		t.Errorf("unexpected manifest: %+v", b.Manifest)
	}
	if b.Manifest.Author != "souz" || b.Manifest.Version != "1.0.0" {
		t.Errorf("unexpected manifest extras: %+v", b.Manifest)
	}
	if b.Manifest.Metadata["category"] != "utility" || b.Manifest.Metadata["requires_network"] != "true" {
		t.Errorf("unexpected metadata: %+v", b.Manifest.Metadata)
	}
	if b.SkillID != "weather-lookup" {
		t.Errorf("SkillID = %q, want %q", b.SkillID, "weather-lookup")
	}
	if len(b.Files) != 2 {
		t.Errorf("expected 2 files, got %d: %+v", len(b.Files), b.Files)
	}
	wantBody := "# Weather Lookup\n\nUse this skill to fetch weather data."
	if b.Body != wantBody {
		t.Errorf("Body = %q, want %q", b.Body, wantBody)
	}
}

func TestLoad_MissingSkillMDFails(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "notes.txt", "hello")

	if _, err := Load(root, DefaultPolicy()); err == nil {
		t.Fatal("expected error for missing SKILL.md")
	}
}

func TestLoad_RequiresNameAndDescription(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "SKILL.md", "---\nname: X\n---\nbody")

	if _, err := Load(root, DefaultPolicy()); err == nil {
		t.Fatal("expected error for missing description")
	}
}

func TestLoad_RejectsDisallowedExtension(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "SKILL.md", sampleSkillMD)
	writeFile(t, root, "payload.exe", "MZ")

	if _, err := Load(root, DefaultPolicy()); err == nil {
		t.Fatal("expected error for disallowed extension")
	}
}

func TestLoad_RejectsOversizedFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "SKILL.md", sampleSkillMD)

	policy := DefaultPolicy()
	policy.MaxFileBytes = 4
	writeFile(t, root, "big.txt", "this is way more than 4 bytes")

	if _, err := Load(root, policy); err == nil {
		t.Fatal("expected error for oversized file")
	}
}

func TestLoad_RejectsSymlink(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "SKILL.md", sampleSkillMD)

	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	if _, err := Load(root, DefaultPolicy()); err == nil {
		t.Fatal("expected error for symlink in bundle")
	}
}

func TestHash_IsOrderIndependentAndChangesWithContent(t *testing.T) {
	b1, err := FromFiles([]File{
		{Path: SkillMDPath, Content: []byte("---\nname: A\ndescription: d\n---\nbody")},
		{Path: "a.txt", Content: []byte("1")},
		{Path: "b.txt", Content: []byte("2")},
	})
	if err != nil {
		t.Fatalf("FromFiles: %v", err)
	}
	b2, err := FromFiles([]File{
		{Path: "b.txt", Content: []byte("2")},
		{Path: SkillMDPath, Content: []byte("---\nname: A\ndescription: d\n---\nbody")},
		{Path: "a.txt", Content: []byte("1")},
	})
	if err != nil {
		t.Fatalf("FromFiles: %v", err)
	}
	if b1.Hash() != b2.Hash() {
		t.Errorf("expected order-independent hash, got %q vs %q", b1.Hash(), b2.Hash())
	}

	b3, err := FromFiles([]File{
		{Path: SkillMDPath, Content: []byte("---\nname: A\ndescription: d\n---\nbody")},
		{Path: "a.txt", Content: []byte("DIFFERENT")},
		{Path: "b.txt", Content: []byte("2")},
	})
	if err != nil {
		t.Fatalf("FromFiles: %v", err)
	}
	if b1.Hash() == b3.Hash() {
		t.Error("expected hash to change when content changes")
	}
}

func TestFromFiles_RejectsDuplicatePaths(t *testing.T) {
	_, err := FromFiles([]File{
		{Path: SkillMDPath, Content: []byte("---\nname: A\ndescription: d\n---\n")},
		{Path: "a.txt", Content: []byte("1")},
		{Path: "a.txt", Content: []byte("2")},
	})
	if err == nil {
		t.Fatal("expected error for duplicate path")
	}
}

func TestNormalizePath(t *testing.T) {
	valid := []string{"a.txt", "sub/dir/file.py", "a-b_c.md"}
	for _, p := range valid {
		if _, err := NormalizePath(p); err != nil {
			t.Errorf("NormalizePath(%q): unexpected error %v", p, err)
		}
	}

	invalid := []string{"", "/abs/path", "../escape.txt", "a/../b.txt", "a\\b.txt", "."}
	for _, p := range invalid {
		if _, err := NormalizePath(p); err == nil {
			t.Errorf("NormalizePath(%q): expected error", p)
		}
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Weather Lookup":  "weather-lookup",
		"  Multi   Space": "multi-space",
		"Already-slug":    "already-slug",
		"Émoji 🎉 Name":    "moji-name",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
