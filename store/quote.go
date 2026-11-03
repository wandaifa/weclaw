package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// sentChunkSchema maps each iLink server message ID of a sent text chunk back
// to its text, so a later WeChat quote that carries only the ID can be resolved.
// A long reply is split into several chunks, each with its own server ID.
const sentChunkSchema = `
CREATE TABLE IF NOT EXISTS sent_chunks (
  bot_id        TEXT NOT NULL,
  user_id       TEXT NOT NULL,
  server_msg_id TEXT NOT NULL,
  time          TEXT NOT NULL,
  text          TEXT NOT NULL,
  PRIMARY KEY (bot_id, user_id, server_msg_id)
);
`

// InsertSentChunk records one sent text chunk under its server message ID.
func (s *Store) InsertSentChunk(ctx context.Context, botID, userID, serverMsgID, text string, sentAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO sent_chunks (bot_id, user_id, server_msg_id, time, text)
		VALUES (?, ?, ?, ?, ?)`,
		botID, userID, serverMsgID, sentAt.UTC().Format(time.RFC3339), text)
	if err != nil {
		return fmt.Errorf("insert sent chunk: %w", err)
	}
	return nil
}

// SentChunkText returns the text of a chunk this Bot sent to this user. Lookups
// are scoped to the Bot/user pair so one user can never resolve another's messages.
func (s *Store) SentChunkText(ctx context.Context, botID, userID, serverMsgID string) (string, bool, error) {
	var text string
	err := s.db.QueryRowContext(ctx, `
		SELECT text FROM sent_chunks
		WHERE bot_id = ? AND user_id = ? AND server_msg_id = ?`,
		botID, userID, serverMsgID).Scan(&text)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("query sent chunk: %w", err)
	}
	return text, true, nil
}

// InboundMessageText returns the text of a message this user sent to this Bot,
// looked up by its iLink message_id, so a quote of the user's own message can
// be resolved. Only text/voice rows count: media rows store a file name, which
// would mislead the agent. Scoped to the Bot/user pair like SentChunkText.
func (s *Store) InboundMessageText(ctx context.Context, botID, userID, messageID string) (string, bool, error) {
	var text string
	err := s.db.QueryRowContext(ctx, `
		SELECT text FROM messages
		WHERE role = 'user' AND bot_id = ? AND user_id = ? AND message_id = ?
		  AND message_type IN ('text', 'voice') AND text != ''
		ORDER BY id DESC
		LIMIT 1`,
		botID, userID, messageID).Scan(&text)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("query inbound message: %w", err)
	}
	return text, true, nil
}
