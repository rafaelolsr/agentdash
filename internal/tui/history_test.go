package tui

import (
	"strings"
	"testing"

	"github.com/datageek/agentdash/internal/agent"
	"github.com/datageek/agentdash/internal/session"
)

func TestActivityHistoryRecordAndPrune(t *testing.T) {
	h := newActivityHistory()
	a := session.Session{Branch: "feat-a", Activity: agent.ActivityRunning}
	b := session.Session{Branch: "feat-b", Activity: agent.ActivityIdle}

	h.record([]session.Session{a, b})
	h.record([]session.Session{a, b})
	if got := len(h.series[a.Key()]); got != 2 {
		t.Errorf("feat-a series len = %d, want 2", got)
	}

	// feat-b disappears; it should be pruned on the next record.
	h.record([]session.Session{a})
	if _, ok := h.series[b.Key()]; ok {
		t.Error("feat-b should be pruned after disappearing")
	}
}

func TestActivityHistoryCap(t *testing.T) {
	h := newActivityHistory()
	s := session.Session{Branch: "x", Activity: agent.ActivityRunning}
	for i := 0; i < historyLen*2; i++ {
		h.record([]session.Session{s})
	}
	if got := len(h.series[s.Key()]); got != historyLen {
		t.Errorf("series len = %d, want capped at %d", got, historyLen)
	}
}

func TestSparklineRendersBars(t *testing.T) {
	out := renderSparkline([]int{0, 1, 2, 3, 4})
	if strings.TrimSpace(out) == "" {
		t.Fatal("expected non-empty sparkline")
	}
	// Highest sample should render the tallest bar.
	r := []rune(out)
	if r[len(r)-1] != sparkBars[len(sparkBars)-1] {
		t.Errorf("last bar = %q, want %q", string(r[len(r)-1]), string(sparkBars[len(sparkBars)-1]))
	}
}

func TestIntensityOrdering(t *testing.T) {
	if intensity(agent.ActivityRunning) <= intensity(agent.ActivityWaiting) {
		t.Error("active should be more intense than waiting")
	}
	if intensity(agent.ActivityWaiting) <= intensity(agent.ActivityIdle) {
		t.Error("waiting should be more intense than idle")
	}
	if intensity(agent.ActivityExited) != 0 {
		t.Error("exited should be zero intensity")
	}
}

func TestSpinnerGlyphAnimatesOnlyWhenActive(t *testing.T) {
	// Active state cycles through spinner frames.
	g0 := spinnerGlyph(agent.ActivityRunning, 0)
	g1 := spinnerGlyph(agent.ActivityRunning, 1)
	if g0 == g1 {
		t.Error("spinner should advance between frames for active states")
	}
	// Non-active state is static regardless of frame.
	if spinnerGlyph(agent.ActivityIdle, 0) != spinnerGlyph(agent.ActivityIdle, 5) {
		t.Error("idle glyph should not animate")
	}
}
