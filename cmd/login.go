package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fastclaw-ai/weclaw/api"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(loginCmd)
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Add a WeChat account via QR code scan",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		creds, err := doLogin(ctx)
		if err != nil {
			return err
		}

		cfg, cfgErr := config.Load()
		if cfgErr != nil {
			fmt.Printf("Account %s added. Run 'weclaw restart' to load it.\n", creds.ILinkBotID)
			return nil
		}
		running, reloadErr := reloadAccountsAt(ctx, cfg.APIAddr)
		switch {
		case reloadErr != nil:
			fmt.Printf("Account %s added, but hot reload failed: %v\n", creds.ILinkBotID, reloadErr)
			fmt.Println("Run 'weclaw restart' to recover.")
		case running:
			fmt.Printf("Account %s added and loaded by the running service.\n", creds.ILinkBotID)
		default:
			fmt.Printf("Account %s added. Run 'weclaw start' to begin.\n", creds.ILinkBotID)
		}
		return nil
	},
}

func reloadAccountsAt(ctx context.Context, addr string) (bool, error) {
	endpoint, err := localAPIURL(addr, api.AccountReloadPath)
	if err != nil {
		return false, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, nil)
	if err != nil {
		return false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var netErr *net.OpError
		if errors.As(err, &netErr) {
			return false, nil
		}
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return true, fmt.Errorf("service returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return true, nil
}

func localAPIURL(addr, path string) (string, error) {
	if addr == "" {
		addr = "127.0.0.1:18011"
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	parsed, err := url.Parse(addr)
	if err != nil {
		return "", err
	}
	host := parsed.Hostname()
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	port := parsed.Port()
	if port == "" {
		return "", fmt.Errorf("API address %q has no port", addr)
	}
	parsed.Scheme = "http"
	parsed.Host = net.JoinHostPort(host, port)
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}
