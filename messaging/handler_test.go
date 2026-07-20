package messaging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/ilink"
)

func newTestHandler() *Handler {
	return &Handler{
		agents:     make(map[string]agent.Agent),
		userAgents: make(map[string]string),
	}
}

func TestParseCommand_NoPrefix(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("hello world")
	if len(names) != 0 {
		t.Errorf("expected nil names, got %v", names)
	}
	if msg != "hello world" {
		t.Errorf("expected full text, got %q", msg)
	}
}

func TestParseCommand_SlashWithAgent(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/claude explain this code")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "explain this code" {
		t.Errorf("expected 'explain this code', got %q", msg)
	}
}

func TestParseCommand_AtPrefix(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@claude explain this code")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "explain this code" {
		t.Errorf("expected 'explain this code', got %q", msg)
	}
}

func TestParseCommand_MultiAgent(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@cc @cx hello")
	if len(names) != 2 || names[0] != "claude" || names[1] != "codex" {
		t.Errorf("expected [claude codex], got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestParseCommand_MultiAgentDedup(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@cc @cc hello")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] (deduped), got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestParseCommand_SwitchOnly(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/claude")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "" {
		t.Errorf("expected empty message, got %q", msg)
	}
}

func TestParseCommand_Alias(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/cc write a function")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] from /cc alias, got %v", names)
	}
	if msg != "write a function" {
		t.Errorf("expected 'write a function', got %q", msg)
	}
}

func TestParseCommand_CustomAlias(t *testing.T) {
	h := newTestHandler()
	h.customAliases = map[string]string{"ai": "claude", "c": "claude"}
	names, msg := h.parseCommand("/ai hello")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] from custom alias, got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestResolveAlias(t *testing.T) {
	h := newTestHandler()
	tests := map[string]string{
		"cc":  "claude",
		"cx":  "codex",
		"oc":  "openclaw",
		"cs":  "cursor",
		"km":  "kimi",
		"gm":  "gemini",
		"ocd": "opencode",
	}
	for alias, want := range tests {
		got := h.resolveAlias(alias)
		if got != want {
			t.Errorf("resolveAlias(%q) = %q, want %q", alias, got, want)
		}
	}
	if got := h.resolveAlias("unknown"); got != "unknown" {
		t.Errorf("resolveAlias(unknown) = %q, want %q", got, "unknown")
	}
	h.customAliases = map[string]string{"cc": "custom-claude"}
	if got := h.resolveAlias("cc"); got != "custom-claude" {
		t.Errorf("resolveAlias(cc) with custom = %q, want custom-claude", got)
	}
}

func TestBuildHelpText(t *testing.T) {
	text := buildHelpText()
	if text == "" {
		t.Error("help text is empty")
	}
	if !strings.Contains(text, "/info") {
		t.Error("help text should mention /info")
	}
	if !strings.Contains(text, "/help") {
		t.Error("help text should mention /help")
	}
}

func TestAgentRecoveringReplyDoesNotEchoUserText(t *testing.T) {
	reply := agentRecoveringReply()
	if reply == "" {
		t.Fatal("recovery reply is empty")
	}
	if strings.Contains(reply, "[echo]") {
		t.Fatalf("recovery reply should not use echo mode: %q", reply)
	}
	if strings.Contains(reply, "帮我生成图片") {
		t.Fatalf("recovery reply should not include user text: %q", reply)
	}
}

func TestTryBeginChatBlocksSameUser(t *testing.T) {
	h := newTestHandler()
	wait, ok := h.tryBeginChat("user-1")
	if !ok {
		t.Fatalf("first chat should start, wait=%s", wait)
	}

	wait, ok = h.tryBeginChat("user-1")
	if ok {
		t.Fatal("second chat for same user should be blocked")
	}
	if wait < 0 {
		t.Fatalf("wait should not be negative: %s", wait)
	}

	h.endChat("user-1")
	if _, ok = h.tryBeginChat("user-1"); !ok {
		t.Fatal("chat should start after previous chat ends")
	}
}

func TestChatQueueProcessesOneConversationInOrder(t *testing.T) {
	h := newTestHandler()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var order []string

	if queued, _ := h.enqueueChat("bot\x00user", chatJob{run: func() {
		order = append(order, "first")
		close(started)
		<-release
	}}); !queued {
		t.Fatal("first message was not queued")
	}
	<-started
	if queued, _ := h.enqueueChat("bot\x00user", chatJob{run: func() {
		order = append(order, "second")
		close(done)
	}}); !queued {
		t.Fatal("second message was not queued")
	}
	close(release)
	<-done
	if got := strings.Join(order, ","); got != "first,second" {
		t.Fatalf("order = %q, want first,second", got)
	}
}

func TestMergeQueuedTextCombinesConsecutiveMessages(t *testing.T) {
	h := newTestHandler()
	if err := h.SetMergeSettings(MergeSettings{IdleDelay: time.Second, MaxWait: 2 * time.Second, MaxMessages: 10, MaxChars: 4000}); err != nil {
		t.Fatal(err)
	}
	first := chatJob{msg: ilink.WeixinMessage{FromUserID: "user", ContextToken: "first", ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "第一句"}}}}, text: "第一句", mergeable: true}
	second := chatJob{msg: ilink.WeixinMessage{FromUserID: "user", ContextToken: "second", ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "第二句"}}}}, text: "第二句", mergeable: true}
	queue := &chatQueue{jobs: []chatJob{second}, wake: make(chan struct{}, 1)}
	queue.wake <- struct{}{}

	merged := h.mergeQueuedText(queue, first)
	if got := extractText(merged.msg); got != "第一句\n第二句" {
		t.Fatalf("merged text = %q", got)
	}
	if merged.msg.ContextToken != "second" {
		t.Fatalf("context token = %q, want second", merged.msg.ContextToken)
	}
}

func TestTryBeginChatAllowsDifferentUsers(t *testing.T) {
	h := newTestHandler()
	if _, ok := h.tryBeginChat("user-1"); !ok {
		t.Fatal("first user should start")
	}
	if _, ok := h.tryBeginChat("user-2"); !ok {
		t.Fatal("different user should start independently")
	}
}

func TestAgentFailureReplyClassifiesClaudeErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "session limit", err: fmt.Errorf("You've hit your session limit"), want: "额度"},
		{name: "login", err: fmt.Errorf("OAuth login expired"), want: "登录状态"},
		{name: "startup", err: fmt.Errorf("start claude: executable file not found"), want: "启动失败"},
		{name: "generic", err: fmt.Errorf("unexpected EOF"), want: "处理失败"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply := agentFailureReply("claude", tt.err)
			if !strings.Contains(reply, tt.want) {
				t.Fatalf("reply %q does not contain %q", reply, tt.want)
			}
			if strings.Contains(reply, tt.err.Error()) {
				t.Fatalf("reply should not expose raw error: %q", reply)
			}
		})
	}
}

func TestUserAgentSelectionsAreIndependent(t *testing.T) {
	h := newTestHandler()
	h.defaultName = "codex"
	h.SetUserAgents(map[string]string{
		"user-1": "claude",
		"user-2": "codex",
	})

	if name, _, selected := h.selectedAgent("user-1"); name != "claude" || !selected {
		t.Fatalf("user-1 selection = %q, selected=%v; want claude, true", name, selected)
	}
	if name, _, selected := h.selectedAgent("user-2"); name != "codex" || !selected {
		t.Fatalf("user-2 selection = %q, selected=%v; want codex, true", name, selected)
	}
	if name, _, selected := h.selectedAgent("user-3"); name != "codex" || selected {
		t.Fatalf("user-3 selection = %q, selected=%v; want codex fallback, false", name, selected)
	}
}

func TestClaudeImageGenerationRedirect(t *testing.T) {
	requests := []string{
		"帮我生成一只银渐层小猫的图片",
		"生成一个儿童饰品海报",
		"帮我画一只猫",
		"generate an image of a cat",
	}
	for _, request := range requests {
		if !shouldRedirectClaudeImageGeneration("claude", request) {
			t.Fatalf("Claude request should redirect: %q", request)
		}
	}

	if shouldRedirectClaudeImageGeneration("codex", requests[0]) {
		t.Fatal("Codex image request should not redirect")
	}
	if shouldRedirectClaudeImageGeneration("claude", "分析图片里的商品") {
		t.Fatal("image analysis request should not redirect")
	}

	reply := claudeImageGenerationReply()
	if !strings.Contains(reply, "/codex") || !strings.Contains(reply, "重新发送") {
		t.Fatalf("redirect reply should include switch command and next step: %q", reply)
	}
}

func TestSaveInboundImage(t *testing.T) {
	dir := t.TempDir()
	data := []byte{0x89, 0x50, 0x4E, 0x47, 1, 2, 3}

	path, err := saveInboundImage(dir, "abc@im.wechat", data)
	if err != nil {
		t.Fatalf("saveInboundImage returned error: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("saved path dir = %q, want %q", filepath.Dir(path), dir)
	}
	if filepath.Ext(path) != ".png" {
		t.Fatalf("saved path ext = %q, want .png", filepath.Ext(path))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved image: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("saved data = %v, want %v", got, data)
	}
}

func TestSaveInboundMedia(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello")

	path, err := saveInboundMedia(dir, "abc@im.wechat", "file", "../report final.pdf", data, "application/pdf")
	if err != nil {
		t.Fatalf("saveInboundMedia returned error: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("saved path dir = %q, want %q", filepath.Dir(path), dir)
	}
	if filepath.Ext(path) != ".pdf" {
		t.Fatalf("saved path ext = %q, want .pdf", filepath.Ext(path))
	}
	if !strings.Contains(filepath.Base(path), "report_final.pdf") {
		t.Fatalf("saved path base = %q, want sanitized filename", filepath.Base(path))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved media: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("saved data = %v, want %v", got, data)
	}
}

func TestDefaultInboundMediaDirByKind(t *testing.T) {
	fileDir := defaultInboundMediaDir("file")
	if filepath.Base(fileDir) != "inbound-files" {
		t.Fatalf("file media dir = %q, want inbound-files", fileDir)
	}

	videoDir := defaultInboundMediaDir("video")
	if filepath.Base(videoDir) != "inbound-videos" {
		t.Fatalf("video media dir = %q, want inbound-videos", videoDir)
	}
}

func TestWithPendingMediaContextConsumesOnce(t *testing.T) {
	h := newTestHandler()
	userID := "abc@im.wechat"
	h.addPendingMedia(userID, pendingMedia{
		Kind: "file",
		Name: "report.pdf",
		Path: "/tmp/report.pdf",
	})

	first := h.withPendingMediaContext(userID, "总结这个文件")
	if !strings.Contains(first, "总结这个文件") {
		t.Fatalf("context lost original text: %q", first)
	}
	if !strings.Contains(first, "/tmp/report.pdf") {
		t.Fatalf("context missing media path: %q", first)
	}

	second := h.withPendingMediaContext(userID, "下一条")
	if second != "下一条" {
		t.Fatalf("pending media should be consumed once, got %q", second)
	}
}

func TestMessageMetaHelpers(t *testing.T) {
	if got := messageStateName(2); got != "finish" {
		t.Fatalf("messageStateName(2) = %q, want finish", got)
	}
	if got := itemTypeName(4); got != "file" {
		t.Fatalf("itemTypeName(4) = %q, want file", got)
	}
	if got := shortToken("1234567890abcdef"); got != "123456...abcdef" {
		t.Fatalf("shortToken() = %q, want masked token", got)
	}
}

func TestSafeInboundMediaNameAddsVideoExt(t *testing.T) {
	got := safeInboundMediaName("", "video", "video/mp4")
	if got != "video.mp4" {
		t.Fatalf("safeInboundMediaName() = %q, want video.mp4", got)
	}
}
