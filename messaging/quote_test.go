package messaging

import (
	"context"
	"crypto/md5"
	"encoding/hex"
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

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestSlicePartialQuote(t *testing.T) {
	full := "第一条消息：我今天要测试不分文字引用功能。😀我今天要测试"
	sel := "不分文字引用功能。😀我今天要测试"
	runeStart := len([]rune("第一条消息：我今天要测试"))
	runeEnd := len([]rune(full))
	cases := []struct {
		name     string
		p        partialQuote
		wantUnit string
	}{
		{"rune exclusive", partialQuote{StartIndex: runeStart, EndIndex: runeEnd, MD5: md5Hex(sel)}, "rune"},
		{"rune inclusive", partialQuote{StartIndex: runeStart, EndIndex: runeEnd - 1, MD5: md5Hex(sel)}, "rune+incl"},
		// The emoji is two UTF-16 units, so only the utf16 slicer lands on the same text.
		{"utf16", partialQuote{StartIndex: runeStart, EndIndex: runeEnd + 1, MD5: md5Hex(sel)}, "utf16"},
		{"byte", partialQuote{StartIndex: len("第一条消息：我今天要测试"), EndIndex: len(full), MD5: md5Hex(sel)}, "byte"},
		{"no digest uses first/last chars", partialQuote{StartIndex: runeStart, EndIndex: runeEnd, Start: "不", End: "试"}, "rune"},
	}
	for _, c := range cases {
		got, unit, ok := slicePartialQuote(full, &c.p)
		if !ok || got != sel || unit != c.wantUnit {
			t.Fatalf("%s: got %q unit=%s ok=%v", c.name, got, unit, ok)
		}
	}
	for name, p := range map[string]partialQuote{
		"digest mismatch":   {StartIndex: runeStart, EndIndex: runeEnd, MD5: md5Hex("别的")},
		"out of range":      {StartIndex: 5, EndIndex: 999, MD5: md5Hex(sel)},
		"reversed":          {StartIndex: 9, EndIndex: 3},
		"no digest no ends": {StartIndex: runeStart, EndIndex: runeEnd},
	} {
		if _, _, ok := slicePartialQuote(full, &p); ok {
			t.Fatalf("%s must not match", name)
		}
	}
}

func TestQuotedOwnMessageIsResolvedForAgent(t *testing.T) {
	withTestStore(t)
	srv := newIDIlinkServer(t)
	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	h := NewHandler(nil, nil)
	fake := &recordingAgent{}
	h.SetDefaultAgent("codex-shared", fake)

	full := "我今天要测试不分文字引用功能。我今天要测试"
	sel := "不分文字引用功能。"
	start := len([]rune("我今天要测试"))
	partial := fmt.Sprintf(`{"message_item":{"msg_id":"7523403615187383560"},"partial_text":{"start":"不","end":"。","startindex":%d,"endindex":%d,"quotemd5":"%s"}}`,
		start, start+len([]rune(sel)), md5Hex(sel))
	badPartial := `{"message_item":{"msg_id":"7523403615187383560"},"partial_text":{"start":"x","end":"y","startindex":1,"endindex":3,"quotemd5":"00000000000000000000000000000000"}}`

	// HandleMessage persists the inbound text synchronously, so the quotes below can resolve it.
	h.HandleMessage(context.Background(), client, newTestMessage("user-a@im.wechat", full, 7523403615187383560))

	h.HandleMessage(context.Background(), client, quotedMessage("user-a@im.wechat", "整条什么意思", 2, `{"message_item":{"msg_id":"7523403615187383560"}}`))
	h.HandleMessage(context.Background(), client, quotedMessage("user-a@im.wechat", "这几个字呢", 3, partial))
	h.HandleMessage(context.Background(), client, quotedMessage("user-a@im.wechat", "对不上的片段", 4, badPartial))
	// Another user quoting the same ID must not see user-a's message.
	h.HandleMessage(context.Background(), client, quotedMessage("user-b@im.wechat", "偷看", 5, `{"message_item":{"msg_id":"7523403615187383560"}}`))

	waitFor(t, 6*time.Second, func() bool { return len(fake.callsSnapshot()) == 5 })
	h.waitForIdle()
	got := map[string]bool{}
	for _, call := range fake.callsSnapshot() {
		got[call] = true
	}
	for _, want := range []string{
		"[引用: " + full + "]\n整条什么意思",
		"[引用片段: " + sel + "]\n[片段所在原消息: " + full + "]\n这几个字呢",
		"[引用: " + full + "]\n对不上的片段", // digest mismatch -> keep full text, never guess
		"偷看",
	} {
		if !got[want] {
			t.Fatalf("agent calls missing %q: %q", want, fake.callsSnapshot())
		}
	}
}
