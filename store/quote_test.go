package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSentChunkLookupIsScopedToBotAndUser(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "weclaw.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	if err := s.InsertSentChunk(ctx, "bot1", "alice", "9001", "第一段", time.Now()); err != nil {
		t.Fatal(err)
	}
	if text, ok, err := s.SentChunkText(ctx, "bot1", "alice", "9001"); err != nil || !ok || text != "第一段" {
		t.Fatalf("own chunk: text=%q ok=%v err=%v", text, ok, err)
	}
	for _, other := range [][2]string{{"bot1", "bob"}, {"bot2", "alice"}} {
		if _, ok, err := s.SentChunkText(ctx, other[0], other[1], "9001"); err != nil || ok {
			t.Fatalf("%v must not resolve alice's chunk (ok=%v err=%v)", other, ok, err)
		}
	}
	if err := s.InsertSentChunk(ctx, "bot1", "alice", "9001", "重发", time.Now()); err != nil {
		t.Fatalf("re-recording the same server ID must not fail: %v", err)
	}
	if text, _, _ := s.SentChunkText(ctx, "bot1", "alice", "9001"); text != "重发" {
		t.Fatalf("text = %q, want latest", text)
	}
}

func TestInboundMessageLookupIsScopedAndTextOnly(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "weclaw.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, rec := range []MessageRecord{
		{Time: time.Now(), Role: "user", BotID: "bot1", UserID: "alice", Text: "原话", MessageID: "100", MessageType: "text"},
		{Time: time.Now(), Role: "user", BotID: "bot1", UserID: "alice", Text: "photo.jpg", MessageID: "101", MessageType: "image"},
		{Time: time.Now(), Role: "bot", BotID: "bot1", UserID: "alice", Text: "回复", MessageID: "102"},
	} {
		if err := s.InsertMessage(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	if text, ok, err := s.InboundMessageText(ctx, "bot1", "alice", "100"); err != nil || !ok || text != "原话" {
		t.Fatalf("own message: text=%q ok=%v err=%v", text, ok, err)
	}
	for _, c := range [][3]string{{"bot1", "bob", "100"}, {"bot2", "alice", "100"}, {"bot1", "alice", "101"}, {"bot1", "alice", "102"}} {
		if _, ok, err := s.InboundMessageText(ctx, c[0], c[1], c[2]); err != nil || ok {
			t.Fatalf("%v must not resolve (ok=%v err=%v)", c, ok, err)
		}
	}
}
