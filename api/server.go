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
	AccountReloadPath      = "/api/internal/accounts/reload"
	AccountsPath           = "/api/internal/accounts"
	AccountStatePath       = "/api/internal/accounts/state"
	AccountRemovePath      = "/api/internal/accounts/remove"
	AccountsDeletedPath    = "/api/internal/accounts/deleted"
	MessageMergePath       = "/api/internal/settings/message-merge"
	MediaRetentionPath     = "/api/internal/settings/media-retention"
	AccessModePath         = "/api/internal/settings/access-mode"
	NonOwnerRoutingPath    = "/api/internal/settings/non-owner-routing"
	PermissionsPath        = "/api/internal/permissions"
	PermissionsBlockPath   = "/api/internal/permissions/block"
	PermissionsUsagePath   = "/api/internal/permissions/usage"
	PersonasPath           = "/api/internal/personas"
	PersonaDeletePath      = "/api/internal/personas/delete"
	PermissionsPersonaPath = "/api/internal/permissions/persona"
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

// AccountRemoveController permanently deletes one disabled Bot's
// credentials and reloads accounts.
type AccountRemoveController func(context.Context, string) (AccountReloadResult, error)

// DeletedAccountsProvider returns every account whose credentials were
// permanently removed, for a view-only "deleted accounts" listing.
type DeletedAccountsProvider func() ([]ilink.DeletedAccount, error)

// MessageMergeSettings is the JSON-safe representation of message coalescing settings.
type MessageMergeSettings struct {
	IdleSeconds    int `json:"idle_seconds"`
	MaxWaitSeconds int `json:"max_wait_seconds"`
	MaxMessages    int `json:"max_messages"`
	MaxChars       int `json:"max_chars"`
}

type MessageMergeProvider func() MessageMergeSettings
type MessageMergeController func(context.Context, MessageMergeSettings) (MessageMergeSettings, error)

// MediaRetentionSettings is the JSON-safe representation of the media cleanup retention window.
type MediaRetentionSettings struct {
	Days int `json:"days"`
}

type MediaRetentionProvider func() MediaRetentionSettings
type MediaRetentionController func(context.Context, MediaRetentionSettings) (MediaRetentionSettings, error)

// AccessModeSettings is the JSON-safe representation of the non-owner access
// gate: "public" (today's default), "allowlist" (owner + users with an
// explicit permission entry), or "owner_only" (refuse all non-owner messages).
type AccessModeSettings struct {
	Mode string `json:"mode"`
}

type AccessModeProvider func() AccessModeSettings
type AccessModeController func(context.Context, AccessModeSettings) (AccessModeSettings, error)

// NonOwnerRoutingSettings is the JSON-safe representation of which agent
// non-owner users default to, and whether they may switch between
// claude/codex-shared themselves.
type NonOwnerRoutingSettings struct {
	DefaultAgent string `json:"default_agent"` // "" displayed/stored as "codex-shared" (the actual default)
	AllowSwitch  bool   `json:"allow_switch"`
}

type NonOwnerRoutingProvider func() NonOwnerRoutingSettings
type NonOwnerRoutingController func(context.Context, NonOwnerRoutingSettings) (NonOwnerRoutingSettings, error)

// UserPermissionInfo describes one WeChat user's configured sandbox tier for
// the internal permissions API. Owners are listed read-only (IsOwner=true)
// and cannot be changed through this API.
type UserPermissionInfo struct {
	UserID     string `json:"user_id"`
	Level      string `json:"level"` // "read_only" | "workspace_write"
	DailyLimit int    `json:"daily_limit"`
	IsOwner    bool   `json:"is_owner"`
	// Blocked refuses this user's messages before they reach an agent,
	// independent of Level/DailyLimit and of AccessMode. Set only through
	// PermissionsBlockPath, never through the level/quota save below, so
	// the two actions can't clobber each other.
	Blocked bool `json:"blocked"`
	// Persona is the explicitly bound persona name, or "" if this user is
	// using the default persona. Owners never have one (always full access).
	Persona string `json:"persona,omitempty"`
}

// PermissionsProvider lists every non-owner user seen so far with their
// current tier (defaulted to read_only if never configured).
type PermissionsProvider func() []UserPermissionInfo

// PermissionSetRequest is the JSON body for POST /api/internal/permissions.
type PermissionSetRequest struct {
	UserID     string `json:"user_id"`
	Level      string `json:"level"`
	DailyLimit int    `json:"daily_limit,omitempty"`
}

// PermissionController persists and immediately applies one user's tier.
type PermissionController func(context.Context, PermissionSetRequest) (UserPermissionInfo, error)

// PermissionBlockRequest is the JSON body for POST /api/internal/permissions/block.
// It is deliberately a separate request/endpoint from PermissionSetRequest so
// that blocking a user and saving their level/quota are two independent
// actions that never clobber each other.
type PermissionBlockRequest struct {
	UserID  string `json:"user_id"`
	Blocked bool   `json:"blocked"`
}

// PermissionBlockController persists and immediately applies one user's
// blocked flag, leaving their Level/DailyLimit untouched.
type PermissionBlockController func(context.Context, PermissionBlockRequest) (UserPermissionInfo, error)

// UsageInfo is one non-owner user's message count for the current local day.
type UsageInfo struct {
	UserID string `json:"user_id"`
	Date   string `json:"date"`
	Count  int    `json:"count"`
	Limit  int    `json:"limit"`
}

// UsageProvider returns today's quota usage snapshot for the internal API.
type UsageProvider func() []UsageInfo

// PersonaInfo describes one stored persona for the internal personas API.
type PersonaInfo struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// PersonasProvider lists every stored persona.
type PersonasProvider func() ([]PersonaInfo, error)

// PersonaSaveRequest is the JSON body for POST /api/internal/personas.
type PersonaSaveRequest struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// PersonaSaveController creates or updates one persona's text.
type PersonaSaveController func(context.Context, PersonaSaveRequest) (PersonaInfo, error)

// PersonaDeleteRequest is the JSON body for POST /api/internal/personas/delete.
type PersonaDeleteRequest struct {
	Name string `json:"name"`
}

// PersonaDeleteController removes one persona. Deleting "default" is
// rejected by the underlying persona package.
type PersonaDeleteController func(context.Context, PersonaDeleteRequest) error

// PermissionPersonaRequest is the JSON body for POST
// /api/internal/permissions/persona. A separate endpoint from
// PermissionSetRequest for the same reason Blocked has its own endpoint:
// binding a persona and saving a sandbox tier must never clobber each other.
type PermissionPersonaRequest struct {
	UserID  string `json:"user_id"`
	Persona string `json:"persona"` // empty = clear the binding, fall back to the default persona
}

// PermissionPersonaController persists and immediately applies one user's
// persona binding, leaving Level/DailyLimit/Blocked untouched.
type PermissionPersonaController func(context.Context, PermissionPersonaRequest) (UserPermissionInfo, error)

// Server provides an HTTP API for sending messages.
type Server struct {
	mu                 sync.RWMutex
	clients            []*ilink.Client
	reloader           AccountReloader
	status             AccountStatusProvider
	state              AccountStateController
	remove             AccountRemoveController
	deletedAccounts    DeletedAccountsProvider
	merge              MessageMergeProvider
	setMerge           MessageMergeController
	mediaRetention     MediaRetentionProvider
	setMediaRetention  MediaRetentionController
	accessMode         AccessModeProvider
	setAccessMode      AccessModeController
	nonOwnerRouting    NonOwnerRoutingProvider
	setNonOwnerRouting NonOwnerRoutingController
	permissions        PermissionsProvider
	setPermission      PermissionController
	blockPermission    PermissionBlockController
	usage              UsageProvider
	personas           PersonasProvider
	savePersona        PersonaSaveController
	deletePersona      PersonaDeleteController
	setPersonaBinding  PermissionPersonaController
	addr               string
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

// SetAccountRemoveController enables permanent account deletion through the loopback API.
func (s *Server) SetAccountRemoveController(controller AccountRemoveController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remove = controller
}

// SetDeletedAccountsProvider exposes the deleted-accounts tombstone list through the loopback-only endpoint.
func (s *Server) SetDeletedAccountsProvider(provider DeletedAccountsProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletedAccounts = provider
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

func (s *Server) SetMediaRetentionProvider(provider MediaRetentionProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mediaRetention = provider
}

func (s *Server) SetMediaRetentionController(controller MediaRetentionController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setMediaRetention = controller
}

// SetAccessModeProvider exposes the non-owner access gate through the loopback-only settings endpoint.
func (s *Server) SetAccessModeProvider(provider AccessModeProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessMode = provider
}

// SetAccessModeController enables changing the non-owner access gate through the loopback API.
func (s *Server) SetAccessModeController(controller AccessModeController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setAccessMode = controller
}

func (s *Server) SetNonOwnerRoutingProvider(provider NonOwnerRoutingProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nonOwnerRouting = provider
}

func (s *Server) SetNonOwnerRoutingController(controller NonOwnerRoutingController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setNonOwnerRouting = controller
}

// SetPermissionsProvider exposes per-user sandbox tiers through the loopback-only permissions endpoint.
func (s *Server) SetPermissionsProvider(provider PermissionsProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permissions = provider
}

// SetPermissionController enables setting one user's sandbox tier through the loopback API.
func (s *Server) SetPermissionController(controller PermissionController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setPermission = controller
}

// SetPermissionBlockController enables blocking/unblocking one user through
// the loopback API, independent of SetPermissionController.
func (s *Server) SetPermissionBlockController(controller PermissionBlockController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockPermission = controller
}

// SetUsageProvider exposes today's quota usage through the loopback-only usage endpoint.
func (s *Server) SetUsageProvider(provider UsageProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = provider
}

// SetPersonasProvider exposes the list of stored personas through the loopback-only endpoint.
func (s *Server) SetPersonasProvider(provider PersonasProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.personas = provider
}

// SetPersonaSaveController enables creating/updating a persona through the loopback API.
func (s *Server) SetPersonaSaveController(controller PersonaSaveController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savePersona = controller
}

// SetPersonaDeleteController enables deleting a persona through the loopback API.
func (s *Server) SetPersonaDeleteController(controller PersonaDeleteController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletePersona = controller
}

// SetPermissionPersonaController enables binding a user to a persona through the loopback API.
func (s *Server) SetPermissionPersonaController(controller PermissionPersonaController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setPersonaBinding = controller
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
	mux.HandleFunc(AccountRemovePath, s.handleAccountRemove)
	mux.HandleFunc(AccountsDeletedPath, s.handleDeletedAccounts)
	mux.HandleFunc(MessageMergePath, s.handleMessageMerge)
	mux.HandleFunc(MediaRetentionPath, s.handleMediaRetention)
	mux.HandleFunc(AccessModePath, s.handleAccessMode)
	mux.HandleFunc(NonOwnerRoutingPath, s.handleNonOwnerRouting)
	mux.HandleFunc(PermissionsPath, s.handlePermissions)
	mux.HandleFunc(PermissionsBlockPath, s.handlePermissionsBlock)
	mux.HandleFunc(PermissionsUsagePath, s.handlePermissionsUsage)
	mux.HandleFunc(PersonasPath, s.handlePersonas)
	mux.HandleFunc(PersonaDeletePath, s.handlePersonaDelete)
	mux.HandleFunc(PermissionsPersonaPath, s.handlePermissionsPersona)
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
		if err := messaging.SendTextReply(ctx, client, req.To, req.Text, "", "", messaging.ReplyMeta{Agent: "push"}); err != nil {
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

// AccountRemoveRequest is the JSON body for POST /api/internal/accounts/remove.
type AccountRemoveRequest struct {
	BotID string `json:"bot_id"`
}

func (s *Server) handleAccountRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	var req AccountRemoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BotID == "" {
		http.Error(w, "bot_id is required", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	controller := s.remove
	s.mu.RUnlock()
	if controller == nil {
		http.Error(w, "account removal is unavailable", http.StatusServiceUnavailable)
		return
	}
	result, err := controller(r.Context(), req.BotID)
	if err != nil {
		log.Printf("[api] account removal failed: %v", err)
		http.Error(w, "account removal failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.clients = append([]*ilink.Client(nil), result.Clients...)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"bot_id":   req.BotID,
		"accounts": len(result.Clients),
	})
}

func (s *Server) handleDeletedAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	s.mu.RLock()
	provider := s.deletedAccounts
	s.mu.RUnlock()
	if provider == nil {
		http.Error(w, "deleted accounts are unavailable", http.StatusServiceUnavailable)
		return
	}
	deleted, err := provider()
	if err != nil {
		log.Printf("[api] load deleted accounts failed: %v", err)
		http.Error(w, "load deleted accounts failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string][]ilink.DeletedAccount{"deleted_accounts": deleted})
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

func (s *Server) handleMediaRetention(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	s.mu.RLock()
	provider, controller := s.mediaRetention, s.setMediaRetention
	s.mu.RUnlock()
	if r.Method == http.MethodGet {
		if provider == nil {
			http.Error(w, "media retention settings are unavailable", http.StatusServiceUnavailable)
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
		http.Error(w, "media retention settings are unavailable", http.StatusServiceUnavailable)
		return
	}
	var settings MediaRetentionSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if settings.Days < 1 || settings.Days > 3650 {
		http.Error(w, "days must be between 1 and 3650", http.StatusBadRequest)
		return
	}
	updated, err := controller(r.Context(), settings)
	if err != nil {
		http.Error(w, "invalid media retention settings: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

func (s *Server) handleAccessMode(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	s.mu.RLock()
	provider, controller := s.accessMode, s.setAccessMode
	s.mu.RUnlock()
	if r.Method == http.MethodGet {
		if provider == nil {
			http.Error(w, "access mode is unavailable", http.StatusServiceUnavailable)
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
		http.Error(w, "access mode is unavailable", http.StatusServiceUnavailable)
		return
	}
	var settings AccessModeSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	updated, err := controller(r.Context(), settings)
	if err != nil {
		http.Error(w, "invalid access mode: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

func (s *Server) handleNonOwnerRouting(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	s.mu.RLock()
	provider, controller := s.nonOwnerRouting, s.setNonOwnerRouting
	s.mu.RUnlock()
	if r.Method == http.MethodGet {
		if provider == nil {
			http.Error(w, "non-owner routing settings unavailable", http.StatusServiceUnavailable)
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
		http.Error(w, "non-owner routing settings unavailable", http.StatusServiceUnavailable)
		return
	}
	var settings NonOwnerRoutingSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	updated, err := controller(r.Context(), settings)
	if err != nil {
		http.Error(w, "invalid settings: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

func (s *Server) handlePermissions(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	s.mu.RLock()
	provider, controller := s.permissions, s.setPermission
	s.mu.RUnlock()

	if r.Method == http.MethodGet {
		if provider == nil {
			http.Error(w, "permissions are unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string][]UserPermissionInfo{"users": provider()})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	if controller == nil {
		http.Error(w, "permissions are unavailable", http.StatusServiceUnavailable)
		return
	}
	var req PermissionSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}
	updated, err := controller(r.Context(), req)
	if err != nil {
		http.Error(w, "invalid permission update: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

func (s *Server) handlePermissionsBlock(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	controller := s.blockPermission
	s.mu.RUnlock()
	if controller == nil {
		http.Error(w, "permissions are unavailable", http.StatusServiceUnavailable)
		return
	}
	var req PermissionBlockRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}
	updated, err := controller(r.Context(), req)
	if err != nil {
		http.Error(w, "invalid block update: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

func (s *Server) handlePermissionsUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	s.mu.RLock()
	provider := s.usage
	s.mu.RUnlock()
	if provider == nil {
		http.Error(w, "usage is unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string][]UsageInfo{"usage": provider()})
}

func (s *Server) handlePersonas(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	s.mu.RLock()
	provider, controller := s.personas, s.savePersona
	s.mu.RUnlock()

	if r.Method == http.MethodGet {
		if provider == nil {
			http.Error(w, "personas are unavailable", http.StatusServiceUnavailable)
			return
		}
		list, err := provider()
		if err != nil {
			http.Error(w, "failed to list personas: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string][]PersonaInfo{"personas": list})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	if controller == nil {
		http.Error(w, "personas are unavailable", http.StatusServiceUnavailable)
		return
	}
	var req PersonaSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	info, err := controller(r.Context(), req)
	if err != nil {
		http.Error(w, "invalid persona save: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func (s *Server) handlePersonaDelete(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	controller := s.deletePersona
	s.mu.RUnlock()
	if controller == nil {
		http.Error(w, "personas are unavailable", http.StatusServiceUnavailable)
		return
	}
	var req PersonaDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := controller(r.Context(), req); err != nil {
		http.Error(w, "invalid persona delete: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) handlePermissionsPersona(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		http.Error(w, "local requests only", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	controller := s.setPersonaBinding
	s.mu.RUnlock()
	if controller == nil {
		http.Error(w, "permissions are unavailable", http.StatusServiceUnavailable)
		return
	}
	var req PermissionPersonaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}
	updated, err := controller(r.Context(), req)
	if err != nil {
		http.Error(w, "invalid persona binding: "+err.Error(), http.StatusBadRequest)
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
