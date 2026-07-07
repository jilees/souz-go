package channels

import (
	"context"
	"testing"
	"time"

	"souz.ru/souz-go/pkg/bus"
)

func TestBaseChannel_IsAllowed(t *testing.T) {
	cases := []struct {
		name      string
		allowList []string
		sender    string
		want      bool
	}{
		{"empty allow-list permits everyone", nil, "anyone", true},
		{"explicit match", []string{"a", "b"}, "b", true},
		{"no match", []string{"a", "b"}, "c", false},
		{"wildcard", []string{"*"}, "anyone", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBaseChannel("test", bus.New(), tc.allowList)
			if got := b.IsAllowed(tc.sender); got != tc.want {
				t.Errorf("IsAllowed(%q) = %v, want %v", tc.sender, got, tc.want)
			}
		})
	}
}

func TestBaseChannel_HandleInbound_FiltersDisallowedSenders(t *testing.T) {
	mb := bus.New()
	defer mb.Close()
	b := NewBaseChannel("test", mb, []string{"allowed"})

	if err := b.HandleInbound(context.Background(), bus.InboundMessage{SenderID: "blocked", Text: "hi"}); err != nil {
		t.Fatalf("HandleInbound: %v", err)
	}
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected disallowed message to be dropped, got %+v", msg)
	case <-time.After(50 * time.Millisecond):
	}

	if err := b.HandleInbound(context.Background(), bus.InboundMessage{SenderID: "allowed", Text: "hi"}); err != nil {
		t.Fatalf("HandleInbound: %v", err)
	}
	select {
	case msg := <-mb.InboundChan():
		if msg.Text != "hi" {
			t.Errorf("unexpected message: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for allowed message")
	}
}

func TestBaseChannel_RunningState(t *testing.T) {
	b := NewBaseChannel("test", bus.New(), nil)
	if b.IsRunning() {
		t.Fatal("expected not running initially")
	}
	b.SetRunning(true)
	if !b.IsRunning() {
		t.Fatal("expected running after SetRunning(true)")
	}
}
