package tray

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/piciuok/juhas/internal/app"
	"github.com/piciuok/juhas/internal/config"
	"github.com/piciuok/juhas/internal/doctor"
	"github.com/piciuok/juhas/internal/effects"
	"github.com/piciuok/juhas/internal/platform"
	"github.com/piciuok/juhas/internal/queue"
	"github.com/piciuok/juhas/internal/review"
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
	Short   string       `json:"short"`
	Mode    string       `json:"mode"`
	Open    int          `json:"open"`
	Queued  int          `json:"queued"`
	Urgent  int          `json:"urgent"`
	ListURL string       `json:"list_url"`
	Entries []panelEntry `json:"entries"`
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
		if len(entries) == 0 {
			continue
		}
		pr := panelRepo{Key: r.Key, Short: app.ShortRepo(r.Key), Mode: r.Mode, Open: r.Open, Queued: r.Queued, Urgent: r.Urgent, ListURL: listURL(r.Key, entries)}
		for _, e := range entries {
			pr.Entries = append(pr.Entries, a.panelEntry(e, st, cfg, runner))
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
