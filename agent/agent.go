package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentInfo holds metadata about an agent for logging/debugging.
type AgentInfo struct {
	Name    string // e.g. "claude-acp", "claude", "gpt-4o"
	Type    string // e.g. "acp", "cli", "http"
	Model   string // e.g. "sonnet", "gpt-4o-mini"
	Command string // binary path, e.g. "/usr/local/bin/claude-agent-acp"
	PID     int    // subprocess PID (0 if not applicable, e.g. http agent)
}

// String returns a human-readable summary for logging.
func (i AgentInfo) String() string {
	s := fmt.Sprintf("name=%s, type=%s, model=%s, command=%s", i.Name, i.Type, i.Model, i.Command)
	if i.PID > 0 {
		s += fmt.Sprintf(", pid=%d", i.PID)
	}
	return s
}

// defaultWorkspace returns ~/.weclaw/workspace as the default working directory.
func defaultWorkspace() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	dir := filepath.Join(home, ".weclaw", "workspace")
	os.MkdirAll(dir, 0o755)
	return dir
}

// mergeEnv merges extra environment variables into the base environment.
func mergeEnv(base []string, extra map[string]string) ([]string, error) {
	if len(extra) == 0 {
		return base, nil
	}

	merged := append([]string(nil), base...)
	indexByKey := make(map[string]int, len(base))
	for i, entry := range merged {
		key, _, found := strings.Cut(entry, "=")
		if !found || key == "" {
			continue
		}
		indexByKey[key] = i
	}

	newKeys := make([]string, 0, len(extra))
	for key, value := range extra {
		if key == "" || strings.Contains(key, "=") {
			return nil, fmt.Errorf("invalid env key %q", key)
		}
		entry := key + "=" + value
		if idx, ok := indexByKey[key]; ok {
			merged[idx] = entry
			continue
		}
		newKeys = append(newKeys, key)
	}

	sort.Strings(newKeys)
	for _, key := range newKeys {
		merged = append(merged, key+"="+extra[key])
	}

	return merged, nil
}

// Agent is the interface for AI chat agents.
type Agent interface {
	// Chat sends a message to the agent and returns the response.
	// conversationID is used to maintain conversation history per user.
	Chat(ctx context.Context, conversationID string, message string) (string, error)

	// ResetSession clears the existing session for the given conversationID and
	// starts a new one. Returns the new session ID if immediately available
	// (ACP mode), or an empty string if the ID will be assigned on next Chat
	// (CLI mode) or is not applicable (HTTP mode).
	ResetSession(ctx context.Context, conversationID string) (string, error)

	// Info returns metadata about this agent.
	Info() AgentInfo

	// SetCwd changes the working directory for subsequent operations.
	SetCwd(cwd string)
}

// SandboxLevel is how much filesystem/exec access a conversation gets.
type SandboxLevel string

const (
	// SandboxReadOnly allows chatting and reading but no writes or command execution.
	SandboxReadOnly SandboxLevel = "read_only"
	// SandboxWorkspaceWrite allows reads/writes confined to ConversationPolicy.WritableRoots.
	SandboxWorkspaceWrite SandboxLevel = "workspace_write"
	// SandboxFullAccess is today's unrestricted behavior, reserved for the owner.
	SandboxFullAccess SandboxLevel = "full_access"
)

// ConversationPolicy is the sandbox tier applied to one conversationID.
type ConversationPolicy struct {
	Level SandboxLevel
	// Cwd overrides the agent's working directory for this conversation.
	// Empty means "use the agent's global cwd" (owner behavior, respects /cwd).
	Cwd string
	// WritableRoots lists directories writable under SandboxWorkspaceWrite.
	WritableRoots []string
}

// PolicyAwareAgent is implemented by agents that can apply a different sandbox
// per conversation on top of a single shared process (currently only the
// codex app-server ACP backend). Callers should type-assert for it rather
// than adding it to the base Agent interface, since CLI/HTTP agents have no
// equivalent concept.
type PolicyAwareAgent interface {
	Agent
	SetConversationPolicy(conversationID string, policy ConversationPolicy)
}

// PersonaOverride carries per-conversation persona injection for agents that
// can switch identity per user on top of a shared process configuration.
// Only the claude CLI backend implements this today — codex's app-server is
// a single long-lived process and can't reload its global AGENTS.md per
// conversation (see docs/superpowers/specs/2026-07-23-user-persona-isolation-design.md).
type PersonaOverride struct {
	// SafeMode, when true, passes --safe-mode to the claude CLI. This is
	// what actually excludes CLAUDE.md (and skills/plugins/hooks/custom
	// commands/agents) for the conversation while leaving auth, model
	// selection, built-in tools, and permissions untouched.
	//
	// CORRECTED 2026-07-24: this field used to be "SettingSources string"
	// passed as --setting-sources project,local, on the assumption that
	// excluding claude's "user" settings source would exclude
	// ~/.claude/CLAUDE.md. That assumption was wrong and went undetected
	// through code review and all automated tests because nothing in this
	// codebase actually spawns a real claude process to check the reply —
	// --setting-sources governs settings.json-style config file sources
	// (permissions/hooks/env), a mechanism entirely separate from
	// CLAUDE.md/memory loading. This was only caught because the owner
	// personally tested a real non-owner conversation and got a reply
	// containing their private CLAUDE.md persona verbatim. Confirmed via
	// manual reproduction (`claude -p ... --setting-sources project,local`
	// still returns the full private persona; `claude -p ... --safe-mode`
	// does not) before writing this fix — see final review's Important
	// finding #1 for the design-time concern this replaces.
	SafeMode bool
	// SystemPrompt is appended (via --append-system-prompt) after the
	// agent's own configured system prompt. Empty means nothing is injected.
	SystemPrompt string
}

// PersonaAwareAgent is implemented by agents that can inject a different
// PersonaOverride per conversation. Callers should type-assert for it rather
// than adding it to the base Agent interface, since ACP/HTTP agents have no
// equivalent concept today.
type PersonaAwareAgent interface {
	Agent
	SetPersonaOverride(conversationID string, override PersonaOverride)
}

// ImageInput holds image data for multimodal chat.
type ImageInput struct {
	MimeType string
	Data     []byte
}

// ImageChatAgent is implemented by agents that can process image input.
type ImageChatAgent interface {
	Agent
	ChatWithImage(ctx context.Context, conversationID string, message string, image *ImageInput) (string, error)
}
