package messaging

import (
	"context"
	"fmt"
	"log"
	"time"

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

// typingRefreshInterval is how often the typing indicator is resent while an
// agent call is in flight. WeChat's "对方正在输入" animation fades on its own
// if not refreshed; previously it was only sent once at the start of a turn,
// so long-running requests (e.g. image generation, which can take minutes)
// went visibly silent partway through.
const typingRefreshInterval = 8 * time.Second

// stillWorkingThreshold is how long an ordinary agent call must run before a
// one-time "还在处理中" text nudge is sent. The typing animation alone
// doesn't tell an anxious user whether the bot is still working or stuck.
const stillWorkingThreshold = 20 * time.Second

// imageGenerationNudgeThreshold is the shorter threshold used for requests
// already known to be slow (image generation, which routinely takes 1-2
// minutes) — waiting the generic 20s before the first sign of life feels too
// long when the wait is both long and predictable, so these get an earlier,
// more specific nudge instead.
const imageGenerationNudgeThreshold = 5 * time.Second

// longRunningNudgeThreshold covers other known-slow request types besides
// image generation (web search, deep research, long-form writing) — usually
// faster than image generation but still slower than an ordinary chat
// reply, so it sits between imageGenerationNudgeThreshold and
// stillWorkingThreshold.
const longRunningNudgeThreshold = 10 * time.Second

func stillWorkingReply() string {
	return "还在处理中，请稍等…"
}

func imageGenerationStillWorkingReply() string {
	return "收到，正在生成图片，预计需要 1-2 分钟，请稍等~"
}

func longRunningStillWorkingReply() string {
	return "收到，这个请求需要多花点时间处理，请稍等~"
}

// withTypingRefresh runs work() while periodically refreshing the typing
// indicator and, if work() is still running past stillWorkingThreshold,
// sending one "还在处理中" text nudge. client may be nil (as in tests that
// never run long enough to reach a refresh tick) — refresh/nudge sends are
// skipped in that case rather than dereferencing a nil client.
func withTypingRefresh(ctx context.Context, client *ilink.Client, userID, contextToken string, work func()) {
	withTypingRefreshEvery(ctx, client, userID, contextToken, typingRefreshInterval, stillWorkingThreshold, stillWorkingReply(), work)
}

// withTypingRefreshEvery is withTypingRefresh with explicit durations and
// nudge text, split out so tests can use short intervals instead of the real
// constants, and so callers that already know a request is slow (e.g. image
// generation) can use a shorter threshold and a more specific message.
func withTypingRefreshEvery(ctx context.Context, client *ilink.Client, userID, contextToken string, refreshInterval, nudgeThreshold time.Duration, nudgeText string, work func()) {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		nudge := time.NewTimer(nudgeThreshold)
		defer nudge.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if client == nil {
					continue
				}
				if err := SendTypingState(ctx, client, userID, contextToken); err != nil {
					log.Printf("[handler] failed to refresh typing state: %v", err)
				}
			case <-nudge.C:
				if client == nil {
					continue
				}
				if err := SendTextReply(ctx, client, userID, nudgeText, contextToken, NewClientID()); err != nil {
					log.Printf("[handler] failed to send still-working nudge: %v", err)
				}
			}
		}
	}()
	work()
	close(done)
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
