// Package files implements filesystem tools (read, list, write, search),
// all confined to a single sandbox root — see sandbox.go.
package files

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/tools"
)

const (
	maxReadChars       = 25_000
	maxListEntries     = 1_000
	maxSearchResults   = 200
	defaultMaxResults  = 50
	maxSearchFileBytes = 1 << 20 // 1MB; larger files are skipped, not truncated
	binarySniffBytes   = 8_000
)

// New builds the filesystem tool set, confined to root. root need not exist
// yet (it will be created lazily by WriteFile), but every path the tools
// touch — including symlink targets — must resolve inside it.
func New(root string) ([]tools.Tool, error) {
	sb, err := newSandbox(root)
	if err != nil {
		return nil, err
	}
	return []tools.Tool{
		&ReadFile{sb: sb},
		&ListFiles{sb: sb},
		&WriteFile{sb: sb},
		&SearchFiles{sb: sb},
	}, nil
}

// --- ReadFile ---

type ReadFile struct{ sb *sandbox }

var _ tools.Tool = (*ReadFile)(nil)

func (t *ReadFile) Name() string        { return "read_file" }
func (t *ReadFile) Description() string { return "Reads a UTF-8 text file and returns its content." }
func (t *ReadFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"path": {"type": "string", "description": "File path, relative to the sandbox root"}},
		"required": ["path"]
	}`)
}

func (t *ReadFile) Execute(_ context.Context, args map[string]json.RawMessage, _ agent.InvocationMeta) (string, error) {
	path, err := tools.ArgString(args, "path", "")
	if err != nil {
		return "", err
	}
	abs, err := t.sb.resolve(path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("read %q: is a directory", path)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", path, err)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("read %q: not a UTF-8 text file", path)
	}

	text := string(data)
	if truncated, ok := truncateRunes(text, maxReadChars); ok {
		return truncated + fmt.Sprintf("\n\n[truncated: file exceeds %d characters]", maxReadChars), nil
	}
	return text, nil
}

// --- ListFiles ---

type ListFiles struct{ sb *sandbox }

var _ tools.Tool = (*ListFiles)(nil)

func (t *ListFiles) Name() string { return "list_files" }
func (t *ListFiles) Description() string {
	return "Lists files and directories under a path, recursively. Directories are suffixed with \"/\"."
}
func (t *ListFiles) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Directory path, relative to the sandbox root; defaults to \".\""},
			"depth": {"type": "integer", "description": "Max recursion depth; omit or 0 for unlimited"}
		}
	}`)
}

func (t *ListFiles) Execute(_ context.Context, args map[string]json.RawMessage, _ agent.InvocationMeta) (string, error) {
	path, err := tools.ArgString(args, "path", ".")
	if err != nil {
		return "", err
	}
	depth, err := tools.ArgInt(args, "depth", 0)
	if err != nil {
		return "", err
	}

	abs, err := t.sb.resolve(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("list %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("list %q: not a directory", path)
	}

	var entries []string
	truncated := false
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == abs {
			return nil
		}
		if depth > 0 && relDepth(abs, p) > depth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(entries) >= maxListEntries {
			truncated = true
			return filepath.SkipAll
		}
		rel := t.sb.relative(p)
		if d.IsDir() {
			rel += "/"
		}
		entries = append(entries, rel)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("list %q: %w", path, err)
	}

	out, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	if truncated {
		return string(out) + fmt.Sprintf("\n[truncated: more than %d entries]", maxListEntries), nil
	}
	return string(out), nil
}

func relDepth(root, p string) int {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

// --- WriteFile ---

type WriteFile struct{ sb *sandbox }

var _ tools.Tool = (*WriteFile)(nil)

func (t *WriteFile) Name() string { return "write_file" }
func (t *WriteFile) Description() string {
	return "Writes a UTF-8 text file, creating parent directories as needed. " +
		"Fails if the file already exists unless overwrite is true."
}
func (t *WriteFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "File path, relative to the sandbox root"},
			"content": {"type": "string", "description": "Text content to write"},
			"overwrite": {"type": "boolean", "description": "Replace the file if it already exists; defaults to false"}
		},
		"required": ["path", "content"]
	}`)
}

func (t *WriteFile) Execute(_ context.Context, args map[string]json.RawMessage, _ agent.InvocationMeta) (string, error) {
	path, err := tools.ArgString(args, "path", "")
	if err != nil {
		return "", err
	}
	content, err := tools.ArgString(args, "content", "")
	if err != nil {
		return "", err
	}
	overwrite, err := tools.ArgBool(args, "overwrite", false)
	if err != nil {
		return "", err
	}

	abs, err := t.sb.resolve(path)
	if err != nil {
		return "", err
	}
	if !overwrite {
		if _, err := os.Stat(abs); err == nil {
			return "", fmt.Errorf("write %q: already exists (pass overwrite=true to replace)", path)
		}
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("write %q: %w", path, err)
	}
	if err := writeAtomic(abs, []byte(content)); err != nil {
		return "", fmt.Errorf("write %q: %w", path, err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// --- SearchFiles ---

type SearchFiles struct{ sb *sandbox }

var _ tools.Tool = (*SearchFiles)(nil)

func (t *SearchFiles) Name() string { return "search_files" }
func (t *SearchFiles) Description() string {
	return "Recursively searches text files under a path for a case-insensitive substring, " +
		"returning matching lines. Files over 1MB or that look binary are skipped."
}
func (t *SearchFiles) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Directory path, relative to the sandbox root; defaults to \".\""},
			"query": {"type": "string", "description": "Substring to search for"},
			"max_results": {"type": "integer", "description": "Cap on matches returned; defaults to 50, max 200"}
		},
		"required": ["query"]
	}`)
}

type searchHit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func (t *SearchFiles) Execute(_ context.Context, args map[string]json.RawMessage, _ agent.InvocationMeta) (string, error) {
	path, err := tools.ArgString(args, "path", ".")
	if err != nil {
		return "", err
	}
	query, err := tools.ArgString(args, "query", "")
	if err != nil {
		return "", err
	}
	if query == "" {
		return "", fmt.Errorf("query must not be empty")
	}
	maxResults, err := tools.ArgInt(args, "max_results", defaultMaxResults)
	if err != nil {
		return "", err
	}
	if maxResults <= 0 || maxResults > maxSearchResults {
		maxResults = maxSearchResults
	}

	abs, err := t.sb.resolve(path)
	if err != nil {
		return "", err
	}
	needle := strings.ToLower(query)

	var hits []searchHit
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if len(hits) >= maxResults {
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxSearchFileBytes {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil || looksBinary(data) {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if len(hits) >= maxResults {
				break
			}
			if strings.Contains(strings.ToLower(line), needle) {
				hits = append(hits, searchHit{Path: t.sb.relative(p), Line: i + 1, Text: strings.TrimSpace(line)})
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("search %q: %w", path, err)
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Path < hits[j].Path })
	out, err := json.Marshal(hits)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func looksBinary(data []byte) bool {
	n := len(data)
	if n > binarySniffBytes {
		n = binarySniffBytes
	}
	return bytes.IndexByte(data[:n], 0) != -1
}

func truncateRunes(s string, max int) (string, bool) {
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	runes := []rune(s)
	return string(runes[:max]), true
}
