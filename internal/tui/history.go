package tui

import (
	"github.com/datageek/agentdash/internal/agent"
	"github.com/datageek/agentdash/internal/session"
)

// historyLen is how many samples (poll cycles) the sparkline shows.
const historyLen = 24

// activityHistory keeps a rolling per-session series of activity intensity,
// sampled once per poll, so the TUI can draw a sparkline of recent activity.
// In-memory only — it reflects the current run, not past runs.
type activityHistory struct {
	series map[string][]int
}

func newActivityHistory() *activityHistory {
	return &activityHistory{series: map[string][]int{}}
}

// record appends one intensity sample per session and prunes sessions that have
// disappeared so the map does not grow unbounded.
func (h *activityHistory) record(sessions []session.Session) {
	seen := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		key := s.ID()
		seen[key] = true
		series := append(h.series[key], intensity(s.Activity))
		if len(series) > historyLen {
			series = series[len(series)-historyLen:]
		}
		h.series[key] = series
	}
	for key := range h.series {
		if !seen[key] {
			delete(h.series, key)
		}
	}
}

// sparkline returns a braille bar chart of a session's recent activity.
func (h *activityHistory) sparkline(key string) string {
	series := h.series[key]
	if len(series) == 0 {
		return ""
	}
	return renderSparkline(series)
}

// intensity maps an activity state to a 0–4 bar height: idle/exited are flat,
// waiting is low, active states are high.
func intensity(a agent.Activity) int {
	switch {
	case a.Active():
		return 4
	case a == agent.ActivityWaiting:
		return 2
	case a == agent.ActivityIdle:
		return 1
	default: // exited / unknown
		return 0
	}
}

var sparkBars = []rune{' ', '▁', '▃', '▅', '▇'}

func renderSparkline(series []int) string {
	out := make([]rune, len(series))
	for i, v := range series {
		if v < 0 {
			v = 0
		}
		if v >= len(sparkBars) {
			v = len(sparkBars) - 1
		}
		out[i] = sparkBars[v]
	}
	return string(out)
}
