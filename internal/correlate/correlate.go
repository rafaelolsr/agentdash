// Package correlate joins discovered agent sessions to git worktrees and
// detects cross-worktree file collisions.
//
// The join key is the filesystem path: an agent's working directory is matched
// to the worktree that contains it (the agent may run in a subdirectory of the
// worktree, so the match walks up the path). Collision detection intersects the
// changed-file sets across all worktrees so two agents editing the same file
// are flagged.
package correlate

import (
	"path/filepath"
	"sort"

	"github.com/datageek/agentdash/internal/agent"
	"github.com/datageek/agentdash/internal/session"
	"github.com/datageek/agentdash/internal/worktree"
)

// Build merges worktrees and agent sessions into unified rows and annotates
// cross-worktree collisions. Equivalent to BuildWithOverlays with no overlays.
func Build(worktrees []worktree.Worktree, agents []agent.AgentSession) []session.Session {
	return BuildWithOverlays(worktrees, agents, nil)
}

// BuildWithOverlays is Build plus optional user-supplied task/notes metadata,
// keyed by resolved worktree path.
func BuildWithOverlays(worktrees []worktree.Worktree, agents []agent.AgentSession, overlays map[string]session.Overlay) []session.Session {
	// Index worktrees by absolute path for the longest-prefix match.
	wtByPath := make(map[string]worktree.Worktree, len(worktrees))
	wtOrder := make([]string, 0, len(worktrees))
	for _, wt := range worktrees {
		wtByPath[wt.Path] = wt
		wtOrder = append(wtOrder, wt.Path)
	}

	// Each agent becomes its own session; multiple agents in the same worktree
	// all appear (each sharing that worktree's git data). Track which worktrees
	// got at least one agent so we can still show empty worktrees afterward.
	var sessions []session.Session
	matchedWT := make(map[string]bool)
	for _, as := range agents {
		if wtPath := matchWorktreePath(as.CWD, wtByPath); wtPath != "" {
			s := fromWorktree(wtByPath[wtPath])
			mergeAgent(&s, as)
			s.Matched = true
			sessions = append(sessions, s)
			matchedWT[wtPath] = true
		} else {
			sessions = append(sessions, fromAgent(as))
		}
	}

	// Worktrees with no agent attached still appear (git-only rows).
	for _, p := range wtOrder {
		if !matchedWT[p] {
			sessions = append(sessions, fromWorktree(wtByPath[p]))
		}
	}

	applyOverlays(sessions, overlays)
	detectCollisions(sessions)
	sortSessions(sessions)
	return sessions
}

// sortSessions orders the list the way a supervisor scans it: sessions with a
// readable transcript first (those have a conversation and rich activity),
// then by most-recent activity. This keeps process-scan-only rows — which have
// no conversation — from crowding out the real sessions.
func sortSessions(sessions []session.Session) {
	// Rank each group by its most recent activity so busy projects float up,
	// then keep a group's sessions contiguous (for the grouped fleet view).
	groupRecency := map[string]int64{}
	for _, s := range sessions {
		if t := s.LastActive.Unix(); t > groupRecency[s.GroupKey()] {
			groupRecency[s.GroupKey()] = t
		}
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		ga, gb := a.GroupKey(), b.GroupKey()
		if ga != gb {
			ra, rb := groupRecency[ga], groupRecency[gb]
			if ra != rb {
				return ra > rb // most-recently-active group first
			}
			return ga < gb
		}
		// Within a group: transcript-backed first, then most recent.
		if a.HasTranscript() != b.HasTranscript() {
			return a.HasTranscript()
		}
		return a.LastActive.After(b.LastActive)
	})
}

// applyOverlays attaches user-supplied task/notes to sessions by worktree path.
func applyOverlays(sessions []session.Session, overlays map[string]session.Overlay) {
	if len(overlays) == 0 {
		return
	}
	for i := range sessions {
		o, ok := overlays[sessions[i].WorktreePath]
		if !ok {
			continue
		}
		if sessions[i].Task == "" {
			sessions[i].Task = o.Task
		}
		sessions[i].Notes = o.Notes
	}
}

// matchWorktreePath returns the worktree path that contains cwd, choosing the
// longest matching prefix (the most specific worktree). Returns "" if none.
//
// It walks cwd up its parent directories, taking the first worktree it hits —
// which is necessarily the deepest (most specific) one. That's O(path depth)
// map lookups instead of scanning every worktree for every agent.
func matchWorktreePath(cwd string, byPath map[string]worktree.Worktree) string {
	if cwd == "" {
		return ""
	}
	for p := filepath.Clean(cwd); ; {
		if _, ok := byPath[p]; ok {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "" // reached the filesystem root without a match
		}
		p = parent
	}
}

func fromWorktree(wt worktree.Worktree) session.Session {
	return session.Session{
		Activity:      agent.ActivityIdle,
		RepoPath:      wt.RepoPath,
		WorktreePath:  wt.Path,
		Branch:        wt.Branch,
		Base:          wt.Base,
		ChangedFiles:  wt.ChangedFiles,
		Diff:          wt.Diff,
		Ahead:         wt.Ahead,
		Behind:        wt.Behind,
		Commits:       wt.Commits,
		LastCommit:    wt.LastCommit,
		Mergeable:     wt.Mergeable,
		MergeChecked:  wt.MergeChecked,
		ConflictFiles: wt.ConflictFiles,
	}
}

func fromAgent(as agent.AgentSession) session.Session {
	return session.Session{
		Agent:        as.Agent,
		SessionID:    as.SessionID,
		Model:        as.Model,
		Activity:     as.Activity,
		PID:          as.PID,
		LastActive:   as.LastActive,
		WorktreePath: as.CWD,
		Branch:       as.GitBranch,
		Task:         as.Task,
		SourceFile:   as.SourceFile,
	}
}

// mergeAgent copies an agent's fields onto a worktree-derived session. Each
// agent gets its own session, so there's no winner logic — multiple agents in
// the same worktree all appear, each sharing the worktree's git data.
func mergeAgent(s *session.Session, as agent.AgentSession) {
	s.Agent = as.Agent
	s.SessionID = as.SessionID
	s.Model = as.Model
	s.Activity = as.Activity
	s.PID = as.PID
	s.LastActive = as.LastActive
	s.SourceFile = as.SourceFile
	if as.GitBranch != "" {
		// Agent's recorded branch is more specific than the worktree HEAD label.
		s.Branch = as.GitBranch
	}
	if s.Task == "" {
		s.Task = as.Task
	}
}

// detectCollisions flags sessions whose changed-file sets overlap. Comparison
// is by repo-relative path within the same repository.
func detectCollisions(sessions []session.Session) {
	type owner struct {
		idx int
		key string
	}
	fileOwners := map[string][]owner{}

	for i := range sessions {
		s := &sessions[i]
		for _, f := range s.ChangedFiles {
			id := s.RepoPath + "::" + f
			fileOwners[id] = append(fileOwners[id], owner{idx: i, key: s.Key()})
		}
	}

	collisions := make([]map[string]struct{}, len(sessions))
	for _, owners := range fileOwners {
		if len(owners) < 2 {
			continue
		}
		for _, a := range owners {
			for _, b := range owners {
				if a.idx == b.idx {
					continue
				}
				if collisions[a.idx] == nil {
					collisions[a.idx] = map[string]struct{}{}
				}
				collisions[a.idx][b.key] = struct{}{}
			}
		}
	}

	for i := range sessions {
		if collisions[i] == nil {
			continue
		}
		keys := make([]string, 0, len(collisions[i]))
		for k := range collisions[i] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sessions[i].CollidesWith = keys
	}
}
