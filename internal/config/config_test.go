package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envHome, dir)

	in := Default()
	repoDir := t.TempDir() // a real, resolvable path
	in.Repos = []string{repoDir}
	in.RefreshSeconds = 5
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RefreshSeconds != 5 {
		t.Errorf("RefreshSeconds = %d want 5", got.RefreshSeconds)
	}
	if len(got.Repos) != 1 {
		t.Fatalf("Repos = %v", got.Repos)
	}
	// Path should be expanded to an absolute, resolved form.
	if !filepath.IsAbs(got.Repos[0]) {
		t.Errorf("repo path not absolute: %q", got.Repos[0])
	}
}

func TestLoadMissingConfigIsActionable(t *testing.T) {
	t.Setenv(envHome, t.TempDir())
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing config")
	}
	if want := "agentdash init"; !contains(err.Error(), want) {
		t.Errorf("error %q should mention %q", err.Error(), want)
	}
}

func TestEnsureDirs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envHome, dir)
	if err := EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	for _, sub := range []string{"sessions", "history"} {
		if _, err := os.Stat(filepath.Join(dir, sub)); err != nil {
			t.Errorf("missing dir %s: %v", sub, err)
		}
	}
}

func TestExpandPathTilde(t *testing.T) {
	home, _ := os.UserHomeDir()
	got, err := ExpandPath("~/somewhere")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "somewhere")
	if got != want {
		t.Errorf("ExpandPath(~/somewhere) = %q want %q", got, want)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
