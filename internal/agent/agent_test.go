package agent

import (
	"testing"
	"time"
)

func TestDedupeProcscanSuppressesCoveredSessions(t *testing.T) {
	sessions := []AgentSession{
		// Transcript-based (rich) session.
		{Agent: "claude", CWD: "/repo/a", Model: "opus", SourceFile: "/x.jsonl"},
		// Process-scan duplicate of the same agent + cwd — should drop.
		{Agent: "claude", CWD: "/repo/a", SourceFile: "(process scan)"},
		// Process-scan for a directory no transcript covers — should stay.
		{Agent: "claude", CWD: "/repo/b", SourceFile: "(process scan)"},
		// Different agent, same cwd — not a duplicate, stays.
		{Agent: "codex", CWD: "/repo/a", SourceFile: "(process scan)"},
	}
	got := dedupeProcscan(sessions)
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3: %+v", len(got), got)
	}
	for _, s := range got {
		if s.Agent == "claude" && s.CWD == "/repo/a" && s.SourceFile == "(process scan)" {
			t.Error("process-scan duplicate of covered session was not dropped")
		}
	}
}

func TestTooOld(t *testing.T) {
	now := time.Now()
	if TooOld(now.Add(-time.Hour), now) {
		t.Error("1h-old session should not be too old")
	}
	if !TooOld(now.Add(-48*time.Hour), now) {
		t.Error("48h-old session should be too old")
	}
	if TooOld(time.Time{}, now) {
		t.Error("zero time should not be considered too old")
	}
}
