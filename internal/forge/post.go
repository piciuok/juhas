package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// Publishing is done by the app itself, only for comments the user
// accepted in the panel. Review agents never get here.

// PostComment is one inline comment to publish.
type PostComment struct {
	ID   string
	File string
	Line int
	Body string
}

// PostResult says where a comment ended up.
type PostResult struct {
	ID   string
	URL  string
	Note string // e.g. "outside the diff — posted as a general comment"
	Err  error
}

// runInput is run with a request body on stdin (gh/glab api --input -).
func runInput(ctx context.Context, body []byte, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), noPrompt...)
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String() + " " + stdout.String())
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		return stdout.Bytes(), fmt.Errorf("%s: %s", name, msg)
	}
	return stdout.Bytes(), nil
}

// Publish posts the summary (if not empty) and the inline comments.
func Publish(ctx context.Context, it Item, summary string, comments []PostComment) (PostResult, []PostResult) {
	if it.Platform == GitHub {
		return publishGitHub(ctx, it, summary, comments)
	}
	return publishGitLab(ctx, it, summary, comments)
}

func publishGitLab(ctx context.Context, it Item, summary string, comments []PostComment) (PostResult, []PostResult) {
	host, project := SplitKey(it.Repo)
	base := fmt.Sprintf("projects/%s/merge_requests/%d", url.PathEscape(project), it.Number)
	api := func(method, path string, body any) ([]byte, error) {
		b, _ := json.Marshal(body)
		return runInput(ctx, b, "glab", "api", "--hostname", host, "-X", method, "-H", "Content-Type: application/json", path, "--input", "-")
	}
	noteURL := func(id int) string { return fmt.Sprintf("%s#note_%d", it.URL, id) }

	var sum PostResult
	if strings.TrimSpace(summary) != "" {
		sum.ID = "summary"
		out, err := api("POST", base+"/notes", map[string]string{"body": summary})
		var n struct {
			ID int `json:"id"`
		}
		if err == nil && json.Unmarshal(out, &n) == nil {
			sum.URL = noteURL(n.ID)
		}
		sum.Err = err
	}

	// positions need the MR's diff refs and, for renamed files, the old path
	var mr struct {
		DiffRefs struct {
			Base  string `json:"base_sha"`
			Head  string `json:"head_sha"`
			Start string `json:"start_sha"`
		} `json:"diff_refs"`
	}
	refsErr := error(nil)
	if b, err := run(ctx, nil, "glab", "api", "--hostname", host, base); err != nil {
		refsErr = err
	} else if err := json.Unmarshal(b, &mr); err != nil {
		refsErr = err
	}
	oldPath := map[string]string{}
	var diffs []struct {
		Old string `json:"old_path"`
		New string `json:"new_path"`
	}
	if b, err := run(ctx, nil, "glab", "api", "--hostname", host, base+"/diffs?per_page=100"); err == nil && json.Unmarshal(b, &diffs) == nil {
		for _, d := range diffs {
			oldPath[d.New] = d.Old
		}
	}

	var res []PostResult
	for _, c := range comments {
		r := PostResult{ID: c.ID}
		general := func(note string) {
			body := fmt.Sprintf("**`%s:%d`**\n\n%s", c.File, c.Line, c.Body)
			if c.Line <= 0 {
				body = fmt.Sprintf("**`%s`**\n\n%s", c.File, c.Body)
			}
			out, err := api("POST", base+"/notes", map[string]string{"body": body})
			var n struct {
				ID int `json:"id"`
			}
			if err == nil && json.Unmarshal(out, &n) == nil {
				r.URL, r.Note, r.Err = noteURL(n.ID), note, nil
			} else {
				r.Err = err
			}
		}
		_, inDiff := oldPath[c.File]
		if refsErr != nil || c.Line <= 0 || !inDiff {
			general("outside the diff — posted as a general comment")
			res = append(res, r)
			continue
		}
		old := oldPath[c.File]
		pos := map[string]any{"position_type": "text", "base_sha": mr.DiffRefs.Base, "start_sha": mr.DiffRefs.Start, "head_sha": mr.DiffRefs.Head,
			"old_path": old, "new_path": c.File, "new_line": c.Line}
		out, err := api("POST", base+"/discussions", map[string]any{"body": c.Body, "position": pos})
		var d struct {
			Notes []struct {
				ID       int             `json:"id"`
				Position json.RawMessage `json:"position"`
			} `json:"notes"`
		}
		switch {
		case err != nil:
			general("line outside the changes — posted as a general comment") // e.g. line_code can't be blank
		case json.Unmarshal(out, &d) != nil || len(d.Notes) == 0:
			r.Err = fmt.Errorf("unexpected response from GitLab")
		default:
			r.URL = noteURL(d.Notes[0].ID)
			if p := string(d.Notes[0].Position); p == "" || p == "null" {
				r.Note = "GitLab did not attach it to the line — posted as a general comment"
			}
		}
		res = append(res, r)
	}
	return sum, res
}

func publishGitHub(ctx context.Context, it Item, summary string, comments []PostComment) (PostResult, []PostResult) {
	host, repo := SplitKey(it.Repo)
	api := func(method, path string, body any) ([]byte, error) {
		b, _ := json.Marshal(body)
		return runInput(ctx, b, "gh", "api", "--hostname", host, "-X", method, path, "--input", "-")
	}
	type ghComment struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Side string `json:"side"`
		Body string `json:"body"`
	}
	var inline []ghComment
	var res []PostResult
	var outside []PostComment
	for _, c := range comments {
		if c.Line > 0 && c.File != "" {
			inline = append(inline, ghComment{Path: c.File, Line: c.Line, Side: "RIGHT", Body: c.Body})
		} else {
			outside = append(outside, c)
		}
	}
	reviewBody := summary
	for _, c := range outside {
		reviewBody += fmt.Sprintf("\n\n**`%s`**\n\n%s", c.File, c.Body)
	}
	payload := map[string]any{"commit_id": it.HeadSHA, "event": "COMMENT", "body": strings.TrimSpace(reviewBody), "comments": inline}
	out, err := api("POST", fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, it.Number), payload)
	var rv struct {
		HTMLURL string `json:"html_url"`
	}
	if err == nil && json.Unmarshal(out, &rv) == nil {
		sum := PostResult{ID: "summary", URL: rv.HTMLURL}
		for _, c := range comments {
			r := PostResult{ID: c.ID, URL: rv.HTMLURL}
			if c.Line <= 0 {
				r.Note = "no line — included in the review body"
			}
			res = append(res, r)
		}
		return sum, res
	}
	// One bad line fails the whole review (422): post comments one by one and
	// fold the ones GitHub rejects into the summary comment.
	var folded []string
	for _, c := range comments {
		r := PostResult{ID: c.ID}
		if c.Line > 0 && c.File != "" {
			out, err := api("POST", fmt.Sprintf("repos/%s/pulls/%d/comments", repo, it.Number),
				map[string]any{"body": c.Body, "commit_id": it.HeadSHA, "path": c.File, "line": c.Line, "side": "RIGHT"})
			var pc struct {
				HTMLURL string `json:"html_url"`
			}
			if err == nil && json.Unmarshal(out, &pc) == nil {
				r.URL = pc.HTMLURL
				res = append(res, r)
				continue
			}
		}
		folded = append(folded, fmt.Sprintf("**`%s:%d`**\n\n%s", c.File, c.Line, c.Body))
		r.Note = "outside the diff — folded into the general comment"
		res = append(res, r)
	}
	sum := PostResult{ID: "summary"}
	body := strings.TrimSpace(strings.Join(append([]string{summary}, folded...), "\n\n"))
	if body != "" {
		out, err := api("POST", fmt.Sprintf("repos/%s/issues/%d/comments", repo, it.Number), map[string]string{"body": body})
		var ic struct {
			HTMLURL string `json:"html_url"`
		}
		if err == nil && json.Unmarshal(out, &ic) == nil {
			sum.URL = ic.HTMLURL
			for i := range res {
				if res[i].URL == "" {
					res[i].URL = ic.HTMLURL
				}
			}
		} else {
			sum.Err = err
			for i := range res {
				if res[i].URL == "" {
					res[i].Err = err
				}
			}
		}
	}
	return sum, res
}
