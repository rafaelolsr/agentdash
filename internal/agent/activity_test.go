package agent

import (
	"testing"
	"time"
)

func TestStateForTool(t *testing.T) {
	cases := []struct {
		tool string
		want Activity
	}{
		{"Read", ActivityReading},
		{"Glob", ActivityReading},
		{"Read_file_v2", ActivityReading}, // Cursor
		{"Edit", ActivityWriting},
		{"Write", ActivityWriting},
		{"apply_patch", ActivityWriting}, // Codex
		{"Bash", ActivityRunning},
		{"shell", ActivityRunning},
		{"Grep", ActivitySearching},
		{"WebFetch", ActivityBrowsing},
		{"Task", ActivitySpawning},
		{"SomethingUnknown", ActivityThinking}, // unfinished tool ⇒ still working
	}
	for _, c := range cases {
		if got := StateForTool(c.tool); got != c.want {
			t.Errorf("StateForTool(%q) = %s, want %s", c.tool, got, c.want)
		}
	}
}

func TestCombineActivity(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-2 * time.Second)
	grace := now.Add(-20 * time.Second)
	cool := now.Add(-2 * time.Minute)
	gone := now.Add(-10 * time.Minute)

	// A fresh active state is trusted as-is.
	if got := CombineActivity(ActivityRunning, fresh, now); got != ActivityRunning {
		t.Errorf("fresh running = %s, want running", got)
	}
	// Within the 30s grace, still trusted.
	if got := CombineActivity(ActivityWriting, grace, now); got != ActivityWriting {
		t.Errorf("grace writing = %s, want writing", got)
	}
	// After the grace but not stale, an active state cools to idle.
	if got := CombineActivity(ActivityReading, cool, now); got != ActivityIdle {
		t.Errorf("cool reading = %s, want idle", got)
	}
	// Waiting is preserved while not stale.
	if got := CombineActivity(ActivityWaiting, cool, now); got != ActivityWaiting {
		t.Errorf("cool waiting = %s, want waiting", got)
	}
	// Stale always collapses to exited regardless of content.
	if got := CombineActivity(ActivityRunning, gone, now); got != ActivityExited {
		t.Errorf("stale running = %s, want exited", got)
	}
}

func TestActivityActive(t *testing.T) {
	if !ActivityWriting.Active() {
		t.Error("writing should be active")
	}
	if ActivityWaiting.Active() || ActivityIdle.Active() || ActivityExited.Active() {
		t.Error("waiting/idle/exited should not be active")
	}
}
