package messaging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
)

// newTypingCountingServer is like newFakeIlinkServer but also counts
// sendtyping hits, so tests can assert the typing indicator was actually
// refreshed (not just sent once).
func newTypingCountingServer(t *testing.T) (*httptest.Server, *int32, *sentReplies) {
	t.Helper()
	var typingCount int32
	sent := &sentReplies{}
	mux := http.NewServeMux()
	mux.HandleFunc("/ilink/bot/getconfig", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"ret": 0, "typing_ticket": "tk"})
	})
	mux.HandleFunc("/ilink/bot/sendtyping", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&typingCount, 1)
		json.NewEncoder(w).Encode(map[string]interface{}{"ret": 0})
	})
	mux.HandleFunc("/ilink/bot/sendmessage", func(w http.ResponseWriter, r *http.Request) {
		var req ilink.SendMessageRequest
		json.NewDecoder(r.Body).Decode(&req)
		text := ""
		if len(req.Msg.ItemList) > 0 && req.Msg.ItemList[0].TextItem != nil {
			text = req.Msg.ItemList[0].TextItem.Text
		}
		sent.add(text)
		json.NewEncoder(w).Encode(map[string]interface{}{"ret": 0})
	})
	return httptest.NewServer(mux), &typingCount, sent
}

func TestWithTypingRefreshEveryRefreshesTypingPeriodically(t *testing.T) {
	srv, typingCount, _ := newTypingCountingServer(t)
	defer srv.Close()
	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})

	withTypingRefreshEvery(context.Background(), client, "user-a", "", 20*time.Millisecond, stillWorkingReply(), func() {
		time.Sleep(90 * time.Millisecond) // long enough for ~4 refresh ticks at 20ms
	})

	if got := atomic.LoadInt32(typingCount); got < 2 {
		t.Fatalf("typing refresh count = %d, want at least 2 for a call spanning multiple refresh intervals", got)
	}
}

func TestWithTypingRefreshEverySendsNoticeBeforeWork(t *testing.T) {
	srv, _, sent := newTypingCountingServer(t)
	defer srv.Close()
	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})

	withTypingRefreshEvery(context.Background(), client, "user-a", "", time.Hour, stillWorkingReply(), func() {
		for _, text := range sent.snapshot() {
			if text == stillWorkingReply() {
				return
			}
		}
		t.Fatal("processing notice must be sent before work starts")
	})
}

func TestWithTypingRefreshEveryUsesGivenNudgeText(t *testing.T) {
	srv, _, sent := newTypingCountingServer(t)
	defer srv.Close()
	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})

	withTypingRefreshEvery(context.Background(), client, "user-a", "", time.Hour, imageGenerationStillWorkingReply(), func() {
		for _, text := range sent.snapshot() {
			if text == imageGenerationStillWorkingReply() {
				return
			}
		}
		t.Fatal("specific processing notice must be sent before work starts")
	})
}

func TestWithTypingRefreshEverySendsOneNoticeWhenWorkReturnsQuickly(t *testing.T) {
	srv, typingCount, sent := newTypingCountingServer(t)
	defer srv.Close()
	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})

	withTypingRefreshEvery(context.Background(), client, "user-a", "", time.Hour, stillWorkingReply(), func() {})

	// Give the background goroutine a moment to (not) fire before asserting.
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(typingCount); got != 0 {
		t.Fatalf("typing refresh count = %d, want 0 when work() returns before the first interval elapses", got)
	}
	if replies := sent.snapshot(); len(replies) != 1 || replies[0] != stillWorkingReply() {
		t.Fatalf("sent replies = %v, want one immediate processing notice", replies)
	}
}

func TestWithTypingRefreshEveryNilClientDoesNotPanic(t *testing.T) {
	ran := false
	withTypingRefreshEvery(context.Background(), nil, "user-a", "", 5*time.Millisecond, stillWorkingReply(), func() {
		time.Sleep(30 * time.Millisecond)
		ran = true
	})
	if !ran {
		t.Fatal("work() should still run to completion with a nil client")
	}
}
