// Package codex implements the AgentAdapter for OpenAI Codex CLI.
//
// Codex persists each session as a rollout JSONL file at
//
//	~/.codex/sessions/YYYY/MM/DD/rollout-<timestamp>-<uuid>.jsonl
//
// (root relocatable via CODEX_HOME). Each line is {type, timestamp, payload}.
// The first line is a "session_meta" carrying cwd, git info, and model provider.
// Subsequent lines are "event_msg" (task_started, exec_command_*, token_count,
// task_complete, agent_message) and "response_item" (message, reasoning,
// function_call). Verified against real rollout files.
package codex

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

// Adapter discovers Codex CLI sessions.
type Adapter struct{}

// New returns a Codex adapter.
func New() *Adapter { return &Adapter{} }

// Name implements agent.AgentAdapter.
func (a *Adapter) Name() string { return "codex" }

type rolloutLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	CWD           string `json:"cwd"`
	ID            string `json:"id"`
	ModelProvider string `json:"model_provider"`
	Git           struct {
		Branch string `json:"branch"`
	} `json:"git"`
}

// payloadHeader pulls the discriminator fields shared by event_msg and
// response_item payloads.
type payloadHeader struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Name    string          `json:"name"`
	Command json.RawMessage `json:"command"`
	Message string          `json:"message"`
}

// Discover walks the date-bucketed sessions tree for rollout files.
func (a *Adapter) Discover() ([]agent.AgentSession, error) {
	home, err := agent.HomeSubdir("CODEX_HOME", ".codex")
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, "sessions")

	now := time.Now()
	var sessions []agent.AgentSession
	// The tree is sessions/YYYY/MM/DD/rollout-*.jsonl. WalkDir is fine; the
	// recency filter keeps us from parsing ancient files.
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable dirs, keep walking
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		if s, ok := a.parseSession(path, now); ok {
			sessions = append(sessions, s)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return sessions, nil
	}
	return sessions, nil
}

func (a *Adapter) parseSession(path string, now time.Time) (agent.AgentSession, bool) {
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return agent.AgentSession{}, false
	}
	if agent.TooOld(info.ModTime(), now) {
		return agent.AgentSession{}, false
	}

	// Read the head for session_meta.
	head, err := headLines(path, 5)
	if err != nil {
		return agent.AgentSession{}, false
	}
	s := agent.AgentSession{
		Agent:      a.Name(),
		SessionID:  strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		SourceFile: path,
		LastActive: info.ModTime(),
	}
	for _, l := range head {
		var line rolloutLine
		if err := json.Unmarshal([]byte(l), &line); err != nil {
			continue
		}
		if line.Type == "session_meta" {
			var meta sessionMeta
			if err := json.Unmarshal(line.Payload, &meta); err == nil {
				s.CWD = agent.Resolve(meta.CWD)
				s.GitBranch = meta.Git.Branch
				if meta.ModelProvider != "" {
					s.Model = meta.ModelProvider // Codex records provider, not model id
				}
				if meta.ID != "" {
					s.SessionID = meta.ID
				}
			}
			break
		}
	}
	if s.CWD == "" {
		return agent.AgentSession{}, false
	}

	if tail, err := agent.TailLines(path, 30); err == nil {
		s.Activity = agent.CombineActivity(activityFromTail(tail), info.ModTime(), now)
	}
	return s, true
}

// activityFromTail classifies from the most recent meaningful rollout event.
func activityFromTail(tail []string) agent.Activity {
	for i := len(tail) - 1; i >= 0; i-- {
		var line rolloutLine
		if err := json.Unmarshal([]byte(tail[i]), &line); err != nil {
			continue
		}
		var p payloadHeader
		_ = json.Unmarshal(line.Payload, &p)
		switch p.Type {
		case "task_complete":
			return agent.ActivityWaiting
		case "task_started", "reasoning", "agent_message", "token_count":
			return agent.ActivityThinking
		case "exec_command_begin", "exec_command_end", "exec_command":
			return agent.ActivityRunning
		case "function_call", "function_call_output":
			if p.Name != "" {
				return agent.StateForTool(p.Name)
			}
			return agent.ActivityRunning
		case "message":
			if p.Role == "user" {
				return agent.ActivityThinking
			}
			return agent.ActivityWaiting
		}
	}
	return agent.ActivityUnknown
}

// Conversation implements agent.Conversational for Codex rollouts.
func (a *Adapter) Conversation(sourceFile string, limit int) ([]agent.Event, error) {
	if sourceFile == "" {
		return nil, nil
	}
	tail, err := agent.TailLines(sourceFile, limit*4)
	if err != nil {
		return nil, err
	}
	var events []agent.Event
	for _, l := range tail {
		var line rolloutLine
		if err := json.Unmarshal([]byte(l), &line); err != nil {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, line.Timestamp)
		var p payloadHeader
		if err := json.Unmarshal(line.Payload, &p); err != nil {
			continue
		}
		switch p.Type {
		case "message":
			if txt := contentText(p.Content); txt != "" {
				role := p.Role
				if role == "" {
					role = "assistant"
				}
				events = append(events, agent.Event{Role: role, Kind: "text", Text: clip(txt, 600), Time: ts})
			}
		case "agent_message":
			if p.Message != "" {
				events = append(events, agent.Event{Role: "assistant", Kind: "text", Text: clip(p.Message, 600), Time: ts})
			}
		case "exec_command_begin", "function_call":
			label := p.Name
			if label == "" {
				label = cmdText(p.Command)
			}
			events = append(events, agent.Event{Role: "tool", Kind: "tool_use", Text: "⚙ " + clip(label, 60), Time: ts})
		}
	}
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

// contentText extracts text from a Codex message content field, which may be a
// string or an array of {type,text} blocks.
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

func cmdText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "command"
	}
	var parts []string
	if err := json.Unmarshal(raw, &parts); err == nil {
		return strings.Join(parts, " ")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return "command"
}

func clip(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// headLines reads the first n lines of a file.
func headLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for i := 0; i < n && sc.Scan(); i++ {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

// ComputeMetrics implements agent.Metricser. Codex rollouts record message
// turns and tool/exec calls but do NOT include token counts, so tokens/cost
// are left zero (shown as unavailable for such agents).
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
		var line rolloutLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			continue
		}
		var p payloadHeader
		if err := json.Unmarshal(line.Payload, &p); err != nil {
			continue
		}
		switch p.Type {
		case "message":
			switch p.Role {
			case "user":
				m.UserMsgs++
			case "assistant":
				m.AsstMsgs++
			}
		case "exec_command_begin":
			m.LastTool = "exec"
			m.LastFile = ""
			if cmd := cmdText(p.Command); cmd != "" {
				m.LastFile = cmd
			}
			if ts, err := time.Parse(time.RFC3339, line.Timestamp); err == nil {
				m.LastOpAt = ts
			}
		case "function_call":
			if p.Name != "" {
				m.LastTool = p.Name
				if ts, err := time.Parse(time.RFC3339, line.Timestamp); err == nil {
					m.LastOpAt = ts
				}
			}
		}
	}
	return m, sc.Err()
}
