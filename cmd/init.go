package cmd

import (
	"fmt"

	"github.com/datageek/agentdash/internal/config"
	"github.com/spf13/cobra"
)

var initForce bool

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create the AgentDash config and data directories",
	Long: `init creates ~/.agentdash/config.yaml along with the sessions/ and
history/ directories. Edit config.yaml to add the repositories whose worktrees
you want to monitor.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := config.ResolvePaths()
		if err != nil {
			return err
		}

		if _, statErr := config.Load(); statErr == nil && !initForce {
			return fmt.Errorf("config already exists at %s (use --force to overwrite)", p.ConfigFile)
		}

		if err := config.Save(config.Default()); err != nil {
			return err
		}

		fmt.Printf("Initialized AgentDash.\n")
		fmt.Printf("  config:   %s\n", p.ConfigFile)
		fmt.Printf("  sessions: %s\n", p.SessionsDir)
		fmt.Printf("  history:  %s\n", p.HistoryDir)
		fmt.Printf("\nNext: edit config.yaml and add repositories under 'repos:', e.g.\n\n")
		fmt.Printf("  repos:\n    - ~/code/my-project\n\nThen run 'agentdash' to open the dashboard.\n")
		return nil
	},
}

func init() {
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite an existing config")
	rootCmd.AddCommand(initCmd)
}
