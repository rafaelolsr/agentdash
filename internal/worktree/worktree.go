// Package worktree discovers git worktrees for the configured repositories and
// collects their git status. It is agent-agnostic: a worktree appears whether
// or not an agent is running in it.
package worktree

import (
	"bufio"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/datageek/agentdash/internal/gitutil"
)

// Worktree is the git-side view of one checkout. Activity correlation attaches
// agent data later; this struct contains only what git can tell us.
type Worktree struct {
	RepoPath      string
	Path          string // absolute, symlink-resolved worktree path (join key)
	Branch        string
	Base          string
	ChangedFiles  []string
	Diff          gitutil.DiffStat
	Ahead         int
	Behind        int
	Commits       []gitutil.Commit
	LastCommit    gitutil.Commit
	Mergeable     bool     // HEAD merges into base without conflict
	MergeChecked  bool     // whether a mergeability check ran (vs unknown)
	ConflictFiles []string // files that conflict with base, if any
}

// List returns the worktrees of repoPath via `git worktree list --porcelain`.
func List(repoPath string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseWorktreeList(string(out)), nil
}

// parseWorktreeList extracts worktree paths from porcelain output. Each record
// begins with a "worktree <path>" line.
func parseWorktreeList(out string) []string {
	var paths []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "worktree "); ok {
			if resolved, err := filepath.EvalSymlinks(rest); err == nil {
				paths = append(paths, resolved)
			} else {
				paths = append(paths, rest)
			}
		}
	}
	return paths
}

// Collect gathers full git status for a single worktree. base may be empty, in
// which case it is auto-detected.
func Collect(repoPath, wtPath, base string) Worktree {
	if base == "" {
		base = gitutil.DetectBase(wtPath)
	}
	wt := Worktree{RepoPath: repoPath, Path: wtPath, Base: base}

	wt.Branch, _ = gitutil.CurrentBranch(wtPath)
	wt.ChangedFiles, _ = gitutil.ChangedFiles(wtPath)
	wt.Diff, _ = gitutil.Diff(wtPath)
	wt.LastCommit, _ = gitutil.LastCommit(wtPath)

	if base != "HEAD" {
		wt.Ahead, wt.Behind, _ = gitutil.AheadBehind(wtPath, base)
		wt.Commits, _ = gitutil.CommitsSince(wtPath, base)
		// Mergeability only matters when the branch is ahead of base.
		if wt.Ahead > 0 {
			wt.Mergeable, wt.ConflictFiles = gitutil.MergesClean(wtPath, base)
			wt.MergeChecked = true
		}
	}
	return wt
}

// CollectAll discovers and collects every worktree across the given repos.
// bases maps a repo path to its configured base branch (optional).
func CollectAll(repos []string, bases map[string]string) []Worktree {
	var all []Worktree
	for _, repo := range repos {
		paths, err := List(repo)
		if err != nil {
			// A bad repo path should not sink the whole scan.
			continue
		}
		for _, p := range paths {
			all = append(all, Collect(repo, p, bases[repo]))
		}
	}
	return all
}
