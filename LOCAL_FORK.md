# WeClaw Wandaifa Fork

This repository is Liang's fork of `github.com/fastclaw-ai/weclaw` for personal WeChat bot customization.

## Source

- Upstream module: `github.com/fastclaw-ai/weclaw`
- Fork owner: `wandaifa`
- First customization date: `2026-07-17`

## Local Changes

1. Codex app-server image input support
   - `agent/acp_agent.go`
   - `ChatWithImage` now routes Codex app-server agents through `thread/start` and `turn/start`.
   - Image payload is sent as Codex `UserInput`:
     - text: `{ "type": "text", "text": "...", "text_elements": [] }`
     - image: `{ "type": "image", "url": "data:<mime>;base64,<data>", "detail": "high" }`
   - This fixes the old broken path that called legacy ACP `session/new` against Codex app-server.

2. Image prompt text
   - `messaging/handler.go`
   - The image prompt is changed to Chinese: `请识别并描述这张图片的内容。`

3. Web embed placeholder
   - `web/out/.keep`
   - Keeps Go embed buildable when the source tree has no built frontend output.

4. Bot/user identity logging
   - `messaging/handler.go`
   - `messaging/sender.go`
   - `ilink/monitor.go`
   - Incoming text/image logs, outgoing reply/typing logs, and monitor warnings now include `bot=<bot_id>`.
   - This lets `wx-clawbot` maintain exact `@im.bot` ↔ `@im.wechat` relationship records for new messages.

5. Account hot reload after login
   - `cmd/login.go`
   - `cmd/account_manager.go`
   - `cmd/start.go`
   - `api/server.go`
   - `weclaw login` now asks the running local service to load saved credentials immediately.
   - New accounts start one monitor without restarting agents or existing account monitors.
   - Repeated reloads are idempotent, and changed credentials replace the matching bot monitor.
   - The internal reload endpoint only accepts loopback requests.

## Build

```bash
go test ./...
go build -o weclaw .
```

## Run

```bash
weclaw stop
./weclaw start
tail -f ~/.weclaw/weclaw.log
```

## Verified

On `2026-07-17`, a WeChat image message was downloaded, passed to Codex via `ChatWithImage`, and replied successfully without the previous `session/new` protocol error.
