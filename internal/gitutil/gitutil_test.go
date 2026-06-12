package gitutil

import "testing"

func TestParseStatusShort(t *testing.T) {
	in := " M internal/auth/oauth.go\n" +
		"A  internal/auth/token.go\n" +
		" D StarBaseAIAssistant/.env.example\n" +
		"?? new.txt\n" +
		`R  old.go -> internal/new.go` + "\n"
	got := parseStatusShort(in)
	want := []string{"internal/auth/oauth.go", "internal/auth/token.go", "StarBaseAIAssistant/.env.example", "new.txt", "internal/new.go"}
	if len(got) != len(want) {
		t.Fatalf("got %d files %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestParseNumstat(t *testing.T) {
	in := "10\t2\tmain.go\n5\t0\tutil.go\n-\t-\timage.png\n"
	st := parseNumstat(in)
	if st.FilesChanged != 3 {
		t.Errorf("FilesChanged: got %d want 3", st.FilesChanged)
	}
	if st.Insertions != 15 {
		t.Errorf("Insertions: got %d want 15", st.Insertions)
	}
	if st.Deletions != 2 {
		t.Errorf("Deletions: got %d want 2", st.Deletions)
	}
}

func TestParseOneline(t *testing.T) {
	in := "a1b2c3d add token store\ne4f5g6h initial commit\n"
	got := parseOneline(in)
	if len(got) != 2 {
		t.Fatalf("got %d commits want 2", len(got))
	}
	if got[0].Hash != "a1b2c3d" || got[0].Subject != "add token store" {
		t.Errorf("commit[0] = %+v", got[0])
	}
}
