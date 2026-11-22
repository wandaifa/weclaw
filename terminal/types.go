package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SessionInfo describes one live iTerm2 session returned by iterm_bridge.
type SessionInfo struct {
	Index       int    `json:"index"`
	WindowID    string `json:"window_id"`
	TabID       string `json:"tab_id"`
	SessionID   string `json:"session_id"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Path        string `json:"path"`
	JobName     string `json:"job_name"`
	CommandLine string `json:"command_line"`
	IsAlive     bool   `json:"is_alive"`
}

// SendResult describes the response from BridgeClient.SendText.
type SendResult struct {
	OK        bool   `json:"ok"`
	SessionID string `json:"session_id"`
	JobName   string `json:"job_name"`
}

// DangerousMatch describes a matched high-risk command rule.
type DangerousMatch struct {
	Category string
	Command  string
}

// PendingConfirm holds one suspended dangerous command waiting for explicit
// owner confirmation ("确认").
type PendingConfirm struct {
	SessionID string    `json:"session_id"`
	Command   string    `json:"command"`
	Category  string    `json:"category"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Binding records one owner conversation bound to an iTerm2 session.
type Binding struct {
	BotID          string          `json:"bot_id"`
	UserID         string          `json:"user_id"`
	SessionID      string          `json:"session_id"`
	Label          string          `json:"label"`
	LastActivity   time.Time       `json:"last_activity"`
	PendingConfirm *PendingConfirm `json:"pending_confirm,omitempty"`
}

// ResultNotification is pushed from an iTerm2 shell watcher or agent hook to
// weclaw's local loopback callback endpoint.
type ResultNotification struct {
	SessionID string `json:"session_id"`
	Source    string `json:"source"` // "shell" | "codex" | "claude"
	Command   string `json:"command,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Text      string `json:"text,omitempty"`
	Output    string `json:"output,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
}

// AllowedKey normalizes a supported /key argument and returns whether it is valid.
func AllowedKey(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	switch key {
	case "esc", "enter", "ctrl-c", "up", "down", "tab":
		return key, true
	default:
		return "", false
	}
}

// NormalizeSessionID extracts the iTerm2 session UUID from either a raw UUID or
// an ITERM_SESSION_ID value formatted as "w1t0p0:<UUID>".
func NormalizeSessionID(raw string) string {
	s := strings.TrimSpace(raw)
	if idx := strings.IndexByte(s, ':'); idx >= 0 && idx+1 < len(s) {
		return strings.TrimSpace(s[idx+1:])
	}
	return s
}

// IsShellJob reports whether jobName represents an interactive shell rather than
// a TUI agent like codex or claude.
func IsShellJob(jobName string) bool {
	j := strings.ToLower(strings.TrimSpace(jobName))
	switch j {
	case "", "zsh", "-zsh", "bash", "-bash", "sh", "-sh", "fish", "-fish":
		return true
	default:
		return false
	}
}

// ShortProjectName returns the concise directory basename (or "~" for $HOME)
// for display in /ls and /use replies.
func ShortProjectName(path, fallbackName, sessionID string) string {
	p := strings.TrimSpace(path)
	if p != "" {
		if home, err := os.UserHomeDir(); err == nil && filepath.Clean(p) == filepath.Clean(home) {
			return "~"
		}
		base := filepath.Base(filepath.Clean(p))
		if base != "" && base != "." && base != "/" {
			return base
		}
		return p
	}
	if strings.TrimSpace(fallbackName) != "" {
		return strings.TrimSpace(fallbackName)
	}
	return sessionID
}

// FormatSessionSummary formats a concise one-line summary for /ls:
//
//	1. **weclaw** · agy — Weclaw and Wx-Clawbot Difference [当前]
//	4. **~** · zsh
func FormatSessionSummary(s SessionInfo, current bool) string {
	marker := ""
	if current {
		marker = " [当前]"
	}
	job := strings.TrimLeft(strings.TrimSpace(s.JobName), "-")
	if job == "" {
		job = "-"
	}
	project := ShortProjectName(s.Path, s.Name, s.SessionID)
	title := strings.TrimSpace(s.Title)
	if title != "" {
		return fmt.Sprintf("%d. **%s** · %s — %s%s", s.Index, project, job, title, marker)
	}
	return fmt.Sprintf("%d. **%s** · %s%s", s.Index, project, job, marker)
}
