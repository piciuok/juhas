// Package app decides what happens when new PR/MRs show up (ask, auto,
// silent) and owns the runner; the tray and the CLI share it.
package app

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"juhas/internal/config"
	"juhas/internal/platform"
	"juhas/internal/queue"
	"juhas/internal/review"
	"juhas/internal/state"
)

type Core struct {
	Cfg    *config.Config
	Store  *state.Store
	Runner *review.Runner
	Ask    func(platform.Question) platform.Choice
	Notify func(platform.Notification)
	Logf   func(format string, args ...any)

	wg sync.WaitGroup
}

// Dir is the clone a review of e runs in.
func Dir(e queue.Entry) string {
	if e.Path != "" {
		return e.Path
	}
	return e.PathGuess
}

// Why a PR/MR is asked about.
const (
	ReasonNew     = "new"     // never seen
	ReasonChanged = "changed" // new commits since it was skipped or reviewed
	ReasonAgain   = "again"   // a manual check re-asks a skipped / postponed item
)

// reason returns why e should be asked about now ("" = it should not).
// manual is set for a check the user started ("Check now").
func reason(j *state.Job, e queue.Entry, manual bool) string {
	head := e.HeadSHA
	switch {
	case j == nil:
		return ReasonNew
	case j.Status == state.Skipped && head != "" && j.SeenSHA != head:
		return ReasonChanged
	case j.Status == state.Done && j.Stale(head) && j.AskedSHA != head:
		return ReasonChanged // reviewed, then the author pushed
	case j.Status == state.Pending && head != "" && j.AskedSHA != "" && j.AskedSHA != head:
		return ReasonChanged // answered "Later" (or the dialog timed out), then the author pushed
	case manual && (j.Status == state.Skipped || j.Status == state.Pending) && j.AskedSHA != "" && j.AskedSHA == head:
		return ReasonAgain // answered "Skip"/"Later" before; same head
	}
	return ""
}

// maxBaselineTries is how many failing first polls stay silent.
const maxBaselineTries = 5

// Plan is what HandleNew decided for one poll.
type Plan struct {
	Baseline bool              // first poll: everything recorded, nothing asked
	Ask      []queue.Entry     // ask the user
	Reasons  map[string]string // URL -> Reason* for Ask
	Auto     []queue.Entry     // start now
	Silent   []queue.Entry     // just record
	NoClone  []queue.Entry     // cannot be reviewed, notify once
}

// Decide records the new entries in st and returns what to do with them.
func Decide(st *state.State, snap *queue.Snapshot, autoMax int, manual bool) Plan {
	p := Plan{Reasons: map[string]string{}}
	put := func(e queue.Entry, status string) *state.Job {
		j := st.Job(e.URL)
		if j == nil {
			j = &state.Job{URL: e.URL}
			st.Put(j)
		}
		j.Repo, j.Number, j.Title, j.SeenSHA = e.Repo, e.Number, e.Title, e.HeadSHA
		if status != "" {
			j.Status = status
		}
		return j
	}
	if !st.Initialized {
		for _, e := range snap.Entries {
			if st.Job(e.URL) == nil {
				put(e, state.Pending)
			}
		}
		// A poll with errors (offline at login) may have missed whole hosts:
		// keep recording the baseline, or the next poll asks about everything.
		// A persistent error must not keep it silent forever, though.
		if len(snap.Errors) == 0 || st.BaselineTries >= maxBaselineTries {
			st.Initialized = true
		} else {
			st.BaselineTries++
		}
		p.Baseline = true
		return p
	}
	for _, e := range snap.Entries {
		j := st.Job(e.URL)
		why := reason(j, e, manual)
		if why == "" {
			continue
		}
		reviewed := j != nil && j.Status == state.Done
		switch {
		case e.OnNew == config.OnNewSilent:
			put(e, keepDone(reviewed, state.Pending))
			p.Silent = append(p.Silent, e)
		case Dir(e) == "":
			put(e, keepDone(reviewed, state.Pending))
			if why == ReasonNew {
				p.NoClone = append(p.NoClone, e)
			}
		case e.OnNew == config.OnNewAuto && len(p.Auto) < autoMax && why != ReasonAgain:
			put(e, state.Queued).AskedSHA = e.HeadSHA
			p.Auto = append(p.Auto, e)
		case e.OnNew == config.OnNewAuto && why != ReasonAgain:
			put(e, keepDone(reviewed, state.Pending)) // over the per-poll limit: leave in the menu
			p.Silent = append(p.Silent, e)
		default:
			// a reviewed item keeps its brief and status while the question is open
			put(e, keepDone(reviewed, state.Asked)).AskedSHA = e.HeadSHA
			p.Ask = append(p.Ask, e)
			p.Reasons[e.URL] = why
		}
	}
	return p
}

// keepDone leaves a reviewed item's status alone ("" = unchanged).
func keepDone(reviewed bool, status string) string {
	if reviewed {
		return ""
	}
	return status
}

// HandleNew applies Decide and acts on it. Questions run in the background,
// so a poll never waits for the user.
func (c *Core) HandleNew(snap *queue.Snapshot, manual bool) Plan {
	var p Plan
	if err := c.Store.Update(func(st *state.State) error {
		p = Decide(st, snap, c.Cfg.AutoMaxPerPoll, manual)
		return nil
	}); err != nil {
		c.logf("state: %v", err)
		return p
	}
	if p.Baseline {
		c.logf("first run: recorded %d PR/MRs without asking", len(snap.Entries))
		return p
	}
	for _, e := range p.NoClone {
		c.notify(platform.Notification{Title: "Review: no local clone", Message: e.Repo + " " + e.Ref() + " — set path in config"})
	}
	for _, e := range p.Auto {
		c.logf("auto review: %s", e.URL)
		c.Runner.Enqueue(e.Item, Dir(e), "")
	}
	if len(p.Ask) > 0 {
		c.wg.Add(1)
		go func(entries []queue.Entry, why map[string]string) {
			defer c.wg.Done()
			c.ask(entries, why)
		}(p.Ask, p.Reasons)
	}
	return p
}

func (c *Core) ask(entries []queue.Entry, why map[string]string) {
	q := Question(c.Cfg, entries, why)
	choice := platform.Later
	if c.Ask != nil {
		choice = c.Ask(q)
	}
	c.logf("question about %d PR/MRs: %s", len(entries), choice)
	if agent, ok := choice.Agent(); ok {
		for _, e := range entries {
			c.Runner.Enqueue(e.Item, Dir(e), agent)
		}
		return
	}
	{
		status := state.Pending
		if choice == platform.Skip {
			status = state.Skipped
		}
		_ = c.Store.Update(func(st *state.State) error {
			for _, e := range entries {
				j := st.Job(e.URL)
				switch {
				case j == nil:
				case j.Status == state.Asked:
					j.Status = status
				case j.Status == state.Done && choice == platform.Skip:
					// reviewed item: "Skip" just silences this head (AskedSHA)
				}
			}
			return nil
		})
		if c.Runner.OnChange != nil {
			c.Runner.OnChange()
		}
	}
}

// Question builds the "run AI review?" prompt for one or more entries.
func Question(cfg *config.Config, entries []queue.Entry, why map[string]string) platform.Question {
	q := platform.Question{Style: cfg.AskStyle}
	// default agent: the first entry's repo setting; the other one is offered too
	def := config.AgentClaude
	if len(entries) > 0 {
		if s, err := cfg.Effective(entries[0].Repo); err == nil {
			def = s.Agent
		}
	}
	q.Agents = []platform.Agent{{Name: def, Label: config.AgentLabel(def)}}
	for _, a := range config.Agents {
		if a != def {
			q.Agents = append(q.Agents, platform.Agent{Name: a, Label: config.AgentLabel(a)})
		}
	}
	line := func(e queue.Entry) string {
		s, _ := cfg.Effective(e.Repo)
		m := review.ResolveFor(s.Agent, Dir(e), s.Review)
		u := ""
		if e.Urgent {
			u = "⚡ "
		}
		tag := ""
		switch why[e.URL] {
		case ReasonChanged:
			tag = "   ⟳ changes since your last decision / review\n"
		case ReasonAgain:
			tag = "   ↺ again (manual check)\n"
		}
		return fmt.Sprintf("%s%s %s — %s\n%s   author: %s · %s", u, shortRepo(e.Repo), e.Ref(), e.Title, tag, e.Author, m)
	}
	if len(entries) == 1 {
		e := entries[0]
		q.Title = fmt.Sprintf("AI Review: %s %s", shortRepo(e.Repo), e.Ref())
		q.Message = "Run AI review?\n\n" + line(e)
		if why[e.URL] == ReasonChanged {
			q.Title = fmt.Sprintf("Changes in %s %s", shortRepo(e.Repo), e.Ref())
			q.Message = "New commits arrived. Review again?\n\n" + line(e)
			q.RunL = "Re-review"
		}
		return q
	}
	q.Title = fmt.Sprintf("AI Review: %d new PR/MRs", len(entries))
	var lines []string
	for _, e := range entries {
		lines = append(lines, line(e))
	}
	sort.Strings(lines)
	q.Message = fmt.Sprintf("Run AI review for all %d?\n\n%s", len(entries), strings.Join(lines, "\n"))
	q.RunL = "Run all"
	q.SkipL = "Skip all"
	return q
}

// shortRepo drops the host: "team/app".
func shortRepo(key string) string {
	if _, p, ok := strings.Cut(key, "/"); ok {
		return p
	}
	return key
}

// ShortRepo is exported for the tray.
func ShortRepo(key string) string { return shortRepo(key) }

// Wait blocks until pending questions are answered and reviews finish.
func (c *Core) Wait() {
	c.wg.Wait()
	c.Runner.Wait()
}

func (c *Core) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

func (c *Core) notify(n platform.Notification) {
	if c.Notify != nil {
		c.Notify(n)
	}
}
