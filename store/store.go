// Package store persists messages to SQLite so chat history survives
// restarts and log rotation instead of being reconstructed by parsing text
// logs after the fact.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS messages (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  time               TEXT NOT NULL,
  role               TEXT NOT NULL,
  agent              TEXT,
  elapsed_ms         INTEGER,
  bot_id             TEXT,
  user_id            TEXT,
  text               TEXT NOT NULL,
  message_id         TEXT,
  context_token      TEXT,
  message_type       TEXT,
  message_state      TEXT,
  file_name          TEXT,
  file_len           TEXT,
  voice_playtime     TEXT,
  media_kind         TEXT,
  media_url          TEXT,
  media_name         TEXT,
  media_display_name TEXT,
  media_size         INTEGER,
  media_source       TEXT
);
CREATE INDEX IF NOT EXISTS idx_messages_bot_user_time ON messages(bot_id, user_id, time);
CREATE INDEX IF NOT EXISTS idx_messages_message_id ON messages(message_id);
`

// Store wraps the SQLite connection used to persist messages.
type Store struct {
	db *sql.DB
}

// MessageRecord is one row to persist. Role is "user" or "bot". Agent is the
// agent name for a real agent reply, "push" for a CLI/API-initiated send, or
// empty for built-in system replies (busy/quota/help/etc.) and inbound
// messages. Text must be the full, untruncated content.
type MessageRecord struct {
	Time             time.Time
	Role             string
	Agent            string
	ElapsedMS        int64
	BotID            string
	UserID           string
	Text             string
	MessageID        string
	ContextToken     string
	MessageType      string
	MessageState     string
	FileName         string
	FileLen          string
	VoicePlaytime    string
	MediaKind        string
	MediaURL         string
	MediaName        string
	MediaDisplayName string
	MediaSize        int64
	MediaSource      string
}

// DefaultPath returns ~/.weclaw/weclaw.db.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", "weclaw.db"), nil
}

// Open creates (if needed) and opens the SQLite database at path, enabling
// WAL mode so a read-only reader (the wx-clawbot viewer) can query it
// concurrently with writes.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// InsertMessage persists one message. Callers should log and continue on
// error rather than fail the surrounding send/receive flow — persistence is
// best-effort and must never block message delivery.
func (s *Store) InsertMessage(ctx context.Context, rec MessageRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO messages (
			time, role, agent, elapsed_ms, bot_id, user_id, text,
			message_id, context_token, message_type, message_state,
			file_name, file_len, voice_playtime,
			media_kind, media_url, media_name, media_display_name, media_size, media_source
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.Time.UTC().Format(time.RFC3339), rec.Role, nullableString(rec.Agent), nullableInt(rec.ElapsedMS),
		nullableString(rec.BotID), nullableString(rec.UserID), rec.Text,
		nullableString(rec.MessageID), nullableString(rec.ContextToken),
		nullableString(rec.MessageType), nullableString(rec.MessageState),
		nullableString(rec.FileName), nullableString(rec.FileLen), nullableString(rec.VoicePlaytime),
		nullableString(rec.MediaKind), nullableString(rec.MediaURL), nullableString(rec.MediaName),
		nullableString(rec.MediaDisplayName), nullableInt(rec.MediaSize), nullableString(rec.MediaSource),
	)
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	return nil
}

// StoredMessage is one row read back, for tests and any future query helpers.
type StoredMessage struct {
	ID   int64
	Time string
	Role string
	Text string
}

// RecentMessages returns the most recent messages, newest first. Intended
// for tests; the wx-clawbot viewer queries the database directly in Python.
func (s *Store) RecentMessages(ctx context.Context, limit int) ([]StoredMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, time, role, text FROM messages ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var out []StoredMessage
	for rows.Next() {
		var m StoredMessage
		if err := rows.Scan(&m.ID, &m.Time, &m.Role, &m.Text); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(v int64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}
