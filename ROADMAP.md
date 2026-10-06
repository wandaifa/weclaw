# WeClaw 本地 fork 进度

## 当前阶段

本地定制微信桥接运行中；2026-10-07 新增 owner Codex 模型管理，配套后台在相邻 wx-clawbot 项目，完整功能历史见其 ROADMAP.md。版本 1.0.1，运行产物 bin/weclaw。

## 已完成

- owner 微信真实消息端到端验收通过，turn_context 确认 gpt-6.1-sol/high。

- max/ultra 推理档位校验已支持，owner/shared 的真实 API 保存验证通过，保留旧 none 配置兼容。

- 本机模型管理 API 支持 owner codex、非 owner codex-shared 和共用 claude；owner 配置更新不改变 shared 实例，回归测试通过。
- 模型设置保存及重启持久化通过，实际 Codex app-server 以 gpt-6.1-sol 完成调用。

## 进行中

## 待办


## 阻塞

- messaging 全量测试的 TestProjectProgressReplyRecognizesFixedShortQuestions 使用过期项目快照，本次未改该无关测试。

## 最近验证

- 2026-10-07 收工复验：微信实际回复对应 thread 01a11284-7ad3-7d72-bef0-3fc69d9bbd7b 的 turn_context 为 gpt-6.1-sol/high。回复自述 low 是读取全局 config.toml，未反映 WeClaw 的覆盖配置；后台 High 已实际生效。本次仅补验收文档，不改运行行为、不升版。

- 2026-10-07 提交前：仅包含本次变更的暂存区快照验证通过；WeClaw go test ./... 全包通过，后台 Python 27 项、py_compile 与生成页面 node --check 通过。工作区原有改动保留未纳入提交。

- 2026-10-07：1.0.1 构建部署；agent/api/cmd 与模型定向测试通过；18022 API 对 owner/shared 的 max/ultra 保存成功，另一实例不变，收尾恢复原始 high 配置。

- 2026-10-07：go test ./agent ./api ./cmd 通过；messaging 的 TestOwnerModelConfig 与 TestSetAgentModelConfig 定向测试通过；构建 bin/weclaw 并重启 bridge，API 返回持久化模型，weclaw --version 为 1.0.0。
