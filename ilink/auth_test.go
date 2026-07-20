package ilink

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCredentialsAtSkipsDisabledAccounts(t *testing.T) {
	dir := t.TempDir()
	writeCredentialsFixture(t, dir, "enabled", Credentials{BotToken: "token-1", ILinkBotID: "enabled@im.bot", ILinkUserID: "scanner-1@im.wechat"})
	writeCredentialsFixture(t, dir, "disabled", Credentials{BotToken: "token-2", ILinkBotID: "disabled@im.bot", ILinkUserID: "scanner-2@im.wechat"})
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

func writeCredentialsFixture(t *testing.T, dir, name string, creds Credentials) {
	t.Helper()
	data, err := json.Marshal(creds)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
