// Package tui implements the AgentDash dashboard: a left session list and a
// right information deck (detail, git, collisions), refreshed on a poll loop.
package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/datageek/agentdash/internal/agent"
	"github.com/datageek/agentdash/internal/config"
	"github.com/datageek/agentdash/internal/correlate"
	"github.com/datageek/agentdash/internal/gitutil"
	"github.com/datageek/agentdash/internal/session"
	"github.com/datageek/agentdash/internal/worktree"

	// Register the agent adapters.
	_ "github.com/datageek/agentdash/internal/agent/claude"
	_ "github.com/datageek/agentdash/internal/agent/codex"
	_ "github.com/datageek/agentdash/internal/agent/copilot"
	_ "github.com/datageek/agentdash/internal/agent/kimi"
	_ "github.com/datageek/agentdash/internal/agent/opencode"
	_ "github.com/datageek/agentdash/internal/agent/procscan"
)

// Run launches the dashboard with the given config.
func Run(cfg config.Config) error {
	m := newModel(cfg)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// --- messages ---

type sessionsMsg struct{ sessions []session.Session }
type tickMsg struct{}
type spinnerMsg struct{}

const spinnerInterval = 120 * time.Millisecond

func spinnerCmd() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerMsg{} })
}

type overlayTextMsg struct {
	o    overlay
	text string
}
type overlayEventsMsg struct {
	events []agent.Event
}

// fetchLogsCmd reads the recent conversation for a session off the UI thread.
func fetchLogsCmd(s session.Session) tea.Cmd {
	return func() tea.Msg {
		events, _ := agent.ConversationFor(s.Agent, s.SourceFile, 40)
		return overlayEventsMsg{events: events}
	}
}

type metricsMsg struct {
	key     string
	metrics agent.Metrics
}

// fetchMetricsCmd computes rich metrics for the selected session off-thread.
func fetchMetricsCmd(s session.Session) tea.Cmd {
	return func() tea.Msg {
		m, ok := agent.MetricsFor(s.Agent, s.SourceFile)
		if !ok {
			return metricsMsg{key: s.ID()}
		}
		return metricsMsg{key: s.ID(), metrics: m}
	}
}

type previewMsg struct {
	key    string
	events []agent.Event
}

// fetchPreviewCmd loads a short conversation preview for the right box.
func fetchPreviewCmd(s session.Session) tea.Cmd {
	return func() tea.Msg {
		events, _ := agent.ConversationFor(s.Agent, s.SourceFile, 12)
		return previewMsg{key: s.ID(), events: events}
	}
}

// fetchOverlayCmd runs the git command for an overlay off the UI thread.
func fetchOverlayCmd(o overlay, s session.Session) tea.Cmd {
	return func() tea.Msg {
		if s.WorktreePath == "" {
			return overlayTextMsg{o: o, text: "(no worktree for this session)"}
		}
		var text string
		var err error
		switch o {
		case overlayDiff:
			text, err = gitutil.DiffText(s.WorktreePath)
		case overlayStatus:
			text, err = gitutil.StatusText(s.WorktreePath)
		}
		if err != nil {
			text = "error: " + err.Error()
		}
		return overlayTextMsg{o: o, text: text}
	}
}

// pollCmd runs both collectors off the UI thread and returns merged sessions.
func pollCmd(cfg config.Config) tea.Cmd {
	return func() tea.Msg {
		worktrees := worktree.CollectAll(cfg.Repos, cfg.Bases)
		agents := agent.DiscoverAll(cfg.Agents)
		var overlays map[string]session.Overlay
		if p, err := config.ResolvePaths(); err == nil {
			overlays = session.LoadOverlays(p.SessionsDir)
		}
		return sessionsMsg{sessions: correlate.BuildWithOverlays(worktrees, agents, overlays)}
	}
}

func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

// --- model ---

// overlay identifies a full-pane detail view opened over the dashboard.
type overlay int

const (
	overlayNone overlay = iota
	overlayHelp
	overlayDetail // full session detail (opened with enter)
	overlayDiff
	overlayStatus
	overlayConflicts
	overlayTerminal
	overlayLogs
)

// viewMode is the active top-level lens, cycled with v.
type viewMode int

const (
	viewFleet  viewMode = iota // per-agent monitor (default)
	viewBoard                  // kanban by what the supervisor must do
	viewStream                 // live fleet event feed
	viewCount  = 3
)

func (v viewMode) String() string {
	switch v {
	case viewBoard:
		return "board"
	case viewStream:
		return "stream"
	default:
		return "fleet"
	}
}

type model struct {
	cfg      config.Config
	interval time.Duration

	sessions        []session.Session // raw, unfiltered
	visible         []session.Session // after filter + search
	cursor          int
	width           int
	height          int
	overlay         overlay
	focusDetail     bool // tab toggles list vs detail focus
	detailScroll    int
	maxDetailScroll int // set during render so down-key can't overscroll

	view     viewMode // fleet / board / stream (cycled with v)
	boardCol int      // selected column in board view

	filter        activityFilter
	windowMinutes int
	searching     bool
	searchQuery   string

	polling bool // in-flight guard so slow polls do not overlap
	loaded  bool

	overlayText   string        // fetched content for diff/status overlays
	overlayEvents []agent.Event // fetched conversation for the logs overlay

	spinnerFrame int
	history      *activityHistory
	eventLog     *eventLog

	metricsCache map[string]metricsMsg // session ID → computed metrics

	previewEvents []agent.Event // conversation preview for the selected session
	previewKey    string        // session ID the preview belongs to
}

// activityFilter selects which sessions appear. The default (filterLive) hides
// the noise — exited sessions and worktree-only rows with no agent attached.
type activityFilter int

const (
	filterLive    activityFilter = iota // has an agent, not exited (default)
	filterAll                           // everything, including exited + agentless
	filterWorking                       // actively doing something
	filterWaiting                       // awaiting user input
)

const filterCount = 4

func (f activityFilter) String() string {
	switch f {
	case filterAll:
		return "all"
	case filterWorking:
		return "working"
	case filterWaiting:
		return "waiting"
	default:
		return "live"
	}
}

func (f activityFilter) match(s session.Session) bool {
	switch f {
	case filterAll:
		return true
	case filterWorking:
		return s.Activity.Active()
	case filterWaiting:
		return s.Activity == agent.ActivityWaiting
	default: // filterLive: a real agent (not a bare worktree) that hasn't exited
		return s.Agent != "" && s.Activity != agent.ActivityExited
	}
}

const defaultWindowMinutes = 30

func newModel(cfg config.Config) model {
	interval := time.Duration(cfg.RefreshSeconds) * time.Second
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return model{cfg: cfg, interval: interval, windowMinutes: defaultWindowMinutes, history: newActivityHistory(), eventLog: newEventLog(), metricsCache: map[string]metricsMsg{}}
}

// recompute rebuilds the visible slice from sessions, applying the time window,
// activity filter, and search query. Keeps the cursor in range.
func (m *model) recompute() {
	cutoff := time.Now().Add(-time.Duration(m.windowMinutes) * time.Minute)
	q := strings.ToLower(strings.TrimSpace(m.searchQuery))

	m.visible = m.visible[:0]
	for _, s := range m.sessions {
		if !s.LastActive.IsZero() && s.LastActive.Before(cutoff) {
			continue
		}
		if !m.filter.match(s) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(s.WorktreePath+" "+s.Branch+" "+s.Agent), q) {
			continue
		}
		m.visible = append(m.visible, s)
	}
	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
}

func (m model) Init() tea.Cmd {
	// Kick off an immediate poll, the slow poll loop, and the fast spinner loop.
	m.polling = true
	return tea.Batch(pollCmd(m.cfg), tickCmd(m.interval), spinnerCmd())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		var cmd tea.Cmd
		if !m.polling {
			m.polling = true
			cmd = pollCmd(m.cfg)
		}
		return m, tea.Batch(cmd, tickCmd(m.interval))

	case spinnerMsg:
		m.spinnerFrame++
		return m, spinnerCmd()

	case sessionsMsg:
		m.polling = false
		m.loaded = true
		m.sessions = msg.sessions
		m.history.record(msg.sessions)
		m.eventLog.record(msg.sessions, time.Now())
		m.recompute()
		m.applyMetrics()
		return m, m.maybeFetchMetrics()

	case overlayTextMsg:
		// Ignore stale fetches if the overlay changed before this returned.
		if m.overlay == msg.o {
			m.overlayText = msg.text
		}
		return m, nil

	case overlayEventsMsg:
		if m.overlay == overlayLogs {
			m.overlayEvents = msg.events
		}
		return m, nil

	case metricsMsg:
		m.metricsCache[msg.key] = msg
		m.applyMetrics()
		return m, nil

	case previewMsg:
		if msg.key == m.previewKey {
			m.previewEvents = msg.events
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Search mode captures typing until esc/enter.
	if m.searching {
		switch key {
		case "esc":
			m.searching = false
			m.searchQuery = ""
			m.recompute()
		case "enter":
			m.searching = false
		case "backspace":
			if m.searchQuery != "" {
				m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
				m.recompute()
			}
		default:
			if len(key) == 1 {
				m.searchQuery += key
				m.recompute()
			}
		}
		return m, nil
	}

	// When an overlay is open, esc/q closes it; up/down scroll its content.
	if m.overlay != overlayNone {
		switch key {
		case "esc", "q", "enter":
			m.overlay = overlayNone
			m.detailScroll = 0
			return m, nil
		case "up", "k":
			if m.detailScroll > 0 {
				m.detailScroll--
			}
			return m, nil
		case "down", "j":
			m.detailScroll++
			return m, nil
		}
		return m, nil
	}

	switch key {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "r":
		if !m.polling {
			m.polling = true
			return m, pollCmd(m.cfg)
		}
	case "v":
		m.view = (m.view + 1) % viewCount
		m.detailScroll = 0
	case "tab":
		if m.view == viewFleet {
			m.focusDetail = !m.focusDetail
			m.detailScroll = 0
		}
	case "enter":
		if _, ok := m.selected(); ok {
			return m.openOverlay(overlayDetail)
		}
	case "left", "h":
		if m.view == viewBoard {
			m.moveBoard(-1, 0)
			return m, m.maybeFetchMetrics()
		}
	case "right":
		if m.view == viewBoard {
			m.moveBoard(1, 0)
			return m, m.maybeFetchMetrics()
		}
	case "up", "k":
		if m.view == viewBoard {
			m.moveBoard(0, -1)
			return m, m.maybeFetchMetrics()
		}
		if m.focusDetail {
			if m.detailScroll > 0 {
				m.detailScroll--
			}
		} else if m.cursor > 0 {
			m.cursor--
			return m, m.maybeFetchMetrics()
		}
	case "down", "j":
		if m.view == viewBoard {
			m.moveBoard(0, 1)
			return m, m.maybeFetchMetrics()
		}
		if m.focusDetail {
			if m.detailScroll < m.rightMaxScroll() {
				m.detailScroll++
			}
		} else if m.cursor < len(m.visible)-1 {
			m.cursor++
			return m, m.maybeFetchMetrics()
		}
	case "f":
		m.filter = (m.filter + 1) % filterCount
		m.recompute()
	case "/":
		m.searching = true
		m.searchQuery = ""
	case "+", "=":
		m.windowMinutes += 10
		m.recompute()
	case "-", "_":
		if m.windowMinutes > 10 {
			m.windowMinutes -= 10
			m.recompute()
		}
	case "d":
		return m.openOverlay(overlayDiff)
	case "s":
		return m.openOverlay(overlayStatus)
	case "c":
		return m.openOverlay(overlayConflicts)
	case "t":
		return m.openOverlay(overlayTerminal)
	case "l":
		return m.openOverlay(overlayLogs)
	case "?":
		return m.openOverlay(overlayHelp)
	}
	return m, nil
}

// openOverlay toggles a detail overlay for the selected session. Diff and
// status overlays fetch their content lazily, off the UI thread.
func (m model) openOverlay(o overlay) (tea.Model, tea.Cmd) {
	if m.overlay == o {
		m.overlay = overlayNone
		m.overlayText = ""
		m.overlayEvents = nil
		return m, nil
	}
	m.overlay = o
	m.overlayText = ""
	m.overlayEvents = nil
	m.detailScroll = 0

	s, ok := m.selected()
	if !ok {
		m.overlayText = "(no session selected)"
		return m, nil
	}
	switch o {
	case overlayDiff, overlayStatus:
		m.overlayText = "loading…"
		return m, fetchOverlayCmd(o, s)
	case overlayLogs:
		m.overlayText = "loading…"
		return m, fetchLogsCmd(s)
	}
	return m, nil
}

// boardColumns groups the visible sessions into the five board buckets,
// returning, for each bucket, the indices into m.visible.
func (m model) boardColumns() [5][]int {
	var cols [5][]int
	for i, s := range m.visible {
		b := s.Board()
		cols[b] = append(cols[b], i)
	}
	return cols
}

// moveBoard navigates the kanban: dx changes column, dy changes card. It keeps
// m.cursor pointed at the selected card's index in m.visible.
func (m *model) moveBoard(dx, dy int) {
	cols := m.boardColumns()
	// Find current (col, row) from m.cursor.
	curCol, curRow := m.boardCol, 0
	if len(cols[curCol]) > 0 {
		for r, idx := range cols[curCol] {
			if idx == m.cursor {
				curRow = r
				break
			}
		}
	}
	if dx != 0 {
		// Move to an adjacent non-empty column.
		c := curCol
		for step := 0; step < 5; step++ {
			c = (c + dx + 5) % 5
			if len(cols[c]) > 0 {
				break
			}
		}
		curCol = c
		curRow = 0
	}
	if dy != 0 && len(cols[curCol]) > 0 {
		curRow += dy
		if curRow < 0 {
			curRow = 0
		}
		if curRow >= len(cols[curCol]) {
			curRow = len(cols[curCol]) - 1
		}
	}
	m.boardCol = curCol
	if len(cols[curCol]) > 0 {
		m.cursor = cols[curCol][curRow]
	}
}

// applyMetrics copies any cached metrics into the matching visible session so
// the detail panel can render them.
func (m *model) applyMetrics() {
	for i := range m.visible {
		if mm, ok := m.metricsCache[m.visible[i].ID()]; ok {
			m.visible[i].Metrics = mm.metrics
		}
	}
}

// maybeFetchMetrics returns commands to load the selected session's metrics and
// conversation preview, applying cached values immediately. Triggered on
// selection change and after each poll.
func (m *model) maybeFetchMetrics() tea.Cmd {
	s, ok := m.selected()
	if !ok || !s.HasTranscript() {
		m.previewEvents = nil
		m.previewKey = ""
		return nil
	}
	var cmds []tea.Cmd
	if _, cached := m.metricsCache[s.ID()]; cached {
		m.applyMetrics()
	} else {
		cmds = append(cmds, fetchMetricsCmd(s))
	}
	// Refresh the preview whenever the selection changes.
	if m.previewKey != s.ID() {
		m.previewKey = s.ID()
		m.previewEvents = nil
		cmds = append(cmds, fetchPreviewCmd(s))
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func (m model) selected() (session.Session, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return session.Session{}, false
	}
	return m.visible[m.cursor], true
}
