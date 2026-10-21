package ilink

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Login welcome state is separate from credentials and ignored by account discovery.
// A scanner ID means pending; an empty file means the welcome was delivered.
func MarkLoginWelcomePending(botID, userID string) error {
	if userID == "" {
		return fmt.Errorf("login did not return the scanner's WeChat user ID")
	}
	return writeLoginWelcomeState(botID, userID)
}

func PendingLoginWelcomeUser(botID string) (string, error) {
	dir, err := AccountsDir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, NormalizeAccountID(botID)+".welcome"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func CompleteLoginWelcome(botID string) error { return writeLoginWelcomeState(botID, "") }

func writeLoginWelcomeState(botID, userID string) error {
	if botID == "" {
		return fmt.Errorf("missing bot ID for login welcome")
	}
	dir, err := AccountsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, NormalizeAccountID(botID)+".welcome"), []byte(userID), 0o600)
}
