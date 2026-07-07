package files

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sandbox confines all path resolution to a single root directory, refusing
// any path — via "..", an absolute path, or a symlink — that would resolve
// outside of it. This is a security boundary, not a desktop-UI convenience,
// so it is kept even though the Kotlin original's permission-broker UI flow
// (staged edits, trash-instead-of-delete) is not ported.
type sandbox struct {
	root string // absolute, symlink-resolved (to the extent it exists)
}

func newSandbox(root string) (*sandbox, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox root: %w", err)
	}
	resolved, err := effectivePath(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox root: %w", err)
	}
	return &sandbox{root: resolved}, nil
}

// resolve turns a user-supplied path (relative to the sandbox root, or
// absolute) into a safe absolute path confined to the root. It returns an
// error if the effective path — after resolving symlinks and ".." — would
// escape the root.
func (s *sandbox) resolve(userPath string) (string, error) {
	if userPath == "" {
		userPath = "."
	}

	var candidate string
	if filepath.IsAbs(userPath) {
		candidate = filepath.Clean(userPath)
	} else {
		candidate = filepath.Join(s.root, userPath)
	}

	effective, err := effectivePath(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if effective != s.root && !strings.HasPrefix(effective, s.root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the sandbox root", userPath)
	}
	return effective, nil
}

// relative renders an absolute in-sandbox path relative to the root, for
// display back to the LLM (which only ever sees sandbox-relative paths).
func (s *sandbox) relative(absPath string) string {
	rel, err := filepath.Rel(s.root, absPath)
	if err != nil {
		return absPath
	}
	return rel
}

// effectivePath resolves symlinks in p. If p does not exist yet, it walks up
// to the nearest existing ancestor, resolves that, and rejoins the
// not-yet-existing suffix — so a symlink hidden behind a not-yet-created
// path component can't be used to escape the sandbox.
func effectivePath(p string) (string, error) {
	resolved, err := filepath.EvalSymlinks(p)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p, nil
	}
	resolvedParent, err := effectivePath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(p)), nil
}
