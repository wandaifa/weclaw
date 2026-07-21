package messaging

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/store"
)

func withTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "weclaw.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	SetMessageStore(s)
	t.Cleanup(func() {
		SetMessageStore(nil)
		s.Close()
	})
	return s
}

func TestSendTextReplyPersistsAgentReply(t *testing.T) {
	srv, _ := newFakeIlinkServer(t)
	defer srv.Close()
	s := withTestStore(t)

	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	if err := SendTextReply(context.Background(), client, "user1@im.wechat", "hello from codex", "", "", ReplyMeta{Agent: "codex", ElapsedMS: 1500}); err != nil {
		t.Fatalf("SendTextReply: %v", err)
	}

	got, err := s.RecentMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(got) != 1 || got[0].Role != "bot" || got[0].Text != "hello from codex" {
		t.Fatalf("got %+v, want one bot message with the full reply text", got)
	}
}

func TestSendTextReplyWithoutMetaPersistsAsSystemReply(t *testing.T) {
	srv, _ := newFakeIlinkServer(t)
	defer srv.Close()
	s := withTestStore(t)

	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	// No ReplyMeta passed — matches every existing busy/quota/help call site.
	if err := SendTextReply(context.Background(), client, "user1@im.wechat", "busy notice", "", ""); err != nil {
		t.Fatalf("SendTextReply: %v", err)
	}

	got, err := s.RecentMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(got) != 1 || got[0].Text != "busy notice" {
		t.Fatalf("got %+v, want the system reply persisted too", got)
	}
}

func TestPersistInboundTextStoresFullMessage(t *testing.T) {
	s := withTestStore(t)

	msg := ilink.WeixinMessage{FromUserID: "user1@im.wechat", ContextToken: "ctx-1"}
	persistInboundText("bot1@im.bot", msg, "a fairly long incoming message that must not be truncated at all")

	got, err := s.RecentMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(got) != 1 || got[0].Role != "user" || got[0].Text != "a fairly long incoming message that must not be truncated at all" {
		t.Fatalf("got %+v, want the full inbound text persisted", got)
	}
}

func TestPersistenceIsNoopWithoutStore(t *testing.T) {
	SetMessageStore(nil)
	// Must not panic when no store has been configured.
	persistInboundText("bot1@im.bot", ilink.WeixinMessage{FromUserID: "user1@im.wechat"}, "hello")
	persistMessage(store.MessageRecord{Time: time.Now(), Role: "bot", Text: "hi"})
}
