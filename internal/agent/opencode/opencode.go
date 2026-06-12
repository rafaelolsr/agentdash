// Package opencode implements the AgentAdapter for opencode.
//
// opencode stores state under OPENCODE_DATA_DIR (default
// ~/.local/share/opencode):
//
//	storage/session/<projectHash>/<sessionID>.json   session info
//	storage/message/<sessionID>/msg_<messageID>.json  per-message records
//
// The session JSON schema (from opencode source) includes: directory (the
// working dir — our join key), title, agent, model {id, providerID}, summary,
// and time {created, updated} as Unix-millisecond timestamps. Messages carry a
// role (user/assistant) and time; their text lives in message "parts".
//
// Built from the documented/source schema; not live-tested against a local
// install. The parser is tolerant of missing fields and fails soft.
package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/datageek/agentdash/internal/agent"
)

func init() { agent.Register(New()) }

// Adapter discovers opencode sessions.
type Adapter struct{}

// New returns an opencode adapter.
func New() *Adapter { return &Adapter{} }

// Name implements agent.AgentAdapter.
func (a *Adapter) Name() string { return "opencode" }

type sessionInfo struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Agent     string `json:"agent"`
	Model     struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
	} `json:"model"`
	Time struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

func (a *Adapter) dataDir() (string, error) {
	return agent.HomeSubdir("OPENCODE_DATA_DIR", ".local", "share", "opencode")
}

// Discover walks storage/session/*/*.json.
func (a *Adapter) Discover() ([]agent.AgentSession, error) {
	dir, err := a.dataDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(dir, "storage", "session")

	now := time.Now()
	var sessions []agent.AgentSession
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		if s, ok := a.parseSession(dir, path, now); ok {
			sessions = append(sessions, s)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return sessions, nil
	}
	return sessions, nil
}

func (a *Adapter) parseSession(dataDir, path string, now time.Time) (agent.AgentSession, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return agent.AgentSession{}, false
	}
	var info sessionInfo
	if err := json.Unmarshal(data, &info); err != nil || info.Directory == "" {
		return agent.AgentSession{}, false
	}

	last := unixMillis(info.Time.Updated)
	if last.IsZero() {
		if st, err := os.Stat(path); err == nil {
			last = st.ModTime()
		}
	}
	if agent.TooOld(last, now) {
		return agent.AgentSession{}, false
	}

	s := agent.AgentSession{
		Agent:      a.Name(),
		SessionID:  info.ID,
		CWD:        agent.Resolve(info.Directory),
		Task:       firstNonEmpty(info.Title, info.Summary),
		LastActive: last,
		SourceFile: path,
	}
	if info.Model.ID != "" {
		s.Model = info.Model.ID
	} else if info.Model.ProviderID != "" {
		s.Model = info.Model.ProviderID
	}

	// Activity from the most recent message in storage/message/<sessionID>/.
	s.Activity = agent.CombineActivity(a.activity(dataDir, info.ID), last, now)
	return s, true
}

// messageInfo is the subset of a message file we read.
type messageInfo struct {
	Role string `json:"role"`
	Time struct {
		Created int64 `json:"created"`
	} `json:"time"`
}

// messageFiles returns the message file paths for a session, sorted by name
// (message ids are time-ordered).
func (a *Adapter) messageFiles(dataDir, sessionID string) []string {
	msgDir := filepath.Join(dataDir, "storage", "message", sessionID)
	entries, err := os.ReadDir(msgDir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			files = append(files, filepath.Join(msgDir, e.Name()))
		}
	}
	sort.Strings(files)
	return files
}

// activity infers state from the last message: a trailing user message means
// the agent is thinking; a trailing assistant message means it is waiting.
func (a *Adapter) activity(dataDir, sessionID string) agent.Activity {
	files := a.messageFiles(dataDir, sessionID)
	if len(files) == 0 {
		return agent.ActivityUnknown
	}
	data, err := os.ReadFile(files[len(files)-1])
	if err != nil {
		return agent.ActivityUnknown
	}
	var m messageInfo
	if err := json.Unmarshal(data, &m); err != nil {
		return agent.ActivityUnknown
	}
	switch m.Role {
	case "user":
		return agent.ActivityThinking
	case "assistant":
		return agent.ActivityWaiting
	}
	return agent.ActivityUnknown
}

// Conversation implements agent.Conversational. The session id is recovered
// from the source file name; messages and their text parts are read from the
// message directory.
func (a *Adapter) Conversation(sourceFile string, limit int) ([]agent.Event, error) {
	if sourceFile == "" {
		return nil, nil
	}
	dataDir, err := a.dataDir()
	if err != nil {
		return nil, err
	}
	sessionID := strings.TrimSuffix(filepath.Base(sourceFile), ".json")
	files := a.messageFiles(dataDir, sessionID)
	if len(files) > limit {
		files = files[len(files)-limit:]
	}

	var events []agent.Event
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		role, text := messageRoleText(data)
		if text == "" {
			continue
		}
		events = append(events, agent.Event{Role: role, Kind: "text", Text: clip(text, 600)})
	}
	return events, nil
}

// messageRoleText extracts the role and concatenated text parts from a message
// file. opencode stores parts either inline under "parts" or as {text} blocks.
func messageRoleText(data []byte) (role, text string) {
	var m struct {
		Role  string `json:"role"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", ""
	}
	role = m.Role
	if m.Text != "" {
		return role, m.Text
	}
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Text != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(p.Text)
		}
	}
	return role, b.String()
}

func unixMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
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
