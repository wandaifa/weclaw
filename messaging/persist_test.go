package messaging

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
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

// TestHandleMessageWaitForIdleWaitsForPersistToComplete is a regression test
// for a real data race (found via `go test -race`): HandleMessage dispatches
// onto an async per-conversation queue (enqueueChat/runChatQueue) and
// returns immediately, well before the queued job's SendTextReply call (and
// therefore persistMessage) has actually run. Tests that used to poll for an
// early, OBSERVABLE side effect (e.g. a fake ilink server recording the sent
// text) could see that condition become true and return before persistMessage
// -- which runs after the network call inside the SAME goroutine -- had
// actually executed. If some OTHER test's withTestStore() then reassigned
// the global messageStore while that straggler goroutine was still running,
// -race caught concurrent read/write. waitForIdle() drains the actual
// runChatQueue goroutine (via Handler.chatWG), so no polling/guessing is
// needed: immediately after it returns, every enqueued job -- including its
// persistMessage call -- is guaranteed complete.
func TestHandleMessageWaitForIdleWaitsForPersistToComplete(t *testing.T) {
	srv, _ := newFakeIlinkServer(t)
	defer srv.Close()
	s := withTestStore(t)

	h := NewHandler(nil, nil)
	fake := &recordingAgent{}
	h.SetDefaultAgent("codex", fake)

	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	ownerID := config.OwnerUserIDs()[0]
	msg := newTestMessage(ownerID, "hello", 1)
	h.HandleMessage(context.Background(), client, msg)
	h.waitForIdle()

	got, err := s.RecentMessages(context.Background(), 2)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages immediately after waitForIdle, want 2 (the user message and the agent's reply) with no polling needed: %+v", len(got), got)
	}
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
