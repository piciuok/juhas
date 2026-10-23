package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
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
func runInput(ctx context.Context, env []string, body []byte, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(append(os.Environ(), noPrompt...), env...)
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
// It acts as the account that found the PR/MR.
func Publish(ctx context.Context, it Item, summary string, comments []PostComment) (PostResult, []PostResult) {
	env, err := ItemAccount(it).Env(ctx)
	if err != nil {
		sum := PostResult{ID: "summary", Err: err}
		res := make([]PostResult, len(comments))
		for i, c := range comments {
			res[i] = PostResult{ID: c.ID, Err: err}
		}
		if strings.TrimSpace(summary) == "" {
			sum = PostResult{}
		}
		return sum, res
	}
	if it.Platform == GitHub {
		return publishGitHub(ctx, env, it, summary, comments)
	}
	return publishGitLab(ctx, env, it, summary, comments)
}

func publishGitLab(ctx context.Context, env []string, it Item, summary string, comments []PostComment) (PostResult, []PostResult) {
	host, project := SplitKey(it.Repo)
	base := fmt.Sprintf("projects/%s/merge_requests/%d", url.PathEscape(project), it.Number)
	api := func(method, path string, body any) ([]byte, error) {
		b, _ := json.Marshal(body)
		return runInput(ctx, env, b, "glab", "api", "--hostname", host, "-X", method, "-H", "Content-Type: application/json", path, "--input", "-")
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

	refs, diffs, refsErr := glDiffs(ctx, env, host, base, it.HeadSHA)
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
		d, inDiff := diffs[c.File]
		oldLine, inHunk := 0, false
		if inDiff {
			oldLine, inHunk = hunkLine(d.Diff, c.Line)
		}
		if refsErr != nil || c.Line <= 0 || !inDiff || (d.Diff != "" && !inHunk) {
			general("outside the diff — posted as a general comment")
			res = append(res, r)
			continue
		}
		pos := map[string]any{"position_type": "text", "base_sha": refs.Base, "start_sha": refs.Start, "head_sha": refs.Head,
			"old_path": d.Old, "new_path": c.File, "new_line": c.Line}
		if oldLine > 0 {
			pos["old_line"] = oldLine // an unchanged line needs both sides
		}
		out, err := api("POST", base+"/discussions", map[string]any{"body": c.Body, "position": pos})
		var dn struct {
			Notes []struct {
				ID       int             `json:"id"`
				Position json.RawMessage `json:"position"`
			} `json:"notes"`
		}
		switch {
		case err != nil && rejected(err):
			general("line outside the changes — posted as a general comment") // e.g. line_code can't be blank
		case err != nil:
			r.Err = err // a timeout or 5xx may still have created it: do not post twice
		case json.Unmarshal(out, &dn) != nil || len(dn.Notes) == 0:
			r.Err = fmt.Errorf("unexpected response from GitLab")
		default:
			r.URL = noteURL(dn.Notes[0].ID)
			if p := string(dn.Notes[0].Position); p == "" || p == "null" {
				r.Note = "GitLab did not attach it to the line — posted as a general comment"
			}
		}
		res = append(res, r)
	}
	return sum, res
}

type glRefs struct {
	Base, Start, Head string
}

type glFileDiff struct {
	Old  string // old path (differs for renames)
	Diff string // unified hunks ("" = unknown, e.g. too large)
}

// glDiffs returns the diff refs and per-file diffs of the MR version whose
// head is sha (the reviewed commit), so lines are anchored to what the review
// saw even after new pushes; without a match it uses the current version.
func glDiffs(ctx context.Context, env []string, host, base, sha string) (glRefs, map[string]glFileDiff, error) {
	var refs glRefs
	files := map[string]glFileDiff{}
	type diff struct {
		Old  string `json:"old_path"`
		New  string `json:"new_path"`
		Diff string `json:"diff"`
	}
	var versions []struct {
		ID    int    `json:"id"`
		Head  string `json:"head_commit_sha"`
		Base  string `json:"base_commit_sha"`
		Start string `json:"start_commit_sha"`
	}
	if sha != "" {
		if b, err := run(ctx, env, "glab", "api", "--hostname", host, base+"/versions"); err == nil && json.Unmarshal(b, &versions) == nil {
			for _, v := range versions {
				if v.Head != sha {
					continue
				}
				var full struct {
					Diffs []diff `json:"diffs"`
				}
				b, err := run(ctx, env, "glab", "api", "--hostname", host, fmt.Sprintf("%s/versions/%d", base, v.ID))
				if err != nil || json.Unmarshal(b, &full) != nil {
					break
				}
				for _, d := range full.Diffs {
					files[d.New] = glFileDiff{Old: d.Old, Diff: d.Diff}
				}
				return glRefs{Base: v.Base, Start: v.Start, Head: v.Head}, files, nil
			}
		}
	}
	var mr struct {
		DiffRefs struct {
			Base  string `json:"base_sha"`
			Head  string `json:"head_sha"`
			Start string `json:"start_sha"`
		} `json:"diff_refs"`
	}
	b, err := run(ctx, env, "glab", "api", "--hostname", host, base)
	if err == nil {
		err = json.Unmarshal(b, &mr)
	}
	if err != nil {
		return refs, files, err
	}
	refs = glRefs{Base: mr.DiffRefs.Base, Start: mr.DiffRefs.Start, Head: mr.DiffRefs.Head}
	for page := 1; page <= 30; page++ {
		var diffs []diff
		b, err := run(ctx, env, "glab", "api", "--hostname", host, fmt.Sprintf("%s/diffs?per_page=100&page=%d", base, page))
		if err != nil || json.Unmarshal(b, &diffs) != nil {
			break // GitLab < 15.7: files stay unknown and comments go as general notes
		}
		for _, d := range diffs {
			files[d.New] = glFileDiff{Old: d.Old, Diff: d.Diff}
		}
		if len(diffs) < 100 {
			break
		}
	}
	return refs, files, nil
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// hunkLine finds a new-side line in a unified diff: ok reports whether it is
// inside a hunk, and oldLine is its old-side number when the line is
// unchanged context (0 for an added line).
func hunkLine(diff string, line int) (oldLine int, ok bool) {
	o, n := 0, 0
	for _, t := range strings.Split(diff, "\n") {
		if m := hunkHeader.FindStringSubmatch(t); m != nil {
			o, _ = strconv.Atoi(m[1])
			n, _ = strconv.Atoi(m[2])
			continue
		}
		if n == 0 || t == "" || strings.HasPrefix(t, "\\") {
			continue
		}
		switch {
		case strings.HasPrefix(t, "+"):
			if n == line {
				return 0, true
			}
			n++
		case strings.HasPrefix(t, "-"):
			o++
		default:
			if n == line {
				return o, true
			}
			o++
			n++
		}
	}
	return 0, false
}

var httpStatus = regexp.MustCompile(`\(HTTP (\d{3})\)`)

// rejected reports whether gh/glab got a 400/422 — the forge refused the
// request (e.g. a line outside the diff), so nothing was created and a
// fallback is safe. Timeouts and 5xx are not: the request may have landed.
func rejected(err error) bool {
	m := httpStatus.FindStringSubmatch(err.Error())
	return m != nil && (m[1] == "422" || m[1] == "400")
}

func publishGitHub(ctx context.Context, env []string, it Item, summary string, comments []PostComment) (PostResult, []PostResult) {
	host, repo := SplitKey(it.Repo)
	api := func(method, path string, body any) ([]byte, error) {
		b, _ := json.Marshal(body)
		return runInput(ctx, env, b, "gh", "api", "--hostname", host, "-X", method, path, "--input", "-")
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
	if err == nil {
		_ = json.Unmarshal(out, &rv) // created either way; the URL is a nicety
		if rv.HTMLURL == "" {
			rv.HTMLURL = it.URL
		}
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
	if !rejected(err) {
		// a timeout or 5xx may have created the review: report, never repost
		sum := PostResult{ID: "summary", Err: err}
		for _, c := range comments {
			res = append(res, PostResult{ID: c.ID, Err: err})
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
			if err == nil {
				_ = json.Unmarshal(out, &pc)
				r.URL = pc.HTMLURL
				if r.URL == "" {
					r.URL = it.URL
				}
				res = append(res, r)
				continue
			}
			if !rejected(err) {
				r.Err = err
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
				if res[i].URL == "" && res[i].Err == nil {
					res[i].URL = ic.HTMLURL
				}
			}
		} else {
			sum.Err = err
			for i := range res {
				if res[i].URL == "" && res[i].Err == nil {
					res[i].Err = err
				}
			}
		}
	}
	return sum, res
}
