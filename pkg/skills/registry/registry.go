// Package registry is the filesystem catalog of installed skill bundles.
// It only stores bundles and their catalog metadata; validation verdicts
// live in pkg/skills/validation.Store, coordinated by the caller (the
// skills activation graph node).
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"souz.ru/souz-go/pkg/skills/bundle"
)

const storedSkillFile = "stored-skill.json"

// StoredSkill is the lightweight catalog entry for one skill — enough to
// list and pick from, without loading its full bundle content.
type StoredSkill struct {
	SkillID    string          `json:"skillId"`
	Manifest   bundle.Manifest `json:"manifest"`
	BundleHash string          `json:"bundleHash"`
	CreatedAt  time.Time       `json:"createdAt"`
	// Loose is true for a bundle discovered as a bare directory containing
	// SKILL.md, installed by dropping it in rather than via SaveSkillBundle.
	// Not persisted — recomputed on every list/get.
	Loose bool `json:"-"`
}

// Registry is a filesystem-backed skill catalog rooted at dir (the
// souz-go state layout's ~/.local/state/souz-go/skills).
type Registry struct {
	root   string
	policy bundle.Policy
}

// New wraps root (created lazily on first install) using policy to load
// bundles (both managed and loose).
func New(root string, policy bundle.Policy) *Registry {
	return &Registry{root: root, policy: policy}
}

// ListSkills returns every installed and loose skill under the registry
// root. Entries that fail to load (corrupt metadata, an invalid loose
// bundle) are skipped rather than failing the whole listing.
func (r *Registry) ListSkills() ([]StoredSkill, error) {
	entries, err := os.ReadDir(r.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("skill registry: %w", err)
	}

	var out []StoredSkill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if stored, ok := r.readEntry(filepath.Join(r.root, e.Name())); ok {
			out = append(out, stored)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SkillID < out[j].SkillID })
	return out, nil
}

// readEntry loads one skillID directory as either a managed (stored-skill.json)
// or loose (bare SKILL.md) skill.
func (r *Registry) readEntry(dir string) (StoredSkill, bool) {
	if data, err := os.ReadFile(filepath.Join(dir, storedSkillFile)); err == nil {
		var stored StoredSkill
		if err := json.Unmarshal(data, &stored); err == nil {
			return stored, true
		}
		return StoredSkill{}, false
	}

	if _, err := os.Stat(filepath.Join(dir, bundle.SkillMDPath)); err != nil {
		return StoredSkill{}, false
	}
	b, err := bundle.Load(dir, r.policy)
	if err != nil {
		return StoredSkill{}, false
	}
	return StoredSkill{
		SkillID:    b.SkillID,
		Manifest:   b.Manifest,
		BundleHash: b.Hash(),
		Loose:      true,
	}, true
}

// GetSkill looks up one skill by id.
func (r *Registry) GetSkill(skillID string) (*StoredSkill, error) {
	stored, ok := r.readEntry(filepath.Join(r.root, skillID))
	if !ok {
		return nil, nil
	}
	return &stored, nil
}

// GetSkillByName looks up one skill by its manifest display name (not its id).
func (r *Registry) GetSkillByName(name string) (*StoredSkill, error) {
	skills, err := r.ListSkills()
	if err != nil {
		return nil, err
	}
	for i := range skills {
		if skills[i].Manifest.Name == name {
			return &skills[i], nil
		}
	}
	return nil, nil
}

// SaveSkillBundle installs b: writes its files content-addressed under
// {skillId}/bundles/{bundleHash}/ (write-once — a second save of the same
// hash is a no-op) and refreshes {skillId}/stored-skill.json.
func (r *Registry) SaveSkillBundle(b *bundle.SkillBundle) (StoredSkill, error) {
	hash := b.Hash()
	dir := filepath.Join(r.root, b.SkillID)
	bundleDir := filepath.Join(dir, "bundles", hash)

	if _, err := os.Stat(bundleDir); errors.Is(err, os.ErrNotExist) {
		if err := writeBundleAtomic(bundleDir, b); err != nil {
			return StoredSkill{}, fmt.Errorf("skill registry: save bundle: %w", err)
		}
	} else if err != nil {
		return StoredSkill{}, fmt.Errorf("skill registry: %w", err)
	}

	stored := StoredSkill{SkillID: b.SkillID, Manifest: b.Manifest, BundleHash: hash, CreatedAt: time.Now().UTC()}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return StoredSkill{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return StoredSkill{}, fmt.Errorf("skill registry: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, storedSkillFile), data); err != nil {
		return StoredSkill{}, fmt.Errorf("skill registry: %w", err)
	}
	return stored, nil
}

// LoadSkillBundle reads back the full bundle (manifest + all files) for a
// skill previously seen via ListSkills/GetSkill, verifying the loaded
// content still matches bundleHash.
func (r *Registry) LoadSkillBundle(skillID, bundleHash string) (*bundle.SkillBundle, error) {
	root, err := r.BundleRoot(skillID, bundleHash)
	if err != nil {
		return nil, err
	}
	b, err := bundle.Load(root, r.policy)
	if err != nil {
		return nil, fmt.Errorf("skill registry: %w", err)
	}
	if b.Hash() != bundleHash {
		return nil, fmt.Errorf("skill registry: skill %q content changed on disk (hash mismatch)", skillID)
	}
	return b, nil
}

// BundleRoot resolves the on-disk directory containing a skill's files —
// the content-addressed bundles/{bundleHash} dir for a managed skill, or
// the skill's own directory for a loose one. Tools that need a real working
// directory (RunSkillCommand) use this instead of LoadSkillBundle.
func (r *Registry) BundleRoot(skillID, bundleHash string) (string, error) {
	dir := filepath.Join(r.root, skillID)
	bundleDir := filepath.Join(dir, "bundles", bundleHash)
	if _, err := os.Stat(bundleDir); err == nil {
		return bundleDir, nil
	}
	if _, err := os.Stat(filepath.Join(dir, bundle.SkillMDPath)); err == nil {
		return dir, nil
	}
	return "", fmt.Errorf("skill registry: skill %q bundle %q not found", skillID, bundleHash)
}

func writeBundleAtomic(dest string, b *bundle.SkillBundle) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	for _, f := range b.Files {
		p := filepath.Join(tmp, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, f.Content, 0o644); err != nil {
			return err
		}
	}

	if err := os.Rename(tmp, dest); err != nil {
		if _, statErr := os.Stat(dest); statErr == nil {
			// Another install of the same content-addressed bundle won the
			// race; that's fine, the content is identical by construction.
			return nil
		}
		return err
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
