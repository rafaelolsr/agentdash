package tui

import (
	"testing"
	"time"

	"github.com/datageek/agentdash/internal/agent"
	"github.com/datageek/agentdash/internal/session"
)

func TestEventLogTransitions(t *testing.T) {
	l := newEventLog()
	t0 := time.Unix(1_700_000_000, 0)
	s := session.Session{Agent: "claude", RepoPath: "/x/proj", Activity: agent.ActivityRunning, SourceFile: "/a.jsonl"}

	// First sighting → "appeared".
	l.record([]session.Session{s}, t0)
	// State change → one "state" event.
	s.Activity = agent.ActivityWaiting
	l.record([]session.Session{s}, t0.Add(time.Minute))
	// New collision → "collision" event.
	s.CollidesWith = []string{"feat-x"}
	l.record([]session.Session{s}, t0.Add(2*time.Minute))
	// Disappears → "gone".
	l.record(nil, t0.Add(3*time.Minute))

	kinds := map[string]int{}
	for _, e := range l.events {
		kinds[e.Kind]++
	}
	for _, want := range []string{"appeared", "state", "collision", "gone"} {
		if kinds[want] == 0 {
			t.Errorf("expected a %q event, got events: %+v", want, l.events)
		}
	}
	// recent() returns newest first.
	r := l.recent(10)
	if len(r) > 1 && r[0].At.Before(r[1].At) {
		t.Error("recent() should be newest-first")
	}
}

func TestEventLogNoSpuriousEvents(t *testing.T) {
	l := newEventLog()
	t0 := time.Unix(1_700_000_000, 0)
	s := session.Session{Agent: "codex", RepoPath: "/x/p", Activity: agent.ActivityRunning, SourceFile: "/c.jsonl"}
	l.record([]session.Session{s}, t0)
	before := len(l.events)
	// Same state again → no new event.
	l.record([]session.Session{s}, t0.Add(time.Minute))
	if len(l.events) != before {
		t.Errorf("unchanged session produced %d new events", len(l.events)-before)
	}
}
