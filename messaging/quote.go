package messaging

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/fastclaw-ai/weclaw/ilink"
)

// maxQuoteRunes caps how much quoted text is prepended to the agent prompt.
const maxQuoteRunes = 1000

// quoteRef is what a WeChat quote carries. Newer clients often send only an ID
// (msg_id / svr_id, "0" when unknown) and no text, so the text is looked up in
// sent_chunks (Bot replies) or messages (the user's own messages); title/text
// are used directly when the server does include them.
type quoteRef struct {
	ID      string
	Text    string
	Partial *partialQuote
}

// partialQuote is ref_msg.partial_text, sent when the user quotes only part of
// a message. It carries offsets, the first/last characters and an MD5 of the
// selection, never the selected text itself.
type partialQuote struct {
	Start      string `json:"start"`
	End        string `json:"end"`
	StartIndex int    `json:"startindex"`
	EndIndex   int    `json:"endindex"`
	MD5        string `json:"quotemd5"`
}

type rawRefMsg struct {
	Title       string          `json:"title"`
	SvrID       ilink.MessageID `json:"svr_id"`
	PartialText *partialQuote   `json:"partial_text"`
	MessageItem *struct {
		MsgID    ilink.MessageID `json:"msg_id"`
		TextItem *ilink.TextItem `json:"text_item"`
	} `json:"message_item"`
}

type quoteRefKey struct{}

// extractQuoteRef returns the first quote in msg, or nil when it carries nothing usable.
func extractQuoteRef(msg ilink.WeixinMessage) *quoteRef {
	for _, item := range msg.ItemList {
		if len(item.RefMsg) == 0 {
			continue
		}
		var raw rawRefMsg
		if err := json.Unmarshal(item.RefMsg, &raw); err != nil {
			log.Printf("[quote] unparsable ref_msg in message_id=%d: %v", msg.MessageID, err)
			return nil
		}
		ref := &quoteRef{ID: raw.SvrID.String(), Partial: raw.PartialText}
		var parts []string
		if raw.Title != "" {
			parts = append(parts, raw.Title)
		}
		if raw.MessageItem != nil {
			if ref.ID == "" {
				ref.ID = raw.MessageItem.MsgID.String()
			}
			if raw.MessageItem.TextItem != nil && raw.MessageItem.TextItem.Text != "" {
				parts = append(parts, raw.MessageItem.TextItem.Text)
			}
		}
		ref.Text = strings.Join(parts, " | ")
		if ref.ID == "" && ref.Text == "" {
			return nil
		}
		return ref
	}
	return nil
}

// withQuoteRef carries msg's quote, if any, down to chatWithAgent.
func withQuoteRef(ctx context.Context, msg ilink.WeixinMessage) context.Context {
	if ref := extractQuoteRef(msg); ref != nil {
		return context.WithValue(ctx, quoteRefKey{}, ref)
	}
	return ctx
}

// resolveQuoteText finds the full text of the quoted message and says where it
// came from: inline in ref_msg, a Bot reply chunk, or the user's own message.
func resolveQuoteText(ctx context.Context, botID, userID string, ref *quoteRef) (string, string) {
	if ref.Text != "" {
		return ref.Text, "inline"
	}
	if messageStore == nil || ref.ID == "" {
		return "", "none"
	}
	lookups := []struct {
		source string
		fn     func(context.Context, string, string, string) (string, bool, error)
	}{
		{"sent", messageStore.SentChunkText},
		{"inbound", messageStore.InboundMessageText},
	}
	for _, l := range lookups {
		text, ok, err := l.fn(ctx, botID, userID, ref.ID)
		if err != nil {
			log.Printf("[quote] %s lookup failed bot=%s user=%s ref=%s: %v", l.source, botID, userID, ref.ID, err)
			continue
		}
		if ok {
			return text, l.source
		}
	}
	return "", "none"
}

// slicePartialQuote cuts the selected part out of full. The server's index unit
// is not documented, so rune, UTF-16 and byte offsets are each tried with an
// exclusive and an inclusive end. A candidate is accepted only if it matches
// quotemd5 (or, without a digest, the first/last characters); otherwise ok is
// false and the caller keeps the full text rather than guess.
func slicePartialQuote(full string, p *partialQuote) (text, unit string, ok bool) {
	if p == nil || p.StartIndex < 0 || p.EndIndex < p.StartIndex {
		return "", "", false
	}
	runes := []rune(full)
	units16 := utf16.Encode(runes)
	slicers := []struct {
		name  string
		n     int
		slice func(i, j int) string
	}{
		{"rune", len(runes), func(i, j int) string { return string(runes[i:j]) }},
		{"utf16", len(units16), func(i, j int) string { return string(utf16.Decode(units16[i:j])) }},
		{"byte", len(full), func(i, j int) string { return full[i:j] }},
	}
	for _, s := range slicers {
		for _, end := range []struct {
			suffix string
			j      int
		}{{"", p.EndIndex}, {"+incl", p.EndIndex + 1}} {
			if end.j > s.n || end.j <= p.StartIndex {
				continue
			}
			sub := s.slice(p.StartIndex, end.j)
			if partialMatches(sub, p) {
				return sub, s.name + end.suffix, true
			}
		}
	}
	return "", "", false
}

func partialMatches(sub string, p *partialQuote) bool {
	if p.MD5 != "" {
		sum := md5.Sum([]byte(sub))
		return strings.EqualFold(hex.EncodeToString(sum[:]), p.MD5)
	}
	return p.Start != "" && p.End != "" && strings.HasPrefix(sub, p.Start) && strings.HasSuffix(sub, p.End)
}

func truncateQuote(text string) string {
	if r := []rune(text); len(r) > maxQuoteRunes {
		return string(r[:maxQuoteRunes]) + "…"
	}
	return text
}

// withQuotedContext prepends the quoted text to an agent prompt. It is applied
// only at the agent boundary so command matching and request classification
// still see the user's own words.
func withQuotedContext(ctx context.Context, client *ilink.Client, userID, message string) string {
	ref, _ := ctx.Value(quoteRefKey{}).(*quoteRef)
	if ref == nil || client == nil {
		return message
	}
	botID := client.BotID()
	text, source := resolveQuoteText(ctx, botID, userID, ref)
	partial := "none"
	var selected string
	if ref.Partial != nil && text != "" {
		if sub, unit, ok := slicePartialQuote(text, ref.Partial); ok {
			selected, partial = sub, unit
		} else {
			partial = "unmatched"
		}
	}
	log.Printf("[quote] bot=%s user=%s ref=%s resolved=%t source=%s partial=%s len=%d",
		botID, userID, ref.ID, text != "", source, partial, len([]rune(text)))
	if text == "" {
		return message
	}
	if selected != "" && selected != text {
		return fmt.Sprintf("[引用片段: %s]\n[片段所在原消息: %s]\n%s", truncateQuote(selected), truncateQuote(text), message)
	}
	return fmt.Sprintf("[引用: %s]\n%s", truncateQuote(text), message)
}

// recordSentChunk remembers a sent chunk's server ID so it can be quoted later.
func recordSentChunk(botID, userID string, id ilink.MessageID, text string) {
	if messageStore == nil || id == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := messageStore.InsertSentChunk(ctx, botID, userID, id.String(), text, time.Now()); err != nil {
		log.Printf("[store] failed to record sent chunk: %v", err)
	}
}
