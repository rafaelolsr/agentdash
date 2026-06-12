package session

import (
	"path/filepath"
	"testing"
)

func TestSafeSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Wire up OAuth login flow", "wire-up-oauth-login-flow"},
		{"  Fix the Bug!! (urgent)  ", "fix-the-bug-urgent"},
		{"feature/add-thing", "feature-add-thing"},
		{"", "session"},
		{"!!!", "session"},
		{"a very long task description that should be truncated to a reasonable length for branches", "a-very-long-task-description-that-should"},
	}
	for _, c := range cases {
		if got := SafeSlug(c.in); got != c.want {
			t.Errorf("SafeSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSafeSlugNoTrailingDashAfterTruncate(t *testing.T) {
	// Truncation must not leave a dangling dash.
	got := SafeSlug("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa bbb")
	if got[len(got)-1] == '-' {
		t.Errorf("slug ends with dash: %q", got)
	}
}

func TestOverlayRoundTrip(t *testing.T) {
	dir := t.TempDir()
	wt := t.TempDir()
	in := Overlay{
		SessionID:    "sess-1",
		WorktreePath: wt,
		Task:         "demo task",
		Notes:        "remember to run migrations",
	}
	path, err := SaveOverlay(dir, in)
	if err != nil {
		t.Fatalf("SaveOverlay: %v", err)
	}
	if filepath.Base(path) != "sess-1.json" {
		t.Errorf("unexpected overlay filename: %s", path)
	}

	got, err := LoadOverlay(path)
	if err != nil {
		t.Fatalf("LoadOverlay: %v", err)
	}
	if got.Task != "demo task" || got.Notes != "remember to run migrations" {
		t.Errorf("round trip mismatch: %+v", got)
	}

	byPath := LoadOverlays(dir)
	resolved, _ := filepath.EvalSymlinks(wt)
	if _, ok := byPath[resolved]; !ok {
		t.Errorf("LoadOverlays missing key %q; have %v", resolved, byPath)
	}
}

func TestSaveOverlayRequiresID(t *testing.T) {
	if _, err := SaveOverlay(t.TempDir(), Overlay{WorktreePath: "/x"}); err == nil {
		t.Fatal("expected error when session_id is empty")
	}
}
