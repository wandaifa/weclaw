package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
)

const (
	DefaultIdleTimeout    = 30 * time.Minute
	DefaultConfirmTimeout = 60 * time.Second
)

// Bridge abstracts iTerm2 operations for testability.
type Bridge interface {
	ListSessions(ctx context.Context) ([]SessionInfo, error)
	NewSession(ctx context.Context, cwd string) (SessionInfo, error)
	SendText(ctx context.Context, sessionID, text string, submit bool) (SendResult, error)
	SendKey(ctx context.Context, sessionID, key string) error
	Snap(ctx context.Context, sessionID string) (string, error)
	CloseSession(ctx context.Context, sessionID string) error
}

// Manager coordinates owner-only iTerm2 terminal bindings, high-risk command
// confirmations, inactivity expiry, and audit logs.
type Manager struct {
	mu             sync.Mutex
	bridge         Bridge
	statePath      string
	idleTimeout    time.Duration
	confirmTimeout time.Duration
	now            func() time.Time
	bindings       map[string]*Binding // key: botID + "\x00" + userID
}

// DefaultStatePath returns ~/.weclaw/terminal_state.json.
func DefaultStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", "terminal_state.json"), nil
}

// NewManager creates a terminal session manager.
func NewManager(bridge Bridge, statePath string) *Manager {
	m := &Manager{
		bridge:         bridge,
		statePath:      statePath,
		idleTimeout:    DefaultIdleTimeout,
		confirmTimeout: DefaultConfirmTimeout,
		now:            time.Now,
		bindings:       make(map[string]*Binding),
	}
	m.load()
	return m
}

func bindingKey(botID, userID string) string {
	return botID + "\x00" + userID
}

func (m *Manager) load() {
	if m.statePath == "" {
		return
	}
	data, err := os.ReadFile(m.statePath)
	if err != nil {
		return
	}
	var saved map[string]*Binding
	if err := json.Unmarshal(data, &saved); err != nil {
		log.Printf("[terminal] failed to parse state file %s: %v", m.statePath, err)
		return
	}
	for k, b := range saved {
		if b != nil && config.IsOwner(b.UserID) && b.SessionID != "" {
			m.bindings[k] = b
		}
	}
}

func (m *Manager) saveLocked() {
	if m.statePath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o700); err != nil {
		log.Printf("[terminal] create state dir failed: %v", err)
		return
	}
	data, err := json.MarshalIndent(m.bindings, "", "  ")
	if err != nil {
		log.Printf("[terminal] marshal state failed: %v", err)
		return
	}
	tmp := m.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("[terminal] write temp state failed: %v", err)
		return
	}
	if err := os.Rename(tmp, m.statePath); err != nil {
		log.Printf("[terminal] rename state failed: %v", err)
	}
}

func (m *Manager) expireStaleLocked(key string) bool {
	b := m.bindings[key]
	if b == nil {
		return false
	}
	if m.idleTimeout > 0 && m.now().Sub(b.LastActivity) > m.idleTimeout {
		log.Printf("[terminal] audit event=idle_exit bot=%s user=%s session=%s", b.BotID, b.UserID, b.SessionID)
		delete(m.bindings, key)
		m.saveLocked()
		return true
	}
	return false
}

// InTerminalMode reports whether an owner user currently has an active,
// non-expired terminal binding. Always returns false for non-owners.
func (m *Manager) InTerminalMode(botID, userID string) bool {
	if !config.IsOwner(userID) {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := bindingKey(botID, userID)
	if m.expireStaleLocked(key) {
		return false
	}
	return m.bindings[key] != nil
}

// HasPendingConfirm reports whether this owner conversation has a pending
// high-risk confirmation waiting.
func (m *Manager) HasPendingConfirm(botID, userID string) bool {
	if !config.IsOwner(userID) {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := bindingKey(botID, userID)
	if m.expireStaleLocked(key) {
		return false
	}
	b := m.bindings[key]
	return b != nil && b.PendingConfirm != nil
}

// CurrentSessionID returns the currently bound iTerm2 session ID for an owner.
func (m *Manager) CurrentSessionID(botID, userID string) (string, bool) {
	if !config.IsOwner(userID) {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := bindingKey(botID, userID)
	if m.expireStaleLocked(key) {
		return "", false
	}
	b := m.bindings[key]
	if b == nil {
		return "", false
	}
	return b.SessionID, true
}

// LookupBoundOwner resolves which (botID, userID) owns an active binding for
// the given iTerm2 session ID (or ITERM_SESSION_ID).
func (m *Manager) LookupBoundOwner(rawSessionID string) (botID, userID string, ok bool) {
	sid := NormalizeSessionID(rawSessionID)
	if sid == "" {
		return "", "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, b := range m.bindings {
		if m.expireStaleLocked(key) {
			continue
		}
		if b.SessionID == sid && config.IsOwner(b.UserID) {
			return b.BotID, b.UserID, true
		}
	}
	return "", "", false
}

// NewWindow creates a new iTerm2 window and binds this owner conversation to it.
func (m *Manager) NewWindow(ctx context.Context, botID, userID, cwd string) (SessionInfo, error) {
	if !config.IsOwner(userID) {
		return SessionInfo{}, fmt.Errorf("forbidden")
	}
	sess, err := m.bridge.NewSession(ctx, cwd)
	if err != nil {
		return SessionInfo{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bindings[bindingKey(botID, userID)] = &Binding{
		BotID:        botID,
		UserID:       userID,
		SessionID:    sess.SessionID,
		Label:        sess.Name,
		LastActivity: m.now(),
	}
	m.saveLocked()
	log.Printf("[terminal] audit event=new_window bot=%s user=%s session=%s cwd=%q", botID, userID, sess.SessionID, cwd)
	return sess, nil
}

// ListSessions returns all live iTerm2 sessions and the owner's currently bound session ID.
func (m *Manager) ListSessions(ctx context.Context, botID, userID string) ([]SessionInfo, string, error) {
	if !config.IsOwner(userID) {
		return nil, "", fmt.Errorf("forbidden")
	}
	sessions, err := m.bridge.ListSessions(ctx)
	if err != nil {
		return nil, "", err
	}
	currentID, _ := m.CurrentSessionID(botID, userID)
	return sessions, currentID, nil
}

// UseSession binds the owner conversation to the 1-based session index from /ls.
func (m *Manager) UseSession(ctx context.Context, botID, userID string, index int) (SessionInfo, error) {
	if !config.IsOwner(userID) {
		return SessionInfo{}, fmt.Errorf("forbidden")
	}
	sessions, err := m.bridge.ListSessions(ctx)
	if err != nil {
		return SessionInfo{}, err
	}
	var target *SessionInfo
	for i := range sessions {
		if sessions[i].Index == index {
			target = &sessions[i]
			break
		}
	}
	if target == nil {
		return SessionInfo{}, fmt.Errorf("未找到编号为 %d 的会话（先发 /ls 查看）", index)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bindings[bindingKey(botID, userID)] = &Binding{
		BotID:        botID,
		UserID:       userID,
		SessionID:    target.SessionID,
		Label:        target.Name,
		LastActivity: m.now(),
	}
	m.saveLocked()
	log.Printf("[terminal] audit event=use_session bot=%s user=%s index=%d session=%s", botID, userID, index, target.SessionID)
	return *target, nil
}

// CloseSession closes the 1-based session index and unbinds if it was active.
func (m *Manager) CloseSession(ctx context.Context, botID, userID string, index int) (SessionInfo, error) {
	if !config.IsOwner(userID) {
		return SessionInfo{}, fmt.Errorf("forbidden")
	}
	sessions, err := m.bridge.ListSessions(ctx)
	if err != nil {
		return SessionInfo{}, err
	}
	var target *SessionInfo
	for i := range sessions {
		if sessions[i].Index == index {
			target = &sessions[i]
			break
		}
	}
	if target == nil {
		return SessionInfo{}, fmt.Errorf("未找到编号为 %d 的会话（先发 /ls 查看）", index)
	}
	if err := m.bridge.CloseSession(ctx, target.SessionID); err != nil {
		return SessionInfo{}, err
	}
	m.mu.Lock()
	for k, b := range m.bindings {
		if b.SessionID == target.SessionID {
			delete(m.bindings, k)
		}
	}
	m.saveLocked()
	m.mu.Unlock()
	log.Printf("[terminal] audit event=close_session bot=%s user=%s index=%d session=%s", botID, userID, index, target.SessionID)
	return *target, nil
}

// Exit exits terminal mode for this owner without closing the iTerm2 window.
func (m *Manager) Exit(botID, userID string) bool {
	if !config.IsOwner(userID) {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := bindingKey(botID, userID)
	b := m.bindings[key]
	if b == nil {
		return false
	}
	log.Printf("[terminal] audit event=exit_mode bot=%s user=%s session=%s", botID, userID, b.SessionID)
	delete(m.bindings, key)
	m.saveLocked()
	return true
}

// SendCommandResult describes the outcome of sending text in terminal mode.
type SendCommandResult struct {
	Sent           bool
	PendingConfirm *DangerousMatch
	JobName        string
}

// SendInput sends text to the currently bound session. When the bound session
// appears to be at a shell prompt (or job lookup is unavailable/unknown) and
// checkDanger is true, dangerous shell commands pause for confirmation.
func (m *Manager) SendInput(ctx context.Context, botID, userID, text string, checkDanger bool) (SendCommandResult, error) {
	if !config.IsOwner(userID) {
		return SendCommandResult{}, fmt.Errorf("forbidden")
	}
	m.mu.Lock()
	key := bindingKey(botID, userID)
	if m.expireStaleLocked(key) {
		m.mu.Unlock()
		return SendCommandResult{}, fmt.Errorf("终端模式已超时退出，请用 /ls 和 /use 编号 重新进入")
	}
	b := m.bindings[key]
	if b == nil {
		m.mu.Unlock()
		return SendCommandResult{}, fmt.Errorf("当前未绑定终端会话")
	}
	sessionID := b.SessionID
	b.LastActivity = m.now()
	m.saveLocked()
	m.mu.Unlock()

	if checkDanger {
		inShell := true
		if sessions, err := m.bridge.ListSessions(ctx); err == nil {
			for _, s := range sessions {
				if s.SessionID == sessionID {
					inShell = IsShellJob(s.JobName)
					break
				}
			}
		}
		if inShell {
			if match, dangerous := CheckDangerousCommand(text); dangerous {
				m.mu.Lock()
				if cur := m.bindings[key]; cur != nil && cur.SessionID == sessionID {
					cur.PendingConfirm = &PendingConfirm{
						SessionID: sessionID,
						Command:   match.Command,
						Category:  match.Category,
						ExpiresAt: m.now().Add(m.confirmTimeout),
					}
					m.saveLocked()
				}
				m.mu.Unlock()
				log.Printf("[terminal] audit event=danger_paused bot=%s user=%s session=%s category=%q cmd=%q", botID, userID, sessionID, match.Category, match.Command)
				return SendCommandResult{PendingConfirm: &match}, nil
			}
		}
	}

	res, err := m.bridge.SendText(ctx, sessionID, text, true)
	if err != nil {
		return SendCommandResult{}, err
	}
	log.Printf("[terminal] audit event=send_input bot=%s user=%s session=%s job=%q text=%q", botID, userID, sessionID, res.JobName, truncateAudit(text, 120))
	return SendCommandResult{Sent: true, JobName: res.JobName}, nil
}

// ResolveConfirm handles a reply when PendingConfirm is active.
// Only literal "确认" (trimmed) executes the suspended command if unexpired;
// anything else cancels it.
func (m *Manager) ResolveConfirm(ctx context.Context, botID, userID, reply string) (executed bool, msg string, err error) {
	if !config.IsOwner(userID) {
		return false, "", fmt.Errorf("forbidden")
	}
	m.mu.Lock()
	key := bindingKey(botID, userID)
	b := m.bindings[key]
	if b == nil || b.PendingConfirm == nil {
		m.mu.Unlock()
		return false, "", fmt.Errorf("当前没有待确认的高危命令")
	}
	pending := *b.PendingConfirm
	b.PendingConfirm = nil
	b.LastActivity = m.now()
	m.saveLocked()
	m.mu.Unlock()

	if m.now().After(pending.ExpiresAt) {
		log.Printf("[terminal] audit event=danger_expired bot=%s user=%s session=%s cmd=%q", botID, userID, pending.SessionID, pending.Command)
		return false, fmt.Sprintf("高危命令已超过 60 秒自动取消：%s", pending.Command), nil
	}
	if strings.TrimSpace(reply) != "确认" {
		log.Printf("[terminal] audit event=danger_cancelled bot=%s user=%s session=%s cmd=%q", botID, userID, pending.SessionID, pending.Command)
		return false, fmt.Sprintf("已取消执行：%s", pending.Command), nil
	}
	if _, err := m.bridge.SendText(ctx, pending.SessionID, pending.Command, true); err != nil {
		return false, "", err
	}
	log.Printf("[terminal] audit event=danger_confirmed bot=%s user=%s session=%s cmd=%q", botID, userID, pending.SessionID, pending.Command)
	return true, fmt.Sprintf("已确认执行：%s", pending.Command), nil
}

// SendKey sends a validated special key to the bound session.
func (m *Manager) SendKey(ctx context.Context, botID, userID, rawKey string) (string, error) {
	if !config.IsOwner(userID) {
		return "", fmt.Errorf("forbidden")
	}
	key, ok := AllowedKey(rawKey)
	if !ok {
		return "", fmt.Errorf("不支持的按键 %q，支持：esc、enter、ctrl-c、up、down、tab", rawKey)
	}
	sid, ok := m.CurrentSessionID(botID, userID)
	if !ok {
		return "", fmt.Errorf("当前未绑定终端会话")
	}
	m.touch(botID, userID)
	if err := m.bridge.SendKey(ctx, sid, key); err != nil {
		return "", err
	}
	log.Printf("[terminal] audit event=send_key bot=%s user=%s session=%s key=%s", botID, userID, sid, key)
	return key, nil
}

// Snap captures visible screen text of the bound session.
func (m *Manager) Snap(ctx context.Context, botID, userID string) (string, error) {
	if !config.IsOwner(userID) {
		return "", fmt.Errorf("forbidden")
	}
	sid, ok := m.CurrentSessionID(botID, userID)
	if !ok {
		return "", fmt.Errorf("当前未绑定终端会话")
	}
	m.touch(botID, userID)
	return m.bridge.Snap(ctx, sid)
}

func (m *Manager) touch(botID, userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.bindings[bindingKey(botID, userID)]; b != nil {
		b.LastActivity = m.now()
		m.saveLocked()
	}
}

func truncateAudit(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
