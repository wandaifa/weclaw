package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "weclaw.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesSchema(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.RecentMessages(context.Background(), 10); err != nil {
		t.Fatalf("querying a freshly created store should not error: %v", err)
	}
}

func TestInsertAndReadBackMessage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	rec := MessageRecord{
		Time:   time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC),
		Role:   "user",
		BotID:  "bot1@im.bot",
		UserID: "user1@im.wechat",
		Text:   "hello there, this is a full untruncated message",
	}
	if err := s.InsertMessage(ctx, rec); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	got, err := s.RecentMessages(ctx, 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].Role != "user" || got[0].Text != rec.Text {
		t.Fatalf("got %+v, want role=user text=%q", got[0], rec.Text)
	}
}

func TestRecentMessagesOrdersNewestFirst(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for i, text := range []string{"first", "second", "third"} {
		rec := MessageRecord{
			Time:   time.Now().Add(time.Duration(i) * time.Second),
			Role:   "user",
			BotID:  "bot1@im.bot",
			UserID: "user1@im.wechat",
			Text:   text,
		}
		if err := s.InsertMessage(ctx, rec); err != nil {
			t.Fatalf("InsertMessage %q: %v", text, err)
		}
	}

	got, err := s.RecentMessages(ctx, 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(got) != 3 || got[0].Text != "third" || got[2].Text != "first" {
		t.Fatalf("got %+v, want newest-first [third second first]", got)
	}
}

func TestInsertMessagePreservesFullUntruncatedText(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	long := ""
	for i := 0; i < 500; i++ {
		long += "x"
	}
	if err := s.InsertMessage(ctx, MessageRecord{Time: time.Now(), Role: "bot", Text: long}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	got, err := s.RecentMessages(ctx, 1)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(got[0].Text) != 500 {
		t.Fatalf("stored text length = %d, want 500 (full text, not truncated)", len(got[0].Text))
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weclaw.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s1.InsertMessage(context.Background(), MessageRecord{Time: time.Now(), Role: "user", Text: "hi"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open (existing db): %v", err)
	}
	defer s2.Close()
	got, err := s2.RecentMessages(context.Background(), 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("reopening should preserve existing rows, got %d", len(got))
	}
}
