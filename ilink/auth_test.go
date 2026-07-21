package ilink

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCredentialsAtSkipsDisabledAccounts(t *testing.T) {
	dir := t.TempDir()
	writeCredentialsFixture(t, dir, Credentials{BotToken: "token-1", ILinkBotID: "enabled@im.bot", ILinkUserID: "scanner-1@im.wechat"})
	writeCredentialsFixture(t, dir, Credentials{BotToken: "token-2", ILinkBotID: "disabled@im.bot", ILinkUserID: "scanner-2@im.wechat"})
	if err := os.WriteFile(filepath.Join(dir, NormalizeAccountID("disabled@im.bot")+".disabled"), []byte("disabled\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	accounts, err := loadAccountsAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	byBot := make(map[string]Account, len(accounts))
	for _, account := range accounts {
		byBot[account.BotID] = account
	}
	if len(accounts) != 2 || byBot["enabled@im.bot"].Disabled || !byBot["disabled@im.bot"].Disabled {
		t.Fatalf("accounts = %+v, want enabled and disabled accounts", accounts)
	}

	credentials, err := loadCredentialsAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 1 || credentials[0].ILinkBotID != "enabled@im.bot" {
		t.Fatalf("loaded credentials = %+v, want only enabled account", credentials)
	}
}

func writeCredentialsFixture(t *testing.T, dir string, creds Credentials) {
	t.Helper()
	data, err := json.Marshal(creds)
	if err != nil {
		t.Fatal(err)
	}
	// Use normalized bot ID for filename to match production behavior
	filename := NormalizeAccountID(creds.ILinkBotID) + ".json"
	if err := os.WriteFile(filepath.Join(dir, filename), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveAccountRequiresDisabledFirst(t *testing.T) {
	dir := t.TempDir()
	writeCredentialsFixture(t, dir, Credentials{BotToken: "token-1", ILinkBotID: "active@im.bot", ILinkUserID: "scanner@im.wechat"})

	if err := removeAccountAt(dir, "active@im.bot"); err == nil {
		t.Fatal("expected error when account is not disabled")
	}
	if _, err := os.Stat(filepath.Join(dir, NormalizeAccountID("active@im.bot")+".json")); err != nil {
		t.Fatalf("credential file should still exist: %v", err)
	}
}

func TestRemoveAccountErrorsWhenNotFound(t *testing.T) {
	dir := t.TempDir()
	if err := removeAccountAt(dir, "missing@im.bot"); err == nil {
		t.Fatal("expected error for a bot_id with no saved credentials")
	}
}

func TestRemoveAccountDeletesFilesAndWritesTombstone(t *testing.T) {
	dir := t.TempDir()
	writeCredentialsFixture(t, dir, Credentials{BotToken: "super-secret-token", ILinkBotID: "gone@im.bot", ILinkUserID: "scanner@im.wechat"})
	id := NormalizeAccountID("gone@im.bot")
	if err := os.WriteFile(filepath.Join(dir, id+".disabled"), []byte("disabled\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := removeAccountAt(dir, "gone@im.bot"); err != nil {
		t.Fatalf("removeAccountAt failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, id+".json")); !os.IsNotExist(err) {
		t.Fatalf("credential file should be gone, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, id+".disabled")); !os.IsNotExist(err) {
		t.Fatalf("disabled marker should be gone, stat err = %v", err)
	}

	tombstonePath := filepath.Join(dir, "deleted", id+".json")
	data, err := os.ReadFile(tombstonePath)
	if err != nil {
		t.Fatalf("tombstone should exist: %v", err)
	}
	if strings.Contains(string(data), "super-secret-token") {
		t.Fatal("tombstone must never contain the bot token")
	}
	var tombstone DeletedAccount
	if err := json.Unmarshal(data, &tombstone); err != nil {
		t.Fatal(err)
	}
	if tombstone.BotID != "gone@im.bot" || tombstone.ILinkUserID != "scanner@im.wechat" || tombstone.RemovedAt == "" {
		t.Fatalf("tombstone = %+v, want populated bot_id/ilink_user_id/removed_at", tombstone)
	}
}

func TestLoadDeletedAccountsAtReturnsTombstones(t *testing.T) {
	dir := t.TempDir()
	deletedDir := filepath.Join(dir, "deleted")
	if err := os.MkdirAll(deletedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, rec DeletedAccount) {
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(deletedDir, name+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("one", DeletedAccount{BotID: "one@im.bot", ILinkUserID: "u1@im.wechat", RemovedAt: "2026-07-22T10:00:00Z"})
	write("two", DeletedAccount{BotID: "two@im.bot", ILinkUserID: "u2@im.wechat", RemovedAt: "2026-07-22T11:00:00Z"})

	records, err := loadDeletedAccountsAt(deletedDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %+v, want 2", records)
	}
}

func TestLoadDeletedAccountsAtHandlesMissingDir(t *testing.T) {
	records, err := loadDeletedAccountsAt(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatal(err)
	}
	if records != nil {
		t.Fatalf("records = %+v, want nil for a directory that was never created", records)
	}
}
