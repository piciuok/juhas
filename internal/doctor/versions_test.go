package doctor

import "testing"

func TestCheckVersion(t *testing.T) {
	cases := []struct {
		name, line string
		want       Level
	}{
		{"gh", "gh version 2.98.0 (2026-08-20)", OK},
		{"gh", "gh version 2.21.2 (2023-01-05)", Warn},
		{"glab", "glab 1.108.0 (5de20850)", OK},
		{"glab", "glab version 1.22.0", Warn},
		{"claude", "2.1.308 (Claude Code)", OK},
		{"claude", "1.0.88 (Claude Code)", Warn},
		{"codex", "codex-cli 0.163.0", OK},
		{"codex", "codex-cli 0.31.0", Warn},
		{"git", "git version 2.50.1 (Apple Git-155)", OK},
		{"git", "git version 2.17.1", Warn},
		{"gh", "?", OK},
		{"jq", "jq-1.6", OK},
	}
	for _, c := range cases {
		if l, msg, hint := checkVersion(c.name, c.line); l != c.want || (l == Warn && (msg == "" || hint == "")) {
			t.Errorf("%s %q = %v %q %q, want %v", c.name, c.line, l, msg, hint, c.want)
		}
	}
}
