package storage

import (
	"path/filepath"
	"testing"
	"time"

	"souz.ru/souz-go/pkg/providers"
)

func TestSaveAndGet_RoundTrips(t *testing.T) {
	s := New(t.TempDir())
	cs := &ChatState{
		ChatID: "telegram:123",
		Title:  "Test chat",
		History: []providers.Message{
			{Role: providers.RoleUser, Content: "hi"},
			{Role: providers.RoleAssistant, Content: "hello"},
		},
		Messages: []Message{
			{ID: "m1", Role: "user", Content: "hi", CreatedAt: time.Now()},
		},
	}

	if err := s.Save(cs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Get("telegram:123")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected a stored chat, got nil")
	}
	if got.Title != "Test chat" || len(got.History) != 2 || len(got.Messages) != 1 {
		t.Errorf("unexpected round trip: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("expected timestamps set, got %+v", got)
	}
}

func TestGet_MissingChatReturnsNilNil(t *testing.T) {
	s := New(t.TempDir())
	got, err := s.Get("does-not-exist")
	if err != nil || got != nil {
		t.Fatalf("expected nil, nil for a missing chat, got %+v, %v", got, err)
	}
}

func TestGet_RejectsUnsafeChatID(t *testing.T) {
	s := New(t.TempDir())
	cases := []string{"../escape", "a/b", "", "with spaces"}
	for _, id := range cases {
		if _, err := s.Get(id); err == nil {
			t.Errorf("Get(%q): expected error for unsafe chat id", id)
		}
	}
}

func TestSave_RejectsUnsafeChatID(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(&ChatState{ChatID: "../escape"}); err == nil {
		t.Fatal("expected error for unsafe chat id")
	}
}

func TestList_SortsByUpdatedAtDescending(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(&ChatState{ChatID: "first"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := s.Save(&ChatState{ChatID: "second"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].ChatID != "second" || list[1].ChatID != "first" {
		t.Fatalf("unexpected order: %+v", list)
	}
}

func TestList_EmptyDirDoesNotError(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "does-not-exist-yet"))
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %+v", list)
	}
}

func TestDelete_RemovesChatAndIsIdempotent(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(&ChatState{ChatID: "gone-soon"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Delete("gone-soon"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := s.Get("gone-soon")
	if err != nil || got != nil {
		t.Fatalf("expected chat gone, got %+v, %v", got, err)
	}
	if err := s.Delete("gone-soon"); err != nil {
		t.Fatalf("Delete on already-deleted chat should be a no-op, got %v", err)
	}
}
