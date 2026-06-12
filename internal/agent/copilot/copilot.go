// Package copilot implements the AgentAdapter for GitHub Copilot CLI.
//
// Copilot CLI persists each session under
//
//	~/.copilot/session-state/<session-id>/
//	    events.jsonl     full session history (one JSON event per line)
//	    workspace.yaml   session metadata, including the working directory
//
// The config root may be relocated via COPILOT_HOME. We read the workspace
// directory from workspace.yaml (the correlation join key) and derive activity
// from the mtime of events.jsonl.
package copilot

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

// Adapter discovers GitHub Copilot CLI sessions.
type Adapter struct{}

// New returns a Copilot adapter.
func New() *Adapter { return &Adapter{} }

// Name implements agent.AgentAdapter.
func (a *Adapter) Name() string { return "copilot" }

// Discover scans the Copilot session-state directory.
func (a *Adapter) Discover() ([]agent.AgentSession, error) {
	home, err := agent.HomeSubdir("COPILOT_HOME", ".copilot")
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, "session-state")

	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Copilot CLI not installed / never run.
		}
		return nil, err
	}

	now := time.Now()
	var sessions []agent.AgentSession
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if s, ok := a.parseSession(filepath.Join(root, e.Name()), e.Name(), now); ok {
			sessions = append(sessions, s)
		}
	}
	return sessions, nil
}

func (a *Adapter) parseSession(dir, id string, now time.Time) (agent.AgentSession, bool) {
	ws := readWorkspace(filepath.Join(dir, "workspace.yaml"))
	if ws["cwd"] == "" {
		return agent.AgentSession{}, false
	}

	s := agent.AgentSession{
		Agent:     a.Name(),
		SessionID: id,
		CWD:       agent.Resolve(ws["cwd"]),
		GitBranch: ws["branch"],
		Task:      ws["summary"],
		Activity:  agent.ActivityIdle,
	}

	// Activity and last-active come from the events log when present.
	events := filepath.Join(dir, "events.jsonl")
	if info, err := os.Stat(events); err == nil {
		s.LastActive = info.ModTime()
		s.SourceFile = events
		content := agent.ActivityUnknown
		if tail, err := agent.TailLines(events, 30); err == nil {
			content = activityFromTail(tail)
		}
		s.Activity = agent.CombineActivity(content, info.ModTime(), now)
	} else if info, err := os.Stat(dir); err == nil {
		s.LastActive = info.ModTime()
		s.Activity = agent.ClassifyByMtime(info.ModTime(), now)
	}

	if agent.TooOld(s.LastActive, now) {
		return agent.AgentSession{}, false
	}
	return s, true
}

// copilotEvent is the subset of an events.jsonl line we read. Copilot uses
// dotted event types (e.g. "assistant.turn_start") with content under data.
type copilotEvent struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Data      struct {
		Role         string `json:"role"`
		Content      string `json:"content"`
		Name         string `json:"name"`
		Tool         string `json:"tool"`
		ToolName     string `json:"toolName"` // Copilot's actual tool-name field
		Model        string `json:"model"`
		OutputTokens int    `json:"outputTokens"`
	} `json:"data"`
}

// toolName returns the tool name from whichever field Copilot used.
func (e copilotEvent) toolName() string {
	if e.Data.ToolName != "" {
		return e.Data.ToolName
	}
	if e.Data.Name != "" {
		return e.Data.Name
	}
	return e.Data.Tool
}

// activityFromTail derives the fine-grained state from the event tail. An
// in-flight tool execution maps to its tool's state; an open assistant turn is
// thinking; a closed turn is waiting.
func activityFromTail(tail []string) agent.Activity {
	for i := len(tail) - 1; i >= 0; i-- {
		var ev copilotEvent
		if err := json.Unmarshal([]byte(tail[i]), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "tool.execution_start":
			return agent.StateForTool(ev.toolName())
		case "tool.execution_complete", "assistant.turn_start", "assistant.message":
			return agent.ActivityThinking
		case "assistant.turn_end":
			return agent.ActivityWaiting
		case "user.message":
			return agent.ActivityThinking
		}
	}
	return agent.ActivityUnknown
}

// Conversation implements agent.Conversational for Copilot CLI.
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
		var ev copilotEvent
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, ev.Timestamp)
		switch ev.Type {
		case "user.message", "assistant.message":
			if ev.Data.Content == "" {
				continue
			}
			role := "assistant"
			if ev.Type == "user.message" {
				role = "user"
			}
			events = append(events, agent.Event{Role: role, Kind: "text", Text: clip(ev.Data.Content, 600), Time: ts})
		case "tool.execution_start":
			name := ev.toolName()
			if name == "" {
				name = "tool"
			}
			events = append(events, agent.Event{Role: "tool", Kind: "tool_use", Text: "⚙ " + name, Time: ts})
		}
	}
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

func clip(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// wantedKeys are the top-level workspace.yaml fields we extract. Copilot CLI
// writes these as simple "key: value" lines (verified against real sessions).
var wantedKeys = []string{"cwd", "branch", "summary", "git_root", "repository"}

// readWorkspace does a dependency-free scan of workspace.yaml for a small set of
// top-level scalar keys. Returns a map of the keys it found.
func readWorkspace(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		// Only top-level keys (no leading indentation) to avoid nested fields.
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		for _, w := range wantedKeys {
			if key == w {
				out[w] = strings.Trim(strings.TrimSpace(val), `"'`)
				break
			}
		}
	}
	return out
}

// ComputeMetrics implements agent.Metricser. Copilot records per-message
// outputTokens (no input token count) plus tool executions, so we report
// message counts, output tokens, and the last tool. Input tokens stay zero.
func (a *Adapter) ComputeMetrics(sourceFile string) (agent.Metrics, error) {
	tail, err := agent.TailLines(sourceFile, 100000) // whole file; events are small
	if err != nil {
		return agent.Metrics{}, err
	}
	var m agent.Metrics
	for _, l := range tail {
		var ev copilotEvent
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "user.message":
			m.UserMsgs++
		case "assistant.message":
			m.AsstMsgs++
			m.TokensOut += ev.Data.OutputTokens
		case "tool.execution_start":
			if tool := ev.toolName(); tool != "" {
				m.LastTool = tool
				if ts, err := time.Parse(time.RFC3339, ev.Timestamp); err == nil {
					m.LastOpAt = ts
				}
			}
		}
	}
	return m, nil
}
