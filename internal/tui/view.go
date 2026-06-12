package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/datageek/agentdash/internal/agent"
	"github.com/datageek/agentdash/internal/session"
	"github.com/datageek/agentdash/internal/version"
)

func homeDir() (string, error) { return os.UserHomeDir() }

var (
	colSubtle = lipgloss.Color("240")
	colAccent = lipgloss.Color("69")
	colWarn   = lipgloss.Color("214")
	colOK     = lipgloss.Color("78")
	colErr    = lipgloss.Color("203")

	colUser = lipgloss.Color("75")  // blue — the human
	colTool = lipgloss.Color("245") // grey — tool calls

	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	labelStyle    = lipgloss.NewStyle().Foreground(colSubtle)
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(colAccent)
	warnStyle     = lipgloss.NewStyle().Foreground(colWarn)
	okStyle       = lipgloss.NewStyle().Foreground(colOK)
	borderStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colSubtle)
	helpStyle     = lipgloss.NewStyle().Foreground(colSubtle)

	convText = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
)

func (m model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	if !m.loaded {
		return "scanning for agent sessions…"
	}

	header := m.renderHeader()
	bodyHeight := m.height - 3 // header + help line

	// Overlays (detail / conversation / diff / …) take the full body.
	if m.overlay != overlayNone {
		content := borderStyle.Width(m.width - 2).Height(bodyHeight).Render(m.renderOverlay(m.width-4, bodyHeight))
		return lipgloss.JoinVertical(lipgloss.Left, header, content, m.renderHelp())
	}

	// Board view is full-width; enter opens detail as an overlay.
	if m.view == viewBoard {
		board := boxStyle(true).Width(m.width - 2).Height(bodyHeight).PaddingLeft(1).Render(m.renderBoard(m.width - 4))
		return lipgloss.JoinVertical(lipgloss.Left, header, board, m.renderHelp())
	}

	// Stream view: full-width reverse-chronological fleet event feed.
	if m.view == viewStream {
		stream := boxStyle(true).Width(m.width - 2).Height(bodyHeight).PaddingLeft(1).Render(m.renderStream(m.width-4, bodyHeight-2))
		return lipgloss.JoinVertical(lipgloss.Left, header, stream, m.renderHelp())
	}

	// Fleet view: two boxes — left = data-viz fleet, right = session detail.
	leftWidth := m.width * 9 / 20
	if leftWidth < 40 {
		leftWidth = 40
	}
	rightWidth := m.width - leftWidth - 4
	leftFocused := !m.focusDetail
	rightFocused := m.focusDetail

	left := boxStyle(leftFocused).Width(leftWidth).Height(bodyHeight).PaddingLeft(1).Render(m.renderFleet(leftWidth - 4))
	right := boxStyle(rightFocused).Width(rightWidth).Height(bodyHeight).Render(m.renderRight(rightWidth-2, bodyHeight-2))

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, m.renderHelp())
}

// boardCols defines the five kanban columns: title + accent color.
var boardCols = []struct {
	title string
	color lipgloss.Color
}{
	{"NEEDS INPUT", colWarn},              // BucketNeedsInput
	{"WORKING", colOK},                    // BucketWorking
	{"⚠ COLLIDING", colErr},               // BucketColliding
	{"READY MERGE", lipgloss.Color("75")}, // BucketReadyMerge
	{"DONE / IDLE", colSubtle},            // BucketDone
}

// renderBoard draws the kanban: columns by what the supervisor must do.
func (m model) renderBoard(width int) string {
	if len(m.visible) == 0 {
		return m.renderEmptyState(width, 0)
	}
	cols := m.boardColumns()
	n := len(boardCols)
	colW := (width-(n-1))/n - 1
	if colW < 12 {
		colW = 12
	}

	rendered := make([]string, n)
	for c := 0; c < n; c++ {
		var b strings.Builder
		hdr := lipgloss.NewStyle().Foreground(boardCols[c].color).Bold(true).Render(boardCols[c].title)
		b.WriteString(hdr + " " + labelStyle.Render(fmt.Sprintf("%d", len(cols[c]))) + "\n")
		b.WriteString(lipgloss.NewStyle().Foreground(boardCols[c].color).Render(strings.Repeat("─", colW)) + "\n\n")
		for _, idx := range cols[c] {
			b.WriteString(m.renderBoardCard(m.visible[idx], idx == m.cursor, colW) + "\n")
		}
		rendered[c] = lipgloss.NewStyle().Width(colW).Render(b.String())
	}

	// Join columns with a thin vertical gutter.
	gap := lipgloss.NewStyle().Foreground(lipgloss.Color("237")).Render(" │ ")
	out := rendered[0]
	for c := 1; c < n; c++ {
		out = lipgloss.JoinHorizontal(lipgloss.Top, out, gap, rendered[c])
	}
	return out
}

// renderStream draws the live fleet event feed, newest first.
func (m model) renderStream(width, height int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("ACTIVITY STREAM") + "  " +
		labelStyle.Render("newest first · events since launch") + "\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("237")).Render(strings.Repeat("─", width)) + "\n\n")

	rows := height - 3
	if rows < 1 {
		rows = 1
	}
	events := m.eventLog.recent(rows)
	if len(events) == 0 {
		b.WriteString(labelStyle.Render("  Watching the fleet… events appear as agents change state,") + "\n")
		b.WriteString(labelStyle.Render("  start, collide, or finish. Leave AgentDash running.") + "\n")
		return b.String()
	}

	for _, e := range events {
		clock := lipgloss.NewStyle().Foreground(colSubtle).Render(humanAge(e.At))
		who := lipgloss.NewStyle().Foreground(agentColor(e.Agent)).Bold(true).Render(fmt.Sprintf("%-8s", truncate(e.Agent, 8)))
		proj := labelStyle.Render(truncate(e.Project, 22))
		detail := streamColor(e.Color).Render(truncate(e.Detail, width-44))
		b.WriteString(fmt.Sprintf("  %-9s %s %-24s %s\n", clock, who, proj, detail))
	}
	return b.String()
}

// streamColor maps an event color hint to a style.
func streamColor(c string) lipgloss.Style {
	switch c {
	case "ok":
		return okStyle
	case "warn":
		return warnStyle
	case "err":
		return lipgloss.NewStyle().Foreground(colErr)
	case "info":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	}
}

// renderBoardCard renders one agent card in a board column.
func (m model) renderBoardCard(s session.Session, selected bool, width int) string {
	ac := activityColor(s.Activity)
	marker := "  "
	if selected {
		marker = lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render("▸ ")
	}
	name := s.Agent
	if name == "" {
		name = "—"
	}
	head := marker + lipgloss.NewStyle().Foreground(agentColor(s.Agent)).Bold(true).Render(truncate(name, width-3))

	var sub string
	switch s.Board() {
	case session.BucketColliding:
		who := strings.Join(s.CollidesWith, ", ")
		sub = warnStyle.Render(truncate(firstChangedFile(s), width-2)) + "\n  " +
			labelStyle.Render(truncate("vs "+who, width-2))
	case session.BucketWorking:
		sub = labelStyle.Render(gauge(m.history.series[s.ID()], 5, ac)) + " " +
			labelStyle.Render(truncate(s.Metrics.LastTool, width-8))
	case session.BucketNeedsInput:
		sub = labelStyle.Render(truncate("replied "+humanAgeShort(s.LastActive), width-2))
	case session.BucketReadyMerge:
		sub = lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Render(
			truncate(fmt.Sprintf("↑%d clean", s.Ahead), width-2))
	default:
		sub = labelStyle.Render(truncate(string(s.Activity), width-2))
	}
	proj := labelStyle.Render("  " + truncate(projectLabel(s), width-2))
	return head + "\n" + proj + "\n  " + sub + "\n"
}

func firstChangedFile(s session.Session) string {
	if len(s.ConflictFiles) > 0 {
		return baseName2(s.ConflictFiles[0])
	}
	if len(s.ChangedFiles) > 0 {
		return baseName2(s.ChangedFiles[0])
	}
	return "shared files"
}

func baseName2(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func humanAgeShort(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return humanAge(t)
}

// boxColor is the strong yellow panel outline.
var boxColor = lipgloss.Color("214")

// boxStyle returns the panel border style: a thick yellow outline, brighter
// when focused.
func boxStyle(focused bool) lipgloss.Style {
	s := lipgloss.NewStyle().Border(lipgloss.ThickBorder()).BorderForeground(lipgloss.Color("136"))
	if focused {
		s = s.BorderForeground(boxColor)
	}
	return s
}

// renderFleet draws the left box: a fleet summary, then per-session rows that
// lead with an activity gauge and a smart second meter.
func (m model) renderFleet(width int) string {
	if len(m.visible) == 0 {
		return m.renderEmptyState(width, 0)
	}
	var b strings.Builder

	// Summary: stacked state bar + aggregates across the visible fleet.
	working, waiting, idle := 0, 0, 0
	var tokTotal int
	ins, del, maxTok := 0, 0, 1
	for _, s := range m.visible {
		switch {
		case s.Activity.Active():
			working++
		case s.Activity == agent.ActivityWaiting:
			waiting++
		default:
			idle++
		}
		tok := s.Metrics.TokensIn + s.Metrics.TokensOut
		tokTotal += tok
		if tok > maxTok {
			maxTok = tok
		}
		ins += s.Diff.Insertions
		del += s.Diff.Deletions
	}
	_ = maxTok
	summary := fmt.Sprintf("%s  %s  %s",
		okStyle.Render(fmt.Sprintf("%d working", working)),
		warnStyle.Render(fmt.Sprintf("%d waiting", waiting)),
		labelStyle.Render(fmt.Sprintf("%d idle", idle)))
	agg := fmt.Sprintf("%s tok", humanCount(tokTotal))
	if ins+del > 0 {
		agg += fmt.Sprintf("  ·  %s/%s", okStyle.Render(fmt.Sprintf("+%d", ins)), warnStyle.Render(fmt.Sprintf("-%d", del)))
	}
	b.WriteString(titleStyle.Render("FLEET") + "  " + summary + "\n")
	b.WriteString(labelStyle.Render(agg) + "\n\n")

	// Sessions grouped by project. m.visible stays the flat cursor order; we
	// insert a project header whenever the group changes.
	lastProject := "\x00"
	for i, s := range m.visible {
		proj := projectLabel(s)
		if proj != lastProject {
			lastProject = proj
			b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(boxColor).Render("◆ "+truncate(proj, width-2)) + "\n")
		}

		ac := activityColor(s.Activity)
		marker := "  "
		nameStyle := lipgloss.NewStyle().Foreground(agentColor(s.Agent)).Bold(true)
		if i == m.cursor {
			marker = lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render("▸ ")
		}
		glyph := lipgloss.NewStyle().Foreground(ac).Render(spinnerGlyph(s.Activity, m.spinnerFrame))
		agentName := s.Agent
		if agentName == "" {
			agentName = "—"
		}
		// Agent row: marker glyph agent  branch/worktree … full status word.
		statusTxt := lipgloss.NewStyle().Foreground(ac).Bold(i == m.cursor).Render(string(s.Activity))
		sub := s.Branch
		if wt := worktreeLabel(s); wt != "" {
			sub = wt // non-primary worktree is more informative than the branch
		}
		left := marker + glyph + " " + nameStyle.Render(truncate(agentName, 12))
		if sub != "" {
			left += "  " + labelStyle.Render(truncate(sub, width-26))
		}
		head := padBetween(left, statusTxt, width)
		b.WriteString(head + "\n")

		// Activity-trend line: a sparkline of recent intensity, with a label.
		series := m.history.series[s.ID()]
		line := activityTrend(series, 18, ac)
		trendLabel := trendNote(series)
		b.WriteString("    " + line + "  " + labelStyle.Render(trendLabel) + "\n")
	}
	return b.String()
}

// activityTrend renders a connected line-graph of intensity samples using
// Braille-style cells, padded to width samples (most recent kept).
func activityTrend(series []int, width int, fill lipgloss.Color) string {
	glyphs := []rune{'⣀', '⣀', '⣤', '⣶', '⣿', '⣿'} // low→high density
	if width < 1 {
		width = 1
	}
	out := make([]rune, width)
	// Right-align the series; pad the front with the baseline.
	pad := width - len(series)
	for i := 0; i < width; i++ {
		var v int
		if i >= pad {
			v = series[i-pad]
		}
		if v < 0 {
			v = 0
		}
		if v >= len(glyphs) {
			v = len(glyphs) - 1
		}
		out[i] = glyphs[v]
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
	// Color the filled (recent) portion; dim the leading baseline.
	if pad > 0 && pad < width {
		return dim.Render(string(out[:pad])) + lipgloss.NewStyle().Foreground(fill).Render(string(out[pad:]))
	}
	return lipgloss.NewStyle().Foreground(fill).Render(string(out))
}

// trendNote describes the recent activity trend in a few words.
func trendNote(series []int) string {
	if len(series) == 0 {
		return "no activity yet"
	}
	last := series[len(series)-1]
	// Compare recent half vs earlier half to detect rising/falling.
	if len(series) >= 4 {
		mid := len(series) / 2
		early, late := avg(series[:mid]), avg(series[mid:])
		switch {
		case last == 0:
			return "gone quiet"
		case late > early+0.5:
			return "ramping up"
		case late < early-0.5:
			return "winding down"
		default:
			return "steady"
		}
	}
	if last == 0 {
		return "idle"
	}
	return "active"
}

func avg(xs []int) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0
	for _, x := range xs {
		sum += x
	}
	return float64(sum) / float64(len(xs))
}

// renderRight draws the right box: detail fields (most of the room) plus a
// short conversation preview at the bottom.
// rightMaxScroll returns the maximum scroll offset for the right box given the
// current terminal size, so the key handler can clamp downward scrolling.
func (m model) rightMaxScroll() int {
	if m.width == 0 {
		return 0
	}
	bodyHeight := m.height - 3
	leftWidth := m.width * 9 / 20
	if leftWidth < 40 {
		leftWidth = 40
	}
	rightWidth := m.width - leftWidth - 4
	// Render unscrolled to count lines (scroll=0 path returns full content when
	// it fits, or a window otherwise — so count via a scroll-agnostic build).
	content := m.renderRightContent(rightWidth-2, bodyHeight-2)
	lines := strings.Count(content, "\n") + 1
	view := (bodyHeight - 2) - 1
	if view < 1 {
		view = 1
	}
	if lines <= view {
		return 0
	}
	return lines - view
}

// renderRightContent builds the full (unscrolled) right-box content with the
// left gutter applied. renderRight handles the scroll window.
func (m model) renderRightContent(width, height int) string {
	s, ok := m.selected()
	if !ok {
		return labelStyle.Render("Select a session.")
	}
	const gutter = 2
	w := width - gutter*2
	var b strings.Builder

	// Title bar: project name + status, above the detail fields.
	ac := activityColor(s.Activity)
	titleProj := lipgloss.NewStyle().Bold(true).Foreground(boxColor).Render(truncate(projectLabel(s), w-14))
	titleStatus := lipgloss.NewStyle().Foreground(ac).Bold(true).Render(string(s.Activity))
	b.WriteString(padBetween(titleProj, titleStatus, w) + "\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("237")).Render(strings.Repeat("─", w)) + "\n\n")

	b.WriteString(m.renderDeck(w))

	// Conversation (messages only) → Recent tools → Git → Activity.
	b.WriteString("\n" + section("Conversation", w))
	b.WriteString(m.previewBlock(s, w))
	b.WriteString("\n" + section("Recent tools", w))
	b.WriteString(m.toolsBlock(w))
	b.WriteString("\n" + m.renderGitActivity(w))

	// Left gutter on every line.
	pad := strings.Repeat(" ", gutter)
	lines := strings.Split(b.String(), "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

func (m model) renderRight(width, height int) string {
	content := m.renderRightContent(width, height)
	lines := strings.Split(content, "\n")

	if height < 1 {
		height = 1
	}
	if len(lines) <= height {
		return content
	}
	// Scroll window: a slice starting at detailScroll, with a hint line.
	view := height - 1
	if view < 1 {
		view = 1
	}
	maxScroll := len(lines) - view
	scroll := m.detailScroll
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	window := lines[scroll : scroll+view]
	more := ""
	if scroll > 0 {
		more += "↑"
	}
	if scroll < maxScroll {
		more += "↓"
	}
	hint := fmt.Sprintf("  %s %d–%d/%d", more, scroll+1, scroll+view, len(lines))
	if !m.focusDetail {
		hint += "   (tab to scroll)"
	}
	return strings.Join(window, "\n") + "\n" + helpStyle.Render(hint)
}

// previewBlock renders the short conversation preview — messages only, no tool
// calls (those live in the Recent tools section).
func (m model) previewBlock(s session.Session, width int) string {
	if !s.HasTranscript() {
		return labelStyle.Render("  (no transcript for this session)") + "\n"
	}
	// Only show the preview if it actually belongs to THIS session; otherwise
	// the async fetch for the new selection hasn't returned yet.
	if m.previewKey != s.ID() {
		return labelStyle.Render("  …loading conversation") + "\n"
	}
	msgs := messagesOnly(m.previewEvents)
	if len(msgs) == 0 {
		return labelStyle.Render("  (no recent messages — press l for full view)") + "\n"
	}
	var b strings.Builder
	if len(msgs) > 5 {
		msgs = msgs[len(msgs)-5:]
	}
	agentClr := agentColor(m.selectedAgent())
	for _, ev := range msgs {
		label := m.selectedAgent()
		lstyle := lipgloss.NewStyle().Foreground(agentClr).Bold(true)
		if ev.Role == "user" {
			label = "you"
			lstyle = lipgloss.NewStyle().Foreground(colUser).Bold(true)
		}
		b.WriteString("  " + lstyle.Render(fmt.Sprintf("%-7s", label)) + " " + convText.Render(truncate(ev.Text, width-12)) + "\n")
	}
	b.WriteString(labelStyle.Render("  press l for full conversation") + "\n")
	return b.String()
}

// toolsBlock renders the Recent tools section: the most recent tool calls.
func (m model) toolsBlock(width int) string {
	// Guard: only show tools that belong to the currently-selected session.
	if s, ok := m.selected(); !ok || m.previewKey != s.ID() {
		return labelStyle.Render("  …loading") + "\n"
	}
	tools := toolsOnly(m.previewEvents)
	if len(tools) == 0 {
		return labelStyle.Render("  (no recent tool calls)") + "\n"
	}
	if len(tools) > 6 {
		tools = tools[len(tools)-6:]
	}
	var b strings.Builder
	for _, ev := range tools {
		b.WriteString("  " + lipgloss.NewStyle().Foreground(colUser).Render(truncate(ev.Text, width-4)) + "\n")
	}
	return b.String()
}

func messagesOnly(evs []agent.Event) []agent.Event {
	var out []agent.Event
	for _, e := range evs {
		if e.Role == "user" || e.Role == "assistant" {
			out = append(out, e)
		}
	}
	return out
}

func toolsOnly(evs []agent.Event) []agent.Event {
	var out []agent.Event
	for _, e := range evs {
		if e.Role == "tool" && e.Kind == "tool_use" {
			out = append(out, e)
		}
	}
	return out
}

// --- card grid layout ---

const (
	cardWidth  = 34 // inner content width of a card
	cardOuter  = cardWidth + 2
	cardHeight = 5 // inner content lines
	cardHGap   = 1
)

// gridCols returns how many cards fit across the given total width.
func gridCols(width int) int {
	cols := (width + cardHGap) / (cardOuter + cardHGap)
	if cols < 1 {
		cols = 1
	}
	if cols > 4 {
		cols = 4
	}
	return cols
}

// renderGrid lays out session cards in a wrapping grid and clips to bodyHeight.
func (m model) renderGrid(width, bodyHeight int) string {
	if len(m.visible) == 0 {
		return m.renderEmptyState(width, bodyHeight)
	}
	cols := gridCols(width)

	var rows []string
	for i := 0; i < len(m.visible); i += cols {
		var cells []string
		for c := 0; c < cols && i+c < len(m.visible); c++ {
			idx := i + c
			cells = append(cells, m.renderCard(m.visible[idx], idx == m.cursor))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, joinWithGap(cells, cardHGap)...))
	}

	// Each card row is cardHeight+2 (border) tall; clip to the visible body.
	maxRows := bodyHeight / (cardHeight + 2)
	if maxRows < 1 {
		maxRows = 1
	}
	// Keep the cursor's row visible by scrolling whole rows.
	cursorRow := m.cursor / cols
	start := 0
	if cursorRow >= maxRows {
		start = cursorRow - maxRows + 1
	}
	end := start + maxRows
	if end > len(rows) {
		end = len(rows)
	}
	return strings.Join(rows[start:end], "\n")
}

// joinWithGap inserts n spaces of gap between rendered blocks.
func joinWithGap(cells []string, n int) []string {
	if n <= 0 || len(cells) <= 1 {
		return cells
	}
	gap := strings.Repeat(" ", n)
	out := make([]string, 0, len(cells)*2-1)
	for i, c := range cells {
		if i > 0 {
			out = append(out, gap)
		}
		out = append(out, c)
	}
	return out
}

// renderCard renders one session as a bordered tile.
func (m model) renderCard(s session.Session, selected bool) string {
	ac := activityColor(s.Activity)

	// Header: spinner/glyph + agent (left) … status (right).
	glyph := spinnerGlyph(s.Activity, m.spinnerFrame)
	agentName := s.Agent
	if agentName == "" {
		agentName = "—"
	}
	statusTxt := lipgloss.NewStyle().Foreground(ac).Render(string(s.Activity))
	head := padBetween(
		lipgloss.NewStyle().Foreground(ac).Render(glyph+" "+agentName),
		statusTxt, cardWidth)

	// Project name (bold).
	project := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252")).
		Render(truncate(projectLabel(s), cardWidth))

	// Sparkline of recent activity.
	spark := labelStyle.Render(lastRunes(m.history.sparkline(s.ID()), cardWidth))

	// Stats row: diff + messages + collision flag.
	var stats []string
	if s.Diff.FilesChanged > 0 || s.Diff.Insertions > 0 || s.Diff.Deletions > 0 {
		stats = append(stats, fmt.Sprintf("%df %s %s",
			s.Diff.FilesChanged,
			okStyle.Render(fmt.Sprintf("+%d", s.Diff.Insertions)),
			warnStyle.Render(fmt.Sprintf("-%d", s.Diff.Deletions))))
	}
	if n := s.Metrics.UserMsgs + s.Metrics.AsstMsgs; n > 0 {
		stats = append(stats, labelStyle.Render(humanCount(n)+" msg"))
	}
	if len(s.CollidesWith) > 0 {
		stats = append(stats, warnStyle.Render("⚠ collide"))
	}
	statsLine := labelStyle.Render(strings.Join(stats, "  "))
	if len(stats) == 0 {
		statsLine = labelStyle.Render("no changes")
	}
	statsLine = truncate(statsLine, cardWidth)

	// Footer: model (left) · age (right). Model adds info the project line lacks.
	age := ""
	if !s.LastActive.IsZero() {
		age = humanAge(s.LastActive)
	}
	modelTxt := s.Model
	if modelTxt == "" {
		modelTxt = "—"
	}
	footer := padBetween(
		labelStyle.Render(truncate(modelTxt, cardWidth-len(age)-2)),
		labelStyle.Render(age), cardWidth)

	body := strings.Join([]string{head, project, spark, statsLine, footer}, "\n")

	bs := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("237")).Width(cardWidth)
	if selected {
		// Make the selected card unmistakable: thick accent border.
		bs = bs.Border(lipgloss.ThickBorder()).BorderForeground(colAccent)
	}
	return bs.Render(body)
}

// padBetween left-justifies a and right-justifies b within width (visible cols).
func padBetween(a, b string, width int) string {
	gap := width - lipgloss.Width(a) - lipgloss.Width(b)
	if gap < 1 {
		gap = 1
	}
	return a + strings.Repeat(" ", gap) + b
}

func (m model) renderEmptyState(width, bodyHeight int) string {
	var b strings.Builder
	if len(m.sessions) == 0 {
		writeLines(&b, labelStyle,
			"",
			"  No agent sessions detected.",
			"",
			"  Start an agent (claude, copilot, codex, …) in any terminal —",
			"  it appears here automatically.",
			"",
			"  Add repos to ~/.agentdash/config.yaml for git status and",
			"  cross-worktree collision detection.")
	} else {
		writeLines(&b, labelStyle,
			"",
			fmt.Sprintf("  No sessions match filter '%s'.", m.filter),
			"",
			"  Press f to change the filter, + to widen the time window.")
	}
	return b.String()
}

// renderHeader draws the full-width banner: brand, session count, filter, and
// time window in a purple title bar.
func (m model) renderHeader() string {
	brandStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(colAccent).Padding(0, 1)
	metaStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("60")).Padding(0, 1)

	left := brandStyle.Render("agentdash")
	meta := metaStyle.Render(fmt.Sprintf("%d agents  view:%s  filter:%s  [last %dm]  %s",
		len(m.visible), strings.ToUpper(m.view.String()), m.filter, m.windowMinutes, version.Short()))

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(meta)
	if gap < 0 {
		gap = 0
	}
	fill := metaStyle.Render(strings.Repeat(" ", gap))
	return left + meta + fill
}

func (m model) renderList(width int) string {
	var b strings.Builder
	// Column header row: PROJECT (left) … STATUS (right).
	hdrStyle := titleStyle
	if m.focusDetail {
		hdrStyle = labelStyle.Bold(true)
	}
	hdrGap := width - len("PROJECT") - len("STATUS") - 2
	if hdrGap < 1 {
		hdrGap = 1
	}
	b.WriteString(hdrStyle.Render("PROJECT") + strings.Repeat(" ", hdrGap) + hdrStyle.Render("STATUS") + "\n")
	b.WriteString(labelStyle.Render(strings.Repeat("─", width-2)) + "\n")

	if len(m.visible) == 0 {
		if len(m.sessions) == 0 {
			writeLines(&b, labelStyle,
				"No agent sessions detected.",
				"",
				"Start an agent (claude, copilot, …)",
				"in any terminal — it appears here.",
				"",
				"Add repos to config.yaml for full",
				"git status and collision detection.")
		} else {
			writeLines(&b, labelStyle,
				fmt.Sprintf("No sessions match filter '%s'.", m.filter),
				"",
				"Press f to change filter,",
				"+ to widen the time window.")
		}
		return b.String()
	}

	for i, s := range m.visible {
		// Right side (STATUS): a short sparkline + the state label.
		stateLabel := string(s.Activity)
		spark := lastRunes(m.history.sparkline(s.ID()), 8)
		right := ""
		if spark != "" {
			right = labelStyle.Render(spark) + " "
		}
		right += colorForActivity(s.Activity).Render(stateLabel)
		rightW := lipgloss.Width(right)

		// Left side (PROJECT): marker + spinner glyph + a relative project label.
		glyph := spinnerGlyph(s.Activity, m.spinnerFrame)
		label := projectLabel(s)
		if len(s.CollidesWith) > 0 {
			label += " ⚠"
		}
		marker := "  "
		if i == m.cursor {
			marker = lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render("▸ ")
		}

		// Width budget: total - marker(2) - glyph(2) - gap(1) - right.
		leftBudget := width - 2 - 2 - 1 - rightW
		if leftBudget < 6 {
			leftBudget = 6
		}
		label = truncate(label, leftBudget)
		lstyle := colorForActivity(s.Activity)
		if i == m.cursor {
			lstyle = lstyle.Bold(true)
		}
		left := marker + lstyle.Render(glyph+" "+label)

		// Pad between left and right so STATUS hugs the right edge.
		gap := width - 2 - lipgloss.Width(left) - rightW
		if gap < 1 {
			gap = 1
		}
		b.WriteString(left + strings.Repeat(" ", gap) + right + "\n")
	}
	return b.String()
}

// projectLabel is the short, scannable name for the list: the branch if known,
// otherwise the worktree's base directory.
// agentColor returns a brand color per agent (claude = orange).
func agentColor(name string) lipgloss.Color {
	switch name {
	case "claude":
		return lipgloss.Color("208") // orange
	case "copilot":
		return lipgloss.Color("75") // blue
	case "codex":
		return lipgloss.Color("114") // green
	case "opencode":
		return lipgloss.Color("213") // magenta
	case "kimi":
		return lipgloss.Color("220") // yellow
	default:
		return lipgloss.Color("252")
	}
}

// projectLabel is the repository/project name for grouping — the repo dir,
// else the worktree dir, else the agent cwd. Matches Session.GroupKey so the
// group header and the rows agree. The branch is shown separately, not here.
func projectLabel(s session.Session) string {
	if s.RepoPath != "" {
		return filepath.Base(s.RepoPath)
	}
	if s.WorktreePath != "" {
		return filepath.Base(s.WorktreePath)
	}
	if s.Agent != "" {
		return s.Agent
	}
	return "—"
}

// worktreeLabel names the specific worktree within a project, shown under the
// project group when it differs from the project (i.e. a non-primary worktree).
func worktreeLabel(s session.Session) string {
	wt := filepath.Base(s.WorktreePath)
	if wt == "" || wt == projectLabel(s) {
		return ""
	}
	return wt
}

// lastRunes keeps the trailing n runes of s (used to cap the inline sparkline).
func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

func (m model) renderDeck(width int) string {
	s, ok := m.selected()
	if !ok {
		return labelStyle.Render("Select a session.")
	}
	var b strings.Builder

	// Fields in the requested order, no section label.
	valW := width - kvLabelWidth - 3

	sid := s.SessionID
	if sid == "" {
		sid = "—"
	}
	b.WriteString(kv("session", truncate(sid, valW)))
	b.WriteString(kv("model", emptyDash(s.Model)))
	branch := emptyDash(truncate(s.Branch, valW))
	if s.Base != "" && s.Base != "HEAD" {
		branch = truncate(s.Branch, valW-12) + "  " +
			okStyle.Render(fmt.Sprintf("↑%d", s.Ahead)) + warnStyle.Render(fmt.Sprintf(" ↓%d", s.Behind))
	}
	b.WriteString(kv("branch", branch))
	b.WriteString(kv("worktree", truncatePath(shortenPlain(s.WorktreePath), valW)))

	if s.Metrics.UserMsgs > 0 || s.Metrics.AsstMsgs > 0 {
		b.WriteString(kv("messages", fmt.Sprintf("%d   %s",
			s.Metrics.UserMsgs+s.Metrics.AsstMsgs,
			labelStyle.Render(fmt.Sprintf("(%d user, %d assistant)", s.Metrics.UserMsgs, s.Metrics.AsstMsgs)))))
	} else {
		b.WriteString(kv("messages", labelStyle.Render("—")))
	}
	if s.Metrics.HasTokens() {
		var tok string
		if s.Metrics.TokensIn > 0 {
			tok = fmt.Sprintf("%s in / %s out", humanCount(s.Metrics.TokensIn), humanCount(s.Metrics.TokensOut))
		} else {
			tok = fmt.Sprintf("%s out", humanCount(s.Metrics.TokensOut))
		}
		// Cache tokens dominate long sessions; show them so the totals make sense.
		if s.Metrics.CacheRead > 0 || s.Metrics.CacheWrite > 0 {
			tok += labelStyle.Render(fmt.Sprintf("   cache %s↑ %s↓",
				humanCount(s.Metrics.CacheWrite), humanCount(s.Metrics.CacheRead)))
		}
		b.WriteString(kv("tokens", tok))
	} else {
		b.WriteString(kv("tokens", labelStyle.Render("—")))
	}

	lastOp := "—"
	if s.Metrics.LastTool != "" {
		lastOp = s.Metrics.LastTool
		if !s.Metrics.LastOpAt.IsZero() {
			lastOp += labelStyle.Render("   " + humanAge(s.Metrics.LastOpAt))
		}
	}
	b.WriteString(kv("last op", lastOp))

	lastFile := "—"
	if s.Metrics.LastFile != "" {
		lastFile = truncatePath(s.Metrics.LastFile, valW-10)
		if !s.Metrics.LastOpAt.IsZero() {
			lastFile += labelStyle.Render("  " + humanAge(s.Metrics.LastOpAt))
		}
	}
	b.WriteString(kv("last file", labelStyle.Render(lastFile)))
	return b.String()
}

// renderGitActivity renders the Git and Activity sections (used below the
// conversation in the right box).
func (m model) renderGitActivity(width int) string {
	s, ok := m.selected()
	if !ok {
		return ""
	}
	var b strings.Builder

	b.WriteString(section("Git", width))
	b.WriteString(fmt.Sprintf("  %d files  %s%d %s%d\n",
		s.Diff.FilesChanged,
		okStyle.Render("+"), s.Diff.Insertions,
		warnStyle.Render("-"), s.Diff.Deletions,
	))
	shown := s.ChangedFiles
	if len(shown) > 5 {
		shown = shown[:5]
	}
	for _, f := range shown {
		b.WriteString("  " + truncatePath(f, width-4) + "\n")
	}
	if len(s.ChangedFiles) > 5 {
		b.WriteString(labelStyle.Render(fmt.Sprintf("  … %d more\n", len(s.ChangedFiles)-5)))
	}

	b.WriteString("\n" + section("Activity", width))
	if len(s.CollidesWith) > 0 {
		b.WriteString(warnStyle.Render("  ⚠ collides with "+strings.Join(s.CollidesWith, ", ")) + "\n")
	}
	if s.LastCommit.Hash != "" {
		b.WriteString(fmt.Sprintf("  last commit  %s  %s\n",
			s.LastCommit.Hash, truncate(s.LastCommit.Subject, width-18)))
	}
	if len(s.Commits) > 0 {
		b.WriteString(labelStyle.Render(fmt.Sprintf("  %d commit(s) since %s\n", len(s.Commits), s.Base)))
	}
	return b.String()
}

func (m model) renderHelp() string {
	if m.searching {
		return titleStyle.Render("search: ") + m.searchQuery + helpStyle.Render("   (esc cancel · enter apply)")
	}
	if m.overlay != overlayNone {
		return helpStyle.Render("esc/q close · ↑/↓ scroll")
	}
	if m.view == viewBoard {
		return helpStyle.Render("q quit · v view · ←→ column · ↑↓ card · enter detail · f filter · ? help")
	}
	if m.view == viewStream {
		return helpStyle.Render("q quit · v view (→ fleet) · live fleet event feed · ? help")
	}
	return helpStyle.Render("q quit · v view · ↑/↓ nav · tab focus · l logs · d diff · enter detail · f filter · / search · ? help")
}

// renderOverlay draws the active full-pane overlay for the selected session.
func (m model) renderOverlay(width, height int) string {
	s, ok := m.selected()
	if !ok {
		return labelStyle.Render("No session selected.")
	}

	var title, body string
	switch m.overlay {
	case overlayDetail:
		title = emptyDash(s.Agent) + " — " + projectLabel(s)
		body = m.renderDeck(width)
	case overlayHelp:
		title = "Help"
		body = helpBody()
	case overlayDiff:
		title = "Diff — " + emptyDash(s.Branch)
		body = m.overlayText
	case overlayStatus:
		title = "Status — " + emptyDash(s.Branch)
		body = m.overlayText
	case overlayConflicts:
		title = "Conflicts — " + emptyDash(s.Branch)
		body = conflictBody(s)
	case overlayTerminal:
		title = "Attach — " + emptyDash(s.Branch)
		body = terminalBody(s)
	case overlayLogs:
		title = "Conversation — " + emptyDash(s.Agent)
		body = m.logsBody(width)
	}

	// Scrollable viewport: a window of `viewport` lines starting at detailScroll.
	lines := strings.Split(body, "\n")
	viewport := height - 4
	if viewport < 1 {
		viewport = 1
	}
	start := m.detailScroll
	if start > len(lines)-1 {
		start = max(0, len(lines)-1)
	}
	if start < 0 {
		start = 0
	}
	end := start + viewport
	if end > len(lines) {
		end = len(lines)
	}
	window := lines[start:end]

	clamped := make([]string, len(window))
	for i, l := range window {
		clamped[i] = truncate(l, width)
	}
	scrollInfo := ""
	if len(lines) > viewport {
		scrollInfo = labelStyle.Render(fmt.Sprintf("  [%d-%d/%d  ↑/↓ scroll]", start+1, end, len(lines)))
	}
	return titleStyle.Render(title) + scrollInfo + "\n\n" + strings.Join(clamped, "\n")
}

func helpBody() string {
	return strings.Join([]string{
		"Navigation",
		"  ↑/k, ↓/j   move between sessions (or scroll, when detail focused)",
		"  tab        switch focus between list and detail",
		"  r          refresh now",
		"  q          quit",
		"",
		"Filtering",
		"  f          cycle activity filter (all → active → waiting → idle)",
		"  /          search sessions by path / branch / agent",
		"  +/-        widen / narrow the time window (±10 min)",
		"",
		"Session views (selected session)",
		"  l          conversation / recent activity",
		"  d          full git diff        s   git status",
		"  c          cross-worktree collisions",
		"  t          terminal attach hint  ?   this help",
		"",
		"Activity states (color-coded badge per session)",
		"  thinking · reading · writing · running · searching",
		"  browsing · spawning · compacting · waiting · idle · exited",
		"  ⚠          this session shares changed files with another",
	}, "\n")
}

func conflictBody(s session.Session) string {
	if len(s.CollidesWith) == 0 {
		return okStyle.Render("✓ No file collisions with other active worktrees.")
	}
	var b strings.Builder
	b.WriteString(warnStyle.Render("⚠ This worktree shares changed files with:") + "\n\n")
	for _, k := range s.CollidesWith {
		b.WriteString("  • " + k + "\n")
	}
	b.WriteString("\n" + labelStyle.Render("Changed files in this worktree:") + "\n")
	for _, f := range s.ChangedFiles {
		b.WriteString("  " + f + "\n")
	}
	return b.String()
}

func (m model) logsBody(width int) string {
	if m.overlayText == "loading…" && len(m.overlayEvents) == 0 {
		return labelStyle.Render("loading…")
	}
	if len(m.overlayEvents) == 0 {
		s, _ := m.selected()
		if !s.HasTranscript() {
			var b strings.Builder
			writeLines(&b, labelStyle,
				"This session was detected as a running process,",
				"but writes no readable transcript on disk —",
				"so there is no conversation to show.",
				"",
				"This is common for IDE-embedded agents (e.g. Zed's",
				"ACP agents) that don't use the CLI's session files.",
				"",
				"Sessions with a ● transcript (sorted to the top) do",
				"show their conversation here.")
			return b.String()
		}
		return labelStyle.Render("No conversation events found in the transcript.")
	}
	// Restrained label-column style: a muted fixed-width speaker
	// label, with the wrapped message text aligned in a hanging indent beside
	// it. Calm, scannable; no colored bubbles.
	const labelW = 8
	indent := strings.Repeat(" ", labelW+1)
	wrapWidth := width - labelW - 2
	if wrapWidth < 16 {
		wrapWidth = 16
	}
	agentName := m.selectedAgent()
	var b strings.Builder
	for _, ev := range m.overlayEvents {
		switch ev.Role {
		case "tool":
			b.WriteString(indent + labelStyle.Render(ev.Text) + "\n")
		default:
			label := agentName
			lstyle := okStyle
			if ev.Role == "user" {
				label, lstyle = "you", lipgloss.NewStyle().Foreground(colUser)
			}
			wrapped := strings.Split(lipgloss.NewStyle().Width(wrapWidth).Render(ev.Text), "\n")
			for j, ln := range wrapped {
				if j == 0 {
					b.WriteString(lstyle.Render(fmt.Sprintf("%-*s", labelW, label)) + " " + convText.Render(ln) + "\n")
				} else {
					b.WriteString(indent + convText.Render(ln) + "\n")
				}
			}
		}
	}
	return b.String()
}

func (m model) selectedAgent() string {
	if s, ok := m.selected(); ok && s.Agent != "" {
		return s.Agent
	}
	return "agent"
}

func terminalBody(s session.Session) string {
	var b strings.Builder
	b.WriteString(labelStyle.Render("AgentDash is read-only and does not attach to agents.") + "\n")
	b.WriteString(labelStyle.Render("To work in this session's worktree, run:") + "\n\n")
	if s.WorktreePath != "" {
		b.WriteString("  cd " + s.WorktreePath + "\n")
	}
	if s.Agent != "" && s.Agent != "procscan" {
		b.WriteString("  " + s.Agent + "\n")
	}
	if s.PID > 0 {
		b.WriteString("\n" + labelStyle.Render(fmt.Sprintf("Live process pid %d", s.PID)) + "\n")
	}
	return b.String()
}

// --- helpers ---

// kvLabelWidth aligns all detail values to one column.
const kvLabelWidth = 11

func kv(k, v string) string {
	return fmt.Sprintf("  %s%s\n", labelStyle.Render(fmt.Sprintf("%-*s", kvLabelWidth, k)), v)
}

// section renders a section title followed by a faint full-width rule and a
// blank line of breathing room, for a calm, readable detail panel.
func section(title string, width int) string {
	rule := width - 2
	if rule < 0 {
		rule = 0
	}
	ruleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
	return titleStyle.Render(title) + "\n" + ruleStyle.Render(strings.Repeat("─", rule)) + "\n\n"
}

// writeLines renders each line with style separately so embedded newlines never
// get padded into stray trailing whitespace by lipgloss.
func writeLines(b *strings.Builder, style lipgloss.Style, lines ...string) {
	for _, l := range lines {
		if l == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(style.Render(l) + "\n")
	}
}

// activityColor maps each fine-grained state to a color — a distinct hue per
// state so the list reads at a glance.
func activityColor(a agent.Activity) lipgloss.Color {
	switch a {
	case agent.ActivityThinking:
		return lipgloss.Color("213") // magenta
	case agent.ActivityReading:
		return lipgloss.Color("75") // blue
	case agent.ActivityWriting:
		return colOK // green
	case agent.ActivityRunning:
		return lipgloss.Color("220") // yellow
	case agent.ActivitySearching:
		return lipgloss.Color("80") // cyan
	case agent.ActivityBrowsing:
		return lipgloss.Color("141") // purple
	case agent.ActivitySpawning:
		return lipgloss.Color("208") // orange
	case agent.ActivityCompacting:
		return lipgloss.Color("180") // tan
	case agent.ActivityWaiting:
		return colWarn
	case agent.ActivityExited:
		return colErr
	default: // idle / unknown
		return colSubtle
	}
}

func colorForActivity(a agent.Activity) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(activityColor(a))
}

func emptyDash(s string) string {
	if s == "" {
		return labelStyle.Render("—")
	}
	return s
}

// shortenPlain replaces the home prefix with ~ and returns plain text (no style).
func shortenPlain(path string) string {
	if path == "" {
		return "—"
	}
	if home, err := homeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// truncatePath shortens a file path from the left, keeping the most
// informative tail (filename), with a leading ellipsis: "…/auth/oauth.go".
func truncatePath(p string, max int) string {
	r := []rune(p)
	if len(r) <= max || max < 2 {
		return p
	}
	return "…" + string(r[len(r)-(max-1):])
}

// truncate clips s to at most max display columns (rune-aware, so multi-byte
// glyphs like ◐ count as one), appending an ellipsis when shortened.
func truncate(s string, max int) string {
	if max < 1 {
		max = 1
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// humanCount formats a token count compactly: 1234 → "1.2k", 1286731 → "1.3M".
func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func humanAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}
