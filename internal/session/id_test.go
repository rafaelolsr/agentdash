package session

import "testing"

// Two sessions sharing branch "HEAD" must have distinct IDs (the bug that made
// metrics/history caches collide). Key may collide; ID must not.
func TestIDUniquenessAcrossSharedBranch(t *testing.T) {
	a := Session{Agent: "claude", Branch: "HEAD", SourceFile: "/a.jsonl"}
	b := Session{Agent: "claude", Branch: "HEAD", SourceFile: "/b.jsonl"}

	if a.Key() != b.Key() {
		t.Skip("keys unexpectedly differ; test premise invalid")
	}
	if a.ID() == b.ID() {
		t.Errorf("IDs collided for distinct sessions: %q", a.ID())
	}
}

func TestIDFallbacks(t *testing.T) {
	// No transcript → falls back to agent+sessionID.
	s := Session{Agent: "codex", SessionID: "abc", SourceFile: "(process scan)"}
	if s.ID() != "codex:abc" {
		t.Errorf("ID = %q, want codex:abc", s.ID())
	}
	// Nothing but a worktree.
	s2 := Session{Agent: "x", WorktreePath: "/repo/wt"}
	if s2.ID() != "x@/repo/wt" {
		t.Errorf("ID = %q, want x@/repo/wt", s2.ID())
	}
}
