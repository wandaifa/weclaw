package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(stopCmd)
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the background weclaw process",
	RunE: func(cmd *cobra.Command, args []string) error {
		if launchAgentInstalled() {
			return stopLaunchAgent()
		}
		stopAllWeclaw()
		fmt.Println("weclaw stopped")
		return nil
	},
}
