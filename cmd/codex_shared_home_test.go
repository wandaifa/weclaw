package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureCodexSharedHomeCreatesSymlinkAndMinimalConfig(t *testing.T) {
	realHome := t.TempDir()
	authPath := filepath.Join(realHome, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"fake":"auth"}`), 0o600); err != nil {
		t.Fatalf("seed auth.json: %v", err)
	}

	sharedRoot := t.TempDir()
	sharedHome := filepath.Join(sharedRoot, "codex-shared-home")

	got, err := ensureCodexSharedHomeAt(sharedHome, realHome)
	if err != nil {
		t.Fatalf("ensureCodexSharedHomeAt: %v", err)
	}
	if got != sharedHome {
		t.Fatalf("ensureCodexSharedHomeAt returned %q, want %q", got, sharedHome)
	}

	linkTarget, err := os.Readlink(filepath.Join(sharedHome, "auth.json"))
	if err != nil {
		t.Fatalf("expected auth.json symlink: %v", err)
	}
	if linkTarget != authPath {
		t.Fatalf("auth.json symlink target = %q, want %q", linkTarget, authPath)
	}

	configPath := filepath.Join(sharedHome, "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("expected config.toml to be written: %v", err)
	}
	if !strings.Contains(string(data), "network_access = true") {
		t.Fatalf("config.toml missing network_access = true, got:\n%s", data)
	}

	// AGENTS.md / memories must NOT exist -- that's the entire point of isolation.
	if _, err := os.Stat(filepath.Join(sharedHome, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("codex-shared home must not have an AGENTS.md")
	}
}

func TestEnsureCodexSharedHomeDoesNotOverwriteExistingConfig(t *testing.T) {
	realHome := t.TempDir()
	os.WriteFile(filepath.Join(realHome, "auth.json"), []byte(`{}`), 0o600)

	sharedHome := filepath.Join(t.TempDir(), "codex-shared-home")
	if _, err := ensureCodexSharedHomeAt(sharedHome, realHome); err != nil {
		t.Fatalf("first call: %v", err)
	}

	customized := "# customized by devliang\nmodel = \"gpt-5.6-terra\"\n"
	if err := os.WriteFile(filepath.Join(sharedHome, "config.toml"), []byte(customized), 0o644); err != nil {
		t.Fatalf("customize config.toml: %v", err)
	}

	if _, err := ensureCodexSharedHomeAt(sharedHome, realHome); err != nil {
		t.Fatalf("second call: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(sharedHome, "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	if string(data) != customized {
		t.Fatalf("second call overwrote a customized config.toml, got:\n%s", data)
	}
}

func TestEnsureCodexSharedHomeMissingRealAuthIsNotFatal(t *testing.T) {
	realHome := t.TempDir() // no auth.json seeded
	sharedHome := filepath.Join(t.TempDir(), "codex-shared-home")

	if _, err := ensureCodexSharedHomeAt(sharedHome, realHome); err != nil {
		t.Fatalf("ensureCodexSharedHomeAt should not fail just because auth.json doesn't exist yet: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sharedHome, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("should not create a symlink to a target that doesn't exist")
	}
}
