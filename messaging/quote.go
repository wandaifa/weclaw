package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
)

// maxQuoteRunes caps how much quoted text is prepended to the agent prompt.
const maxQuoteRunes = 1000

// quoteRef is what a WeChat quote carries. Newer clients often send only an ID
// (msg_id / svr_id, "0" when unknown) and no text, so the text is looked up in
// sent_chunks; title/text are used directly when the server does include them.
type quoteRef struct {
	ID   string
	Text string
}

type rawRefMsg struct {
	Title       string          `json:"title"`
	SvrID       ilink.MessageID `json:"svr_id"`
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
		ref := &quoteRef{ID: raw.SvrID.String()}
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

// withQuotedContext prepends the quoted text to an agent prompt. It is applied
// only at the agent boundary so command matching and request classification
// still see the user's own words.
func withQuotedContext(ctx context.Context, client *ilink.Client, userID, message string) string {
	ref, _ := ctx.Value(quoteRefKey{}).(*quoteRef)
	if ref == nil || client == nil {
		return message
	}
	botID := client.BotID()
	text := ref.Text
	if text == "" && messageStore != nil {
		found, ok, err := messageStore.SentChunkText(ctx, botID, userID, ref.ID)
		if err != nil {
			log.Printf("[quote] lookup failed bot=%s user=%s ref=%s: %v", botID, userID, ref.ID, err)
		} else if ok {
			text = found
		}
	}
	log.Printf("[quote] bot=%s user=%s ref=%s resolved=%t len=%d", botID, userID, ref.ID, text != "", len([]rune(text)))
	if text == "" {
		return message
	}
	if r := []rune(text); len(r) > maxQuoteRunes {
		text = string(r[:maxQuoteRunes]) + "…"
	}
	return fmt.Sprintf("[引用: %s]\n%s", text, message)
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
