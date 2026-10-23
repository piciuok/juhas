package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// MaxFileView caps how much of a file the proposal page shows.
const MaxFileView = 1 << 20

// FileView is one changed file of a PR/MR, for the proposal page: its diff
// and its content at the reviewed commit.
type FileView struct {
	Path      string `json:"path"`
	Patch     string `json:"patch"`   // unified diff hunks ("" if not in the diff)
	Content   string `json:"content"` // the file at ref ("" if deleted or binary)
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Deleted   bool   `json:"deleted,omitempty"`
	Error     string `json:"error,omitempty"` // content could not be fetched (the patch may still be there)
}

// File fetches one file of a PR/MR as the account that found it. ref is the
// commit to read the content at (the reviewed head; empty means the PR/MR's head).
func File(ctx context.Context, it Item, path, ref string) (FileView, error) {
	v := FileView{Path: path}
	env, err := ItemAccount(it).Env(ctx)
	if err != nil {
		return v, err
	}
	if ref == "" {
		ref = it.HeadSHA
	}
	host, project := SplitKey(it.Repo)
	var raw []byte
	if it.Platform == GitHub {
		if v.Patch, v.Deleted, err = ghPatch(ctx, env, host, project, it.Number, path); err != nil {
			return v, err
		}
		if !v.Deleted {
			raw, err = run(ctx, env, "gh", "api", "--hostname", host, "-H", "Accept: application/vnd.github.raw",
				fmt.Sprintf("repos/%s/contents/%s?ref=%s", project, escapePath(path), url.QueryEscape(ref)))
		}
	} else {
		if v.Patch, v.Deleted, err = glPatch(ctx, env, host, project, it.Number, path); err != nil {
			return v, err
		}
		if !v.Deleted {
			raw, err = run(ctx, env, "glab", "api", "--hostname", host,
				fmt.Sprintf("projects/%s/repository/files/%s/raw?ref=%s", url.PathEscape(project), url.PathEscape(path), url.QueryEscape(ref)))
		}
	}
	if err != nil {
		v.Error = err.Error()
		return v, nil
	}
	if len(raw) > MaxFileView {
		raw, v.Truncated = raw[:MaxFileView], true
	}
	if !utf8.Valid(raw) && !v.Truncated || strings.ContainsRune(string(raw), 0) {
		v.Binary = true
		return v, nil
	}
	v.Content = strings.ToValidUTF8(string(raw), "")
	return v, nil
}

// escapePath escapes each segment of a repo path for a URL.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func ghPatch(ctx context.Context, env []string, host, project string, number int, path string) (string, bool, error) {
	for page := 1; page <= 30; page++ { // GitHub lists at most 3000 files
		var files []struct {
			Filename string `json:"filename"`
			Status   string `json:"status"`
			Patch    string `json:"patch"`
		}
		b, err := run(ctx, env, "gh", "api", "--hostname", host, fmt.Sprintf("repos/%s/pulls/%d/files?per_page=100&page=%d", project, number, page))
		if err != nil {
			return "", false, err
		}
		if err := json.Unmarshal(b, &files); err != nil {
			return "", false, fmt.Errorf("gh api pulls/files: %w", err)
		}
		for _, f := range files {
			if f.Filename == path {
				return f.Patch, f.Status == "removed", nil
			}
		}
		if len(files) < 100 {
			break
		}
	}
	return "", false, nil
}

func glPatch(ctx context.Context, env []string, host, project string, number int, path string) (string, bool, error) {
	for page := 1; page <= 30; page++ {
		var diffs []struct {
			NewPath string `json:"new_path"`
			Deleted bool   `json:"deleted_file"`
			Diff    string `json:"diff"`
		}
		b, err := run(ctx, env, "glab", "api", "--hostname", host,
			fmt.Sprintf("projects/%s/merge_requests/%d/diffs?per_page=100&page=%d", url.PathEscape(project), number, page))
		if err != nil {
			return "", false, err
		}
		if err := json.Unmarshal(b, &diffs); err != nil {
			return "", false, fmt.Errorf("glab api diffs: %w", err)
		}
		for _, d := range diffs {
			if d.NewPath == path {
				return d.Diff, d.Deleted, nil
			}
		}
		if len(diffs) < 100 {
			break
		}
	}
	return "", false, nil
}
