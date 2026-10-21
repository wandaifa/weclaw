# WeClaw 本地 fork 进度

## 当前阶段

本地定制微信桥接运行中；2026-10-07 新增 owner Codex 模型管理，配套后台在相邻 wx-clawbot 项目，完整功能历史见其 ROADMAP.md。版本 1.0.2，运行产物 bin/weclaw。

## 已完成

- 首条入站消息延后欢迎已部署 1.0.2，2026-10-10 owner-only 真实发送验收通过，完成状态阻止重复发送。

- owner 微信真实消息端到端验收通过，turn_context 确认 gpt-6.1-sol/high。

- max/ultra 推理档位校验已支持，owner/shared 的真实 API 保存验证通过，保留旧 none 配置兼容。

- 本机模型管理 API 支持 owner codex、非 owner codex-shared 和共用 claude；owner 配置更新不改变 shared 实例，回归测试通过。
- 模型设置保存及重启持久化通过，实际 Codex app-server 以 gpt-6.1-sol 完成调用。

## 进行中


## 待办


## 阻塞

- messaging 全量测试的 TestProjectProgressReplyRecognizesFixedShortQuestions 使用过期项目快照，本次未改该无关测试。

## 最近验证

- 2026-10-21 提交前：仅包含延后欢迎相关改动的暂存区快照 go test ./... 全包通过，后台 Python 27 项通过；owner 真实发送沿用 2026-10-10 已通过的验收，本次没有发送微信消息。原有无关改动保留。

- 2026-10-10：TestOwnerDeferredLoginWelcomeLive 显式启用后通过，owner 真实欢迎成功，重复调用未再次发送；临时 HOME 存放欢迎状态，真实账号状态未修改，未向非 owner 发测试消息。

- 2026-10-09：扫码流程不再发送欢迎，只保存 .welcome 状态；入站带 token 且通过访问检查后仅给扫码用户欢迎一次，失败等待下一条消息，原消息继续处理。7 项回归与 race 检查通过；cmd/ilink 包测试、构建与 diff 检查通过。部署 1.0.2，现有账号未标记待欢迎。全量测试仍只有原有过期快照测试失败。owner 最后入站为 10 月 7 日，已超过 24h，真实发送待 owner 新消息后验收；没有向非 owner 发测试消息。

- 2026-10-07 收工复验：微信实际回复对应 thread 01a11284-7ad3-7d72-bef0-3fc69d9bbd7b 的 turn_context 为 gpt-6.1-sol/high。回复自述 low 是读取全局 config.toml，未反映 WeClaw 的覆盖配置；后台 High 已实际生效。本次仅补验收文档，不改运行行为、不升版。

- 2026-10-07 提交前：仅包含本次变更的暂存区快照验证通过；WeClaw go test ./... 全包通过，后台 Python 27 项、py_compile 与生成页面 node --check 通过。工作区原有改动保留未纳入提交。

- 2026-10-07：1.0.1 构建部署；agent/api/cmd 与模型定向测试通过；18022 API 对 owner/shared 的 max/ultra 保存成功，另一实例不变，收尾恢复原始 high 配置。

- 2026-10-07：go test ./agent ./api ./cmd 通过；messaging 的 TestOwnerModelConfig 与 TestSetAgentModelConfig 定向测试通过；构建 bin/weclaw 并重启 bridge，API 返回持久化模型，weclaw --version 为 1.0.0。
