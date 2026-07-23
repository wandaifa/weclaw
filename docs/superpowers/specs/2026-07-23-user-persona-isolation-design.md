# 按用户身份分离人格 + 脱敏（persona）设计

日期：2026-07-23
状态：已批准，已实施；**2026-07-24 修正过一处关键机制错误，见下方勘误**

## 勘误（2026-07-24）

本文档下方所有 `--setting-sources project,local` 的描述**是错的**，已在实际代码里改成 `--safe-mode`（`PersonaOverride.SettingSources string` 字段也已改名为 `SafeMode bool`）。

`--setting-sources` 管的是 `settings.json` 类配置文件来源（权限/hooks/环境变量），跟 `CLAUDE.md`/记忆加载是两套完全不同的机制——排除 `user` 来源根本不会排除 `~/.claude/CLAUDE.md`。这个错误一路通过了任务级 review 和全分支终审（当时终审还给出了"已对照官方文档验证"的说法，但没人真的拿一个真实 claude 进程测过实际回复内容），直到梁师傅亲自用非 owner 微信号测试，收到了完整的"晚秋"人设+隐私信息，才现场复现根因、改用 `--safe-mode`（实测验证：能让 claude CLI 彻底不做 CLAUDE.md 自动发现，同时不影响登录态/工具/权限）。

详见 `agent/agent.go` 里 `PersonaOverride.SafeMode` 字段上的完整事故记录注释，以及 commit `7a01107`。

## 背景与问题

`~/.claude/CLAUDE.md`（claude 全局配置）和 `~/.codex/AGENTS.md`（codex 全局配置）里都写了「晚秋」人设和梁师傅的完整私人信息（家庭背景、父亲病逝、经济状况等）。

现状核实（2026-07-23）：

- **claude（CLI 类型 agent）**：`agent/cli_agent.go` 的 `CLIAgent` 每次对话都重新 `exec.CommandContext` spawn 一个 `claude` 子进程。子进程不区分 cwd/env/system-prompt 是 owner 还是普通微信用户发的消息——`conversationPolicy()`（`messaging/handler.go:1146`）算出的 `ConversationPolicy` 只在 `PolicyAwareAgent` 接口上生效，`CLIAgent` 没实现这个接口，所以策略对 claude **完全不生效**。任何微信用户跟 claude 聊天，都会加载梁师傅 `~/.claude/CLAUDE.md` 的全部隐私内容。
- **codex（ACP 类型 agent）**：`agent/acp_agent.go` 的 `ACPAgent` 启动时 spawn **一个长驻进程**（`Start()`），已经实现了 `PolicyAwareAgent`，`conversationPolicy()` 会给非 owner 用户分配独立的 `userSandboxDir(userID)` 作为 session 级 cwd。但 `~/.codex/AGENTS.md` 是 codex CLI 自己的全局配置，不随 session 级 cwd 变化，长驻进程也没法按用户重新加载配置目录——这条线的隐私没法在现有 ACP 长驻进程架构下干净地按用户切换。
- 两处泄露内容几乎完全重复（都是「晚秋」人设 + 梁师傅个人背景）。

## 目标

1. **管理员（owner）看到完整版 CLAUDE.md/晚秋人设，其他微信用户看到一份不含隐私的脱敏/通用人格**——按用户身份分，不是按 agent 类型分。
2. 后台（18022 内部设置页）加人格管理入口，后期能扩展任意多套人格，按用户绑定。
3. 不碰梁师傅本人的日常使用体验（他自己用 claude/codex 完全不变）。
4. 不修改 `~/.claude/`、`~/.codex/` 下任何全局配置文件（只读）。

## 决策记录

- **codex 线策略**：codex 长驻进程 + 全局 AGENTS.md 架构上做不到像 claude 那样干净地按用户切换配置。MVP 选择**普通用户强制路由到已脱敏的 claude，codex 只保留给 owner 使用**，不改造 codex 的进程模型。二期如需给普通用户开放 codex，需要单独设计（比如为非 owner 会话拉起独立的 codex 子进程而非复用长驻进程）。
- **`default` persona 初始内容**：保留工作背景（1688 电商助手定位，对话有用），完全去掉家庭/父亲/经济状况/「主人」称呼等私事。具体文案后台可随时改。

## 架构设计

### 1. persona 模型

persona（人格）与现有权限体系（`access_mode` / 沙盒 tier / 配额）正交：权限管「能不能进、能不能写」，persona 管「bot 是谁、认不认识这个人」。

- **`owner` persona**：硬编码行为标记，不对应文件。命中时 claude 子进程按现状（不加 `--setting-sources`、不注入替代 `--append-system-prompt`），完整加载 `~/.claude/CLAUDE.md`。
- **`default` persona**：对应 `~/.weclaw/personas/default.md`，脱敏通用人格文本。
- 后期可任意新增 persona 文件，通过后台绑定给具体用户。

### 2. 绑定规则（优先级从高到低）

1. `config.IsOwner(userID)` 为真 → 恒定 `owner`，不可通过任何 API 改写。
2. `config.json` 的 `user_personas[userID]` 有显式绑定 → 用该 persona。
3. 都没有 → 兜底 `config.json` 的 `default_persona`（初始值 `"default"`）。

### 3. agent 路由铁律

非 owner 用户请求切换到 `codex`（或任何非 claude 的 agent，除非未来白名单开放）时，`switchUserAgent` 直接拒绝并回复「codex 仅管理员可用」，不执行切换。owner 不受影响。

落点：`messaging/handler.go` 的 `switchUserAgent()` 和消息路由入口（`sendToNamedAgent` / `selectedAgent` 之前的分发逻辑）新增一次 owner 检查。

### 4. claude 脱敏注入机制

`CLIAgent`（`agent/cli_agent.go`）新增 per-conversation override，与 codex 现有 `PolicyAwareAgent.SetConversationPolicy` 同一套路：

```go
type CLIPersonaOverride struct {
    SettingSources string // 空 = 不限制（owner）；非空如 "project,local"（排除 user 来源）
    SystemPrompt   string // 追加给 --append-system-prompt 的人格文本
}
```

- `chatWithAgent`（`messaging/handler.go:1122`）在调用 `ag.Chat(...)` 前，若 `ag` 实现了新接口，按 `userID` 解析出的 persona 设置 override。
- `chatClaude()`（`agent/cli_agent.go:116`）组装 `args` 时：
  - 非 owner 且 override 非空：追加 `--setting-sources project,local`（排除 `~/.claude/CLAUDE.md` 所在的 `user` 来源），并把 override 的 `SystemPrompt` 拼进 `--append-system-prompt`（而非 agent 全局配置的 `system_prompt`，两者可共存，脱敏文本在后）。
  - owner：现状不变，不追加任何参数。
- `cmd.Dir` 复用现有 `userSandboxDir(userID)`（已实现，不用改）。
- **待实现时验证**：`--setting-sources` 排除 `user` 来源不影响 claude 的登录态（登录走 keychain/OAuth，理论独立于 setting sources，需实测确认不报 auth 错误）。

### 5. 存储

- 人格正文文件：`~/.weclaw/personas/<name>.md`，纯文本，UTF-8。目录不存在则创建，`default.md` 首次启动时若不存在就写入内置默认文本。不进 git（隐私目录，`~/.weclaw` 本就不在仓库里）。
- 绑定关系并入现有 `config.Config`（`config/config.go`）：

```go
type Config struct {
    // ...现有字段
    DefaultPersona string            `json:"default_persona,omitempty"` // 空 = "default"
    UserPersonas   map[string]string `json:"user_personas,omitempty"`   // userID -> persona name
}
```

复用现有 `Load`/`Save`，`DefaultConfig()` 里初始化 `UserPersonas` 为空 map。

### 6. 后台 18022 入口

在现有权限面板（`api/server.go` 的 `PermissionsPath` 一带）新增：

- **人格管理面板**：新端点 `/api/internal/personas`（GET 列出 / POST 新建或更新正文 / DELETE 删除，`default` 不可删）。
- **权限面板每个非 owner 用户加 persona 下拉**：读 `user_personas`，选择项来自人格管理面板的列表，未设置显示「默认」。写入走新端点或扩展现有 `PermissionsPath` 请求体（`PersonaName` 可选字段）。
- owner 那行固定显示「完整人格 · 不可改」，无下拉。

### 7. 错误处理

- persona 文件读取失败（被误删等）：回退到内置的最小安全文本（不含任何隐私），并记日志告警，不阻断对话。
- `user_personas` 里绑定的 persona 名字对应文件已被删除：同上回退逻辑，而非报错给用户。

## 测试计划

- `config` 包：`UserPersonas`/`DefaultPersona` 的 `Load`/`Save`/legacy 无字段兼容单测。
- `agent/cli_agent_test.go`：验证非 owner override 时 `args` 包含 `--setting-sources project,local` 和拼接后的 `--append-system-prompt`；owner/无 override 时不包含。
- `messaging/handler_test.go`：
  - persona 绑定优先级（owner > 显式绑定 > 默认）解析正确。
  - 非 owner 请求切到 codex 被拒绝，owner 请求正常。
- 冒烟（人工）：
  - 用非 owner 测试微信号给 claude 发消息，确认回复不含梁师傅任何隐私内容，语气是 default 人格。
  - 同一账号发 `/codex`（或等效切换指令），确认收到拒绝提示。
  - owner 本人操作全程无感知差异。
- `go test ./...` 全绿；`plutil -lint launchd/*.plist`（不改动 plist，回归确认）。

## 范围之外（本期不做）

- 不改造 codex 的长驻进程架构，非 owner 用户不可用 codex。
- 不修改 `~/.claude/`、`~/.codex/` 下任何文件。
- 不做 persona 的版本管理、继承、模板变量替换，纯文本注入。
- 不做多语言/多层级人格组合。
