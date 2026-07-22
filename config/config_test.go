package config

import (
	"encoding/json"
	"testing"
)

func TestAgentConfigUnmarshalEnv(t *testing.T) {
	var cfg Config
	data := []byte(`{
		"agents": {
			"claude": {
				"type": "cli",
				"command": "claude",
				"env": {
					"ANTHROPIC_API_KEY": "test-key",
					"EMPTY": ""
				}
			}
		}
	}`)

	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	ag, ok := cfg.Agents["claude"]
	if !ok {
		t.Fatalf("expected claude agent config")
	}
	if got := ag.Env["ANTHROPIC_API_KEY"]; got != "test-key" {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want %q", got, "test-key")
	}
	if got, ok := ag.Env["EMPTY"]; !ok || got != "" {
		t.Fatalf("EMPTY = %q, present=%v; want empty string present", got, ok)
	}
}

func TestAgentConfigMarshalEnvRoundTrip(t *testing.T) {
	cfg := Config{
		Agents: map[string]AgentConfig{
			"claude": {
				Type:    "cli",
				Command: "claude",
				Env: map[string]string{
					"ANTHROPIC_API_KEY": "test-key",
					"EMPTY":             "",
				},
			},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}

	got := decoded.Agents["claude"].Env
	if got["ANTHROPIC_API_KEY"] != "test-key" || got["EMPTY"] != "" {
		t.Fatalf("round-trip env = %#v", got)
	}
}

func TestAgentConfigWithoutEnvStillLoads(t *testing.T) {
	var cfg Config
	data := []byte(`{
		"agents": {
			"claude": {
				"type": "cli",
				"command": "claude"
			}
		}
	}`)

	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config without env: %v", err)
	}

	if cfg.Agents["claude"].Env != nil {
		t.Fatalf("Env = %#v, want nil", cfg.Agents["claude"].Env)
	}
}

func TestDefaultConfigInitializesMaps(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Agents == nil {
		t.Fatal("DefaultConfig() Agents = nil, want initialized map")
	}
	if cfg.UserAgents == nil {
		t.Fatal("DefaultConfig() UserAgents = nil, want initialized map")
	}
}

func TestConfigUserAgentsRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UserAgents["user-1"] = "claude"

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got := decoded.UserAgents["user-1"]; got != "claude" {
		t.Fatalf("UserAgents[user-1] = %q, want claude", got)
	}
}

func TestLoadEnvOverridesTopLevelOnly(t *testing.T) {
	t.Setenv("WECLAW_DEFAULT_AGENT", "codex")
	t.Setenv("WECLAW_API_ADDR", "127.0.0.1:18011")

	cfg := DefaultConfig()
	cfg.Agents["claude"] = AgentConfig{
		Type: "cli",
		Env: map[string]string{
			"KEEP": "value",
		},
	}

	loadEnv(cfg)

	if cfg.DefaultAgent != "codex" {
		t.Fatalf("DefaultAgent = %q, want %q", cfg.DefaultAgent, "codex")
	}
	if cfg.APIAddr != "127.0.0.1:18011" {
		t.Fatalf("APIAddr = %q, want %q", cfg.APIAddr, "127.0.0.1:18011")
	}
	if got := cfg.Agents["claude"].Env["KEEP"]; got != "value" {
		t.Fatalf("agent env = %q, want preserved value", got)
	}
}

func TestUserPermissionsRoundTrip(t *testing.T) {
	cfg := Config{
		UserPermissions: map[string]UserPermission{
			"someone@im.wechat": {Level: PermissionWorkspaceWrite, DailyLimit: 20},
		},
	}
	data, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Config
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	perm, ok := out.UserPermissions["someone@im.wechat"]
	if !ok {
		t.Fatal("expected someone@im.wechat to round-trip")
	}
	if perm.Level != PermissionWorkspaceWrite || perm.DailyLimit != 20 {
		t.Fatalf("perm = %+v, want workspace_write/20", perm)
	}
}

func TestParsePermissionLevel(t *testing.T) {
	if _, err := ParsePermissionLevel("read_only"); err != nil {
		t.Fatalf("read_only should be valid: %v", err)
	}
	if _, err := ParsePermissionLevel("workspace_write"); err != nil {
		t.Fatalf("workspace_write should be valid: %v", err)
	}
	if _, err := ParsePermissionLevel("full_access"); err == nil {
		t.Fatal("full_access should be rejected — it is reserved for the hardcoded owner, not settable via config")
	}
	if _, err := ParsePermissionLevel("nonsense"); err == nil {
		t.Fatal("unknown level should be rejected")
	}
}

func TestParseAccessMode(t *testing.T) {
	for _, valid := range []string{"public", "allowlist", "owner_only"} {
		if _, err := ParseAccessMode(valid); err != nil {
			t.Fatalf("%q should be valid: %v", valid, err)
		}
	}
	if _, err := ParseAccessMode("nonsense"); err == nil {
		t.Fatal("unknown access mode should be rejected")
	}
}

func TestAccessModeOrDefault(t *testing.T) {
	if got := AccessMode("").OrDefault(); got != AccessPublic {
		t.Fatalf("empty AccessMode.OrDefault() = %q, want public (legacy config files predate access_mode)", got)
	}
	if got := AccessOwnerOnly.OrDefault(); got != AccessOwnerOnly {
		t.Fatalf("AccessOwnerOnly.OrDefault() = %q, want unchanged", got)
	}
}

func TestAccessModeRoundTrip(t *testing.T) {
	cfg := Config{AccessMode: AccessAllowlist}
	data, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Config
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.AccessMode != AccessAllowlist {
		t.Fatalf("AccessMode = %q, want allowlist", out.AccessMode)
	}
}

func TestIsOwner(t *testing.T) {
	for _, id := range OwnerUserIDs() {
		if !IsOwner(id) {
			t.Fatalf("IsOwner(%q) = false, want true", id)
		}
	}
	if IsOwner("random-stranger@im.wechat") {
		t.Fatal("IsOwner should reject arbitrary IDs")
	}
	if IsOwner("") {
		t.Fatal("IsOwner should reject empty string")
	}
	if len(OwnerUserIDs()) != 1 {
		t.Fatalf("OwnerUserIDs() len = %d, want 1", len(OwnerUserIDs()))
	}
}
