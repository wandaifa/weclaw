package ilink

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	qrCodeURL       = "https://ilinkai.weixin.qq.com/ilink/bot/get_bot_qrcode?bot_type=3"
	qrStatusURL     = "https://ilinkai.weixin.qq.com/ilink/bot/get_qrcode_status?qrcode="
	statusWait      = "wait"
	statusScanned   = "scaned"
	statusConfirmed = "confirmed"
	statusExpired   = "expired"
)

// FetchQRCode retrieves a new QR code for login.
func FetchQRCode(ctx context.Context) (*QRCodeResponse, error) {
	c := NewUnauthenticatedClient()
	var resp QRCodeResponse
	if err := c.doGet(ctx, qrCodeURL, &resp); err != nil {
		return nil, fmt.Errorf("fetch QR code: %w", err)
	}
	return &resp, nil
}

// PollQRStatus polls for QR code scan status until confirmed or expired.
// It calls onStatus for each status change so the caller can display progress.
func PollQRStatus(ctx context.Context, qrcode string, onStatus func(status string)) (*Credentials, error) {
	c := NewUnauthenticatedClient()
	url := qrStatusURL + qrcode

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		pollCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
		var resp QRStatusResponse
		err := c.doGet(pollCtx, url, &resp)
		cancel()

		if err != nil {
			// Timeout is normal for long-poll, retry
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}

		if onStatus != nil {
			onStatus(resp.Status)
		}

		switch resp.Status {
		case statusConfirmed:
			creds := &Credentials{
				BotToken:    resp.BotToken,
				ILinkBotID:  resp.ILinkBotID,
				BaseURL:     resp.BaseURL,
				ILinkUserID: resp.ILinkUserID,
			}
			return creds, nil
		case statusExpired:
			return nil, fmt.Errorf("QR code expired")
		case statusWait, statusScanned:
			// Continue polling
		default:
			// Unknown status, continue
		}
	}
}

// AccountsDir returns the directory where account credentials are stored.
func AccountsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", "accounts"), nil
}

// NormalizeAccountID converts raw bot ID to filesystem-safe format.
func NormalizeAccountID(raw string) string {
	s := raw
	for _, ch := range []string{"@", ".", ":"} {
		s = filepath.Clean(s)
		s = replaceAll(s, ch, "-")
	}
	return s
}

func replaceAll(s, old, new string) string {
	for {
		i := indexOf(s, old)
		if i < 0 {
			return s
		}
		s = s[:i] + new + s[i+len(old):]
	}
}

func indexOf(s, sub string) int {
	for i := range s {
		if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// SaveCredentials saves credentials to disk under ~/.weclaw/accounts/{id}.json.
func SaveCredentials(creds *Credentials) error {
	dir, err := AccountsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create accounts dir: %w", err)
	}

	id := NormalizeAccountID(creds.ILinkBotID)
	path := filepath.Join(dir, id+".json")

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}

// Account describes a saved Bot credential and its local loading state.
// Tokens are intentionally not exposed by this type.
type Account struct {
	BotID       string
	ILinkUserID string
	Disabled    bool
}

// DisabledAccountPath returns the local marker path used to keep a Bot from
// being loaded. The credential itself is never removed by disabling an account.
func DisabledAccountPath(botID string) (string, error) {
	dir, err := AccountsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, NormalizeAccountID(botID)+".disabled"), nil
}

// SetAccountDisabled changes only the local loading marker for a saved Bot.
func SetAccountDisabled(botID string, disabled bool) error {
	if botID == "" {
		return fmt.Errorf("bot ID is required")
	}
	path, err := DisabledAccountPath(botID)
	if err != nil {
		return err
	}
	if disabled {
		if err := os.WriteFile(path, []byte("disabled\n"), 0o600); err != nil {
			return fmt.Errorf("disable account: %w", err)
		}
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("enable account: %w", err)
	}
	return nil
}

// LoadAccounts returns every saved account, including locally disabled ones.
func LoadAccounts() ([]Account, error) {
	dir, err := AccountsDir()
	if err != nil {
		return nil, err
	}
	return loadAccountsAt(dir)
}

// LoadAllCredentials loads all saved account credentials.
func LoadAllCredentials() ([]*Credentials, error) {
	dir, err := AccountsDir()
	if err != nil {
		return nil, err
	}
	return loadCredentialsAt(dir)
}

func loadCredentialsAt(dir string) ([]*Credentials, error) {
	accounts, err := loadAccountsWithCredentialsAt(dir)
	if err != nil {
		return nil, err
	}
	result := make([]*Credentials, 0, len(accounts))
	for _, account := range accounts {
		if !account.Disabled {
			result = append(result, account.credentials)
		}
	}
	return result, nil
}

type accountWithCredentials struct {
	Account
	credentials *Credentials
}

func loadAccountsAt(dir string) ([]Account, error) {
	accounts, err := loadAccountsWithCredentialsAt(dir)
	if err != nil {
		return nil, err
	}
	result := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		result = append(result, account.Account)
	}
	return result, nil
}

func loadAccountsWithCredentialsAt(dir string) ([]accountWithCredentials, error) {

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read accounts dir: %w", err)
	}

	var result []accountWithCredentials
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var creds Credentials
		if json.Unmarshal(data, &creds) == nil && creds.BotToken != "" && creds.ILinkBotID != "" {
			disabledPath := filepath.Join(dir, NormalizeAccountID(creds.ILinkBotID)+".disabled")
			_, disabledErr := os.Stat(disabledPath)
			result = append(result, accountWithCredentials{
				Account:     Account{BotID: creds.ILinkBotID, ILinkUserID: creds.ILinkUserID, Disabled: disabledErr == nil},
				credentials: &creds,
			})
		}
	}
	return result, nil
}

// CredentialsPath returns the path for display purposes.
func CredentialsPath() (string, error) {
	return AccountsDir()
}
