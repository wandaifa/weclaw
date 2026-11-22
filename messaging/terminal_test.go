package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/terminal"
)

type fakeTermBridge struct {
	sessions  []terminal.SessionInfo
	newSess   terminal.SessionInfo
	sentTexts []string
	sentKeys  []string
	snapText  string
	closedIDs []string
}

func (f *fakeTermBridge) ListSessions(context.Context) ([]terminal.SessionInfo, error) {
	return append([]terminal.SessionInfo(nil), f.sessions...), nil
}

func (f *fakeTermBridge) NewSession(_ context.Context, cwd string) (terminal.SessionInfo, error) {
	s := f.newSess
	if cwd != "" {
		s.Path = cwd
	}
	return s, nil
}

func (f *fakeTermBridge) SendText(_ context.Context, sessionID, text string, submit bool) (terminal.SendResult, error) {
	f.sentTexts = append(f.sentTexts, sessionID+":"+text)
	job := "zsh"
	for _, s := range f.sessions {
		if s.SessionID == sessionID && s.JobName != "" {
			job = s.JobName
		}
	}
	return terminal.SendResult{OK: true, SessionID: sessionID, JobName: job}, nil
}

func (f *fakeTermBridge) SendKey(_ context.Context, sessionID, key string) error {
	f.sentKeys = append(f.sentKeys, sessionID+":"+key)
	return nil
}

func (f *fakeTermBridge) Snap(context.Context, string) (string, error) {
	return f.snapText, nil
}

func (f *fakeTermBridge) CloseSession(_ context.Context, sessionID string) error {
	f.closedIDs = append(f.closedIDs, sessionID)
	return nil
}

func makeWeChatTextMsg(id int64, from, text string) ilink.WeixinMessage {
	return ilink.WeixinMessage{
		MessageID:    ilink.MessageID(id),
		FromUserID:   from,
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx-tok",
		ItemList: []ilink.MessageItem{{
			Type:     ilink.ItemTypeText,
			TextItem: &ilink.TextItem{Text: text},
		}},
	}
}

func TestOwnerTerminalFlowAndNonOwnerIsolation(t *testing.T) {
	srv, sent := newFakeIlinkServer(t)
	defer srv.Close()

	client := ilink.NewClient(&ilink.Credentials{
		ILinkBotID: "bot-1@im.bot",
		BotToken:   "tok",
		BaseURL:    srv.URL,
	})

	ownerID := config.OwnerUserIDs()[0]
	nonOwnerID := "stranger@im.wechat"

	codexAgent := &recordingAgent{}
	sharedAgent := &recordingAgent{}
	h := newTestHandler()
	h.defaultName = "codex"
	h.agents["codex"] = codexAgent
	h.agents["codex-shared"] = sharedAgent

	bridge := &fakeTermBridge{
		sessions: []terminal.SessionInfo{
			{Index: 1, SessionID: "s1", Name: "zsh", Path: "/repo", JobName: "zsh"},
			{Index: 2, SessionID: "s2", Name: "work", Path: "/app", JobName: "codex"},
		},
		newSess:  terminal.SessionInfo{Index: 3, SessionID: "s3", Name: "zsh", Path: "/tmp", JobName: "zsh"},
		snapText: "screen snapshot line",
	}
	mgr := terminal.NewManager(bridge, filepath.Join(t.TempDir(), "term.json"))
	h.SetTerminalManager(mgr)

	ctx := context.Background()

	// 1. Non-owner /new still resets non-owner's chat session and never hits terminal.
	h.HandleMessage(ctx, client, makeWeChatTextMsg(1, nonOwnerID, "/new"))
	h.HandleMessage(ctx, client, makeWeChatTextMsg(2, nonOwnerID, "/ls"))
	h.waitForIdle()
	if len(bridge.sentTexts) != 0 || mgr.InTerminalMode("bot-1@im.bot", nonOwnerID) {
		t.Fatalf("non-owner must never touch terminal bridge")
	}
	if calls := sharedAgent.callsSnapshot(); len(calls) != 1 || calls[0] != "/ls" {
		t.Fatalf("expected non-owner /ls to route to shared agent as chat text, got %v", calls)
	}

	// 2. Owner chat-mode /codex keeps existing chat switch behavior.
	h.HandleMessage(ctx, client, makeWeChatTextMsg(3, ownerID, "/codex"))
	h.waitForIdle()
	if len(bridge.sentTexts) != 0 {
		t.Fatalf("chat-mode /codex should not hit terminal")
	}

	// 3. Owner /new opens window & enters terminal mode.
	h.HandleMessage(ctx, client, makeWeChatTextMsg(4, ownerID, "/new /tmp"))
	h.waitForIdle()
	if !mgr.InTerminalMode("bot-1@im.bot", ownerID) {
		t.Fatal("owner should be in terminal mode after /new")
	}

	// 4. Case-insensitive exact /Codex and /Agy launch in bound session;
	// "/codex hello" passes through literally.
	h.HandleMessage(ctx, client, makeWeChatTextMsg(5, ownerID, "/Codex"))
	h.HandleMessage(ctx, client, makeWeChatTextMsg(6, ownerID, "/codex hello"))
	h.HandleMessage(ctx, client, makeWeChatTextMsg(7, ownerID, "//new"))
	h.HandleMessage(ctx, client, makeWeChatTextMsg(15, ownerID, "/Agy"))
	h.waitForIdle()
	if len(bridge.sentTexts) < 4 {
		t.Fatalf("expected 4 terminal sends, got %v", bridge.sentTexts)
	}
	if bridge.sentTexts[0] != "s3:codex" || bridge.sentTexts[1] != "s3:/codex hello" || bridge.sentTexts[2] != "s3:/new" || bridge.sentTexts[3] != "s3:agy" {
		t.Fatalf("unexpected sentTexts: %v", bridge.sentTexts)
	}

	// 5. High-risk command in shell session pauses and requires literal "确认".
	h.HandleMessage(ctx, client, makeWeChatTextMsg(8, ownerID, "/use 1"))
	h.HandleMessage(ctx, client, makeWeChatTextMsg(9, ownerID, "rm -rf build"))
	h.waitForIdle()
	if len(bridge.sentTexts) != 4 {
		t.Fatalf("dangerous command should pause before SendText, got %v", bridge.sentTexts)
	}
	h.HandleMessage(ctx, client, makeWeChatTextMsg(10, ownerID, "确认"))
	h.waitForIdle()
	if len(bridge.sentTexts) != 5 || bridge.sentTexts[4] != "s1:rm -rf build" {
		t.Fatalf("expected confirmed execution, got %v", bridge.sentTexts)
	}

	// 6. /snap, /key esc, /exit, and /clear in chat mode.
	h.HandleMessage(ctx, client, makeWeChatTextMsg(11, ownerID, "/snap"))
	h.HandleMessage(ctx, client, makeWeChatTextMsg(12, ownerID, "/key esc"))
	h.HandleMessage(ctx, client, makeWeChatTextMsg(13, ownerID, "/exit"))
	h.HandleMessage(ctx, client, makeWeChatTextMsg(14, ownerID, "/clear"))
	h.waitForIdle()

	if mgr.InTerminalMode("bot-1@im.bot", ownerID) {
		t.Fatal("owner should have exited terminal mode")
	}
	if len(bridge.sentKeys) != 1 || bridge.sentKeys[0] != "s1:esc" {
		t.Fatalf("sentKeys = %v", bridge.sentKeys)
	}
	allReplies := strings.Join(sent.snapshot(), "\n---\n")
	for _, want := range []string{"已新建终端窗口", "screen snapshot line", "已发送按键：esc", "已退出终端模式", "已创建新的"} {
		if !strings.Contains(allReplies, want) {
			t.Errorf("missing %q in replies:\n%s", want, allReplies)
		}
	}
	if calls := codexAgent.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("terminal commands should not leak to chat agent, got %v", calls)
	}
}

func TestBuildHelpTextNonOwnerStillHidesTerminal(t *testing.T) {
	if got := buildHelpText(false); strings.Contains(got, "/ls") || strings.Contains(got, "/snap") {
		t.Fatalf("non-owner help leaked terminal commands: %s", got)
	}
	if got := buildHelpText(true); !strings.Contains(got, "/ls") || !strings.Contains(got, "/clear") {
		t.Fatalf("owner help should list terminal and /clear commands: %s", got)
	}
}

func TestFormatTerminalResultRedactsAndFormats(t *testing.T) {
	code := 2
	out := FormatTerminalResult(terminal.ResultNotification{
		SessionID: "s1",
		Source:    "shell",
		ExitCode:  &code,
		Output:    "TOKEN=sk-1234567890abcdef123456",
	})
	if strings.Contains(out, "sk-1234567890abcdef123456") {
		t.Fatalf("secret leaked: %s", out)
	}
	if !strings.Contains(out, "[退出码 2]") || !strings.Contains(out, "/snap 看屏幕") {
		t.Fatalf("unexpected formatted output: %s", out)
	}

	codexOut := FormatTerminalResult(terminal.ResultNotification{
		SessionID: "s1",
		Source:    "codex",
		Text:      "### 1. 标题\n- **技术栈**：[package.json](file:///Users/a/package.json)",
	})
	chunks := WeChatMarkdownChunks(codexOut, 16000)
	if len(chunks) != 1 || !strings.Contains(chunks[0], "### 1. 标题") || !strings.Contains(chunks[0], "**技术栈**") || !strings.Contains(chunks[0], "`package.json`") {
		t.Fatalf("expected Markdown preserved in WeChatMarkdownChunks: %v", chunks)
	}
}

func TestTerminalReplySendsContiguousMarkdownTable(t *testing.T) {
	srv, sent := newFakeIlinkServer(t)
	defer srv.Close()
	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot-1@im.bot", BotToken: "tok", BaseURL: srv.URL})
	reply := FormatTerminalResult(terminal.ResultNotification{
		Source: "codex",
		Text:   "| 维度 | 内容 |\n| --- | --- |\n| **项目名称** | `ai-video-studio` |\n| 命令 | `npm test` |",
	})
	h := newTestHandler()
	h.sendTerminalReply(context.Background(), client, config.OwnerUserIDs()[0], reply, "ctx", "test-table")
	want := "| 维度 | 内容 |\n| --- | --- |\n| **项目名称** | `ai-video-studio` |\n| 命令 | `npm test` |\n\n---\n\n/snap 看屏幕 · /key esc 中断 · /exit 退出终端模式"
	if got := sent.snapshot(); len(got) != 1 || got[0] != want {
		t.Fatalf("sent table = %q\nwant one message with %q", got, want)
	}
}

// Keep unused agent import check clean if refactoring later.
var _ agent.Agent = (*recordingAgent)(nil)
