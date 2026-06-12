package procscan

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct{ cmd, want string }{
		{"npm exec @github/copilot@1.0.55 --acp", "copilot"},
		{"/path/@anthropic-ai/claude-agent-sdk-darwin-arm64/claude", "claude"},
		{"node /usr/local/bin/codex serve", "codex"},
		{"opencode tui", "opencode"},
		// Should NOT match unrelated apps that merely contain "copilot".
		{"/System/.../com.microsoft.teams2/.../CopilotHelper", "copilot"}, // contains @github? no
		{"/usr/bin/some-random-process", ""},
	}
	for _, c := range cases {
		got := classify(c.cmd)
		if c.cmd == "/System/.../com.microsoft.teams2/.../CopilotHelper" {
			// This is the false-positive guard: the bare word "Copilot" must
			// not match because our needles require @github/ or the language
			// server binary name.
			if got != "" {
				t.Errorf("classify(%q) = %q, want no match (false positive)", c.cmd, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("classify(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

func TestPlausibleProjectDir(t *testing.T) {
	home := "/Users/me"
	cases := []struct {
		cwd  string
		want bool
	}{
		{"/Users/me/code/proj", true},
		{"/", false},
		{"", false},
		{home, false},
		{"/Applications/Claude.app/Contents/Helpers", false},
		{"/Users/me/Library/Caches/x", false},
		{"/Users/me/node_modules/pkg", false},
	}
	for _, c := range cases {
		if got := plausibleProjectDir(c.cwd, home); got != c.want {
			t.Errorf("plausibleProjectDir(%q) = %v, want %v", c.cwd, got, c.want)
		}
	}
}
