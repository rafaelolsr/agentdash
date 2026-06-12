package correlate

import (
	"path/filepath"
	"testing"

	"github.com/datageek/agentdash/internal/agent"
	"github.com/datageek/agentdash/internal/gitutil"
	"github.com/datageek/agentdash/internal/worktree"
)

func TestJoinAgentToWorktreeBySubdir(t *testing.T) {
	wtPath := filepath.FromSlash("/repo/wt-a")
	wts := []worktree.Worktree{{RepoPath: "/repo", Path: wtPath, Branch: "feat-a"}}
	// Agent runs in a subdirectory of the worktree.
	ags := []agent.AgentSession{{
		Agent:    "claude",
		CWD:      filepath.Join(wtPath, "internal", "auth"),
		Activity: agent.ActivityRunning,
		Model:    "opus",
	}}

	got := Build(wts, ags)
	if len(got) != 1 {
		t.Fatalf("got %d sessions want 1", len(got))
	}
	if !got[0].Matched {
		t.Fatal("expected agent to be matched to worktree")
	}
	if got[0].Agent != "claude" || got[0].Model != "opus" {
		t.Errorf("agent data not merged: %+v", got[0])
	}
}

func TestUnmatchedAgentAppears(t *testing.T) {
	ags := []agent.AgentSession{{Agent: "claude", CWD: "/elsewhere"}}
	got := Build(nil, ags)
	if len(got) != 1 || got[0].Matched {
		t.Fatalf("expected one unmatched session, got %+v", got)
	}
}

func TestWorktreeWithoutAgentAppears(t *testing.T) {
	wts := []worktree.Worktree{{RepoPath: "/repo", Path: "/repo/wt", Branch: "x"}}
	got := Build(wts, nil)
	if len(got) != 1 || got[0].Matched {
		t.Fatalf("expected one unmatched worktree, got %+v", got)
	}
}

func TestCollisionDetection(t *testing.T) {
	wts := []worktree.Worktree{
		{RepoPath: "/repo", Path: "/repo/a", Branch: "feat-a",
			ChangedFiles: []string{"util.go", "a.go"}, Diff: gitutil.DiffStat{FilesChanged: 2}},
		{RepoPath: "/repo", Path: "/repo/b", Branch: "feat-b",
			ChangedFiles: []string{"util.go", "b.go"}, Diff: gitutil.DiffStat{FilesChanged: 2}},
		{RepoPath: "/repo", Path: "/repo/c", Branch: "feat-c",
			ChangedFiles: []string{"c.go"}},
	}
	got := Build(wts, nil)

	byBranch := map[string][]string{}
	for _, s := range got {
		byBranch[s.Branch] = s.CollidesWith
	}
	if len(byBranch["feat-a"]) != 1 || byBranch["feat-a"][0] != "feat-b" {
		t.Errorf("feat-a collisions = %v, want [feat-b]", byBranch["feat-a"])
	}
	if len(byBranch["feat-b"]) != 1 || byBranch["feat-b"][0] != "feat-a" {
		t.Errorf("feat-b collisions = %v, want [feat-a]", byBranch["feat-b"])
	}
	if len(byBranch["feat-c"]) != 0 {
		t.Errorf("feat-c should not collide, got %v", byBranch["feat-c"])
	}
}

func TestCollisionScopedByRepo(t *testing.T) {
	// Same relative filename in different repos must NOT collide.
	wts := []worktree.Worktree{
		{RepoPath: "/repo1", Path: "/repo1/a", Branch: "a", ChangedFiles: []string{"main.go"}},
		{RepoPath: "/repo2", Path: "/repo2/b", Branch: "b", ChangedFiles: []string{"main.go"}},
	}
	for _, s := range Build(wts, nil) {
		if len(s.CollidesWith) != 0 {
			t.Errorf("%s should not collide across repos, got %v", s.Branch, s.CollidesWith)
		}
	}
}

// TestMultipleAgentsPerWorktree ensures two agents in the SAME worktree each
// get their own session (regression: they used to collapse into one).
func TestMultipleAgentsPerWorktree(t *testing.T) {
	wtPath := filepath.FromSlash("/repo/wt")
	wts := []worktree.Worktree{{RepoPath: "/repo", Path: wtPath, Branch: "main"}}
	ags := []agent.AgentSession{
		{Agent: "copilot", SessionID: "a", CWD: wtPath, SourceFile: "/a.jsonl", Activity: agent.ActivityRunning},
		{Agent: "copilot", SessionID: "b", CWD: wtPath, SourceFile: "/b.jsonl", Activity: agent.ActivityWaiting},
	}
	got := Build(wts, ags)
	copilots := 0
	for _, s := range got {
		if s.Agent == "copilot" {
			copilots++
		}
	}
	if copilots != 2 {
		t.Fatalf("expected 2 copilot sessions in one worktree, got %d (total %d)", copilots, len(got))
	}
}
