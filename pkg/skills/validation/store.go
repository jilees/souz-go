package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Store persists validation Records under root as
// {skillId}/policies/{policyVersion}/{bundleHash}.json — matching
// souz-go's ~/.local/state/souz-go/skill-validations/ state layout.
// Single-user, so unlike the Kotlin original there's no per-user path
// segment.
type Store struct {
	root string
}

// NewStore wraps the given cache root directory (created lazily on Save).
func NewStore(root string) *Store {
	return &Store{root: root}
}

func (s *Store) recordPath(skillID string, policyVersion int, bundleHash string) string {
	return filepath.Join(s.root, skillID, "policies", strconv.Itoa(policyVersion), bundleHash+".json")
}

// Get returns the cached record, or (nil, nil) if none exists yet.
func (s *Store) Get(skillID string, policyVersion int, bundleHash string) (*Record, error) {
	data, err := os.ReadFile(s.recordPath(skillID, policyVersion, bundleHash))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("skill validation store: read: %w", err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("skill validation store: decode: %w", err)
	}
	return &rec, nil
}

// Save writes rec to the cache, creating parent directories as needed.
func (s *Store) Save(rec Record) error {
	p := s.recordPath(rec.SkillID, rec.PolicyVersion, rec.BundleHash)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("skill validation store: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("skill validation store: encode: %w", err)
	}
	if err := writeFileAtomic(p, data); err != nil {
		return fmt.Errorf("skill validation store: %w", err)
	}
	return nil
}

// InvalidateOthers marks any cached APPROVED record for skillID/policyVersion
// whose bundle hash differs from currentHash as STALE, so a superseded
// bundle version is re-validated on next use instead of being served from a
// hash that no longer matches what's on disk. Run this before consulting
// Get, so a stale APPROVED record is never mistaken for current.
func (s *Store) InvalidateOthers(skillID string, policyVersion int, currentHash string) error {
	dir := filepath.Join(s.root, skillID, "policies", strconv.Itoa(policyVersion))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("skill validation store: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		hash := strings.TrimSuffix(e.Name(), ".json")
		if hash == currentHash {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var rec Record
		if err := json.Unmarshal(data, &rec); err != nil || rec.Status != StatusApproved {
			continue
		}
		rec.Status = StatusStale
		if updated, err := json.MarshalIndent(rec, "", "  "); err == nil {
			_ = writeFileAtomic(p, updated)
		}
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
