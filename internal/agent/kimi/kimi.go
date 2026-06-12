// Package kimi implements the AgentAdapter for Kimi Code CLI.
//
// Kimi stores runtime data under ~/.kimi-code (relocatable via KIMI_CODE_HOME):
//
//	config.toml
//	sessions/                 per-session data
//	session_index.jsonl       index of sessions
//	user-history/<md5(workDir)>.jsonl
//
// The exact per-field schema is not publicly documented and Kimi was not
// available to inspect locally, so this adapter is deliberately tolerant: it
// reads the session index (and falls back to session files), accepting any of
// several common field names for the working directory, timestamp, and message
// role/text. It fails soft — a session is only emitted when a working
// directory can be determined. Field handling should be revisited against a
// real Kimi install.
package kimi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/datageek/agentdash/internal/agent"
)

func init() { agent.Register(New()) }

// Adapter discovers Kimi CLI sessions.
type Adapter struct{}

// New returns a Kimi adapter.
func New() *Adapter { return &Adapter{} }

// Name implements agent.AgentAdapter.
func (a *Adapter) Name() string { return "kimi" }

func (a *Adapter) home() (string, error) {
	return agent.HomeSubdir("KIMI_CODE_HOME", ".kimi-code")
}

// flexRecord captures the union of field names Kimi might use, so the adapter
// keeps working across versions without a confirmed schema.
type flexRecord struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`

	CWD       string `json:"cwd"`
	WorkDir   string `json:"work_dir"`
	Directory string `json:"directory"`
	Path      string `json:"path"`
	Project   string `json:"project"`

	Model   string `json:"model"`
	Branch  string `json:"branch"`
	Title   string `json:"title"`
	Summary string `json:"summary"`

	UpdatedAt string `json:"updated_at"`
	Timestamp string `json:"timestamp"`
	Time      string `json:"time"`
}

func (r flexRecord) cwd() string {
	return firstNonEmpty(r.CWD, r.WorkDir, r.Directory, r.Path, r.Project)
}

func (r flexRecord) sessionID() string {
	return firstNonEmpty(r.ID, r.SessionID)
}

func (r flexRecord) when() time.Time {
	for _, v := range []string{r.UpdatedAt, r.Timestamp, r.Time} {
		if v == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

// Discover reads the session index, then enriches from session files.
func (a *Adapter) Discover() ([]agent.AgentSession, error) {
	home, err := a.home()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(home); os.IsNotExist(err) {
		return nil, nil // Kimi not installed
	}

	now := time.Now()
	var sessions []agent.AgentSession

	// Primary source: session_index.jsonl (one record per line).
	indexPath := filepath.Join(home, "session_index.jsonl")
	if lines, err := agent.TailLines(indexPath, 200); err == nil {
		for _, l := range lines {
			var r flexRecord
			if err := json.Unmarshal([]byte(l), &r); err != nil {
				continue
			}
			cwd := r.cwd()
			if cwd == "" {
				continue
			}
			last := r.when()
			if last.IsZero() {
				if st, err := os.Stat(indexPath); err == nil {
					last = st.ModTime()
				}
			}
			if agent.TooOld(last, now) {
				continue
			}
			src := filepath.Join(home, "sessions", r.sessionID()+".jsonl")
			sessions = append(sessions, agent.AgentSession{
				Agent:      a.Name(),
				SessionID:  r.sessionID(),
				CWD:        agent.Resolve(cwd),
				Model:      r.Model,
				GitBranch:  r.Branch,
				Task:       firstNonEmpty(r.Title, r.Summary),
				LastActive: last,
				SourceFile: src,
				Activity:   a.activity(src, last, now),
			})
		}
	}
	return sessions, nil
}

func (a *Adapter) activity(src string, last, now time.Time) agent.Activity {
	tail, err := agent.TailLines(src, 20)
	if err != nil {
		return agent.CombineActivity(agent.ActivityUnknown, last, now)
	}
	content := agent.ActivityUnknown
	for i := len(tail) - 1; i >= 0; i-- {
		role, _, tool := parseMessage(tail[i])
		switch {
		case tool != "":
			content = agent.StateForTool(tool)
		case role == "assistant":
			content = agent.ActivityWaiting
		case role == "user":
			content = agent.ActivityThinking
		default:
			continue
		}
		break
	}
	return agent.CombineActivity(content, last, now)
}

// Conversation implements agent.Conversational, reading the per-session JSONL.
func (a *Adapter) Conversation(sourceFile string, limit int) ([]agent.Event, error) {
	if sourceFile == "" {
		return nil, nil
	}
	tail, err := agent.TailLines(sourceFile, limit*2)
	if err != nil {
		return nil, nil // session file may not exist; not fatal
	}
	var events []agent.Event
	for _, l := range tail {
		role, text, tool := parseMessage(l)
		switch {
		case tool != "":
			events = append(events, agent.Event{Role: "tool", Kind: "tool_use", Text: "⚙ " + clip(tool, 60)})
		case text != "":
			if role == "" {
				role = "assistant"
			}
			events = append(events, agent.Event{Role: role, Kind: "text", Text: clip(text, 600)})
		}
	}
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

// parseMessage tolerantly extracts (role, text, toolName) from a Kimi message
// line, accepting several plausible field shapes.
func parseMessage(line string) (role, text, tool string) {
	var m struct {
		Role    string          `json:"role"`
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
		Text    string          `json:"text"`
		Message string          `json:"message"`
		Tool    string          `json:"tool"`
		Name    string          `json:"name"`
	}
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return "", "", ""
	}
	tool = firstNonEmpty(m.Tool, m.Name)
	if m.Type == "tool_use" || m.Type == "tool_call" {
		if tool == "" {
			tool = "tool"
		}
		return "", "", tool
	}
	role = m.Role
	text = firstNonEmpty(m.Text, m.Message, contentText(m.Content))
	return role, text, ""
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		for _, b := range blocks {
			if b.Text != "" {
				return b.Text
			}
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func clip(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
