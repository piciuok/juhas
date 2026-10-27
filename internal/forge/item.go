package forge

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	GitHub = "github"
	GitLab = "gitlab"
)

// Item is one open pull/merge request.
type Item struct {
	Platform     string    `json:"platform"`
	Repo         string    `json:"repo"` // host/path key
	Number       int       `json:"number"`
	URL          string    `json:"url"`
	Title        string    `json:"title"`
	Author       string    `json:"author"`
	Draft        bool      `json:"draft"`
	HeadSHA      string    `json:"head_sha"`
	SourceBranch string    `json:"source_branch"`
	TargetBranch string    `json:"target_branch"`
	Labels       []string  `json:"labels"`
	CreatedAt    time.Time `json:"created_at"`

	Files *Files `json:"files,omitempty"` // nil = not fetched

	Account string `json:"account,omitempty"` // Account.Key that found it ("" = the CLI's default)

	Requested    bool `json:"requested"`      // my review was requested / I am a reviewer
	Assigned     bool `json:"assigned"`       // I am an assignee
	ApprovedByMe bool `json:"approved_by_me"` // my approval is on it
	Mine         bool `json:"mine"`           // I am the author
}

// Files counts the changed files of a PR/MR by kind.
type Files struct {
	Added    int `json:"added"`
	Modified int `json:"modified"` // includes renamed
	Deleted  int `json:"deleted"`
	More     int `json:"more"` // files beyond what was listed (kind unknown)
}

// String is the compact "+2 ~5 −1" form (omitting zeros).
func (f *Files) String() string {
	if f == nil {
		return ""
	}
	var parts []string
	if f.Added > 0 {
		parts = append(parts, "+"+strconv.Itoa(f.Added))
	}
	if f.Modified > 0 {
		parts = append(parts, "~"+strconv.Itoa(f.Modified))
	}
	if f.Deleted > 0 {
		parts = append(parts, "−"+strconv.Itoa(f.Deleted))
	}
	if f.More > 0 {
		parts = append(parts, "…"+strconv.Itoa(f.More))
	}
	if len(parts) == 0 {
		return "0"
	}
	return strings.Join(parts, " ")
}

// Ref is the short human form: "!883" on GitLab, "#340" on GitHub.
func (i Item) Ref() string {
	if i.Platform == GitLab {
		return "!" + strconv.Itoa(i.Number)
	}
	return "#" + strconv.Itoa(i.Number)
}

// Forge lists PR/MRs from one host, as one account.
type Forge interface {
	Host() string
	Account() Account
	Platform() string
	// Me is the authenticated username.
	Me(ctx context.Context) (string, error)
	// Requested lists open PR/MRs across the host where I am asked to review
	// (or am assigned).
	Requested(ctx context.Context) ([]Item, error)
	// Open lists all open PR/MRs of one project (path without host).
	Open(ctx context.Context, project string) ([]Item, error)
	// ListOpen lists all open PR/MRs of one project like Open, but without
	// the extra per-item calls (approvals, file counts): for "Show all".
	ListOpen(ctx context.Context, project string) ([]Item, error)
	// Get fetches one PR/MR.
	Get(ctx context.Context, project string, number int) (Item, error)
	// Projects lists the projects I am a member of, recently active first
	// and filtered by search when set (paths without host): to pin one.
	Projects(ctx context.Context, search string) ([]string, error)
}

// ParseURL extracts platform, repo key and number from a PR/MR web URL:
//
//	https://github.com/o/r/pull/340
//	https://gitlab.example.com/group/sub/proj/-/merge_requests/883
func ParseURL(raw string) (platform, repo string, number int, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", "", 0, fmt.Errorf("unrecognised PR/MR URL: %q", raw)
	}
	p := strings.Trim(u.Path, "/")
	host := strings.ToLower(u.Hostname())
	if i := strings.Index(p, "/-/merge_requests/"); i > 0 {
		n, err := strconv.Atoi(strings.SplitN(p[i+len("/-/merge_requests/"):], "/", 2)[0])
		if err != nil {
			return "", "", 0, fmt.Errorf("bad MR number in %q", raw)
		}
		return GitLab, host + "/" + p[:i], n, nil
	}
	parts := strings.Split(p, "/")
	if len(parts) >= 4 && parts[2] == "pull" {
		n, err := strconv.Atoi(parts[3])
		if err != nil {
			return "", "", 0, fmt.Errorf("bad PR number in %q", raw)
		}
		return GitHub, host + "/" + parts[0] + "/" + parts[1], n, nil
	}
	return "", "", 0, fmt.Errorf("unrecognised PR/MR URL: %q", raw)
}

// New returns the forge for an account.
func New(a Account) Forge {
	if a.Platform == GitLab {
		return NewGitLab(a)
	}
	return NewGitHub(a)
}
