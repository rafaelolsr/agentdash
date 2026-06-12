// Package gitutil wraps the git commands AgentDash runs against worktrees and
// parses their porcelain output. All commands are read-only.
package gitutil

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DiffStat summarizes the working-tree changes of a worktree.
type DiffStat struct {
	FilesChanged int
	Insertions   int
	Deletions    int
}

// Commit is a single line of git log output.
type Commit struct {
	Hash    string
	Subject string
}

// run executes a read-only git command inside dir and returns trimmed stdout.
func run(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// runAllowExit runs git and returns stdout even when git exits non-zero (e.g.
// `merge-tree` exits 1 to signal conflicts). Only a failure to launch, a
// timeout, or empty stdout on error is treated as an error.
func runAllowExit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	out, err := cmd.Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok && len(out) > 0 {
			return strings.TrimSpace(string(out)), nil // conflicts: stdout still valid
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CurrentBranch returns the checked-out branch, or empty for a detached HEAD.
func CurrentBranch(dir string) (string, error) {
	return run(dir, "branch", "--show-current")
}

// ChangedFiles returns the set of paths with uncommitted changes (staged or
// unstaged), parsed from `git status --short`.
func ChangedFiles(dir string) ([]string, error) {
	out, err := run(dir, "status", "--short")
	if err != nil {
		return nil, err
	}
	return parseStatusShort(out), nil
}

// parseStatusShort extracts file paths from porcelain v1 short status output.
// It handles renames ("R  old -> new") by taking the destination path.
func parseStatusShort(out string) []string {
	var files []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if len(line) < 4 {
			continue
		}
		// Porcelain v1 short format is "XY PATH": two status columns followed
		// by a separating space, then the path. Strip the 2 status columns and
		// any leading spaces rather than assuming a fixed offset, so paths are
		// never clipped regardless of which status column is set.
		path := strings.TrimLeft(line[2:], " ")
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx+len(" -> "):]
		}
		path = strings.Trim(path, "\"")
		if path != "" {
			files = append(files, path)
		}
	}
	return files
}

// Diff returns the working-tree diff stat (unstaged + staged) for dir.
func Diff(dir string) (DiffStat, error) {
	out, err := run(dir, "diff", "--numstat", "HEAD")
	if err != nil {
		// A repo with no commits has no HEAD; fall back to staged numstat.
		out, err = run(dir, "diff", "--numstat", "--cached")
		if err != nil {
			return DiffStat{}, err
		}
	}
	return parseNumstat(out), nil
}

// parseNumstat sums insertions/deletions and counts files from `git diff
// --numstat` output. Binary files appear as "-\t-\tpath" and count toward
// FilesChanged only.
func parseNumstat(out string) DiffStat {
	var st DiffStat
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		fields := strings.SplitN(sc.Text(), "\t", 3)
		if len(fields) < 3 {
			continue
		}
		st.FilesChanged++
		if n, err := strconv.Atoi(fields[0]); err == nil {
			st.Insertions += n
		}
		if n, err := strconv.Atoi(fields[1]); err == nil {
			st.Deletions += n
		}
	}
	return st
}

// AheadBehind returns how many commits HEAD is ahead of and behind base.
func AheadBehind(dir, base string) (ahead, behind int, err error) {
	out, err := run(dir, "rev-list", "--left-right", "--count", base+"...HEAD")
	if err != nil {
		return 0, 0, err
	}
	// Output: "<behind>\t<ahead>" for base...HEAD (left=base, right=HEAD).
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output: %q", out)
	}
	behind, _ = strconv.Atoi(fields[0])
	ahead, _ = strconv.Atoi(fields[1])
	return ahead, behind, nil
}

// MergesClean reports whether HEAD merges into base without conflicts, using
// `git merge-tree` (which computes the merge in memory without touching the
// working tree or index). Requires git 2.38+ for the --write-tree form; falls
// back to the older form. Returns (clean, conflictingFiles).
func MergesClean(dir, base string) (bool, []string) {
	// Modern form: `git merge-tree --write-tree <base> HEAD`. Exit code 0 =
	// clean; 1 = conflicts; conflicting paths appear in the "Conflicts" section.
	out, err := runAllowExit(dir, "merge-tree", "--write-tree", "--name-only", base, "HEAD")
	if err != nil {
		return false, nil // unknown / old git / unrelated histories — treat as not-clean
	}
	// First line is the tree OID; remaining non-empty lines are conflicted paths.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) <= 1 {
		return true, nil // only the tree OID → no conflicts
	}
	var conflicts []string
	for _, l := range lines[1:] {
		if l = strings.TrimSpace(l); l != "" {
			conflicts = append(conflicts, l)
		}
	}
	return len(conflicts) == 0, conflicts
}

// CommitsSince returns commits on HEAD not in base, newest first.
func CommitsSince(dir, base string) ([]Commit, error) {
	out, err := run(dir, "log", base+"..HEAD", "--oneline", "--no-color")
	if err != nil {
		return nil, err
	}
	return parseOneline(out), nil
}

// LastCommit returns the most recent commit on HEAD.
func LastCommit(dir string) (Commit, error) {
	out, err := run(dir, "log", "-1", "--oneline", "--no-color")
	if err != nil {
		return Commit{}, err
	}
	commits := parseOneline(out)
	if len(commits) == 0 {
		return Commit{}, nil
	}
	return commits[0], nil
}

func parseOneline(out string) []Commit {
	var commits []Commit
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		c := Commit{Hash: parts[0]}
		if len(parts) > 1 {
			c.Subject = parts[1]
		}
		commits = append(commits, c)
	}
	return commits
}

// StatusText returns the raw `git status --short` output for display.
func StatusText(dir string) (string, error) {
	return run(dir, "status", "--short", "--branch")
}

// DiffText returns the working-tree diff (unified) for display. Limited to a
// reasonable size by the caller's viewport; git itself is not truncated here.
func DiffText(dir string) (string, error) {
	out, err := run(dir, "diff", "HEAD")
	if err != nil {
		out, err = run(dir, "diff")
		if err != nil {
			return "", err
		}
	}
	if out == "" {
		return "(no uncommitted changes)", nil
	}
	return out, nil
}

// DetectBase returns the base branch for ahead/behind comparisons. It prefers
// the remote's default branch (origin/HEAD), then a local main/master.
func DetectBase(dir string) string {
	if out, err := run(dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && out != "" {
		return out
	}
	for _, candidate := range []string{"main", "master"} {
		if _, err := run(dir, "rev-parse", "--verify", "--quiet", candidate); err == nil {
			return candidate
		}
	}
	return "HEAD"
}
