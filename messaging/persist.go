package messaging

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/store"
)

// messageStore is package-level rather than threaded through every call site:
// SendTextReply alone has ~28 call sites across cmd/api/messaging, and
// persistence is a best-effort side effect of sending/receiving, not part of
// those functions' core contract. nil is valid and just no-ops.
var messageStore *store.Store

// SetMessageStore wires up message persistence. Called once at startup.
func SetMessageStore(s *store.Store) {
	messageStore = s
}

func persistMessage(rec store.MessageRecord) {
	if messageStore == nil {
		return
	}
	if rec.Time.IsZero() {
		rec.Time = time.Now()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := messageStore.InsertMessage(ctx, rec); err != nil {
		log.Printf("[store] failed to persist message: %v", err)
	}
}

// persistInboundText records a user-sent text/voice-transcription message.
// MessageType stores the WeChat item type (text/voice/image/file/video, the
// same "items=" value logMessageMeta prints), not the user/bot envelope type
// — that's already captured by Role — since that's what the UI's message
// detail popup actually displays.
func persistInboundText(botID string, msg ilink.WeixinMessage, text string) {
	itemType, fileName, fileLen, voicePlaytime := messageItemMeta(msg)
	persistMessage(store.MessageRecord{
		Role:          "user",
		BotID:         botID,
		UserID:        msg.FromUserID,
		Text:          text,
		MessageID:     formatInt64(msg.MessageID),
		ContextToken:  msg.ContextToken,
		MessageType:   itemType,
		MessageState:  messageStateName(msg.MessageState),
		FileName:      fileName,
		FileLen:       fileLen,
		VoicePlaytime: voicePlaytime,
	})
}

// persistInboundMedia records a saved inbound image/video/file.
func persistInboundMedia(botID string, msg ilink.WeixinMessage, kind, displayName, path string, size int64) {
	itemType, fileName, fileLen, voicePlaytime := messageItemMeta(msg)
	persistMessage(store.MessageRecord{
		Role:             "user",
		BotID:            botID,
		UserID:           msg.FromUserID,
		Text:             displayName,
		MessageID:        formatInt64(msg.MessageID),
		ContextToken:     msg.ContextToken,
		MessageType:      itemType,
		MessageState:     messageStateName(msg.MessageState),
		FileName:         fileName,
		FileLen:          fileLen,
		VoicePlaytime:    voicePlaytime,
		MediaKind:        kind,
		MediaURL:         path,
		MediaName:        displayName,
		MediaDisplayName: displayName,
		MediaSize:        size,
		MediaSource:      "inbound",
	})
}

// persistOutboundMedia records an image/video/file sent to a user.
func persistOutboundMedia(botID, userID, kind, source string) {
	persistMessage(store.MessageRecord{
		Role:        "bot",
		Agent:       "media",
		BotID:       botID,
		UserID:      userID,
		Text:        source,
		MediaKind:   kind,
		MediaURL:    source,
		MediaName:   source,
		MediaSource: source,
	})
}

func messageItemMeta(msg ilink.WeixinMessage) (itemType, fileName, fileLen, voicePlaytime string) {
	for _, item := range msg.ItemList {
		if itemType == "" {
			itemType = itemTypeName(item.Type)
		}
		if item.Type == ilink.ItemTypeFile && item.FileItem != nil {
			fileName = item.FileItem.FileName
			fileLen = item.FileItem.Len
		}
		if item.Type == ilink.ItemTypeVoice && item.VoiceItem != nil && item.VoiceItem.Playtime != 0 {
			voicePlaytime = formatInt64(int64(item.VoiceItem.Playtime))
		}
	}
	return itemType, fileName, fileLen, voicePlaytime
}

func formatInt64(v int64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}
