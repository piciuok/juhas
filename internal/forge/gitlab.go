package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// GitLabForge talks to one GitLab host through `glab api`, reusing glab's auth.
type GitLabForge struct {
	host  string
	acc   Account
	me    string
	mu    sync.Mutex
	files map[string]*Files // by MR URL + head SHA
}

func NewGitLab(a Account) *GitLabForge {
	a.Platform = GitLab
	return &GitLabForge{host: a.Host, acc: a}
}

func (g *GitLabForge) Host() string     { return g.host }
func (g *GitLabForge) Account() Account { return g.acc }
func (g *GitLabForge) Platform() string { return GitLab }

type glUser struct {
	Username string `json:"username"`
}

type glMR struct {
	IID          int       `json:"iid"`
	ProjectID    int       `json:"project_id"`
	Title        string    `json:"title"`
	WebURL       string    `json:"web_url"`
	Draft        bool      `json:"draft"`
	WIP          bool      `json:"work_in_progress"`
	SHA          string    `json:"sha"`
	SourceBranch string    `json:"source_branch"`
	TargetBranch string    `json:"target_branch"`
	Labels       []string  `json:"labels"`
	CreatedAt    time.Time `json:"created_at"`
	Author       glUser    `json:"author"`
	Assignees    []glUser  `json:"assignees"`
	Reviewers    []glUser  `json:"reviewers"`
	References   struct {
		Full string `json:"full"`
	} `json:"references"`
}

func (g *GitLabForge) api(ctx context.Context, path string, v any) error {
	out, err := runAs(ctx, g.acc, "glab", "api", "--hostname", g.host, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("glab api %s (%s): %w", path, g.host, err)
	}
	return nil
}

func (g *GitLabForge) Me(ctx context.Context) (string, error) {
	g.mu.Lock()
	me := g.me
	g.mu.Unlock()
	if me != "" {
		return me, nil
	}
	var u glUser
	if err := g.api(ctx, "user", &u); err != nil {
		return "", err
	}
	if u.Username == "" {
		return "", fmt.Errorf("glab (%s): not authenticated", g.host)
	}
	g.mu.Lock()
	g.me = u.Username
	g.mu.Unlock()
	return u.Username, nil
}

// Requested lists open MRs where I am a reviewer or an assignee (not author).
func (g *GitLabForge) Requested(ctx context.Context) ([]Item, error) {
	me, err := g.Me(ctx)
	if err != nil {
		return nil, err
	}
	var items []Item
	seen := map[string]int{}
	for _, filter := range []string{"reviewer_username", "assignee_username"} {
		var mrs []glMR
		q := fmt.Sprintf("merge_requests?%s=%s&state=opened&scope=all&per_page=100", filter, url.QueryEscape(me))
		if err := g.api(ctx, q, &mrs); err != nil {
			return nil, err
		}
		for _, mr := range mrs {
			it := g.item(mr, me)
			if it.Mine {
				continue
			}
			if i, ok := seen[it.URL]; ok {
				items[i].Requested = items[i].Requested || it.Requested
				items[i].Assigned = items[i].Assigned || it.Assigned
				continue
			}
			seen[it.URL] = len(items)
			items = append(items, it)
		}
	}
	if err := g.fillApprovals(ctx, items, me); err != nil {
		return items, err
	}
	g.fillFiles(ctx, items)
	return items, nil
}

// Open lists every open MR of a project. Approvals are fetched only for MRs
// that are neither mine nor drafts, since only those can reach the queue.
func (g *GitLabForge) Open(ctx context.Context, project string) ([]Item, error) {
	me, err := g.Me(ctx)
	if err != nil {
		return nil, err
	}
	var mrs []glMR
	if err := g.api(ctx, "projects/"+url.PathEscape(project)+"/merge_requests?state=opened&per_page=100", &mrs); err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(mrs))
	for _, mr := range mrs {
		items = append(items, g.item(mr, me))
	}
	if err := g.fillApprovals(ctx, items, me); err != nil {
		return items, err
	}
	g.fillFiles(ctx, items)
	return items, nil
}

// fillFiles counts changed files per MR (GET .../diffs, GitLab 15.7+),
// cached by head SHA so an unchanged MR costs nothing on the next poll.
// Errors leave Files nil: the counter is cosmetic.
func (g *GitLabForge) fillFiles(ctx context.Context, items []Item) {
	for i := range items {
		it := &items[i]
		if it.Mine || it.Draft || it.ApprovedByMe {
			continue
		}
		key := it.URL + "@" + it.HeadSHA
		g.mu.Lock()
		f, ok := g.files[key]
		g.mu.Unlock()
		if !ok {
			var err error
			if f, err = g.countFiles(ctx, it); err != nil {
				continue
			}
			g.mu.Lock()
			if g.files == nil {
				g.files = map[string]*Files{}
			}
			g.files[key] = f
			g.mu.Unlock()
		}
		it.Files = f
	}
}

func (g *GitLabForge) countFiles(ctx context.Context, it *Item) (*Files, error) {
	_, project := SplitKey(it.Repo)
	f := &Files{}
	for page := 1; page <= 10; page++ {
		var diffs []struct {
			New     bool `json:"new_file"`
			Deleted bool `json:"deleted_file"`
		}
		path := fmt.Sprintf("projects/%s/merge_requests/%d/diffs?per_page=100&page=%d", url.PathEscape(project), it.Number, page)
		if err := g.api(ctx, path, &diffs); err != nil {
			return nil, err
		}
		for _, d := range diffs {
			switch {
			case d.New:
				f.Added++
			case d.Deleted:
				f.Deleted++
			default:
				f.Modified++
			}
		}
		if len(diffs) < 100 {
			break
		}
	}
	return f, nil
}

func (g *GitLabForge) fillApprovals(ctx context.Context, items []Item, me string) error {
	failed, tried := 0, 0
	var last error
	for i := range items {
		it := &items[i]
		if it.Mine || it.Draft {
			continue
		}
		tried++
		_, project := SplitKey(it.Repo)
		var ap struct {
			ApprovedBy []struct {
				User glUser `json:"user"`
			} `json:"approved_by"`
		}
		path := fmt.Sprintf("projects/%s/merge_requests/%d/approvals", url.PathEscape(project), it.Number)
		if err := g.api(ctx, path, &ap); err != nil {
			// one flaky call must not drop the whole host; unknown approvals
			// only mean the MR may stay in the queue until the next poll
			failed++
			last = err
			continue
		}
		for _, a := range ap.ApprovedBy {
			if strings.EqualFold(a.User.Username, me) {
				it.ApprovedByMe = true
			}
		}
	}
	if tried > 0 && failed == tried {
		return last // e.g. offline: report it, like any other failed call
	}
	return nil
}

func (g *GitLabForge) CountOpen(ctx context.Context, project string) (int, error) {
	var mrs []struct {
		IID int `json:"iid"`
	}
	// per_page=100 is enough for a counter; anything above shows as 100.
	if err := g.api(ctx, "projects/"+url.PathEscape(project)+"/merge_requests?state=opened&per_page=100", &mrs); err != nil {
		return 0, err
	}
	return len(mrs), nil
}

func (g *GitLabForge) item(mr glMR, me string) Item {
	project := mr.References.Full
	if i := strings.LastIndex(project, "!"); i >= 0 {
		project = project[:i]
	}
	it := Item{
		Platform:     GitLab,
		Repo:         strings.ToLower(g.host) + "/" + project,
		Number:       mr.IID,
		URL:          mr.WebURL,
		Title:        mr.Title,
		Author:       mr.Author.Username,
		Draft:        mr.Draft || mr.WIP,
		HeadSHA:      mr.SHA,
		SourceBranch: mr.SourceBranch,
		TargetBranch: mr.TargetBranch,
		Labels:       mr.Labels,
		CreatedAt:    mr.CreatedAt,
		Mine:         strings.EqualFold(mr.Author.Username, me),
		Account:      g.acc.Key(),
	}
	for _, u := range mr.Reviewers {
		if strings.EqualFold(u.Username, me) {
			it.Requested = true
		}
	}
	for _, u := range mr.Assignees {
		if strings.EqualFold(u.Username, me) {
			it.Assigned = true
		}
	}
	return it
}

// RawMR is the cheap view of an open MR used by doctor's mode hint.
type RawMR struct {
	IID       int
	Reviewers int
}

// OpenRaw lists open MRs of a project without fetching approvals.
func (g *GitLabForge) OpenRaw(ctx context.Context, project string) ([]RawMR, error) {
	var mrs []glMR
	if err := g.api(ctx, "projects/"+url.PathEscape(project)+"/merge_requests?state=opened&per_page=100", &mrs); err != nil {
		return nil, err
	}
	out := make([]RawMR, len(mrs))
	for i, mr := range mrs {
		out[i] = RawMR{IID: mr.IID, Reviewers: len(mr.Reviewers)}
	}
	return out, nil
}

func (g *GitLabForge) Get(ctx context.Context, project string, number int) (Item, error) {
	me, err := g.Me(ctx)
	if err != nil {
		return Item{}, err
	}
	var mr glMR
	if err := g.api(ctx, fmt.Sprintf("projects/%s/merge_requests/%d", url.PathEscape(project), number), &mr); err != nil {
		return Item{}, err
	}
	items := []Item{g.item(mr, me)}
	err = g.fillApprovals(ctx, items, me)
	g.fillFiles(ctx, items)
	return items[0], err
}
