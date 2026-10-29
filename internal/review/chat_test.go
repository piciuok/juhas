package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/piciuok/juhas/internal/config"
	"github.com/piciuok/juhas/internal/forge"
	"github.com/piciuok/juhas/internal/state"
)

// isolate points the state and cache directories at a temp dir.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	return home
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// cloneWithPR makes an origin with a PR head ref and a clone of it; it
// returns the clone and a function that pushes a new PR commit.
func cloneWithPR(t *testing.T, home string) (string, func(string) string) {
	origin, work, clone := filepath.Join(home, "origin.git"), filepath.Join(home, "work"), filepath.Join(home, "clone")
	git(t, home, "init", "-q", "--bare", origin)
	git(t, home, "init", "-q", "-b", "main", work)
	commit := func(name string) string {
		if err := os.WriteFile(filepath.Join(work, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, work, "add", ".")
		git(t, work, "commit", "-q", "-m", name)
		return git(t, work, "rev-parse", "HEAD")
	}
	commit("base.txt")
	git(t, work, "push", "-q", origin, "main")
	push := func(name string) string {
		sha := commit(name)
		git(t, work, "push", "-q", "-f", origin, "HEAD:refs/pull/1/head")
		return sha
	}
	git(t, home, "clone", "-q", origin, clone)
	return clone, push
}

func TestWorktreeLifecycle(t *testing.T) {
	home := isolate(t)
	clone, push := cloneWithPR(t, home)
	it := forge.Item{Platform: forge.GitHub, Repo: "github.com/acme/widgets", Number: 1, URL: "https://github.com/acme/widgets/pull/1", Title: "Widgets", TargetBranch: "main"}
	ctx := context.Background()
	var log strings.Builder

	it.HeadSHA = push("one.txt")
	wt, err := EnsureWorktree(ctx, it, clone, "0123456789ab", &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if _, err := os.Stat(filepath.Join(wt, "one.txt")); err != nil {
		t.Fatalf("worktree lacks the PR file: %v", err)
	}
	// new commits: the kept worktree moves to them
	it.HeadSHA = push("two.txt")
	if wt2, err := EnsureWorktree(ctx, it, clone, "0123456789ab", &log); err != nil || wt2 != wt {
		t.Fatalf("second ensure: %q %v", wt2, err)
	}
	if head := git(t, wt, "rev-parse", "HEAD"); head != it.HeadSHA {
		t.Errorf("worktree at %s, want %s", head, it.HeadSHA)
	}
	ws := Worktrees()
	if len(ws) != 1 || ws[0].Ref != "#1" || ws[0].Bytes == 0 || ws[0].Clone != clone {
		t.Fatalf("registry: %+v", ws)
	}

	// still open (in the lists): not looked up, kept
	lookups := 0
	lookup := func(context.Context, Worktree) (forge.Item, error) {
		lookups++
		return forge.Item{State: forge.StateMerged}, nil
	}
	if gone := SweepWorktrees(ctx, time.Hour, map[string]bool{it.URL: true}, nil, lookup); len(gone) != 0 || lookups != 0 {
		t.Fatalf("open PR swept: %v (lookups %d)", gone, lookups)
	}
	// busy: kept even when merged
	if gone := SweepWorktrees(ctx, time.Hour, nil, func(Worktree) bool { return true }, lookup); len(gone) != 0 {
		t.Fatalf("busy worktree swept: %v", gone)
	}
	// left the lists and merged: removed
	gone := SweepWorktrees(ctx, time.Hour, nil, nil, lookup)
	if len(gone) != 1 || gone[0].Reason != forge.StateMerged {
		t.Fatalf("sweep: %+v", gone)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree dir still there: %v", err)
	}
	if out := git(t, clone, "worktree", "list"); strings.Contains(out, wt) {
		t.Errorf("git still lists it:\n%s", out)
	}
	if len(Worktrees()) != 0 {
		t.Errorf("registry not empty")
	}
}

func TestSweepUnusedWorktree(t *testing.T) {
	home := isolate(t)
	clone, push := cloneWithPR(t, home)
	it := forge.Item{Platform: forge.GitHub, Repo: "github.com/acme/widgets", Number: 1, URL: "https://github.com/acme/widgets/pull/1", HeadSHA: push("one.txt")}
	var log strings.Builder
	if _, err := EnsureWorktree(context.Background(), it, clone, "0123456789ab", &log); err != nil {
		t.Fatal(err)
	}
	_ = updateWorktrees(func(reg map[string]*Worktree) error {
		reg["0123456789ab"].Used = time.Now().Add(-8 * 24 * time.Hour)
		return nil
	})
	// open, but a week unused
	gone := SweepWorktrees(context.Background(), 7*24*time.Hour, map[string]bool{it.URL: true}, nil, nil)
	if len(gone) != 1 || !strings.HasPrefix(gone[0].Reason, "unused") {
		t.Fatalf("sweep: %+v", gone)
	}
}

func TestRemoveWorktreeOnlyOurs(t *testing.T) {
	isolate(t)
	other := t.TempDir()
	_ = updateWorktrees(func(reg map[string]*Worktree) error {
		reg["0123456789ab"] = &Worktree{ID: "0123456789ab", Path: other}
		return nil
	})
	if err := RemoveWorktree(context.Background(), "0123456789ab"); err == nil {
		t.Fatal("removed a directory outside the worktrees folder")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other dir gone: %v", err)
	}
}

func TestChatComments(t *testing.T) {
	text := "Yes.\n\n```juhas-comment\n{\"file\": \"./a/b.go\", \"line\": 12, \"body\": \"Handle the nil case.\"}\n```\n\n```juhas-comment\n{\"file\": \"\", \"body\": \"Thanks!\"}\n```\n```juhas-comment\nnot json\n```"
	cs := ChatComments(text)
	if len(cs) != 2 || cs[0].File != "a/b.go" || cs[0].Line != 12 || cs[1].File != "" || cs[1].Body != "Thanks!" {
		t.Fatalf("%+v", cs)
	}
}

func TestAddChatComment(t *testing.T) {
	isolate(t)
	id := "0123456789ab"
	job := &state.Job{URL: "https://gitlab.example.com/team/app/-/merge_requests/7", Title: "App", BriefSHA: "abc"}
	// no proposal yet: one is made
	if err := AddChatComment(id, ChatComment{File: "x.go", Line: 3, Body: "Check this."}, job); err != nil {
		t.Fatal(err)
	}
	if err := AddChatComment(id, ChatComment{Body: "General note."}, job); err != nil {
		t.Fatal(err)
	}
	if err := AddChatComment(id, ChatComment{File: "../etc/passwd", Body: "x"}, job); err == nil {
		t.Error("accepted a path outside the repo")
	}
	p, err := LoadProposal(ProposalPath(JobLogPath(id)))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Comments) != 1 || p.Comments[0].ID != "c1" || p.Comments[0].Status != CommentPending || p.Summary.Body != "General note." || p.Number != 7 || p.HeadSHA != "abc" {
		t.Fatalf("%+v", p)
	}
}

func TestStreamSession(t *testing.T) {
	in := `{"type":"system","subtype":"init","session_id":"s-1","model":"m"}
{"type":"result","subtype":"success","result":"ok","session_id":"s-1"}`
	var log strings.Builder
	if sr := ConsumeStream(strings.NewReader(in), &log); sr.Session != "s-1" || sr.Text != "ok" {
		t.Fatalf("%+v", sr)
	}
	cr := ConsumeCodexStream(strings.NewReader(`{"type":"thread.started","thread_id":"th-9"}`), &log)
	if cr.Thread != "th-9" {
		t.Fatalf("%+v", cr)
	}
}

// The chat forks the review's session first, then resumes its own.
func TestChatResumesReviewSession(t *testing.T) {
	home := isolate(t)
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	argsLog := filepath.Join(home, "args.log")
	bin := filepath.Join(home, "fake-claude")
	result := filepath.Join(home, "result.jsonl")
	if err := os.WriteFile(result, []byte(`{"type":"system","subtype":"init","session_id":"chat-1"}
{"type":"result","subtype":"success","result":"It is fine.\n\n`+"```"+`juhas-comment\n{\"file\": \"a.go\", \"line\": 1, \"body\": \"Nit.\"}\n`+"```"+`","num_turns":1,"session_id":"chat-1"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$@\" >> " + argsLog + "\ncat > /dev/null\ncat " + result + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(`{"claude": {"bin": "`+bin+`"}}`), filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	store := state.Open()
	id := "0123456789ab"
	url := "https://github.com/acme/widgets/pull/1"
	_ = store.Update(func(st *state.State) error {
		st.Put(&state.Job{URL: url, Repo: "github.com/acme/widgets", Number: 1, Status: state.Done, Brief: filepath.Join(home, "brief.md"),
			Log: filepath.Join(store.LogDir(), id+".log"), BriefSHA: "abc", Session: &state.Session{Agent: "claude", ID: "review-1", Dir: repo, SHA: "abc"}})
		return nil
	})
	env := ChatEnv{Cfg: cfg, Store: store, Dir: func(string) string { return repo }}
	ask := func(q string) *Chat {
		t.Helper()
		if err := ChatManager.Send(env, id, q); err != nil {
			t.Fatal(err)
		}
		ChatManager.wg.Wait()
		c, err := LoadChat(id)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := ask("Why?")
	if len(c.Messages) != 2 || c.Messages[1].Role != RoleAssistant || !strings.Contains(c.Messages[1].Text, "It is fine.") {
		t.Fatalf("messages: %+v", c.Messages)
	}
	if cs := ChatComments(c.Messages[1].Text); len(cs) != 1 || cs[0].File != "a.go" {
		t.Errorf("suggested comments: %+v", cs)
	}
	if c.Session != "chat-1" || c.Review != "review-1" || c.Dir != repo {
		t.Errorf("chat: %+v", c)
	}
	ask("And then?")
	b, _ := os.ReadFile(argsLog)
	calls := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(calls) != 2 || !strings.Contains(calls[0], "--resume review-1 --fork-session") || !strings.Contains(calls[1], "--resume chat-1") || strings.Contains(calls[1], "fork") {
		t.Fatalf("calls:\n%s", b)
	}
	for _, c := range calls {
		if !strings.Contains(c, "--settings") || !strings.Contains(c, "--disallowedTools Edit Write") {
			t.Errorf("chat without the read-only guard: %s", c)
		}
	}
	if err := ChatManager.Reset(id); err != nil {
		t.Fatal(err)
	}
	if c, _ := LoadChat(id); len(c.Messages) != 0 || c.Session != "" {
		t.Errorf("after reset: %+v", c)
	}
}
