package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
)

func quotedMessage(userID, text string, messageID int64, refMsg string) ilink.WeixinMessage {
	msg := newTestMessage(userID, text, messageID)
	msg.ItemList[0].RefMsg = json.RawMessage(refMsg)
	return msg
}

// newIDIlinkServer acknowledges each sendmessage with an increasing server message_id
// encoded as a string, as newer iLink servers do.
func newIDIlinkServer(t *testing.T) *httptest.Server {
	t.Helper()
	var next int64 = 9000
	mux := http.NewServeMux()
	mux.HandleFunc("/ilink/bot/getconfig", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"ret": 0, "typing_ticket": "tk"})
	})
	mux.HandleFunc("/ilink/bot/sendtyping", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"ret": 0})
	})
	mux.HandleFunc("/ilink/bot/sendmessage", func(w http.ResponseWriter, r *http.Request) {
		id := atomic.AddInt64(&next, 1)
		json.NewEncoder(w).Encode(map[string]interface{}{"ret": 0, "message_id": fmt.Sprintf("%d", id)})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestExtractQuoteRef(t *testing.T) {
	cases := []struct {
		name, ref string
		want      *quoteRef
	}{
		{"empty shell", `{"message_item":{"type":0,"msg_id":"0","create_time_ms":1}}`, nil},
		{"msg_id only", `{"message_item":{"type":0,"msg_id":"7518900011053171976"}}`, &quoteRef{ID: "7518900011053171976"}},
		{"svr_id preferred", `{"svr_id":"11","message_item":{"msg_id":"22"}}`, &quoteRef{ID: "11"}},
		{"inline text", `{"title":"摘要","message_item":{"msg_id":"0","text_item":{"text":"原文"}}}`, &quoteRef{Text: "摘要 | 原文"}},
	}
	for _, c := range cases {
		got := extractQuoteRef(quotedMessage("u", "hi", 1, c.ref))
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Fatalf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
	if extractQuoteRef(newTestMessage("u", "hi", 1)) != nil {
		t.Fatal("message without ref_msg must have no quote")
	}
}

func TestQuotedBotReplyIsResolvedForAgent(t *testing.T) {
	withTestStore(t)
	srv := newIDIlinkServer(t)
	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})

	if err := SendTextReply(context.Background(), client, "user-a@im.wechat", "第一条：先备份。第二条：再迁移。", "", ""); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(nil, nil)
	fake := &recordingAgent{}
	h.SetDefaultAgent("codex-shared", fake)
	h.HandleMessage(context.Background(), client, quotedMessage("user-a@im.wechat", "第二条展开说说", 2, `{"message_item":{"msg_id":"9001"}}`))
	// A different user quoting the same server ID must not see user-a's message.
	h.HandleMessage(context.Background(), client, quotedMessage("user-b@im.wechat", "这是什么", 3, `{"message_item":{"msg_id":"9001"}}`))
	// An unknown ID falls back to the user's own words.
	h.HandleMessage(context.Background(), client, quotedMessage("user-c@im.wechat", "看看", 4, `{"message_item":{"msg_id":"424242"}}`))

	waitFor(t, 2*time.Second, func() bool { return len(fake.callsSnapshot()) == 3 })
	h.waitForIdle()
	got := map[string]bool{}
	for _, call := range fake.callsSnapshot() {
		got[call] = true
	}
	for _, want := range []string{
		"[引用: 第一条：先备份。第二条：再迁移。]\n第二条展开说说",
		"这是什么", // user-b: same ID, other user -> not resolved
		"看看",   // unknown ID -> user's own words
	} {
		if !got[want] {
			t.Fatalf("agent calls missing %q: %q", want, fake.callsSnapshot())
		}
	}
}

func TestQuotedTextIsTruncated(t *testing.T) {
	ctx := context.WithValue(context.Background(), quoteRefKey{}, &quoteRef{Text: strings.Repeat("长", maxQuoteRunes+50)})
	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot1@im.bot"})
	got := withQuotedContext(ctx, client, "u", "问")
	if want := "[引用: " + strings.Repeat("长", maxQuoteRunes) + "…]\n问"; got != want {
		t.Fatalf("unexpected truncation, len=%d", len([]rune(got)))
	}
	if withQuotedContext(context.Background(), client, "u", "问") != "问" {
		t.Fatal("message without quote must pass through unchanged")
	}
}

func TestQuotedMessageIsNotMerged(t *testing.T) {
	withTestStore(t)
	srv := newIDIlinkServer(t)
	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	if err := SendTextReply(context.Background(), client, "user-a@im.wechat", "方案原文", "", ""); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(nil, nil)
	if err := h.SetMergeSettings(MergeSettings{IdleDelay: time.Second, MaxWait: 2 * time.Second, MaxMessages: 10, MaxChars: 4000}); err != nil {
		t.Fatal(err)
	}
	fake := &recordingAgent{}
	h.SetDefaultAgent("codex-shared", fake)
	h.HandleMessage(context.Background(), client, newTestMessage("user-a@im.wechat", "第一句", 1))
	h.HandleMessage(context.Background(), client, quotedMessage("user-a@im.wechat", "这个呢", 2, `{"message_item":{"msg_id":"9001"}}`))

	waitFor(t, 5*time.Second, func() bool { return len(fake.callsSnapshot()) == 2 })
	h.waitForIdle()
	calls := fake.callsSnapshot()
	if len(calls) != 2 || calls[0] != "第一句" || calls[1] != "[引用: 方案原文]\n这个呢" {
		t.Fatalf("quoted message must stay separate with its quote, got %q", calls)
	}
}
