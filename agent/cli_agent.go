package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// CLIAgent invokes a local CLI agent (claude, codex, etc.) via streaming JSON.
type CLIAgent struct {
	name         string
	command      string
	args         []string          // extra args from config
	cwd          string            // working directory
	env          map[string]string // extra environment variables
	model        string
	systemPrompt string
	mu           sync.Mutex
	sessions     map[string]string          // conversationID -> session ID for multi-turn
	personas     map[string]PersonaOverride // conversationID -> persona override (claude only)
}

// var _ PersonaAwareAgent = (*CLIAgent)(nil) guards against a future change to
// SetPersonaOverride's signature silently breaking interface satisfaction:
// without this, the runtime type-assertion in messaging/handler.go's
// chatWithAgent would just return false with no compile error, and the
// non-owner persona-isolation guarantee would silently stop applying.
var _ PersonaAwareAgent = (*CLIAgent)(nil)

// var _ ModelConfigurableAgent = (*CLIAgent)(nil) guards SetModelConfig the
// same way, for the live model-tier admin panel.
var _ ModelConfigurableAgent = (*CLIAgent)(nil)

// var _ ImageChatAgent = (*CLIAgent)(nil) guards ChatWithImage the same way.
var _ ImageChatAgent = (*CLIAgent)(nil)

// CLIAgentConfig holds configuration for a CLI agent.
type CLIAgentConfig struct {
	Name         string            // agent name for logging, e.g. "claude", "codex"
	Command      string            // path to binary
	Args         []string          // extra args (e.g. ["--dangerously-skip-permissions"])
	Cwd          string            // working directory (workspace)
	Env          map[string]string // extra environment variables
	Model        string
	SystemPrompt string
}

// NewCLIAgent creates a new CLI agent.
func NewCLIAgent(cfg CLIAgentConfig) *CLIAgent {
	cwd := cfg.Cwd
	if cwd == "" {
		cwd = defaultWorkspace()
	}
	return &CLIAgent{
		name:         cfg.Name,
		command:      cfg.Command,
		args:         cfg.Args,
		cwd:          cwd,
		env:          cfg.Env,
		model:        cfg.Model,
		systemPrompt: cfg.SystemPrompt,
		sessions:     make(map[string]string),
		personas:     make(map[string]PersonaOverride),
	}
}

// streamEvent represents a single event from claude's stream-json output.
type streamEvent struct {
	Type      string         `json:"type"`
	SessionID string         `json:"session_id"`
	Result    string         `json:"result"`
	IsError   bool           `json:"is_error"`
	Message   *streamMessage `json:"message,omitempty"`
}

// streamMessage represents the message field in an assistant event.
type streamMessage struct {
	Content []streamContent `json:"content"`
}

// streamContent represents a content block in an assistant message.
type streamContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Info returns metadata about this agent.
func (a *CLIAgent) Info() AgentInfo {
	return AgentInfo{
		Name:    a.name,
		Type:    "cli",
		Model:   a.model,
		Command: a.command,
	}
}

// ResetSession clears the existing session for the given conversationID.
// Returns an empty string because the new session ID is only known after the
// next Chat call (claude assigns it during the conversation).
func (a *CLIAgent) ResetSession(_ context.Context, conversationID string) (string, error) {
	a.mu.Lock()
	delete(a.sessions, conversationID)
	a.mu.Unlock()
	log.Printf("[cli] session reset (command=%s, conversation=%s)", a.command, conversationID)
	return "", nil
}

// SetCwd changes the working directory for subsequent CLI invocations.
func (a *CLIAgent) SetCwd(cwd string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cwd = cwd
}

// SetPersonaOverride records the persona override for one conversationID,
// applied on the next Chat call for the claude backend. No-op for codex
// (chatCodex never reads a.personas).
func (a *CLIAgent) SetPersonaOverride(conversationID string, override PersonaOverride) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.personas[conversationID] = override
}

// SetModelConfig updates the model this agent uses for future Chat calls
// (e.g. claude's model tier, changed live from the 18022 admin panel).
// Unlike ACPAgent, claude spawns a fresh process per turn and reads a.model
// fresh each time (see chatClaude), so there's no cache to invalidate — the
// very next message picks up the change. reasoningEffort is accepted only
// to satisfy agent.ModelConfigurableAgent; claude's CLI has no equivalent
// concept and this parameter is ignored.
func (a *CLIAgent) SetModelConfig(model, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
}

// Chat sends a message to the CLI agent and returns the response.
func (a *CLIAgent) Chat(ctx context.Context, conversationID string, message string) (string, error) {
	switch a.name {
	case "codex":
		return a.chatCodex(ctx, message)
	default:
		return a.chatClaude(ctx, conversationID, message)
	}
}

// chatClaude uses claude -p with stream-json to get structured output and session persistence.
func (a *CLIAgent) chatClaude(ctx context.Context, conversationID string, message string) (string, error) {
	a.mu.Lock()
	override := a.personas[conversationID]
	sessionID, hasSession := a.sessions[conversationID]
	model := a.model
	a.mu.Unlock()

	args := buildClaudeArgs(message, model, a.systemPrompt, a.args, override, sessionID, hasSession)

	if hasSession {
		log.Printf("[cli] resuming session (command=%s, session=%s, conversation=%s)", a.command, sessionID, conversationID)
	} else {
		log.Printf("[cli] starting new conversation (command=%s, conversation=%s)", a.command, conversationID)
	}

	return a.runClaudeStreamJSON(ctx, conversationID, args, "")
}

// ChatWithImage sends an image (plus accompanying text) to claude. Unlike
// chatClaude, the message can't go as a positional -p argument (there's
// nowhere to attach an image to that), so this uses --input-format
// stream-json and writes a Messages-API-style content array (text + base64
// image) to stdin instead — see buildClaudeImageArgs/buildClaudeImageStdin,
// both empirically verified against a real claude process (2026-07-24),
// including --safe-mode together with --input-format stream-json.
//
// Callers MUST call SetPersonaOverride/SetConversationPolicy for this
// conversationID before calling this (same as chatWithAgent already does
// for text turns) — this method does not do it itself. Skipping that step
// for a non-owner's first-ever message (if it happens to be an image) would
// run claude without safe-mode and the sanitized persona, leaking the
// owner's real CLAUDE.md — exactly the class of incident this project's
// isolation work exists to prevent.
func (a *CLIAgent) ChatWithImage(ctx context.Context, conversationID string, message string, image *ImageInput) (string, error) {
	a.mu.Lock()
	override := a.personas[conversationID]
	sessionID, hasSession := a.sessions[conversationID]
	model := a.model
	a.mu.Unlock()

	args := buildClaudeImageArgs(model, a.systemPrompt, a.args, override, sessionID, hasSession)
	stdinPayload, err := buildClaudeImageStdin(message, image)
	if err != nil {
		return "", err
	}

	if hasSession {
		log.Printf("[cli] resuming session for image turn (command=%s, session=%s, conversation=%s)", a.command, sessionID, conversationID)
	} else {
		log.Printf("[cli] starting new conversation for image turn (command=%s, conversation=%s)", a.command, conversationID)
	}

	return a.runClaudeStreamJSON(ctx, conversationID, args, stdinPayload)
}

// runClaudeStreamJSON spawns the claude CLI with args, optionally piping
// stdinPayload to it first (used by ChatWithImage's stream-json input mode;
// empty for chatClaude's positional -p message mode), parses the streamed
// stream-json output into a final result, and saves any new session ID for
// conversationID.
func (a *CLIAgent) runClaudeStreamJSON(ctx context.Context, conversationID string, args []string, stdinPayload string) (string, error) {
	cmd := exec.CommandContext(ctx, a.command, args...)
	if a.cwd != "" {
		cmd.Dir = a.cwd
	}
	if len(a.env) > 0 {
		cmdEnv, err := mergeEnv(os.Environ(), a.env)
		if err != nil {
			return "", fmt.Errorf("build %s env: %w", a.name, err)
		}
		cmd.Env = cmdEnv
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("create stdout pipe: %w", err)
	}

	var stdin io.WriteCloser
	if stdinPayload != "" {
		stdin, err = cmd.StdinPipe()
		if err != nil {
			return "", fmt.Errorf("create stdin pipe: %w", err)
		}
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start %s: %w", a.name, err)
	}

	log.Printf("[cli] spawned process (command=%s, pid=%d, conversation=%s)", a.command, cmd.Process.Pid, conversationID)

	if stdin != nil {
		go func() {
			defer stdin.Close()
			io.WriteString(stdin, stdinPayload)
		}()
	}

	// Parse streaming JSON events
	var result string
	var newSessionID string
	var assistantTexts []string

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer for large responses

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event streamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		// Capture session ID from any event
		if event.SessionID != "" {
			newSessionID = event.SessionID
		}

		switch event.Type {
		case "result":
			if event.IsError {
				return "", fmt.Errorf("%s returned error: %s", a.name, event.Result)
			}
			result = event.Result
		case "assistant":
			// Newer claude CLI versions send text in assistant events
			// instead of the result event's result field.
			if event.Message != nil {
				for _, c := range event.Message.Content {
					if c.Type == "text" && c.Text != "" {
						assistantTexts = append(assistantTexts, c.Text)
					}
				}
			}
		}
	}

	// If the result event had an empty result, fall back to accumulated assistant texts.
	if result == "" && len(assistantTexts) > 0 {
		result = strings.Join(assistantTexts, "")
	}

	if err := cmd.Wait(); err != nil {
		if result == "" {
			errMsg := strings.TrimSpace(stderr.String())
			if errMsg != "" {
				return "", fmt.Errorf("%s exited with error: %w, stderr: %s", a.name, err, errMsg)
			}
			return "", fmt.Errorf("%s exited with error: %w", a.name, err)
		}
		// If we got a result but exit code is non-zero (e.g. hook failures), still return the result
	}

	log.Printf("[cli] process exited (command=%s, pid=%d)", a.command, cmd.Process.Pid)

	// Save session ID for multi-turn conversation
	if newSessionID != "" {
		a.mu.Lock()
		a.sessions[conversationID] = newSessionID
		a.mu.Unlock()
		log.Printf("[cli] saved session (session=%s, conversation=%s)", newSessionID, conversationID)
	}

	result = strings.TrimSpace(result)
	if result == "" {
		return "", fmt.Errorf("%s returned empty response", a.name)
	}

	return result, nil
}

// buildClaudeArgs assembles the claude CLI argument list for one turn. It is
// a pure function so persona injection can be tested without spawning a
// real claude process.
func buildClaudeArgs(message, model, systemPrompt string, extraArgs []string, override PersonaOverride, sessionID string, hasSession bool) []string {
	args := []string{"-p", message, "--output-format", "stream-json", "--verbose"}
	return appendClaudeCommonArgs(args, model, systemPrompt, extraArgs, override, sessionID, hasSession)
}

// buildClaudeImageArgs is buildClaudeArgs for image turns: the message
// doesn't go as a positional arg (there isn't one to attach an image to),
// it goes over stdin as a stream-json event instead (see
// buildClaudeImageStdin) — --input-format stream-json is what tells claude
// to read it that way. Empirically verified against a real claude process
// (2026-07-24): this combination, including --safe-mode together with
// --input-format stream-json, produces a correct image-aware reply with no
// leakage of the owner's real CLAUDE.md persona.
func buildClaudeImageArgs(model, systemPrompt string, extraArgs []string, override PersonaOverride, sessionID string, hasSession bool) []string {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"}
	return appendClaudeCommonArgs(args, model, systemPrompt, extraArgs, override, sessionID, hasSession)
}

// appendClaudeCommonArgs appends the flags shared by every claude turn
// regardless of how the message itself is delivered (positional -p arg vs
// stdin stream-json event).
func appendClaudeCommonArgs(args []string, model, systemPrompt string, extraArgs []string, override PersonaOverride, sessionID string, hasSession bool) []string {
	if model != "" {
		args = append(args, "--model", model)
	}
	if override.SafeMode {
		args = append(args, "--safe-mode")
	}

	combinedPrompt := systemPrompt
	if override.SystemPrompt != "" {
		if combinedPrompt != "" {
			combinedPrompt = combinedPrompt + "\n\n" + override.SystemPrompt
		} else {
			combinedPrompt = override.SystemPrompt
		}
	}
	if combinedPrompt != "" {
		args = append(args, "--append-system-prompt", combinedPrompt)
	}

	args = append(args, extraArgs...)

	if hasSession {
		args = append(args, "--resume", sessionID)
	}
	return args
}

// claudeImageStdinMessage is the stream-json input line shape claude reads
// from stdin when invoked with --input-format stream-json: a "user" event
// wrapping a Messages-API-style content array. Empirically verified against
// a real claude process (2026-07-24) — this exact shape produced a correct
// description of a real test image.
type claudeImageStdinMessage struct {
	Type    string `json:"type"`
	Message struct {
		Role    string                    `json:"role"`
		Content []claudeImageContentBlock `json:"content"`
	} `json:"message"`
}

type claudeImageContentBlock struct {
	Type   string             `json:"type"`
	Text   string             `json:"text,omitempty"`
	Source *claudeImageSource `json:"source,omitempty"`
}

type claudeImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// buildClaudeImageStdin builds the one-line NDJSON payload to write to
// claude's stdin for an image turn: message text plus the image, base64
// encoded. Pure function so it's testable without spawning a real process.
func buildClaudeImageStdin(message string, image *ImageInput) (string, error) {
	msg := claudeImageStdinMessage{Type: "user"}
	msg.Message.Role = "user"
	msg.Message.Content = []claudeImageContentBlock{
		{Type: "text", Text: message},
		{Type: "image", Source: &claudeImageSource{
			Type:      "base64",
			MediaType: image.MimeType,
			Data:      base64.StdEncoding.EncodeToString(image.Data),
		}},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshal claude image stdin message: %w", err)
	}
	return string(data) + "\n", nil
}

// chatCodex handles codex CLI invocation using "codex exec".
func (a *CLIAgent) chatCodex(ctx context.Context, message string) (string, error) {
	args := []string{"exec", message}
	if a.model != "" {
		args = append(args, "--model", a.model)
	}
	// Append extra args from config (e.g. --skip-git-repo-check)
	args = append(args, a.args...)

	log.Printf("[cli] running codex exec (command=%s)", a.command)
	cmd := exec.CommandContext(ctx, a.command, args...)
	if a.cwd != "" {
		cmd.Dir = a.cwd
	}
	if len(a.env) > 0 {
		cmdEnv, err := mergeEnv(os.Environ(), a.env)
		if err != nil {
			return "", fmt.Errorf("build %s env: %w", a.name, err)
		}
		cmd.Env = cmdEnv
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("codex error: %w, stderr: %s", err, errMsg)
		}
		return "", fmt.Errorf("codex error: %w", err)
	}

	result := strings.TrimSpace(string(out))
	if result == "" {
		return "", fmt.Errorf("codex returned empty response")
	}
	return result, nil
}
