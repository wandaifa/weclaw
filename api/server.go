package api

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/messaging"
)

const (
	AccountReloadPath = "/api/internal/accounts/reload"
	AccountsPath      = "/api/internal/accounts"
	AccountStatePath  = "/api/internal/accounts/state"
	MessageMergePath  = "/api/internal/settings/message-merge"
)

// AccountReloadResult describes the accounts active after a reload.
type AccountReloadResult struct {
	Clients  []*ilink.Client
	Added    int
	Replaced int
}

// AccountReloader refreshes credentials and starts monitors for changed accounts.
type AccountReloader func(context.Context) (AccountReloadResult, error)

// AccountStatus describes one loaded Bot without exposing its credential.
type AccountStatus struct {
	BotID       string `json:"bot_id"`
	ILinkUserID string `json:"ilink_user_id"`
	Status      string `json:"status"`
}

// AccountStatusProvider returns the current monitor state for every loaded Bot.
type AccountStatusProvider func() []AccountStatus

// AccountStateController changes one Bot's enabled state and reloads accounts.
type AccountStateController func(context.Context, string, bool) (AccountReloadResult, error)

// MessageMergeSettings is the JSON-safe representation of message coalescing settings.
type MessageMergeSettings struct {
	IdleSeconds    int `json:"idle_seconds"`
	MaxWaitSeconds int `json:"max_wait_seconds"`
	MaxMessages    int `json:"max_messages"`
	MaxChars       int `json:"max_chars"`
}

type MessageMergeProvider func() MessageMergeSettings
type MessageMergeController func(context.Context, MessageMergeSettings) (MessageMergeSettings, error)

// Server provides an HTTP API for sending messages.
type Server struct {
	mu       sync.RWMutex
	clients  []*ilink.Client
	reloader AccountReloader
	status   AccountStatusProvider
	state    AccountStateController
	merge    MessageMergeProvider
	setMerge MessageMergeController
	addr     string
}

// NewServer creates an API server.
func NewServer(clients []*ilink.Client, addr string) *Server {
	if addr == "" {
		addr = "127.0.0.1:18011"
	}
	return &Server{clients: clients, addr: addr}
}

// SetAccountReloader enables hot-loading credentials through the local API.
func (s *Server) SetAccountReloader(reloader AccountReloader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloader = reloader
}

// SetAccountStatusProvider exposes monitor state through the loopback-only account endpoint.
func (s *Server) SetAccountStatusProvider(provider AccountStatusProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = provider
}

// SetAccountStateController enables local account state changes through the loopback API.
func (s *Server) SetAccountStateController(controller AccountStateController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = controller
}

func (s *Server) SetMessageMergeProvider(provider MessageMergeProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.merge = provider
}

func (s *Server) SetMessageMergeController(controller MessageMergeController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setMerge = controller
}

// SendRequest is the JSON body for POST /api/send.
type SendRequest struct {
	BotID    string `json:"bot_id,omitempty"`
	To       string `json:"to"`
	Text     string `json:"text,omitempty"`
	MediaURL string `json:"media_url,omitempty"` // image/video/file URL
}

// Run starts the HTTP server. Blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/send", s.handleSend)
	mux.HandleFunc(AccountReloadPath, s.handleAccountReload)
	mux.HandleFunc(AccountsPath, s.handleAccounts)
	mux.HandleFunc(AccountStatePath, s.handleAccountState)
	mux.HandleFunc(MessageMergePath, s.handleMessageMerge)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{Addr: s.addr, Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	log.Printf("[api] listening on %s", s.addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	clients := s.clientsSnapshot()
	data := struct {
		Addr         string
		AccountCount int
		HasAccounts  bool
		Accounts     []AccountStatus
	}{
		Addr:         s.addr,
		AccountCount: len(clients),
		HasAccounts:  len(clients) > 0,
		Accounts:     s.accountStatuses(),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTemplate.Execute(w, data); err != nil {
		log.Printf("[api] render index failed: %v", err)
	}
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.To == "" {
		http.Error(w, `"to" is required`, http.StatusBadRequest)
		return
	}
	if req.Text == "" && req.MediaURL == "" {
		http.Error(w, `"text" or "media_url" is required`, http.StatusBadRequest)
		return
	}

	clients := s.clientsSnapshot()
	if len(clients) == 0 {
		http.Error(w, "no accounts configured", http.StatusServiceUnavailable)
		return
	}

	client, err := ilink.SelectClient(clients, req.BotID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	// Send text if provided
	if req.Text != "" {
		if err := messaging.SendTextReply(ctx, client, req.To, req.Text, "", ""); err != nil {
			log.Printf("[api] send text failed: %v", err)
			http.Error(w, "send text failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[api] sent text to %s: %q", req.To, req.Text)

		// Extract and send any markdown images embedded in text
		for _, imgURL := range messaging.ExtractImageURLs(req.Text) {
			if err := messaging.SendMediaFromURL(ctx, client, req.To, imgURL, ""); err != nil {
				log.Printf("[api] send extracted image failed: %v", err)
			} else {
				log.Printf("[api] sent extracted image to %s: %s", req.To, imgURL)
			}
		}
	}

	// Send media if provided
	if req.MediaURL != "" {
		if err := messaging.SendMediaFromURL(ctx, client, req.To, req.MediaURL, ""); err != nil {
			log.Printf("[api] send media failed: %v", err)
			http.Error(w, "send media failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[api] sent media to %s: %s", req.To, req.MediaURL)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleAccountReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}

	s.mu.RLock()
	reloader := s.reloader
	s.mu.RUnlock()
	if reloader == nil {
		http.Error(w, "account reload is unavailable", http.StatusServiceUnavailable)
		return
	}

	result, err := reloader(r.Context())
	if err != nil {
		log.Printf("[api] account reload failed: %v", err)
		http.Error(w, "account reload failed", http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	s.clients = append([]*ilink.Client(nil), result.Clients...)
	s.mu.Unlock()

	log.Printf("[api] accounts reloaded: total=%d added=%d replaced=%d", len(result.Clients), result.Added, result.Replaced)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{
		"accounts": len(result.Clients),
		"added":    result.Added,
		"replaced": result.Replaced,
	})
}

// AccountStateRequest is the JSON body for POST /api/internal/accounts/state.
type AccountStateRequest struct {
	BotID    string `json:"bot_id"`
	Disabled bool   `json:"disabled"`
}

func (s *Server) handleAccountState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	var req AccountStateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BotID == "" {
		http.Error(w, "bot_id is required", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	controller := s.state
	s.mu.RUnlock()
	if controller == nil {
		http.Error(w, "account state changes are unavailable", http.StatusServiceUnavailable)
		return
	}
	result, err := controller(r.Context(), req.BotID, req.Disabled)
	if err != nil {
		log.Printf("[api] account state change failed: %v", err)
		http.Error(w, "account state change failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.clients = append([]*ilink.Client(nil), result.Clients...)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"bot_id":   req.BotID,
		"disabled": req.Disabled,
		"accounts": len(result.Clients),
	})
}

func (s *Server) handleMessageMerge(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	s.mu.RLock()
	provider, controller := s.merge, s.setMerge
	s.mu.RUnlock()
	if r.Method == http.MethodGet {
		if provider == nil {
			http.Error(w, "message merge settings are unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(provider())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	if controller == nil {
		http.Error(w, "message merge settings are unavailable", http.StatusServiceUnavailable)
		return
	}
	var settings MessageMergeSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	updated, err := controller(r.Context(), settings)
	if err != nil {
		http.Error(w, "invalid message merge settings: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string][]AccountStatus{"accounts": s.accountStatuses()})
}

func (s *Server) accountStatuses() []AccountStatus {
	s.mu.RLock()
	provider := s.status
	s.mu.RUnlock()
	if provider != nil {
		return provider()
	}
	clients := s.clientsSnapshot()
	accounts := make([]AccountStatus, 0, len(clients))
	for _, client := range clients {
		if client != nil {
			accounts = append(accounts, AccountStatus{BotID: client.BotID(), Status: "active"})
		}
	}
	return accounts
}

func (s *Server) clientsSnapshot() []*ilink.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]*ilink.Client(nil), s.clients...)
}

func isLoopbackRequest(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func accountStatusLabel(status string) string {
	switch status {
	case "active":
		return "在线"
	case "expired":
		return "会话已失效"
	case "disabled":
		return "已停用"
	default:
		return "启动中"
	}
}

var indexTemplate = template.Must(template.New("index").Funcs(template.FuncMap{"statusLabel": accountStatusLabel}).Parse(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>WeClaw API</title>
<style>
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:#111b21;color:#e9edef;font:15px/1.6 -apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif}.card{width:min(680px,calc(100vw - 32px));background:#202c33;border:1px solid #2a3942;border-radius:8px;padding:24px;box-shadow:0 18px 48px #0006}.top{display:flex;align-items:center;gap:10px;margin-bottom:18px}.dot{width:10px;height:10px;border-radius:50%;background:#00a884;box-shadow:0 0 0 4px #00a88422}h1{font-size:22px;line-height:1.2;margin:0}.muted{color:#8696a0}.status{display:inline-flex;align-items:center;gap:8px;margin:8px 0 20px;padding:7px 11px;border-radius:999px;background:#12372f;color:#8df4cf;font-weight:600}.warn{background:#3a2d16;color:#ffd98b}.grid{display:grid;grid-template-columns:130px 1fr;gap:8px 14px;margin:18px 0}.key{color:#8696a0}.value{word-break:break-all}code{font-family:"SFMono-Regular",Consolas,monospace;background:#111b21;border:1px solid #2a3942;border-radius:5px;padding:2px 6px}.endpoints{display:grid;gap:10px;margin-top:18px}.endpoint,.account{padding:12px;border-radius:6px;background:#111b21;border:1px solid #2a3942}.method{display:inline-block;min-width:46px;margin-right:8px;color:#00a884;font-weight:700}.accounts{display:grid;gap:8px;margin-top:18px}.account{display:grid;gap:3px}.account b{word-break:break-all}.account span{color:#8696a0;font-size:13px;word-break:break-all}.account em{font-style:normal;color:#8df4cf}.account em.expired{color:#ffd98b}.hint{margin-top:18px;color:#8696a0;font-size:13px}
</style>
</head>
<body>
<main class="card">
  <div class="top"><span class="dot"></span><h1>WeClaw API 服务</h1></div>
  {{if .HasAccounts}}
  <div class="status">服务已启动，微信账号已加载</div>
  {{else}}
  <div class="status warn">服务已启动，但还没有可用微信账号</div>
  {{end}}
  <div class="grid">
    <div class="key">监听地址</div><div class="value"><code>{{.Addr}}</code></div>
    <div class="key">微信账号数</div><div class="value">{{.AccountCount}}</div>
    <div class="key">健康检查</div><div class="value"><a style="color:#8df4cf" href="/health">/health</a></div>
  </div>
  <div class="endpoints">
    <div class="endpoint"><span class="method">GET</span><code>/health</code><div class="muted">返回 ok，表示 API 服务在线。</div></div>
    <div class="endpoint"><span class="method">POST</span><code>/api/send</code><div class="muted">发送微信文字、图片、视频或文件消息；多账号时请求体必须提供 bot_id。</div></div>
  </div>
  <section class="accounts">
    <div class="muted">已加载微信账号</div>
    {{range .Accounts}}<div class="account"><b>{{.BotID}}</b><span>扫码者：{{.ILinkUserID}}</span><em class="{{.Status}}">{{statusLabel .Status}}</em></div>{{end}}
  </section>
  <div class="hint">聊天记录查看页在 <code>http://127.0.0.1:18022/</code>，这里是 WeClaw 主服务 API。</div>
</main>
</body>
</html>`))
