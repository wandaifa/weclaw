package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/api"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/fastclaw-ai/weclaw/persona"
	"github.com/fastclaw-ai/weclaw/store"
	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
)

var (
	foregroundFlag bool
	apiAddrFlag    string
)

func init() {
	startCmd.Flags().BoolVarP(&foregroundFlag, "foreground", "f", false, "Run in foreground (default is background)")
	startCmd.Flags().StringVar(&apiAddrFlag, "api-addr", "", "API server listen address (default 127.0.0.1:18011)")
	rootCmd.AddCommand(startCmd)
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the WeChat message bridge (auto-login if needed)",
	RunE:  runStart,
}

func runStart(cmd *cobra.Command, args []string) error {
	if !foregroundFlag {
		// Check if login is needed — if so, do it in foreground first, then daemon
		accounts, _ := ilink.LoadAllCredentials()
		if len(accounts) == 0 {
			fmt.Println("No WeChat accounts found, starting login...")
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			_, err := doLogin(ctx)
			cancel()
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
		}
		if launchAgentInstalled() {
			return startLaunchAgent()
		}
		return runDaemon()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := writePid(os.Getpid()); err != nil {
		log.Printf("Warning: failed to write pid file: %v", err)
	} else {
		defer os.Remove(pidFile())
	}

	// Load all accounts
	accounts, err := ilink.LoadAllCredentials()
	if err != nil {
		return fmt.Errorf("failed to load credentials: %w", err)
	}

	// No accounts — trigger login
	if len(accounts) == 0 {
		log.Println("No WeChat accounts found, starting login...")
		creds, err := doLogin(ctx)
		if err != nil {
			return fmt.Errorf("login failed: %w", err)
		}
		accounts = append(accounts, creds)
	}

	// Load config and auto-detect agents
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if config.DetectAndConfigure(cfg) {
		if err := config.Save(cfg); err != nil {
			log.Printf("Warning: failed to save auto-detected config: %v", err)
		} else {
			path, _ := config.ConfigPath()
			log.Printf("Auto-detected agents saved to %s", path)
		}
	}

	// Log all available agents
	if len(cfg.Agents) > 0 {
		names := make([]string, 0, len(cfg.Agents))
		for name := range cfg.Agents {
			names = append(names, name)
		}
		log.Printf("Available agents: %v (default: %s)", names, cfg.DefaultAgent)
	}

	// Open message persistence. Best-effort: a failure here logs and
	// continues without it rather than blocking the whole bridge from starting.
	dbPath, err := store.DefaultPath()
	if err != nil {
		log.Printf("Warning: could not resolve message store path: %v", err)
	} else if msgStore, err := store.Open(dbPath); err != nil {
		log.Printf("Warning: failed to open message store at %s: %v", dbPath, err)
	} else {
		messaging.SetMessageStore(msgStore)
		defer msgStore.Close()
		log.Printf("Message store ready: %s", dbPath)
	}

	// Create handler with an agent factory for on-demand agent creation
	var configMu sync.Mutex

	// Daily media cleanup: runs once at startup, then every 24h. Best-effort
	// and non-blocking — a failure here must never stop message handling.
	go func() {
		runCleanup := func() {
			configMu.Lock()
			days := cfg.MediaRetention.WithDefaults().Days
			configMu.Unlock()
			deleted, err := messaging.CleanupExpiredMedia(days)
			if err != nil {
				log.Printf("[media-cleanup] completed with errors, deleted %d file(s): %v", deleted, err)
			} else if deleted > 0 {
				log.Printf("[media-cleanup] deleted %d expired media file(s) (retention: %d days)", deleted, days)
			}
		}
		runCleanup()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCleanup()
			}
		}
	}()
	personaDir, err := persona.Dir()
	if err != nil {
		return fmt.Errorf("resolve personas dir: %w", err)
	}
	if err := persona.EnsureDefault(personaDir); err != nil {
		return fmt.Errorf("seed default persona: %w", err)
	}
	handler := messaging.NewHandler(
		func(ctx context.Context, name string) agent.Agent {
			return createAgentByName(ctx, cfg, name)
		},
		func(userID, name string) error {
			configMu.Lock()
			defer configMu.Unlock()
			if cfg.UserAgents == nil {
				cfg.UserAgents = make(map[string]string)
			}
			cfg.UserAgents[userID] = name
			return config.Save(cfg)
		},
	)
	if err := handler.SetMergeSettings(messageMergeSettings(cfg.MessageMerge.WithDefaults())); err != nil {
		return fmt.Errorf("invalid message merge settings: %w", err)
	}
	handler.SetUserAgents(cfg.UserAgents)
	handler.SetUserPermissions(cfg.UserPermissions)
	handler.SetAccessMode(cfg.AccessMode)
	handler.SetPersonaDir(personaDir)
	handler.SetDefaultPersona(cfg.DefaultPersona)
	handler.SetUserPersonas(cfg.UserPersonas)

	// Populate agent metas for /status
	var metas []messaging.AgentMeta
	workDirs := make(map[string]string, len(cfg.Agents))
	for name, agCfg := range cfg.Agents {
		command := agCfg.Command
		if agCfg.Type == "http" {
			command = agCfg.Endpoint
		}
		metas = append(metas, messaging.AgentMeta{
			Name:    name,
			Type:    agCfg.Type,
			Command: command,
			Model:   agCfg.Model,
		})
		if agCfg.Cwd != "" {
			workDirs[name] = agCfg.Cwd
		}
	}
	handler.SetAgentMetas(metas)
	handler.SetAgentWorkDirs(workDirs)

	// Load custom aliases from agent configs
	handler.SetCustomAliases(config.BuildAliasMap(cfg.Agents))

	// Set save directory for images/files if configured
	if cfg.SaveDir != "" {
		handler.SetSaveDir(cfg.SaveDir)
		log.Printf("Image save directory: %s", cfg.SaveDir)
	}

	// Start default agent initialization in background so monitors can start immediately
	go func() {
		if cfg.DefaultAgent == "" {
			log.Println("No default agent configured, staying in echo mode")
			return
		}
		for attempt := 1; ; attempt++ {
			log.Printf("Initializing default agent %q in background (attempt %d)...", cfg.DefaultAgent, attempt)
			ag := createAgentByName(ctx, cfg, cfg.DefaultAgent)
			if ag != nil {
				handler.SetDefaultAgent(cfg.DefaultAgent, ag)
				return
			}
			log.Printf("Failed to initialize default agent %q, will retry in 30s", cfg.DefaultAgent)

			timer := time.NewTimer(30 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()

	// Start account monitors and expose hot reload through the local API.
	log.Printf("Starting message bridge for %d account(s)...", len(accounts))
	accountManager := newAccountManager(ctx, handler)
	initialAccounts, err := accountManager.Reload(accounts)
	if err != nil {
		return fmt.Errorf("start account monitors: %w", err)
	}

	// Resolve API addr: flag > env/config > default
	apiAddr := cfg.APIAddr // already includes env override from loadEnv
	if apiAddrFlag != "" {
		apiAddr = apiAddrFlag
	}
	apiServer := api.NewServer(initialAccounts.Clients, apiAddr)
	apiServer.SetMessageMergeProvider(func() api.MessageMergeSettings {
		return apiMessageMergeSettings(handler.MergeSettings())
	})
	apiServer.SetMessageMergeController(func(_ context.Context, settings api.MessageMergeSettings) (api.MessageMergeSettings, error) {
		merge := messaging.MergeSettings{
			IdleDelay:   time.Duration(settings.IdleSeconds) * time.Second,
			MaxWait:     time.Duration(settings.MaxWaitSeconds) * time.Second,
			MaxMessages: settings.MaxMessages,
			MaxChars:    settings.MaxChars,
		}
		if err := handler.SetMergeSettings(merge); err != nil {
			return api.MessageMergeSettings{}, err
		}
		configMu.Lock()
		defer configMu.Unlock()
		cfg.MessageMerge = config.MessageMergeConfig{
			IdleSeconds: settings.IdleSeconds, MaxWaitSeconds: settings.MaxWaitSeconds,
			MaxMessages: settings.MaxMessages, MaxChars: settings.MaxChars,
		}
		if err := config.Save(cfg); err != nil {
			return api.MessageMergeSettings{}, err
		}
		return apiMessageMergeSettings(handler.MergeSettings()), nil
	})
	apiServer.SetMediaRetentionProvider(func() api.MediaRetentionSettings {
		configMu.Lock()
		defer configMu.Unlock()
		return api.MediaRetentionSettings{Days: cfg.MediaRetention.WithDefaults().Days}
	})
	apiServer.SetMediaRetentionController(func(_ context.Context, settings api.MediaRetentionSettings) (api.MediaRetentionSettings, error) {
		configMu.Lock()
		defer configMu.Unlock()
		cfg.MediaRetention = config.MediaRetentionConfig{Days: settings.Days}
		if err := config.Save(cfg); err != nil {
			return api.MediaRetentionSettings{}, err
		}
		return api.MediaRetentionSettings{Days: cfg.MediaRetention.Days}, nil
	})
	apiServer.SetAccessModeProvider(func() api.AccessModeSettings {
		return api.AccessModeSettings{Mode: string(handler.AccessMode())}
	})
	apiServer.SetAccessModeController(func(_ context.Context, settings api.AccessModeSettings) (api.AccessModeSettings, error) {
		mode, err := config.ParseAccessMode(settings.Mode)
		if err != nil {
			return api.AccessModeSettings{}, err
		}
		configMu.Lock()
		cfg.AccessMode = mode
		saveErr := config.Save(cfg)
		configMu.Unlock()
		if saveErr != nil {
			return api.AccessModeSettings{}, saveErr
		}
		handler.SetAccessMode(mode)
		return api.AccessModeSettings{Mode: string(mode)}, nil
	})
	apiServer.SetPermissionsProvider(func() []api.UserPermissionInfo {
		infos := make([]api.UserPermissionInfo, 0)
		for _, ownerID := range config.OwnerUserIDs() {
			infos = append(infos, api.UserPermissionInfo{UserID: ownerID, Level: "full_access", IsOwner: true})
		}
		for _, perm := range handler.PermissionsSnapshot() {
			infos = append(infos, api.UserPermissionInfo{
				UserID: perm.UserID, Level: string(perm.Level), DailyLimit: perm.DailyLimit, Blocked: perm.Blocked,
				Persona: perm.Persona,
			})
		}
		return infos
	})
	apiServer.SetPermissionController(func(_ context.Context, req api.PermissionSetRequest) (api.UserPermissionInfo, error) {
		if config.IsOwner(req.UserID) {
			return api.UserPermissionInfo{}, fmt.Errorf("cannot set permission for the owner")
		}
		level, err := config.ParsePermissionLevel(req.Level)
		if err != nil {
			return api.UserPermissionInfo{}, err
		}

		configMu.Lock()
		if cfg.UserPermissions == nil {
			cfg.UserPermissions = make(map[string]config.UserPermission)
		}
		// Preserve any existing Blocked flag: saving level/quota here must
		// never clobber a block set through the separate block endpoint.
		blocked := cfg.UserPermissions[req.UserID].Blocked
		perm := config.UserPermission{Level: level, DailyLimit: req.DailyLimit, Blocked: blocked}
		cfg.UserPermissions[req.UserID] = perm
		saveErr := config.Save(cfg)
		configMu.Unlock()
		if saveErr != nil {
			return api.UserPermissionInfo{}, saveErr
		}

		handler.SetUserPermission(req.UserID, perm)
		return api.UserPermissionInfo{UserID: req.UserID, Level: string(level), DailyLimit: req.DailyLimit, Blocked: blocked}, nil
	})
	apiServer.SetPermissionBlockController(func(_ context.Context, req api.PermissionBlockRequest) (api.UserPermissionInfo, error) {
		if config.IsOwner(req.UserID) {
			return api.UserPermissionInfo{}, fmt.Errorf("cannot block the owner")
		}

		configMu.Lock()
		if cfg.UserPermissions == nil {
			cfg.UserPermissions = make(map[string]config.UserPermission)
		}
		perm := cfg.UserPermissions[req.UserID]
		if perm.Level == "" {
			perm.Level = config.PermissionReadOnly
		}
		perm.Blocked = req.Blocked
		cfg.UserPermissions[req.UserID] = perm
		saveErr := config.Save(cfg)
		configMu.Unlock()
		if saveErr != nil {
			return api.UserPermissionInfo{}, saveErr
		}

		handler.SetUserPermission(req.UserID, perm)
		return api.UserPermissionInfo{UserID: req.UserID, Level: string(perm.Level), DailyLimit: perm.DailyLimit, Blocked: perm.Blocked}, nil
	})
	apiServer.SetPersonasProvider(func() ([]api.PersonaInfo, error) {
		list, err := persona.List(personaDir)
		if err != nil {
			return nil, err
		}
		infos := make([]api.PersonaInfo, 0, len(list))
		for _, p := range list {
			infos = append(infos, api.PersonaInfo{Name: p.Name, Text: p.Text})
		}
		return infos, nil
	})
	apiServer.SetPersonaSaveController(func(_ context.Context, req api.PersonaSaveRequest) (api.PersonaInfo, error) {
		if err := persona.Save(personaDir, req.Name, req.Text); err != nil {
			return api.PersonaInfo{}, err
		}
		return api.PersonaInfo{Name: req.Name, Text: req.Text}, nil
	})
	apiServer.SetPersonaDeleteController(func(_ context.Context, req api.PersonaDeleteRequest) error {
		return persona.Delete(personaDir, req.Name)
	})
	apiServer.SetPermissionPersonaController(func(_ context.Context, req api.PermissionPersonaRequest) (api.UserPermissionInfo, error) {
		if config.IsOwner(req.UserID) {
			return api.UserPermissionInfo{}, fmt.Errorf("cannot set persona for the owner")
		}
		configMu.Lock()
		if cfg.UserPersonas == nil {
			cfg.UserPersonas = make(map[string]string)
		}
		if req.Persona == "" {
			delete(cfg.UserPersonas, req.UserID)
		} else {
			cfg.UserPersonas[req.UserID] = req.Persona
		}
		saveErr := config.Save(cfg)
		existing := cfg.UserPermissions[req.UserID]
		configMu.Unlock()
		if saveErr != nil {
			return api.UserPermissionInfo{}, saveErr
		}

		handler.SetUserPersona(req.UserID, req.Persona)
		return api.UserPermissionInfo{
			UserID: req.UserID, Level: string(existing.Level), DailyLimit: existing.DailyLimit,
			Blocked: existing.Blocked, Persona: req.Persona,
		}, nil
	})
	apiServer.SetUsageProvider(func() []api.UsageInfo {
		entries := handler.UsageSnapshot()
		usage := make([]api.UsageInfo, 0, len(entries))
		for _, e := range entries {
			usage = append(usage, api.UsageInfo{UserID: e.UserID, Date: e.Date, Count: e.Count, Limit: e.Limit})
		}
		return usage
	})
	apiServer.SetAccountStatusProvider(func() []api.AccountStatus {
		saved, err := ilink.LoadAccounts()
		if err != nil {
			return accountManager.Statuses()
		}
		loaded := make(map[string]string)
		for _, status := range accountManager.Statuses() {
			loaded[status.BotID] = status.Status
		}
		statuses := make([]api.AccountStatus, 0, len(saved))
		for _, account := range saved {
			status := loaded[account.BotID]
			if account.Disabled {
				status = "disabled"
			} else if status == "" {
				status = "starting"
			}
			statuses = append(statuses, api.AccountStatus{BotID: account.BotID, ILinkUserID: account.ILinkUserID, Status: status})
		}
		return statuses
	})
	apiServer.SetAccountStateController(func(ctx context.Context, botID string, disabled bool) (api.AccountReloadResult, error) {
		accounts, err := ilink.LoadAccounts()
		if err != nil {
			return api.AccountReloadResult{}, err
		}
		found := false
		for _, account := range accounts {
			if account.BotID == botID {
				found = true
				break
			}
		}
		if !found {
			return api.AccountReloadResult{}, fmt.Errorf("bot_id %q is not saved locally", botID)
		}
		if err := ilink.SetAccountDisabled(botID, disabled); err != nil {
			return api.AccountReloadResult{}, err
		}
		credentials, err := ilink.LoadAllCredentials()
		if err != nil {
			return api.AccountReloadResult{}, err
		}
		result, err := accountManager.Reload(credentials)
		if err != nil {
			return api.AccountReloadResult{}, err
		}
		return api.AccountReloadResult{Clients: result.Clients, Added: result.Added, Replaced: result.Replaced}, nil
	})
	apiServer.SetAccountRemoveController(func(ctx context.Context, botID string) (api.AccountReloadResult, error) {
		if err := ilink.RemoveAccount(botID); err != nil {
			return api.AccountReloadResult{}, err
		}
		credentials, err := ilink.LoadAllCredentials()
		if err != nil {
			return api.AccountReloadResult{}, err
		}
		result, err := accountManager.Reload(credentials)
		if err != nil {
			return api.AccountReloadResult{}, err
		}
		return api.AccountReloadResult{Clients: result.Clients, Added: result.Added, Replaced: result.Replaced}, nil
	})
	apiServer.SetDeletedAccountsProvider(ilink.LoadDeletedAccounts)
	apiServer.SetAccountReloader(func(context.Context) (api.AccountReloadResult, error) {
		credentials, err := ilink.LoadAllCredentials()
		if err != nil {
			return api.AccountReloadResult{}, err
		}
		result, err := accountManager.Reload(credentials)
		if err != nil {
			return api.AccountReloadResult{}, err
		}
		return api.AccountReloadResult{
			Clients:  result.Clients,
			Added:    result.Added,
			Replaced: result.Replaced,
		}, nil
	})
	go func() {
		if err := apiServer.Run(ctx); err != nil {
			log.Printf("API server error: %v", err)
		}
	}()

	accountManager.Wait()
	log.Println("All monitors stopped")
	return nil
}

// runMonitorWithRestart runs a monitor with automatic restart on failure.
func runMonitorWithRestart(ctx context.Context, creds *ilink.Credentials, handler *messaging.Handler, setStatus func(string)) {
	const maxRestartDelay = 30 * time.Second
	restartDelay := 3 * time.Second

	for {
		log.Printf("[%s] Starting monitor...", creds.ILinkBotID)
		setStatus("active")

		client := ilink.NewClient(creds)
		monitor, err := ilink.NewMonitor(client, handler.HandleMessage)
		if err != nil {
			log.Printf("[%s] Failed to create monitor: %v", creds.ILinkBotID, err)
		} else {
			err = monitor.Run(ctx)
		}

		// If context is cancelled, exit
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ilink.ErrSessionExpired) {
			setStatus("expired")
			log.Printf("[%s] Monitor stopped because its WeChat session expired; other accounts remain online.", creds.ILinkBotID)
			return
		}

		log.Printf("[%s] Monitor stopped: %v, restarting in %s", creds.ILinkBotID, err, restartDelay)
		select {
		case <-time.After(restartDelay):
		case <-ctx.Done():
			return
		}

		// Exponential backoff for restarts, capped
		restartDelay *= 2
		if restartDelay > maxRestartDelay {
			restartDelay = maxRestartDelay
		}
	}
}

func messageMergeSettings(cfg config.MessageMergeConfig) messaging.MergeSettings {
	return messaging.MergeSettings{
		IdleDelay:   time.Duration(cfg.IdleSeconds) * time.Second,
		MaxWait:     time.Duration(cfg.MaxWaitSeconds) * time.Second,
		MaxMessages: cfg.MaxMessages,
		MaxChars:    cfg.MaxChars,
	}
}

func apiMessageMergeSettings(settings messaging.MergeSettings) api.MessageMergeSettings {
	return api.MessageMergeSettings{
		IdleSeconds: int(settings.IdleDelay / time.Second), MaxWaitSeconds: int(settings.MaxWait / time.Second),
		MaxMessages: settings.MaxMessages, MaxChars: settings.MaxChars,
	}
}

// createAgentByName creates and starts an agent by its config name.
// Returns nil if the agent is not configured or fails to start.
func createAgentByName(ctx context.Context, cfg *config.Config, name string) agent.Agent {
	agCfg, ok := cfg.Agents[name]
	if !ok {
		log.Printf("[agent] %q not found in config", name)
		return nil
	}

	switch agCfg.Type {
	case "acp":
		ag := agent.NewACPAgent(agent.ACPAgentConfig{
			Command:      agCfg.Command,
			Args:         agCfg.Args,
			Cwd:          agCfg.Cwd,
			Env:          agCfg.Env,
			Model:        agCfg.Model,
			SystemPrompt: agCfg.SystemPrompt,
		})
		if err := ag.Start(ctx); err != nil {
			log.Printf("[agent] failed to start ACP agent %q: %v", name, err)
			return nil
		}
		log.Printf("[agent] started ACP agent: %s (command=%s, type=%s, model=%s)", name, agCfg.Command, agCfg.Type, agCfg.Model)
		return ag
	case "cli":
		ag := agent.NewCLIAgent(agent.CLIAgentConfig{
			Name:         name,
			Command:      agCfg.Command,
			Args:         agCfg.Args,
			Cwd:          agCfg.Cwd,
			Env:          agCfg.Env,
			Model:        agCfg.Model,
			SystemPrompt: agCfg.SystemPrompt,
		})
		log.Printf("[agent] created CLI agent: %s (command=%s, type=%s, model=%s)", name, agCfg.Command, agCfg.Type, agCfg.Model)
		return ag
	case "http":
		if agCfg.Endpoint == "" {
			log.Printf("[agent] HTTP agent %q has no endpoint", name)
			return nil
		}
		ag := agent.NewHTTPAgent(agent.HTTPAgentConfig{
			Endpoint:     agCfg.Endpoint,
			APIKey:       agCfg.APIKey,
			Headers:      agCfg.Headers,
			Model:        agCfg.Model,
			SystemPrompt: agCfg.SystemPrompt,
			MaxHistory:   agCfg.MaxHistory,
		})
		log.Printf("[agent] created HTTP agent: %s (endpoint=%s, model=%s)", name, agCfg.Endpoint, agCfg.Model)
		return ag
	default:
		log.Printf("[agent] unknown type %q for %q", agCfg.Type, name)
		return nil
	}
}

// doLogin runs the interactive QR login flow and returns credentials.
func doLogin(ctx context.Context) (*ilink.Credentials, error) {
	fmt.Println("Fetching QR code...")
	qr, err := ilink.FetchQRCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch QR code: %w", err)
	}

	fmt.Println("\nScan this QR code with WeChat:")
	fmt.Println()
	qrterminal.GenerateWithConfig(qr.QRCodeImgContent, qrterminal.Config{
		Level:          qrterminal.L,
		Writer:         os.Stdout,
		HalfBlocks:     true,
		BlackChar:      qrterminal.BLACK_BLACK,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
		WhiteChar:      qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		QuietZone:      1,
	})
	fmt.Printf("\nQR URL: %s\n", qr.QRCodeImgContent)
	fmt.Println("\nWaiting for scan...")

	lastStatus := ""
	creds, err := ilink.PollQRStatus(ctx, qr.QRCode, func(status string) {
		if status != lastStatus {
			lastStatus = status
			switch status {
			case "scaned":
				fmt.Println("QR code scanned! Please confirm on your phone.")
			case "confirmed":
				fmt.Println("Login confirmed!")
			case "expired":
				fmt.Println("QR code expired.")
			}
		}
	})
	if err != nil {
		return nil, err
	}

	if err := ilink.SaveCredentials(creds); err != nil {
		return nil, fmt.Errorf("failed to save credentials: %w", err)
	}
	if err := sendLoginWelcome(ctx, creds); err != nil {
		log.Printf("Login welcome message failed for %s: %v", creds.ILinkBotID, err)
	}

	dir, _ := ilink.CredentialsPath()
	fmt.Printf("\nLogin successful! Credentials saved to %s\n", dir)
	fmt.Printf("Bot ID: %s\n\n", creds.ILinkBotID)
	return creds, nil
}

// --- Daemon mode ---

func weclawDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".weclaw")
}

func pidFile() string {
	return filepath.Join(weclawDir(), "weclaw.pid")
}

func logFile() string {
	return filepath.Join(weclawDir(), "weclaw.log")
}

func writePid(pid int) error {
	if err := os.MkdirAll(weclawDir(), 0o700); err != nil {
		return fmt.Errorf("create weclaw dir: %w", err)
	}
	return os.WriteFile(pidFile(), []byte(fmt.Sprintf("%d", pid)), 0o644)
}

// runDaemon spawns weclaw start (without --daemon) as a background process.
func runDaemon() error {
	if launchAgentInstalled() {
		return startLaunchAgent()
	}
	// Kill any existing weclaw processes before starting a new one
	stopAllWeclaw()

	// Ensure log directory exists
	if err := os.MkdirAll(weclawDir(), 0o700); err != nil {
		return fmt.Errorf("create weclaw dir: %w", err)
	}

	// Open log file
	lf, err := os.OpenFile(logFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	// Re-exec ourselves without --daemon
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}

	cmd := exec.Command(exe, "start", "-f")
	cmd.Stdout = lf
	cmd.Stderr = lf
	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		lf.Close()
		return fmt.Errorf("start daemon: %w", err)
	}

	// Save PID
	pid := cmd.Process.Pid
	if err := writePid(pid); err != nil {
		_ = cmd.Process.Kill()
		lf.Close()
		return err
	}

	// Detach — don't wait
	cmd.Process.Release()
	lf.Close()

	fmt.Printf("weclaw started in background (pid=%d)\n", pid)
	fmt.Printf("Log: %s\n", logFile())
	fmt.Printf("Stop: weclaw stop\n")
	return nil
}

func readPid() (int, error) {
	data, err := os.ReadFile(pidFile())
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		return 0, err
	}
	return pid, nil
}

func processExists(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks if process exists without killing it
	return p.Signal(syscall.Signal(0)) == nil
}

// stopAllWeclaw kills all running weclaw processes (by PID file and by process scan).
func stopAllWeclaw() {
	// 1. Kill by PID file
	if pid, err := readPid(); err == nil && processExists(pid) {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
	}
	os.Remove(pidFile())

	// 2. Kill any remaining weclaw processes by scanning
	exe, err := os.Executable()
	if err != nil {
		return
	}
	// Use pkill to kill all processes matching the executable path
	_ = exec.Command("pkill", "-f", exe+" start").Run()
	time.Sleep(500 * time.Millisecond)
}
