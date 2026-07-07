// Package bundle parses and loads skill bundles: a directory containing a
// SKILL.md (YAML-ish frontmatter + markdown instructions) plus optional
// supporting files, in the spirit of Anthropic's Agent Skills SKILL.md
// convention. "author"/"version" are extensions beyond that spec; keep
// "name"/"description"/"metadata" semantics compatible if this format is
// ever read by other tooling.
package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SkillMDPath is the required manifest file name at the bundle root.
const SkillMDPath = "SKILL.md"

// File is one file within a bundle, path normalized relative to the bundle root.
type File struct {
	Path    string
	Content []byte
}

// SkillBundle is a fully loaded, validated-for-structure skill.
type SkillBundle struct {
	SkillID        string
	Manifest       Manifest
	Files          []File // sorted by Path; includes SKILL.md
	Body           string // SKILL.md markdown body, after the frontmatter
	RawFrontmatter string
}

// Policy bounds what a bundle may contain, enforced while loading from disk.
type Policy struct {
	MaxFiles       int
	MaxFileBytes   int64
	MaxBundleBytes int64
	AllowedExt     map[string]bool
}

// DefaultPolicy mirrors the original implementation's limits: small text-only
// bundles, no binaries, no room for smuggling large payloads.
func DefaultPolicy() Policy {
	return Policy{
		MaxFiles:       64,
		MaxFileBytes:   128 * 1024,
		MaxBundleBytes: 512 * 1024,
		AllowedExt: extSet(
			"cjs", "css", "csv", "html", "js", "json", "jsx", "md", "mjs",
			"py", "sh", "sql", "text", "toml", "ts", "tsx", "txt", "xml", "yaml", "yml", "zsh",
		),
	}
}

func extSet(exts ...string) map[string]bool {
	m := make(map[string]bool, len(exts))
	for _, e := range exts {
		m[e] = true
	}
	return m
}

// Load reads a bundle from a directory on disk, enforcing policy, and
// parses its SKILL.md. root must exist and be a directory.
func Load(root string, policy Policy) (*SkillBundle, error) {
	entries, err := collectFiles(root, policy)
	if err != nil {
		return nil, err
	}
	return FromFiles(entries)
}

func collectFiles(root string, policy Policy) ([]File, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("skill bundle: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skill bundle: %q is not a directory", root)
	}

	var files []File
	var totalBytes int64

	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill bundle: %q is a symlink, which is not allowed", relOrSelf(root, p))
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("skill bundle: %q is not a regular file", relOrSelf(root, p))
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		normalized, err := NormalizePath(rel)
		if err != nil {
			return fmt.Errorf("skill bundle: %w", err)
		}

		ext := strings.TrimPrefix(filepath.Ext(normalized), ".")
		if !policy.AllowedExt[strings.ToLower(ext)] {
			return fmt.Errorf("skill bundle: %q has a disallowed extension", normalized)
		}

		if len(files) >= policy.MaxFiles {
			return fmt.Errorf("skill bundle: exceeds the %d file limit", policy.MaxFiles)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > policy.MaxFileBytes {
			return fmt.Errorf("skill bundle: %q exceeds the %d byte per-file limit", normalized, policy.MaxFileBytes)
		}
		totalBytes += info.Size()
		if totalBytes > policy.MaxBundleBytes {
			return fmt.Errorf("skill bundle: exceeds the %d byte bundle size limit", policy.MaxBundleBytes)
		}

		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files = append(files, File{Path: normalized, Content: content})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func relOrSelf(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil {
		return rel
	}
	return p
}

// FromFiles builds a SkillBundle from an already-loaded, already-policy-checked
// file set (used directly by tests and by callers that source files from
// somewhere other than a plain directory walk, e.g. content-addressed storage).
func FromFiles(files []File) (*SkillBundle, error) {
	seen := make(map[string]bool, len(files))
	var skillMD *File
	for i := range files {
		f := files[i]
		if f.Path == "" {
			return nil, fmt.Errorf("skill bundle: empty file path")
		}
		if seen[f.Path] {
			return nil, fmt.Errorf("skill bundle: duplicate path %q", f.Path)
		}
		seen[f.Path] = true
		if f.Path == SkillMDPath {
			skillMD = &files[i]
		}
	}
	if skillMD == nil {
		return nil, fmt.Errorf("skill bundle: missing required %s", SkillMDPath)
	}

	manifest, raw, body, err := parseFrontmatter(string(skillMD.Content))
	if err != nil {
		return nil, fmt.Errorf("skill bundle: %w", err)
	}

	sorted := make([]File, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	return &SkillBundle{
		SkillID:        Slug(manifest.Name),
		Manifest:       manifest,
		Files:          sorted,
		Body:           body,
		RawFrontmatter: raw,
	}, nil
}

// Hash returns a deterministic content hash for the bundle: SHA-256 folded
// over "path\nsha256(content)\n" for each file in path-sorted order, so it
// is independent of filesystem iteration order and changes if any file's
// path or content changes.
func (b *SkillBundle) Hash() string {
	h := sha256.New()
	for _, f := range b.Files {
		fileDigest := sha256.Sum256(f.Content)
		fmt.Fprintf(h, "%s\n%s\n", f.Path, hex.EncodeToString(fileDigest[:]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

var invalidSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns a display name into a filesystem/URL-safe skill id.
func Slug(name string) string {
	s := invalidSlugChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	return strings.Trim(s, "-")
}

// NormalizePath validates and normalizes a bundle-relative file path:
// forward slashes, no "." or ".." segments, no absolute paths, no NUL bytes.
func NormalizePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("path %q contains a NUL byte", p)
	}
	clean := filepath.ToSlash(p)
	if strings.Contains(clean, "\\") {
		return "", fmt.Errorf("path %q contains a backslash", p)
	}
	if strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("path %q must be relative", p)
	}

	segments := strings.Split(clean, "/")
	for _, seg := range segments {
		switch seg {
		case "", ".", "..":
			return "", fmt.Errorf("path %q contains an invalid segment %q", p, seg)
		}
	}
	return strings.Join(segments, "/"), nil
}
