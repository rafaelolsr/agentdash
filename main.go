// Command agentdash is a read-only terminal dashboard that monitors local AI
// coding agent sessions (Claude Code, Copilot CLI, and others) and correlates
// each running agent with the git worktree it is working in.
package main

import (
	"fmt"
	"os"

	"github.com/datageek/agentdash/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "agentdash:", err)
		os.Exit(1)
	}
}
