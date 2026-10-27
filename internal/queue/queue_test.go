package queue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/piciuok/juhas/internal/config"
	"github.com/piciuok/juhas/internal/forge"
)

type fakeForge struct {
	host      string
	user      string
	requested []forge.Item
	open      map[string][]forge.Item
	err       error
}

func (f *fakeForge) Host() string     { return f.host }
func (f *fakeForge) Platform() string { return forge.GitLab }
func (f *fakeForge) Account() forge.Account {
	return forge.Account{Platform: forge.GitLab, Host: f.host}
}
func (f *fakeForge) Me(context.Context) (string, error) {
	if f.user != "" {
		return f.user, nil
	}
	return "me", nil
}
func (f *fakeForge) Requested(context.Context) ([]forge.Item, error) {
	return f.requested, f.err
}
func (f *fakeForge) Open(_ context.Context, p string) ([]forge.Item, error) { return f.open[p], nil }
func (f *fakeForge) ListOpen(_ context.Context, p string) ([]forge.Item, error) {
	if p == "team/other" {
		return []forge.Item{mr("gl.example/team/other", 1, nil), mr("gl.example/team/other", 2, nil)}, nil
	}
	return f.open[p], nil
}
func (f *fakeForge) Get(context.Context, string, int) (forge.Item, error) { return forge.Item{}, nil }

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func mr(repo string, n int, mod func(*forge.Item)) forge.Item {
	it := forge.Item{Platform: forge.GitLab, Repo: repo, Number: n, URL: repo + "/" + string(rune('a'+n)), Author: "dev", CreatedAt: now.Add(-time.Hour)}
	if mod != nil {
		mod(&it)
	}
	return it
}

const cfgJSON = `{
  "gitlab": [{"host": "gl.example"}],
  "github": [{"host": "gh.example"}],
  "scan_dirs": [],
  "defaults": {"urgent": {"labels": ["urgent"], "older_than": "24h"}},
  "repos": {
    "gl.example/team/app": {"mode": "open"},
    "gl.example/team/quiet": {"on_new": "off"}
  }
}`

func TestCollect(t *testing.T) {
	cfg, err := config.Parse([]byte(cfgJSON), "t")
	if err != nil {
		t.Fatal(err)
	}
	app := "gl.example/team/app"
	gl := &fakeForge{host: "gl.example",
		requested: []forge.Item{
			mr("gl.example/team/other", 1, func(i *forge.Item) { i.Requested = true; i.Labels = []string{"Urgent"} }),
			mr("gl.example/team/quiet", 2, func(i *forge.Item) { i.Requested = true }),
			// same MR also comes from the open listing below: flags must merge
			mr(app, 5, func(i *forge.Item) { i.Assigned = true; i.CreatedAt = now.Add(-48 * time.Hour) }),
		},
		open: map[string][]forge.Item{"team/app": {
			mr(app, 3, nil), // plain open MR, nobody asked me: kept in open mode
			mr(app, 4, func(i *forge.Item) { i.Mine = true }),
			mr(app, 5, nil),
			mr(app, 6, func(i *forge.Item) { i.Draft = true }),
			mr(app, 7, func(i *forge.Item) { i.Author = "project_123_bot_abc" }),
			mr(app, 8, func(i *forge.Item) { i.ApprovedByMe = true }),
		}},
	}
	gh := &fakeForge{host: "gh.example", err: errors.New("gh down")}
	c := &Collector{Cfg: cfg, Forges: map[string]forge.Forge{"gl.example": gl, "gh.example": gh}, Now: func() time.Time { return now }}

	snap := c.Collect(context.Background())

	var got []string
	for _, e := range snap.Entries {
		s := e.Repo + e.Ref()
		if e.Urgent {
			s += "!"
		}
		got = append(got, s)
	}
	want := "gl.example/team/app!5! gl.example/team/other!1! gl.example/team/app!3"
	if strings.Join(got, " ") != want {
		t.Errorf("entries = %s\nwant      %s", strings.Join(got, " "), want)
	}
	if len(snap.Errors) != 1 || snap.Errors[0] != "gh down" {
		t.Errorf("errors = %v", snap.Errors)
	}
	if snap.UrgentCount() != 2 {
		t.Errorf("urgent = %d", snap.UrgentCount())
	}
	stats := map[string]RepoStat{}
	for _, r := range snap.Repos {
		stats[r.Key] = r
	}
	if s := stats[app]; s.Open != 6 || s.Queued != 2 || s.Urgent != 1 {
		t.Errorf("app stat = %+v (open = all listed, queued = kept)", s)
	}
	if s := stats["gl.example/team/other"]; s.Open != 2 || s.Queued != 1 || len(s.All) != 2 {
		t.Errorf("other stat = %+v (open via ListOpen)", s)
	}
	if s := stats[app]; len(s.All) != 6 || s.All[0].Number < s.All[1].Number {
		t.Errorf("app: all open PR/MRs, newest first: %+v", s.All)
	}
	if _, ok := stats["gl.example/team/quiet"]; ok {
		t.Error("on_new=off repo must not show up")
	}
}

func TestIsUrgentNeedsMe(t *testing.T) {
	u := config.Urgent{Labels: []string{"hotfix"}, TargetBranches: []string{"release/*"}}
	it := forge.Item{Labels: []string{"hotfix"}, TargetBranch: "release/1.2"}
	if ok, _ := IsUrgent(it, u, now); ok {
		t.Error("not requested/assigned: must not be urgent")
	}
	it.Requested = true
	it.Labels = nil
	if ok, why := IsUrgent(it, u, now); !ok || !strings.Contains(why, "release/1.2") {
		t.Errorf("target branch glob: %v %q", ok, why)
	}
}

func TestMatchAuthor(t *testing.T) {
	p := []string{"re:^project_\\d+_bot", "Renovate"}
	for a, want := range map[string]bool{"project_123_bot_3f02": true, "renovate": true, "klaudia": false, "my_project_1_bot": false} {
		if MatchAuthor(a, p) != want {
			t.Errorf("MatchAuthor(%q) != %v", a, want)
		}
	}
}

func TestSplit(t *testing.T) {
	s := &Snapshot{
		Entries: []Entry{{Item: forge.Item{Repo: "a/b", URL: "1"}, Urgent: true}, {Item: forge.Item{Repo: "a/b", URL: "2"}}},
		Repos:   []RepoStat{{Key: "a/b", Queued: 2, Urgent: 1, Open: 5}},
	}
	active, hist := s.Split(func(e Entry) bool { return e.URL == "1" })
	if len(active.Entries) != 1 || len(hist) != 1 || active.Repos[0].Queued != 1 || active.Repos[0].Urgent != 0 || active.Repos[0].Open != 5 {
		t.Errorf("active=%+v hist=%+v", active, hist)
	}
	if len(s.Entries) != 2 || s.Repos[0].Queued != 2 {
		t.Error("Split modified the original snapshot")
	}
}

func TestCollectAccounts(t *testing.T) {
	cfg, err := config.Parse([]byte(`{
  "github": [{"host": "gh.example", "user": "work"}, {"host": "gh.example", "user": "home"}, {"host": "gh.example", "user": "old", "watch": false}],
  "scan_dirs": [],
  "repos": {"gh.example/home/tool": {"mode": "open", "account": "home"}}
}`), "t")
	if err != nil {
		t.Fatal(err)
	}
	pr := func(repo string, n int, acc string, mod func(*forge.Item)) forge.Item {
		it := forge.Item{Platform: forge.GitHub, Repo: repo, Number: n, URL: repo + "/" + string(rune('a'+n)), Author: "dev", Account: acc}
		if mod != nil {
			mod(&it)
		}
		return it
	}
	work := &fakeForge{host: "gh.example", user: "work", requested: []forge.Item{
		pr("gh.example/acme/app", 1, "gh.example@work", func(i *forge.Item) { i.Requested = true }),
		pr("gh.example/home/tool", 2, "gh.example@work", func(i *forge.Item) { i.Author = "home" }), // seen from work, written by home
	}}
	home := &fakeForge{host: "gh.example", user: "home", requested: []forge.Item{
		pr("gh.example/acme/app", 1, "gh.example@home", nil), // the same PR, not asked of home
	}, open: map[string][]forge.Item{"home/tool": {pr("gh.example/home/tool", 3, "gh.example@home", nil), pr("gh.example/home/tool", 2, "gh.example@home", func(i *forge.Item) { i.Author = "home" })}}}
	old := &fakeForge{host: "gh.example", user: "old", err: errors.New("must not be asked")}
	c := &Collector{Cfg: cfg, Now: func() time.Time { return now },
		Forges:    map[string]forge.Forge{"gh.example@work": work, "gh.example@home": home, "gh.example@old": old},
		Order:     []string{"gh.example@work", "gh.example@home", "gh.example@old"},
		Unwatched: map[string]bool{"gh.example@old": true}}
	snap := c.Collect(context.Background())
	if len(snap.Errors) != 0 {
		t.Fatalf("errors: %v", snap.Errors)
	}
	got := map[string]string{}
	for _, e := range snap.Entries {
		got[e.Repo+e.Ref()] = e.Account
	}
	if got["gh.example/acme/app#1"] != "gh.example@work" {
		t.Errorf("PR asked of work must act as work: %v", got)
	}
	if got["gh.example/home/tool#3"] != "gh.example@home" {
		t.Errorf("open repo read by its account: %v", got)
	}
	if _, ok := got["gh.example/home/tool#2"]; ok {
		t.Errorf("a PR by another of my accounts is mine: %v", got)
	}
	if f, _ := c.ForRepo("gh.example", ""); f != work {
		t.Error("ForRepo without account: the first watched one")
	}
}
