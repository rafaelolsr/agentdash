package tui

import "github.com/datageek/agentdash/internal/agent"

// spinnerFrames is a braille spinner cycle, advanced on the fast UI tick.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerGlyph returns the animated spinner frame for active sessions, or the
// session's static activity glyph otherwise. frame is a free-running counter.
func spinnerGlyph(a agent.Activity, frame int) string {
	if a.Active() {
		return spinnerFrames[frame%len(spinnerFrames)]
	}
	return a.Glyph()
}
