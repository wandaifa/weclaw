# Codex 用户隔离（二期）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 非 owner 用户默认路由到一个隔离过的 codex 常驻进程（`codex-shared`，独立 `CODEX_HOME`，看不到 owner 的 `~/.codex/AGENTS.md`），claude 留作可切换的后备，切换能力后台开关默认关闭；owner 自己用 codex 的行为完全不变。

**Architecture:** 复用 weclaw 已有的"一个命令注册多个 agent 条目"机制（如现有 `openclaw`/`openclaw-acp`），新增 `codex-shared` 配置条目，`env.CODEX_HOME` 指向一个只软链 `auth.json`（保登录态同步）的隔离目录，天然排除 `AGENTS.md`/`memories`。`ACPAgent` 补齐 `PersonaAwareAgent` 接口（一期只有 claude 侧的 `CLIAgent` 有），复用一期的 `persona` 包和绑定解析逻辑，注入方式是 `thread/start` 的 `baseInstructions` 参数（而非 claude 侧的 `--append-system-prompt`）。`selectedAgent()` 的路由目标从一期写死的 `"claude"` 改成可配置，默认 `codex-shared`。

**Tech Stack:** Go（weclaw 主体不变）、Python 3 标准库（wx-clawbot 18022 后台不变）。

## Global Constraints

- 全部结论来自本次会话的真实实测（拿真实 `codex app-server` 进程手动跑 `initialize`→`thread/start`→`turn/start`），不是读文档/协议 schema 推断——`baseInstructions` 单独用不管用、`project_doc_max_bytes` 不管用（两次实测证伪）；`CODEX_HOME` 隔离目录管用（已验证）；`model`/`model_reasoning_effort` 通过 `thread/start` 独立生效（已验证，合法 `model_reasoning_effort` 值：`none`/`low`/`medium`/`high`/`xhigh`）。
- **安全红线**：`AllowNonOwnerAgentSwitch=true` 时，非 owner 打 `/codex` 必须解析到 `codex-shared`，永远不能解析到真实 `codex`（owner 专属）。owner 打 `/codex` 继续解析到真实 `codex`。同一个命令名，两套身份两种解析结果，不能共用一张不分身份的静态映射表。
- `codex-shared` 隔离目录只软链 `auth.json`，不软链 `AGENTS.md`/`memories`/真实 `config.toml`——精简版 `config.toml` 独立维护，必须显式设 `sandbox_workspace_write.network_access = true`（生图需要联网）。
- owner 使用 codex（真实 `codex` 条目）的行为不能有任何变化。
- 每个任务完成后运行 `go test ./...`（weclaw 目录）确认全绿，才能进入下一个任务。
- 人格文本存储复用一期的 `persona` 包和 `~/.weclaw/personas/*.md`，不新增存储。

---

## Task 1: `config` 包 — 新增三个字段

**Files:**
- Modify: `config/config.go`
- Modify: `config/config_test.go`

**Interfaces:**
- Produces：
  - `config.AgentConfig.ModelReasoningEffort string`（JSON key `model_reasoning_effort`）
  - `config.Config.NonOwnerDefaultAgent string`（JSON key `non_owner_default_agent`）
  - `config.Config.AllowNonOwnerAgentSwitch bool`（JSON key `allow_non_owner_agent_switch`）

- [ ] **Step 1: 写失败的测试**

在 `config/config_test.go` 末尾追加：

```go
func TestAgentConfigModelReasoningEffortRoundTrip(t *testing.T) {
	cfg := Config{
		Agents: map[string]AgentConfig{
			"codex-shared": {Type: "acp", Command: "codex", ModelReasoningEffort: "low"},
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if got := decoded.Agents["codex-shared"].ModelReasoningEffort; got != "low" {
		t.Fatalf("ModelReasoningEffort = %q, want %q", got, "low")
	}
}

func TestNonOwnerRoutingFieldsRoundTripAndDefaultToZeroValue(t *testing.T) {
	var cfg Config
	data := []byte(`{"agents":{}}`)
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal legacy config: %v", err)
	}
	if cfg.NonOwnerDefaultAgent != "" {
		t.Fatalf("NonOwnerDefaultAgent = %q, want empty for legacy config", cfg.NonOwnerDefaultAgent)
	}
	if cfg.AllowNonOwnerAgentSwitch {
		t.Fatal("AllowNonOwnerAgentSwitch = true, want false for legacy config")
	}

	cfg2 := Config{NonOwnerDefaultAgent: "codex-shared", AllowNonOwnerAgentSwitch: true, Agents: map[string]AgentConfig{}}
	data2, err := json.Marshal(cfg2)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded2 Config
	if err := json.Unmarshal(data2, &decoded2); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if decoded2.NonOwnerDefaultAgent != "codex-shared" || !decoded2.AllowNonOwnerAgentSwitch {
		t.Fatalf("round-trip = %+v, want NonOwnerDefaultAgent=codex-shared AllowNonOwnerAgentSwitch=true", decoded2)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ~/AiCodeProject/weclaw && go test ./config/... -run 'TestAgentConfigModelReasoningEffort|TestNonOwnerRoutingFields' -v`
Expected: 编译失败（字段不存在）

- [ ] **Step 3: 改 `config/config.go`**

`AgentConfig` struct 里，`SystemPrompt` 字段之后加一行：

```go
type AgentConfig struct {
	Type                 string            `json:"type"`                              // "acp", "cli", or "http"
	Command              string            `json:"command,omitempty"`                 // binary path (cli/acp type)
	Args                 []string          `json:"args,omitempty"`                    // extra args for command (e.g. ["acp"] for cursor)
	Aliases              []string          `json:"aliases,omitempty"`                 // custom trigger commands (e.g. ["gpt", "4o"])
	Cwd                  string            `json:"cwd,omitempty"`                     // working directory (workspace)
	Env                  map[string]string `json:"env,omitempty"`                     // extra environment variables (cli/acp type)
	Model                string            `json:"model,omitempty"`                   // model name
	ModelReasoningEffort string            `json:"model_reasoning_effort,omitempty"`  // "none"|"low"|"medium"|"high"|"xhigh" (acp/codex only)
	SystemPrompt         string            `json:"system_prompt,omitempty"`           // system prompt
	Endpoint             string            `json:"endpoint,omitempty"`                // API endpoint (http type)
	APIKey               string            `json:"api_key,omitempty"`                 // API key (http type)
	Headers              map[string]string `json:"headers,omitempty"`                 // extra HTTP headers (http type)
	MaxHistory           int               `json:"max_history,omitempty"`             // max history (http type)
}
```

`Config` struct 里，紧跟一期加的 `UserPersonas` 字段之后加两行：

```go
	DefaultPersona  string                    `json:"default_persona,omitempty"`
	UserPersonas    map[string]string         `json:"user_personas,omitempty"`
	NonOwnerDefaultAgent     string           `json:"non_owner_default_agent,omitempty"`    // 空 = "codex-shared"
	AllowNonOwnerAgentSwitch bool             `json:"allow_non_owner_agent_switch,omitempty"` // 默认 false
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd ~/AiCodeProject/weclaw && go test ./config/... -v`
Expected: 全部 PASS

- [ ] **Step 5: Commit**

```bash
cd ~/AiCodeProject/weclaw
git add config/config.go config/config_test.go
git commit -m "Add config fields for codex-shared model tier and non-owner default agent routing"
```

---

## Task 2: `agent` 包 — `ACPAgent` 实现 `PersonaAwareAgent`

**Files:**
- Modify: `agent/acp_agent.go`
- Create: `agent/acp_agent_persona_test.go`

**Interfaces:**
- Consumes：`agent.PersonaOverride{SafeMode bool; SystemPrompt string}`（一期已有，`SafeMode` 字段对 codex 无意义，此任务只读 `SystemPrompt`）
- Produces：`(*ACPAgent).SetPersonaOverride(conversationID string, override PersonaOverride)`（满足 `agent.PersonaAwareAgent`）

- [ ] **Step 1: 写失败的测试**

`agent/acp_agent_persona_test.go`：

```go
package agent

import "testing"

func TestACPAgentSetPersonaOverrideInvalidatesCachedThread(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	a.threads["user-a"] = "thread-1"
	a.sessions["user-a"] = "sess-1"
	a.sessionOwners["sess-1"] = "user-a"

	a.SetPersonaOverride("user-a", PersonaOverride{SystemPrompt: "脱敏人格"})

	a.mu.Lock()
	_, threadStillCached := a.threads["user-a"]
	_, sessionStillCached := a.sessions["user-a"]
	got := a.personas["user-a"]
	a.mu.Unlock()

	if threadStillCached {
		t.Fatal("SetPersonaOverride should invalidate the cached thread so the next turn re-does thread/start with the new baseInstructions")
	}
	if sessionStillCached {
		t.Fatal("SetPersonaOverride should also clear the legacy-ACP session cache for the same conversationID")
	}
	if got.SystemPrompt != "脱敏人格" {
		t.Fatalf("stored override = %+v, want SystemPrompt=脱敏人格", got)
	}
}

func TestACPAgentSetPersonaOverrideIsNoOpWhenUnchanged(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	a.SetPersonaOverride("user-a", PersonaOverride{SystemPrompt: "脱敏人格"})
	a.threads["user-a"] = "thread-1" // simulate a real thread/start having happened since

	a.SetPersonaOverride("user-a", PersonaOverride{SystemPrompt: "脱敏人格"}) // identical override

	a.mu.Lock()
	_, threadStillCached := a.threads["user-a"]
	a.mu.Unlock()
	if !threadStillCached {
		t.Fatal("setting an identical override must not invalidate an already-established thread")
	}
}

func TestACPAgentSetPersonaOverrideIsPerConversation(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
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

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ~/AiCodeProject/weclaw && go test ./agent/... -run TestACPAgentSetPersonaOverride -v`
Expected: 编译失败（`a.personas` 字段、`SetPersonaOverride` 方法都不存在）

- [ ] **Step 3: 改 `agent/acp_agent.go`**

`ACPAgent` struct 里，紧跟 `policies` 字段之后加一行：

```go
	policies      map[string]ConversationPolicy // conversationID -> sandbox tier
	personas      map[string]PersonaOverride    // conversationID -> persona override (codex app-server only)
```

`NewACPAgent` 里，紧跟 `policies: make(...)` 之后加初始化：

```go
		policies:      make(map[string]ConversationPolicy),
		personas:      make(map[string]PersonaOverride),
```

紧跟 `SetConversationPolicy` 方法之后加新方法，逐字复刻它的"值不变就跳过、值变了就清缓存强制下一轮重建 thread"模式：

```go
// SetPersonaOverride records the persona override for one conversationID.
// Like SetConversationPolicy, codex ties baseInstructions to a thread at
// creation time, so a change invalidates the cached thread/session for that
// conversation to force a rebuild with the new baseInstructions on the next
// turn — persona rebinds take effect on the very next message, not just on
// a brand-new conversation.
func (a *ACPAgent) SetPersonaOverride(conversationID string, override PersonaOverride) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if old, ok := a.personas[conversationID]; ok && old == override {
		return
	}
	a.personas[conversationID] = override
	delete(a.threads, conversationID)
	if oldSessionID, ok := a.sessions[conversationID]; ok {
		delete(a.sessionOwners, oldSessionID)
	}
	delete(a.sessions, conversationID)
}
```

在 `getOrCreateThread` 里（构造 `thread/start` 的 `params` 那段），紧跟 `agentCwd := a.cwd` 之后加一行读 override，紧跟 `if a.model != "" { params["model"] = a.model }` 之后加注入：

```go
	a.mu.Lock()
	policy := a.effectivePolicyLocked(conversationID)
	override := a.personas[conversationID]
	agentCwd := a.cwd
	a.mu.Unlock()
	sandboxMode, _, cwd := codexSandboxParams(policy, agentCwd)

	params := map[string]interface{}{
		"approvalPolicy": "never",
		"cwd":            cwd,
		"sandbox":        sandboxMode,
	}
	if a.model != "" {
		params["model"] = a.model
	}
	if a.modelReasoningEffort != "" {
		params["config"] = map[string]interface{}{"model_reasoning_effort": a.modelReasoningEffort}
	}
	if override.SystemPrompt != "" {
		params["baseInstructions"] = override.SystemPrompt
	}
```

（`a.modelReasoningEffort` 字段和赋值在 Task 4 加；这一步先把 `getOrCreateThread` 改完，`a.modelReasoningEffort` 还不存在时这一步会编译失败，属于正常——Task 4 会补上那个字段，Task 2 的验证范围只覆盖 `baseInstructions`/`SetPersonaOverride` 这部分，跑测试前先看 Step 4 的说明。）

- [ ] **Step 4: 跑测试确认通过（先注释掉 `modelReasoningEffort` 那两行）**

在正式加 Task 4 的字段之前，`go build` 会因为 `a.modelReasoningEffort` 未定义而失败。为了让 Task 2 可以独立验证，**Step 3 里 `if a.modelReasoningEffort != "" {...}` 这两行本任务先不加**，只加 `baseInstructions` 那三行。真实顺序：Step 3 只贴 `override`/`baseInstructions` 相关的改动，不贴 `modelReasoningEffort` 相关的改动（那部分挪到 Task 4 一起做，避免这里编译不过）。改完后跑：

Run: `cd ~/AiCodeProject/weclaw && go test ./agent/... -v`
Expected: 全部 PASS（含 `acp_agent_test.go` 里原有的 `TestGetOrCreateThreadAppliesConversationPolicy` 等测试不受影响）

- [ ] **Step 5: Commit**

```bash
cd ~/AiCodeProject/weclaw
git add agent/acp_agent.go agent/acp_agent_persona_test.go
git commit -m "Implement PersonaAwareAgent on ACPAgent, injecting baseInstructions per conversation"
```

---

## Task 3: `agent` 包 — 修复生成图片路径认错 home 的 bug

**Files:**
- Modify: `agent/acp_agent.go`
- Modify: `agent/acp_agent_test.go`（如果已有相关测试则改，没有则新增用例）

**Interfaces:**
- Consumes：无
- Produces：`codexGeneratedImagePaths(home, threadID string) []string`（原签名 `codexGeneratedImagePaths(threadID string)`，新增 `home` 参数）；`(*ACPAgent).codexHome() string`

- [ ] **Step 1: 写失败的测试**

在 `agent/acp_agent_test.go` 末尾追加（先读一下这个文件现有的测试辅助函数/风格，跟已有测试保持一致的写法）：

```go
func TestCodexHomeUsesEnvOverrideWhenSet(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Env: map[string]string{"CODEX_HOME": "/tmp/isolated-codex-home"}})
	if got := a.codexHome(); got != "/tmp/isolated-codex-home" {
		t.Fatalf("codexHome() = %q, want %q", got, "/tmp/isolated-codex-home")
	}
}

func TestCodexHomeFallsBackToRealHomeWhenUnset(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine real home dir in this environment")
	}
	if got := a.codexHome(); got != realHome {
		t.Fatalf("codexHome() = %q, want real home %q", got, realHome)
	}
}

func TestCodexGeneratedImagePathsUsesGivenHomeNotRealHome(t *testing.T) {
	dir := t.TempDir()
	imgDir := filepath.Join(dir, ".codex", "generated_images", "thread-x")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	imgPath := filepath.Join(imgDir, "pic.png")
	if err := os.WriteFile(imgPath, []byte("fake-png"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got := codexGeneratedImagePaths(dir, "thread-x")
	if len(got) != 1 || got[0] != imgPath {
		t.Fatalf("codexGeneratedImagePaths(%q, thread-x) = %v, want [%q]", dir, got, imgPath)
	}

	// A different home must not see it.
	otherDir := t.TempDir()
	if got := codexGeneratedImagePaths(otherDir, "thread-x"); len(got) != 0 {
		t.Fatalf("codexGeneratedImagePaths(otherHome, thread-x) = %v, want empty", got)
	}
}
```

需要在文件顶部 import 块确认 `"os"`、`"path/filepath"` 已存在（这个文件大概率已经 import 了，先读文件确认，没有就加）。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ~/AiCodeProject/weclaw && go test ./agent/... -run 'TestCodexHome|TestCodexGeneratedImagePaths' -v`
Expected: 编译失败（`codexHome` 方法不存在，`codexGeneratedImagePaths` 签名不对）

- [ ] **Step 3: 改 `agent/acp_agent.go`**

加一个新方法（放在 `SetCwd` 附近，或紧跟 `ACPAgent` 其他简单 getter 风格的方法旁边）：

```go
// codexHome returns the CODEX_HOME this agent instance actually runs with:
// the env override if the agent config set one (e.g. codex-shared's
// isolated home), otherwise the real OS home directory (owner's real
// codex instance, which has no CODEX_HOME override).
func (a *ACPAgent) codexHome() string {
	if home := a.env["CODEX_HOME"]; home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
```

把 `codexGeneratedImagePaths`、`snapshotCodexGeneratedImages`、`appendNewCodexGeneratedImages` 三个函数都加一个 `home string` 参数，去掉内部的 `os.UserHomeDir()` 调用：

```go
func codexGeneratedImagePaths(home, threadID string) []string {
	if threadID == "" || home == "" {
		return nil
	}
	root := filepath.Join(home, ".codex", "generated_images", threadID)
	var paths []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if isCodexGeneratedImagePath(path) {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return nil
	}
	sort.Strings(paths)
	return paths
}

func snapshotCodexGeneratedImages(home, threadID string) map[string]struct{} {
	paths := codexGeneratedImagePaths(home, threadID)
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		seen[path] = struct{}{}
	}
	return seen
}

func appendNewCodexGeneratedImages(reply, home, threadID string, existing map[string]struct{}) string {
```

（`appendNewCodexGeneratedImages` 的函数体内部逻辑不变，只是签名多了 `home` 参数，内部调用 `codexGeneratedImagePaths(threadID)` 的地方改成 `codexGeneratedImagePaths(home, threadID)` ——读一下这个函数当前的完整实现，只改调用点和签名，函数体其余逻辑原样保留。）

更新两个调用点（`chatCodexAppServerWithInput` 方法内）：

```go
	existingGeneratedImages := snapshotCodexGeneratedImages(a.codexHome(), threadID)
```

```go
				result = appendNewCodexGeneratedImages(result, a.codexHome(), threadID, existingGeneratedImages)
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd ~/AiCodeProject/weclaw && go test ./agent/... -v`
Expected: 全部 PASS

- [ ] **Step 5: Commit**

```bash
cd ~/AiCodeProject/weclaw
git add agent/acp_agent.go agent/acp_agent_test.go
git commit -m "Fix generated-image lookup to use the agent's actual CODEX_HOME, not the real OS home"
```

---

## Task 4: `agent` 包 — `ACPAgentConfig`/`ACPAgent` 加 `model_reasoning_effort` 透传

**Files:**
- Modify: `agent/acp_agent.go`
- Modify: `agent/acp_agent_test.go`

**Interfaces:**
- Consumes：`config.AgentConfig.ModelReasoningEffort`（Task 1）
- Produces：`agent.ACPAgentConfig.ModelReasoningEffort string`；`getOrCreateThread` 在设置了该值时，`thread/start` 的 `params["config"]` 带 `model_reasoning_effort`

**这个任务把 Task 2 里注释掉的 `modelReasoningEffort` 那部分正式加上。**

- [ ] **Step 1: 写失败的测试**

在 `agent/acp_agent_test.go` 里找到已有的 `TestGetOrCreateThreadAppliesConversationPolicy`（或同类直接调 `getOrCreateThread` 的测试），照着它的 stub/mock RPC 写法加一个新测试：

```go
func TestGetOrCreateThreadIncludesModelReasoningEffortWhenConfigured(t *testing.T) {
	var capturedParams map[string]interface{}
	a := NewACPAgent(ACPAgentConfig{Command: "codex", ModelReasoningEffort: "low"})
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method == "thread/start" {
			b, _ := json.Marshal(params)
			json.Unmarshal(b, &capturedParams)
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		}
		return json.RawMessage(`{}`), nil
	}

	if _, _, err := a.getOrCreateThread(context.Background(), "user-a"); err != nil {
		t.Fatalf("getOrCreateThread: %v", err)
	}

	cfg, ok := capturedParams["config"].(map[string]interface{})
	if !ok {
		t.Fatalf("thread/start params missing config object, got %+v", capturedParams)
	}
	if cfg["model_reasoning_effort"] != "low" {
		t.Fatalf("config.model_reasoning_effort = %v, want %q", cfg["model_reasoning_effort"], "low")
	}
}

func TestGetOrCreateThreadOmitsConfigWhenReasoningEffortNotSet(t *testing.T) {
	var capturedParams map[string]interface{}
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method == "thread/start" {
			b, _ := json.Marshal(params)
			json.Unmarshal(b, &capturedParams)
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		}
		return json.RawMessage(`{}`), nil
	}

	if _, _, err := a.getOrCreateThread(context.Background(), "user-a"); err != nil {
		t.Fatalf("getOrCreateThread: %v", err)
	}
	if _, ok := capturedParams["config"]; ok {
		t.Fatalf("thread/start params should not have a config object when ModelReasoningEffort is unset, got %+v", capturedParams)
	}
}
```

（读一下 `agent/acp_agent_test.go` 里 `rpcCall`/`a.rpc` 现有的 stub 用法，确认字段名和调用约定跟上面写的一致——这个文件已经有类似的 mock 模式，照抄它的风格，不要凭空发明新的 stub 机制。）

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ~/AiCodeProject/weclaw && go test ./agent/... -run TestGetOrCreateThreadIncludesModelReasoningEffort -v`
Expected: 编译失败（`ACPAgentConfig.ModelReasoningEffort` 不存在）

- [ ] **Step 3: 改 `agent/acp_agent.go`**

`ACPAgent` struct 里 `model` 字段之后加一行：

```go
	model                string
	modelReasoningEffort string
```

`ACPAgentConfig` struct 里 `Model` 字段之后加一行：

```go
	Model                string
	ModelReasoningEffort string
```

`NewACPAgent` 里 `model: cfg.Model,` 之后加一行：

```go
		model:                cfg.Model,
		modelReasoningEffort: cfg.ModelReasoningEffort,
```

`getOrCreateThread` 里补上 Task 2 暂时跳过的那两行（紧跟 `if a.model != "" { params["model"] = a.model }` 之后）：

```go
	if a.model != "" {
		params["model"] = a.model
	}
	if a.modelReasoningEffort != "" {
		params["config"] = map[string]interface{}{"model_reasoning_effort": a.modelReasoningEffort}
	}
	if override.SystemPrompt != "" {
		params["baseInstructions"] = override.SystemPrompt
	}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd ~/AiCodeProject/weclaw && go test ./agent/... -v`
Expected: 全部 PASS

- [ ] **Step 5: Commit**

```bash
cd ~/AiCodeProject/weclaw
git add agent/acp_agent.go agent/acp_agent_test.go
git commit -m "Thread model_reasoning_effort through to codex thread/start"
```

---

## Task 5: `messaging/handler.go` — 非 owner 默认 agent 改成可配置

**Files:**
- Modify: `messaging/handler.go`
- Modify: `messaging/handler_test.go`

**Interfaces:**
- Consumes：`config.Config.NonOwnerDefaultAgent`（Task 1）
- Produces：`(*Handler).SetNonOwnerDefaultAgent(name string)`；`selectedAgent()` 对非 owner 恒定路由到该值（空值兜底 `"codex-shared"`），不再写死 `"claude"`

一期把非 owner 恒定路由到 `"claude"`（`nonOwnerAgentName` 常量）。这次改成一个可配置的 Handler 字段，默认值改成 `"codex-shared"`。

- [ ] **Step 1: 写失败的测试**

在 `messaging/handler_test.go` 里找到一期的 `TestSelectedAgentIgnoresUserAgentsOverrideForNonOwner`/`TestSelectedAgentHonorsOwnerUserAgentsOverride`（Task 4 of 一期新增的），在它们附近加：

```go
func TestSelectedAgentDefaultsNonOwnerToCodexSharedWhenUnconfigured(t *testing.T) {
	h := newTestHandler()
	h.defaultName = "codex"
	if name, _, selected := h.selectedAgent("user-x"); name != "codex-shared" || !selected {
		t.Fatalf("selectedAgent(non-owner) = (%q, selected=%v), want (\"codex-shared\", true) when NonOwnerDefaultAgent is unset", name, selected)
	}
}

func TestSelectedAgentHonorsConfiguredNonOwnerDefaultAgent(t *testing.T) {
	h := newTestHandler()
	h.SetNonOwnerDefaultAgent("claude")
	if name, _, selected := h.selectedAgent("user-x"); name != "claude" || !selected {
		t.Fatalf("selectedAgent(non-owner) = (%q, selected=%v), want (\"claude\", true) after SetNonOwnerDefaultAgent(\"claude\")", name, selected)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ~/AiCodeProject/weclaw && go test ./messaging/... -run 'TestSelectedAgentDefaultsNonOwner|TestSelectedAgentHonorsConfiguredNonOwner' -v`
Expected: 编译失败（`SetNonOwnerDefaultAgent` 不存在）

- [ ] **Step 3: 改 `messaging/handler.go`**

`Handler` struct 里，紧跟一期加的 `userPersonas` 字段之后加一行：

```go
	userPersonas   map[string]string // userID -> persona name (non-owner only)
	nonOwnerAgent  string            // agent name non-owner users are routed to; "" falls back to defaultNonOwnerAgentName
```

紧跟一期 `nonOwnerAgentName` 常量之后（现在改名，见下）加一个新常量和 setter 方法：

```go
// defaultNonOwnerAgentName is used when no NonOwnerDefaultAgent is
// configured. codex-shared is an isolated codex-app-server instance with
// its own CODEX_HOME (see cmd/start.go) — safe to expose to non-owners,
// unlike the real "codex" entry which is the owner's instance with the
// owner's real ~/.codex/AGENTS.md.
const defaultNonOwnerAgentName = "codex-shared"

// SetNonOwnerDefaultAgent configures which agent non-owner users are routed
// to. Empty falls back to defaultNonOwnerAgentName.
func (h *Handler) SetNonOwnerDefaultAgent(name string) {
	h.mu.Lock()
	h.nonOwnerAgent = name
	h.mu.Unlock()
}
```

**把一期写死的 `nonOwnerAgentName` 常量删掉**，`selectedAgent()` 改成读 `h.nonOwnerAgent`（带兜底）：

```go
// selectedAgent returns the agent selected by this user, falling back to the
// global default. Non-owner users always get h.nonOwnerAgent (or
// defaultNonOwnerAgentName if unset) regardless of defaultName or any
// userAgents override. selected is always true for non-owner so a
// not-yet-running agent still gets lazily started by callers' "ag == nil &&
// selected" fallback.
func (h *Handler) selectedAgent(userID string) (string, agent.Agent, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if !config.IsOwner(userID) {
		name := h.nonOwnerAgent
		if name == "" {
			name = defaultNonOwnerAgentName
		}
		return name, h.agents[name], true
	}
	name, selected := h.userAgents[userID]
	if !selected {
		name = h.defaultName
	}
	return name, h.agents[name], selected
}
```

**一期遗留的测试要跟着改**：一期的 `TestSelectedAgentIgnoresUserAgentsOverrideForNonOwner`/`TestNonOwnerAgentSwitchPrefixIsPlainText`/`TestNonOwnerCwdIsPlainText`/`TestQuotaExceededBlocksNonOwnerBeforeAgentCall` 这几个测试断言的是"非 owner 恒定拿到 `\"claude\"`"，现在默认值变成 `\"codex-shared\"` 了，这些测试会跟着失败——**这是本任务改动的直接后果，不是范围蔓延**。找到这几个测试，把其中断言 agent 名字是 `"claude"` 的地方改成 `"codex-shared"`（`TestSelectedAgentHonorsOwnerUserAgentsOverride` 用的是 owner ID，不受影响，不用改）。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd ~/AiCodeProject/weclaw && go test ./messaging/... -v`
Expected: 全部 PASS

- [ ] **Step 5: Commit**

```bash
cd ~/AiCodeProject/weclaw
git add messaging/handler.go messaging/handler_test.go
git commit -m "Make non-owner default agent configurable, defaulting to codex-shared"
```

---

## Task 6: `messaging/handler.go` — 非 owner 切换命令（`AllowNonOwnerAgentSwitch`）

**Files:**
- Modify: `messaging/handler.go`
- Modify: `messaging/handler_test.go`

**Interfaces:**
- Consumes：`config.Config.AllowNonOwnerAgentSwitch`（Task 1）；`(*Handler).selectedAgent`/`SetNonOwnerDefaultAgent`（Task 5）
- Produces：`(*Handler).SetAllowNonOwnerAgentSwitch(allow bool)`；非 owner 在开关打开时，`/claude`、`/codex` 两个命令可用，`/codex` 解析到 `codex-shared`（不是真实 `codex`），其余命令继续对非 owner 不可见

这是本期安全上最关键的一个任务：同一个命令字符串 `/codex`，owner 和非 owner 必须解析到不同的实际 agent。**不能复用现有的 `resolveAlias`/`agentAliases` 静态映射表**（那张表不分调用者身份，`"cx": "codex"` 是给 owner 用的，非 owner 绝不能通过任何路径摸到那张表解析出真实 `codex`）。

- [ ] **Step 1: 写失败的测试**

在 `messaging/handler_test.go` 末尾追加：

```go
func TestNonOwnerCodexCommandResolvesToCodexSharedNotRealCodex(t *testing.T) {
	srv, _ := newFakeIlinkServer(t)
	defer srv.Close()

	h := NewHandler(nil, nil)
	h.SetAllowNonOwnerAgentSwitch(true)
	fakeShared := &recordingAgent{}
	fakeReal := &recordingAgent{}
	h.SetDefaultAgent("codex-shared", fakeShared) // registers under h.agents["codex-shared"]
	h.agents["codex"] = fakeReal                  // simulate the real owner-only codex agent also being registered

	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	msg := newTestMessage("non-owner-test@im.wechat", "/codex 你好", 1)
	h.HandleMessage(context.Background(), client, msg)

	waitFor(t, 2*time.Second, func() bool { return len(fakeShared.callsSnapshot()) > 0 || len(fakeReal.callsSnapshot()) > 0 })
	if calls := fakeReal.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("non-owner /codex must never reach the real codex agent, got calls=%v", calls)
	}
	if calls := fakeShared.callsSnapshot(); len(calls) != 1 || calls[0] != "你好" {
		t.Fatalf("non-owner /codex should reach codex-shared with the message, got %v", calls)
	}
}

func TestNonOwnerAgentSwitchDisabledByDefaultTreatsSlashCodexAsPlainText(t *testing.T) {
	srv, _ := newFakeIlinkServer(t)
	defer srv.Close()

	h := NewHandler(nil, nil)
	// AllowNonOwnerAgentSwitch left at its default (false).
	fake := &recordingAgent{}
	h.SetDefaultAgent("codex-shared", fake)

	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	msg := newTestMessage("non-owner-test@im.wechat", "/codex 你好", 1)
	h.HandleMessage(context.Background(), client, msg)

	waitFor(t, 2*time.Second, func() bool { return len(fake.callsSnapshot()) > 0 })
	calls := fake.callsSnapshot()
	if len(calls) != 1 || calls[0] != "/codex 你好" {
		t.Fatalf("with switching disabled, /codex should reach the default agent verbatim as plain text, got %v", calls)
	}
}

func TestOwnerCodexCommandStillResolvesToRealCodex(t *testing.T) {
	srv, _ := newFakeIlinkServer(t)
	defer srv.Close()

	h := NewHandler(nil, nil)
	h.SetAllowNonOwnerAgentSwitch(true)
	fakeReal := &recordingAgent{}
	h.SetDefaultAgent("codex", fakeReal)

	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	ownerID := config.OwnerUserIDs()[0]
	msg := newTestMessage(ownerID, "/codex 你好", 1)
	h.HandleMessage(context.Background(), client, msg)

	waitFor(t, 2*time.Second, func() bool { return len(fakeReal.callsSnapshot()) > 0 })
	if calls := fakeReal.callsSnapshot(); len(calls) != 1 || calls[0] != "你好" {
		t.Fatalf("owner /codex should still reach the real codex agent, got %v", calls)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ~/AiCodeProject/weclaw && go test ./messaging/... -run 'TestNonOwnerCodexCommand|TestNonOwnerAgentSwitchDisabled|TestOwnerCodexCommandStillResolves' -v`
Expected: 编译失败（`SetAllowNonOwnerAgentSwitch` 不存在）；`TestOwnerCodexCommandStillResolvesToRealCodex` 应该已经能过（owner 路径本期没改），先确认这一条即使在编译通过后也是绿的

- [ ] **Step 3: 改 `messaging/handler.go`**

`Handler` struct 里紧跟 `nonOwnerAgent` 字段之后加一行：

```go
	nonOwnerAgent          string // agent name non-owner users are routed to
	allowNonOwnerSwitch    bool   // whether non-owner users may switch between claude/codex-shared via /claude, /codex
```

加 setter（紧跟 `SetNonOwnerDefaultAgent` 之后）：

```go
// SetAllowNonOwnerAgentSwitch controls whether non-owner users may switch
// between claude and codex-shared via /claude and /codex. Default false:
// non-owner is pinned to whatever SetNonOwnerDefaultAgent configured.
func (h *Handler) SetAllowNonOwnerAgentSwitch(allow bool) {
	h.mu.Lock()
	h.allowNonOwnerSwitch = allow
	h.mu.Unlock()
}

// nonOwnerSwitchTarget resolves a non-owner's literal "/claude" or "/codex"
// prefix to the actual agent name they're allowed to reach. Deliberately a
// separate, narrow resolver from resolveAlias/agentAliases (the owner's
// alias table) — those are not scoped by caller identity, and non-owner
// must never be able to resolve "/codex" to the real owner-only "codex"
// agent. Returns ("", false) for anything else, including every alias the
// owner's table understands (cx, cc, etc.) — non-owner gets exactly these
// two literal, spelled-out command words, nothing shorter.
func nonOwnerSwitchTarget(trimmed string) (agentName, rest string, ok bool) {
	for _, prefix := range []string{"/claude", "@claude"} {
		if trimmed == prefix {
			return "claude", "", true
		}
		if strings.HasPrefix(trimmed, prefix+" ") {
			return "claude", strings.TrimSpace(trimmed[len(prefix):]), true
		}
	}
	for _, prefix := range []string{"/codex", "@codex"} {
		if trimmed == prefix {
			return defaultNonOwnerAgentName, "", true
		}
		if strings.HasPrefix(trimmed, prefix+" ") {
			return defaultNonOwnerAgentName, strings.TrimSpace(trimmed[len(prefix):]), true
		}
	}
	return "", "", false
}
```

在 `handleMessage` 里，找到一期加的这段（非 owner 直接转发到默认 agent 那一段）：

```go
	// Non-owners never reach the agent-switch/broadcast routing below: /xxx or
	// @xxx prefixes (including "/cwd" for them) are just ordinary chat text
	// sent to their locked-down agent. This keeps the switching feature and
	// its existence entirely invisible to them.
	if !owner {
		h.sendToDefaultAgent(ctx, client, msg, text, clientID)
		return
	}
```

改成：

```go
	// Non-owners never reach the owner's agent-switch/broadcast routing
	// below (that alias table is not scoped by caller identity — resolving
	// through it could hand a non-owner the real, owner-only "codex"
	// agent). The only switching non-owner ever gets is the narrow
	// nonOwnerSwitchTarget check just below, gated by allowNonOwnerSwitch.
	if !owner {
		h.mu.RLock()
		allowSwitch := h.allowNonOwnerSwitch
		h.mu.RUnlock()
		if allowSwitch {
			if targetAgent, rest, matched := nonOwnerSwitchTarget(trimmed); matched {
				if rest == "" {
					reply := h.switchUserAgent(ctx, msg.FromUserID, targetAgent)
					if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
						log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
					}
				} else {
					h.sendToNamedAgent(ctx, client, msg, targetAgent, rest, clientID)
				}
				return
			}
		}
		h.sendToDefaultAgent(ctx, client, msg, text, clientID)
		return
	}
```

**注意**：`switchUserAgent(ctx, userID, name)` 是一期就有的方法，会把 `h.userAgents[userID] = name` 存起来——但 `selectedAgent()`（Task 5 改的那版）对非 owner 完全无视 `h.userAgents`，恒定用 `h.nonOwnerAgent`。也就是说非 owner 调用 `switchUserAgent("codex-shared")` 存下的选择**实际不会被 `selectedAgent()` 读取**，这条切换命令目前只是**单次消息级别的路由**（`sendToNamedAgent`/`switchUserAgent` 这一次调用直接生效），不是"以后这个用户默认都用这个 agent"式的持久切换。这个行为差异要在 `nonOwnerSwitchTarget` 或这段代码上方补一行注释说明，写实施代码时一并加上，避免以后有人以为切换是持久的。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd ~/AiCodeProject/weclaw && go test ./messaging/... -v`
Expected: 全部 PASS

- [ ] **Step 5: Commit**

```bash
cd ~/AiCodeProject/weclaw
git add messaging/handler.go messaging/handler_test.go
git commit -m "Add gated /claude, /codex switching for non-owner, resolving /codex to codex-shared never the real codex"
```

---

## Task 7: `cmd/start.go` — `codex-shared` 隔离主目录搭建

**Files:**
- Modify: `cmd/start.go`
- Create: `cmd/codex_shared_home.go`（新文件，逻辑独立，不用塞进已经很大的 `start.go`）
- Create: `cmd/codex_shared_home_test.go`

**Interfaces:**
- Produces：`ensureCodexSharedHome(realCodexHome string) (string, error)`——建隔离目录、软链 `auth.json`、写精简 `config.toml`（已存在则不覆盖），返回隔离目录的绝对路径

- [ ] **Step 1: 写失败的测试**

`cmd/codex_shared_home_test.go`：

```go
package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureCodexSharedHomeCreatesSymlinkAndMinimalConfig(t *testing.T) {
	realHome := t.TempDir()
	authPath := filepath.Join(realHome, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"fake":"auth"}`), 0o600); err != nil {
		t.Fatalf("seed auth.json: %v", err)
	}

	sharedRoot := t.TempDir()
	sharedHome := filepath.Join(sharedRoot, "codex-shared-home")

	got, err := ensureCodexSharedHomeAt(sharedHome, realHome)
	if err != nil {
		t.Fatalf("ensureCodexSharedHomeAt: %v", err)
	}
	if got != sharedHome {
		t.Fatalf("ensureCodexSharedHomeAt returned %q, want %q", got, sharedHome)
	}

	linkTarget, err := os.Readlink(filepath.Join(sharedHome, "auth.json"))
	if err != nil {
		t.Fatalf("expected auth.json symlink: %v", err)
	}
	if linkTarget != authPath {
		t.Fatalf("auth.json symlink target = %q, want %q", linkTarget, authPath)
	}

	configPath := filepath.Join(sharedHome, "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("expected config.toml to be written: %v", err)
	}
	if !strings.Contains(string(data), "network_access = true") {
		t.Fatalf("config.toml missing network_access = true, got:\n%s", data)
	}

	// AGENTS.md / memories must NOT exist -- that's the entire point of isolation.
	if _, err := os.Stat(filepath.Join(sharedHome, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("codex-shared home must not have an AGENTS.md")
	}
}

func TestEnsureCodexSharedHomeDoesNotOverwriteExistingConfig(t *testing.T) {
	realHome := t.TempDir()
	os.WriteFile(filepath.Join(realHome, "auth.json"), []byte(`{}`), 0o600)

	sharedHome := filepath.Join(t.TempDir(), "codex-shared-home")
	if _, err := ensureCodexSharedHomeAt(sharedHome, realHome); err != nil {
		t.Fatalf("first call: %v", err)
	}

	customized := "# customized by devliang\nmodel = \"gpt-5.6-terra\"\n"
	if err := os.WriteFile(filepath.Join(sharedHome, "config.toml"), []byte(customized), 0o644); err != nil {
		t.Fatalf("customize config.toml: %v", err)
	}

	if _, err := ensureCodexSharedHomeAt(sharedHome, realHome); err != nil {
		t.Fatalf("second call: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(sharedHome, "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	if string(data) != customized {
		t.Fatalf("second call overwrote a customized config.toml, got:\n%s", data)
	}
}

func TestEnsureCodexSharedHomeMissingRealAuthIsNotFatal(t *testing.T) {
	realHome := t.TempDir() // no auth.json seeded
	sharedHome := filepath.Join(t.TempDir(), "codex-shared-home")

	if _, err := ensureCodexSharedHomeAt(sharedHome, realHome); err != nil {
		t.Fatalf("ensureCodexSharedHomeAt should not fail just because auth.json doesn't exist yet: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sharedHome, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("should not create a symlink to a target that doesn't exist")
	}
}
```

（顶部 import 需要 `"strings"`，跟其余几个一起写在 import 块里。）

- [ ] **Step 2: 跑测试确认失败**

Run: `cd ~/AiCodeProject/weclaw && go test ./cmd/... -run TestEnsureCodexSharedHome -v`
Expected: 编译失败（`ensureCodexSharedHomeAt` 不存在）

- [ ] **Step 3: 写 `cmd/codex_shared_home.go`**

```go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
)

// codexSharedConfigTOML is deliberately minimal: this is NOT a copy of the
// owner's real ~/.codex/config.toml (that would defeat the isolation this
// whole feature exists for -- see docs/superpowers/specs/
// 2026-07-24-codex-user-isolation-design.md). network_access=true is the
// one setting that's functionally required (image generation needs it);
// everything else uses codex's own defaults, and model/reasoning-effort
// tier is controlled separately via the codex-shared agent's own config.json
// entry (ACPAgentConfig.Model/ModelReasoningEffort), not this file.
const codexSharedConfigTOML = `[sandbox_workspace_write]
network_access = true
`

// ensureCodexSharedHome creates (idempotently) the isolated CODEX_HOME used
// by the codex-shared agent under ~/.weclaw/codex-shared-home, and returns
// its path. realCodexHome is the owner's real CODEX_HOME (normally
// ~/.codex) -- only auth.json is symlinked from there, so login stays in
// sync with token rotation while AGENTS.md/memories/config.toml stay fully
// isolated.
func ensureCodexSharedHome(realCodexHome string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	sharedHome := filepath.Join(home, ".weclaw", "codex-shared-home")
	return ensureCodexSharedHomeAt(sharedHome, realCodexHome)
}

// ensureCodexSharedHomeAt is ensureCodexSharedHome with an explicit target
// path, split out so tests don't have to touch the real home directory.
func ensureCodexSharedHomeAt(sharedHome, realCodexHome string) (string, error) {
	if err := os.MkdirAll(sharedHome, 0o700); err != nil {
		return "", fmt.Errorf("create codex-shared home: %w", err)
	}

	authLink := filepath.Join(sharedHome, "auth.json")
	realAuth := filepath.Join(realCodexHome, "auth.json")
	if _, err := os.Lstat(authLink); os.IsNotExist(err) {
		if _, statErr := os.Stat(realAuth); statErr == nil {
			if linkErr := os.Symlink(realAuth, authLink); linkErr != nil {
				return "", fmt.Errorf("symlink auth.json: %w", linkErr)
			}
		}
		// If realAuth doesn't exist yet (fresh install, not logged in),
		// skip silently -- codex-shared just won't be authenticated until
		// the owner logs in and this function runs again on next restart.
	} else if err != nil {
		return "", fmt.Errorf("check existing auth.json symlink: %w", err)
	}

	configPath := filepath.Join(sharedHome, "config.toml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.WriteFile(configPath, []byte(codexSharedConfigTOML), 0o644); err != nil {
			return "", fmt.Errorf("write codex-shared config.toml: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("check existing config.toml: %w", err)
	}

	return sharedHome, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd ~/AiCodeProject/weclaw && go test ./cmd/... -v`
Expected: 全部 PASS

- [ ] **Step 5: Commit**

```bash
cd ~/AiCodeProject/weclaw
git add cmd/codex_shared_home.go cmd/codex_shared_home_test.go
git commit -m "Add idempotent setup for codex-shared's isolated CODEX_HOME"
```

---

## Task 8: `cmd/start.go` — 全部接线

**Files:**
- Modify: `cmd/start.go`
- Modify: `~/.weclaw/config.json`（不是仓库文件，是本机运行时配置——这一步是手动操作，不是代码改动，见 Step 4）

**Interfaces:**
- Consumes：Task 1-7 的全部 Produces
- Produces：可运行的完整 weclaw 二进制，`codex-shared` 真正跑起来

- [ ] **Step 1: 加 import 和启动时调用 `ensureCodexSharedHome`**

在 `runStart` 里，紧跟一期加的 `persona.EnsureDefault(personaDir)` 那段之后加：

```go
	realCodexHome := os.Getenv("CODEX_HOME")
	if realCodexHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			realCodexHome = filepath.Join(home, ".codex")
		}
	}
	codexSharedHome, err := ensureCodexSharedHome(realCodexHome)
	if err != nil {
		return fmt.Errorf("set up codex-shared home: %w", err)
	}
	log.Printf("codex-shared isolated home ready: %s", codexSharedHome)
```

（读一下 `cmd/start.go` 顶部 import 块，确认 `"path/filepath"` 已经导入——一期已经加过 `persona` 包，大概率这个也在，没有就补上。）

- [ ] **Step 2: 扩展 `createAgentByName`，透传 `ModelReasoningEffort`**

找到 `createAgentByName` 里 `case "acp":` 分支，`agent.NewACPAgent(agent.ACPAgentConfig{...})` 那段加一行：

```go
	case "acp":
		ag := agent.NewACPAgent(agent.ACPAgentConfig{
			Command:              agCfg.Command,
			Args:                 agCfg.Args,
			Cwd:                  agCfg.Cwd,
			Env:                  agCfg.Env,
			Model:                agCfg.Model,
			ModelReasoningEffort: agCfg.ModelReasoningEffort,
			SystemPrompt:         agCfg.SystemPrompt,
		})
```

- [ ] **Step 3: 接线 handler 的两个新配置**

紧跟一期的 `handler.SetUserPersonas(cfg.UserPersonas)` 之后加：

```go
	handler.SetNonOwnerDefaultAgent(cfg.NonOwnerDefaultAgent)
	handler.SetAllowNonOwnerAgentSwitch(cfg.AllowNonOwnerAgentSwitch)
```

- [ ] **Step 4: 手动更新本机 `~/.weclaw/config.json`，加 `codex-shared` 条目**

这一步不是代码改动，是本机运行时配置文件的手动编辑（`~/.weclaw/config.json` 不在 git 仓库里）。在 `agents` 对象里，紧挨着现有 `"codex"` 条目之后加一个新条目（`env` 里的代理设置照抄现有 `codex`/`claude` 条目的值，`CODEX_HOME` 用 Step 1 打印出来的实际隔离目录路径，一般是 `~/.weclaw/codex-shared-home`）：

```json
"codex-shared": {
  "type": "acp",
  "command": "/opt/homebrew/bin/codex",
  "args": ["app-server", "--listen", "stdio://"],
  "env": {
    "CODEX_HOME": "/Users/devliang/.weclaw/codex-shared-home",
    "HTTPS_PROXY": "http://127.0.0.1:7890",
    "HTTP_PROXY": "http://127.0.0.1:7890",
    "NO_PROXY": "localhost,127.0.0.1,::1,*.local,192.168.0.0/16"
  }
}
```

模型档位（`model`/`model_reasoning_effort`）先不加，让它用 codex 默认——梁师傅要单独定档位的话，在这个条目里加 `"model": "..."`/`"model_reasoning_effort": "..."` 两行即可，代码侧（Task 4）已经支持读取。

- [ ] **Step 5: 编译 + 全量测试**

Run: `cd ~/AiCodeProject/weclaw && go build ./... && go test ./...`
Expected: 编译成功，全部测试 PASS

- [ ] **Step 6: Commit（只提交代码改动，不提交 `~/.weclaw/config.json`）**

```bash
cd ~/AiCodeProject/weclaw
git add cmd/start.go
git commit -m "Wire codex-shared setup, non-owner routing config, and model_reasoning_effort into startup"
```

---

## Task 9: 18022 后台 — 两个新开关的管理面板

**Files:**
- Modify: `~/AiCodeProject/weclaw/api/server.go`
- Modify: `~/AiCodeProject/weclaw/api/server_test.go`
- Modify: `~/AiCodeProject/weclaw/cmd/start.go`
- Modify: `~/AiCodeProject/weclaw/messaging/handler.go`
- Modify: `~/AiCodeProject/wx-clawbot/scripts/chat_viewer.py`

**Interfaces:**
- Consumes：`(*Handler).SetNonOwnerDefaultAgent`/`SetAllowNonOwnerAgentSwitch`（Task 5/6，本任务补上对应的只读 getter）
- Produces：weclaw 内部 API `GET/POST /api/internal/settings/non-owner-routing`（一次性返回/更新 `default_agent`+`allow_switch` 两个字段，跟一期 `access_mode` 是同一种"少量枚举/布尔值"性质，没必要拆成两个端点）；wx-clawbot 18022 对应的代理函数 + UI 面板

- [ ] **Step 1: `api/server.go` 加类型和端点**

紧跟一期的 `AccessModeSettings`/`AccessModeProvider`/`AccessModeController` 定义之后加：

```go
// NonOwnerRoutingSettings is the JSON-safe representation of which agent
// non-owner users default to, and whether they may switch between
// claude/codex-shared themselves.
type NonOwnerRoutingSettings struct {
	DefaultAgent string `json:"default_agent"` // "" displayed/stored as "codex-shared" (the actual default)
	AllowSwitch  bool   `json:"allow_switch"`
}

type NonOwnerRoutingProvider func() NonOwnerRoutingSettings
type NonOwnerRoutingController func(context.Context, NonOwnerRoutingSettings) (NonOwnerRoutingSettings, error)
```

`const` 块加路径：

```go
	NonOwnerRoutingPath = "/api/internal/settings/non-owner-routing"
```

`Server` struct 加字段（紧跟 `accessMode`/`setAccessMode` 之后）：

```go
	nonOwnerRouting     NonOwnerRoutingProvider
	setNonOwnerRouting  NonOwnerRoutingController
```

加两个 setter（紧跟 `SetAccessModeController` 之后），照抄它的写法：

```go
func (s *Server) SetNonOwnerRoutingProvider(provider NonOwnerRoutingProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nonOwnerRouting = provider
}

func (s *Server) SetNonOwnerRoutingController(controller NonOwnerRoutingController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setNonOwnerRouting = controller
}
```

路由注册（紧跟 `mux.HandleFunc(AccessModePath, s.handleAccessMode)` 之后）：

```go
	mux.HandleFunc(NonOwnerRoutingPath, s.handleNonOwnerRouting)
```

handler（照抄 `handleAccessMode` 的结构，紧跟它之后加）：

```go
func (s *Server) handleNonOwnerRouting(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	s.mu.RLock()
	provider, controller := s.nonOwnerRouting, s.setNonOwnerRouting
	s.mu.RUnlock()
	if r.Method == http.MethodGet {
		if provider == nil {
			http.Error(w, "non-owner routing settings unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(provider())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	if controller == nil {
		http.Error(w, "non-owner routing settings unavailable", http.StatusServiceUnavailable)
		return
	}
	var settings NonOwnerRoutingSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	updated, err := controller(r.Context(), settings)
	if err != nil {
		http.Error(w, "invalid settings: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}
```

测试（照抄 `TestHandleAccessMode*` 系列的写法，加到 `api/server_test.go`）：

```go
func TestHandleNonOwnerRoutingReadsAndUpdates(t *testing.T) {
	stored := NonOwnerRoutingSettings{DefaultAgent: "codex-shared", AllowSwitch: false}
	server := NewServer(nil, "")
	server.SetNonOwnerRoutingProvider(func() NonOwnerRoutingSettings { return stored })
	server.SetNonOwnerRoutingController(func(_ context.Context, s NonOwnerRoutingSettings) (NonOwnerRoutingSettings, error) {
		stored = s
		return stored, nil
	})

	get := httptest.NewRequest(http.MethodGet, NonOwnerRoutingPath, nil)
	get.RemoteAddr = "127.0.0.1:12345"
	getResp := httptest.NewRecorder()
	server.handleNonOwnerRouting(getResp, get)
	if getResp.Code != http.StatusOK || !strings.Contains(getResp.Body.String(), "codex-shared") {
		t.Fatalf("GET = %d %s", getResp.Code, getResp.Body.String())
	}

	post := httptest.NewRequest(http.MethodPost, NonOwnerRoutingPath, bytes.NewBufferString(`{"default_agent":"claude","allow_switch":true}`))
	post.RemoteAddr = "127.0.0.1:12345"
	postResp := httptest.NewRecorder()
	server.handleNonOwnerRouting(postResp, post)
	if postResp.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", postResp.Code, postResp.Body.String())
	}
	if stored.DefaultAgent != "claude" || !stored.AllowSwitch {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestHandleNonOwnerRoutingRejectsNonLoopback(t *testing.T) {
	called := false
	server := NewServer(nil, "")
	server.SetNonOwnerRoutingController(func(_ context.Context, s NonOwnerRoutingSettings) (NonOwnerRoutingSettings, error) {
		called = true
		return s, nil
	})
	req := httptest.NewRequest(http.MethodPost, NonOwnerRoutingPath, bytes.NewBufferString(`{}`))
	req.RemoteAddr = "192.0.2.1:12345"
	resp := httptest.NewRecorder()
	server.handleNonOwnerRouting(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.Code)
	}
	if called {
		t.Fatal("controller should not be called for a non-loopback request")
	}
}
```

Run: `cd ~/AiCodeProject/weclaw && go test ./api/... -v`
Expected: 全部 PASS

- [ ] **Step 2: `cmd/start.go` 接线 Provider/Controller**

紧跟一期的 `apiServer.SetAccessModeController(...)` 那段之后加：

```go
	apiServer.SetNonOwnerRoutingProvider(func() api.NonOwnerRoutingSettings {
		defaultAgent := handler.NonOwnerDefaultAgent()
		if defaultAgent == "" {
			defaultAgent = "codex-shared"
		}
		return api.NonOwnerRoutingSettings{DefaultAgent: defaultAgent, AllowSwitch: handler.AllowNonOwnerAgentSwitch()}
	})
	apiServer.SetNonOwnerRoutingController(func(_ context.Context, settings api.NonOwnerRoutingSettings) (api.NonOwnerRoutingSettings, error) {
		if settings.DefaultAgent != "claude" && settings.DefaultAgent != "codex-shared" {
			return api.NonOwnerRoutingSettings{}, fmt.Errorf("default_agent must be \"claude\" or \"codex-shared\", got %q", settings.DefaultAgent)
		}
		configMu.Lock()
		cfg.NonOwnerDefaultAgent = settings.DefaultAgent
		cfg.AllowNonOwnerAgentSwitch = settings.AllowSwitch
		saveErr := config.Save(cfg)
		configMu.Unlock()
		if saveErr != nil {
			return api.NonOwnerRoutingSettings{}, saveErr
		}
		handler.SetNonOwnerDefaultAgent(settings.DefaultAgent)
		handler.SetAllowNonOwnerAgentSwitch(settings.AllowSwitch)
		return settings, nil
	})
```

`messaging/handler.go` 需要补两个只读 getter（Task 5/6 只加了 setter，这里为了给 provider 读取补上，紧跟对应 setter 之后加）：

```go
func (h *Handler) NonOwnerDefaultAgent() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.nonOwnerAgent
}

func (h *Handler) AllowNonOwnerAgentSwitch() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.allowNonOwnerSwitch
}
```

Run: `cd ~/AiCodeProject/weclaw && go build ./... && go test ./...`
Expected: 编译成功，全部 PASS

- [ ] **Step 3: Commit（weclaw 侧）**

```bash
cd ~/AiCodeProject/weclaw
git add api/server.go api/server_test.go cmd/start.go messaging/handler.go
git commit -m "Add internal API endpoint for non-owner default agent and switch toggle"
```

- [ ] **Step 4: wx-clawbot 侧 — Python 代理函数**

`cd ~/AiCodeProject/wx-clawbot`，在 `scripts/chat_viewer.py` 顶部常量区，紧跟 `WECLAW_ACCESS_MODE_URL` 之后加：

```python
WECLAW_NON_OWNER_ROUTING_URL = "http://127.0.0.1:18011/api/internal/settings/non-owner-routing"
```

紧跟 `_weclaw_access_mode()`/`_set_weclaw_access_mode()`（照抄它俩的写法，找到这两个函数的位置，在附近加）：

```python
def _weclaw_non_owner_routing():
    try:
        with urllib.request.urlopen(WECLAW_NON_OWNER_ROUTING_URL, timeout=1.5) as resp:
            payload = json.loads(resp.read().decode("utf-8"))
        return {"online": True, "settings": payload}
    except (OSError, ValueError, urllib.error.URLError) as exc:
        return {"online": False, "settings": None, "error": str(exc)}


def _set_weclaw_non_owner_routing(default_agent, allow_switch):
    payload = {"default_agent": default_agent, "allow_switch": allow_switch}
    request = urllib.request.Request(
        WECLAW_NON_OWNER_ROUTING_URL,
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

`do_GET` 加分支（紧跟 `/api/settings/access-mode` 分支之后）：

```python
        elif parsed.path == "/api/settings/non-owner-routing":
            body = json.dumps(_weclaw_non_owner_routing(), ensure_ascii=False).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json; charset=utf-8")
```

`do_POST` 白名单加路径 `"/api/settings/non-owner-routing"`，加分支（紧跟 `/api/settings/access-mode` 分支之后）：

```python
            elif path == "/api/settings/non-owner-routing":
                default_agent = str(payload.get("default_agent", ""))
                allow_switch = payload.get("allow_switch")
                if default_agent not in {"claude", "codex-shared"} or not isinstance(allow_switch, bool):
                    raise ValueError("invalid non-owner routing settings")
                body = json.dumps(_set_weclaw_non_owner_routing(default_agent, allow_switch), ensure_ascii=False).encode("utf-8")
```

- [ ] **Step 5: HTML + JS 面板**

紧挨着一期加的「人格管理」面板（`data-collapse-key="personas"`）之后插入新面板：

```html
      <div class="panel collapsible" data-collapse-key="non-owner-routing"><button class="panel-head" type="button"><h3>非 owner 默认模型</h3><span class="chev">▾</span></button><div class="panel-body"><div class="section-desc">非 owner 用户（不管是谁）默认用哪个 agent；「允许非 owner 自行切换」打开后，对方可以用 /claude、/codex 在这两个选项间切，但永远碰不到你自己用的真实 codex。</div><div id="nonOwnerRoutingSettings"></div></div></div>
```

JS 里 `state` 对象加 `nonOwnerRouting:null`；`renderPersonas()` 定义附近加一个新渲染函数（照抄 `renderAccessModeSettings` 的结构和风格）：

```javascript
function renderNonOwnerRoutingSettings(data){
  const key=JSON.stringify(data);if(settingsRenderState.nonOwnerRouting===key)return;settingsRenderState.nonOwnerRouting=key;
  const el=$('nonOwnerRoutingSettings');
  if(!data||!data.online){el.innerHTML='<div class="empty">无法读取 18011 的设置：'+esc(data?.error||'WeClaw 服务离线')+'</div>';return}
  const s=data.settings;
  const form=document.createElement('div');form.className='settings-form';
  const row1=document.createElement('div');row1.className='setting-row';
  const label1=document.createElement('label');label1.textContent='默认 agent';
  const select=document.createElement('select');select.className='permission-select';
  for(const [value,label] of [['codex-shared','codex（隔离版）'],['claude','claude']]){const opt=document.createElement('option');opt.value=value;opt.textContent=label;if(s.default_agent===value)opt.selected=true;select.appendChild(opt)}
  row1.append(label1,select);form.appendChild(row1);
  const row2=document.createElement('div');row2.className='setting-row';
  const label2=document.createElement('label');label2.textContent='允许非 owner 自行切换';
  const checkbox=document.createElement('input');checkbox.type='checkbox';checkbox.checked=!!s.allow_switch;
  row2.append(label2,checkbox);form.appendChild(row2);
  const button=document.createElement('button');button.className='settings-save';button.textContent='保存并立即生效';
  button.onclick=async()=>{if(!confirm('保存非 owner 路由设置？'))return;button.disabled=true;button.textContent='保存中…';try{const r=await fetch('/api/settings/non-owner-routing',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({default_agent:select.value,allow_switch:checkbox.checked})});const d=await r.json();if(!r.ok)throw new Error(d.error||'保存失败');state.nonOwnerRouting={online:true,settings:d};showToast('已保存');renderNonOwnerRoutingSettings(state.nonOwnerRouting)}catch(e){showToast('保存失败：'+e.message,'error')}finally{button.disabled=false;button.textContent='保存并立即生效'}};
  form.appendChild(button);el.replaceChildren(form)
}
```

`refresh()` 加一路 fetch（照一期的模式，加进 `Promise.all` 数组和对应的解析赋值），`renderAll()` 的 settings 分支里加 `renderNonOwnerRoutingSettings(state.nonOwnerRouting)` 调用。

- [ ] **Step 6: 验证**

Run: `python3 -c "import ast; ast.parse(open('scripts/chat_viewer.py').read())"`
Expected: 无输出

- [ ] **Step 7: Commit（wx-clawbot 侧）**

```bash
cd ~/AiCodeProject/wx-clawbot
git add scripts/chat_viewer.py
git commit -m "Add non-owner default agent and switch-toggle panel to 18022 settings"
```

---

## Task 10: 端到端人工验证

**Files:** 无代码改动。

- [ ] **Step 1: 编译 + 重启**

```bash
cd ~/AiCodeProject/weclaw && go build -o bin/weclaw . && go test ./...
weclaw restart
launchctl kickstart -k gui/$(id -u)/ai.weclaw.viewer
```

- [ ] **Step 2: 确认 codex-shared 隔离主目录真的建出来了**

```bash
ls -la ~/.weclaw/codex-shared-home/
cat ~/.weclaw/codex-shared-home/config.toml
```
Expected：能看到 `auth.json` 是个软链、`config.toml` 里有 `network_access = true`，没有 `AGENTS.md`。

- [ ] **Step 3: 真实非 owner 微信号冒烟**

用非 owner 微信号发消息，人工确认：
- 回复不含任何梁师傅的隐私信息（人名、家庭、经济状况等）
- 请求生成一张图片，能正常收到图（验证 `network_access=true` 那条精简配置真的让生图跑通了，也验证 `codexGeneratedImagePaths` 认对了隔离 home）
- 若 `AllowNonOwnerAgentSwitch` 手动打开，试 `/claude`、`/codex` 都能切且不报错；确认没开的时候 `/codex` 是纯文本转发

- [ ] **Step 4: owner 自己验证无变化**

owner 本人正常使用 codex，确认行为、人设、生图能力都跟改动前完全一致。

- [ ] **Step 5: 更新记忆**

这次改动完成后，更新 `weclaw-feature-gaps.md`、`persona-isolation-project.md`、`MEMORY.md`，记录二期已完成、真实验证结果。
