// Package storage is the atomic JSON session store: one file per chat under
// ~/.local/state/souz-go/sessions/, holding both the resumable LLM
// conversation history and the UI-facing message list.
//
// The Kotlin original splits this across a directory of seven files per
// chat (chat.json, messages.jsonl, agent-state.json, executions.jsonl,
// options.jsonl, events.jsonl, tool-calls.jsonl) to support append-only
// logs and independent optimistic-concurrency versioning — needed for a
// multi-process backend server. souz-go is a single process with no
// concurrent writers to race against, so collapsing that into one
// whole-file-rewrite-per-turn JSON document (per CLAUDE.md's documented
// state layout) loses nothing that matters here.
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"souz.ru/souz-go/pkg/providers"
)

// Message is one UI-facing chat message (distinct from providers.Message,
// which is the LLM-shaped conversation history — a tool-call round trip is
// several providers.Message entries but usually zero extra Messages here).
type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

// ChatState is everything needed to resume and display one conversation.
type ChatState struct {
	ChatID    string    `json:"chatId"`
	Title     string    `json:"title,omitempty"`
	Archived  bool      `json:"archived"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// History is the resumable LLM conversation state — what gets fed back
	// into AgentContext.History on the next turn.
	History []providers.Message `json:"history"`
	// Messages is the UI-facing list (for a chat pane), independent of how
	// many providers.Message entries a turn produced internally.
	Messages []Message `json:"messages"`
}

var validChatID = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// Store is a filesystem-backed session store rooted at dir
// (~/.local/state/souz-go/sessions).
type Store struct {
	root string
}

// New wraps root, created lazily on first Save.
func New(root string) *Store {
	return &Store{root: root}
}

func (s *Store) path(chatID string) (string, error) {
	if !validChatID.MatchString(chatID) {
		return "", fmt.Errorf("storage: invalid chat id %q", chatID)
	}
	return filepath.Join(s.root, chatID+".json"), nil
}

// Get returns the stored state for chatID, or (nil, nil) if there is none yet.
func (s *Store) Get(chatID string) (*ChatState, error) {
	p, err := s.path(chatID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	var cs ChatState
	if err := json.Unmarshal(data, &cs); err != nil {
		return nil, fmt.Errorf("storage: decode %s: %w", chatID, err)
	}
	return &cs, nil
}

// Save writes cs atomically, setting UpdatedAt (and CreatedAt, if unset) to now.
func (s *Store) Save(cs *ChatState) error {
	p, err := s.path(cs.ChatID)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	if cs.CreatedAt.IsZero() {
		cs.CreatedAt = now
	}
	cs.UpdatedAt = now

	data, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		return fmt.Errorf("storage: encode %s: %w", cs.ChatID, err)
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	if err := writeFileAtomic(p, data); err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	return nil
}

// List returns every stored chat, most recently updated first. Entries that
// fail to parse are skipped rather than failing the whole listing.
func (s *Store) List() ([]*ChatState, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}

	var out []*ChatState
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, e.Name()))
		if err != nil {
			continue
		}
		var cs ChatState
		if err := json.Unmarshal(data, &cs); err != nil {
			continue
		}
		out = append(out, &cs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// Delete removes a chat's stored state. Deleting a chat that doesn't exist is a no-op.
func (s *Store) Delete(chatID string) error {
	p, err := s.path(chatID)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("storage: %w", err)
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
