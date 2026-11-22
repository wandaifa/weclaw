package messaging

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/terminal"
)

const (
	terminalResultFooter = "/snap 看屏幕 · /key esc 中断 · /exit 退出终端模式"
	terminalBoundFooter  = "发送 /codex、/claude 或 /agy 启动 · /ls 查看会话 · /exit 退出终端模式"
	terminalListFooter   = "回复 /use 编号 切换 · /close 编号 关闭 · /new 新建"
)

// SetTerminalManager enables owner-only iTerm2 terminal control commands.
func (h *Handler) SetTerminalManager(mgr *terminal.Manager) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.terminal = mgr
}

func (h *Handler) terminalManager() *terminal.Manager {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.terminal
}

func (h *Handler) isTerminalPassthroughNoMerge(botID, userID string) bool {
	if !config.IsOwner(userID) {
		return false
	}
	mgr := h.terminalManager()
	if mgr == nil {
		return false
	}
	return mgr.InTerminalMode(botID, userID) || mgr.HasPendingConfirm(botID, userID)
}

func (h *Handler) handleOwnerTerminal(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text, clientID string) bool {
	if !config.IsOwner(msg.FromUserID) {
		return false
	}
	mgr := h.terminalManager()
	if mgr == nil {
		return false
	}

	botID := client.BotID()
	userID := msg.FromUserID
	trimmed := strings.TrimSpace(text)

	// Any message while a high-risk command is awaiting confirmation resolves or
	// cancels that confirmation first (spec: only literal "确认" runs it).
	if mgr.InTerminalMode(botID, userID) && mgr.HasPendingConfirm(botID, userID) {
		_, reply, err := mgr.ResolveConfirm(ctx, botID, userID, trimmed)
		if err != nil {
			reply = fmt.Sprintf("确认处理失败：%v", err)
		}
		h.sendTerminalReply(ctx, client, userID, reply, msg.ContextToken, clientID)
		return true
	}

	// Commands available to the owner in both chat mode and terminal mode.
	if reply, handled := h.handleSharedTerminalCommand(ctx, mgr, botID, userID, trimmed); handled {
		h.sendTerminalReply(ctx, client, userID, reply, msg.ContextToken, clientID)
		return true
	}

	if !mgr.InTerminalMode(botID, userID) {
		return false
	}

	// Terminal-mode-only control commands.
	if trimmed == "/exit" {
		mgr.Exit(botID, userID)
		reply := "已退出终端模式，回到聊天模式（终端窗口仍在电脑上运行）。\n发送 /ls 查看会话 · /use 编号 重新接入"
		h.sendTerminalReply(ctx, client, userID, reply, msg.ContextToken, clientID)
		return true
	}

	if trimmed == "/snap" {
		snap, err := mgr.Snap(ctx, botID, userID)
		if err != nil {
			h.sendTerminalReply(ctx, client, userID, fmt.Sprintf("截屏失败：%v", err), msg.ContextToken, clientID)
			return true
		}
		snap = terminal.RedactSecrets(strings.TrimSpace(snap))
		if snap == "" {
			snap = "(当前屏幕为空)"
		}
		h.sendTerminalReply(ctx, client, userID, snap+"\n\n/key esc 中断 · /exit 退出终端模式", msg.ContextToken, clientID)
		return true
	}

	if trimmed == "/key" || strings.HasPrefix(trimmed, "/key ") {
		rawKey := strings.TrimSpace(strings.TrimPrefix(trimmed, "/key"))
		if rawKey == "" {
			h.sendTerminalReply(ctx, client, userID, "用法：/key 按键（支持 esc、enter、ctrl-c、up、down、tab）", msg.ContextToken, clientID)
			return true
		}
		key, err := mgr.SendKey(ctx, botID, userID, rawKey)
		if err != nil {
			h.sendTerminalReply(ctx, client, userID, fmt.Sprintf("%v", err), msg.ContextToken, clientID)
			return true
		}
		h.sendTerminalReply(ctx, client, userID, fmt.Sprintf("已发送按键：%s", key), msg.ContextToken, clientID)
		return true
	}

	// Exact /codex, /claude, or /agy (case-insensitive) launches that CLI in the bound window.
	if strings.EqualFold(trimmed, "/codex") || strings.EqualFold(trimmed, "/claude") || strings.EqualFold(trimmed, "/agy") {
		cmdName := strings.ToLower(strings.TrimPrefix(trimmed, "/"))
		if _, err := mgr.SendInput(ctx, botID, userID, cmdName, false); err != nil {
			h.sendTerminalReply(ctx, client, userID, fmt.Sprintf("启动 %s 失败：%v", cmdName, err), msg.ContextToken, clientID)
			return true
		}
		reply := fmt.Sprintf("已在当前终端启动 %s。\n直接发消息即可与 %s 对话 · %s", cmdName, cmdName, terminalResultFooter)
		h.sendTerminalReply(ctx, client, userID, reply, msg.ContextToken, clientID)
		return true
	}

	// Double-slash escape ("//new" -> "/new") forwards a slash command literally.
	payload := strings.TrimRight(text, "\r\n")
	if strings.HasPrefix(trimmed, "//") {
		payload = trimmed[1:]
	}

	res, err := mgr.SendInput(ctx, botID, userID, payload, true)
	if err != nil {
		h.sendTerminalReply(ctx, client, userID, fmt.Sprintf("发送到终端失败：%v", err), msg.ContextToken, clientID)
		return true
	}
	if res.PendingConfirm != nil {
		reply := fmt.Sprintf(
			"检测到高危命令：%s（%s）\n回复「确认」执行，其他内容取消。60 秒内有效。",
			res.PendingConfirm.Command,
			res.PendingConfirm.Category,
		)
		h.sendTerminalReply(ctx, client, userID, reply, msg.ContextToken, clientID)
		return true
	}

	go func() {
		if err := SendTypingState(ctx, client, userID, msg.ContextToken); err != nil {
			log.Printf("[terminal] failed to send typing indicator: %v", err)
		}
	}()
	return true
}

func (h *Handler) handleSharedTerminalCommand(ctx context.Context, mgr *terminal.Manager, botID, userID, trimmed string) (string, bool) {
	if trimmed == "/new" || strings.HasPrefix(trimmed, "/new ") {
		cwd := strings.TrimSpace(strings.TrimPrefix(trimmed, "/new"))
		sess, err := mgr.NewWindow(ctx, botID, userID, cwd)
		if err != nil {
			return fmt.Sprintf("新建终端窗口失败：%v", err), true
		}
		label := sess.Name
		if label == "" {
			label = sess.SessionID
		}
		path := sess.Path
		if path == "" {
			path = "-"
		}
		return fmt.Sprintf("已新建终端窗口并进入终端模式：%s\n目录：%s\n%s", label, path, terminalBoundFooter), true
	}

	if trimmed == "/ls" {
		sessions, currentID, err := mgr.ListSessions(ctx, botID, userID)
		if err != nil {
			return fmt.Sprintf("获取会话列表失败：%v", err), true
		}
		if len(sessions) == 0 {
			return "当前没有打开的 iTerm2 会话。\n发送 /new 新建终端窗口", true
		}
		lines := make([]string, 0, len(sessions)+2)
		lines = append(lines, "当前 iTerm2 会话：")
		for _, s := range sessions {
			lines = append(lines, terminal.FormatSessionSummary(s, currentID != "" && s.SessionID == currentID))
		}
		lines = append(lines, "", terminalListFooter)
		return strings.Join(lines, "\n"), true
	}

	if trimmed == "/use" || strings.HasPrefix(trimmed, "/use ") {
		arg := strings.TrimSpace(strings.TrimPrefix(trimmed, "/use"))
		idx, err := strconv.Atoi(arg)
		if err != nil || idx <= 0 {
			return "用法：/use 编号（例如 /use 1），先发 /ls 查看编号", true
		}
		sess, err := mgr.UseSession(ctx, botID, userID, idx)
		if err != nil {
			return fmt.Sprintf("切换会话失败：%v", err), true
		}
		proj := terminal.ShortProjectName(sess.Path, sess.Name, sess.SessionID)
		job := strings.TrimLeft(strings.TrimSpace(sess.JobName), "-")
		if job == "" {
			job = "-"
		}
		if strings.TrimSpace(sess.Title) != "" {
			return fmt.Sprintf("已切换到会话 %d：**%s** · %s — %s\n%s", idx, proj, job, strings.TrimSpace(sess.Title), terminalBoundFooter), true
		}
		return fmt.Sprintf("已切换到会话 %d：**%s** · %s\n%s", idx, proj, job, terminalBoundFooter), true
	}

	if trimmed == "/close" || strings.HasPrefix(trimmed, "/close ") {
		arg := strings.TrimSpace(strings.TrimPrefix(trimmed, "/close"))
		idx, err := strconv.Atoi(arg)
		if err != nil || idx <= 0 {
			return "用法：/close 编号（例如 /close 1），先发 /ls 查看编号", true
		}
		sess, err := mgr.CloseSession(ctx, botID, userID, idx)
		if err != nil {
			return fmt.Sprintf("关闭会话失败：%v", err), true
		}
		return fmt.Sprintf("已关闭会话 %d：%s", idx, sess.Name), true
	}

	return "", false
}

func (h *Handler) sendTerminalReply(ctx context.Context, client *ilink.Client, userID, reply, contextToken, clientID string) {
	if err := SendTextReply(ctx, client, userID, reply, contextToken, clientID, ReplyMeta{Agent: "terminal", RawMarkdown: true}); err != nil {
		log.Printf("[terminal] failed to send reply to %s: %v", userID, err)
	}
}

// FormatTerminalResult formats an asynchronous shell/codex/claude/tui callback for WeChat.
// WeChat bot text messages render Markdown natively, so codex/claude/tui output
// preserves Markdown instead of flattening via MarkdownToPlainText.
func FormatTerminalResult(req terminal.ResultNotification) string {
	var body string
	switch req.Source {
	case "codex", "claude", "tui":
		body = strings.TrimSpace(req.Text)
		if body == "" {
			body = "(本轮无文本输出)"
		}
	default:
		out := strings.TrimSpace(req.Output)
		if req.ExitCode != nil && *req.ExitCode != 0 {
			if out != "" {
				body = fmt.Sprintf("[退出码 %d]\n```\n%s\n```", *req.ExitCode, out)
			} else {
				body = fmt.Sprintf("[退出码 %d]", *req.ExitCode)
			}
		} else if out != "" {
			body = fmt.Sprintf("```\n%s\n```", out)
		} else {
			body = "(命令已完成，无输出)"
		}
	}
	body = terminal.RedactSecrets(body)
	return body + "\n\n---\n" + terminalResultFooter
}
