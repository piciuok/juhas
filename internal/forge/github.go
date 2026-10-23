package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// GitHubForge talks to one GitHub host through `gh api graphql`, so it reuses
// gh's auth (keyring) and works the same for github.com and Enterprise.
type GitHubForge struct {
	host string
	acc  Account
	me   string
	mu   sync.Mutex
}

func NewGitHub(a Account) *GitHubForge {
	a.Platform = GitHub
	return &GitHubForge{host: a.Host, acc: a}
}

func (g *GitHubForge) Host() string     { return g.host }
func (g *GitHubForge) Account() Account { return g.acc }
func (g *GitHubForge) Platform() string { return GitHub }

const ghPRFields = `
  url number title isDraft createdAt headRefOid headRefName baseRefName
  author { login }
  repository { nameWithOwner }
  labels(first: 20) { nodes { name } }
  assignees(first: 10) { nodes { login } }
  reviewRequests(first: 20) { nodes { requestedReviewer { ... on User { login } } } }
  latestReviews(first: 20) { nodes { state author { login } } }
  files(first: 100) { totalCount nodes { changeType } }`

type ghPR struct {
	URL         string    `json:"url"`
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	IsDraft     bool      `json:"isDraft"`
	CreatedAt   time.Time `json:"createdAt"`
	HeadRefOid  string    `json:"headRefOid"`
	HeadRefName string    `json:"headRefName"`
	BaseRefName string    `json:"baseRefName"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Assignees struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer struct {
				Login string `json:"login"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	Files *struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			ChangeType string `json:"changeType"`
		} `json:"nodes"`
	} `json:"files"`
	LatestReviews struct {
		Nodes []struct {
			State  string `json:"state"`
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
		} `json:"nodes"`
	} `json:"latestReviews"`
}

type ghSearch struct {
	Data struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
		Search struct {
			IssueCount int    `json:"issueCount"`
			Nodes      []ghPR `json:"nodes"`
		} `json:"search"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (g *GitHubForge) search(ctx context.Context, q string, first int) (*ghSearch, error) {
	query := fmt.Sprintf(`query($q: String!) { viewer { login } search(query: $q, type: ISSUE, first: %d) { issueCount nodes { ... on PullRequest { %s } } } }`, first, ghPRFields)
	out, err := runAs(ctx, g.acc, "gh", "api", "graphql", "--hostname", g.host, "-f", "q="+q, "-f", "query="+query)
	if err != nil {
		return nil, err
	}
	var res ghSearch
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("gh graphql (%s): %w", g.host, err)
	}
	if len(res.Errors) > 0 {
		return nil, fmt.Errorf("gh graphql (%s): %s", g.host, res.Errors[0].Message)
	}
	if l := res.Data.Viewer.Login; l != "" {
		g.mu.Lock()
		g.me = l
		g.mu.Unlock()
	}
	return &res, nil
}

func (g *GitHubForge) Me(ctx context.Context) (string, error) {
	g.mu.Lock()
	me := g.me
	g.mu.Unlock()
	if me != "" {
		return me, nil
	}
	out, err := runAs(ctx, g.acc, "gh", "api", "--hostname", g.host, "user", "--jq", ".login")
	if err != nil {
		return "", err
	}
	me = strings.TrimSpace(string(out))
	g.mu.Lock()
	g.me = me
	g.mu.Unlock()
	return me, nil
}

// Requested covers direct and team review requests, plus PRs assigned to me.
func (g *GitHubForge) Requested(ctx context.Context) ([]Item, error) {
	var items []Item
	seen := map[string]int{}
	for _, q := range []string{
		"is:pr is:open archived:false review-requested:@me",
		"is:pr is:open archived:false assignee:@me -author:@me",
	} {
		res, err := g.search(ctx, q, 100)
		if err != nil {
			return nil, err
		}
		for _, pr := range res.Data.Search.Nodes {
			if pr.URL == "" {
				continue
			}
			it := g.item(pr, res.Data.Viewer.Login)
			if strings.Contains(q, "review-requested") {
				it.Requested = true // includes team requests, which item() cannot see
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
	return items, nil
}

func (g *GitHubForge) Open(ctx context.Context, project string) ([]Item, error) {
	res, err := g.search(ctx, "is:pr is:open repo:"+project, 100)
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, pr := range res.Data.Search.Nodes {
		if pr.URL != "" {
			items = append(items, g.item(pr, res.Data.Viewer.Login))
		}
	}
	return items, nil
}

func (g *GitHubForge) CountOpen(ctx context.Context, project string) (int, error) {
	res, err := g.search(ctx, "is:pr is:open repo:"+project, 1)
	if err != nil {
		return 0, err
	}
	return res.Data.Search.IssueCount, nil
}

func (g *GitHubForge) item(pr ghPR, me string) Item {
	it := Item{
		Platform:     GitHub,
		Repo:         strings.ToLower(g.host) + "/" + pr.Repository.NameWithOwner,
		Number:       pr.Number,
		URL:          pr.URL,
		Title:        pr.Title,
		Author:       pr.Author.Login,
		Draft:        pr.IsDraft,
		HeadSHA:      pr.HeadRefOid,
		SourceBranch: pr.HeadRefName,
		TargetBranch: pr.BaseRefName,
		CreatedAt:    pr.CreatedAt,
		Mine:         me != "" && strings.EqualFold(pr.Author.Login, me),
		Account:      g.acc.Key(),
	}
	for _, l := range pr.Labels.Nodes {
		it.Labels = append(it.Labels, l.Name)
	}
	if pr.Files != nil {
		f := &Files{More: pr.Files.TotalCount - len(pr.Files.Nodes)}
		for _, n := range pr.Files.Nodes {
			switch n.ChangeType {
			case "ADDED", "COPIED":
				f.Added++
			case "DELETED":
				f.Deleted++
			default: // MODIFIED, RENAMED, CHANGED
				f.Modified++
			}
		}
		it.Files = f
	}
	for _, a := range pr.Assignees.Nodes {
		if strings.EqualFold(a.Login, me) {
			it.Assigned = true
		}
	}
	for _, r := range pr.ReviewRequests.Nodes {
		if strings.EqualFold(r.RequestedReviewer.Login, me) {
			it.Requested = true
		}
	}
	for _, r := range pr.LatestReviews.Nodes {
		if strings.EqualFold(r.Author.Login, me) && r.State == "APPROVED" {
			it.ApprovedByMe = true
		}
	}
	return it
}

func (g *GitHubForge) Get(ctx context.Context, project string, number int) (Item, error) {
	owner, name, _ := strings.Cut(project, "/")
	query := fmt.Sprintf(`query($o: String!, $n: String!, $num: Int!) { viewer { login } repository(owner: $o, name: $n) { pullRequest(number: $num) { %s } } }`, ghPRFields)
	out, err := runAs(ctx, g.acc, "gh", "api", "graphql", "--hostname", g.host, "-f", "o="+owner, "-f", "n="+name, "-F", fmt.Sprintf("num=%d", number), "-f", "query="+query)
	if err != nil {
		return Item{}, err
	}
	var res struct {
		Data struct {
			Viewer struct {
				Login string `json:"login"`
			} `json:"viewer"`
			Repository struct {
				PullRequest *ghPR `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return Item{}, err
	}
	if len(res.Errors) > 0 {
		return Item{}, fmt.Errorf("gh graphql: %s", res.Errors[0].Message)
	}
	if res.Data.Repository.PullRequest == nil {
		return Item{}, fmt.Errorf("%s/%s#%d: not found", g.host, project, number)
	}
	return g.item(*res.Data.Repository.PullRequest, res.Data.Viewer.Login), nil
}
