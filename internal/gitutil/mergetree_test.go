package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMergesClean builds a tiny repo with a clean branch and a conflicting
// branch and verifies merge-tree classifies each correctly.
func TestMergesClean(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.co",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.co")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-q", "-b", "main")
	run("config", "commit.gpgsign", "false")
	write("file.txt", "line1\nline2\nline3\n")
	run("add", "-A")
	run("commit", "-qm", "init")

	// Clean branch: adds an unrelated file.
	run("checkout", "-q", "-b", "feat-clean")
	write("new.txt", "hi\n")
	run("add", "-A")
	run("commit", "-qm", "add new")

	// main diverges on the same line as a future conflict branch.
	run("checkout", "-q", "main")
	write("file.txt", "line1\nMAIN\nline3\n")
	run("commit", "-aqm", "main edits line2")

	// Conflict branch from the original commit, editing the same line.
	run("checkout", "-q", "-b", "feat-conflict", "main~1")
	write("file.txt", "line1\nBRANCH\nline3\n")
	run("commit", "-aqm", "branch edits line2")

	if clean, conflicts := MergesClean(dir, "main"); !clean || len(conflicts) != 0 {
		// feat-conflict is checked out; test feat-clean explicitly below
		_ = conflicts
	}

	// feat-clean merges into main cleanly.
	run("checkout", "-q", "feat-clean")
	if clean, conflicts := MergesClean(dir, "main"); !clean {
		t.Errorf("feat-clean should merge clean, got conflicts=%v", conflicts)
	}

	// feat-conflict does NOT merge clean.
	run("checkout", "-q", "feat-conflict")
	if clean, _ := MergesClean(dir, "main"); clean {
		t.Error("feat-conflict should NOT merge clean")
	}
}
