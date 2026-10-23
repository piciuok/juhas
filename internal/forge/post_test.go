package forge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCLI puts a script named bin on PATH; it appends "args | stdin" to
// calls.log and answers from the case statement in script.
func fakeCLI(t *testing.T, bin, script string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	body := "#!/bin/sh\nin=$(cat)\necho \"$* | $in\" >> " + log + "\n" + script
	if err := os.WriteFile(filepath.Join(dir, bin), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestPublishGitLab(t *testing.T) {
	log := fakeCLI(t, "glab", `
case "$*" in
  *"/notes"*) echo '{"id": 501}';;
  *"/discussions"*)
    case "$in" in
      *'"new_line":99'*) echo "line_code can't be blank" >&2; exit 1;;
      *) echo '{"notes":[{"id": 601, "position": {"new_line": 12}}]}';;
    esac;;
  *"/diffs"*) echo '[{"old_path":"old/a.ts","new_path":"apps/a.ts"}]';;
  *) echo '{"diff_refs":{"base_sha":"b","head_sha":"h","start_sha":"s"}}';;
esac`)
	it := Item{Platform: GitLab, Repo: "gl.example/team/app", Number: 7, URL: "https://gl.example/team/app/-/merge_requests/7"}
	sum, res := Publish(context.Background(), it, "Thanks!", []PostComment{
		{ID: "c1", File: "apps/a.ts", Line: 12, Body: "inline"},
		{ID: "c2", File: "apps/a.ts", Line: 99, Body: "line outside the diff"},
		{ID: "c3", File: "not/in/diff.ts", Line: 3, Body: "file not in diff"},
	})
	if sum.Err != nil || !strings.HasSuffix(sum.URL, "#note_501") {
		t.Errorf("summary: %+v", sum)
	}
	if res[0].Err != nil || !strings.HasSuffix(res[0].URL, "#note_601") || res[0].Note != "" {
		t.Errorf("inline: %+v", res[0])
	}
	if res[1].Err != nil || res[1].Note == "" || !strings.HasSuffix(res[1].URL, "#note_501") {
		t.Errorf("fallback after rejected line: %+v", res[1])
	}
	if res[2].Note == "" {
		t.Errorf("file outside diff must go as general note: %+v", res[2])
	}
	b, _ := os.ReadFile(log)
	calls := string(b)
	if !strings.Contains(calls, `"old_path":"old/a.ts"`) || !strings.Contains(calls, `"head_sha":"h"`) {
		t.Errorf("position missing renamed old_path or diff refs:\n%s", calls)
	}
	if !strings.Contains(calls, "**`apps/a.ts:99`**") {
		t.Errorf("general note lacks file:line:\n%s", calls)
	}
}

func TestPublishGitHubFallsBackPerComment(t *testing.T) {
	log := fakeCLI(t, "gh", `
case "$*" in
  *"/reviews"*) echo '{"message":"Unprocessable"}'; exit 1;;
  *"/pulls/5/comments"*)
    case "$in" in
      *'"line":1000'*) exit 1;;
      *) echo '{"html_url":"https://github.com/o/r/pull/5#discussion_r1"}';;
    esac;;
  *"/issues/5/comments"*) echo '{"html_url":"https://github.com/o/r/pull/5#issuecomment-9"}';;
esac`)
	it := Item{Platform: GitHub, Repo: "github.com/o/r", Number: 5, HeadSHA: "abc"}
	sum, res := Publish(context.Background(), it, "Podsumowanie", []PostComment{
		{ID: "c1", File: "a.go", Line: 10, Body: "ok"},
		{ID: "c2", File: "a.go", Line: 1000, Body: "bad line"},
	})
	if !strings.Contains(res[0].URL, "discussion_r1") || res[0].Err != nil {
		t.Errorf("c1: %+v", res[0])
	}
	if !strings.Contains(res[1].URL, "issuecomment-9") || res[1].Note == "" {
		t.Errorf("c2 should be folded into the summary comment: %+v", res[1])
	}
	if !strings.Contains(sum.URL, "issuecomment-9") {
		t.Errorf("summary: %+v", sum)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), `"commit_id":"abc"`) || !strings.Contains(string(b), "bad line") {
		t.Errorf("calls:\n%s", b)
	}
}
