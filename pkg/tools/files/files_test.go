package files

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"souz.ru/souz-go/pkg/agent"
)

func newTestTools(t *testing.T) (root string, read *ReadFile, list *ListFiles, write *WriteFile, search *SearchFiles) {
	t.Helper()
	root = t.TempDir()
	all, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return root, all[0].(*ReadFile), all[1].(*ListFiles), all[2].(*WriteFile), all[3].(*SearchFiles)
}

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

func TestWriteThenReadFile(t *testing.T) {
	_, read, _, write, _ := newTestTools(t)
	ctx := context.Background()

	if _, err := write.Execute(ctx, rawArgs(t, map[string]any{"path": "notes/todo.txt", "content": "buy milk"}), agent.InvocationMeta{}); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := read.Execute(ctx, rawArgs(t, map[string]any{"path": "notes/todo.txt"}), agent.InvocationMeta{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "buy milk" {
		t.Errorf("read = %q, want %q", got, "buy milk")
	}
}

func TestWriteFile_RefusesOverwriteByDefault(t *testing.T) {
	_, _, _, write, _ := newTestTools(t)
	ctx := context.Background()
	args := rawArgs(t, map[string]any{"path": "a.txt", "content": "1"})

	if _, err := write.Execute(ctx, args, agent.InvocationMeta{}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := write.Execute(ctx, args, agent.InvocationMeta{}); err == nil {
		t.Fatal("expected error on second write without overwrite")
	}

	overwriteArgs := rawArgs(t, map[string]any{"path": "a.txt", "content": "2", "overwrite": true})
	if _, err := write.Execute(ctx, overwriteArgs, agent.InvocationMeta{}); err != nil {
		t.Fatalf("overwrite write: %v", err)
	}
}

func TestReadFile_RejectsPathEscapingRoot(t *testing.T) {
	_, read, _, _, _ := newTestTools(t)
	_, err := read.Execute(context.Background(), rawArgs(t, map[string]any{"path": "../../etc/passwd"}), agent.InvocationMeta{})
	if err == nil {
		t.Fatal("expected error escaping sandbox root")
	}
}

func TestReadFile_RejectsSymlinkEscapingRoot(t *testing.T) {
	root, read, _, _, _ := newTestTools(t)

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	_, err := read.Execute(context.Background(), rawArgs(t, map[string]any{"path": "link.txt"}), agent.InvocationMeta{})
	if err == nil {
		t.Fatal("expected error reading through a symlink that escapes the sandbox")
	}
}

func TestListFiles(t *testing.T) {
	root, _, list, write, _ := newTestTools(t)
	ctx := context.Background()

	for _, p := range []string{"a.txt", "sub/b.txt"} {
		if _, err := write.Execute(ctx, rawArgs(t, map[string]any{"path": p, "content": "x"}), agent.InvocationMeta{}); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	got, err := list.Execute(ctx, rawArgs(t, map[string]any{"path": "."}), agent.InvocationMeta{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var entries []string
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("unmarshal result: %v (%s)", err, got)
	}
	want := map[string]bool{"a.txt": true, "sub/": true, filepath.Join("sub", "b.txt"): true}
	if len(entries) != len(want) {
		t.Fatalf("entries = %v, want keys of %v", entries, want)
	}
	for _, e := range entries {
		if !want[e] {
			t.Errorf("unexpected entry %q", e)
		}
	}
	_ = root
}

func TestSearchFiles(t *testing.T) {
	_, _, _, write, search := newTestTools(t)
	ctx := context.Background()

	if _, err := write.Execute(ctx, rawArgs(t, map[string]any{"path": "log.txt", "content": "line one\nERROR: boom\nline three"}), agent.InvocationMeta{}); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := search.Execute(ctx, rawArgs(t, map[string]any{"path": ".", "query": "error"}), agent.InvocationMeta{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var hits []searchHit
	if err := json.Unmarshal([]byte(got), &hits); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, got)
	}
	if len(hits) != 1 || hits[0].Line != 2 || !strings.Contains(hits[0].Text, "boom") {
		t.Errorf("unexpected hits: %+v", hits)
	}
}

func TestSearchFiles_SkipsBinaryFiles(t *testing.T) {
	root, _, _, _, search := newTestTools(t)
	binPath := filepath.Join(root, "data.bin")
	if err := os.WriteFile(binPath, []byte("query\x00binary"), 0o644); err != nil {
		t.Fatalf("write binary file: %v", err)
	}

	got, err := search.Execute(context.Background(), rawArgs(t, map[string]any{"path": ".", "query": "query"}), agent.InvocationMeta{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var hits []searchHit
	if err := json.Unmarshal([]byte(got), &hits); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected binary file to be skipped, got %+v", hits)
	}
}

func TestReadFile_MissingFileReturnsError(t *testing.T) {
	_, read, _, _, _ := newTestTools(t)
	if _, err := read.Execute(context.Background(), rawArgs(t, map[string]any{"path": "missing.txt"}), agent.InvocationMeta{}); err == nil {
		t.Fatal("expected error for missing file")
	}
}
