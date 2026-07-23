# 按用户身份分离人格（二期）：codex 侧隔离 + 默认路由切换

日期：2026-07-24
状态：已批准，待写实施计划
前置：一期（claude 侧）design doc 见 `2026-07-23-user-persona-isolation-design.md`，已上线并修过一次生产事故（见该文档勘误）

## 背景与需求变化

一期把非 owner 用户恒定路由到已脱敏的 claude，codex 完全保留给 owner——理由是 codex（ACP 类型）是长驻单进程，`~/.codex/AGENTS.md` 对所有会话生效，架构上做不到按用户脱敏。

现在需求变了：**梁师傅要求非 owner 用户默认改用 codex**（不再是 claude），因为 codex 自带图片生成能力、claude 没有。claude 保留作为后备，是否允许非 owner 自己切换 agent 做成后台可控开关，**默认关闭**（先不开放切换，但底层能力要做出来）。

## 技术验证（先测后设计，避免重蹈一期覆辙）

一期在生产环境验证过一次惨痛教训：`--setting-sources project,local` 看文档"该管用"，实测完全不管用，一路闯关过了单测和全分支终审才在生产环境被梁师傅亲自测出来（见一期 design doc 勘误、`mistakes-log.md` 铁律 10）。这次动手设计前，先把 codex 一侧对应的隔离机制在真实调用路径上实测过，不再重复"文档说该管用就当验证过"的错误。

**已实测证伪（不能用）：**
- `thread/start` 参数里的 `baseInstructions` **单独使用不管用**——设了这个参数，回复依然是完整的"晚秋"人设。
- `project_doc_max_bytes`（codex 官方文档描述的"合并 AGENTS.md 内容的字节上限旋钮"，设为 0 理论上应该让全局 AGENTS.md 加载不到任何内容）**实测同样不管用**——不管是通过 ACP `thread/start` 的 `config` 覆盖，还是 `codex exec -c project_doc_max_bytes=0` 直接覆盖，回复都还是完整"晚秋"人设。这条本来是三方教程文档（`ai-coding-guide` 项目，非官方一手来源）里查到的，同样属于"文档说该管用，实测不管用"。

**已实测证实（可以用）：**
- `CODEX_HOME` 环境变量指向一个隔离目录，`~/.codex/AGENTS.md`（以及 memories 等全局层内容）就不会被加载，回复变成完全通用的身份，登录态（如果该目录下有 `auth.json`）和图片生成技能都正常保留。**在真实会用到的调用路径（`codex app-server --listen stdio://` 长驻进程，手动发 `initialize`→`thread/start`→`turn/start`，跟 `ACPAgent` 实际做的事完全一致）上完整测过**，不是走简化的 `codex exec` 模式测的（那个不是生产实际路径，测了也不能直接当数据）。
- `baseInstructions` 在 `CODEX_HOME` 隔离生效之后**可以正常叠加使用**，用来注入具体的脱敏人格文本（类似一期 claude 侧 `--append-system-prompt` 的角色）。

## 已知技术坑：生成图片的落盘路径

`agent/acp_agent.go` 的 `codexGeneratedImagePaths(threadID)` 现在写死用 `os.UserHomeDir()`（真实 OS 用户主目录）拼出 `~/.codex/generated_images/<threadID>/` 去找 codex 生成的图片文件。如果非 owner 走的是 `CODEX_HOME` 被覆盖成隔离目录的 codex 进程，生成的图片实际会落在隔离目录下的 `generated_images/<threadID>/`，而这个函数还在真实主目录里找——图片生成了，但传不回微信。这个函数必须跟着改成认对应 `ACPAgent` 实例实际使用的 codex home，不能硬编码真实主目录。

## 架构设计

### 1. 新增 `codex-shared` agent 配置条目

不需要新写"多实例管理"机制——`config.json` 的 `agents` map 本来就支持同一个命令注册多个条目（现有 `openclaw`/`openclaw-acp` 就是先例：同一个底层服务，两个不同的 agent 名字、不同配置）。新增：

```json
"codex-shared": {
  "type": "acp",
  "command": "/opt/homebrew/bin/codex",
  "args": ["app-server", "--listen", "stdio://"],
  "env": {
    "CODEX_HOME": "/Users/devliang/.weclaw/codex-shared-home",
    "HTTPS_PROXY": "...", "HTTP_PROXY": "...", "NO_PROXY": "..."
  }
}
```

`~/.weclaw/codex-shared-home/` 由 weclaw 启动时确保存在（类似一期 `persona.EnsureDefault` 的角色）：目录里放一个指向真实 `~/.codex/auth.json` 的软链（登录态自动跟着真实账号走，token 轮转不用维护同步），不软链 `AGENTS.md`/`memories`——天然隔离，不需要额外的"排除逻辑"。

**`config.toml` 不是"带不带"的二选一，是单独写一份精简版**（不软链真实那份，两边完全独立，改一边不影响另一边）。核实过真实账号 `~/.codex/config.toml` 里实际有意义的几项：

- `approval_policy`/`sandbox_mode`：**不用管**，weclaw 自己在 `getOrCreateThread` 构造 `thread/start` 参数时已经显式传了 `approvalPolicy`/`sandbox`（`codexSandboxParams` 算出来的），程序传的值本来就盖过 config.toml，带不带这两项都没区别。
- `sandbox_workspace_write.network_access`：真实账号是 `true`。**这条必须在精简 config.toml 里显式写 `true`**——如果落到 codex 默认的"不联网"，生图这类需要联网的能力大概率跑不通，等于白隔离了。
- `[[hooks.Stop]]`（token-tracker 状态栏脚本）、`[projects...] trust_level`：真实账号里这些是梁师傅个人终端习惯用的东西，跟 bot 回复内容无关，`codex-shared` 不带。
- `model`/`model_reasoning_effort`：**梁师傅要求单独控制、不跟随真实账号**——非 owner 走这个高频路径，用真实账号那个 `high` 推理强度档位成本会明显更高。走 weclaw 自己已有的机制：`agents.codex-shared` 配置条目的 `model` 字段（跟 `getOrCreateThread` 里 `params["model"] = a.model` 这条现成的 per-thread 覆盖一样，`ACPAgentConfig.Model` 早就有了，不用新增字段），具体档位留给梁师傅在 config.json 里配置决定，本设计不预设具体模型名。`model_reasoning_effort` 目前没找到对应的 `thread/start` 顶层参数，**写实施计划前需要先实测**它到底能不能通过 `thread/start` 的 `config` 覆盖对象生效（今天已经证明这个 `config` 覆盖对 `project_doc_max_bytes` 不生效，不能想当然认为对其他 key 也不生效或生效，必须单独测）——测不出来就先用 codex 默认推理强度，不强求。

owner 继续用现有的 `codex` 条目（真实 `CODEX_HOME`、真实 `config.toml`、真实 `model`，行为完全不变）。

### 2. `ACPAgent` 补 `PersonaAwareAgent` 接口

一期只有 `CLIAgent`（claude）实现了 `agent.PersonaAwareAgent`。现在 `ACPAgent`（codex）也要实现，复用已有的按 conversationID 缓存 override 的模式（跟 `CLIAgent.personas` 同构）。

集成点是 `getOrCreateThread`（`agent/acp_agent.go`，构造 `thread/start` 的 `params` map 那段，现在只有 `approvalPolicy`/`cwd`/`sandbox`/`model` 四个字段）：非 owner 会话按已解析好的 persona override，往 `params` 里加 `baseInstructions`。

限制（要在代码注释里写清楚）：`baseInstructions` 只在**创建新 thread 时**生效——`getOrCreateThread` 对已存在的 conversationID 会直接复用缓存的 thread，不会重新走 `thread/start`。也就是说人格绑定变更对**已经在进行中的对话**不会立刻生效，要等用户开新会话（跟一期 claude 侧「每轮都重新拼 `--append-system-prompt`」不同——codex 是长驻 thread，claude 是每轮重新起进程）。

人格文本复用一期已有的 `persona` 包和 `~/.weclaw/personas/*.md` 存储、`persona.Load`/绑定解析逻辑（`Handler.personaOverride`）完全不用改，codex 和 claude 共享同一套人格定义，只是注入方式不同（`baseInstructions` vs `--append-system-prompt` + `--safe-mode`）。

### 3. 路由规则变更 + 后台开关

`messaging/handler.go` 的 `nonOwnerAgentName` 常量（一期写死 `"claude"`）改成可配置：

- `config.Config` 新增 `NonOwnerDefaultAgent string`（空值兜底 `"codex-shared"`）和 `AllowNonOwnerAgentSwitch bool`（默认 `false`）。
- `selectedAgent()`：`AllowNonOwnerAgentSwitch` 为 `false` 时，非 owner 恒定路由到 `NonOwnerDefaultAgent`（不管 `userAgents`/`defaultName`），跟一期的行为结构完全一样，只是目标从写死的 `"claude"` 变成可配置值。
- `AllowNonOwnerAgentSwitch` 为 `true` 时（本期只搭好开关和路由分支，实际把这条路走通留到梁师傅真要开的时候）：非 owner 可以用 `/claude`、`/codex`（映射到 `codex-shared`，不是真实 `codex`）这类命令切换，但**只能在 `{"claude", "codex-shared"}` 这个白名单内切**，任何时候都碰不到真实的 `codex`（owner 专属）或其他 agent。`handleMessage` 里"非 owner 完全跳过命令解析"的那道门（`if !owner { h.sendToDefaultAgent(...); return }`）要在这个开关打开时松开一道缝，仅放行这两个 agent 名字的切换命令，其余命令（`/cwd`、broadcast 等）依然对非 owner 不可见。

18022 后台设置页新增这两项的配置入口（复用现有 access-mode 面板的 Provider/Controller 模式）。

### 4. 修复生成图片路径 bug

`codexGeneratedImagePaths` 改成接受一个 `home string` 参数而非内部调用 `os.UserHomeDir()`；`ACPAgent` 加一个小helper（读 `a.env["CODEX_HOME"]`，为空则退回真实 `os.UserHomeDir()`）算出自己实际生效的 codex home，调用处传进去。owner 走真实 codex 条目（没设 `CODEX_HOME` env），helper 退回真实主目录，行为完全不变；`codex-shared` 走隔离目录，图片路径也跟着对。

## 存储

不新增人格存储——复用一期的 `persona` 包和 `~/.weclaw/personas/*.md`。

`config.Config` 新增两个字段（`config/config.go`）：
```go
NonOwnerDefaultAgent      string `json:"non_owner_default_agent,omitempty"`
AllowNonOwnerAgentSwitch  bool   `json:"allow_non_owner_agent_switch,omitempty"`
```

`~/.weclaw/codex-shared-home/config.toml` 是新增的、独立维护的一份精简配置（不进 weclaw 仓库、不进 git，跟 `~/.weclaw/` 下其他运行时产物一样是本机状态），内容只有 `sandbox_workspace_write.network_access = true` 这一条是本设计明确要求的；由 weclaw 启动时确保存在（同一处逻辑负责建目录、建软链、写这份精简 config.toml，若已存在则不覆盖，避免抹掉手工调整）。

## 测试计划

- **写实施计划前的实测前置任务**：确认 `model_reasoning_effort` 能否通过 `thread/start` 的 `config` 覆盖对象生效（今天已经证明这个 `config` 覆盖对 `project_doc_max_bytes` 不生效，不能假设对其他 key 也不生效或生效，必须单独拿真实 `app-server` 进程测一次）。测出结果再决定要不要把它也做成可配置项，测不出来就先用 codex 默认值，不阻塞其余任务。
- `agent` 包：`ACPAgent` 实现 `PersonaAwareAgent` 的单测（`getOrCreateThread` 在有 override 时 `thread/start` 参数带 `baseInstructions`，无 override/owner 时不带）；`codexGeneratedImagePaths(home, threadID)` 改造后的参数化测试。
- `messaging` 包：`selectedAgent()` 在 `AllowNonOwnerAgentSwitch=false`/`true` 两种状态下的路由行为；`AllowNonOwnerAgentSwitch=true` 时非 owner 切换只能命中白名单内的两个 agent。
- `config` 包：新字段的 JSON round-trip + legacy 配置兼容（字段不存在时 `NonOwnerDefaultAgent` 空值要在使用处兜底成 `"codex-shared"`，不能让空字符串直接当 agent 名字用）。
- **手动实测（不可省略，这是一期教训直接换来的纪律）**：weclaw 编译、`codex-shared` 条目跑起来后，拿真实非 owner 微信号发消息，人工确认回复不含任何私人信息、且请求生成一张图片能正常收到（重点验证 `network_access=true` 那条精简配置真的让生图跑通了）——不能只看单测绿、只看参数拼对了就当验证过。

## 范围之外（本期不做）

- `AllowNonOwnerAgentSwitch=true` 时的实际后台勾选、非 owner 切换命令的完整交互打磨——本期只需要把开关和路由分支的骨架搭对，默认关闭不影响现状。
- 二期不涉及图片*识别*能力（claude 缺 `ImageChatAgent`），那是 `weclaw-feature-gaps.md` 第 5 条的独立任务。
- 不改动 owner 自己使用 codex 的任何行为（继续用真实 `CODEX_HOME`，完整能力不受影响）。
