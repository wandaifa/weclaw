package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/fastclaw-ai/weclaw/api"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/spf13/cobra"
)

var accountBotID string
var accountRemoveYes bool

func init() {
	accountsCmd.AddCommand(accountsDisableCmd, accountsEnableCmd, accountsRemoveCmd)
	accountsDisableCmd.Flags().StringVar(&accountBotID, "bot", "", "Bot ID to disable")
	accountsEnableCmd.Flags().StringVar(&accountBotID, "bot", "", "Bot ID to enable")
	accountsRemoveCmd.Flags().StringVar(&accountBotID, "bot", "", "Bot ID to remove")
	accountsRemoveCmd.Flags().BoolVar(&accountRemoveYes, "yes", false, "Skip interactive confirmation")
	rootCmd.AddCommand(accountsCmd)
}

var accountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "List local WeChat Bot accounts and their loading state",
	RunE: func(cmd *cobra.Command, args []string) error {
		accounts, err := ilink.LoadAccounts()
		if err != nil {
			return fmt.Errorf("load accounts: %w", err)
		}
		active, serviceOnline, err := activeBotStatuses(cmd.Context())
		if err != nil {
			return err
		}

		out := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(out, "BOT ID\t扫码者 ID\t状态")
		for _, account := range accounts {
			status := "已启用"
			if account.Disabled {
				status = "已停用"
			} else if serviceOnline && active[account.BotID] != "" {
				switch active[account.BotID] {
				case "active":
					status = "在线"
				case "expired":
					status = "会话已失效"
				default:
					status = "启动中"
				}
			} else if serviceOnline {
				status = "未加载"
			}
			fmt.Fprintf(out, "%s\t%s\t%s\n", account.BotID, account.ILinkUserID, status)
		}
		out.Flush()
		if !serviceOnline {
			fmt.Println("服务未运行；上表仅展示本地启用/停用状态。")
		}

		deleted, err := ilink.LoadDeletedAccounts()
		if err != nil {
			return fmt.Errorf("load deleted accounts: %w", err)
		}
		if len(deleted) > 0 {
			fmt.Println()
			deletedOut := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(deletedOut, "已删除账号")
			fmt.Fprintln(deletedOut, "BOT ID\t扫码者 ID\t删除时间")
			for _, d := range deleted {
				fmt.Fprintf(deletedOut, "%s\t%s\t%s\n", d.BotID, d.ILinkUserID, d.RemovedAt)
			}
			deletedOut.Flush()
		}
		return nil
	},
}

var accountsDisableCmd = &cobra.Command{
	Use:   "disable --bot <bot_id>",
	Short: "Disable one Bot without deleting its credentials",
	RunE: func(cmd *cobra.Command, args []string) error {
		return setAccountEnabled(cmd.Context(), accountBotID, false)
	},
}

var accountsEnableCmd = &cobra.Command{
	Use:   "enable --bot <bot_id>",
	Short: "Enable one previously disabled Bot",
	RunE: func(cmd *cobra.Command, args []string) error {
		return setAccountEnabled(cmd.Context(), accountBotID, true)
	},
}

var accountsRemoveCmd = &cobra.Command{
	Use:   "remove --bot <bot_id>",
	Short: "Permanently delete one disabled Bot's credentials (irreversible)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return removeAccount(cmd.Context(), accountBotID, accountRemoveYes)
	},
}

func removeAccount(ctx context.Context, botID string, skipConfirm bool) error {
	if botID == "" {
		return fmt.Errorf("--bot is required")
	}
	if !skipConfirm {
		fmt.Printf("此操作不可恢复：将永久删除 Bot %s 的凭证。输入完整 bot_id 确认：\n", botID)
		var typed string
		if _, err := fmt.Scanln(&typed); err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		if typed != botID {
			return fmt.Errorf("输入不匹配，已取消")
		}
	}
	if err := ilink.RemoveAccount(botID); err != nil {
		return err
	}
	running, err := reloadConfiguredAccounts(ctx)
	if err != nil {
		return fmt.Errorf("Bot %s 已删除，但热重载失败：%w；请执行 weclaw restart", botID, err)
	}
	if running {
		fmt.Printf("Bot %s 已永久删除，并已更新运行中的服务。\n", botID)
	} else {
		fmt.Printf("Bot %s 已永久删除。\n", botID)
	}
	return nil
}

func setAccountEnabled(ctx context.Context, botID string, enabled bool) error {
	if botID == "" {
		return fmt.Errorf("--bot is required")
	}
	accounts, err := ilink.LoadAccounts()
	if err != nil {
		return fmt.Errorf("load accounts: %w", err)
	}
	found := false
	for _, account := range accounts {
		if account.BotID == botID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("bot_id %q is not saved locally", botID)
	}
	if err := ilink.SetAccountDisabled(botID, !enabled); err != nil {
		return err
	}

	verb := "停用"
	if enabled {
		verb = "启用"
	}
	running, err := reloadConfiguredAccounts(ctx)
	if err != nil {
		return fmt.Errorf("Bot %s 已%s，但热重载失败：%w；请执行 weclaw restart", botID, verb, err)
	}
	if running {
		fmt.Printf("Bot %s 已%s，并已更新运行中的服务。\n", botID, verb)
	} else {
		fmt.Printf("Bot %s 已%s；下次启动服务时生效。\n", botID, verb)
	}
	return nil
}

func reloadConfiguredAccounts(ctx context.Context) (bool, error) {
	cfg, err := config.Load()
	if err != nil {
		return false, err
	}
	return reloadAccountsAt(ctx, cfg.APIAddr)
}

func activeBotStatuses(ctx context.Context) (map[string]string, bool, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, false, err
	}
	endpoint, err := localAPIURL(cfg.APIAddr, api.AccountsPath)
	if err != nil {
		return nil, false, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var netErr *net.OpError
		if errors.As(err, &netErr) {
			return map[string]string{}, false, nil
		}
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, true, fmt.Errorf("account status request returned %s", resp.Status)
	}
	var payload struct {
		Accounts []api.AccountStatus `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, true, err
	}
	active := make(map[string]string, len(payload.Accounts))
	for _, account := range payload.Accounts {
		active[account.BotID] = account.Status
	}
	return active, true, nil
}
