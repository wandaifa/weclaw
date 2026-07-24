package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/persona"
)

func newTestHandler() *Handler {
	return &Handler{
		agents:       make(map[string]agent.Agent),
		userAgents:   make(map[string]string),
		permissions:  make(map[string]config.UserPermission),
		userPersonas: make(map[string]string),
		usage:        make(map[string]*dailyUsage),
	}
}

// recordingAgent is a minimal agent.Agent test double that records every
// message it receives without doing any real work.
type recordingAgent struct {
	mu               sync.Mutex
	calls            []string
	personaOverrides map[string]agent.PersonaOverride
}

func (a *recordingAgent) Chat(_ context.Context, _ string, message string) (string, error) {
	a.mu.Lock()
	a.calls = append(a.calls, message)
	a.mu.Unlock()
	return "ok", nil
}
func (a *recordingAgent) ResetSession(_ context.Context, _ string) (string, error) { return "", nil }
func (a *recordingAgent) Info() agent.AgentInfo {
	return agent.AgentInfo{Name: "fake", Type: "fake", Model: "fake-model"}
}
func (a *recordingAgent) SetCwd(string) {}

func (a *recordingAgent) SetPersonaOverride(conversationID string, override agent.PersonaOverride) {
	a.mu.Lock()
	if a.personaOverrides == nil {
		a.personaOverrides = make(map[string]agent.PersonaOverride)
	}
	a.personaOverrides[conversationID] = override
	a.mu.Unlock()
}

func (a *recordingAgent) personaOverrideFor(conversationID string) (agent.PersonaOverride, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	override, ok := a.personaOverrides[conversationID]
	return override, ok
}

func (a *recordingAgent) callsSnapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

// newFakeIlinkServer stubs the three iLink endpoints a normal chat turn
// touches (getconfig for the typing ticket, sendtyping, sendmessage for the
// reply) so tests can exercise HandleMessage end-to-end without hitting the
// real WeChat bot API. It records every reply text sent via sendmessage.
func newFakeIlinkServer(t *testing.T) (*httptest.Server, *sentReplies) {
	t.Helper()
	sent := &sentReplies{}
	mux := http.NewServeMux()
	mux.HandleFunc("/ilink/bot/getconfig", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"ret": 0, "typing_ticket": "tk"})
	})
	mux.HandleFunc("/ilink/bot/sendtyping", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"ret": 0})
	})
	mux.HandleFunc("/ilink/bot/sendmessage", func(w http.ResponseWriter, r *http.Request) {
		var req ilink.SendMessageRequest
		json.NewDecoder(r.Body).Decode(&req)
		text := ""
		if len(req.Msg.ItemList) > 0 && req.Msg.ItemList[0].TextItem != nil {
			text = req.Msg.ItemList[0].TextItem.Text
		}
		sent.add(text)
		json.NewEncoder(w).Encode(map[string]interface{}{"ret": 0})
	})
	return httptest.NewServer(mux), sent
}

type sentReplies struct {
	mu    sync.Mutex
	texts []string
}

func (s *sentReplies) add(text string) {
	s.mu.Lock()
	s.texts = append(s.texts, text)
	s.mu.Unlock()
}

func (s *sentReplies) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.texts...)
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("condition not met before timeout")
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
	text := buildHelpText(true)
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

func TestBuildHelpTextHidesAgentSwitchForNonOwner(t *testing.T) {
	text := buildHelpText(false)
	for _, hidden := range []string{"@agent", "/cwd", "Aliases:"} {
		if strings.Contains(text, hidden) {
			t.Errorf("non-owner help text should not mention %q, got: %s", hidden, text)
		}
	}
	if !strings.Contains(text, "/info") || !strings.Contains(text, "/help") {
		t.Error("non-owner help text should still mention /info and /help")
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

func TestSelectedAgentIgnoresUserAgentsOverrideForNonOwner(t *testing.T) {
	h := newTestHandler()
	h.defaultName = "codex"
	h.SetUserAgents(map[string]string{
		"user-1": "claude",
		"user-2": "codex",
	})

	// Non-owner users are always routed to defaultNonOwnerAgentName — see
	// selectedAgent's comment — regardless of any userAgents override or
	// the configured default agent.
	if name, _, selected := h.selectedAgent("user-1"); name != "codex-shared" || !selected {
		t.Fatalf("user-1 selection = %q, selected=%v; want codex-shared, true", name, selected)
	}
	if name, _, selected := h.selectedAgent("user-2"); name != "codex-shared" || !selected {
		t.Fatalf("user-2 selection = %q, selected=%v; want codex-shared, true (override ignored for non-owner)", name, selected)
	}
	if name, _, selected := h.selectedAgent("user-3"); name != "codex-shared" || !selected {
		t.Fatalf("user-3 selection = %q, selected=%v; want codex-shared fallback, true", name, selected)
	}
}

func TestSelectedAgentHonorsOwnerUserAgentsOverride(t *testing.T) {
	h := newTestHandler()
	h.defaultName = "codex"
	ownerID := config.OwnerUserIDs()[0]
	h.SetUserAgents(map[string]string{ownerID: "claude"})

	if name, _, selected := h.selectedAgent(ownerID); name != "claude" || !selected {
		t.Fatalf("owner selection = %q, selected=%v; want claude, true", name, selected)
	}
}

func TestSelectedAgentDefaultsNonOwnerToCodexSharedWhenUnconfigured(t *testing.T) {
	h := newTestHandler()
	h.defaultName = "codex"
	if name, _, selected := h.selectedAgent("user-x"); name != "codex-shared" || !selected {
		t.Fatalf("selectedAgent(non-owner) = (%q, selected=%v), want (\"codex-shared\", true) when NonOwnerDefaultAgent is unset", name, selected)
	}
}

func TestSelectedAgentHonorsConfiguredNonOwnerDefaultAgent(t *testing.T) {
	h := newTestHandler()
	h.SetNonOwnerDefaultAgent("claude")
	if name, _, selected := h.selectedAgent("user-x"); name != "claude" || !selected {
		t.Fatalf("selectedAgent(non-owner) = (%q, selected=%v), want (\"claude\", true) after SetNonOwnerDefaultAgent(\"claude\")", name, selected)
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

func newTestMessage(userID, text string, messageID int64) ilink.WeixinMessage {
	return ilink.WeixinMessage{
		FromUserID:   userID,
		MessageID:    messageID,
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: text}},
		},
	}
}

func TestNonOwnerAgentSwitchPrefixIsPlainText(t *testing.T) {
	srv, _ := newFakeIlinkServer(t)
	defer srv.Close()

	h := NewHandler(nil, nil)
	fake := &recordingAgent{}
	h.SetDefaultAgent("codex-shared", fake)

	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	msg := newTestMessage("non-owner-test@im.wechat", "/codex hello", 1)
	h.HandleMessage(context.Background(), client, msg)

	waitFor(t, 2*time.Second, func() bool { return len(fake.callsSnapshot()) > 0 })
	calls := fake.callsSnapshot()
	if len(calls) != 1 || calls[0] != "/codex hello" {
		t.Fatalf("non-owner /codex text should reach the default agent verbatim, got %v", calls)
	}
}

func TestNonOwnerCwdIsPlainText(t *testing.T) {
	srv, _ := newFakeIlinkServer(t)
	defer srv.Close()

	h := NewHandler(nil, nil)
	fake := &recordingAgent{}
	h.SetDefaultAgent("codex-shared", fake)
	h.SetAgentWorkDirs(map[string]string{"codex": "/original/cwd"})

	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	msg := newTestMessage("non-owner-test@im.wechat", "/cwd /tmp/should-not-apply", 2)
	h.HandleMessage(context.Background(), client, msg)

	waitFor(t, 2*time.Second, func() bool { return len(fake.callsSnapshot()) > 0 })
	calls := fake.callsSnapshot()
	if len(calls) != 1 || calls[0] != "/cwd /tmp/should-not-apply" {
		t.Fatalf("non-owner /cwd should be forwarded as plain text, got %v", calls)
	}
}

func TestOwnerCwdStillWorks(t *testing.T) {
	srv, _ := newFakeIlinkServer(t)
	defer srv.Close()

	h := NewHandler(nil, nil)
	fake := &recordingAgent{}
	h.SetDefaultAgent("codex", fake)
	dir := t.TempDir()

	ownerID := config.OwnerUserIDs()[0]
	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	msg := newTestMessage(ownerID, "/cwd "+dir, 3)
	h.HandleMessage(context.Background(), client, msg)

	waitFor(t, 2*time.Second, func() bool {
		h.mu.RLock()
		defer h.mu.RUnlock()
		return h.agentWorkDirs["codex"] == dir
	})
	if len(fake.callsSnapshot()) != 0 {
		t.Fatalf("owner /cwd should not be forwarded to the agent, got %v", fake.callsSnapshot())
	}
}

func TestQuotaExceededBlocksNonOwnerBeforeAgentCall(t *testing.T) {
	srv, sent := newFakeIlinkServer(t)
	defer srv.Close()

	h := NewHandler(nil, nil)
	fake := &recordingAgent{}
	h.SetDefaultAgent("codex-shared", fake)
	h.SetUserPermission("quota-test@im.wechat", config.UserPermission{Level: config.PermissionReadOnly, DailyLimit: 1})
	// Plain text without a "/" or "@" prefix is subject to the consecutive-message
	// merge wait; use the minimum allowed idle delay so the test doesn't sit
	// through the 3s default before each turn dispatches.
	if err := h.SetMergeSettings(MergeSettings{IdleDelay: time.Second, MaxWait: time.Second, MaxMessages: 10, MaxChars: 4000}); err != nil {
		t.Fatalf("SetMergeSettings: %v", err)
	}

	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})

	h.HandleMessage(context.Background(), client, newTestMessage("quota-test@im.wechat", "first", 10))
	waitFor(t, 4*time.Second, func() bool { return len(fake.callsSnapshot()) == 1 })

	h.HandleMessage(context.Background(), client, newTestMessage("quota-test@im.wechat", "second", 11))
	waitFor(t, 4*time.Second, func() bool { return len(sent.snapshot()) >= 2 })

	if calls := fake.callsSnapshot(); len(calls) != 1 {
		t.Fatalf("second message should not reach the agent once quota is exhausted, got calls=%v", calls)
	}
	found := false
	for _, text := range sent.snapshot() {
		if text == quotaExceededReply() {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected quota exceeded reply to be sent, got %v", sent.snapshot())
	}
}

func TestQuotaDoesNotLimitOwner(t *testing.T) {
	h := newTestHandler()
	ownerID := config.OwnerUserIDs()[0]
	for i := 0; i < 500; i++ {
		if !h.consumeQuota(ownerID) {
			t.Fatalf("owner should never be quota-limited, blocked at attempt %d", i)
		}
	}
}

func TestUsageSnapshotTracksOwnerAsUnlimited(t *testing.T) {
	h := newTestHandler()
	ownerID := config.OwnerUserIDs()[0]
	for i := 0; i < 3; i++ {
		h.consumeQuota(ownerID)
	}

	var entry *UsageEntry
	for _, e := range h.UsageSnapshot() {
		if e.UserID == ownerID {
			e := e
			entry = &e
		}
	}
	if entry == nil {
		t.Fatal("owner should appear in the usage snapshot once they've sent messages")
	}
	if entry.Count != 3 {
		t.Fatalf("owner usage count = %d, want 3", entry.Count)
	}
	if entry.Limit != 0 {
		t.Fatalf("owner limit = %d, want 0 (unlimited)", entry.Limit)
	}
}

func TestConversationPolicyOwnerVsNonOwner(t *testing.T) {
	h := newTestHandler()
	ownerID := config.OwnerUserIDs()[0]

	ownerPolicy := h.conversationPolicy(ownerID)
	if ownerPolicy.Level != agent.SandboxFullAccess {
		t.Fatalf("owner policy level = %v, want full_access", ownerPolicy.Level)
	}

	readOnly := h.conversationPolicy("stranger@im.wechat")
	if readOnly.Level != agent.SandboxReadOnly {
		t.Fatalf("default non-owner policy level = %v, want read_only", readOnly.Level)
	}
	if readOnly.Cwd == "" || len(readOnly.WritableRoots) != 1 || readOnly.WritableRoots[0] != readOnly.Cwd {
		t.Fatalf("non-owner policy should have a private cwd/writable root, got %+v", readOnly)
	}

	h.SetUserPermission("upgraded@im.wechat", config.UserPermission{Level: config.PermissionWorkspaceWrite})
	upgraded := h.conversationPolicy("upgraded@im.wechat")
	if upgraded.Level != agent.SandboxWorkspaceWrite {
		t.Fatalf("upgraded policy level = %v, want workspace_write", upgraded.Level)
	}
	if upgraded.Cwd == readOnly.Cwd {
		t.Fatalf("different non-owner users must not share a sandbox directory")
	}
}

func TestSanitizeUserIDForPathIsSafeAndStable(t *testing.T) {
	id := "o9cq80ykt-8P0b7crARaJZtX1T9g@im.wechat"
	got := sanitizeUserIDForPath(id)
	for _, r := range got {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			t.Fatalf("sanitized path %q contains unsafe character %q", got, r)
		}
	}
	if got2 := sanitizeUserIDForPath(id); got2 != got {
		t.Fatalf("sanitizeUserIDForPath should be deterministic: %q vs %q", got, got2)
	}
	if other := sanitizeUserIDForPath("different-user@im.wechat"); other == got {
		t.Fatalf("different user IDs should not collide: %q", got)
	}
}

func TestDailyQuotaResetsOnNewDay(t *testing.T) {
	h := newTestHandler()
	userID := "reset-test@im.wechat"
	h.SetUserPermission(userID, config.UserPermission{Level: config.PermissionReadOnly, DailyLimit: 1})

	if !h.consumeQuota(userID) {
		t.Fatal("first message today should be allowed")
	}
	if h.consumeQuota(userID) {
		t.Fatal("second message today should be blocked by DailyLimit=1")
	}

	// Simulate the next day by backdating the recorded usage date.
	h.usageMu.Lock()
	h.usage[userID].date = "2000-01-01"
	h.usageMu.Unlock()

	if !h.consumeQuota(userID) {
		t.Fatal("quota should reset once the local date changes")
	}
}

func TestAccessModeDefaultsToPublic(t *testing.T) {
	h := newTestHandler()
	if got := h.AccessMode(); got != config.AccessPublic {
		t.Fatalf("default AccessMode() = %q, want %q", got, config.AccessPublic)
	}
	if !h.checkAccess("anyone@im.wechat") {
		t.Fatal("public mode should allow any non-owner user")
	}
}

func TestAccessModeOwnerOnlyBlocksEveryNonOwner(t *testing.T) {
	h := newTestHandler()
	h.SetAccessMode(config.AccessOwnerOnly)
	if h.checkAccess("stranger@im.wechat") {
		t.Fatal("owner_only mode should refuse every non-owner user")
	}
	// Even a user with an explicit permission entry is still refused: owner_only
	// means owner_only, not "owner + allowlist".
	h.SetUserPermission("configured@im.wechat", config.UserPermission{Level: config.PermissionWorkspaceWrite})
	if h.checkAccess("configured@im.wechat") {
		t.Fatal("owner_only mode should refuse non-owner users regardless of permission entries")
	}
}

func TestAccessModeAllowlistOnlyAllowsConfiguredUsers(t *testing.T) {
	h := newTestHandler()
	h.SetAccessMode(config.AccessAllowlist)
	if h.checkAccess("stranger@im.wechat") {
		t.Fatal("allowlist mode should refuse a user with no permission entry")
	}
	h.SetUserPermission("configured@im.wechat", config.UserPermission{Level: config.PermissionReadOnly})
	if !h.checkAccess("configured@im.wechat") {
		t.Fatal("allowlist mode should allow a user with an existing permission entry")
	}
}

func TestAccessGateRefusesBeforeAgentCallInOwnerOnlyMode(t *testing.T) {
	srv, sent := newFakeIlinkServer(t)
	defer srv.Close()

	h := NewHandler(nil, nil)
	fake := &recordingAgent{}
	h.SetDefaultAgent("codex", fake)
	h.SetAccessMode(config.AccessOwnerOnly)
	// Plain text without a "/" or "@" prefix waits out the consecutive-message
	// merge idle delay before dispatching; use the minimum allowed so this
	// test doesn't sit through the 3s default.
	if err := h.SetMergeSettings(MergeSettings{IdleDelay: time.Second, MaxWait: time.Second, MaxMessages: 10, MaxChars: 4000}); err != nil {
		t.Fatalf("SetMergeSettings: %v", err)
	}

	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	h.HandleMessage(context.Background(), client, newTestMessage("stranger@im.wechat", "hello", 20))

	waitFor(t, 4*time.Second, func() bool { return len(sent.snapshot()) >= 1 })
	if calls := fake.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("owner_only mode should never let a stranger's message reach the agent, got calls=%v", calls)
	}
	found := false
	for _, text := range sent.snapshot() {
		if text == accessDeniedReply() {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected access-denied reply to be sent, got %v", sent.snapshot())
	}
}

func TestCheckAccessBlockedUserRefusedRegardlessOfMode(t *testing.T) {
	for _, mode := range []config.AccessMode{config.AccessPublic, config.AccessAllowlist, config.AccessOwnerOnly} {
		h := newTestHandler()
		h.SetAccessMode(mode)
		h.SetUserPermission("blocked@im.wechat", config.UserPermission{Level: config.PermissionWorkspaceWrite, Blocked: true})
		if h.checkAccess("blocked@im.wechat") {
			t.Fatalf("mode=%q: a blocked user must be refused even with an otherwise-permissive level", mode)
		}
	}
}

func TestCheckAccessLevelSaveDoesNotImplicitlyUnblock(t *testing.T) {
	h := newTestHandler()
	h.SetAccessMode(config.AccessPublic)
	h.SetUserPermission("blocked@im.wechat", config.UserPermission{Level: config.PermissionReadOnly, Blocked: true})
	if h.checkAccess("blocked@im.wechat") {
		t.Fatal("expected user to be blocked after SetUserPermission with Blocked=true")
	}
	// Unblocking (Blocked: false) is what should restore access — this just
	// pins down that checkAccess reads the current Blocked flag each call,
	// not a cached decision.
	h.SetUserPermission("blocked@im.wechat", config.UserPermission{Level: config.PermissionReadOnly, Blocked: false})
	if !h.checkAccess("blocked@im.wechat") {
		t.Fatal("expected user to regain access once Blocked is cleared")
	}
}

func TestAccessGateDoesNotBlockOwnerRegardlessOfMode(t *testing.T) {
	srv, _ := newFakeIlinkServer(t)
	defer srv.Close()

	h := NewHandler(nil, nil)
	fake := &recordingAgent{}
	h.SetDefaultAgent("codex", fake)
	h.SetAccessMode(config.AccessOwnerOnly)
	if err := h.SetMergeSettings(MergeSettings{IdleDelay: time.Second, MaxWait: time.Second, MaxMessages: 10, MaxChars: 4000}); err != nil {
		t.Fatalf("SetMergeSettings: %v", err)
	}

	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	ownerID := config.OwnerUserIDs()[0]
	h.HandleMessage(context.Background(), client, newTestMessage(ownerID, "hello", 21))

	waitFor(t, 4*time.Second, func() bool { return len(fake.callsSnapshot()) == 1 })
}

func TestPersonaOverrideResolutionPriority(t *testing.T) {
	dir := t.TempDir()
	if err := persona.Save(dir, persona.DefaultName, "默认人格"); err != nil {
		t.Fatalf("persona.Save default: %v", err)
	}
	if err := persona.Save(dir, "vip", "VIP人格"); err != nil {
		t.Fatalf("persona.Save vip: %v", err)
	}

	h := newTestHandler()
	h.SetPersonaDir(dir)

	// No binding -> default persona.
	name, override := h.personaOverride("user-a")
	if name != persona.DefaultName || override.SystemPrompt != "默认人格" {
		t.Fatalf("unbound user got (%q, %q), want (%q, \"默认人格\")", name, override.SystemPrompt, persona.DefaultName)
	}

	// Explicit binding -> that persona.
	h.SetUserPersona("user-b", "vip")
	name, override = h.personaOverride("user-b")
	if name != "vip" || override.SystemPrompt != "VIP人格" {
		t.Fatalf("bound user got (%q, %q), want (\"vip\", \"VIP人格\")", name, override.SystemPrompt)
	}

	if !override.SafeMode {
		t.Fatalf("override.SafeMode = %v, want true", override.SafeMode)
	}

	// Configured default persona tier: SetDefaultPersona -> unbound user picks up configured default.
	h.SetDefaultPersona("vip")
	name, override = h.personaOverride("user-c")
	if name != "vip" || override.SystemPrompt != "VIP人格" {
		t.Fatalf("unbound user with configured default got (%q, %q), want (\"vip\", \"VIP人格\")", name, override.SystemPrompt)
	}
}

func TestChatWithAgentInjectsPersonaOverrideForNonOwner(t *testing.T) {
	dir := t.TempDir()
	if err := persona.Save(dir, persona.DefaultName, "脱敏人格文本"); err != nil {
		t.Fatalf("persona.Save: %v", err)
	}

	h := newTestHandler()
	h.SetPersonaDir(dir)
	fake := &recordingAgent{}

	if _, _, err := h.chatWithAgent(context.Background(), fake, "non-owner-test@im.wechat", "hi"); err != nil {
		t.Fatalf("chatWithAgent: %v", err)
	}

	override, ok := fake.personaOverrideFor("non-owner-test@im.wechat")
	if !ok {
		t.Fatal("expected a persona override to be recorded")
	}
	if override.SystemPrompt != "脱敏人格文本" {
		t.Fatalf("override.SystemPrompt = %q, want %q", override.SystemPrompt, "脱敏人格文本")
	}
}

func TestChatWithAgentSkipsPersonaOverrideForOwner(t *testing.T) {
	dir := t.TempDir()
	h := newTestHandler()
	h.SetPersonaDir(dir)
	fake := &recordingAgent{}

	ownerID := config.OwnerUserIDs()[0]
	if _, _, err := h.chatWithAgent(context.Background(), fake, ownerID, "hi"); err != nil {
		t.Fatalf("chatWithAgent: %v", err)
	}

	if _, ok := fake.personaOverrideFor(ownerID); ok {
		t.Fatal("owner conversations must not get a persona override")
	}
}

func TestNonOwnerCodexCommandResolvesToCodexSharedNotRealCodex(t *testing.T) {
	srv, _ := newFakeIlinkServer(t)
	defer srv.Close()

	h := NewHandler(nil, nil)
	h.SetAllowNonOwnerAgentSwitch(true)
	fakeShared := &recordingAgent{}
	fakeReal := &recordingAgent{}
	h.SetDefaultAgent("codex-shared", fakeShared) // registers under h.agents["codex-shared"]
	h.agents["codex"] = fakeReal                  // simulate the real owner-only codex agent also being registered

	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	msg := newTestMessage("non-owner-test@im.wechat", "/codex 你好", 1)
	h.HandleMessage(context.Background(), client, msg)

	waitFor(t, 2*time.Second, func() bool { return len(fakeShared.callsSnapshot()) > 0 || len(fakeReal.callsSnapshot()) > 0 })
	if calls := fakeReal.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("non-owner /codex must never reach the real codex agent, got calls=%v", calls)
	}
	if calls := fakeShared.callsSnapshot(); len(calls) != 1 || calls[0] != "你好" {
		t.Fatalf("non-owner /codex should reach codex-shared with the message, got %v", calls)
	}
}

func TestNonOwnerAgentSwitchDisabledByDefaultTreatsSlashCodexAsPlainText(t *testing.T) {
	srv, _ := newFakeIlinkServer(t)
	defer srv.Close()

	h := NewHandler(nil, nil)
	// AllowNonOwnerAgentSwitch left at its default (false).
	fake := &recordingAgent{}
	h.SetDefaultAgent("codex-shared", fake)

	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	msg := newTestMessage("non-owner-test@im.wechat", "/codex 你好", 1)
	h.HandleMessage(context.Background(), client, msg)

	waitFor(t, 2*time.Second, func() bool { return len(fake.callsSnapshot()) > 0 })
	calls := fake.callsSnapshot()
	if len(calls) != 1 || calls[0] != "/codex 你好" {
		t.Fatalf("with switching disabled, /codex should reach the default agent verbatim as plain text, got %v", calls)
	}
}

func TestOwnerCodexCommandStillResolvesToRealCodex(t *testing.T) {
	srv, _ := newFakeIlinkServer(t)
	defer srv.Close()

	h := NewHandler(nil, nil)
	h.SetAllowNonOwnerAgentSwitch(true)
	fakeReal := &recordingAgent{}
	h.SetDefaultAgent("codex", fakeReal)

	client := ilink.NewClient(&ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	ownerID := config.OwnerUserIDs()[0]
	msg := newTestMessage(ownerID, "/codex 你好", 1)
	h.HandleMessage(context.Background(), client, msg)

	waitFor(t, 2*time.Second, func() bool { return len(fakeReal.callsSnapshot()) > 0 })
	if calls := fakeReal.callsSnapshot(); len(calls) != 1 || calls[0] != "你好" {
		t.Fatalf("owner /codex should still reach the real codex agent, got %v", calls)
	}
}
