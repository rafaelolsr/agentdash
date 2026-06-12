// Package procscan implements a generic, agent-agnostic fallback adapter that
// discovers running AI coding agents by scanning the process table and reading
// each matching process's working directory.
//
// It guarantees that any known agent (cursor, grok, opencode, …) appears in the
// dashboard even before a dedicated transcript adapter exists. The trade-off is
// limited data: a working directory and "alive" status, but no model, task, or
// working/waiting distinction.
package procscan

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/datageek/agentdash/internal/agent"
)

func init() { agent.Register(New()) }

// Adapter discovers agents via process scan.
type Adapter struct{}

// New returns a process-scan adapter.
func New() *Adapter { return &Adapter{} }

// Name implements agent.AgentAdapter.
func (a *Adapter) Name() string { return "procscan" }

// knownAgents maps a substring found in a process command to the agent name we
// report it as. Matched case-insensitively against the full command. Needles
// are chosen to be specific to CLI agents and avoid unrelated apps that merely
// contain the word (e.g. Microsoft Teams' "copilot" helper).
var knownAgents = []struct{ needle, agent string }{
	{"opencode", "opencode"},
	{"@anthropic-ai/claude", "claude"},
	{"claude-code", "claude"},
	{"/claude", "claude"},
	{"codex", "codex"},
	{"@github/copilot", "copilot"},
	{"copilot-language-server", "copilot"},
	{"kimi", "kimi"},
	{"grok", "grok"},
	{"cursor-agent", "cursor"},
	{"aider", "aider"},
}

// Discover scans processes and returns one session per (agent, working
// directory) pair. Multiple helper processes of a single agent invocation share
// a working directory and so collapse into one session. Sessions whose working
// directory is not a plausible project (root or the home directory) are
// dropped, since they cannot be correlated to a worktree.
func (a *Adapter) Discover() ([]agent.AgentSession, error) {
	procs, err := listProcesses()
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	now := time.Now()

	// Deduplicate by (agent, cwd); keep the lowest pid as the representative.
	seen := map[string]agent.AgentSession{}
	for _, p := range procs {
		name := classify(p.command)
		if name == "" {
			continue
		}
		cwd := processCWD(p.pid)
		if !plausibleProjectDir(cwd, home) {
			continue
		}
		cwd = agent.Resolve(cwd)
		key := name + "\x00" + cwd
		if existing, ok := seen[key]; ok {
			if p.pid < existing.PID {
				existing.PID = p.pid
				seen[key] = existing
			}
			continue
		}
		seen[key] = agent.AgentSession{
			Agent:      name,
			SessionID:  name + "@" + cwd,
			CWD:        cwd,
			PID:        p.pid,
			Activity:   agent.ActivityRunning, // a live process is actively executing
			LastActive: now,
			SourceFile: "(process scan)",
		}
	}

	sessions := make([]agent.AgentSession, 0, len(seen))
	for _, s := range seen {
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// nonProjectDirs are path substrings that indicate a working directory is not a
// user project checkout — app bundles, system dirs, caches. A "claude" process
// running here is a desktop-app helper, not a coding session.
var nonProjectDirs = []string{
	"/.app/", ".app/contents/", "/applications/", "/library/",
	"/system/", "/private/var/", "/usr/", "/tmp/", "/.cache/",
	"/node_modules/", "/.vscode/", "/.cursor/",
}

// plausibleProjectDir rejects working directories that cannot be a project
// checkout: empty, filesystem root, the home directory itself, or paths inside
// app bundles / system locations.
func plausibleProjectDir(cwd, home string) bool {
	switch cwd {
	case "", "/", ".":
		return false
	}
	if home != "" && cwd == home {
		return false
	}
	lc := strings.ToLower(cwd)
	for _, bad := range nonProjectDirs {
		if strings.Contains(lc, bad) {
			return false
		}
	}
	return true
}

func classify(command string) string {
	lc := strings.ToLower(command)
	for _, k := range knownAgents {
		if strings.Contains(lc, k.needle) {
			return k.agent
		}
	}
	return ""
}

type proc struct {
	pid     int
	command string
}

// listProcesses returns running processes via `ps`. Works on macOS and Linux.
func listProcesses() ([]proc, error) {
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		// Some Linux ps variants prefer "args" over "command".
		out, err = exec.Command("ps", "-axo", "pid=,args=").Output()
		if err != nil {
			return nil, err
		}
	}
	var procs []proc
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		procs = append(procs, proc{pid: pid, command: strings.TrimSpace(fields[1])})
	}
	return procs, nil
}

// processCWD returns the working directory of a pid, or "" if unavailable.
// Uses lsof, which works without elevated privileges for the user's own procs
// on both macOS and Linux.
func processCWD(pid int) string {
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "n"); ok {
			return filepath.Clean(rest)
		}
	}
	return ""
}
