package messaging

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/google/uuid"
)

// AgentFactory creates an agent by config name. Returns nil if the name is unknown.
type AgentFactory func(ctx context.Context, name string) agent.Agent

// SaveUserAgentFunc persists one user's selected agent to the config file.
type SaveUserAgentFunc func(userID, name string) error

// AgentMeta holds static config info about an agent (for /status display).
type AgentMeta struct {
	Name    string
	Type    string // "acp", "cli", "http"
	Command string // binary path or endpoint
	Model   string
}

// Handler processes incoming WeChat messages and dispatches replies.
type Handler struct {
	mu            sync.RWMutex
	defaultName   string
	userAgents    map[string]string      // user ID -> selected agent; falls back to defaultName
	agents        map[string]agent.Agent // name -> running agent
	agentMetas    []AgentMeta            // all configured agents (for /status)
	agentWorkDirs map[string]string      // agent name -> configured/runtime cwd
	customAliases map[string]string      // custom alias -> agent name (from config)
	factory       AgentFactory
	saveUserAgent SaveUserAgentFunc
	contextTokens sync.Map // map[userID]contextToken
	saveDir       string   // directory to save images/files to
	seenMsgs      sync.Map // map[int64]time.Time — dedup by message_id
	pendingMedia  sync.Map // map[userID][]pendingMedia — inbound files/videos waiting for a text instruction
	activeChats   sync.Map // map[userID]time.Time — prevent overlapping turns across agents for one user
	chatQueues    sync.Map // map[botID+userID]*chatQueue — serializes one conversation's turns
	mergeMu       sync.RWMutex
	mergeSettings MergeSettings
}

type pendingMedia struct {
	Kind string
	Name string
	Path string
}

const maxQueuedMessages = 20

// MergeSettings controls how consecutive plain-text messages become one turn.
type MergeSettings struct {
	IdleDelay   time.Duration
	MaxWait     time.Duration
	MaxMessages int
	MaxChars    int
}

// DefaultMergeSettings keeps normal typing pauses together without delaying a request forever.
func DefaultMergeSettings() MergeSettings {
	return MergeSettings{IdleDelay: 3 * time.Second, MaxWait: 10 * time.Second, MaxMessages: 10, MaxChars: 4000}
}

type chatQueue struct {
	mu               sync.Mutex
	jobs             []chatJob
	wake             chan struct{}
	running          bool
	overloadNotified bool
}

type chatJob struct {
	ctx       context.Context
	client    *ilink.Client
	msg       ilink.WeixinMessage
	text      string
	mergeable bool
	run       func()
}

// NewHandler creates a new message handler.
func NewHandler(factory AgentFactory, saveUserAgent SaveUserAgentFunc) *Handler {
	return &Handler{
		agents:        make(map[string]agent.Agent),
		userAgents:    make(map[string]string),
		agentWorkDirs: make(map[string]string),
		factory:       factory,
		saveUserAgent: saveUserAgent,
		mergeSettings: DefaultMergeSettings(),
	}
}

// SetMergeSettings updates consecutive-message merging for new queued turns.
func (h *Handler) SetMergeSettings(settings MergeSettings) error {
	if settings.IdleDelay < time.Second || settings.IdleDelay > 30*time.Second {
		return fmt.Errorf("idle delay must be between 1 and 30 seconds")
	}
	if settings.MaxWait < settings.IdleDelay || settings.MaxWait > time.Minute {
		return fmt.Errorf("max wait must be between idle delay and 60 seconds")
	}
	if settings.MaxMessages < 1 || settings.MaxMessages > maxQueuedMessages {
		return fmt.Errorf("max messages must be between 1 and %d", maxQueuedMessages)
	}
	if settings.MaxChars < 100 || settings.MaxChars > 12000 {
		return fmt.Errorf("max chars must be between 100 and 12000")
	}
	h.mergeMu.Lock()
	h.mergeSettings = settings
	h.mergeMu.Unlock()
	return nil
}

// MergeSettings returns a snapshot safe for queue workers.
func (h *Handler) MergeSettings() MergeSettings {
	h.mergeMu.RLock()
	defer h.mergeMu.RUnlock()
	return h.mergeSettings
}

// SetSaveDir sets the directory for saving images and files.
func (h *Handler) SetSaveDir(dir string) {
	h.saveDir = dir
}

func defaultInboundImageDir() string {
	return filepath.Join(defaultAttachmentWorkspace(), "inbound-images")
}

func defaultInboundFileDir() string {
	return filepath.Join(defaultAttachmentWorkspace(), "inbound-files")
}

func defaultInboundVideoDir() string {
	return filepath.Join(defaultAttachmentWorkspace(), "inbound-videos")
}

func defaultInboundMediaDir(kind string) string {
	if kind == "video" {
		return defaultInboundVideoDir()
	}
	return defaultInboundFileDir()
}

// cleanSeenMsgs removes entries older than 5 minutes from the dedup cache.
func (h *Handler) cleanSeenMsgs() {
	cutoff := time.Now().Add(-5 * time.Minute)
	h.seenMsgs.Range(func(key, value any) bool {
		if t, ok := value.(time.Time); ok && t.Before(cutoff) {
			h.seenMsgs.Delete(key)
		}
		return true
	})
}

// SetCustomAliases sets custom alias mappings from config.
func (h *Handler) SetCustomAliases(aliases map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.customAliases = aliases
}

// SetAgentMetas sets the list of all configured agents (for /status).
func (h *Handler) SetAgentMetas(metas []AgentMeta) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agentMetas = metas
}

// SetAgentWorkDirs sets the configured working directory for each agent.
func (h *Handler) SetAgentWorkDirs(workDirs map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.agentWorkDirs = make(map[string]string, len(workDirs))
	for name, dir := range workDirs {
		h.agentWorkDirs[name] = dir
	}
}

// SetDefaultAgent sets the default agent (already started).
func (h *Handler) SetDefaultAgent(name string, ag agent.Agent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.defaultName = name
	h.agents[name] = ag
	log.Printf("[handler] default agent ready: %s (%s)", name, ag.Info())
}

// SetUserAgents restores persisted per-user agent selections.
func (h *Handler) SetUserAgents(selections map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.userAgents = make(map[string]string, len(selections))
	for userID, name := range selections {
		if userID != "" && name != "" {
			h.userAgents[userID] = name
		}
	}
}

// getAgent returns a running agent by name, or starts it on demand via factory.
func (h *Handler) getAgent(ctx context.Context, name string) (agent.Agent, error) {
	// Fast path: already running
	h.mu.RLock()
	ag, ok := h.agents[name]
	h.mu.RUnlock()
	if ok {
		return ag, nil
	}

	// Slow path: create on demand
	if h.factory == nil {
		return nil, fmt.Errorf("agent %q not found and no factory configured", name)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Double-check after acquiring write lock
	if ag, ok := h.agents[name]; ok {
		return ag, nil
	}

	log.Printf("[handler] starting agent %q on demand...", name)
	ag = h.factory(ctx, name)
	if ag == nil {
		return nil, fmt.Errorf("agent %q not available", name)
	}

	h.agents[name] = ag
	log.Printf("[handler] agent started on demand: %s (%s)", name, ag.Info())
	return ag, nil
}

// selectedAgent returns the agent selected by this user, falling back to the global default.
func (h *Handler) selectedAgent(userID string) (string, agent.Agent, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	name, selected := h.userAgents[userID]
	if !selected {
		name = h.defaultName
	}
	return name, h.agents[name], selected
}

// isKnownAgent checks if a name corresponds to a configured agent.
func (h *Handler) isKnownAgent(name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	// Check running agents
	if _, ok := h.agents[name]; ok {
		return true
	}
	// Check configured agents (metas)
	for _, meta := range h.agentMetas {
		if meta.Name == name {
			return true
		}
	}
	return false
}

// agentAliases maps short aliases to agent config names.
var agentAliases = map[string]string{
	"cc":  "claude",
	"cx":  "codex",
	"oc":  "openclaw",
	"cs":  "cursor",
	"km":  "kimi",
	"gm":  "gemini",
	"ocd": "opencode",
	"pi":  "pi",
	"cp":  "copilot",
	"dr":  "droid",
	"if":  "iflow",
	"kr":  "kiro",
	"qw":  "qwen",
}

// resolveAlias returns the full agent name for an alias, or the original name if no alias matches.
// Checks custom aliases (from config) first, then built-in aliases.
func (h *Handler) resolveAlias(name string) string {
	h.mu.RLock()
	custom := h.customAliases
	h.mu.RUnlock()
	if custom != nil {
		if full, ok := custom[name]; ok {
			return full
		}
	}
	if full, ok := agentAliases[name]; ok {
		return full
	}
	return name
}

// parseCommand checks if text starts with "/" or "@" followed by agent name(s).
// Supports multiple agents: "@cc @cx hello" returns (["claude","codex"], "hello").
// Returns (agentNames, actualMessage). Aliases are resolved automatically.
// If no command prefix, returns (nil, originalText).
func (h *Handler) parseCommand(text string) ([]string, string) {
	if !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "@") {
		return nil, text
	}

	// Parse consecutive @name or /name tokens from the start
	var names []string
	rest := text
	for {
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, "@") {
			break
		}

		// Strip prefix
		after := rest[1:]
		idx := strings.IndexAny(after, " /@")
		var token string
		if idx < 0 {
			// Rest is just the name, no message
			token = after
			rest = ""
		} else if after[idx] == '/' || after[idx] == '@' {
			// Next token is another @name or /name
			token = after[:idx]
			rest = after[idx:]
		} else {
			// Space — name ends here
			token = after[:idx]
			rest = strings.TrimSpace(after[idx+1:])
		}

		if token != "" {
			names = append(names, h.resolveAlias(token))
		}

		if rest == "" {
			break
		}
	}

	// Deduplicate names preserving order
	seen := make(map[string]bool)
	unique := names[:0]
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			unique = append(unique, n)
		}
	}

	return unique, rest
}

// HandleMessage queues a message so one Bot/user conversation is processed in order.
func (h *Handler) HandleMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) {
	// Only process user messages that are finished
	if msg.MessageType != ilink.MessageTypeUser {
		return
	}
	if msg.MessageState != ilink.MessageStateFinish {
		return
	}

	// Deduplicate by message_id to avoid processing the same message multiple times
	// (voice messages may trigger multiple finish-state updates)
	if msg.MessageID != 0 {
		if _, loaded := h.seenMsgs.LoadOrStore(msg.MessageID, time.Now()); loaded {
			return
		}
		// Clean up old entries periodically (fire-and-forget)
		go h.cleanSeenMsgs()
	}
	botID := client.BotID()
	logMessageMeta(botID, msg)
	text := extractText(msg)
	directText := text != ""
	if text == "" {
		if voiceText := extractVoiceText(msg); voiceText != "" {
			text = voiceText
			log.Printf("[handler] bot=%s voice transcription from %s: %q", botID, msg.FromUserID, truncate(text, 80))
		}
	}
	if text != "" {
		log.Printf("[handler] bot=%s received from %s: %q", botID, msg.FromUserID, truncate(text, 80))
	}
	key := chatKey(client, msg.FromUserID)
	queued, notifyOverload := h.enqueueChat(key, chatJob{ctx: ctx, client: client, msg: msg, text: text, mergeable: directText && h.isMergeableText(text)})
	if !queued && notifyOverload {
		reply := "消息较多，我会按顺序处理；请稍后再发新的需求。"
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, NewClientID()); err != nil {
			log.Printf("[handler] failed to send queue notice to %s: %v", msg.FromUserID, err)
		}
	}
}

func (h *Handler) enqueueChat(key string, job chatJob) (queued, notifyOverload bool) {
	value, _ := h.chatQueues.LoadOrStore(key, &chatQueue{wake: make(chan struct{}, 1)})
	queue := value.(*chatQueue)
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.jobs) >= maxQueuedMessages {
		notifyOverload = !queue.overloadNotified
		queue.overloadNotified = true
		return false, notifyOverload
	}
	queue.jobs = append(queue.jobs, job)
	select {
	case queue.wake <- struct{}{}:
	default:
	}
	if queue.running {
		return true, false
	}
	queue.running = true
	go h.runChatQueue(queue)
	return true, false
}

func (h *Handler) runChatQueue(queue *chatQueue) {
	for {
		queue.mu.Lock()
		if len(queue.jobs) == 0 {
			queue.running = false
			queue.mu.Unlock()
			return
		}
		job := queue.jobs[0]
		queue.jobs = queue.jobs[1:]
		if len(queue.jobs) < maxQueuedMessages {
			queue.overloadNotified = false
		}
		queue.mu.Unlock()
		if job.run != nil {
			job.run()
			continue
		}
		if job.mergeable {
			job = h.mergeQueuedText(queue, job)
		}
		h.handleMessage(job.ctx, job.client, job.msg)
	}
}

func (h *Handler) isMergeableText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "@") {
		return false
	}
	return h.saveDir == "" || !IsURL(trimmed)
}

func (h *Handler) mergeQueuedText(queue *chatQueue, first chatJob) chatJob {
	settings := h.MergeSettings()
	texts := []string{first.text}
	chars := len(first.text)
	latest := first
	idle := time.NewTimer(settings.IdleDelay)
	deadline := time.NewTimer(settings.MaxWait)
	defer idle.Stop()
	defer deadline.Stop()
	for {
		select {
		case <-queue.wake:
			queue.mu.Lock()
			merged := false
			for len(queue.jobs) > 0 && len(texts) < settings.MaxMessages {
				next := queue.jobs[0]
				if !next.mergeable || chars+1+len(next.text) > settings.MaxChars {
					break
				}
				queue.jobs = queue.jobs[1:]
				texts = append(texts, next.text)
				chars += 1 + len(next.text)
				latest = next
				merged = true
			}
			queue.mu.Unlock()
			if len(texts) >= settings.MaxMessages || chars >= settings.MaxChars {
				return mergedJob(first, latest, texts)
			}
			if merged {
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(settings.IdleDelay)
			}
		case <-idle.C:
			return mergedJob(first, latest, texts)
		case <-deadline.C:
			return mergedJob(first, latest, texts)
		}
	}
}

func mergedJob(first, latest chatJob, texts []string) chatJob {
	if len(texts) == 1 {
		return first
	}
	merged := first
	merged.msg.ContextToken = latest.msg.ContextToken
	merged.msg.ItemList = []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: strings.Join(texts, "\n")}}}
	merged.text = strings.Join(texts, "\n")
	log.Printf("[handler] merged %d consecutive messages from %s", len(texts), first.msg.FromUserID)
	return merged
}

func chatKey(client *ilink.Client, userID string) string {
	return client.BotID() + "\x00" + userID
}

// handleMessage processes one already queued incoming message.
func (h *Handler) handleMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) {
	// Extract text from item list (text message or voice transcription)
	text := extractText(msg)
	if text == "" {
		if voiceText := extractVoiceText(msg); voiceText != "" {
			text = voiceText
		}
	}
	if text == "" {
		// Check for image message — forward to agent for processing.
		if img := extractImage(msg); img != nil {
			h.handleImageMessage(ctx, client, msg, img)
			return
		}
		if video := extractVideo(msg); video != nil {
			h.handleStoredMediaMessage(ctx, client, msg, "video", "", video.Media)
			return
		}
		if file := extractFile(msg); file != nil {
			h.handleStoredMediaMessage(ctx, client, msg, "file", file.FileName, file.Media)
			return
		}
		log.Printf("[handler] bot=%s received non-text message from %s, skipping", client.BotID(), msg.FromUserID)
		return
	}

	// Store context token for this user
	h.contextTokens.Store(msg.FromUserID, msg.ContextToken)

	// Generate a clientID for this reply (used to correlate typing → finish)
	clientID := NewClientID()

	// Intercept URLs: save to Linkhoard directly without AI agent
	trimmed := strings.TrimSpace(text)
	if h.saveDir != "" && IsURL(trimmed) {
		rawURL := ExtractURL(trimmed)
		if rawURL != "" {
			log.Printf("[handler] saving URL to linkhoard: %s", rawURL)
			title, err := SaveLinkToLinkhoard(ctx, h.saveDir, rawURL)
			var reply string
			if err != nil {
				log.Printf("[handler] link save failed: %v", err)
				reply = fmt.Sprintf("保存失败: %v", err)
			} else {
				reply = fmt.Sprintf("已保存: %s", title)
			}
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			}
			return
		}
	}

	// Built-in commands (no typing needed)
	if trimmed == "/info" {
		reply := h.buildStatus(msg.FromUserID)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	} else if trimmed == "/help" {
		reply := buildHelpText()
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	} else if trimmed == "/new" || trimmed == "/clear" {
		reply := h.resetDefaultSession(ctx, msg.FromUserID)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	} else if strings.HasPrefix(trimmed, "/cwd") {
		reply := h.handleCwd(trimmed, msg.FromUserID)
		if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
		}
		return
	}

	// Route: "/agentname message" or "@agent1 @agent2 message" -> specific agent(s)
	agentNames, message := h.parseCommand(text)

	// No command prefix -> send to default agent
	if len(agentNames) == 0 {
		h.sendToDefaultAgent(ctx, client, msg, text, clientID)
		return
	}

	// No message -> switch default agent (only first name)
	if message == "" {
		if len(agentNames) == 1 && h.isKnownAgent(agentNames[0]) {
			wait, ok := h.tryBeginChat(chatKey(client, msg.FromUserID))
			if !ok {
				reply := agentBusyReply(wait)
				if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
					log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
				}
				return
			}
			reply := h.switchUserAgent(ctx, msg.FromUserID, agentNames[0])
			h.endChat(chatKey(client, msg.FromUserID))
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			}
		} else if len(agentNames) == 1 && !h.isKnownAgent(agentNames[0]) {
			// Unknown agent -> forward to default
			h.sendToDefaultAgent(ctx, client, msg, text, clientID)
		} else {
			reply := "Usage: specify one agent to switch, or add a message to broadcast"
			if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
				log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
			}
		}
		return
	}

	// Filter to known agents; if single unknown agent -> forward to default
	var knownNames []string
	for _, name := range agentNames {
		if h.isKnownAgent(name) {
			knownNames = append(knownNames, name)
		}
	}
	if len(knownNames) == 0 {
		// No known agents -> forward entire text to default agent
		h.sendToDefaultAgent(ctx, client, msg, text, clientID)
		return
	}

	// Send typing indicator
	go func() {
		if typingErr := SendTypingState(ctx, client, msg.FromUserID, msg.ContextToken); typingErr != nil {
			log.Printf("[handler] failed to send typing state: %v", typingErr)
		}
	}()

	if len(knownNames) == 1 {
		// Single agent
		h.sendToNamedAgent(ctx, client, msg, knownNames[0], message, clientID)
	} else {
		// Multi-agent broadcast: parallel dispatch, send replies as they arrive
		h.broadcastToAgents(ctx, client, msg, knownNames, message)
	}
}

// sendToDefaultAgent sends the message to the default agent and replies.
func (h *Handler) sendToDefaultAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text, clientID string) {
	go func() {
		if typingErr := SendTypingState(ctx, client, msg.FromUserID, msg.ContextToken); typingErr != nil {
			log.Printf("[handler] failed to send typing state: %v", typingErr)
		}
	}()

	agentName, ag, selected := h.selectedAgent(msg.FromUserID)

	wait, ok := h.tryBeginChat(chatKey(client, msg.FromUserID))
	if !ok {
		log.Printf("[handler] selected agent %q is busy for %s, sending busy notice", agentName, msg.FromUserID)
		SendTextReply(ctx, client, msg.FromUserID, agentBusyReply(wait), msg.ContextToken, clientID)
		return
	}
	defer h.endChat(chatKey(client, msg.FromUserID))

	if shouldRedirectClaudeImageGeneration(agentName, text) {
		SendTextReply(ctx, client, msg.FromUserID, claudeImageGenerationReply(), msg.ContextToken, clientID)
		return
	}

	if ag == nil && selected {
		var err error
		ag, err = h.getAgent(ctx, agentName)
		if err != nil {
			log.Printf("[handler] selected agent %q not available: %v", agentName, err)
			SendTextReply(ctx, client, msg.FromUserID, agentFailureReply(agentName, err), msg.ContextToken, clientID)
			return
		}
	}

	text = h.withPendingMediaContext(msg.FromUserID, text)
	var reply string
	if ag != nil {
		var err error
		reply, err = h.chatWithAgent(ctx, ag, msg.FromUserID, text)
		if err != nil {
			reply = agentFailureReply(agentName, err)
		}
	} else {
		log.Printf("[handler] agent not ready, sending recovery notice to %s", msg.FromUserID)
		reply = agentRecoveringReply()
	}

	h.sendReplyWithMedia(ctx, client, msg, agentName, reply, clientID)
}

func agentRecoveringReply() string {
	return "系统正在恢复中，请稍后重试。"
}

func agentBusyReply(wait time.Duration) string {
	if wait < 0 {
		wait = 0
	}
	seconds := int(wait.Round(time.Second).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf("上一条消息还在处理中，已经等了 %d 秒。图片生成和长任务会慢一些，请等当前回复完成后再发新需求。", seconds)
}

func agentFailureReply(agentName string, err error) string {
	name := strings.TrimSpace(agentName)
	if name == "" {
		name = "当前模型"
	}
	detail := strings.ToLower(err.Error())
	switch {
	case strings.Contains(detail, "session limit"), strings.Contains(detail, "usage limit"), strings.Contains(detail, "rate limit"):
		if strings.EqualFold(name, "claude") {
			return "Claude 当前额度或频率已达限制，请稍后重试，或发送 /codex 切换到 Codex。"
		}
		return fmt.Sprintf("%s 当前额度或频率已达限制，请稍后重试。", name)
	case strings.Contains(detail, "auth"), strings.Contains(detail, "login"), strings.Contains(detail, "oauth"):
		return fmt.Sprintf("%s 登录状态异常，请在电脑端重新登录后再试。", name)
	case strings.Contains(detail, "no such file"), strings.Contains(detail, "executable file not found"),
		strings.Contains(detail, "start "+strings.ToLower(name)+":"):
		return fmt.Sprintf("%s 本机进程启动失败，系统会在下一条消息时重新尝试。", name)
	default:
		return fmt.Sprintf("%s 本次处理失败，系统会在下一条消息时重新尝试。", name)
	}
}

func shouldRedirectClaudeImageGeneration(agentName, text string) bool {
	if !strings.EqualFold(strings.TrimSpace(agentName), "claude") {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(text))
	exclusions := []string{
		"分析图片", "识别图片", "描述图片", "图片是怎么生成", "如何生成图片",
	}
	for _, phrase := range exclusions {
		if strings.Contains(normalized, phrase) {
			return false
		}
	}
	if strings.Contains(normalized, "生图") {
		return true
	}
	imageNouns := []string{"图", "照片", "海报", "插画", "头像", "壁纸"}
	for _, noun := range imageNouns {
		if strings.Contains(normalized, noun) &&
			(strings.Contains(normalized, "生成") || strings.Contains(normalized, "做一")) {
			return true
		}
	}
	drawPhrases := []string{"帮我画", "给我画", "画一张", "画一幅", "画个"}
	for _, phrase := range drawPhrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	hasEnglishVerb := strings.Contains(normalized, "generate") ||
		strings.Contains(normalized, "create") ||
		strings.Contains(normalized, "draw")
	hasEnglishImage := strings.Contains(normalized, "image") ||
		strings.Contains(normalized, "picture") ||
		strings.Contains(normalized, "photo") ||
		strings.Contains(normalized, "illustration")
	return hasEnglishVerb && hasEnglishImage
}

func claudeImageGenerationReply() string {
	return "Claude 当前不支持生成图片。请发送 /codex 切换到 Codex，然后重新发送生图需求。"
}

func (h *Handler) tryBeginChat(userID string) (time.Duration, bool) {
	now := time.Now()
	value, loaded := h.activeChats.LoadOrStore(userID, now)
	if !loaded {
		return 0, true
	}
	started, ok := value.(time.Time)
	if !ok {
		return 0, false
	}
	return now.Sub(started), false
}

func (h *Handler) endChat(userID string) {
	h.activeChats.Delete(userID)
}

// sendToNamedAgent sends the message to a specific agent and replies.
func (h *Handler) sendToNamedAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, name, message, clientID string) {
	wait, ok := h.tryBeginChat(chatKey(client, msg.FromUserID))
	if !ok {
		log.Printf("[handler] agent %q is busy for %s, sending busy notice", name, msg.FromUserID)
		SendTextReply(ctx, client, msg.FromUserID, agentBusyReply(wait), msg.ContextToken, clientID)
		return
	}
	defer h.endChat(chatKey(client, msg.FromUserID))

	if shouldRedirectClaudeImageGeneration(name, message) {
		SendTextReply(ctx, client, msg.FromUserID, claudeImageGenerationReply(), msg.ContextToken, clientID)
		return
	}

	message = h.withPendingMediaContext(msg.FromUserID, message)
	ag, agErr := h.getAgent(ctx, name)
	if agErr != nil {
		log.Printf("[handler] agent %q not available: %v", name, agErr)
		reply := agentFailureReply(name, agErr)
		SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}

	reply, err := h.chatWithAgent(ctx, ag, msg.FromUserID, message)
	if err != nil {
		reply = agentFailureReply(name, err)
	}
	h.sendReplyWithMedia(ctx, client, msg, name, reply, clientID)
}

// broadcastToAgents sends the message to multiple agents in parallel.
// Each reply is sent as a separate message with the agent name prefix.
func (h *Handler) broadcastToAgents(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, names []string, message string) {
	wait, ok := h.tryBeginChat(chatKey(client, msg.FromUserID))
	if !ok {
		log.Printf("[handler] broadcast is busy for %s, sending busy notice", msg.FromUserID)
		SendTextReply(ctx, client, msg.FromUserID, agentBusyReply(wait), msg.ContextToken, NewClientID())
		return
	}
	defer h.endChat(chatKey(client, msg.FromUserID))

	message = h.withPendingMediaContext(msg.FromUserID, message)
	type result struct {
		name  string
		reply string
	}

	ch := make(chan result, len(names))

	for _, name := range names {
		go func(n string) {
			ag, err := h.getAgent(ctx, n)
			if err != nil {
				ch <- result{name: n, reply: agentFailureReply(n, err)}
				return
			}
			reply, err := h.chatWithAgent(ctx, ag, msg.FromUserID, message)
			if err != nil {
				ch <- result{name: n, reply: agentFailureReply(n, err)}
				return
			}
			ch <- result{name: n, reply: reply}
		}(name)
	}

	// Send replies as they arrive
	for range names {
		r := <-ch
		reply := fmt.Sprintf("[%s] %s", r.name, r.reply)
		clientID := NewClientID()
		h.sendReplyWithMedia(ctx, client, msg, r.name, reply, clientID)
	}
}

// sendReplyWithMedia sends a text reply and any extracted image URLs.
func (h *Handler) sendReplyWithMedia(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, agentName, reply, clientID string) {
	imageURLs := ExtractImageURLs(reply)
	attachmentPaths := extractLocalAttachmentPaths(reply)
	allowedRoots := h.allowedAttachmentRoots(agentName)

	var sentPaths []string
	var failedPaths []string
	for _, attachmentPath := range attachmentPaths {
		if !isAllowedAttachmentPath(attachmentPath, allowedRoots) {
			log.Printf("[handler] rejected attachment outside allowed roots for agent %q: %s", agentName, attachmentPath)
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		if err := SendMediaFromPath(ctx, client, msg.FromUserID, attachmentPath, msg.ContextToken); err != nil {
			log.Printf("[handler] failed to send attachment to %s: %v", msg.FromUserID, err)
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		sentPaths = append(sentPaths, attachmentPath)
	}

	reply = rewriteReplyWithAttachmentResults(reply, sentPaths, failedPaths)

	if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
		log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
	}

	for _, imgURL := range imageURLs {
		if err := SendMediaFromURL(ctx, client, msg.FromUserID, imgURL, msg.ContextToken); err != nil {
			log.Printf("[handler] failed to send image to %s: %v", msg.FromUserID, err)
		}
	}
}

func (h *Handler) allowedAttachmentRoots(agentName string) []string {
	roots := []string{
		defaultAttachmentWorkspace(),
		defaultCodexGeneratedImagesRoot(),
	}

	h.mu.RLock()
	agentDir := h.agentWorkDirs[agentName]
	h.mu.RUnlock()

	if agentDir != "" {
		roots = append(roots, agentDir)
	}

	return roots
}

// chatWithAgent sends a message to an agent and returns the reply, with logging.
func (h *Handler) chatWithAgent(ctx context.Context, ag agent.Agent, userID, message string) (string, error) {
	info := ag.Info()
	log.Printf("[handler] dispatching to agent (%s) for %s", info, userID)

	start := time.Now()
	reply, err := ag.Chat(ctx, userID, message)
	elapsed := time.Since(start)

	if err != nil {
		log.Printf("[handler] agent error (%s, elapsed=%s): %v", info, elapsed, err)
		return "", err
	}

	log.Printf("[handler] agent replied (%s, elapsed=%s): %q", info, elapsed, truncate(reply, 100))
	return reply, nil
}

// switchUserAgent selects an agent for one user and persists that selection.
func (h *Handler) switchUserAgent(ctx context.Context, userID, name string) string {
	ag, err := h.getAgent(ctx, name)
	if err != nil {
		log.Printf("[handler] failed to switch %s to %q: %v", userID, name, err)
		return agentFailureReply(name, err)
	}

	h.mu.Lock()
	old := h.defaultName
	if selected, ok := h.userAgents[userID]; ok {
		old = selected
	}
	h.userAgents[userID] = name
	h.agents[name] = ag
	h.mu.Unlock()

	if h.saveUserAgent != nil {
		if err := h.saveUserAgent(userID, name); err != nil {
			log.Printf("[handler] failed to save agent %q for %s: %v", name, userID, err)
		} else {
			log.Printf("[handler] saved agent %q for %s", name, userID)
		}
	}

	info := ag.Info()
	log.Printf("[handler] switched agent for %s: %s -> %s (%s)", userID, old, name, info)
	return fmt.Sprintf("已切换到 %s，仅对当前微信用户生效。", name)
}

// resetDefaultSession resets the session for the agent selected by this user.
func (h *Handler) resetDefaultSession(ctx context.Context, userID string) string {
	name, ag, selected := h.selectedAgent(userID)
	if ag == nil && selected {
		var err error
		ag, err = h.getAgent(ctx, name)
		if err != nil {
			return agentFailureReply(name, err)
		}
	}
	if ag == nil {
		return "No agent running."
	}
	displayName := ag.Info().Name
	sessionID, err := ag.ResetSession(ctx, userID)
	if err != nil {
		log.Printf("[handler] reset session failed for %s: %v", userID, err)
		return agentFailureReply(name, err)
	}
	if sessionID != "" {
		return fmt.Sprintf("已创建新的%s会话\n%s", displayName, sessionID)
	}
	return fmt.Sprintf("已创建新的%s会话", displayName)
}

// handleCwd handles the /cwd command. It updates the working directory for all running agents.
func (h *Handler) handleCwd(trimmed, userID string) string {
	arg := strings.TrimSpace(strings.TrimPrefix(trimmed, "/cwd"))
	if arg == "" {
		// No path provided — show cwd info for this user's selected agent.
		name, ag, _ := h.selectedAgent(userID)
		if ag == nil {
			return fmt.Sprintf("agent: %s (not started)", name)
		}
		info := ag.Info()
		return fmt.Sprintf("cwd: (check agent config)\nagent: %s", info.Name)
	}

	// Expand ~ to home directory
	if arg == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			arg = home
		}
	} else if strings.HasPrefix(arg, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			arg = filepath.Join(home, arg[2:])
		}
	}

	// Resolve to absolute path
	absPath, err := filepath.Abs(arg)
	if err != nil {
		return fmt.Sprintf("Invalid path: %v", err)
	}

	// Verify directory exists
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Sprintf("Path not found: %s", absPath)
	}
	if !info.IsDir() {
		return fmt.Sprintf("Not a directory: %s", absPath)
	}

	// Update cwd on all running agents
	h.mu.RLock()
	agents := make(map[string]agent.Agent, len(h.agents))
	for name, ag := range h.agents {
		agents[name] = ag
	}
	h.mu.RUnlock()

	for name, ag := range agents {
		ag.SetCwd(absPath)
		log.Printf("[handler] updated cwd for agent %s: %s", name, absPath)
	}

	h.mu.Lock()
	for name := range agents {
		h.agentWorkDirs[name] = absPath
	}
	h.mu.Unlock()

	return fmt.Sprintf("cwd: %s", absPath)
}

// buildStatus returns a short status string showing this user's selected agent.
func (h *Handler) buildStatus(userID string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	name := h.defaultName
	if selected, ok := h.userAgents[userID]; ok {
		name = selected
	}
	if name == "" {
		return "agent: none (echo mode)"
	}

	ag, ok := h.agents[name]
	if !ok {
		return fmt.Sprintf("agent: %s (not started)", name)
	}

	info := ag.Info()
	return fmt.Sprintf("agent: %s\ntype: %s\nmodel: %s", name, info.Type, info.Model)
}

func buildHelpText() string {
	return `Available commands:
@agent or /agent - Switch agent for the current WeChat user
@agent msg or /agent msg - Send to a specific agent
@a @b msg - Broadcast to multiple agents
/new or /clear - Start a new session
/cwd /path - Switch workspace directory
/info - Show current agent info
/help - Show this help message

Aliases: /cc(claude) /cx(codex) /cs(cursor) /km(kimi) /gm(gemini) /oc(openclaw) /ocd(opencode) /pi(pi) /cp(copilot) /dr(droid) /if(iflow) /kr(kiro) /qw(qwen)`
}

func extractText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeText && item.TextItem != nil {
			return item.TextItem.Text
		}
	}
	return ""
}

func logMessageMeta(botID string, msg ilink.WeixinMessage) {
	var itemTypes []string
	var fileName string
	var fileLen string
	var voicePlaytime int
	for _, item := range msg.ItemList {
		itemTypes = append(itemTypes, itemTypeName(item.Type))
		if item.Type == ilink.ItemTypeFile && item.FileItem != nil {
			fileName = item.FileItem.FileName
			fileLen = item.FileItem.Len
		}
		if item.Type == ilink.ItemTypeVoice && item.VoiceItem != nil {
			voicePlaytime = item.VoiceItem.Playtime
		}
	}
	log.Printf("[handler] meta bot=%s message_id=%d from=%s to=%s type=%s state=%s items=%s context=%s file_name=%q file_len=%q voice_playtime=%d",
		botID,
		msg.MessageID,
		msg.FromUserID,
		msg.ToUserID,
		messageTypeName(msg.MessageType),
		messageStateName(msg.MessageState),
		strings.Join(itemTypes, ","),
		shortToken(msg.ContextToken),
		fileName,
		fileLen,
		voicePlaytime,
	)
}

func messageTypeName(t int) string {
	switch t {
	case ilink.MessageTypeUser:
		return "user"
	case ilink.MessageTypeBot:
		return "bot"
	default:
		return fmt.Sprintf("%d", t)
	}
}

func messageStateName(state int) string {
	switch state {
	case ilink.MessageStateNew:
		return "new"
	case ilink.MessageStateGenerating:
		return "generating"
	case ilink.MessageStateFinish:
		return "finish"
	default:
		return fmt.Sprintf("%d", state)
	}
}

func itemTypeName(t int) string {
	switch t {
	case ilink.ItemTypeText:
		return "text"
	case ilink.ItemTypeImage:
		return "image"
	case ilink.ItemTypeVoice:
		return "voice"
	case ilink.ItemTypeFile:
		return "file"
	case ilink.ItemTypeVideo:
		return "video"
	default:
		return fmt.Sprintf("%d", t)
	}
}

func shortToken(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= 14 {
		return token
	}
	return token[:6] + "..." + token[len(token)-6:]
}

func extractImage(msg ilink.WeixinMessage) *ilink.ImageItem {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeImage && item.ImageItem != nil {
			return item.ImageItem
		}
	}
	return nil
}

func extractVideo(msg ilink.WeixinMessage) *ilink.VideoItem {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeVideo && item.VideoItem != nil {
			return item.VideoItem
		}
	}
	return nil
}

func extractFile(msg ilink.WeixinMessage) *ilink.FileItem {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeFile && item.FileItem != nil {
			return item.FileItem
		}
	}
	return nil
}

func extractVoiceText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeVoice && item.VoiceItem != nil && item.VoiceItem.Text != "" {
			return item.VoiceItem.Text
		}
	}
	return ""
}

// handleImageMessage downloads an image and forwards it to the agent for processing.
func (h *Handler) handleImageMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, img *ilink.ImageItem) {
	clientID := NewClientID()
	botID := client.BotID()
	log.Printf("[handler] bot=%s received image from %s, downloading...", botID, msg.FromUserID)

	var data []byte
	var err error
	if img.URL != "" {
		data, _, err = downloadFile(ctx, img.URL)
	} else if img.Media != nil && img.Media.EncryptQueryParam != "" {
		data, err = DownloadFileFromCDN(ctx, img.Media.EncryptQueryParam, img.Media.AESKey)
	} else {
		log.Printf("[handler] bot=%s image has no URL or media info from %s", botID, msg.FromUserID)
		return
	}
	if err != nil {
		log.Printf("[handler] bot=%s failed to download image from %s: %v", botID, msg.FromUserID, err)
		return
	}

	mimeType := detectImageMime(data)
	log.Printf("[handler] bot=%s downloaded image from %s (%d bytes, %s)", botID, msg.FromUserID, len(data), mimeType)

	if _, err := saveInboundImage(defaultInboundImageDir(), msg.FromUserID, data); err != nil {
		log.Printf("[handler] bot=%s failed to save inbound image from %s: %v", botID, msg.FromUserID, err)
	}

	if h.saveDir != "" {
		go h.handleImageSave(ctx, client, msg, img)
	}

	h.contextTokens.Store(msg.FromUserID, msg.ContextToken)

	wait, ok := h.tryBeginChat(chatKey(client, msg.FromUserID))
	if !ok {
		log.Printf("[handler] image handling is busy for %s, sending busy notice", msg.FromUserID)
		SendTextReply(ctx, client, msg.FromUserID, agentBusyReply(wait), msg.ContextToken, clientID)
		return
	}
	defer h.endChat(chatKey(client, msg.FromUserID))

	go func() {
		if typingErr := SendTypingState(ctx, client, msg.FromUserID, msg.ContextToken); typingErr != nil {
			log.Printf("[handler] failed to send typing state: %v", typingErr)
		}
	}()

	agentName, ag, selected := h.selectedAgent(msg.FromUserID)
	if ag == nil && selected {
		ag, err = h.getAgent(ctx, agentName)
		if err != nil {
			log.Printf("[handler] bot=%s selected agent %q not available for image from %s: %v", botID, agentName, msg.FromUserID, err)
			SendTextReply(ctx, client, msg.FromUserID, agentFailureReply(agentName, err), msg.ContextToken, clientID)
			return
		}
	}
	if ag == nil {
		log.Printf("[handler] bot=%s no agent ready, sending recovery notice for image from %s", botID, msg.FromUserID)
		SendTextReply(ctx, client, msg.FromUserID, agentRecoveringReply(), msg.ContextToken, clientID)
		return
	}

	var reply string
	if imgAgent, ok := ag.(agent.ImageChatAgent); ok {
		log.Printf("[handler] bot=%s sending image to agent via ChatWithImage for %s", botID, msg.FromUserID)
		reply, err = imgAgent.ChatWithImage(ctx, msg.FromUserID, "请识别并描述这张图片的内容。", &agent.ImageInput{
			MimeType: mimeType,
			Data:     data,
		})
	} else {
		reply, err = h.chatWithAgent(ctx, ag, msg.FromUserID, "[用户发送了一张图片，但当前 agent 不支持图片处理]")
	}
	if err != nil {
		reply = agentFailureReply(agentName, err)
	}

	h.sendReplyWithMedia(ctx, client, msg, agentName, reply, clientID)
}

func (h *Handler) handleStoredMediaMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, kind, fileName string, media *ilink.MediaInfo) {
	botID := client.BotID()
	log.Printf("[handler] bot=%s received %s from %s, downloading...", botID, kind, msg.FromUserID)

	if media == nil || media.EncryptQueryParam == "" {
		log.Printf("[handler] bot=%s %s has no media info from %s", botID, kind, msg.FromUserID)
		return
	}
	data, err := DownloadFileFromCDN(ctx, media.EncryptQueryParam, media.AESKey)
	if err != nil {
		log.Printf("[handler] bot=%s failed to download %s from %s: %v", botID, kind, msg.FromUserID, err)
		return
	}

	contentType := inferInboundMediaContentType(kind, fileName)
	path, err := saveInboundMedia(defaultInboundMediaDir(kind), msg.FromUserID, kind, fileName, data, contentType)
	if err != nil {
		log.Printf("[handler] bot=%s failed to save inbound %s from %s: %v", botID, kind, msg.FromUserID, err)
		return
	}

	h.contextTokens.Store(msg.FromUserID, msg.ContextToken)
	h.addPendingMedia(msg.FromUserID, pendingMedia{Kind: kind, Name: safeInboundMediaName(fileName, kind, contentType), Path: path})
	log.Printf("[handler] queued inbound %s from %s for next text instruction: %s", kind, msg.FromUserID, path)
}

func (h *Handler) addPendingMedia(userID string, media pendingMedia) {
	value, _ := h.pendingMedia.LoadOrStore(userID, []pendingMedia{})
	items := append(value.([]pendingMedia), media)
	h.pendingMedia.Store(userID, items)
}

func (h *Handler) withPendingMediaContext(userID, text string) string {
	value, ok := h.pendingMedia.LoadAndDelete(userID)
	if !ok {
		return text
	}
	items, ok := value.([]pendingMedia)
	if !ok || len(items) == 0 {
		return text
	}

	var b strings.Builder
	b.WriteString(text)
	b.WriteString("\n\n用户刚才发送的附件如下，请只在本次需求需要时使用：\n")
	for _, item := range items {
		b.WriteString("- ")
		b.WriteString(mediaKindLabel(item.Kind))
		if item.Name != "" {
			b.WriteString("：")
			b.WriteString(item.Name)
		}
		b.WriteString("\n  本机路径：")
		b.WriteString(item.Path)
		b.WriteByte('\n')
	}
	return b.String()
}

func saveInboundImage(dir, userID string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ts := time.Now().Format("20060102-150405")
	fileName := fmt.Sprintf("%s-%s%s", ts, safeFileToken(userID), detectImageExt(data))
	filePath := filepath.Join(dir, fileName)
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return "", err
	}
	log.Printf("[handler] saved inbound image from %s to %s (%d bytes)", userID, filePath, len(data))
	return filePath, nil
}

func saveInboundMedia(dir, userID, kind, fileName string, data []byte, contentType string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ts := time.Now().Format("20060102-150405")
	name := safeInboundMediaName(fileName, kind, contentType)
	filePath := filepath.Join(dir, fmt.Sprintf("%s-%s-%s", ts, safeFileToken(userID), name))
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return "", err
	}
	log.Printf("[handler] saved inbound %s from %s to %s (%d bytes)", kind, userID, filePath, len(data))
	return filePath, nil
}

func safeInboundMediaName(fileName, kind, contentType string) string {
	name := filepath.Base(strings.TrimSpace(fileName))
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = kind + inboundMediaExt(kind, contentType)
	}
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name = strings.Trim(b.String(), "._")
	if name == "" {
		name = kind + inboundMediaExt(kind, contentType)
	}
	if filepath.Ext(name) == "" {
		name += inboundMediaExt(kind, contentType)
	}
	return name
}

func inferInboundMediaContentType(kind, fileName string) string {
	if fileName != "" {
		return inferContentType(fileName)
	}
	if kind == "video" {
		return "video/mp4"
	}
	return "application/octet-stream"
}

func inboundMediaExt(kind, contentType string) string {
	if contentType != "" {
		if ext := extensionByContentType(contentType); ext != "" {
			return ext
		}
	}
	if kind == "video" {
		return ".mp4"
	}
	return ".bin"
}

func extensionByContentType(contentType string) string {
	switch strings.ToLower(strings.Split(contentType, ";")[0]) {
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	}
	return ""
}

func mediaKindLabel(kind string) string {
	switch kind {
	case "video":
		return "视频"
	case "file":
		return "文件"
	default:
		return "媒体文件"
	}
}

func safeFileToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	token := b.String()
	if len(token) > 24 {
		return token[len(token)-24:]
	}
	if token == "" {
		return "unknown"
	}
	return token
}

func detectImageMime(data []byte) string {
	if len(data) < 4 {
		return "image/jpeg"
	}
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return "image/png"
	}
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46 {
		return "image/gif"
	}
	if len(data) >= 12 && data[0] == 0x52 && data[1] == 0x49 && data[8] == 0x57 && data[9] == 0x45 {
		return "image/webp"
	}
	return "image/jpeg"
}

func (h *Handler) handleImageSave(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, img *ilink.ImageItem) {
	clientID := NewClientID()
	botID := client.BotID()
	log.Printf("[handler] bot=%s received image from %s, saving to %s", botID, msg.FromUserID, h.saveDir)

	// Download image data
	var data []byte
	var err error

	if img.URL != "" {
		// Direct URL download
		data, _, err = downloadFile(ctx, img.URL)
	} else if img.Media != nil && img.Media.EncryptQueryParam != "" {
		// CDN encrypted download
		data, err = DownloadFileFromCDN(ctx, img.Media.EncryptQueryParam, img.Media.AESKey)
	} else {
		log.Printf("[handler] bot=%s image has no URL or media info from %s", botID, msg.FromUserID)
		return
	}

	if err != nil {
		log.Printf("[handler] bot=%s failed to download image from %s: %v", botID, msg.FromUserID, err)
		reply := fmt.Sprintf("Failed to save image: %v", err)
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}

	// Detect extension from content
	ext := detectImageExt(data)

	// Generate filename with timestamp
	ts := time.Now().Format("20060102-150405")
	fileName := fmt.Sprintf("%s%s", ts, ext)
	filePath := filepath.Join(h.saveDir, fileName)

	// Ensure save directory exists
	if err := os.MkdirAll(h.saveDir, 0o755); err != nil {
		log.Printf("[handler] failed to create save dir: %v", err)
		return
	}

	// Write image file
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		log.Printf("[handler] failed to write image: %v", err)
		reply := fmt.Sprintf("Failed to save image: %v", err)
		_ = SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID)
		return
	}

	// Write sidecar file
	sidecarPath := filePath + ".sidecar.md"
	sidecarContent := fmt.Sprintf("---\nid: %s\n---\n", uuid.New().String())
	if err := os.WriteFile(sidecarPath, []byte(sidecarContent), 0o644); err != nil {
		log.Printf("[handler] failed to write sidecar: %v", err)
	}

	log.Printf("[handler] bot=%s saved image from %s to %s (%d bytes)", botID, msg.FromUserID, filePath, len(data))
	reply := fmt.Sprintf("Saved: %s", fileName)
	if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
		log.Printf("[handler] bot=%s failed to send reply to %s: %v", botID, msg.FromUserID, err)
	}
}

func detectImageExt(data []byte) string {
	if len(data) < 4 {
		return ".bin"
	}
	// PNG: 89 50 4E 47
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return ".png"
	}
	// JPEG: FF D8 FF
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return ".jpg"
	}
	// GIF: 47 49 46
	if data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46 {
		return ".gif"
	}
	// WebP: 52 49 46 46 ... 57 45 42 50
	if len(data) >= 12 && data[0] == 0x52 && data[1] == 0x49 && data[8] == 0x57 && data[9] == 0x45 {
		return ".webp"
	}
	// BMP: 42 4D
	if data[0] == 0x42 && data[1] == 0x4D {
		return ".bmp"
	}
	return ".jpg" // default to jpg for WeChat images
}
