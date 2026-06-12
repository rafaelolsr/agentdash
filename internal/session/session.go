// Package session defines the unified Session row rendered by the TUI and the
// optional JSON overlay that supplies task/notes metadata not observable from
// git or agent transcripts.
package session

import (
	"time"

	"github.com/datageek/agentdash/internal/agent"
	"github.com/datageek/agentdash/internal/gitutil"
)

// Session is the merged agent + git view of one unit of work, as shown in the
// dashboard. Either half may be empty: a worktree with no live agent still
// appears, and an agent outside any watched repo appears unmatched.
type Session struct {
	// Agent half
	Agent      string
	SessionID  string
	Model      string
	Activity   agent.Activity
	PID        int
	LastActive time.Time
	SourceFile string // transcript path, for the conversation view

	// Git half
	RepoPath      string
	WorktreePath  string
	Branch        string
	Base          string
	ChangedFiles  []string
	Diff          gitutil.DiffStat
	Ahead         int
	Behind        int
	Commits       []gitutil.Commit
	LastCommit    gitutil.Commit
	Mergeable     bool
	MergeChecked  bool
	ConflictFiles []string

	// Rich metrics (lazily computed for the selected session)
	Metrics agent.Metrics

	// Correlated / overlay
	Task         string   // from agent transcript or JSON overlay
	Notes        string   // from JSON overlay
	CollidesWith []string // session keys touching the same files
	Matched      bool     // true when an agent was joined to a worktree
}

// Bucket is a board column: what the supervisor must do about this session.
type Bucket int

const (
	BucketNeedsInput Bucket = iota // agent waiting on the user
	BucketWorking                  // actively running
	BucketColliding                // shares changed files with another session
	BucketReadyMerge               // ahead of base and merges clean
	BucketDone                     // idle / exited / nothing to do
)

// Board returns the column this session belongs in. Collisions take priority
// (most urgent), then waiting-for-input, then working, then merge-ready.
func (s Session) Board() Bucket {
	if len(s.CollidesWith) > 0 {
		return BucketColliding
	}
	if s.Activity == agent.ActivityWaiting {
		return BucketNeedsInput
	}
	if s.Activity.Active() {
		return BucketWorking
	}
	if s.MergeChecked && s.Mergeable && s.Ahead > 0 {
		return BucketReadyMerge
	}
	return BucketDone
}

// HasTranscript reports whether this session has a readable conversation
// transcript (as opposed to being discovered only via process scan).
func (s Session) HasTranscript() bool {
	return s.SourceFile != "" && s.SourceFile != "(process scan)"
}

// Key is a human-readable label for the session, used in collision reports.
// It is NOT guaranteed unique (many sessions can share branch "HEAD").
func (s Session) Key() string {
	if s.Branch != "" {
		return s.Branch
	}
	if s.SessionID != "" {
		return s.Agent + ":" + s.SessionID
	}
	return s.WorktreePath
}

// GroupKey identifies the project a session belongs to, for grouping the fleet
// view. The project is the repository (so all of a repo's worktrees group
// together), falling back to the worktree directory, then the agent's cwd.
// Never the branch — two repos on "main" are different projects.
func (s Session) GroupKey() string {
	if s.RepoPath != "" {
		return baseName(s.RepoPath)
	}
	if s.WorktreePath != "" {
		return baseName(s.WorktreePath)
	}
	return "ungrouped"
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// ID is a stable, unique identifier for caching per-session data (metrics,
// history). Prefers the transcript path, then agent+session id, then worktree.
func (s Session) ID() string {
	if s.SourceFile != "" && s.SourceFile != "(process scan)" {
		return s.SourceFile
	}
	if s.SessionID != "" {
		return s.Agent + ":" + s.SessionID
	}
	if s.WorktreePath != "" {
		return s.Agent + "@" + s.WorktreePath
	}
	return s.Key()
}
