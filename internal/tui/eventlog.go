package tui

import (
	"strconv"
	"time"

	"github.com/datageek/agentdash/internal/agent"
	"github.com/datageek/agentdash/internal/session"
)

// streamEvent is one entry in the fleet activity feed.
type streamEvent struct {
	At      time.Time
	Agent   string
	Project string
	Kind    string // "state" | "collision" | "appeared" | "gone"
	Detail  string // human-readable description
	Color   string // semantic hint: "ok" | "warn" | "err" | "info" | ""
}

const eventLogMax = 200

// eventLog records fleet state transitions across polls so the Stream view can
// show "what just happened". It diffs each poll's sessions against the prior
// snapshot and appends events for meaningful changes.
type eventLog struct {
	events []streamEvent
	prev   map[string]session.Session // session ID → last seen
}

func newEventLog() *eventLog {
	return &eventLog{prev: map[string]session.Session{}}
}

// record diffs the new session set against the previous one and appends events.
// now is passed in because the runtime forbids Date.now-style calls in some
// contexts; here it's just the poll time.
func (l *eventLog) record(sessions []session.Session, now time.Time) {
	seen := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		id := s.ID()
		seen[id] = true
		prev, existed := l.prev[id]

		if !existed {
			// New session appeared — only log agents, not bare worktrees.
			if s.Agent != "" {
				l.add(streamEvent{At: now, Agent: s.Agent, Project: projectLabel(s),
					Kind: "appeared", Detail: "session started", Color: "info"})
			}
		} else {
			// Activity state change.
			if s.Activity != prev.Activity && s.Agent != "" {
				l.add(streamEvent{At: now, Agent: s.Agent, Project: projectLabel(s),
					Kind: "state", Detail: string(prev.Activity) + " → " + string(s.Activity),
					Color: colorForState(s.Activity)})
			}
			// New collision appeared.
			if len(s.CollidesWith) > 0 && len(prev.CollidesWith) == 0 {
				l.add(streamEvent{At: now, Agent: agentOr(s), Project: projectLabel(s),
					Kind: "collision", Detail: "now collides with " + joinShort(s.CollidesWith),
					Color: "err"})
			}
		}
		l.prev[id] = s
	}

	// Sessions that disappeared.
	for id, prev := range l.prev {
		if !seen[id] {
			if prev.Agent != "" {
				l.add(streamEvent{At: now, Agent: prev.Agent, Project: projectLabel(prev),
					Kind: "gone", Detail: "session ended", Color: ""})
			}
			delete(l.prev, id)
		}
	}
}

func (l *eventLog) add(e streamEvent) {
	l.events = append(l.events, e)
	if len(l.events) > eventLogMax {
		l.events = l.events[len(l.events)-eventLogMax:]
	}
}

// recent returns events newest-first, up to n.
func (l *eventLog) recent(n int) []streamEvent {
	out := make([]streamEvent, 0, n)
	for i := len(l.events) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, l.events[i])
	}
	return out
}

func colorForState(a agent.Activity) string {
	switch {
	case a == agent.ActivityWaiting:
		return "warn"
	case a == agent.ActivityExited:
		return ""
	case a.Active():
		return "ok"
	default:
		return ""
	}
}

func agentOr(s session.Session) string {
	if s.Agent != "" {
		return s.Agent
	}
	return "—"
}

func joinShort(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	if len(xs) == 1 {
		return xs[0]
	}
	return xs[0] + " +" + strconv.Itoa(len(xs)-1)
}
