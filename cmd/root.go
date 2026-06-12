// Package cmd defines the AgentDash command-line interface.
package cmd

import (
	"github.com/datageek/agentdash/internal/config"
	"github.com/datageek/agentdash/internal/tui"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "agentdash",
	Short: "Monitor local AI coding agent sessions from one dashboard",
	Long: `AgentDash is a read-only terminal dashboard that watches your local AI
coding agents (Claude Code, Copilot CLI, and others) and correlates each
running session with the git worktree it is working in.

Run 'agentdash init' once to create the config, then run 'agentdash' to open
the dashboard.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	// With no subcommand, launch the dashboard.
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return tui.Run(cfg)
	},
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}
