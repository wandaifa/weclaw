# 按用户身份分离人格 + 脱敏（persona）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 管理员（owner）跟 claude/codex 聊天时保持现状（完整晚秋人设，加载 `~/.claude/CLAUDE.md`）；任何其他微信用户无论选哪个 agent 都被强制路由到已脱敏的 claude，看到一份不含梁师傅任何隐私信息的可配置人格；18022 后台可管理任意多套人格并按用户绑定。

**Architecture:** 新增 `persona` 包做人格文本的文件存储（`~/.weclaw/personas/<name>.md`）。`agent` 包新增 `PersonaAwareAgent` 接口，`CLIAgent`（claude 后端）按 conversationID 注入 `--setting-sources project,local`（排除 `~/.claude/CLAUDE.md`）+ `--append-system-prompt <人格文本>`。`messaging/handler.go` 的 `selectedAgent()` 对非 owner 用户恒定返回 claude（codex 长驻进程架构做不到按用户脱敏，直接不让非 owner 碰）；新增 `personaOverride()` 做「owner 恒定完整人格 → 用户显式绑定 → 全局默认人格」三层解析，接入 `chatWithAgent`。`config.json` 新增 `default_persona`/`user_personas` 字段持久化绑定关系。`api/server.go` 加三个内部端点（人格 CRUD + 用户绑定，绑定单独成端点避免跟权限档位保存互相覆盖，仿照现有 `Blocked` 字段的做法）。最后 wx-clawbot 的 `chat_viewer.py`（18022 后台，Python，代理调用 weclaw 18011 内部 API）加「人格管理」面板 + 权限表格加一列人格下拉。

**Tech Stack:** Go 1.25（weclaw 主体）、Python 3 标准库（wx-clawbot 的 18022 查看页，零第三方依赖）。

## Global Constraints

- codex 长驻进程架构本期不改造，非 owner 用户永远拿不到 codex —— 详见 spec 决策记录。
- `default` 人格文件不可删除（任何用户失去显式绑定后都要有地方兜底）。
- 排除 `~/.claude/CLAUDE.md` 隐私源用固定值 `--setting-sources project,local`（排除 `user` 来源）。
- 不修改 `~/.claude/`、`~/.codex/` 下任何文件——只读。
- 人格文件目录固定为 `~/.weclaw/personas`，人格名只允许 `[A-Za-z0-9_-]+`（防路径穿越）。
- Go module path：`github.com/fastclaw-ai/weclaw`。
- 每个任务完成后运行 `go test ./...`（weclaw 目录）确认全绿，才能进入下一个任务。

---

## Task 1: `persona` 包 — 人格文本的文件存储

**Files:**
- Create: `persona/persona.go`
- Test: `persona/persona_test.go`

**Interfaces:**
- Produces：
  - `const persona.DefaultName = "default"`
  - `type persona.Persona struct { Name string; Text string }`
  - `func persona.Dir() (string, error)`
  - `func persona.EnsureDefault(dir string) error`
  - `func persona.List(dir string) ([]Persona, error)`
  - `func persona.Load(dir, name string) string`（永不返回 error，读取失败一律回退安全文本）
  - `func persona.Save(dir, name, text string) error`
  - `func persona.Delete(dir, name string) error`（拒绝删除 `DefaultName`）

- [ ] **Step 1: 写 persona 包实现**

`persona/persona.go`:

```go
// Package persona stores the text of each configurable "who is the bot to
// this user" identity, one file per persona, so wx-clawbot's admin panel and
// weclaw's chat handler share a single on-disk source of truth.
package persona

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DefaultName is the persona every non-owner user gets unless explicitly
// bound to a different one. Its file is seeded automatically if missing.
const DefaultName = "default"

// fallbackText is returned by Load when a persona's file is missing or
// unreadable. It carries no owner-specific information, so a storage
// failure can never leak privacy — it just degrades to a generic assistant
// voice.
const fallbackText = "你是一个通用 AI 助手，擅长电商运营、日常问答和文字处理。保持专业、简洁、友善的语气回答问题。不了解的具体人物、公司、私人信息一律如实说不知道，不要编造。"

// Persona is one named system-prompt override.
type Persona struct {
	Name string
	Text string
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Dir returns ~/.weclaw/personas. It does not create the directory.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", "personas"), nil
}

func pathFor(dir, name string) (string, error) {
	if !nameRE.MatchString(name) {
		return "", fmt.Errorf("invalid persona name %q: only letters, digits, - and _ are allowed", name)
	}
	return filepath.Join(dir, name+".md"), nil
}

// EnsureDefault creates dir and dir/default.md (seeded with fallbackText) if
// either is missing. Call once at startup before serving any chat. It never
// overwrites an existing default.md, so an admin's customization survives
// restarts.
func EnsureDefault(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create personas dir: %w", err)
	}
	path, err := pathFor(dir, DefaultName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(fallbackText), 0o600)
}

// List returns every persona file in dir, sorted by name. A missing dir is
// not an error — it just means no personas exist yet.
func List(dir string) ([]Persona, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read personas dir: %w", err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)

	out := make([]Persona, 0, len(names))
	for _, name := range names {
		out = append(out, Persona{Name: name, Text: Load(dir, name)})
	}
	return out, nil
}

// Load reads one persona's text from dir. It never returns an error: a
// missing file, an invalid name, or any read failure all degrade to
// fallbackText, since a storage hiccup must never block a chat turn or leak
// stale content from another persona.
func Load(dir, name string) string {
	path, err := pathFor(dir, name)
	if err != nil {
		return fallbackText
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fallbackText
	}
	return string(data)
}

// Save writes text to dir/<name>.md, creating dir if needed.
func Save(dir, name, text string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create personas dir: %w", err)
	}
	path, err := pathFor(dir, name)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o600)
}

// Delete removes dir/<name>.md. Deleting DefaultName is rejected: every
// non-owner user can fall back to it, so it must always exist.
func Delete(dir, name string) error {
	if name == DefaultName {
		return fmt.Errorf("cannot delete the %q persona", DefaultName)
	}
	path, err := pathFor(dir, name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete persona %q: %w", name, err)
	}
	return nil
}
```

- [ ] **Step 2: 写测试**

`persona/persona_test.go`:

```go
package persona

import (
	"path/filepath"
	"testing"
)

func TestEnsureDefaultSeedsFileOnce(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureDefault(dir); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if got := Load(dir, DefaultName); got != fallbackText {
		t.Fatalf("seeded default text = %q, want fallbackText", got)
	}

	if err := Save(dir, DefaultName, "自定义默认人格"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := EnsureDefault(dir); err != nil {
		t.Fatalf("EnsureDefault (second call): %v", err)
	}
	if got := Load(dir, DefaultName); got != "自定义默认人格" {
		t.Fatalf("EnsureDefault overwrote a customized default: got %q", got)
	}
}

func TestLoadFallsBackToSafeTextWhenMissing(t *testing.T) {
	dir := t.TempDir()
	if got := Load(dir, "does-not-exist"); got != fallbackText {
		t.Fatalf("Load(missing) = %q, want fallbackText", got)
	}
}

func TestLoadFallsBackToSafeTextForInvalidName(t *testing.T) {
	dir := t.TempDir()
	if got := Load(dir, "../../etc/passwd"); got != fallbackText {
		t.Fatalf("Load(path-traversal name) = %q, want fallbackText", got)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "vip", "VIP 专属人格"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Load(dir, "vip"); got != "VIP 专属人格" {
		t.Fatalf("Load(vip) = %q, want %q", got, "VIP 专属人格")
	}
}

func TestSaveRejectsInvalidName(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "../escape", "x"); err == nil {
		t.Fatal("Save should reject a name containing path separators")
	}
}

func TestListReturnsSortedPersonas(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "zeta", "Z"); err != nil {
		t.Fatalf("Save zeta: %v", err)
	}
	if err := Save(dir, "alpha", "A"); err != nil {
		t.Fatalf("Save alpha: %v", err)
	}

	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Fatalf("List() = %+v, want [alpha zeta] sorted", got)
	}
	if got[0].Text != "A" || got[1].Text != "Z" {
		t.Fatalf("List() text = %+v", got)
	}
}

func TestListOnMissingDirReturnsEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	got, err := List(dir)
	if err != nil {
		t.Fatalf("List(missing dir): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List(missing dir) = %+v, want empty", got)
	}
}

func TestDeleteRejectsDefaultPersona(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureDefault(dir); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if err := Delete(dir, DefaultName); err == nil {
		t.Fatal("Delete(default) should be rejected")
	}
	if got := Load(dir, DefaultName); got != fallbackText {
		t.Fatalf("default persona should be untouched, got %q", got)
	}
}

func TestDeleteRemovesPersonaFile(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "temp", "临时人格"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := Delete(dir, "temp"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := Load(dir, "temp"); got != fallbackText {
		t.Fatalf("Load after delete = %q, want fallbackText", got)
	}
}

func TestDeleteMissingPersonaIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	if err := Delete(dir, "never-existed"); err != nil {
		t.Fatalf("Delete(never-existed) = %v, want nil", err)
	}
}
```

- [ ] **Step 3: 跑测试**

Run: `cd ~/AiCodeProject/weclaw && go test ./persona/... -v`
Expected: 全部 PASS（11 个测试）

- [ ] **Step 4: Commit**

```bash
cd ~/AiCodeProject/weclaw
git add persona/persona.go persona/persona_test.go
git commit -m "Add persona package for file-backed persona text storage"
```

---

## Task 2: `agent` 包 — PersonaOverride 注入机制（claude CLI 后端）

**Files:**
- Modify: `agent/agent.go`
- Modify: `agent/cli_agent.go`
- Create: `agent/cli_agent_test.go`

**Interfaces:**
- Consumes：无（不依赖 Task 1）
- Produces：
  - `type agent.PersonaOverride struct { SettingSources string; SystemPrompt string }`
  - `type agent.PersonaAwareAgent interface { Agent; SetPersonaOverride(conversationID string, override PersonaOverride) }`
  - `(*CLIAgent).SetPersonaOverride(conversationID string, override PersonaOverride)`

- [ ] **Step 1: 在 `agent/agent.go` 加类型和接口**

在 `PolicyAwareAgent` 接口定义之后（`agent/agent.go` 第 127 行之后，`ImageInput` 定义之前）插入：

```go
// PersonaOverride carries per-conversation persona injection for agents that
// can switch identity per user on top of a shared process configuration.
// Only the claude CLI backend implements this today — codex's app-server is
// a single long-lived process and can't reload its global AGENTS.md per
// conversation (see docs/superpowers/specs/2026-07-23-user-persona-isolation-design.md).
type PersonaOverride struct {
	// SettingSources restricts which claude settings sources load for this
	// conversation (e.g. "project,local" to exclude the "user" source and
	// therefore ~/.claude/CLAUDE.md). Empty means no restriction.
	SettingSources string
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
```

- [ ] **Step 2: 写失败的测试（先证明 buildClaudeArgs 还不存在）**

`agent/cli_agent_test.go`:

```go
package agent

import (
	"reflect"
	"testing"
)

func TestBuildClaudeArgsOwnerNoOverride(t *testing.T) {
	got := buildClaudeArgs("hi", "sonnet", "", nil, PersonaOverride{}, "", false)
	want := []string{"-p", "hi", "--output-format", "stream-json", "--verbose", "--model", "sonnet"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildClaudeArgs() = %#v, want %#v", got, want)
	}
}

func TestBuildClaudeArgsNonOwnerOverrideExcludesUserSettings(t *testing.T) {
	override := PersonaOverride{SettingSources: "project,local", SystemPrompt: "你是通用助手"}
	got := buildClaudeArgs("hi", "", "", nil, override, "", false)
	want := []string{"-p", "hi", "--output-format", "stream-json", "--verbose", "--setting-sources", "project,local", "--append-system-prompt", "你是通用助手"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildClaudeArgs() = %#v, want %#v", got, want)
	}
}

func TestBuildClaudeArgsCombinesConfiguredAndOverridePrompt(t *testing.T) {
	override := PersonaOverride{SystemPrompt: "脱敏人格"}
	got := buildClaudeArgs("hi", "", "配置的提示", nil, override, "", false)
	want := []string{"-p", "hi", "--output-format", "stream-json", "--verbose", "--append-system-prompt", "配置的提示\n\n脱敏人格"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildClaudeArgs() = %#v, want %#v", got, want)
	}
}

func TestBuildClaudeArgsResumesSession(t *testing.T) {
	got := buildClaudeArgs("hi", "", "", []string{"--dangerously-skip-permissions"}, PersonaOverride{}, "sess-123", true)
	want := []string{"-p", "hi", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--resume", "sess-123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildClaudeArgs() = %#v, want %#v", got, want)
	}
}

func TestSetPersonaOverrideIsPerConversation(t *testing.T) {
	a := NewCLIAgent(CLIAgentConfig{Name: "claude", Command: "claude"})
	a.SetPersonaOverride("user-a", PersonaOverride{SystemPrompt: "A"})
	a.SetPersonaOverride("user-b", PersonaOverride{SystemPrompt: "B"})

	a.mu.Lock()
	gotA := a.personas["user-a"]
	gotB := a.personas["user-b"]
	a.mu.Unlock()

	if gotA.SystemPrompt != "A" || gotB.SystemPrompt != "B" {
		t.Fatalf("personas not stored independently: a=%+v b=%+v", gotA, gotB)
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd ~/AiCodeProject/weclaw && go test ./agent/... -run TestBuildClaudeArgs -v`
Expected: 编译失败（`buildClaudeArgs`/`a.personas` undefined）

- [ ] **Step 4: 修改 `agent/cli_agent.go`**

在 `CLIAgent` struct 加一个字段（紧跟 `sessions` 字段之后）：

```go
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
```

`NewCLIAgent` 里初始化这个 map（紧跟 `sessions: make(...)` 之后）：

```go
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
```

加一个方法（紧跟 `SetCwd` 方法之后）：

```go
// SetPersonaOverride records the persona override for one conversationID,
// applied on the next Chat call for the claude backend. No-op for codex
// (chatCodex never reads a.personas).
func (a *CLIAgent) SetPersonaOverride(conversationID string, override PersonaOverride) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.personas[conversationID] = override
}
```

替换整个 `chatClaude` 函数（原第 116-138 行的参数组装部分）为：

```go
// chatClaude uses claude -p with stream-json to get structured output and session persistence.
func (a *CLIAgent) chatClaude(ctx context.Context, conversationID string, message string) (string, error) {
	a.mu.Lock()
	override := a.personas[conversationID]
	sessionID, hasSession := a.sessions[conversationID]
	a.mu.Unlock()

	args := buildClaudeArgs(message, a.model, a.systemPrompt, a.args, override, sessionID, hasSession)

	if hasSession {
		log.Printf("[cli] resuming session (command=%s, session=%s, conversation=%s)", a.command, sessionID, conversationID)
	} else {
		log.Printf("[cli] starting new conversation (command=%s, conversation=%s)", a.command, conversationID)
	}

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

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start %s: %w", a.name, err)
	}

	log.Printf("[cli] spawned process (command=%s, pid=%d, conversation=%s)", a.command, cmd.Process.Pid, conversationID)

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

	if model != "" {
		args = append(args, "--model", model)
	}
	if override.SettingSources != "" {
		args = append(args, "--setting-sources", override.SettingSources)
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
```

- [ ] **Step 5: 跑测试确认通过**

Run: `cd ~/AiCodeProject/weclaw && go test ./agent/... -v`
Expected: 全部 PASS（含原有 `mergeEnv`/`acp_agent` 测试也不受影响）

- [ ] **Step 6: Commit**

```bash
cd ~/AiCodeProject/weclaw
git add agent/agent.go agent/cli_agent.go agent/cli_agent_test.go
git commit -m "Add per-conversation persona override to the claude CLI backend"
```

---

## Task 3: `config` 包 — 新增 `default_persona`/`user_personas` 字段

**Files:**
- Modify: `config/config.go`
- Modify: `config/config_test.go`

**Interfaces:**
- Consumes：无
- Produces：`config.Config.DefaultPersona string`、`config.Config.UserPersonas map[string]string`（JSON key `default_persona`/`user_personas`）

- [ ] **Step 1: 写失败的测试**

在 `config/config_test.go` 末尾追加：

```go
func TestConfigUserPersonasRoundTrip(t *testing.T) {
	cfg := Config{
		DefaultPersona: "default",
		UserPersonas: map[string]string{
			"someone@im.wechat": "vip",
		},
		Agents: map[string]AgentConfig{},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}

	if decoded.DefaultPersona != "default" {
		t.Fatalf("DefaultPersona = %q, want %q", decoded.DefaultPersona, "default")
	}
	if decoded.UserPersonas["someone@im.wechat"] != "vip" {
		t.Fatalf("UserPersonas[someone] = %q, want %q", decoded.UserPersonas["someone@im.wechat"], "vip")
	}
}

func TestConfigWithoutPersonaFieldsStillLoads(t *testing.T) {
	var cfg Config
	data := []byte(`{"agents":{}}`)
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg.DefaultPersona != "" {
		t.Fatalf("DefaultPersona = %q, want empty for legacy config", cfg.DefaultPersona)
	}
	if cfg.UserPersonas != nil {
		t.Fatalf("UserPersonas = %#v, want nil before Load() normalizes it", cfg.UserPersonas)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ~/AiCodeProject/weclaw && go test ./config/... -run TestConfigUserPersonas -v`
Expected: 编译失败（`DefaultPersona`/`UserPersonas` undefined field）

- [ ] **Step 3: 修改 `config/config.go`**

在 `Config` struct 里，`UserPermissions` 字段之后加两个字段：

```go
type Config struct {
	DefaultAgent    string                    `json:"default_agent"`
	AccessMode      AccessMode                `json:"access_mode,omitempty"`
	UserAgents      map[string]string         `json:"user_agents,omitempty"`
	UserPermissions map[string]UserPermission `json:"user_permissions,omitempty"`
	DefaultPersona  string                    `json:"default_persona,omitempty"`
	UserPersonas    map[string]string         `json:"user_personas,omitempty"` // userID -> persona name
	APIAddr         string                    `json:"api_addr,omitempty"`
	SaveDir         string                    `json:"save_dir,omitempty"`
	MessageMerge    MessageMergeConfig        `json:"message_merge,omitempty"`
	MediaRetention  MediaRetentionConfig      `json:"media_retention,omitempty"`
	Agents          map[string]AgentConfig    `json:"agents"`
}
```

在 `DefaultConfig()` 里加初始化：

```go
func DefaultConfig() *Config {
	return &Config{
		UserAgents:      make(map[string]string),
		UserPermissions: make(map[string]UserPermission),
		UserPersonas:    make(map[string]string),
		Agents:          make(map[string]AgentConfig),
	}
}
```

在 `Load()` 里，紧跟 `UserPermissions` 的 nil 检查之后加：

```go
	if cfg.UserPermissions == nil {
		cfg.UserPermissions = make(map[string]UserPermission)
	}
	if cfg.UserPersonas == nil {
		cfg.UserPersonas = make(map[string]string)
	}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd ~/AiCodeProject/weclaw && go test ./config/... -v`
Expected: 全部 PASS

- [ ] **Step 5: Commit**

```bash
cd ~/AiCodeProject/weclaw
git add config/config.go config/config_test.go
git commit -m "Add default_persona/user_personas fields to config"
```

---

## Task 4: `messaging/handler.go` — 非 owner 强制路由到 claude

这是隐私修复的核心：目前 `selectedAgent()` 对非 owner 用户也遵循 `defaultName`（线上 `config.json` 里 `default_agent` 就是 `"codex"`），也就是说**现在任何陌生人跟 bot 聊天，只要没人手动切过，默认就是 codex** —— 这正是要堵住的洞。这个任务先单独做（不依赖 persona 包），因为它是一个独立可验证的路由修复。

**Files:**
- Modify: `messaging/handler.go`
- Modify: `messaging/handler_test.go`

**Interfaces:**
- Consumes：`config.IsOwner(userID string) bool`（已存在）
- Produces：`(*Handler).selectedAgent(userID string) (string, agent.Agent, bool)` 对非 owner 恒定返回 `("claude", ag, true)`

> **注意（计划修正，2026-07-23）：** 这个任务原本的 Step 1 是「改 `newTestHandler()` 补 `userPersonas` 初始化」，标注"为 Task 5 铺路，现在做不影响本任务"——这个判断是错的：`Handler` struct 在 Task 4 执行时还没有 `userPersonas` 字段（那是 Task 5 Step 4 才加的），此时在 `newTestHandler()` 的结构体字面量里引用它会直接编译失败，报 `unknown field userPersonas in struct literal of type messaging.Handler`。已把这一步挪到 Task 5（见 Task 5 Step 1 的对应修正），Task 4 从下面的 Step 1（原 Step 2）开始。

- [ ] **Step 1: 改 `TestUserAgentSelectionsAreIndependent`（原测试断言的是即将删除的旧行为）**

把 `messaging/handler_test.go` 里的 `TestUserAgentSelectionsAreIndependent` 整个替换为两个测试：

```go
func TestSelectedAgentIgnoresUserAgentsOverrideForNonOwner(t *testing.T) {
	h := newTestHandler()
	h.defaultName = "codex"
	h.SetUserAgents(map[string]string{
		"user-1": "claude",
		"user-2": "codex",
	})

	// Non-owner users are always routed to claude — see nonOwnerAgentName's
	// comment on selectedAgent — regardless of any userAgents override or
	// the configured default agent.
	if name, _, selected := h.selectedAgent("user-1"); name != "claude" || !selected {
		t.Fatalf("user-1 selection = %q, selected=%v; want claude, true", name, selected)
	}
	if name, _, selected := h.selectedAgent("user-2"); name != "claude" || !selected {
		t.Fatalf("user-2 selection = %q, selected=%v; want claude, true (override ignored for non-owner)", name, selected)
	}
	if name, _, selected := h.selectedAgent("user-3"); name != "claude" || !selected {
		t.Fatalf("user-3 selection = %q, selected=%v; want claude fallback, true", name, selected)
	}
}

func TestSelectedAgentHonorsOwnerUserAgentsOverride(t *testing.T) {
	h := newTestHandler()
	h.defaultName = "codex"
	ownerID := config.OwnerUserIDs()[0]
	h.SetUserAgents(map[string]string{ownerID: "claude"})

	if name, _, selected := h.selectedAgent(ownerID); name != "claude" || !selected {
		t.Fatalf("owner selection = %q, selected=%v; want claude, true", name, selected)
	}
}
```

- [ ] **Step 2: 改另外三个原本假定非 owner 走 `codex` 的测试**

`TestNonOwnerAgentSwitchPrefixIsPlainText`（原第 528 行）、`TestNonOwnerCwdIsPlainText`（原第 547 行）、`TestQuotaExceededBlocksNonOwnerBeforeAgentCall`（原第 591 行）三处都有 `h.SetDefaultAgent("codex", fake)`，把这三处全部改成 `h.SetDefaultAgent("claude", fake)`（其余代码不动 —— 这三个测试原本验证的是「非 owner 的消息原样转发到他们唯一能用的 agent」，只是把注册的 fake 名字从 `"codex"` 换成 `"claude"`，跟新的强制路由对齐）。

`TestAccessGateRefusesBeforeAgentCallInOwnerOnlyMode`（原第 764 行）和 `TestAccessGateDoesNotBlockOwnerRegardlessOfMode`（原第 824 行）不用改：前者断言 `fake.callsSnapshot()` 长度为 0（在 selectedAgent 之前就被 access gate 拦下了，跟 agent 名字无关），后者用的是 owner ID（owner 路径不受本次改动影响）。

- [ ] **Step 3: 跑测试确认失败（还没改 selectedAgent 本体）**

Run: `cd ~/AiCodeProject/weclaw && go test ./messaging/... -run 'TestSelectedAgent|TestNonOwnerAgentSwitchPrefixIsPlainText|TestNonOwnerCwdIsPlainText|TestQuotaExceededBlocksNonOwnerBeforeAgentCall' -v`
Expected: `TestSelectedAgentIgnoresUserAgentsOverrideForNonOwner` FAIL（当前实现还是遵循 `userAgents`/`defaultName`）

- [ ] **Step 4: 改 `selectedAgent` 本体**

`messaging/handler.go` 里，紧挨着 `selectedAgent` 函数之前加一个常量，并替换函数体：

```go
// nonOwnerAgentName is the only agent non-owner users are ever routed to.
// codex is a single long-lived process shared by every conversation, so it
// can't apply a different persona/config per user; claude spawns a fresh
// process per turn and supports per-conversation persona injection via
// agent.PersonaAwareAgent, so it's the only backend safe to expose to
// non-owners. See docs/superpowers/specs/2026-07-23-user-persona-isolation-design.md.
const nonOwnerAgentName = "claude"

// selectedAgent returns the agent selected by this user, falling back to the
// global default. Non-owner users always get nonOwnerAgentName regardless of
// defaultName or any userAgents override — see nonOwnerAgentName's comment.
// selected is always true for non-owner so a not-yet-running claude agent
// still gets lazily started by callers' "ag == nil && selected" fallback.
func (h *Handler) selectedAgent(userID string) (string, agent.Agent, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if !config.IsOwner(userID) {
		return nonOwnerAgentName, h.agents[nonOwnerAgentName], true
	}
	name, selected := h.userAgents[userID]
	if !selected {
		name = h.defaultName
	}
	return name, h.agents[name], selected
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `cd ~/AiCodeProject/weclaw && go test ./messaging/... -v`
Expected: 全部 PASS

- [ ] **Step 6: Commit**

```bash
cd ~/AiCodeProject/weclaw
git add messaging/handler.go messaging/handler_test.go
git commit -m "Force non-owner WeChat users onto the sanitized claude backend"
```

---

## Task 5: `messaging/handler.go` — persona 绑定解析 + 注入 chatWithAgent

**Files:**
- Modify: `messaging/handler.go`
- Modify: `messaging/handler_test.go`

**Interfaces:**
- Consumes：`persona.Load(dir, name string) string`、`persona.DefaultName`（Task 1）；`agent.PersonaOverride`、`agent.PersonaAwareAgent`（Task 2）
- Produces：
  - `(*Handler).SetPersonaDir(dir string)`
  - `(*Handler).SetDefaultPersona(name string)`
  - `(*Handler).SetUserPersonas(bindings map[string]string)`
  - `(*Handler).SetUserPersona(userID, name string)`
  - `(*Handler).personaOverride(userID string) (personaName string, override agent.PersonaOverride)`
  - `PermissionSnapshot.Persona string`（供 Task 7 的 api provider 用）

- [ ] **Step 1: 给测试用的 `recordingAgent` 加 `SetPersonaOverride`**

在 `messaging/handler_test.go` 的 `recordingAgent` struct 和方法定义处，加字段和方法：

```go
type recordingAgent struct {
	mu               sync.Mutex
	calls            []string
	personaOverrides map[string]agent.PersonaOverride
}
```

紧跟 `SetCwd` 方法之后加：

```go
func (a *recordingAgent) SetPersonaOverride(conversationID string, override agent.PersonaOverride) {
	a.mu.Lock()
	if a.personaOverrides == nil {
		a.personaOverrides = make(map[string]agent.PersonaOverride)
	}
	a.personaOverrides[conversationID] = override
	a.mu.Unlock()
}

func (a *recordingAgent) personaOverrideFor(conversationID string) (agent.PersonaOverride, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	override, ok := a.personaOverrides[conversationID]
	return override, ok
}
```

- [ ] **Step 2: 写失败的测试**

在 `messaging/handler_test.go` 末尾追加（需要在文件顶部 import 块加一行 `"github.com/fastclaw-ai/weclaw/persona"`）：

```go
func TestPersonaOverrideResolutionPriority(t *testing.T) {
	dir := t.TempDir()
	if err := persona.Save(dir, persona.DefaultName, "默认人格"); err != nil {
		t.Fatalf("persona.Save default: %v", err)
	}
	if err := persona.Save(dir, "vip", "VIP人格"); err != nil {
		t.Fatalf("persona.Save vip: %v", err)
	}

	h := newTestHandler()
	h.SetPersonaDir(dir)

	// No binding -> default persona.
	name, override := h.personaOverride("user-a")
	if name != persona.DefaultName || override.SystemPrompt != "默认人格" {
		t.Fatalf("unbound user got (%q, %q), want (%q, \"默认人格\")", name, override.SystemPrompt, persona.DefaultName)
	}

	// Explicit binding -> that persona.
	h.SetUserPersona("user-b", "vip")
	name, override = h.personaOverride("user-b")
	if name != "vip" || override.SystemPrompt != "VIP人格" {
		t.Fatalf("bound user got (%q, %q), want (\"vip\", \"VIP人格\")", name, override.SystemPrompt)
	}

	if override.SettingSources != "project,local" {
		t.Fatalf("override.SettingSources = %q, want %q", override.SettingSources, "project,local")
	}
}

func TestChatWithAgentInjectsPersonaOverrideForNonOwner(t *testing.T) {
	dir := t.TempDir()
	if err := persona.Save(dir, persona.DefaultName, "脱敏人格文本"); err != nil {
		t.Fatalf("persona.Save: %v", err)
	}

	h := newTestHandler()
	h.SetPersonaDir(dir)
	fake := &recordingAgent{}

	if _, _, err := h.chatWithAgent(context.Background(), fake, "non-owner-test@im.wechat", "hi"); err != nil {
		t.Fatalf("chatWithAgent: %v", err)
	}

	override, ok := fake.personaOverrideFor("non-owner-test@im.wechat")
	if !ok {
		t.Fatal("expected a persona override to be recorded")
	}
	if override.SystemPrompt != "脱敏人格文本" {
		t.Fatalf("override.SystemPrompt = %q, want %q", override.SystemPrompt, "脱敏人格文本")
	}
}

func TestChatWithAgentSkipsPersonaOverrideForOwner(t *testing.T) {
	dir := t.TempDir()
	h := newTestHandler()
	h.SetPersonaDir(dir)
	fake := &recordingAgent{}

	ownerID := config.OwnerUserIDs()[0]
	if _, _, err := h.chatWithAgent(context.Background(), fake, ownerID, "hi"); err != nil {
		t.Fatalf("chatWithAgent: %v", err)
	}

	if _, ok := fake.personaOverrideFor(ownerID); ok {
		t.Fatal("owner conversations must not get a persona override")
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd ~/AiCodeProject/weclaw && go test ./messaging/... -run 'TestPersonaOverride|TestChatWithAgentInjects|TestChatWithAgentSkips' -v`
Expected: 编译失败（`SetPersonaDir`/`SetUserPersona`/`personaOverride` undefined）

- [ ] **Step 4: 改 `messaging/handler.go`**

顶部 import 块加一行：

```go
	"github.com/fastclaw-ai/weclaw/persona"
```

`Handler` struct 里，紧跟 `permissions` 字段之后加三个字段：

```go
	permissions   map[string]config.UserPermission // user ID -> sandbox tier (non-owner only)
	personaDir     string            // ~/.weclaw/personas; empty disables persona injection
	defaultPersona string            // config.json default_persona; "" falls back to persona.DefaultName
	userPersonas   map[string]string // userID -> persona name (non-owner only)
```

`NewHandler` 里加初始化：

```go
func NewHandler(factory AgentFactory, saveUserAgent SaveUserAgentFunc) *Handler {
	return &Handler{
		agents:        make(map[string]agent.Agent),
		userAgents:    make(map[string]string),
		permissions:   make(map[string]config.UserPermission),
		userPersonas:  make(map[string]string),
		agentWorkDirs: make(map[string]string),
		factory:       factory,
		saveUserAgent: saveUserAgent,
		mergeSettings: DefaultMergeSettings(),
		usage:         make(map[string]*dailyUsage),
	}
}
```

**（计划修正，2026-07-23）** 同时更新测试辅助函数 `newTestHandler()`（`messaging/handler_test.go`）——它是绕过 `NewHandler` 直接构造 `Handler{}` 字面量的测试专用辅助函数，不会自动获得上面 `NewHandler` 里加的初始化，必须单独补上，否则本任务下面 Step 2 里 `TestPersonaOverrideResolutionPriority` 调用的 `h.SetUserPersona(...)` 会往 nil map 写入直接 panic：

```go
func newTestHandler() *Handler {
	return &Handler{
		agents:       make(map[string]agent.Agent),
		userAgents:   make(map[string]string),
		permissions:  make(map[string]config.UserPermission),
		userPersonas: make(map[string]string),
		usage:        make(map[string]*dailyUsage),
	}
}
```

紧跟 `SetUserPermission` 方法之后加四个新方法：

```go
// SetPersonaDir configures where persona text files live. Must be called
// before any non-owner chat reaches chatWithAgent, or persona injection
// falls back to persona.Load's built-in safe text (empty dir behaves the
// same as a dir with no files).
func (h *Handler) SetPersonaDir(dir string) {
	h.mu.Lock()
	h.personaDir = dir
	h.mu.Unlock()
}

// SetDefaultPersona sets the persona non-owner users get when they have no
// explicit binding in userPersonas.
func (h *Handler) SetDefaultPersona(name string) {
	h.mu.Lock()
	h.defaultPersona = name
	h.mu.Unlock()
}

// SetUserPersonas restores persisted non-owner persona bindings at startup.
func (h *Handler) SetUserPersonas(bindings map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.userPersonas = make(map[string]string, len(bindings))
	for userID, name := range bindings {
		if userID != "" && name != "" {
			h.userPersonas[userID] = name
		}
	}
}

// SetUserPersona updates one non-owner user's persona binding immediately
// (called by the internal permissions API after saving to config). An empty
// name clears the binding, falling back to defaultPersona.
func (h *Handler) SetUserPersona(userID, name string) {
	h.mu.Lock()
	if name == "" {
		delete(h.userPersonas, userID)
	} else {
		h.userPersonas[userID] = name
	}
	h.mu.Unlock()
}

// personaOverride resolves the agent.PersonaOverride for one non-owner
// user's claude conversation: an explicit userPersonas binding wins, else
// defaultPersona, else persona.DefaultName. personaName is returned
// alongside for logging/snapshot purposes.
func (h *Handler) personaOverride(userID string) (personaName string, override agent.PersonaOverride) {
	h.mu.RLock()
	dir := h.personaDir
	name, bound := h.userPersonas[userID]
	defaultName := h.defaultPersona
	h.mu.RUnlock()

	if !bound || name == "" {
		name = defaultName
	}
	if name == "" {
		name = persona.DefaultName
	}

	text := persona.Load(dir, name)
	return name, agent.PersonaOverride{SettingSources: "project,local", SystemPrompt: text}
}
```

在 `chatWithAgent` 里，紧跟现有的 `PolicyAwareAgent` type-assert 之后加：

```go
func (h *Handler) chatWithAgent(ctx context.Context, ag agent.Agent, userID, message string) (string, time.Duration, error) {
	if pa, ok := ag.(agent.PolicyAwareAgent); ok {
		pa.SetConversationPolicy(userID, h.conversationPolicy(userID))
	}
	if pa, ok := ag.(agent.PersonaAwareAgent); ok && !config.IsOwner(userID) {
		_, override := h.personaOverride(userID)
		pa.SetPersonaOverride(userID, override)
	}

	info := ag.Info()
	log.Printf("[handler] dispatching to agent (%s) for %s", info, userID)
	// ...其余函数体不变
```

最后，扩展 `PermissionSnapshot`（供 Task 7 用）：

```go
type PermissionSnapshot struct {
	UserID     string
	Level      config.PermissionLevel
	DailyLimit int
	Blocked    bool
	Persona    string // "" = using the default persona
}
```

以及 `PermissionsSnapshot()` 方法体里填充这个新字段：

```go
func (h *Handler) PermissionsSnapshot() []PermissionSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]PermissionSnapshot, 0, len(h.permissions))
	for userID, perm := range h.permissions {
		out = append(out, PermissionSnapshot{
			UserID: userID, Level: perm.Level, DailyLimit: perm.DailyLimit, Blocked: perm.Blocked,
			Persona: h.userPersonas[userID],
		})
	}
	return out
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `cd ~/AiCodeProject/weclaw && go test ./messaging/... -v`
Expected: 全部 PASS

- [ ] **Step 6: Commit**

```bash
cd ~/AiCodeProject/weclaw
git add messaging/handler.go messaging/handler_test.go
git commit -m "Resolve and inject per-user persona overrides in chatWithAgent"
```

---

## Task 6: `api/server.go` — 人格管理 + 用户绑定的内部端点

**Files:**
- Modify: `api/server.go`
- Modify: `api/server_test.go`

**Interfaces:**
- Consumes：无（纯 HTTP 层，controller 由 Task 7 在 `cmd/start.go` 里注入）
- Produces：
  - 路径常量：`api.PersonasPath = "/api/internal/personas"`、`api.PersonaDeletePath = "/api/internal/personas/delete"`、`api.PermissionsPersonaPath = "/api/internal/permissions/persona"`
  - `api.PersonaInfo{Name, Text string}`
  - `(*Server).SetPersonasProvider(func() ([]PersonaInfo, error))`
  - `(*Server).SetPersonaSaveController(func(context.Context, PersonaSaveRequest) (PersonaInfo, error))`
  - `(*Server).SetPersonaDeleteController(func(context.Context, PersonaDeleteRequest) error)`
  - `(*Server).SetPermissionPersonaController(func(context.Context, PermissionPersonaRequest) (UserPermissionInfo, error))`
  - `api.UserPermissionInfo` 新增字段 `Persona string`

- [ ] **Step 1: 写失败的测试**

在 `api/server_test.go` 末尾追加：

```go
func TestHandlePersonasListsAndSaves(t *testing.T) {
	stored := map[string]PersonaInfo{"default": {Name: "default", Text: "通用助手"}}
	server := NewServer(nil, "")
	server.SetPersonasProvider(func() ([]PersonaInfo, error) {
		out := make([]PersonaInfo, 0, len(stored))
		for _, v := range stored {
			out = append(out, v)
		}
		return out, nil
	})
	server.SetPersonaSaveController(func(_ context.Context, req PersonaSaveRequest) (PersonaInfo, error) {
		info := PersonaInfo{Name: req.Name, Text: req.Text}
		stored[req.Name] = info
		return info, nil
	})

	get := httptest.NewRequest(http.MethodGet, PersonasPath, nil)
	get.RemoteAddr = "127.0.0.1:12345"
	getResp := httptest.NewRecorder()
	server.handlePersonas(getResp, get)
	if getResp.Code != http.StatusOK || !strings.Contains(getResp.Body.String(), "通用助手") {
		t.Fatalf("GET = %d %s", getResp.Code, getResp.Body.String())
	}

	post := httptest.NewRequest(http.MethodPost, PersonasPath, bytes.NewBufferString(`{"name":"vip","text":"VIP 人格"}`))
	post.RemoteAddr = "127.0.0.1:12345"
	postResp := httptest.NewRecorder()
	server.handlePersonas(postResp, post)
	if postResp.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", postResp.Code, postResp.Body.String())
	}
	if got := stored["vip"]; got.Text != "VIP 人格" {
		t.Fatalf("stored persona = %+v", got)
	}
}

func TestHandlePersonaDeleteRejectsDefault(t *testing.T) {
	server := NewServer(nil, "")
	server.SetPersonaDeleteController(func(_ context.Context, req PersonaDeleteRequest) error {
		if req.Name == "default" {
			return fmt.Errorf("cannot delete the %q persona", "default")
		}
		return nil
	})

	req := httptest.NewRequest(http.MethodPost, PersonaDeletePath, bytes.NewBufferString(`{"name":"default"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	resp := httptest.NewRecorder()
	server.handlePersonaDelete(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.Code)
	}
}

func TestHandlePermissionsPersonaIsIndependentOfLevelSave(t *testing.T) {
	stored := map[string]UserPermissionInfo{}
	server := NewServer(nil, "")
	server.SetPermissionController(func(_ context.Context, req PermissionSetRequest) (UserPermissionInfo, error) {
		info := UserPermissionInfo{UserID: req.UserID, Level: req.Level, DailyLimit: req.DailyLimit, Persona: stored[req.UserID].Persona}
		stored[req.UserID] = info
		return info, nil
	})
	server.SetPermissionPersonaController(func(_ context.Context, req PermissionPersonaRequest) (UserPermissionInfo, error) {
		info := stored[req.UserID]
		info.UserID = req.UserID
		info.Persona = req.Persona
		stored[req.UserID] = info
		return info, nil
	})

	bind := httptest.NewRequest(http.MethodPost, PermissionsPersonaPath, bytes.NewBufferString(`{"user_id":"someone@im.wechat","persona":"vip"}`))
	bind.RemoteAddr = "127.0.0.1:12345"
	bindResp := httptest.NewRecorder()
	server.handlePermissionsPersona(bindResp, bind)
	if bindResp.Code != http.StatusOK || !strings.Contains(bindResp.Body.String(), "vip") {
		t.Fatalf("bind POST = %d %s", bindResp.Code, bindResp.Body.String())
	}

	save := httptest.NewRequest(http.MethodPost, PermissionsPath, bytes.NewBufferString(`{"user_id":"someone@im.wechat","level":"workspace_write","daily_limit":30}`))
	save.RemoteAddr = "127.0.0.1:12345"
	saveResp := httptest.NewRecorder()
	server.handlePermissions(saveResp, save)
	if saveResp.Code != http.StatusOK {
		t.Fatalf("level save POST = %d %s", saveResp.Code, saveResp.Body.String())
	}
	if got := stored["someone@im.wechat"]; got.Persona != "vip" {
		t.Fatalf("level save must not clear persona binding, got %+v", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ~/AiCodeProject/weclaw && go test ./api/... -run TestHandlePersona -v`
Expected: 编译失败

- [ ] **Step 3: 改 `api/server.go` — 加路径常量**

在现有的 `const (...)` 块（`PermissionsUsagePath` 那一行）后面加：

```go
	PersonasPath          = "/api/internal/personas"
	PersonaDeletePath     = "/api/internal/personas/delete"
	PermissionsPersonaPath = "/api/internal/permissions/persona"
```

- [ ] **Step 4: 加类型定义**

紧跟 `UsageProvider` 类型定义之后加：

```go
// PersonaInfo describes one stored persona for the internal personas API.
type PersonaInfo struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// PersonasProvider lists every stored persona.
type PersonasProvider func() ([]PersonaInfo, error)

// PersonaSaveRequest is the JSON body for POST /api/internal/personas.
type PersonaSaveRequest struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// PersonaSaveController creates or updates one persona's text.
type PersonaSaveController func(context.Context, PersonaSaveRequest) (PersonaInfo, error)

// PersonaDeleteRequest is the JSON body for POST /api/internal/personas/delete.
type PersonaDeleteRequest struct {
	Name string `json:"name"`
}

// PersonaDeleteController removes one persona. Deleting "default" is
// rejected by the underlying persona package.
type PersonaDeleteController func(context.Context, PersonaDeleteRequest) error

// PermissionPersonaRequest is the JSON body for POST
// /api/internal/permissions/persona. A separate endpoint from
// PermissionSetRequest for the same reason Blocked has its own endpoint:
// binding a persona and saving a sandbox tier must never clobber each other.
type PermissionPersonaRequest struct {
	UserID  string `json:"user_id"`
	Persona string `json:"persona"` // empty = clear the binding, fall back to the default persona
}

// PermissionPersonaController persists and immediately applies one user's
// persona binding, leaving Level/DailyLimit/Blocked untouched.
type PermissionPersonaController func(context.Context, PermissionPersonaRequest) (UserPermissionInfo, error)
```

在 `UserPermissionInfo` struct 里加一个字段（`Blocked` 之后）：

```go
type UserPermissionInfo struct {
	UserID     string `json:"user_id"`
	Level      string `json:"level"` // "read_only" | "workspace_write"
	DailyLimit int    `json:"daily_limit"`
	IsOwner    bool   `json:"is_owner"`
	Blocked    bool   `json:"blocked"`
	// Persona is the explicitly bound persona name, or "" if this user is
	// using the default persona. Owners never have one (always full access).
	Persona string `json:"persona,omitempty"`
}
```

- [ ] **Step 5: 加 Server 字段和 Setter**

`Server` struct 里，`usage` 字段之后加：

```go
	usage             UsageProvider
	personas          PersonasProvider
	savePersona       PersonaSaveController
	deletePersona     PersonaDeleteController
	setPersonaBinding PermissionPersonaController
	addr              string
```

紧跟现有的 `SetPermissionController` 方法之后加四个 setter：

```go
// SetPersonasProvider exposes the list of stored personas through the loopback-only endpoint.
func (s *Server) SetPersonasProvider(provider PersonasProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.personas = provider
}

// SetPersonaSaveController enables creating/updating a persona through the loopback API.
func (s *Server) SetPersonaSaveController(controller PersonaSaveController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savePersona = controller
}

// SetPersonaDeleteController enables deleting a persona through the loopback API.
func (s *Server) SetPersonaDeleteController(controller PersonaDeleteController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletePersona = controller
}

// SetPermissionPersonaController enables binding a user to a persona through the loopback API.
func (s *Server) SetPermissionPersonaController(controller PermissionPersonaController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setPersonaBinding = controller
}
```

- [ ] **Step 6: 注册路由**

紧跟 `mux.HandleFunc(PermissionsUsagePath, s.handlePermissionsUsage)` 之后加：

```go
	mux.HandleFunc(PersonasPath, s.handlePersonas)
	mux.HandleFunc(PersonaDeletePath, s.handlePersonaDelete)
	mux.HandleFunc(PermissionsPersonaPath, s.handlePermissionsPersona)
```

- [ ] **Step 7: 加 HTTP handler**

紧跟 `handlePermissionsUsage` 函数之后加三个函数：

```go
func (s *Server) handlePersonas(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	s.mu.RLock()
	provider, controller := s.personas, s.savePersona
	s.mu.RUnlock()

	if r.Method == http.MethodGet {
		if provider == nil {
			http.Error(w, "personas are unavailable", http.StatusServiceUnavailable)
			return
		}
		list, err := provider()
		if err != nil {
			http.Error(w, "failed to list personas: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string][]PersonaInfo{"personas": list})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	if controller == nil {
		http.Error(w, "personas are unavailable", http.StatusServiceUnavailable)
		return
	}
	var req PersonaSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	info, err := controller(r.Context(), req)
	if err != nil {
		http.Error(w, "invalid persona save: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func (s *Server) handlePersonaDelete(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	controller := s.deletePersona
	s.mu.RUnlock()
	if controller == nil {
		http.Error(w, "personas are unavailable", http.StatusServiceUnavailable)
		return
	}
	var req PersonaDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := controller(r.Context(), req); err != nil {
		http.Error(w, "invalid persona delete: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handlePermissionsPersona(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	controller := s.setPersonaBinding
	s.mu.RUnlock()
	if controller == nil {
		http.Error(w, "permissions are unavailable", http.StatusServiceUnavailable)
		return
	}
	var req PermissionPersonaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}
	updated, err := controller(r.Context(), req)
	if err != nil {
		http.Error(w, "invalid persona binding: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}
```

- [ ] **Step 8: 跑测试确认通过**

Run: `cd ~/AiCodeProject/weclaw && go test ./api/... -v`
Expected: 全部 PASS

- [ ] **Step 9: Commit**

```bash
cd ~/AiCodeProject/weclaw
git add api/server.go api/server_test.go
git commit -m "Add internal API endpoints for persona CRUD and user binding"
```

---

## Task 7: `cmd/start.go` — 把所有部件接起来

**Files:**
- Modify: `cmd/start.go`

**Interfaces:**
- Consumes：Task 1-6 的全部 Produces
- Produces：可运行的完整 weclaw 二进制

- [ ] **Step 1: 加 import**

`cmd/start.go` 顶部 import 块加一行：

```go
	"github.com/fastclaw-ai/weclaw/persona"
```

- [ ] **Step 2: 启动时准备 persona 目录**

在 `handler := messaging.NewHandler(...)` 那段代码之前插入：

```go
	personaDir, err := persona.Dir()
	if err != nil {
		return fmt.Errorf("resolve personas dir: %w", err)
	}
	if err := persona.EnsureDefault(personaDir); err != nil {
		return fmt.Errorf("seed default persona: %w", err)
	}
```

- [ ] **Step 3: 把 persona 状态接入 handler**

紧跟 `handler.SetAccessMode(cfg.AccessMode)` 之后加：

```go
	handler.SetPersonaDir(personaDir)
	handler.SetDefaultPersona(cfg.DefaultPersona)
	handler.SetUserPersonas(cfg.UserPersonas)
```

- [ ] **Step 4: 扩展现有 `SetPermissionsProvider` 闭包，带上 Persona 字段**

把现有的：

```go
	apiServer.SetPermissionsProvider(func() []api.UserPermissionInfo {
		infos := make([]api.UserPermissionInfo, 0)
		for _, ownerID := range config.OwnerUserIDs() {
			infos = append(infos, api.UserPermissionInfo{UserID: ownerID, Level: "full_access", IsOwner: true})
		}
		for _, perm := range handler.PermissionsSnapshot() {
			infos = append(infos, api.UserPermissionInfo{
				UserID: perm.UserID, Level: string(perm.Level), DailyLimit: perm.DailyLimit, Blocked: perm.Blocked,
			})
		}
		return infos
	})
```

改成：

```go
	apiServer.SetPermissionsProvider(func() []api.UserPermissionInfo {
		infos := make([]api.UserPermissionInfo, 0)
		for _, ownerID := range config.OwnerUserIDs() {
			infos = append(infos, api.UserPermissionInfo{UserID: ownerID, Level: "full_access", IsOwner: true})
		}
		for _, perm := range handler.PermissionsSnapshot() {
			infos = append(infos, api.UserPermissionInfo{
				UserID: perm.UserID, Level: string(perm.Level), DailyLimit: perm.DailyLimit, Blocked: perm.Blocked,
				Persona: perm.Persona,
			})
		}
		return infos
	})
```

- [ ] **Step 5: 新增四个 apiServer wiring 闭包**

紧跟上面那段 `SetPermissionsProvider`（在 `SetPermissionController`/`SetPermissionBlockController` 那几段之后、`SetUsageProvider` 之前）加：

```go
	apiServer.SetPersonasProvider(func() ([]api.PersonaInfo, error) {
		list, err := persona.List(personaDir)
		if err != nil {
			return nil, err
		}
		infos := make([]api.PersonaInfo, 0, len(list))
		for _, p := range list {
			infos = append(infos, api.PersonaInfo{Name: p.Name, Text: p.Text})
		}
		return infos, nil
	})
	apiServer.SetPersonaSaveController(func(_ context.Context, req api.PersonaSaveRequest) (api.PersonaInfo, error) {
		if err := persona.Save(personaDir, req.Name, req.Text); err != nil {
			return api.PersonaInfo{}, err
		}
		return api.PersonaInfo{Name: req.Name, Text: req.Text}, nil
	})
	apiServer.SetPersonaDeleteController(func(_ context.Context, req api.PersonaDeleteRequest) error {
		return persona.Delete(personaDir, req.Name)
	})
	apiServer.SetPermissionPersonaController(func(_ context.Context, req api.PermissionPersonaRequest) (api.UserPermissionInfo, error) {
		if config.IsOwner(req.UserID) {
			return api.UserPermissionInfo{}, fmt.Errorf("cannot set persona for the owner")
		}
		configMu.Lock()
		if cfg.UserPersonas == nil {
			cfg.UserPersonas = make(map[string]string)
		}
		if req.Persona == "" {
			delete(cfg.UserPersonas, req.UserID)
		} else {
			cfg.UserPersonas[req.UserID] = req.Persona
		}
		saveErr := config.Save(cfg)
		existing := cfg.UserPermissions[req.UserID]
		configMu.Unlock()
		if saveErr != nil {
			return api.UserPermissionInfo{}, saveErr
		}

		handler.SetUserPersona(req.UserID, req.Persona)
		return api.UserPermissionInfo{
			UserID: req.UserID, Level: string(existing.Level), DailyLimit: existing.DailyLimit,
			Blocked: existing.Blocked, Persona: req.Persona,
		}, nil
	})
```

- [ ] **Step 6: 编译 + 全量测试**

Run: `cd ~/AiCodeProject/weclaw && go build ./... && go test ./...`
Expected: 编译成功，全部测试 PASS

- [ ] **Step 7: 手动冒烟（真实二进制 + curl，不用真微信号）**

```bash
cd ~/AiCodeProject/weclaw
go build -o /tmp/weclaw-test .
WECLAW_API_ADDR=127.0.0.1:18099 /tmp/weclaw-test start &
sleep 2
curl -s http://127.0.0.1:18099/api/internal/personas | python3 -m json.tool
curl -s -X POST http://127.0.0.1:18099/api/internal/personas -d '{"name":"vip","text":"VIP 测试人格"}' | python3 -m json.tool
curl -s http://127.0.0.1:18099/api/internal/personas | python3 -m json.tool
curl -s -X POST http://127.0.0.1:18099/api/internal/permissions/persona -d '{"user_id":"smoke-test@im.wechat","persona":"vip"}' | python3 -m json.tool
curl -s -X POST http://127.0.0.1:18099/api/internal/personas/delete -d '{"name":"default"}' | python3 -m json.tool  # 期望报错，default 不可删
kill %1
```

Expected：第一条 curl 返回含 `default` 人格；第二条创建 `vip` 成功；第三条能看到两个人格；第四条绑定成功返回 `"persona":"vip"`；第五条返回 400 错误（拒绝删除 default）。

- [ ] **Step 8: Commit**

```bash
cd ~/AiCodeProject/weclaw
git add cmd/start.go
git commit -m "Wire persona storage and API endpoints into weclaw startup"
```

---

## Task 8: wx-clawbot `chat_viewer.py`（18022 后台）— 人格管理面板

这个任务在另一个仓库：`~/AiCodeProject/wx-clawbot`。18022 是一个纯 Python 标准库写的代理页面，所有设置类面板（访问模式、权限）都是「HTML/JS 内嵌在 `chat_viewer.py` 一个文件里 + Python 侧转发调用 weclaw 18011 的内部 API」，本任务照抄这个既有模式。

**Files:**
- Modify: `~/AiCodeProject/wx-clawbot/scripts/chat_viewer.py`

**Interfaces:**
- Consumes：Task 6 新增的三个 weclaw 内部 API：`GET/POST /api/internal/personas`、`POST /api/internal/personas/delete`、`POST /api/internal/permissions/persona`
- Produces：18022 页面新增 `/api/personas`、`/api/personas/delete`、`/api/permissions/persona` 代理端点 + 「人格管理」面板 + 权限表格新增「人格」列

- [ ] **Step 1: 加 URL 常量**

在文件顶部 `WECLAW_PERMISSIONS_USAGE_URL` 那一行之后加：

```python
WECLAW_PERSONAS_URL = "http://127.0.0.1:18011/api/internal/personas"
WECLAW_PERSONA_DELETE_URL = "http://127.0.0.1:18011/api/internal/personas/delete"
WECLAW_PERMISSIONS_PERSONA_URL = "http://127.0.0.1:18011/api/internal/permissions/persona"
```

- [ ] **Step 2: 加 Python 侧代理函数**

紧跟 `_weclaw_permissions_usage()` 函数之后加：

```python
def _weclaw_personas():
    try:
        with urllib.request.urlopen(WECLAW_PERSONAS_URL, timeout=1.5) as resp:
            payload = json.loads(resp.read().decode("utf-8"))
        return {"online": True, "personas": payload.get("personas", [])}
    except (OSError, ValueError, urllib.error.URLError) as exc:
        return {"online": False, "personas": [], "error": str(exc)}


def _save_weclaw_persona(name, text):
    payload = {"name": name, "text": text}
    request = urllib.request.Request(
        WECLAW_PERSONAS_URL,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=5) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", "replace").strip()
        raise ValueError(detail or f"WeClaw 返回 {exc.code}") from exc
    except (OSError, ValueError, urllib.error.URLError) as exc:
        raise ValueError(str(exc)) from exc


def _delete_weclaw_persona(name):
    request = urllib.request.Request(
        WECLAW_PERSONA_DELETE_URL,
        data=json.dumps({"name": name}).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=5) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", "replace").strip()
        raise ValueError(detail or f"WeClaw 返回 {exc.code}") from exc
    except (OSError, ValueError, urllib.error.URLError) as exc:
        raise ValueError(str(exc)) from exc


def _set_weclaw_persona_binding(user_id, persona_name):
    payload = {"user_id": user_id, "persona": persona_name}
    request = urllib.request.Request(
        WECLAW_PERMISSIONS_PERSONA_URL,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=5) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", "replace").strip()
        raise ValueError(detail or f"WeClaw 返回 {exc.code}") from exc
    except (OSError, ValueError, urllib.error.URLError) as exc:
        raise ValueError(str(exc)) from exc
```

- [ ] **Step 3: `do_GET` 加分支**

紧跟 `elif parsed.path == "/api/permissions/usage": ...` 那个分支之后加：

```python
        elif parsed.path == "/api/personas":
            body = json.dumps(_weclaw_personas(), ensure_ascii=False).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json; charset=utf-8")
```

- [ ] **Step 4: `do_POST` 扩展白名单和分支，放宽请求体上限**

把：

```python
    def do_POST(self):
        path = urlparse(self.path).path
        if path not in {"/api/user-notes", "/api/weclaw-accounts/state", "/api/weclaw-accounts/remove", "/api/settings/message-merge", "/api/settings/media-retention", "/api/settings/access-mode", "/api/permissions", "/api/permissions/block"}:
            self.send_response(404)
            self.end_headers()
            return
        try:
            size = int(self.headers.get("Content-Length", "0"))
            if size <= 0 or size > 2048:
                raise ValueError("invalid request size")
```

改成：

```python
    def do_POST(self):
        path = urlparse(self.path).path
        if path not in {"/api/user-notes", "/api/weclaw-accounts/state", "/api/weclaw-accounts/remove", "/api/settings/message-merge", "/api/settings/media-retention", "/api/settings/access-mode", "/api/permissions", "/api/permissions/block", "/api/personas", "/api/personas/delete", "/api/permissions/persona"}:
            self.send_response(404)
            self.end_headers()
            return
        try:
            size = int(self.headers.get("Content-Length", "0"))
            # 20000：留够一段几千字的人格文本（原来的 2048 只够短设置项）。
            if size <= 0 or size > 20000:
                raise ValueError("invalid request size")
```

紧跟 `elif path == "/api/permissions/block": ...` 那个分支之后、`else:`（默认 `/api/permissions` 分支）之前插入：

```python
            elif path == "/api/personas":
                name = str(payload.get("name", "")).strip()
                text = str(payload.get("text", ""))
                if not re.match(r"^[A-Za-z0-9_-]+$", name) or len(text) > 4000:
                    raise ValueError("invalid persona name or text")
                body = json.dumps(_save_weclaw_persona(name, text), ensure_ascii=False).encode("utf-8")
            elif path == "/api/personas/delete":
                name = str(payload.get("name", "")).strip()
                if not re.match(r"^[A-Za-z0-9_-]+$", name):
                    raise ValueError("invalid persona name")
                body = json.dumps(_delete_weclaw_persona(name), ensure_ascii=False).encode("utf-8")
            elif path == "/api/permissions/persona":
                user_id = str(payload.get("user_id", ""))
                persona_name = str(payload.get("persona", ""))
                known_users = {msg.get("user") for msg in cached_messages()}
                if user_id not in known_users:
                    raise ValueError("invalid user")
                if persona_name and not re.match(r"^[A-Za-z0-9_-]+$", persona_name):
                    raise ValueError("invalid persona name")
                body = json.dumps(_set_weclaw_persona_binding(user_id, persona_name), ensure_ascii=False).encode("utf-8")
```

- [ ] **Step 5: HTML — 加「人格管理」面板，权限表格加一列**

紧挨着现有的权限面板（`<div class="panel collapsible table-panel" data-collapse-key="permissions">...`）**之前**插入一个新面板：

```html
      <div class="panel collapsible" data-collapse-key="personas"><button class="panel-head" type="button"><h3>人格管理</h3><span class="chev">▾</span></button><div class="panel-body"><div class="section-desc">每个人格是一段独立的人设文本；在下面「用户」表格里给某个用户绑定人格后，该用户跟 claude 聊天时就会用这段文本代替默认人格。default 人格不可删除，未绑定的用户都会用它。</div><div id="personasBody"></div></div></div>
```

把权限表格的表头：

```html
<table class="table"><thead><tr><th>用户</th><th>权限档位</th><th>今日用量</th><th></th><th>能否发消息</th></tr></thead><tbody id="permissionsBody"></tbody></table>
```

改成（加一列「人格」）：

```html
<table class="table"><thead><tr><th>用户</th><th>权限档位</th><th>今日用量</th><th>人格</th><th></th><th>能否发消息</th></tr></thead><tbody id="permissionsBody"></tbody></table>
```

- [ ] **Step 6: JS — state、refresh()、renderAll() 接入 personas**

把 `state` 初始化：

```javascript
let state={messages:[],overview:null,registry:null,userNotes:{},weclawAccounts:null,deletedAccounts:null,messageMergeSettings:null,mediaRetentionSettings:null,accessModeSettings:null,permissions:null,usage:null};
```

改成：

```javascript
let state={messages:[],overview:null,registry:null,userNotes:{},weclawAccounts:null,deletedAccounts:null,messageMergeSettings:null,mediaRetentionSettings:null,accessModeSettings:null,permissions:null,usage:null,personas:null};
```

把 `refresh()` 函数：

```javascript
async function refresh(){try{const [mr,or,ar,sr,mrr,amr,pr,ur,dr]=await Promise.all([fetch('/api/messages'),fetch('/api/overview'),fetch('/api/weclaw-accounts'),fetch('/api/settings/message-merge'),fetch('/api/settings/media-retention'),fetch('/api/settings/access-mode'),fetch('/api/permissions'),fetch('/api/permissions/usage'),fetch('/api/weclaw-accounts/deleted')]);const md=await mr.json();const od=await or.json();const ad=await ar.json();const sd=await sr.json();const mrd=await mrr.json();const amd=await amr.json();const pd=await pr.json();const ud=await ur.json();const dd=await dr.json();state.messages=md.messages;state.registry=md.identity_registry;state.userNotes=md.user_notes||{};state.overview=od;state.weclawAccounts=ad;state.deletedAccounts=dd;state.messageMergeSettings=sd;state.mediaRetentionSettings=mrd;state.accessModeSettings=amd;state.permissions=pd;state.usage=ud;renderAll()}catch(e){$('topSub').textContent='连不上服务：'+e.message;$('svcDot').className='dot bad';$('svcText').textContent='查看页异常'}}
```

改成：

```javascript
async function refresh(){try{const [mr,or,ar,sr,mrr,amr,pr,ur,dr,zr]=await Promise.all([fetch('/api/messages'),fetch('/api/overview'),fetch('/api/weclaw-accounts'),fetch('/api/settings/message-merge'),fetch('/api/settings/media-retention'),fetch('/api/settings/access-mode'),fetch('/api/permissions'),fetch('/api/permissions/usage'),fetch('/api/weclaw-accounts/deleted'),fetch('/api/personas')]);const md=await mr.json();const od=await or.json();const ad=await ar.json();const sd=await sr.json();const mrd=await mrr.json();const amd=await amr.json();const pd=await pr.json();const ud=await ur.json();const dd=await dr.json();const zd=await zr.json();state.messages=md.messages;state.registry=md.identity_registry;state.userNotes=md.user_notes||{};state.overview=od;state.weclawAccounts=ad;state.deletedAccounts=dd;state.messageMergeSettings=sd;state.mediaRetentionSettings=mrd;state.accessModeSettings=amd;state.permissions=pd;state.usage=ud;state.personas=zd;renderAll()}catch(e){$('topSub').textContent='连不上服务：'+e.message;$('svcDot').className='dot bad';$('svcText').textContent='查看页异常'}}
```

把 `renderAll()` 里 settings 分支：

```javascript
if(activeView==='settings'||force){renderAccountControls(state.weclawAccounts);renderDeletedAccounts(state.deletedAccounts);renderMessageMergeSettings(state.messageMergeSettings);renderMediaRetentionSettings(state.mediaRetentionSettings);renderAccessModeSettings(state.accessModeSettings);renderPermissions()}
```

改成：

```javascript
if(activeView==='settings'||force){renderAccountControls(state.weclawAccounts);renderDeletedAccounts(state.deletedAccounts);renderMessageMergeSettings(state.messageMergeSettings);renderMediaRetentionSettings(state.mediaRetentionSettings);renderAccessModeSettings(state.accessModeSettings);renderPersonas();renderPermissions()}
```

- [ ] **Step 7: JS — 新增 `renderPersonas()`**

紧跟 `renderAccessModeSettings` 函数定义之后加：

```javascript
function renderPersonas(){
  const el=$('personasBody');
  if(!state.personas||!state.personas.online){el.innerHTML='<div class="empty">无法读取 18011 的人格数据：'+esc(state.personas?.error||'WeClaw 服务离线')+'</div>';return}
  const list=state.personas.personas||[];
  const wrap=document.createElement('div');wrap.className='settings-form';
  for(const p of list){
    const row=document.createElement('div');row.className='setting-row';
    const label=document.createElement('label');label.textContent=p.name+(p.name==='default'?'（默认，不可删除）':'');
    const textarea=document.createElement('textarea');textarea.value=p.text;textarea.rows=3;textarea.style.width='100%';
    row.append(label,textarea);
    const actions=document.createElement('div');
    const saveBtn=document.createElement('button');saveBtn.className='account-action';saveBtn.textContent='保存';
    saveBtn.onclick=async()=>{saveBtn.disabled=true;saveBtn.textContent='保存中…';try{const r=await fetch('/api/personas',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:p.name,text:textarea.value})});const d=await r.json();if(!r.ok)throw new Error(d.error||'保存失败');showToast('已保存人格「'+p.name+'」');await refresh()}catch(e){showToast('保存失败：'+e.message,'error')}finally{saveBtn.disabled=false;saveBtn.textContent='保存'}};
    actions.appendChild(saveBtn);
    if(p.name!=='default'){
      const delBtn=document.createElement('button');delBtn.className='account-action remove';delBtn.textContent='删除';
      delBtn.onclick=async()=>{if(!confirm('删除人格「'+p.name+'」？已绑定这个人格的用户会回退到默认人格。'))return;delBtn.disabled=true;try{const r=await fetch('/api/personas/delete',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:p.name})});const d=await r.json();if(!r.ok)throw new Error(d.error||'删除失败');showToast('已删除人格「'+p.name+'」');await refresh()}catch(e){delBtn.disabled=false;showToast('删除失败：'+e.message,'error')}};
      actions.appendChild(delBtn)
    }
    row.appendChild(actions);wrap.appendChild(row)
  }
  const newRow=document.createElement('div');newRow.className='setting-row';
  const newLabel=document.createElement('label');newLabel.textContent='新建人格（名称只能用字母数字下划线短横线）';
  const newName=document.createElement('input');newName.type='text';newName.placeholder='persona-name';
  const newText=document.createElement('textarea');newText.rows=3;newText.style.width='100%';newText.placeholder='人格文本';
  newRow.append(newLabel,newName,newText);
  const newBtn=document.createElement('button');newBtn.className='settings-save';newBtn.textContent='新建';
  newBtn.onclick=async()=>{const name=newName.value.trim();if(!/^[A-Za-z0-9_-]+$/.test(name)){alert('名称只能包含字母、数字、下划线、短横线');return}newBtn.disabled=true;try{const r=await fetch('/api/personas',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name,text:newText.value})});const d=await r.json();if(!r.ok)throw new Error(d.error||'新建失败');showToast('已新建人格「'+name+'」');await refresh()}catch(e){showToast('新建失败：'+e.message,'error')}finally{newBtn.disabled=false}};
  newRow.appendChild(newBtn);wrap.appendChild(newRow);
  el.replaceChildren(wrap)
}
```

- [ ] **Step 8: JS — 整个替换 `renderPermissions()`，加人格列**

把现有的整个 `renderPermissions` 函数替换为：

```javascript
function renderPermissions(){
  const el=$('permissionsBody');
  if(!state.permissions||!state.permissions.online){el.innerHTML='<tr><td colspan="6">无法读取 18011 的权限数据：'+esc(state.permissions?.error||'WeClaw 服务离线')+'</td></tr>';for(const id in permissionRows)delete permissionRows[id];return}
  const personaNames=(state.personas&&state.personas.online?(state.personas.personas||[]):[]).map(p=>p.name);
  if(!personaNames.includes('default'))personaNames.unshift('default');
  const configured={};const ownerIds=new Set();
  for(const u of state.permissions.users||[]){if(u.is_owner){ownerIds.add(u.user_id);continue}configured[u.user_id]=u}
  const usageByUser={};for(const u of (state.usage?.usage||[]))usageByUser[u.user_id]=u;
  const users=(state.overview?.users?.items||[]).filter(u=>u.user_id);
  if(!users.length){el.innerHTML='<tr><td colspan="6">暂无用户</td></tr>';for(const id in permissionRows)delete permissionRows[id];return}
  const seen=new Set();
  for(const u of users){
    seen.add(u.user_id);
    const isOwner=ownerIds.has(u.user_id);
    const usage=usageByUser[u.user_id];
    let row=permissionRows[u.user_id];
    const nameHtml=(blocked)=>'<div class="user-name">'+esc(userName(u))+(blocked?'<span class="badge blocked">已拉黑</span>':'')+'</div><div class="user-id">'+esc(u.user_id)+'</div>';
    if(row){
      const nameTd=row.tr.firstChild;nameTd.innerHTML=nameHtml(row.blocked);
      if(isOwner){row.usedSpan.textContent=(usage?.count||0)+' / 不限'}
      else{
        row.usedSpan.textContent=(usage?.count||0)+' / ';
        const perm=configured[u.user_id]||{persona:''};
        const wanted=perm.persona||'default';
        if(row.personaSelect&&document.activeElement!==row.personaSelect&&row.personaSelect.value!==wanted){row.personaSelect.value=wanted}
      }
      continue
    }
    const tr=document.createElement('tr');
    const nameTd=document.createElement('td');nameTd.innerHTML=nameHtml(false);
    if(isOwner){
      const levelTd=document.createElement('td');levelTd.innerHTML=statusHtml('active','管理员');
      const usageTd=document.createElement('td');const usedSpan=document.createElement('span');usedSpan.textContent=(usage?.count||0)+' / 不限';usageTd.appendChild(usedSpan);
      const personaTd=document.createElement('td');personaTd.textContent='完整人格 · 不可改';
      const actionTd=document.createElement('td');const blockTd=document.createElement('td');
      tr.append(nameTd,levelTd,usageTd,personaTd,actionTd,blockTd);el.appendChild(tr);
      permissionRows[u.user_id]={tr,usedSpan,blocked:false};continue
    }
    const perm=configured[u.user_id]||{level:'read_only',daily_limit:0,blocked:false,persona:''};
    const selectTd=document.createElement('td');const select=document.createElement('select');select.className='permission-select';
    for(const [value,label] of [['read_only','只读'],['workspace_write','可读写']]){const opt=document.createElement('option');opt.value=value;opt.textContent=label;if(perm.level===value)opt.selected=true;select.appendChild(opt)}
    selectTd.appendChild(select);
    const usageTd=document.createElement('td');const limit=usage?.limit||perm.daily_limit||50;const usedSpan=document.createElement('span');usedSpan.textContent=(usage?.count||0)+' / ';const limitInput=document.createElement('input');limitInput.type='number';limitInput.min='1';limitInput.max='500';limitInput.value=limit;limitInput.className='permission-limit';usageTd.append(usedSpan,limitInput);
    const personaTd=document.createElement('td');const personaSelect=document.createElement('select');personaSelect.className='permission-select';
    for(const name of personaNames){const opt=document.createElement('option');opt.value=name;opt.textContent=name;if((perm.persona||'default')===name)opt.selected=true;personaSelect.appendChild(opt)}
    personaSelect.onchange=async()=>{const next=personaSelect.value;if(!confirm('把「'+userName(u)+'」的人格改成「'+next+'」？'))return;personaSelect.disabled=true;try{const r=await fetch('/api/permissions/persona',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({user_id:u.user_id,persona:next})});const d=await r.json();if(!r.ok)throw new Error(d.error||'保存失败');showToast('已把「'+userName(u)+'」的人格改成「'+next+'」')}catch(e){showToast('保存失败：'+e.message,'error')}finally{personaSelect.disabled=false}};
    personaTd.appendChild(personaSelect);
    const actionTd=document.createElement('td');const button=document.createElement('button');button.className='account-action';button.textContent='保存';
    button.onclick=async()=>{const label=select.options[select.selectedIndex].textContent;const dailyLimit=Number(limitInput.value);if(!Number.isInteger(dailyLimit)||dailyLimit<1||dailyLimit>500){alert('请填写 1-500 之间的每日额度');return}if(!confirm('把「'+userName(u)+'」的权限改成「'+label+'」、每日额度改成 '+dailyLimit+'？'))return;button.disabled=true;button.textContent='保存中…';try{const r=await fetch('/api/permissions',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({user_id:u.user_id,level:select.value,daily_limit:dailyLimit})});const d=await r.json();if(!r.ok)throw new Error(d.error||'保存失败');button.textContent='保存';button.disabled=false;showToast('已保存「'+userName(u)+'」的权限设置');refresh()}catch(e){button.textContent='保存';button.disabled=false;showToast('保存失败：'+e.message,'error')}};
    actionTd.appendChild(button);
    const blockTd=document.createElement('td');const blockButton=document.createElement('button');
    const applyBlockedState=(blocked)=>{row.blocked=blocked;blockButton.textContent=blocked?'解封':'拉黑';blockButton.className='account-action '+(blocked?'enable':'remove');select.disabled=blocked;limitInput.disabled=blocked;personaSelect.disabled=blocked;button.disabled=blocked;nameTd.innerHTML=nameHtml(blocked)};
    blockButton.onclick=async()=>{const next=!row.blocked;const msg=next?('确认拉黑「'+userName(u)+'」？拉黑后不管权限档位怎么设，对方发的任何消息都进不来，需要手动解封。'):('解封「'+userName(u)+'」，恢复接收消息？');if(!confirm(msg))return;blockButton.disabled=true;blockButton.textContent=next?'拉黑中…':'解封中…';try{const r=await fetch('/api/permissions/block',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({user_id:u.user_id,blocked:next})});const d=await r.json();if(!r.ok)throw new Error(d.error||'操作失败');applyBlockedState(next);blockButton.disabled=false;showToast(next?('已拉黑「'+userName(u)+'」'):('已解封「'+userName(u)+'」'))}catch(e){blockButton.disabled=false;applyBlockedState(row.blocked);showToast('操作失败：'+e.message,'error')}};
    blockTd.appendChild(blockButton);
    tr.append(nameTd,selectTd,usageTd,personaTd,actionTd,blockTd);el.appendChild(tr);
    row={tr,usedSpan,blocked:false,personaSelect};permissionRows[u.user_id]=row;applyBlockedState(!!perm.blocked)
  }
  for(const id in permissionRows){if(!seen.has(id)){permissionRows[id].tr.remove();delete permissionRows[id]}}
}
```

- [ ] **Step 9: 语法检查**

Run: `cd ~/AiCodeProject/wx-clawbot && python3 -c "import ast; ast.parse(open('scripts/chat_viewer.py').read())"`
Expected: 无输出（无语法错误）

- [ ] **Step 10: 手动启动 + curl 冒烟**

```bash
cd ~/AiCodeProject/wx-clawbot
python3 scripts/chat_viewer.py &
sleep 1
curl -s http://127.0.0.1:18022/api/personas | python3 -m json.tool
kill %1
```

Expected：返回 JSON，`online` 字段视 weclaw 主服务是否在跑而定；不应有 500/连接被拒之外的异常。

- [ ] **Step 11: Commit（wx-clawbot 仓库）**

```bash
cd ~/AiCodeProject/wx-clawbot
git add scripts/chat_viewer.py
git commit -m "Add persona management panel to the 18022 permissions page"
```

---

## Task 9: 端到端联调 + 真实环境验证

**Files:** 无代码改动，纯验证。

- [ ] **Step 1: weclaw 全量测试 + 编译**

Run: `cd ~/AiCodeProject/weclaw && go test ./... && go build -o bin/weclaw .`
Expected: 全绿

- [ ] **Step 2: 重启 launchd 服务**

```bash
openclaw gateway restart 2>/dev/null  # 如果 wx-clawbot 走的是这个网关封装
# 或者直接重启 weclaw 自己的 launchd（按 wx-clawbot 项目实际的重启方式，参考 wx-clawbot/CLAUDE.md 里的常用命令）
```

（这一步的具体命令要跟梁师傅确认当前 weclaw 是通过哪个 launchd plist 常驻的——`docs/superpowers/specs/2026-07-23-user-persona-isolation-design.md` 没有覆盖这个信息，执行到这一步时问一下再动手重启，不要瞎猜命令直接执行。）

- [ ] **Step 3: 浏览器验收 18022**

打开 `http://127.0.0.1:18022/`，进「设置」页：
- 确认新出现「人格管理」面板，能看到 `default` 人格
- 编辑 `default` 人格文本，保存，刷新页面确认内容保留
- 新建一个测试人格，确认「用户」表格的「人格」下拉里能选到它
- 给一个已知非 owner 用户绑定这个测试人格，确认保存成功

- [ ] **Step 4: 真实微信冒烟（需要一个非 owner 测试号）**

用非 owner 微信号给 bot 发一条消息，确认：
- 回复内容不包含梁师傅的任何隐私信息（家庭、父亲、经济状况等）
- 回复语气/内容符合当前绑定的人格文本
- 用 owner 本人账号发消息，确认体验较改动前无变化（完整晚秋人设正常）

- [ ] **Step 5: 更新 wx-clawbot 记忆**

这一步不是代码任务，是收尾动作：这次改动解决了记忆库里 `weclaw-architecture-notes.md` 记录的「codex 全局人设覆盖所有用户（未修复）」这一条（虽然选择的方案是绕开 codex 而非修复它本身），需要在收工时更新这条记忆和 `weclaw-feature-gaps.md`，避免下个 session 又读到过期状态。

---

## Self-Review 备忘（写计划时已核对，供执行者复核）

- 已确认线上 `~/.weclaw/config.json` 的 `default_agent` 就是 `"codex"`，且非 owner 用户目前无法通过任何命令切换 agent（`messaging/handler.go` 的 `owner && strings.HasPrefix(...)` 系列门禁），因此 Task 4 修的是 `selectedAgent()` 的 fallback 逻辑本身，不是拦截一个「切换」动作。
- 已确认并同步修正了三个因为这个路由改动而断言过时行为的既有测试（`TestUserAgentSelectionsAreIndependent` 整个被拆成两个新测试；`TestNonOwnerAgentSwitchPrefixIsPlainText`/`TestNonOwnerCwdIsPlainText`/`TestQuotaExceededBlocksNonOwnerBeforeAgentCall` 里注册 fake agent 的名字从 `"codex"` 改成 `"claude"`）——这是本次改动的直接后果，不是范围蔓延。
- `selected` 返回值刻意在非 owner 分支恒定给 `true`（而不是 `false`），是为了保留 `sendToDefaultAgent`/`resetDefaultSession`/`handleImageMessage` 三处「`ag==nil && selected` 才尝试 `getAgent` 懒启动」的既有逻辑，否则 claude 没预先启动时非 owner 会永远卡在"系统恢复中"。
- wx-clawbot 的 `do_POST` 请求体上限从 2048 提到 20000 字节，是因为人格文本明显会超过原来只为几个数字/短字符串设计的上限；`/api/personas` 路径本身另外做了 4000 字符的业务层限制。
