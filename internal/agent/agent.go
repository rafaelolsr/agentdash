// Package agent defines the adapter interface for discovering running AI coding
// agent sessions and the registry of available adapters.
//
// Each adapter knows how to read one agent's own session/transcript files (or
// scan processes) and report the working directory each session runs in. The
// working directory is the join key the correlator uses to attach git data.
package agent

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Activity is the live state of an agent session, classified from the tail of
// its transcript. The fine-grained states mirror the vocabulary popularized by
// lazyagent so that a glance at the list tells you what each agent is doing.
type Activity string

const (
	ActivityIdle       Activity = "idle"       // nothing written for a while
	ActivityWaiting    Activity = "waiting"    // agent replied, awaiting user
	ActivityThinking   Activity = "thinking"   // generating a response
	ActivityReading    Activity = "reading"    // reading files (Read/Glob/Grep)
	ActivityWriting    Activity = "writing"    // editing files (Edit/Write)
	ActivityRunning    Activity = "running"    // executing shell commands
	ActivitySearching  Activity = "searching"  // searching the codebase
	ActivityBrowsing   Activity = "browsing"   // web browsing / fetching
	ActivitySpawning   Activity = "spawning"   // delegating to a sub-agent
	ActivityCompacting Activity = "compacting" // context compaction
	ActivityExited     Activity = "exited"     // session gone stale / process gone
	ActivityUnknown    Activity = "unknown"    // detected but state undetermined
)

// Glyph returns a compact status symbol for the activity. The TUI shows the
// full state label as a colored badge; the glyph is a fallback for tight space.
func (a Activity) Glyph() string {
	switch a {
	case ActivityExited:
		return "✕"
	case ActivityWaiting:
		return "◐"
	case ActivityIdle, ActivityUnknown:
		return "○"
	default:
		// Any of the active "doing something" states.
		return "●"
	}
}

// Active reports whether the state represents the agent actively doing work
// (as opposed to idle/waiting/exited). Used for the "active" filter.
func (a Activity) Active() bool {
	switch a {
	case ActivityThinking, ActivityReading, ActivityWriting, ActivityRunning,
		ActivitySearching, ActivityBrowsing, ActivitySpawning, ActivityCompacting:
		return true
	}
	return false
}

// StateForTool maps an agent's tool name to the activity state it implies,
// normalized across agents (Claude's "Read" and Cursor's "Read_file_v2" both
// map to reading). Unknown tools fall back to thinking, since an unfinished
// tool call still means the agent is actively working.
func StateForTool(tool string) Activity {
	t := strings.ToLower(tool)
	switch {
	case containsAny(t, "read", "glob", "cat", "view", "open"):
		return ActivityReading
	case containsAny(t, "edit", "write", "apply_patch", "patch", "notebook", "create"):
		return ActivityWriting
	case containsAny(t, "bash", "shell", "exec", "run", "terminal", "command"):
		return ActivityRunning
	case containsAny(t, "grep", "search", "find", "ripgrep"):
		return ActivitySearching
	case containsAny(t, "fetch", "web", "browse", "url", "http"):
		return ActivityBrowsing
	case containsAny(t, "task", "agent", "spawn", "delegate", "subagent"):
		return ActivitySpawning
	default:
		return ActivityThinking
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// AgentSession is one discovered agent session. It carries only what the agent
// itself reveals; git data is joined in later by the correlator.
type AgentSession struct {
	Agent      string    // adapter name: "claude", "copilot", ...
	SessionID  string    // adapter-specific session identifier
	CWD        string    // working directory — the correlation join key
	Model      string    // model name if recorded
	GitBranch  string    // branch if the agent records it
	Task       string    // first user prompt / summary if cheaply available
	LastActive time.Time // last activity timestamp (event time or file mtime)
	Activity   Activity
	PID        int    // process id if known (0 = unknown)
	SourceFile string // transcript path we read, for debugging

	// Rich metrics, populated by adapters that can compute them cheaply.
	Metrics Metrics
}

// Metrics are the per-session counters surfaced in the detail panel. Zero
// values mean "not available for this agent".
type Metrics struct {
	UserMsgs   int
	AsstMsgs   int
	TokensIn   int
	TokensOut  int
	CacheRead  int
	CacheWrite int
	LastTool   string    // most recent tool name
	LastFile   string    // most recent file the agent read/wrote
	LastOpAt   time.Time // when the last tool ran
}

// HasTokens reports whether token accounting is available.
func (m Metrics) HasTokens() bool { return m.TokensIn > 0 || m.TokensOut > 0 }

// Event is one entry in an agent conversation, normalized across agents so the
// TUI can render any agent's transcript uniformly.
type Event struct {
	Role string    // "user" | "assistant" | "tool" | "system"
	Kind string    // "text" | "tool_use" | "tool_result" | other adapter-specific
	Text string    // human-readable summary of the event
	Time time.Time // event timestamp if recorded
}

// AgentAdapter discovers the sessions of one agent. Implementations must be
// cheap (stat directories, read file heads/tails) and must fail soft.
type AgentAdapter interface {
	// Name is the stable adapter identifier (e.g. "claude").
	Name() string
	// Discover returns the sessions currently visible to this adapter.
	Discover() ([]AgentSession, error)
}

// Conversational is implemented by adapters that can return the recent events
// of a session for the conversation/logs view. Adapters lacking it (e.g. the
// process scanner) simply have no conversation to show.
type Conversational interface {
	// Conversation returns up to limit recent events for the given session,
	// oldest first. sourceFile is the AgentSession.SourceFile value.
	Conversation(sourceFile string, limit int) ([]Event, error)
}

// Metricser is implemented by adapters that can compute rich per-session
// metrics (message counts, tokens, last tool/file). Computed on demand for the
// selected session only, since it may require a full transcript scan.
type Metricser interface {
	ComputeMetrics(sourceFile string) (Metrics, error)
}

// MetricsFor returns rich metrics for a session if its adapter supports them.
func MetricsFor(agentName, sourceFile string) (Metrics, bool) {
	mu.RLock()
	a, ok := registry[agentName]
	mu.RUnlock()
	if !ok {
		return Metrics{}, false
	}
	mc, ok := a.(Metricser)
	if !ok {
		return Metrics{}, false
	}
	m, err := mc.ComputeMetrics(sourceFile)
	if err != nil {
		return Metrics{}, false
	}
	return m, true
}

// ConversationFor finds the adapter for agentName and returns its recent events
// for the session, or nil if the adapter is not conversational.
func ConversationFor(agentName, sourceFile string, limit int) ([]Event, error) {
	mu.RLock()
	a, ok := registry[agentName]
	mu.RUnlock()
	if !ok {
		return nil, nil
	}
	conv, ok := a.(Conversational)
	if !ok {
		return nil, nil
	}
	return conv.Conversation(sourceFile, limit)
}

var (
	mu       sync.RWMutex
	registry = map[string]AgentAdapter{}
)

// Register adds an adapter to the global registry. Intended for use in adapter
// package init functions.
func Register(a AgentAdapter) {
	mu.Lock()
	defer mu.Unlock()
	registry[a.Name()] = a
}

// Adapters returns the registered adapters. If only is non-empty, only the
// named adapters are returned (unknown names are ignored).
func Adapters(only []string) []AgentAdapter {
	mu.RLock()
	defer mu.RUnlock()

	var out []AgentAdapter
	if len(only) == 0 {
		for _, a := range registry {
			out = append(out, a)
		}
	} else {
		for _, name := range only {
			if a, ok := registry[name]; ok {
				out = append(out, a)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// DiscoverAll runs the selected adapters concurrently and merges their results.
// An adapter error is swallowed so one failing format never blanks the board.
func DiscoverAll(only []string) []AgentSession {
	adapters := Adapters(only)
	results := make([][]AgentSession, len(adapters))

	var wg sync.WaitGroup
	for i, a := range adapters {
		wg.Add(1)
		go func(i int, a AgentAdapter) {
			defer wg.Done()
			sessions, err := a.Discover()
			if err != nil {
				return
			}
			results[i] = sessions
		}(i, a)
	}
	wg.Wait()

	var all []AgentSession
	for _, r := range results {
		all = append(all, r...)
	}
	return dedupeProcscan(all)
}

// procscanAdapterName is the fallback adapter whose results are suppressed when
// a richer transcript adapter already covers the same agent + directory.
const procscanAdapterName = "procscan"

// dedupeProcscan drops process-scan sessions that duplicate a transcript-based
// session for the same agent and working directory. Transcript adapters carry
// model/task/activity that the process scan cannot, so they win.
func dedupeProcscan(sessions []AgentSession) []AgentSession {
	covered := map[string]bool{} // agent\x00cwd from non-procscan adapters
	for _, s := range sessions {
		if s.SourceFile != "(process scan)" {
			covered[s.Agent+"\x00"+s.CWD] = true
		}
	}

	out := sessions[:0]
	for _, s := range sessions {
		if s.SourceFile == "(process scan)" && covered[s.Agent+"\x00"+s.CWD] {
			continue
		}
		out = append(out, s)
	}
	return out
}

// --- shared helpers for adapters ---

// HomeSubdir resolves a path under the user's home directory, honoring an
// environment override if set and non-empty.
func HomeSubdir(envVar string, fallbackSegments ...string) (string, error) {
	if envVar != "" {
		if v := os.Getenv(envVar); v != "" {
			return v, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, fallbackSegments...)...), nil
}

// Resolve cleans and symlink-resolves a path so it matches worktree join keys.
func Resolve(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// RecencyCutoff is how stale a session may be before adapters omit it. Agents
// like Claude accumulate thousands of historical transcripts; only recent ones
// are relevant to a live dashboard.
const RecencyCutoff = 24 * time.Hour

// TooOld reports whether a session last active at t should be hidden.
func TooOld(t, now time.Time) bool {
	return !t.IsZero() && now.Sub(t) > RecencyCutoff
}

// TailLines returns up to n trailing lines of a file, reading from the end so
// large transcripts are not loaded whole. Lines are returned in file order.
func TailLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	const chunk = 64 * 1024
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := stat.Size()

	var (
		data   []byte
		newCnt int
		pos    = size
	)
	for pos > 0 && newCnt <= n {
		readSize := int64(chunk)
		if pos < readSize {
			readSize = pos
		}
		pos -= readSize
		buf := make([]byte, readSize)
		if _, err := f.ReadAt(buf, pos); err != nil && err != io.EOF {
			return nil, err
		}
		data = append(buf, data...)
		newCnt = 0
		for _, b := range data {
			if b == '\n' {
				newCnt++
			}
		}
	}

	var lines []string
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			if line := string(data[start:i]); line != "" {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(data) {
		if line := string(data[start:]); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// staleAfter is how long without a transcript write before a session is
// considered exited regardless of what its last event was.
const staleAfter = 5 * time.Minute

// CombineActivity merges a content-derived activity with file recency. The
// content signal carries the fine-grained state (thinking/reading/…); mtime
// decides whether that state is still live. A 10-second grace keeps a freshly
// finished turn from flapping. Stale files collapse to exited.
func CombineActivity(fromContent Activity, modTime, now time.Time) Activity {
	age := now.Sub(modTime)
	if age > staleAfter {
		return ActivityExited
	}
	switch fromContent {
	case ActivityWaiting:
		return ActivityWaiting
	case "", ActivityUnknown:
		return ClassifyByMtime(modTime, now)
	default:
		// An active state (thinking/reading/writing/…). Trust it only while
		// the file is fresh; after a short grace it has gone idle.
		if age <= 30*time.Second {
			return fromContent
		}
		return ActivityIdle
	}
}

// ClassifyByMtime derives a coarse activity from how recently a file changed.
// Used as a baseline when no content signal is available.
func ClassifyByMtime(modTime time.Time, now time.Time) Activity {
	switch age := now.Sub(modTime); {
	case age < 0:
		return ActivityIdle
	case age <= 30*time.Second:
		return ActivityThinking
	case age <= staleAfter:
		return ActivityIdle
	default:
		return ActivityExited
	}
}
