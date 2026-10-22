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

func TestLatestInboundContextTokenReturnsNewestMatchingToken(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, rec := range []MessageRecord{
		{Time: time.Now(), Role: "user", BotID: "bot-a", UserID: "owner", Text: "first", ContextToken: "token-1"},
		{Time: time.Now(), Role: "bot", BotID: "bot-a", UserID: "owner", Text: "reply"},
		{Time: time.Now(), Role: "user", BotID: "bot-b", UserID: "owner", Text: "other bot", ContextToken: "wrong-bot"},
		{Time: time.Now(), Role: "user", BotID: "bot-a", UserID: "other", Text: "other user", ContextToken: "wrong-user"},
		{Time: time.Now(), Role: "user", BotID: "bot-a", UserID: "owner", Text: "newest", ContextToken: "token-2"},
	} {
		if err := s.InsertMessage(ctx, rec); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
	}

	got, err := s.LatestInboundContextToken(ctx, "bot-a", "owner")
	if err != nil {
		t.Fatalf("LatestInboundContextToken: %v", err)
	}
	if got != "token-2" {
		t.Fatalf("token = %q, want token-2", got)
	}
}

func TestLatestOutboundMessageReturnsNewestMatchingBotMessage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	latest := time.Date(2026, 8, 26, 8, 30, 0, 0, time.UTC)
	for _, rec := range []MessageRecord{
		{Time: latest.Add(-time.Minute), Role: "bot", BotID: "bot-a", UserID: "owner", Text: "older"},
		{Time: latest, Role: "bot", BotID: "bot-a", UserID: "owner", Text: "latest"},
		{Time: latest.Add(time.Minute), Role: "bot", BotID: "bot-b", UserID: "owner", Text: "wrong bot"},
		{Time: latest.Add(2 * time.Minute), Role: "bot", BotID: "bot-a", UserID: "other", Text: "wrong user"},
	} {
		if err := s.InsertMessage(ctx, rec); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
	}

	got, err := s.LatestOutboundMessage(ctx, "bot-a", "owner")
	if err != nil {
		t.Fatalf("LatestOutboundMessage: %v", err)
	}
	if got == nil || got.Text != "latest" || !got.Time.Equal(latest) {
		t.Fatalf("message = %#v, want latest bot-a/owner row", got)
	}
}
