package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piciuok/juhas/internal/config"
	"github.com/piciuok/juhas/internal/forge"
)

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolve(t *testing.T) {
	auto := config.Review{Skill: "auto", Fallback: "builtin"}

	repo := t.TempDir()
	touch(t, filepath.Join(repo, ".claude/skills/review-queue/SKILL.md"))
	touch(t, filepath.Join(repo, ".claude/skills/fix-review-comments/SKILL.md"))
	if m := Resolve(repo, auto); m.Kind != KindBuiltin {
		t.Errorf("only non-review *review* skills: got %+v", m)
	}
	touch(t, filepath.Join(repo, ".claude/skills/deep-review/SKILL.md"))
	if m := Resolve(repo, auto); m.Kind != KindSkill || m.Skill != "deep-review" {
		t.Errorf("glob match: got %+v", m)
	}
	touch(t, filepath.Join(repo, ".claude/skills/review/SKILL.md"))
	if m := Resolve(repo, auto); m.Skill != "review" {
		t.Errorf("exact review skill wins: got %+v", m)
	}
	if m := Resolve(repo, config.Review{Skill: "none", Fallback: "claude-code-review"}); m.Kind != KindClaudeCodeReview {
		t.Errorf("skill=none: got %+v", m)
	}
	if m := Resolve(t.TempDir(), config.Review{Skill: "auto", Fallback: "none"}); m.Kind != KindNone {
		t.Errorf("fallback none: got %+v", m)
	}
	if m := Resolve(repo, config.Review{Skill: "deep-review", Fallback: "builtin"}); m.Skill != "deep-review" || m.Source == "" {
		t.Errorf("named skill: got %+v", m)
	}
}

func TestPromptAndArgs(t *testing.T) {
	it := forge.Item{Platform: forge.GitLab, Repo: "gitlab.example.com/team/app", Number: 883, URL: "https://gitlab.example.com/team/app/-/merge_requests/883", Title: "fix(web): sort", HeadSHA: "2914842fae54b35d"}
	r := config.Review{Skill: "auto", Fallback: "builtin", MaxTurns: 60, SkillArgs: "--agents 2"}

	set := config.Settings{Review: r, Language: config.Language{Brief: "pl", Comments: "en"}}
	p := Prompt(it, Method{Kind: KindSkill, Skill: "review"}, set)
	if !strings.Contains(p, "brief (the report for me) in Polish") || !strings.Contains(p, "every \"body\" in English") || !strings.Contains(p, "juhas-review") {
		t.Errorf("languages:\n%s", p)
	}
	if !strings.Contains(p, "skill `review`") || !strings.Contains(p, "`883 --agents 2`") || !strings.Contains(p, "--post") {
		t.Errorf("skill prompt:\n%s", p)
	}
	p = Prompt(it, Method{Kind: KindBuiltin}, set)
	if !strings.Contains(p, "glab mr diff 883 -R https://gitlab.example.com/team/app") {
		t.Errorf("builtin prompt:\n%s", p)
	}

	cfg := config.Default()
	a := Args(config.Agent{Bin: "claude", Model: "opus", Effort: "high"}, r, Method{Kind: KindSkill, Skill: "review"}, "/bin/juhas guard")
	joined := strings.Join(a, " ")
	for _, want := range []string{"--permission-mode dontAsk", "--max-turns 60", "--model opus", "--effort high", "--allowedTools Read Grep Glob Skill Task TodoWrite Bash --disallowedTools Edit"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args lack %q: %s", want, joined)
		}
	}
	var settings map[string]any
	for i, v := range a {
		if v == "--settings" {
			if err := json.Unmarshal([]byte(a[i+1]), &settings); err != nil {
				t.Fatal(err)
			}
		}
	}
	if settings["hooks"] == nil {
		t.Error("no hook settings")
	}
	b := strings.Join(Args(cfg.Claude, r, Method{Kind: KindBuiltin}, ""), " ")
	if strings.Contains(b, " Bash ") || !strings.Contains(b, "Bash(glab mr diff:*)") {
		t.Errorf("builtin must not open Bash: %s", b)
	}
}

func TestCleanBrief(t *testing.T) {
	in := "Both reviewers are still running.\n\nThe risk reviewer has finished.\n\n# Review MR !883\n\nbody"
	if got := CleanBrief(in); !strings.HasPrefix(got, "# Review MR !883") {
		t.Errorf("preamble kept: %q", got)
	}
	long := strings.Repeat("paragraph\n", 6) + "# Heading\n"
	if CleanBrief(long) != long {
		t.Error("long intro must be kept")
	}
	if CleanBrief("no headings") != "no headings" {
		t.Error("no heading: unchanged")
	}
}
