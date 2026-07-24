package cmd

import (
	"fmt"
	"os"
	"path/filepath"
)

// codexSharedConfigTOML is deliberately minimal: this is NOT a copy of the
// owner's real ~/.codex/config.toml (that would defeat the isolation this
// whole feature exists for -- see docs/superpowers/specs/
// 2026-07-24-codex-user-isolation-design.md). network_access=true is the
// one setting that's functionally required (image generation needs it);
// everything else uses codex's own defaults, and model/reasoning-effort
// tier is controlled separately via the codex-shared agent's own config.json
// entry (ACPAgentConfig.Model/ModelReasoningEffort), not this file.
const codexSharedConfigTOML = `[sandbox_workspace_write]
network_access = true
`

// ensureCodexSharedHome creates (idempotently) the isolated CODEX_HOME used
// by the codex-shared agent under ~/.weclaw/codex-shared-home, and returns
// its path. realCodexHome is the owner's real CODEX_HOME (normally
// ~/.codex) -- only auth.json is symlinked from there, so login stays in
// sync with token rotation while AGENTS.md/memories/config.toml stay fully
// isolated.
func ensureCodexSharedHome(realCodexHome string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	sharedHome := filepath.Join(home, ".weclaw", "codex-shared-home")
	return ensureCodexSharedHomeAt(sharedHome, realCodexHome)
}

// ensureCodexSharedHomeAt is ensureCodexSharedHome with an explicit target
// path, split out so tests don't have to touch the real home directory.
func ensureCodexSharedHomeAt(sharedHome, realCodexHome string) (string, error) {
	if err := os.MkdirAll(sharedHome, 0o700); err != nil {
		return "", fmt.Errorf("create codex-shared home: %w", err)
	}

	authLink := filepath.Join(sharedHome, "auth.json")
	realAuth := filepath.Join(realCodexHome, "auth.json")
	if _, err := os.Lstat(authLink); os.IsNotExist(err) {
		if _, statErr := os.Stat(realAuth); statErr == nil {
			if linkErr := os.Symlink(realAuth, authLink); linkErr != nil {
				return "", fmt.Errorf("symlink auth.json: %w", linkErr)
			}
		}
		// If realAuth doesn't exist yet (fresh install, not logged in),
		// skip silently -- codex-shared just won't be authenticated until
		// the owner logs in and this function runs again on next restart.
	} else if err != nil {
		return "", fmt.Errorf("check existing auth.json symlink: %w", err)
	}

	configPath := filepath.Join(sharedHome, "config.toml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.WriteFile(configPath, []byte(codexSharedConfigTOML), 0o644); err != nil {
			return "", fmt.Errorf("write codex-shared config.toml: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("check existing config.toml: %w", err)
	}

	return sharedHome, nil
}
