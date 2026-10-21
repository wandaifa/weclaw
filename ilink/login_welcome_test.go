package ilink

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoginWelcomeStatePersistsWithoutChangingCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	creds := &Credentials{ILinkBotID: "test@im.bot", ILinkUserID: "scanner", BotToken: "test-token"}
	if err := SaveCredentials(creds); err != nil {
		t.Fatal(err)
	}
	dir, _ := AccountsDir()
	path := filepath.Join(dir, NormalizeAccountID(creds.ILinkBotID)+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := MarkLoginWelcomePending(creds.ILinkBotID, creds.ILinkUserID); err != nil {
		t.Fatal(err)
	}
	user, err := PendingLoginWelcomeUser(creds.ILinkBotID)
	if err != nil || user != "scanner" {
		t.Fatalf("pending=%q err=%v", user, err)
	}
	accounts, err := loadAccountsAt(dir)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("state interfered with account discovery: %v %v", accounts, err)
	}
	if err := CompleteLoginWelcome(creds.ILinkBotID); err != nil {
		t.Fatal(err)
	}
	user, err = PendingLoginWelcomeUser(creds.ILinkBotID)
	if err != nil || user != "" {
		t.Fatalf("completed=%q err=%v", user, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("credentials changed")
	}
}

func TestLoginWelcomeRequiresScannerAndBot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if MarkLoginWelcomePending("bot", "") == nil {
		t.Fatal("empty scanner accepted")
	}
	if MarkLoginWelcomePending("", "scanner") == nil {
		t.Fatal("empty bot accepted")
	}
}
