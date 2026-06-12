package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// bar renders a horizontal meter of the given cell width: filled cells in
// `fill` color, the remainder in a dim track. ratio is clamped to [0,1].
func bar(ratio float64, width int, fill lipgloss.Color) string {
	if width < 1 {
		width = 1
	}
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	on := int(ratio*float64(width) + 0.5)
	if on > width {
		on = width
	}
	track := lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
	return lipgloss.NewStyle().Foreground(fill).Render(strings.Repeat("█", on)) +
		track.Render(strings.Repeat("░", width-on))
}

// churnBar renders a proportional +/- split across width cells: green for
// insertions, red for deletions, sized by their share of the total.
func churnBar(ins, del, width int) string {
	if width < 1 {
		width = 1
	}
	total := ins + del
	if total == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("237")).Render(strings.Repeat("░", width))
	}
	g := int(float64(ins)/float64(total)*float64(width) + 0.5)
	if g > width {
		g = width
	}
	r := width - g
	return okStyle.Render(strings.Repeat("█", g)) + lipgloss.NewStyle().Foreground(colErr).Render(strings.Repeat("█", r))
}

// stackedBar renders segments of given counts in given colors across width
// cells, proportional to the total. Trailing remainder uses the last color.
func stackedBar(counts []int, colors []lipgloss.Color, width int) string {
	if width < 1 {
		width = 1
	}
	total := 0
	for _, c := range counts {
		total += c
	}
	if total == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("237")).Render(strings.Repeat("░", width))
	}
	var b strings.Builder
	used := 0
	for i, c := range counts {
		seg := int(float64(c)/float64(total)*float64(width) + 0.5)
		if i == len(counts)-1 {
			seg = width - used // last segment fills the rest
		}
		if seg < 0 {
			seg = 0
		}
		if used+seg > width {
			seg = width - used
		}
		b.WriteString(lipgloss.NewStyle().Foreground(colors[i]).Render(strings.Repeat("█", seg)))
		used += seg
	}
	return b.String()
}

// gauge renders an intensity series (0–4 per sample) as a braille-ish bar
// sparkline in the given color, capped to width samples (most recent kept).
func gauge(series []int, width int, fill lipgloss.Color) string {
	bars := []rune{'▁', '▁', '▂', '▄', '▆', '█'}
	if len(series) == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("237")).Render(strings.Repeat("▁", width))
	}
	if len(series) > width {
		series = series[len(series)-width:]
	}
	out := make([]rune, len(series))
	for i, v := range series {
		if v < 0 {
			v = 0
		}
		if v >= len(bars) {
			v = len(bars) - 1
		}
		out[i] = bars[v]
	}
	return lipgloss.NewStyle().Foreground(fill).Render(string(out))
}

// pill renders a filled status badge: white text on the activity's color.
func pill(label string, c lipgloss.Color) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(c).Padding(0, 1).Render(label)
}
