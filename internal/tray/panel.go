package tray

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/piciuok/juhas/internal/app"
	"github.com/piciuok/juhas/internal/config"
	"github.com/piciuok/juhas/internal/doctor"
	"github.com/piciuok/juhas/internal/effects"
	"github.com/piciuok/juhas/internal/forge"
	"github.com/piciuok/juhas/internal/platform"
	"github.com/piciuok/juhas/internal/queue"
	"github.com/piciuok/juhas/internal/review"
	"github.com/piciuok/juhas/internal/scan"
	"github.com/piciuok/juhas/internal/state"
)

// The panel is the browser page that mirrors the tray menu but stays open
// while you click: native status-bar menus always close on a click.

type panelAgent struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Method string `json:"method"`
}

type panelEntry struct {
	URL       string       `json:"url"`
	Ref       string       `json:"ref"`
	Title     string       `json:"title"`
	Author    string       `json:"author"`
	Files     string       `json:"files"`
	Draft     bool         `json:"draft"`
	Urgent    bool         `json:"urgent"`
	UrgentWhy string       `json:"urgent_why"`
	Acked     bool         `json:"acked"`
	Status    string       `json:"status"` // pending|queued|running|done|failed|skipped|noclone
	Busy      bool         `json:"busy"`
	Stale     bool         `json:"stale"`
	Brief     bool         `json:"brief"`
	LogID     string       `json:"log_id"`
	Error     string       `json:"error"`
	Method    string       `json:"method"` // of the last run
	Agents    []panelAgent `json:"agents"` // default first; empty = cannot run
	Guess     string       `json:"guess"`  // clone found but not mapped
	Proposal  *panelProp   `json:"proposal,omitempty"`
	Requested bool         `json:"requested"`
	Assigned  bool         `json:"assigned"`
}

type panelProp struct {
	Pending  int `json:"pending"`
	Accepted int `json:"accepted"`
	Posted   int `json:"posted"`
	Total    int `json:"total"`
}

type panelRepo struct {
	Key     string       `json:"key"`
	Pinned  bool         `json:"pinned"`
	Inline  bool         `json:"inline"` // show_all: inline
	All     []panelOpen  `json:"all"`    // every open PR/MR (empty = not listed)
	Short   string       `json:"short"`
	Mode    string       `json:"mode"`
	Open    int          `json:"open"`
	Queued  int          `json:"queued"`
	Urgent  int          `json:"urgent"`
	ListURL string       `json:"list_url"`
	Entries []panelEntry `json:"entries"`
}

// panelOpen is one line of a repo's full list.
type panelOpen struct {
	URL    string `json:"url"`
	Ref    string `json:"ref"`
	Title  string `json:"title"`
	Author string `json:"author"`
	Draft  bool   `json:"draft"`
	Queued bool   `json:"queued"` // waits for me: also on the main list
	Status string `json:"status"` // of a review started from this list
	Busy   bool   `json:"busy"`
	LogID  string `json:"log_id"`
	Brief  bool   `json:"brief"`
	Clone  bool   `json:"clone"` // the repo has a local clone: it can be reviewed
}

// panelProject is a project one can pin.
type panelProject struct {
	Key    string `json:"key"`
	Pinned bool   `json:"pinned"`
	Clone  string `json:"clone,omitempty"`
	From   string `json:"from"` // config | clone | forge | typed
}

// panelHistory is one reviewed PR/MR.
type panelHistory struct {
	URL      string     `json:"url"`
	Repo     string     `json:"repo"`
	Ref      string     `json:"ref"`
	Title    string     `json:"title"`
	When     string     `json:"when"`
	Method   string     `json:"method"`
	Open     bool       `json:"open"` // still in the queue (otherwise merged/closed/not mine)
	Brief    bool       `json:"brief"`
	LogID    string     `json:"log_id"`
	Proposal *panelProp `json:"proposal,omitempty"`
}

type panelModel struct {
	PolledAt     string         `json:"polled_at"`
	Polling      bool           `json:"polling"`
	Total        int            `json:"total"`
	Urgent       int            `json:"urgent"`
	Unacked      int            `json:"unacked"`
	Running      int            `json:"running"`
	Waiting      int            `json:"waiting"`
	Concurrency  int            `json:"concurrency"`
	Errors       []string       `json:"errors"`
	Doctor       string         `json:"doctor"`
	ScreenEffect bool           `json:"screen_effect"`
	Autostart    bool           `json:"autostart"`
	Update       updateInfo     `json:"update"`
	Repos        []panelRepo    `json:"repos"`
	History      []panelHistory `json:"history"`
}

// PanelModel implements configui.Panel.
func (a *App) PanelModel() any {
	st, err := a.store.Read()
	if err != nil {
		st = &state.State{Jobs: map[string]*state.Job{}}
	}
	a.mu.Lock()
	snap, rep, cfg, polling, runner := a.snap, a.report, a.cfg, a.polling, a.runner
	a.mu.Unlock()

	m := panelModel{Polling: polling, ScreenEffect: cfg.ScreenEffectOn(), Autostart: platform.AutostartEnabled(), Concurrency: cfg.Concurrency, Errors: []string{}, Update: a.updateInfo()}
	if runner != nil {
		run, wait := runner.Queue()
		m.Running, m.Waiting = len(run), len(wait)
	}
	if rep != nil && rep.Worst() != doctor.OK {
		m.Doctor = strings.TrimSpace(
			strings.Join([]string{plural(rep.Count(doctor.Fail), "error", "errors"), plural(rep.Count(doctor.Warn), "warning", "warnings")}, ", "))
	}
	if snap == nil {
		return m
	}
	full := snap
	snap, _ = split(full, st)
	m.History = history(st, full)
	m.PolledAt = snap.At.Format("15:04:05")
	m.Total = len(snap.Entries)
	m.Urgent = snap.UrgentCount()
	m.Errors = append(m.Errors, snap.Errors...)

	byRepo := map[string][]queue.Entry{}
	for _, e := range snap.Entries {
		byRepo[strings.ToLower(e.Repo)] = append(byRepo[strings.ToLower(e.Repo)], e)
		if e.Urgent && !st.Acked(e.URL) {
			m.Unacked++
		}
	}
	for _, r := range snap.Repos {
		entries := byRepo[strings.ToLower(r.Key)]
		if len(entries) == 0 && !r.Pinned {
			continue
		}
		pr := panelRepo{Key: r.Key, Short: app.ShortRepo(r.Key), Mode: r.Mode, Open: r.Open, Queued: r.Queued, Urgent: r.Urgent,
			ListURL: listURL(r.Key, entries), Pinned: r.Pinned, Inline: r.Inline, Entries: []panelEntry{}, All: []panelOpen{}}
		queued := map[string]bool{}
		for _, e := range entries {
			pr.Entries = append(pr.Entries, a.panelEntry(e, st, cfg, runner))
			queued[e.URL] = true
		}
		clone := a.repoDir(r.Key) != ""
		for _, o := range r.All {
			po := panelOpen{URL: o.URL, Ref: o.Ref, Title: o.Title, Author: o.Author, Draft: o.Draft, Queued: queued[o.URL], Clone: clone}
			po.Busy = runner != nil && runner.Busy(o.URL)
			if j := st.Job(o.URL); j != nil {
				po.Status, po.Brief = j.Status, j.Brief != ""
				if j.Log != "" {
					po.LogID = strings.TrimSuffix(lastPath(j.Log), ".log")
				}
			}
			pr.All = append(pr.All, po)
		}
		m.Repos = append(m.Repos, pr)
	}
	sort.SliceStable(m.Repos, func(i, j int) bool {
		if (m.Repos[i].Urgent > 0) != (m.Repos[j].Urgent > 0) {
			return m.Repos[i].Urgent > 0
		}
		return m.Repos[i].Queued > m.Repos[j].Queued
	})
	return m
}

func (a *App) panelEntry(e queue.Entry, st *state.State, cfg *config.Config, runner *review.Runner) panelEntry {
	dir := app.Dir(e)
	pe := panelEntry{URL: e.URL, Ref: e.Ref(), Title: e.Title, Author: e.Author, Files: e.Files.String(), Draft: e.Draft,
		Urgent: e.Urgent, UrgentWhy: e.UrgentWhy, Acked: st.Acked(e.URL), Status: state.Pending,
		Requested: e.Requested, Assigned: e.Assigned}
	if e.Path == "" && e.PathGuess != "" {
		pe.Guess = config.Contract(e.PathGuess)
	}
	pe.Busy = runner != nil && runner.Busy(e.URL)
	if j := st.Job(e.URL); j != nil {
		pe.Status, pe.Error, pe.Method = j.Status, j.Error, j.Method
		pe.Stale = j.Stale(e.HeadSHA)
		pe.Brief = j.Brief != ""
		if j.Log != "" {
			pe.LogID = strings.TrimSuffix(lastPath(j.Log), ".log")
			if p, err := review.LoadProposal(review.ProposalPath(j.Log)); err == nil {
				pend, acc, posted := p.Counts()
				pe.Proposal = &panelProp{Pending: pend, Accepted: acc, Posted: posted, Total: len(p.All())}
			}
		}
		if j.Status == state.Asked {
			pe.Status = state.Pending
		}
	}
	if (pe.Status == state.Running || pe.Status == state.Queued) && !pe.Busy {
		pe.Status = state.Failed // left over by a process that is gone
	}
	if dir == "" {
		if pe.Status == state.Pending {
			pe.Status = "noclone"
		}
		return pe
	}
	s, err := cfg.Effective(e.Repo)
	if err != nil {
		return pe
	}
	agents := []string{s.Agent}
	for _, ag := range config.Agents {
		if ag != s.Agent {
			agents = append(agents, ag)
		}
	}
	for _, ag := range agents {
		m := review.ResolveFor(ag, dir, s.Review)
		if m.Kind == review.KindNone {
			continue
		}
		pe.Agents = append(pe.Agents, panelAgent{Name: ag, Label: config.AgentLabel(ag), Method: m.String()})
	}
	return pe
}

// PanelDo implements configui.Panel: the page's buttons.
func (a *App) PanelDo(action, url, agent string, on bool) error {
	a.mu.Lock()
	snap, runner := a.snap, a.runner
	a.mu.Unlock()
	find := func() (queue.Entry, error) {
		if snap != nil {
			for _, e := range snap.Entries {
				if e.URL == url {
					return e, nil
				}
			}
		}
		return queue.Entry{}, errors.New("this PR/MR is no longer in the queue")
	}
	defer a.requestRedraw()
	switch action {
	case "update":
		if a.updateInfo().Latest == "" {
			return errors.New("no newer release to install")
		}
		go a.installUpdate()
	case "check_update":
		go a.checkUpdate(true)
	case "analyze":
		e, err := find()
		if err != nil {
			return err
		}
		if app.Dir(e) == "" {
			return errors.New("no local clone")
		}
		if agent != config.AgentClaude && agent != config.AgentCodex {
			agent = ""
		}
		runner.Enqueue(e.Item, app.Dir(e), agent)
	case "cancel":
		runner.Cancel(url)
	case "skip", "unskip":
		e, err := find()
		if err != nil {
			return err
		}
		return a.store.Update(func(st *state.State) error {
			j := st.Job(e.URL)
			if j == nil {
				j = &state.Job{URL: e.URL, Repo: e.Repo, Number: e.Number, Title: e.Title}
				st.Put(j)
			}
			j.SeenSHA = e.HeadSHA
			if action == "skip" {
				j.Status = state.Skipped
			} else {
				j.Status = state.Pending
			}
			return nil
		})
	case "ack":
		var urls []string
		if snap != nil {
			for _, e := range snap.Entries {
				if e.Urgent && (url == "" || e.URL == url) {
					urls = append(urls, e.URL)
				}
			}
		}
		return a.store.Update(func(st *state.State) error { st.Ack(urls...); return nil })
	case "open_brief":
		st, err := a.store.Read()
		if err != nil {
			return err
		}
		if j := st.Job(url); j != nil && j.Brief != "" {
			return platform.Open(j.Brief)
		}
		return errors.New("no brief")
	case "map":
		e, err := find()
		if err != nil {
			return err
		}
		if e.PathGuess == "" {
			return errors.New("no clone found")
		}
		a.saveMapping(e.Repo, e.PathGuess)
	case "poll":
		a.requestManualPoll()
	case "pin", "unpin":
		key, err := projectKey(url)
		if err != nil {
			return err
		}
		a.setPinned(key, action == "pin")
	case "show_all":
		mode := config.ShowAllFold
		if on {
			mode = config.ShowAllInline
		}
		a.setShowAll(url, mode)
	case "review_open":
		return a.reviewOpen(url, agent)
	case "screen_effect":
		a.setScreenEffect(on)
	case "zap":
		go effects.Zap()
	case "autostart":
		if on {
			exe, err := InstallBinary()
			if err != nil {
				return err
			}
			return platform.AutostartInstall(exe)
		}
		return platform.AutostartUninstall()
	case "settings":
		a.openSettings()
	case "briefs":
		a.mu.Lock()
		dir := a.cfg.BriefsDir
		a.mu.Unlock()
		return platform.Open(config.Expand(dir))
	default:
		return errors.New("unknown action " + action)
	}
	return nil
}

// repoDir is the repo's clone: its path setting, else one found in scan_dirs.
func (a *App) repoDir(key string) string {
	a.mu.Lock()
	cfg, c := a.cfg, a.collector
	a.mu.Unlock()
	if s, err := cfg.Effective(key); err == nil && s.Path != "" && isDir(config.Expand(s.Path)) {
		return config.Expand(s.Path)
	}
	if c == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.Guess(ctx, key)
}

// reviewOpen starts a review of a PR/MR from a repo's full list, one that
// does not wait for me (nobody asked me, e.g. in a pinned repo).
func (a *App) reviewOpen(url, agent string) error {
	a.mu.Lock()
	cfg, c, runner := a.cfg, a.collector, a.runner
	a.mu.Unlock()
	if c == nil || runner == nil {
		return errors.New("not ready yet — try again after the first check")
	}
	_, repo, n, err := forge.ParseURL(url)
	if err != nil {
		return err
	}
	if k, ok := cfg.RepoKey(repo); ok {
		repo = k
	}
	s, err := cfg.Effective(repo)
	if err != nil {
		return err
	}
	dir := a.repoDir(repo)
	if dir == "" {
		return errors.New("no local clone of " + repo + " — clone it into a scanned folder or set its path in Settings")
	}
	host, project := forge.SplitKey(repo)
	f, ok := c.ForRepo(host, s.Account)
	if !ok {
		return errors.New("no account for " + host + " in the config")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	it, err := f.Get(ctx, project, n)
	if err != nil {
		return err
	}
	it.Repo = repo
	if agent != config.AgentClaude && agent != config.AgentCodex {
		agent = ""
	}
	runner.Enqueue(it, dir, agent)
	return nil
}

// projectKey turns what was typed or picked into a "host/group/proj" key:
// a key, a project URL or any URL inside the project (MR list, an MR).
func projectKey(in string) (string, error) {
	in = strings.TrimSpace(in)
	if _, repo, _, err := forge.ParseURL(in); err == nil {
		return repo, nil
	}
	if i := strings.Index(in, "/-/"); i > 0 {
		in = in[:i] // gitlab: …/proj/-/merge_requests
	}
	if !strings.Contains(in, "://") && !strings.Contains(in, "@") {
		in = "https://" + in
	}
	key, ok := forge.RemoteKey(in)
	key = strings.TrimSuffix(key, "/pulls") // github: …/repo/pulls
	if !ok || strings.Count(key, "/") < 2 {
		return "", errors.New("not a project: give host/group/project or its URL")
	}
	return key, nil
}

// PanelProjects implements configui.Panel: projects to pin, those already
// in the config and the scanned clones first, then the forges' member
// projects, matching q.
func (a *App) PanelProjects(q string) (any, error) {
	a.mu.Lock()
	cfg, c := a.cfg, a.collector
	a.mu.Unlock()
	if cfg == nil || c == nil {
		return nil, errors.New("not ready yet")
	}
	q = strings.TrimSpace(q)
	lq := strings.ToLower(q)
	var out []panelProject
	seen := map[string]bool{}
	add := func(key, from, clone string) {
		lk := strings.ToLower(key)
		if seen[lk] || (lq != "" && from != "typed" && !strings.Contains(lk, lq)) {
			return
		}
		seen[lk] = true
		p := panelProject{Key: key, From: from, Clone: config.Contract(clone)}
		if s, err := cfg.Effective(key); err == nil {
			p.Pinned = s.Pinned
		}
		out = append(out, p)
	}
	if strings.Contains(q, "/") {
		if k, err := projectKey(q); err == nil {
			add(k, "typed", "")
		}
	}
	for _, k := range cfg.RepoKeys() {
		add(k, "config", "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, cl := range scan.OnHosts(c.Clones(ctx), cfg.HostNames()) {
		add(cl.Key, "clone", cl.Dir)
	}
	var errs []string
	for _, k := range c.Order {
		f := c.Forges[k]
		names, err := f.Projects(ctx, q)
		if err != nil {
			errs = append(errs, f.Host()+": "+err.Error())
			continue
		}
		for _, n := range names {
			add(strings.ToLower(f.Host())+"/"+n, "forge", "")
		}
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return map[string]any{"projects": out, "errors": errs}, nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// history lists reviewed PR/MRs, newest first: those still open with an
// unchanged head, and those that left the queue.
func history(st *state.State, snap *queue.Snapshot) []panelHistory {
	in := map[string]queue.Entry{}
	for _, e := range snap.Entries {
		in[e.URL] = e
	}
	type row struct {
		h panelHistory
		t time.Time
	}
	var rows []row
	for url, j := range st.Jobs {
		if j.Status != state.Done || j.Brief == "" {
			continue
		}
		e, open := in[url]
		if open && !done(j, e.HeadSHA) {
			continue // new commits or undecided comments: on the main list
		}
		ref := fmt.Sprintf("!%d", j.Number)
		if strings.Contains(url, "/pull/") {
			ref = fmt.Sprintf("#%d", j.Number)
		}
		h := panelHistory{URL: url, Repo: app.ShortRepo(j.Repo), Ref: ref, Title: j.Title, Method: j.Method, Open: open, Brief: true,
			When: j.FinishedAt.Format("2006-01-02 15:04")}
		if j.Log != "" {
			h.LogID = strings.TrimSuffix(lastPath(j.Log), ".log")
			if p, err := review.LoadProposal(review.ProposalPath(j.Log)); err == nil {
				pend, acc, posted := p.Counts()
				h.Proposal = &panelProp{Pending: pend, Accepted: acc, Posted: posted, Total: len(p.All())}
			}
		}
		rows = append(rows, row{h, j.FinishedAt})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].t.After(rows[j].t) })
	out := make([]panelHistory, 0, len(rows))
	for i, r := range rows {
		if i == 100 {
			break
		}
		out = append(out, r.h)
	}
	return out
}

func lastPath(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// plural picks the form: 1 error, 2 errors.
func plural(n int, one, many string) string {
	form := many
	if n == 1 {
		form = one
	}
	return strconv.Itoa(n) + " " + form
}
