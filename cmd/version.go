package cmd

import (
	"fmt"

	"github.com/datageek/agentdash/internal/version"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print build version, commit, and date",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println(version.Full())
		return nil
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
