package messaging

import (
	"context"
	"fmt"
	"log"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/store"
	"github.com/google/uuid"
)

// NewClientID generates a new unique client ID for message correlation.
func NewClientID() string {
	return uuid.New().String()
}

// SendTypingState sends a typing indicator to a user via the iLink sendtyping API.
// It first fetches a typing_ticket via getconfig, then sends the typing status.
func SendTypingState(ctx context.Context, client *ilink.Client, userID, contextToken string) error {
	// Get typing ticket
	configResp, err := client.GetConfig(ctx, userID, contextToken)
	if err != nil {
		return fmt.Errorf("get config for typing: %w", err)
	}
	if configResp.TypingTicket == "" {
		return fmt.Errorf("no typing_ticket returned from getconfig")
	}

	// Send typing
	if err := client.SendTyping(ctx, userID, configResp.TypingTicket, ilink.TypingStatusTyping); err != nil {
		return fmt.Errorf("send typing: %w", err)
	}

	log.Printf("[sender] bot=%s sent typing indicator to %s", client.BotID(), userID)
	return nil
}

// ReplyMeta carries optional persistence context for a reply: which agent
// produced it and how long that took. Zero value means "not an agent reply"
// (a system/utility reply like a busy or quota notice) unless the "push"
// sentinel is used for CLI/API-initiated sends.
type ReplyMeta struct {
	Agent     string
	ElapsedMS int64
}

// SendTextReply sends a text reply to a user through the iLink API.
// If clientID is empty, a new one is generated. meta is optional and only
// affects what gets persisted, never the send itself.
func SendTextReply(ctx context.Context, client *ilink.Client, toUserID, text, contextToken, clientID string, meta ...ReplyMeta) error {
	if clientID == "" {
		clientID = NewClientID()
	}
	var m ReplyMeta
	if len(meta) > 0 {
		m = meta[0]
	}

	// Convert markdown to plain text for WeChat display
	plainText := MarkdownToPlainText(text)

	req := &ilink.SendMessageRequest{
		Msg: ilink.SendMsg{
			FromUserID:   client.BotID(),
			ToUserID:     toUserID,
			ClientID:     clientID,
			MessageType:  ilink.MessageTypeBot,
			MessageState: ilink.MessageStateFinish,
			ItemList: []ilink.MessageItem{
				{
					Type: ilink.ItemTypeText,
					TextItem: &ilink.TextItem{
						Text: plainText,
					},
				},
			},
			ContextToken: contextToken,
		},
		BaseInfo: ilink.BaseInfo{},
	}

	resp, err := client.SendMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}

	if resp.Ret != 0 {
		return fmt.Errorf("send message failed: ret=%d errmsg=%s", resp.Ret, resp.ErrMsg)
	}

	log.Printf("[sender] bot=%s sent reply to %s: %q", client.BotID(), toUserID, truncate(text, 50))
	persistMessage(store.MessageRecord{
		Role:      "bot",
		Agent:     m.Agent,
		ElapsedMS: m.ElapsedMS,
		BotID:     client.BotID(),
		UserID:    toUserID,
		Text:      text,
	})
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
