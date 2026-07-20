package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/spf13/cobra"
)

var (
	sendBotID    string
	sendTo       string
	sendText     string
	sendMediaURL string
)

func init() {
	sendCmd.Flags().StringVar(&sendBotID, "bot", "", "Sending Bot ID (required when multiple accounts are configured)")
	sendCmd.Flags().StringVar(&sendTo, "to", "", "Target user ID (ilink user ID)")
	sendCmd.Flags().StringVar(&sendText, "text", "", "Message text to send")
	sendCmd.Flags().StringVar(&sendMediaURL, "media", "", "Media URL to send (image/video/file)")
	sendCmd.MarkFlagRequired("to")
	rootCmd.AddCommand(sendCmd)
}

var sendCmd = &cobra.Command{
	Use:   "send",
	Short: "Send a message to a WeChat user",
	Example: `  weclaw send --bot "bot_id@im.bot" --to "user_id@im.wechat" --text "Hello"
  weclaw send --bot "bot_id@im.bot" --to "user_id@im.wechat" --media "https://example.com/image.png"
  weclaw send --bot "bot_id@im.bot" --to "user_id@im.wechat" --text "See this" --media "https://example.com/image.png"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if sendText == "" && sendMediaURL == "" {
			return fmt.Errorf("at least one of --text or --media is required")
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		accounts, err := ilink.LoadAllCredentials()
		if err != nil {
			return fmt.Errorf("load credentials: %w", err)
		}
		if len(accounts) == 0 {
			return fmt.Errorf("no accounts found, run 'weclaw start' first")
		}

		clients := make([]*ilink.Client, 0, len(accounts))
		for _, account := range accounts {
			clients = append(clients, ilink.NewClient(account))
		}
		client, err := ilink.SelectClient(clients, sendBotID)
		if err != nil {
			return err
		}

		if sendText != "" {
			if err := messaging.SendTextReply(ctx, client, sendTo, sendText, "", ""); err != nil {
				return fmt.Errorf("send text failed: %w", err)
			}
			fmt.Println("Text sent")
		}

		if sendMediaURL != "" {
			if err := messaging.SendMediaFromURL(ctx, client, sendTo, sendMediaURL, ""); err != nil {
				return fmt.Errorf("send media failed: %w", err)
			}
			fmt.Println("Media sent")
		}

		return nil
	},
}
