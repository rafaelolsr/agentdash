package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Overlay is the optional, user-supplied metadata for a session that cannot be
// observed from git or agent transcripts (task description, notes). It is keyed
// by worktree path so the correlator can attach it. Stored as JSON at
// <home>/sessions/<id>.json. This is the schema from the original spec, used
// here as enrichment rather than as the source of truth.
type Overlay struct {
	SessionID    string   `json:"session_id"`
	Agent        string   `json:"agent,omitempty"`
	Model        string   `json:"model,omitempty"`
	RepoPath     string   `json:"repo_path,omitempty"`
	WorktreePath string   `json:"worktree_path"`
	Branch       string   `json:"branch,omitempty"`
	Task         string   `json:"task,omitempty"`
	Notes        string   `json:"notes,omitempty"`
	Command      []string `json:"command,omitempty"`
	NotesPath    string   `json:"notes_path,omitempty"`
}

// LoadOverlay reads a single overlay JSON file.
func LoadOverlay(path string) (Overlay, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Overlay{}, err
	}
	var o Overlay
	if err := json.Unmarshal(data, &o); err != nil {
		return Overlay{}, fmt.Errorf("parsing overlay %s: %w", path, err)
	}
	return o, nil
}

// SaveOverlay writes an overlay JSON file, creating the directory if needed.
func SaveOverlay(dir string, o Overlay) (string, error) {
	if o.SessionID == "" {
		return "", fmt.Errorf("overlay session_id is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, o.SessionID+".json")
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// LoadOverlays reads every *.json overlay in dir, keyed by resolved worktree
// path for easy correlation. Missing dir yields an empty map, not an error.
func LoadOverlays(dir string) map[string]Overlay {
	out := map[string]Overlay{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		o, err := LoadOverlay(filepath.Join(dir, e.Name()))
		if err != nil || o.WorktreePath == "" {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(o.WorktreePath); err == nil {
			out[resolved] = o
		} else {
			out[o.WorktreePath] = o
		}
	}
	return out
}

var (
	slugNonAlnum   = regexp.MustCompile(`[^a-z0-9]+`)
	slugTrimDashes = regexp.MustCompile(`^-+|-+$`)
)

// SafeSlug converts a free-form task description into a filesystem- and
// branch-safe slug: lowercase, alphanumerics and dashes only, collapsed and
// trimmed, length-capped. Empty or all-symbol input yields "session".
func SafeSlug(task string) string {
	s := strings.ToLower(strings.TrimSpace(task))
	s = slugNonAlnum.ReplaceAllString(s, "-")
	s = slugTrimDashes.ReplaceAllString(s, "")
	const max = 40
	if len(s) > max {
		s = s[:max]
		s = slugTrimDashes.ReplaceAllString(s, "")
	}
	if s == "" {
		return "session"
	}
	return s
}
