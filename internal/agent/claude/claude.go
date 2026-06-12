// Package claude implements the AgentAdapter for Claude Code.
//
// Claude Code stores each session as an append-only JSONL transcript at
//
//	~/.claude/projects/<path-slug>/<session-id>.jsonl
//
// Every line records the session's cwd and gitBranch, plus message content and
// token usage. We read the first line for stable metadata (cwd, model, first
// prompt) and stat the file for activity, avoiding a full parse on every poll.
package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/datageek/agentdash/internal/agent"
)

func init() { agent.Register(New()) }

// Adapter discovers Claude Code sessions.
type Adapter struct{}

// New returns a Claude adapter.
func New() *Adapter { return &Adapter{} }

// Name implements agent.AgentAdapter.
func (a *Adapter) Name() string { return "claude" }

// transcriptLine is the subset of a Claude JSONL line we care about.
type transcriptLine struct {
	Type      string         `json:"type"`
	CWD       string         `json:"cwd"`
	GitBranch string         `json:"gitBranch"`
	SessionID string         `json:"sessionId"`
	Timestamp string         `json:"timestamp"`
	Message   *transcriptMsg `json:"message"`
}

type transcriptMsg struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
}

// Discover scans the Claude projects directory for session transcripts.
func (a *Adapter) Discover() ([]agent.AgentSession, error) {
	root, err := agent.HomeSubdir("", ".claude", "projects")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Claude not installed / never run — not an error.
		}
		return nil, err
	}

	now := time.Now()
	var sessions []agent.AgentSession
	for _, projDir := range entries {
		if !projDir.IsDir() {
			continue
		}
		projPath := filepath.Join(root, projDir.Name())
		files, err := os.ReadDir(projPath)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			full := filepath.Join(projPath, f.Name())
			if s, ok := a.parseSession(full, now); ok {
				sessions = append(sessions, s)
			}
		}
	}
	return sessions, nil
}

// parseSession reads metadata from the head of a transcript and activity from
// its mtime. ok is false when the file yields no usable session.
func (a *Adapter) parseSession(path string, now time.Time) (agent.AgentSession, bool) {
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return agent.AgentSession{}, false
	}
	// Skip stale transcripts before the (more expensive) parse. Claude keeps
	// thousands of historical sessions; only recent ones matter live.
	if agent.TooOld(info.ModTime(), now) {
		return agent.AgentSession{}, false
	}

	f, err := os.Open(path)
	if err != nil {
		return agent.AgentSession{}, false
	}
	defer f.Close()

	s := agent.AgentSession{
		Agent:      a.Name(),
		SessionID:  strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		SourceFile: path,
		LastActive: info.ModTime(),
		Activity:   agent.ClassifyByMtime(info.ModTime(), now),
	}

	// Scan the first lines for cwd/branch/model — these are stable across the
	// session and appear early. Cap the scan to avoid large reads.
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for i := 0; i < 50 && sc.Scan(); i++ {
		var line transcriptLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			continue
		}
		if s.CWD == "" && line.CWD != "" {
			s.CWD = agent.Resolve(line.CWD)
		}
		if s.GitBranch == "" && line.GitBranch != "" {
			s.GitBranch = line.GitBranch
		}
		if s.Model == "" && line.Message != nil && line.Message.Model != "" {
			s.Model = line.Message.Model
		}
		if s.Task == "" && line.Message != nil && line.Message.Role == "user" {
			s.Task = firstText(line.Message.Content)
		}
		if line.SessionID != "" {
			s.SessionID = line.SessionID
		}
		if s.CWD != "" && s.Model != "" && s.Task != "" {
			break
		}
	}

	if s.CWD == "" {
		// Without a cwd we cannot correlate; skip rather than show a useless row.
		return agent.AgentSession{}, false
	}

	// Refine activity from the last meaningful event in the transcript.
	if tail, err := agent.TailLines(path, 40); err == nil {
		s.Activity = agent.CombineActivity(activityFromTail(tail), info.ModTime(), now)
	}
	return s, true
}

// activityFromTail classifies the fine-grained state from the last meaningful
// transcript line. An unfinished tool_use maps to its tool's state
// (reading/writing/running/…); a tool_result or user turn means the agent is
// processing (thinking); an assistant text turn that ends means waiting.
func activityFromTail(tail []string) agent.Activity {
	for i := len(tail) - 1; i >= 0; i-- {
		var line transcriptLine
		if err := json.Unmarshal([]byte(tail[i]), &line); err != nil {
			continue
		}
		switch line.Type {
		case "assistant":
			if line.Message == nil {
				continue
			}
			if tool := toolName(line.Message.Content); tool != "" {
				return agent.StateForTool(tool)
			}
			return agent.ActivityWaiting
		case "user":
			// A user turn or a tool_result feeding back means work in flight.
			return agent.ActivityThinking
		}
	}
	return agent.ActivityUnknown
}

// toolName returns the name of the first tool_use block, or "" if none.
func toolName(raw json.RawMessage) string {
	var blocks []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == "tool_use" {
			return b.Name
		}
	}
	return ""
}

// usageLine extends transcriptLine with the token-accounting and tool fields
// needed for metrics.
type usageLine struct {
	Type    string `json:"type"`
	Message *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens         int `json:"input_tokens"`
			OutputTokens        int `json:"output_tokens"`
			CacheCreationTokens int `json:"cache_creation_input_tokens"`
			CacheReadTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Timestamp string `json:"timestamp"`
}

// ComputeMetrics implements agent.Metricser with a single full transcript scan.
// Called on demand for the selected session only.
func (a *Adapter) ComputeMetrics(sourceFile string) (agent.Metrics, error) {
	f, err := os.Open(sourceFile)
	if err != nil {
		return agent.Metrics{}, err
	}
	defer f.Close()

	var m agent.Metrics
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var line usageLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil || line.Message == nil {
			continue
		}
		switch line.Message.Role {
		case "user":
			m.UserMsgs++
		case "assistant":
			m.AsstMsgs++
		}
		if u := line.Message.Usage; u != nil {
			m.TokensIn += u.InputTokens
			m.TokensOut += u.OutputTokens
			m.CacheWrite += u.CacheCreationTokens
			m.CacheRead += u.CacheReadTokens
		}
		if tool, file := toolAndFile(line.Message.Content); tool != "" {
			m.LastTool = tool
			if file != "" {
				m.LastFile = file
			}
			if ts, err := time.Parse(time.RFC3339, line.Timestamp); err == nil {
				m.LastOpAt = ts
			}
		}
	}
	return m, sc.Err()
}

// toolAndFile returns the name of the last tool_use block and the file path it
// targets (file_path / path / notebook_path input), if any.
func toolAndFile(raw json.RawMessage) (tool, file string) {
	var blocks []struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		Input struct {
			FilePath     string `json:"file_path"`
			Path         string `json:"path"`
			NotebookPath string `json:"notebook_path"`
		} `json:"input"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", ""
	}
	for _, b := range blocks {
		if b.Type == "tool_use" {
			tool = b.Name
			file = firstNonEmptyS(b.Input.FilePath, b.Input.Path, b.Input.NotebookPath)
		}
	}
	return tool, file
}

func firstNonEmptyS(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Conversation implements agent.Conversational: it returns recent events from a
// Claude transcript, normalized for display.
func (a *Adapter) Conversation(sourceFile string, limit int) ([]agent.Event, error) {
	if sourceFile == "" {
		return nil, nil
	}
	tail, err := agent.TailLines(sourceFile, limit*3) // events filtered below
	if err != nil {
		return nil, err
	}
	var events []agent.Event
	for _, l := range tail {
		var line transcriptLine
		if err := json.Unmarshal([]byte(l), &line); err != nil {
			continue
		}
		if line.Message == nil || (line.Type != "user" && line.Type != "assistant") {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, line.Timestamp)
		for _, ev := range blocksToEvents(line.Type, line.Message.Content, ts) {
			events = append(events, ev)
		}
	}
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

// blocksToEvents converts a Claude content field into display events.
func blocksToEvents(role string, raw json.RawMessage, ts time.Time) []agent.Event {
	// Plain string content.
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if asString == "" {
			return nil
		}
		return []agent.Event{{Role: role, Kind: "text", Text: snippet(asString), Time: ts}}
	}
	var blocks []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	var out []agent.Event
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, agent.Event{Role: role, Kind: "text", Text: snippet(b.Text), Time: ts})
			}
		case "tool_use":
			out = append(out, agent.Event{Role: "tool", Kind: "tool_use", Text: "⚙ " + b.Name, Time: ts})
		case "tool_result":
			out = append(out, agent.Event{Role: "tool", Kind: "tool_result", Text: "↩ result", Time: ts})
		}
	}
	return out
}

// firstText extracts a short snippet of user prompt text from a Claude content
// field, which may be a plain string or an array of typed content blocks.
func firstText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return truncate(asString)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				return truncate(b.Text)
			}
		}
	}
	return ""
}

func truncate(s string) string {
	return clip(s, 80)
}

// snippet keeps a longer single-line preview for the conversation view.
func snippet(s string) string {
	return clip(s, 600)
}

func clip(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
