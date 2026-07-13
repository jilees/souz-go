package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"souz.ru/souz-go/pkg/bus"
)

const testToken = "TEST:TOKEN"

func TestChannel_StartPublishesInboundMessages(t *testing.T) {
	var getUpdatesCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+testToken+"/getUpdates" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		n := getUpdatesCalls.Add(1)
		if n == 1 {
			fmt.Fprint(w, `{"ok":true,"result":[{"update_id":1,"message":{"message_id":10,"from":{"id":42},"chat":{"id":100},"text":"hello"}}]}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":[]}`)
	}))
	defer server.Close()

	mb := bus.New()
	defer mb.Close()

	ch := New(Config{Token: testToken, BaseURL: server.URL}, mb)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ch.Start(ctx) }()

	select {
	case msg := <-mb.InboundChan():
		if msg.ChatID != "100" || msg.SenderID != "42" || msg.Text != "hello" || msg.MessageID != "10" {
			t.Errorf("unexpected inbound message: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for inbound message")
	}

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected Start to return an error on cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Start to return")
	}
}

func TestChannel_StartRespectsAllowList(t *testing.T) {
	var getUpdatesCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := getUpdatesCalls.Add(1)
		if n == 1 {
			fmt.Fprint(w, `{"ok":true,"result":[{"update_id":1,"message":{"message_id":10,"from":{"id":42},"chat":{"id":100},"text":"hello"}}]}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":[]}`)
	}))
	defer server.Close()

	mb := bus.New()
	defer mb.Close()

	ch := New(Config{Token: testToken, BaseURL: server.URL, AllowFrom: []string{"999"}}, mb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ch.Start(ctx)

	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("expected disallowed sender to be filtered, got %+v", msg)
	case <-time.After(300 * time.Millisecond):
		// expected: nothing published
	}
}

func TestChannel_SendPostsExpectedForm(t *testing.T) {
	var gotValues url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+testToken+"/sendMessage" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotValues = r.PostForm
		fmt.Fprint(w, `{"ok":true,"result":{}}`)
	}))
	defer server.Close()

	ch := New(Config{Token: testToken, BaseURL: server.URL}, bus.New())
	err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "100", Text: "hi there", ReplyToMessageID: "10"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotValues.Get("chat_id") != "100" || gotValues.Get("text") != "hi there" || gotValues.Get("reply_to_message_id") != "10" {
		t.Errorf("unexpected form values: %v", gotValues)
	}
}

func TestChannel_SendReturnsErrorOnAPIFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":false,"description":"chat not found"}`)
	}))
	defer server.Close()

	ch := New(Config{Token: testToken, BaseURL: server.URL}, bus.New())
	err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "bad", Text: "hi"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestChannel_StartRequiresToken(t *testing.T) {
	ch := New(Config{}, bus.New())
	if err := ch.Start(context.Background()); err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestChannel_StartTypingSendsActionImmediatelyAndRepeats(t *testing.T) {
	old := typingRepeatInterval
	typingRepeatInterval = 20 * time.Millisecond
	defer func() { typingRepeatInterval = old }()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+testToken+"/sendChatAction" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.PostForm.Get("chat_id") != "100" || r.PostForm.Get("action") != "typing" {
			t.Errorf("unexpected form values: %v", r.PostForm)
		}
		calls.Add(1)
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}))
	defer server.Close()

	ch := New(Config{Token: testToken, BaseURL: server.URL}, bus.New())
	stop, err := ch.StartTyping(context.Background(), "100")
	if err != nil {
		t.Fatalf("StartTyping: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected immediate action, got %d calls", calls.Load())
	}

	time.Sleep(100 * time.Millisecond)
	stop()
	stop() // must be idempotent

	// Let any tick that was already in flight when stop() fired land,
	// so the next snapshot isn't racing an in-progress HTTP round trip.
	time.Sleep(100 * time.Millisecond)
	afterStop := calls.Load()
	if afterStop < 2 {
		t.Fatalf("expected repeated actions before stop, got %d calls", afterStop)
	}

	time.Sleep(100 * time.Millisecond)
	if calls.Load() != afterStop {
		t.Errorf("expected no further actions after stop, got %d calls (was %d)", calls.Load(), afterStop)
	}
}

func TestChannel_StartTypingReturnsErrorOnAPIFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":false,"description":"chat not found"}`)
	}))
	defer server.Close()

	ch := New(Config{Token: testToken, BaseURL: server.URL}, bus.New())
	if _, err := ch.StartTyping(context.Background(), "bad"); err == nil {
		t.Fatal("expected error")
	}
}
