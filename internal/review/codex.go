package review

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"juhas/internal/config"
	"juhas/internal/forge"
)

// Codex has no hook API we can rely on, and its read-only sandbox also cuts
// the network. So the app fetches the PR/MR itself and prepares a detached
// worktree at its head; Codex then reviews under `-s read-only`, where the
// OS, not a deny list, keeps it from writing or posting anything.

// codexWorkspace is the prefetched context and the worktree for one run.
type codexWorkspace struct {
	CtxDir   string // pr.md, diff.patch, ci.txt
	Worktree string // "" when checking out failed; Codex then reads the main clone
	Base     string
	cleanup  func()
}

func cacheDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "juhas")
	}
	return filepath.Join(os.TempDir(), "juhas")
}

func prepareCodex(ctx context.Context, it forge.Item, dir, id string, log io.Writer) (*codexWorkspace, error) {
	ws := &codexWorkspace{CtxDir: filepath.Join(cacheDir(), "runs", id), Base: it.TargetBranch}
	_ = os.RemoveAll(ws.CtxDir)
	if err := os.MkdirAll(ws.CtxDir, 0o700); err != nil {
		return nil, err
	}
	fmt.Fprintf(log, "%s ● fetching description, discussion, diff and CI\n", stamp())
	c, err := forge.Fetch(ctx, it)
	if err != nil {
		return nil, fmt.Errorf("fetching PR/MR: %w", err)
	}
	for name, body := range map[string]string{"pr.md": c.Discussion, "diff.patch": c.Diff, "ci.txt": c.CI} {
		if err := os.WriteFile(filepath.Join(ws.CtxDir, name), []byte(body), 0o600); err != nil {
			return nil, err
		}
	}
	fmt.Fprintf(log, "%s   ↳ diff: %d lines\n", stamp(), strings.Count(c.Diff, "\n"))

	wt := filepath.Join(cacheDir(), "worktrees", id)
	git := func(args ...string) (string, error) {
		return gitIn(ctx, dir, args...)
	}
	ws.cleanup = func() {
		// the run's context is done after a timeout or cancel; clean up anyway
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		_, _ = gitIn(ctx, dir, "worktree", "remove", "--force", wt)
		_, _ = gitIn(ctx, dir, "worktree", "prune")
		_ = os.RemoveAll(ws.CtxDir)
	}
	_, _ = git("worktree", "remove", "--force", wt) // leftover from an interrupted run
	fmt.Fprintf(log, "%s ● worktree at head %s (%s)\n", stamp(), short(it.HeadSHA), forge.HeadRef(it))
	refs := []string{forge.HeadRef(it)}
	if it.TargetBranch != "" {
		refs = append(refs, it.TargetBranch+":refs/remotes/origin/"+it.TargetBranch)
	}
	if out, err := git(append([]string{"fetch", "--no-tags", "origin"}, refs...)...); err != nil {
		fmt.Fprintf(log, "%s   ✗ git fetch: %s — Codex will read the main clone (the diff stays the source of truth)\n", stamp(), oneLine(out, 200))
		return ws, nil
	}
	sha := it.HeadSHA
	if sha == "" {
		sha, _ = git("rev-parse", "FETCH_HEAD")
	}
	if out, err := git("worktree", "add", "--detach", wt, sha); err != nil {
		fmt.Fprintf(log, "%s   ✗ git worktree: %s — Codex will read the main clone\n", stamp(), oneLine(out, 200))
		return ws, nil
	}
	ws.Worktree = wt
	fmt.Fprintf(log, "%s   ↳ %s\n", stamp(), config.Contract(wt))
	return ws, nil
}

// CodexPrompt is the prompt for an offline, read-only Codex review.
func CodexPrompt(it forge.Item, m Method, s config.Settings, ws *codexWorkspace, mainDir string) string {
	var b strings.Builder
	code := "current directory (worktree at the PR/MR head)"
	if ws.Worktree == "" {
		code = "current directory (main clone — may be on a different branch; the diff is the source of truth)"
	}
	fmt.Fprintf(&b, `Prepare a code review brief for this PR/MR.

PR/MR: %s
Title: %s
Author: %s · %s → %s · head %s

You are working in a read-only sandbox with NO network. Do not run gh, glab, curl or git fetch — everything has already been fetched:
- description, metadata and discussion: %s
- diff: %s
- CI status: %s
- code: %s; base branch: origin/%s (e.g. git diff origin/%s...HEAD, git log, git show work locally)
- the project's rules and docs (CLAUDE.md, AGENTS.md, .claude/rules, docs/) are in the main clone: %s
- do your own, independent review: do not copy conclusions from earlier reports (e.g. .claude/reviews/*.json); at most mention them in a section about earlier reviews

%s
`, it.URL, it.Title, it.Author, it.SourceBranch, it.TargetBranch, short(it.HeadSHA),
		filepath.Join(ws.CtxDir, "pr.md"), filepath.Join(ws.CtxDir, "diff.patch"), filepath.Join(ws.CtxDir, "ci.txt"),
		code, ws.Base, ws.Base, mainDir, rules)
	if m.Kind == KindSkill && m.Source != "" {
		fmt.Fprintf(&b, `
The project has its own review procedure: %s
Read it and apply its criteria, dimensions, severities (BLOCK/WARN/INFO etc.) and report format. Skip steps that need the network, writing, subagents, installing dependencies or publishing (--post, --fix) — note in the report what could not be done.
`, m.Source)
	} else {
		b.WriteString(`
Output in markdown, concise and specific:
## TL;DR (max 3 sentences)
## What changes (areas/files)
## Risks and potential bugs (file:line, severity: high/medium/low, rationale)
## Questions for the author
## Tests and CI
## Suggested verdict (approve / request changes / needs discussion) + why
`)
	}
	fmt.Fprintf(&b, "\n%s\n\nThe last message is saved as the brief: it must contain the whole report and start with its heading. Do not guess — if something cannot be determined, say so plainly.\n", languageRules(s.Language))
	return b.String()
}

// CodexArgs builds `codex exec` arguments (prompt on stdin via "-").
func CodexArgs(a config.Agent, workdir, lastMsg string) []string {
	args := []string{"exec", "--sandbox", "read-only", "--json", "--color", "never", "--skip-git-repo-check",
		"--cd", workdir, "--output-last-message", lastMsg}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	if a.Effort != "" {
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", a.Effort))
	}
	args = append(args, a.ExtraArgs...)
	return append(args, "-")
}

func runCodex(ctx context.Context, cfg *config.Config, s config.Settings, it forge.Item, m Method, dir, logPath string, log io.Writer, started func(int)) (string, error) {
	id := strings.TrimSuffix(filepath.Base(logPath), ".log")
	ws, err := prepareCodex(ctx, it, dir, id, log)
	if err != nil {
		return "", err
	}
	defer ws.cleanup()
	workdir := dir
	if ws.Worktree != "" {
		workdir = ws.Worktree
	}
	a := cfg.AgentConfig(config.AgentCodex, s)
	last := filepath.Join(ws.CtxDir, "last-message.md")
	cmd := exec.CommandContext(ctx, a.Bin, CodexArgs(a, workdir, last)...)
	cmd.Dir = workdir
	cmd.Stdin = strings.NewReader(CodexPrompt(it, m, s, ws, dir))
	cmd.Env = append(os.Environ(), "JUHAS=1", "NO_COLOR=1")
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 5 * time.Second
	stderr := &codexStderr{log: log}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	if started != nil {
		started(cmd.Process.Pid)
	}
	sr := ConsumeCodexStream(stdout, log)
	stderr.flush()
	err = cmd.Wait()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	body, _ := os.ReadFile(last)
	text := strings.TrimSpace(string(body))
	if text == "" {
		text = strings.TrimSpace(sr.Text)
	}
	switch {
	case sr.Failed != "":
		return "", fmt.Errorf("codex: %s (log: %s)", sr.Failed, logPath)
	case err != nil && text == "":
		return "", fmt.Errorf("codex exited with an error: %v (log: %s)", err, logPath)
	}
	return text, nil
}

// CodexResult summarises a codex --json stream.
type CodexResult struct {
	Text   string // last agent message
	Turns  int
	Tokens int
	Failed string // turn.failed / error message
}

type codexEvent struct {
	Type string `json:"type"`
	Item *struct {
		Type             string `json:"type"`
		Text             string `json:"text"`
		Message          string `json:"message"`
		Command          string `json:"command"`
		AggregatedOutput string `json:"aggregated_output"`
		ExitCode         *int   `json:"exit_code"`
		Status           string `json:"status"`
		Server           string `json:"server"`
		Tool             string `json:"tool"`
		Query            string `json:"query"`
		Changes          []struct {
			Path string `json:"path"`
		} `json:"changes"`
	} `json:"item"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

// ConsumeCodexStream renders `codex exec --json` events into the run log.
func ConsumeCodexStream(r io.Reader, log io.Writer) CodexResult {
	var res CodexResult
	start := time.Now()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var ev codexEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			fmt.Fprintf(log, "%s   %s\n", stamp(), oneLine(sc.Text(), 200))
			continue
		}
		switch ev.Type {
		case "thread.started":
			fmt.Fprintf(log, "%s ● Codex session started\n", stamp())
		case "turn.completed":
			res.Turns++
			if ev.Usage != nil {
				res.Tokens += ev.Usage.InputTokens + ev.Usage.OutputTokens
			}
		case "turn.failed":
			if ev.Error != nil {
				res.Failed = ev.Error.Message
			}
			fmt.Fprintf(log, "%s ✗ %s\n", stamp(), oneLine(res.Failed, 300))
		case "error":
			fmt.Fprintf(log, "%s ! %s\n", stamp(), oneLine(ev.Message, 300))
		case "item.started":
			if ev.Item != nil && ev.Item.Type == "command_execution" {
				fmt.Fprintf(log, "%s ▶ Shell %s\n", stamp(), oneLine(unwrapShell(ev.Item.Command), 200))
			}
		case "item.completed":
			it := ev.Item
			if it == nil {
				continue
			}
			switch it.Type {
			case "agent_message":
				res.Text = it.Text
				fmt.Fprintf(log, "%s 💬 %s\n", stamp(), fullText(strings.TrimSpace(it.Text), ""))
			case "reasoning":
				if t := strings.TrimSpace(it.Text); t != "" {
					fmt.Fprintf(log, "%s   · %s\n", stamp(), oneLine(t, 200))
				}
			case "command_execution":
				mark := "↳"
				if it.ExitCode != nil && *it.ExitCode != 0 {
					mark = fmt.Sprintf("✗ (exit %d)", *it.ExitCode)
				}
				fmt.Fprintf(log, "%s   %s %s\n", stamp(), mark, resultSummary(it.AggregatedOutput))
			case "file_change":
				var paths []string
				for _, c := range it.Changes {
					paths = append(paths, c.Path)
				}
				fmt.Fprintf(log, "%s ⛔ attempted file change (read-only sandbox): %s\n", stamp(), strings.Join(paths, ", "))
			case "mcp_tool_call":
				fmt.Fprintf(log, "%s ▶ MCP %s.%s\n", stamp(), it.Server, it.Tool)
			case "web_search":
				fmt.Fprintf(log, "%s ▶ web search %s\n", stamp(), oneLine(it.Query, 160))
			case "error":
				fmt.Fprintf(log, "%s ! %s\n", stamp(), oneLine(it.Message, 300))
			}
		}
	}
	status := "✓ done"
	if res.Failed != "" {
		status = "✗ done with error"
	}
	fmt.Fprintf(log, "%s %s: %d turns, %s, %d tokens\n", stamp(), status, res.Turns, time.Since(start).Round(time.Second), res.Tokens)
	return res
}

// codexStderr folds the MCP connection errors Codex prints for every
// configured server (they cannot connect from the offline sandbox anyway)
// into one line, and passes everything else through.
type codexStderr struct {
	log     io.Writer
	buf     []byte
	mcpSeen bool
}

func (c *codexStderr) Write(p []byte) (int, error) {
	c.buf = append(c.buf, p...)
	for {
		i := bytes.IndexByte(c.buf, '\n')
		if i < 0 {
			break
		}
		c.line(string(c.buf[:i]))
		c.buf = c.buf[i+1:]
	}
	return len(p), nil
}

func (c *codexStderr) line(l string) {
	if strings.Contains(l, "rmcp::") || strings.Contains(l, "MCP client for") {
		if !c.mcpSeen {
			c.mcpSeen = true
			fmt.Fprintf(c.log, "%s ! MCP servers from ~/.codex/config.toml are unavailable in the sandbox — ignoring their errors\n", stamp())
		}
		return
	}
	if strings.TrimSpace(l) != "" {
		fmt.Fprintf(c.log, "%s   %s\n", stamp(), oneLine(l, 300))
	}
}

func (c *codexStderr) flush() {
	if len(c.buf) > 0 {
		c.line(string(c.buf))
		c.buf = nil
	}
}

// unwrapShell shows `git log` for "/bin/zsh -lc 'git log'".
func unwrapShell(cmd string) string {
	for _, p := range []string{"/bin/zsh -lc ", "/bin/bash -lc ", "bash -lc ", "zsh -lc ", "/bin/sh -c "} {
		if rest, ok := strings.CutPrefix(cmd, p); ok {
			return strings.Trim(rest, `'"`)
		}
	}
	return cmd
}

func gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
