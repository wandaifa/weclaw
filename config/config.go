package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// Config holds the application configuration.
type Config struct {
	DefaultAgent    string                    `json:"default_agent"`
	UserAgents      map[string]string         `json:"user_agents,omitempty"`
	UserPermissions map[string]UserPermission `json:"user_permissions,omitempty"`
	APIAddr         string                    `json:"api_addr,omitempty"`
	SaveDir         string                    `json:"save_dir,omitempty"`
	MessageMerge    MessageMergeConfig        `json:"message_merge,omitempty"`
	MediaRetention  MediaRetentionConfig      `json:"media_retention,omitempty"`
	Agents          map[string]AgentConfig    `json:"agents"`
}

// PermissionLevel is a non-owner user's sandbox tier for the shared codex agent.
type PermissionLevel string

const (
	PermissionReadOnly       PermissionLevel = "read_only"
	PermissionWorkspaceWrite PermissionLevel = "workspace_write"
)

// DefaultDailyMessageLimit applies to non-owner users without a per-user override.
const DefaultDailyMessageLimit = 50

// UserPermission is one non-owner user's configured sandbox tier and daily quota.
type UserPermission struct {
	Level      PermissionLevel `json:"level"`
	DailyLimit int             `json:"daily_limit,omitempty"` // 0 = use DefaultDailyMessageLimit
}

// ParsePermissionLevel validates a level string from the internal permissions API.
func ParsePermissionLevel(s string) (PermissionLevel, error) {
	switch PermissionLevel(s) {
	case PermissionReadOnly, PermissionWorkspaceWrite:
		return PermissionLevel(s), nil
	default:
		return "", fmt.Errorf("unknown permission level %q", s)
	}
}

// MessageMergeConfig controls how consecutive ordinary text messages are combined.
type MessageMergeConfig struct {
	IdleSeconds    int `json:"idle_seconds,omitempty"`
	MaxWaitSeconds int `json:"max_wait_seconds,omitempty"`
	MaxMessages    int `json:"max_messages,omitempty"`
	MaxChars       int `json:"max_chars,omitempty"`
}

// DefaultMessageMergeConfig is intentionally conservative for normal mobile typing.
func DefaultMessageMergeConfig() MessageMergeConfig {
	return MessageMergeConfig{IdleSeconds: 3, MaxWaitSeconds: 10, MaxMessages: 10, MaxChars: 4000}
}

// WithDefaults makes legacy config files (without message_merge) use current defaults.
func (c MessageMergeConfig) WithDefaults() MessageMergeConfig {
	d := DefaultMessageMergeConfig()
	if c.IdleSeconds > 0 {
		d.IdleSeconds = c.IdleSeconds
	}
	if c.MaxWaitSeconds > 0 {
		d.MaxWaitSeconds = c.MaxWaitSeconds
	}
	if c.MaxMessages > 0 {
		d.MaxMessages = c.MaxMessages
	}
	if c.MaxChars > 0 {
		d.MaxChars = c.MaxChars
	}
	return d
}

// MediaRetentionConfig controls how long received media files are kept on
// disk before the daily cleanup job deletes them.
type MediaRetentionConfig struct {
	Days int `json:"days,omitempty"`
}

// DefaultMediaRetentionConfig keeps media for 180 days, covering most
// look-back scenarios while bounding unbounded disk growth.
func DefaultMediaRetentionConfig() MediaRetentionConfig {
	return MediaRetentionConfig{Days: 180}
}

// WithDefaults makes legacy config files (without media_retention) use the default.
func (c MediaRetentionConfig) WithDefaults() MediaRetentionConfig {
	if c.Days > 0 {
		return c
	}
	return DefaultMediaRetentionConfig()
}

// AgentConfig holds configuration for a single agent.
type AgentConfig struct {
	Type         string            `json:"type"`                    // "acp", "cli", or "http"
	Command      string            `json:"command,omitempty"`       // binary path (cli/acp type)
	Args         []string          `json:"args,omitempty"`          // extra args for command (e.g. ["acp"] for cursor)
	Aliases      []string          `json:"aliases,omitempty"`       // custom trigger commands (e.g. ["gpt", "4o"])
	Cwd          string            `json:"cwd,omitempty"`           // working directory (workspace)
	Env          map[string]string `json:"env,omitempty"`           // extra environment variables (cli/acp type)
	Model        string            `json:"model,omitempty"`         // model name
	SystemPrompt string            `json:"system_prompt,omitempty"` // system prompt
	Endpoint     string            `json:"endpoint,omitempty"`      // API endpoint (http type)
	APIKey       string            `json:"api_key,omitempty"`       // API key (http type)
	Headers      map[string]string `json:"headers,omitempty"`       // extra HTTP headers (http type)
	MaxHistory   int               `json:"max_history,omitempty"`   // max history (http type)
}

// BuildAliasMap builds a map from custom alias to agent name from all agent configs.
// It logs warnings for conflicts: duplicate aliases and aliases shadowing agent keys.
func BuildAliasMap(agents map[string]AgentConfig) map[string]string {
	// Built-in commands that cannot be overridden
	reserved := map[string]bool{
		"info": true, "help": true, "new": true, "clear": true, "cwd": true,
	}

	m := make(map[string]string)
	for name, cfg := range agents {
		for _, alias := range cfg.Aliases {
			if reserved[alias] {
				log.Printf("[config] WARNING: alias %q for agent %q conflicts with built-in command, ignored", alias, name)
				continue
			}
			if existing, ok := m[alias]; ok {
				log.Printf("[config] WARNING: alias %q is defined by both %q and %q, using %q", alias, existing, name, name)
			}
			m[alias] = name
		}
	}

	// Warn if a custom alias shadows an agent key
	for alias, target := range m {
		if _, isAgent := agents[alias]; isAgent && alias != target {
			log.Printf("[config] WARNING: alias %q (-> %q) shadows agent key %q", alias, target, alias)
		}
	}

	return m
}

// DefaultConfig returns an empty configuration.
func DefaultConfig() *Config {
	return &Config{
		UserAgents:      make(map[string]string),
		UserPermissions: make(map[string]UserPermission),
		Agents:          make(map[string]AgentConfig),
	}
}

// ConfigPath returns the path to the config file.
func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", "config.json"), nil
}

// Load loads configuration from disk and environment variables.
func Load() (*Config, error) {
	cfg := DefaultConfig()

	path, err := ConfigPath()
	if err != nil {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			loadEnv(cfg)
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]AgentConfig)
	}
	if cfg.UserAgents == nil {
		cfg.UserAgents = make(map[string]string)
	}
	if cfg.UserPermissions == nil {
		cfg.UserPermissions = make(map[string]UserPermission)
	}

	loadEnv(cfg)
	return cfg, nil
}

func loadEnv(cfg *Config) {
	if v := os.Getenv("WECLAW_DEFAULT_AGENT"); v != "" {
		cfg.DefaultAgent = v
	}
	if v := os.Getenv("WECLAW_API_ADDR"); v != "" {
		cfg.APIAddr = v
	}
	if v := os.Getenv("WECLAW_SAVE_DIR"); v != "" {
		cfg.SaveDir = v
	}
}

// Save saves the configuration to disk.
func Save(cfg *Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(path, data, 0o600)
}
