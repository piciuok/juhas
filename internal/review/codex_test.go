package review

import (
	"strings"
	"testing"
	"time"

	"juhas/internal/config"
	"juhas/internal/forge"
)

func TestCodexArgs(t *testing.T) {
	a := CodexArgs(config.Agent{Bin: "codex", Model: "gpt-x", Effort: "high", ExtraArgs: []string{"--foo"}}, "/wt", "/ctx/last.md")
	got := strings.Join(a, " ")
	for _, want := range []string{"exec --sandbox read-only --json", "--cd /wt", "--output-last-message /ctx/last.md", "--model gpt-x", `model_reasoning_effort="high"`, "--foo -"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q: %s", want, got)
		}
	}
	if strings.Contains(got, "danger") || strings.Contains(got, "workspace-write") {
		t.Errorf("sandbox weakened: %s", got)
	}
}

const codexSample = `{"type":"thread.started","thread_id":"x"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"0","type":"reasoning","text":"Reading the diff"}}
{"type":"item.started","item":{"id":"1","type":"command_execution","command":"/bin/zsh -lc 'git diff origin/dev...HEAD --stat'","status":"in_progress"}}
{"type":"item.completed","item":{"id":"1","type":"command_execution","command":"/bin/zsh -lc 'git diff'","aggregated_output":"a.ts | 2 +-\nb.ts | 4 ++--\n","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"2","type":"file_change","changes":[{"path":"/wt/a.ts","kind":"update"}],"status":"failed"}}
{"type":"item.completed","item":{"id":"3","type":"agent_message","text":"# Review\n\nOK"}}
{"type":"turn.completed","usage":{"input_tokens":1000,"output_tokens":50}}
`

func TestConsumeCodexStream(t *testing.T) {
	var log strings.Builder
	res := ConsumeCodexStream(strings.NewReader(codexSample), &log)
	if res.Text != "# Review\n\nOK" || res.Turns != 1 || res.Tokens != 1050 || res.Failed != "" {
		t.Errorf("result: %+v", res)
	}
	out := log.String()
	for _, want := range []string{"● Codex session", "▶ Shell git diff origin/dev...HEAD --stat", "↳ 2 lines", "⛔ attempted file change", "💬 # Review\n", "✓ done: 1 turns"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

func TestCodexPromptOffline(t *testing.T) {
	it := forge.Item{Platform: forge.GitLab, Repo: "gl/a/b", Number: 1, URL: "u", TargetBranch: "dev"}
	ws := &codexWorkspace{CtxDir: "/ctx", Worktree: "/wt", Base: "dev"}
	s := config.Settings{Language: config.Language{Brief: "pl", Comments: "en"}}
	p := CodexPrompt(it, Method{Kind: KindSkill, Skill: "review", Source: "/repo/.claude/skills/review/SKILL.md"}, s, ws, "/repo")
	for _, want := range []string{"NO network", "/ctx/diff.patch", "/ctx/pr.md", "origin/dev...HEAD", "/repo/.claude/skills/review/SKILL.md", "in English", "juhas-review"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

func TestResolveForCodex(t *testing.T) {
	m := ResolveFor(config.AgentCodex, t.TempDir(), config.Review{Skill: "auto", Fallback: "claude-code-review"})
	if m.Kind != KindBuiltin {
		t.Errorf("codex cannot run /code-review: %+v", m)
	}
}

func TestCodexStderrFoldsMCP(t *testing.T) {
	var log strings.Builder
	w := &codexStderr{log: &log}
	w.Write([]byte("2026 ERROR rmcp::transport::worker: quit\n2026 ERROR rmcp::transport::worker: again\nreal warn"))
	w.flush()
	out := log.String()
	if strings.Count(out, "MCP servers") != 1 || strings.Contains(out, "rmcp") || !strings.Contains(out, "real warn") {
		t.Errorf("log:\n%s", out)
	}
}

func TestBriefNameAgentSuffix(t *testing.T) {
	it := forge.Item{Repo: "gl.example/a/b", Number: 9}
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	if BriefName(it, "claude", now) == BriefName(it, "codex", now) {
		t.Error("codex brief overwrites claude brief")
	}
}
