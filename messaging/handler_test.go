package messaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

func newTestHandler() *Handler {
	return &Handler{agents: make(map[string]agent.Agent)}
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

func TestTryBeginChatBlocksSameAgentAndUser(t *testing.T) {
	h := newTestHandler()
	wait, ok := h.tryBeginChat("codex", "user-1")
	if !ok {
		t.Fatalf("first chat should start, wait=%s", wait)
	}

	wait, ok = h.tryBeginChat("codex", "user-1")
	if ok {
		t.Fatal("second chat for same agent and user should be blocked")
	}
	if wait < 0 {
		t.Fatalf("wait should not be negative: %s", wait)
	}

	h.endChat("codex", "user-1")
	if _, ok = h.tryBeginChat("codex", "user-1"); !ok {
		t.Fatal("chat should start after previous chat ends")
	}
}

func TestTryBeginChatAllowsDifferentUsers(t *testing.T) {
	h := newTestHandler()
	if _, ok := h.tryBeginChat("codex", "user-1"); !ok {
		t.Fatal("first user should start")
	}
	if _, ok := h.tryBeginChat("codex", "user-2"); !ok {
		t.Fatal("different user should start independently")
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
