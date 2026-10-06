# WeClaw 本地 fork 规则

本项目为微信 iLink 与本机 Claude/Codex CLI 的 Go 桥接。遵循相邻 wx-clawbot/AGENTS.md 的安全、权限隔离及精准修改约定。

## 目录约定

- agent/：CLI 与 app-server 协议适配。
- api/：本机管理 API。
- cmd/：启动、配置与命令组装。
- messaging/：微信路由、权限、收发。
- config/、store/：配置与持久化。
- 新目录先写结构约定，不预建空目录。

## 验证

运行改动所在包的 go test，再构建到本项目 bin/weclaw（/opt/homebrew/bin/weclaw 的实际软链接目标）；全量测试存在历史失败时如实报告。不得放宽 owner/非 owner 隔离。模型设置分别允许 codex（owner）、codex-shared（非 owner）和 claude（共用）。

## 版本

版本唯一事实源为 VERSION，格式为主版本.次版本.修订号；构建用 -ldflags 写入 cmd.Version，CLI --version 展示。此前构建只标 dev，本次建立首个可追踪版本 1.0.0。
