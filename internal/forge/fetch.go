package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// Context is what a reviewer without network access needs, fetched up front.
type Context struct {
	Discussion string // description, metadata and comments
	Diff       string
	CI         string
}

// Fetch downloads a PR/MR's discussion, diff and CI status through gh/glab.
// The diff is required; discussion and CI are best effort.
func Fetch(ctx context.Context, it Item) (Context, error) {
	var c Context
	var err error
	if it.Platform == GitHub {
		out, _ := run(ctx, nil, "gh", "pr", "view", it.URL, "--comments")
		c.Discussion = string(out)
		var d []byte
		if d, err = run(ctx, nil, "gh", "pr", "diff", it.URL); err != nil {
			return c, err
		}
		c.Diff = string(d)
		ci, _ := run(ctx, nil, "gh", "pr", "checks", it.URL) // non-zero while checks are pending
		c.CI = string(ci)
		return c, nil
	}
	host, project := SplitKey(it.Repo)
	repo := fmt.Sprintf("https://%s/%s", host, project)
	n := fmt.Sprint(it.Number)
	out, _ := run(ctx, nil, "glab", "mr", "view", n, "-R", repo, "--comments")
	c.Discussion = string(out)
	d, err := run(ctx, nil, "glab", "mr", "diff", n, "-R", repo, "--color=never")
	if err != nil {
		return c, err
	}
	c.Diff = string(d)
	var mr struct {
		HeadPipeline *struct {
			Status string `json:"status"`
			WebURL string `json:"web_url"`
		} `json:"head_pipeline"`
	}
	if b, err := run(ctx, nil, "glab", "api", "--hostname", host, fmt.Sprintf("projects/%s/merge_requests/%d", url.PathEscape(project), it.Number)); err == nil {
		if json.Unmarshal(b, &mr) == nil && mr.HeadPipeline != nil {
			c.CI = fmt.Sprintf("pipeline: %s\n%s\n", mr.HeadPipeline.Status, mr.HeadPipeline.WebURL)
		}
	}
	if c.CI == "" {
		c.CI = "brak informacji o pipeline\n"
	}
	return c, nil
}

// HeadRef is the remote ref that holds a PR/MR's head, also for forks.
func HeadRef(it Item) string {
	if it.Platform == GitHub {
		return fmt.Sprintf("pull/%d/head", it.Number)
	}
	return fmt.Sprintf("merge-requests/%d/head", it.Number)
}
