// Package queue builds the review queue: it asks every forge, merges and
// filters the results per repository settings, and flags the urgent ones.
package queue

import (
	"context"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/piciuok/juhas/internal/config"
	"github.com/piciuok/juhas/internal/forge"
	"github.com/piciuok/juhas/internal/scan"
)

// Entry is a queued PR/MR with what the app needs to act on it.
type Entry struct {
	forge.Item
	Path      string `json:"path"`       // configured clone ("" if unmapped)
	PathGuess string `json:"path_guess"` // clone found in scan_dirs but not in config
	Urgent    bool   `json:"urgent"`
	UrgentWhy string `json:"urgent_why"`
	OnNew     string `json:"on_new"`
}

// RepoStat is the per-repository counter line in the tray menu.
type RepoStat struct {
	Key    string `json:"key"`
	Mode   string `json:"mode"`
	Open   int    `json:"open"`   // all open PR/MRs (-1 = not counted)
	Queued int    `json:"queued"` // entries in the queue
	Urgent int    `json:"urgent"`
	Pinned bool   `json:"pinned,omitempty"` // shown even with nothing for me
	Inline bool   `json:"inline,omitempty"` // list All right in the menu (show_all: inline)
	// All open PR/MRs of the repo, newest first, for "Show all" (empty
	// when not counted).
	All []Open `json:"all,omitempty"`
}

// Open is one PR/MR in a repo's full list.
type Open struct {
	Number int    `json:"number"`
	Ref    string `json:"ref"`
	Title  string `json:"title"`
	Author string `json:"author"`
	URL    string `json:"url"`
	Draft  bool   `json:"draft,omitempty"`
}

func openList(items []forge.Item) []Open {
	out := make([]Open, 0, len(items))
	for _, it := range items {
		out = append(out, Open{Number: it.Number, Ref: it.Ref(), Title: it.Title, Author: it.Author, URL: it.URL, Draft: it.Draft})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	return out
}

type Snapshot struct {
	At      time.Time  `json:"at"`
	Entries []Entry    `json:"entries"`
	Repos   []RepoStat `json:"repos"`
	Errors  []string   `json:"errors"`
	// Mine are my own open PR/MRs across every account, with how ready they
	// are to merge (no review, just a look).
	Mine []forge.Item `json:"mine"`
}

// UrgentCount is the number of urgent entries.
func (s *Snapshot) UrgentCount() int {
	n := 0
	for _, e := range s.Entries {
		if e.Urgent {
			n++
		}
	}
	return n
}

// Collector holds the forges and a cached clone scan between polls.
type Collector struct {
	Cfg       *config.Config
	Forges    map[string]forge.Forge // by account key: "host" or "host@user"
	Order     []string               // Forges keys in config order ("" = sorted)
	Unwatched map[string]bool        // accounts whose review requests are not checked
	Now       func() time.Time

	mu        sync.Mutex
	clones    []scan.Clone
	scannedAt time.Time
}

// ScanEvery is how often scan_dirs are re-walked for auto-mapping.
var ScanEvery = 10 * time.Minute

func NewCollector(cfg *config.Config) *Collector {
	c := &Collector{Cfg: cfg, Forges: map[string]forge.Forge{}, Unwatched: map[string]bool{}, Now: time.Now}
	add := func(platform string, hosts []config.Host) {
		for _, h := range hosts {
			a := h.Account(platform)
			c.Forges[a.Key()] = forge.New(a)
			c.Order = append(c.Order, a.Key())
			if !h.Watched() {
				c.Unwatched[a.Key()] = true
			}
		}
	}
	add(forge.GitHub, cfg.GitHub)
	add(forge.GitLab, cfg.GitLab)
	forge.SetAccounts(cfg.Accounts())
	return c
}

func (c *Collector) order() []string {
	if len(c.Order) > 0 {
		return c.Order
	}
	keys := make([]string, 0, len(c.Forges))
	for k := range c.Forges {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ForRepo is the forge that reads a repository: its "account" setting when
// set, else the host's first watched account, else its first one.
func (c *Collector) ForRepo(host, user string) (forge.Forge, bool) {
	host = strings.ToLower(host)
	if user != "" {
		f, ok := c.Forges[host+"@"+user]
		return f, ok
	}
	var first forge.Forge
	for _, k := range c.order() {
		f := c.Forges[k]
		if strings.ToLower(f.Host()) != host {
			continue
		}
		if !c.Unwatched[k] {
			return f, true
		}
		if first == nil {
			first = f
		}
	}
	return first, first != nil
}

// Collect runs one poll. Errors on one host or repo are recorded in the
// snapshot and do not stop the others.
func (c *Collector) Collect(ctx context.Context) *Snapshot {
	snap := &Snapshot{At: c.Now()}
	byURL := map[string]*forge.Item{}
	var order []string
	add := func(it forge.Item) {
		if p, ok := byURL[it.URL]; ok {
			if !p.Requested && !p.Assigned && (it.Requested || it.Assigned) {
				p.Account = it.Account // act as the account that was asked
			}
			p.Requested = p.Requested || it.Requested
			p.Assigned = p.Assigned || it.Assigned
			p.ApprovedByMe = p.ApprovedByMe || it.ApprovedByMe
			return
		}
		cp := it
		byURL[it.URL] = &cp
		order = append(order, it.URL)
	}

	for _, k := range c.order() {
		if c.Unwatched[k] {
			continue
		}
		items, err := c.Forges[k].Requested(ctx)
		if err != nil {
			snap.Errors = append(snap.Errors, err.Error()) // items may still hold what was fetched
		}
		for _, it := range items {
			add(it)
		}
	}

	snap.Mine = c.mine(ctx, snap)

	openAll := map[string][]Open{}
	for _, key := range c.Cfg.RepoKeys() {
		s, err := c.Cfg.Effective(key)
		if err != nil || s.Mode != config.ModeOpen || s.OnNew == config.OnNewOff {
			continue
		}
		host, project := forge.SplitKey(key)
		f, ok := c.ForRepo(host, s.Account)
		if !ok {
			who := host
			if s.Account != "" {
				who = s.Account + "@" + host
			}
			snap.Errors = append(snap.Errors, fmt.Sprintf("%s: account %s is not in github/gitlab in the config", key, who))
			continue
		}
		items, err := f.Open(ctx, project)
		if err != nil {
			snap.Errors = append(snap.Errors, err.Error())
			if len(items) == 0 {
				continue
			}
		}
		openAll[strings.ToLower(key)] = openList(items)
		for _, it := range items {
			add(it)
		}
	}

	stats := map[string]*RepoStat{}
	stat := func(key string, s config.Settings) *RepoStat {
		lk := strings.ToLower(key)
		if st, ok := stats[lk]; ok {
			return st
		}
		st := &RepoStat{Key: key, Mode: s.Mode, Open: -1, Pinned: s.Pinned, Inline: s.ShowAll == config.ShowAllInline}
		if all, ok := openAll[lk]; ok {
			st.Open, st.All = len(all), all
		}
		stats[lk] = st
		return st
	}
	for _, key := range c.Cfg.RepoKeys() {
		if s, err := c.Cfg.Effective(key); err == nil && (s.Mode == config.ModeOpen || s.Pinned) && s.OnNew != config.OnNewOff {
			stat(key, s)
		}
	}

	mine := c.logins(ctx)
	now := c.Now()
	for _, u := range order {
		it := *byURL[u]
		if h, _ := forge.SplitKey(it.Repo); mine[strings.ToLower(h)+" "+strings.ToLower(it.Author)] {
			it.Mine = true // authored by another of my accounts
		}
		key := it.Repo
		if k, ok := c.Cfg.RepoKey(it.Repo); ok {
			key = k
			it.Repo = k
		}
		s, err := c.Cfg.Effective(key)
		if err != nil {
			snap.Errors = append(snap.Errors, fmt.Sprintf("%s: %v", key, err))
			continue
		}
		if !Keep(it, s) {
			continue
		}
		e := Entry{Item: it, OnNew: s.OnNew}
		e.Urgent, e.UrgentWhy = IsUrgent(it, s.Urgent, now)
		if s.Path != "" && isDir(s.Path) {
			e.Path = s.Path
		} else if g := c.Guess(ctx, key); g != "" {
			e.PathGuess = g
		}
		snap.Entries = append(snap.Entries, e)
		st := stat(key, s)
		st.Queued++
		if e.Urgent {
			st.Urgent++
		}
	}

	for _, st := range stats {
		s, _ := c.Cfg.Effective(st.Key)
		if st.Open < 0 && (s.CountOpen && st.Queued > 0 || s.Pinned) {
			host, project := forge.SplitKey(st.Key)
			if f, ok := c.ForRepo(host, s.Account); ok {
				if items, err := f.ListOpen(ctx, project); err == nil {
					st.All = openList(items)
					st.Open = len(st.All)
				} else {
					snap.Errors = append(snap.Errors, err.Error())
				}
			}
		}
		snap.Repos = append(snap.Repos, *st)
	}
	sort.Slice(snap.Repos, func(i, j int) bool { return snap.Repos[i].Key < snap.Repos[j].Key })
	sort.SliceStable(snap.Entries, func(i, j int) bool {
		a, b := snap.Entries[i], snap.Entries[j]
		if a.Urgent != b.Urgent {
			return a.Urgent
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		return a.Number > b.Number
	})
	return snap
}

// mine collects my PR/MRs from every account (also unwatched ones: they are
// still mine), newest activity first. A failing account is reported and
// skipped.
func (c *Collector) mine(ctx context.Context, snap *Snapshot) []forge.Item {
	seen := map[string]bool{}
	out := []forge.Item{}
	for _, k := range c.order() {
		items, err := c.Forges[k].Mine(ctx)
		if err != nil {
			snap.Errors = append(snap.Errors, "my PR/MRs: "+err.Error())
		}
		for _, it := range items {
			if seen[it.URL] {
				continue
			}
			seen[it.URL] = true
			if key, ok := c.Cfg.RepoKey(it.Repo); ok {
				it.Repo = key
			}
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return updated(out[i]).After(updated(out[j])) })
	return out
}

func updated(it forge.Item) time.Time {
	if it.Ready != nil && !it.Ready.Updated.IsZero() {
		return it.Ready.Updated
	}
	return it.CreatedAt
}

// logins are "host login" of every account, watched or not: a PR authored
// as any of them is mine (cached by the forges).
func (c *Collector) logins(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	for _, f := range c.Forges {
		if me, err := f.Me(ctx); err == nil && me != "" {
			out[strings.ToLower(f.Host())+" "+strings.ToLower(me)] = true
		}
	}
	return out
}

// Keep applies the repository's filters to one item.
func Keep(it forge.Item, s config.Settings) bool {
	switch {
	case s.OnNew == config.OnNewOff:
		return false
	case it.Mine, it.ApprovedByMe:
		return false
	case it.Draft && !s.IncludeDrafts:
		return false
	case s.Mode == config.ModeRequested && !it.Requested && !it.Assigned:
		return false
	case MatchAuthor(it.Author, s.IgnoreAuthors):
		return false
	}
	return true
}

// MatchAuthor matches exact names (case-insensitive) or "re:<regexp>".
func MatchAuthor(author string, patterns []string) bool {
	for _, p := range patterns {
		if rx, ok := strings.CutPrefix(p, "re:"); ok {
			if re, err := regexp.Compile(rx); err == nil && re.MatchString(author) {
				return true
			}
			continue
		}
		if strings.EqualFold(p, author) {
			return true
		}
	}
	return false
}

// IsUrgent: an item asked of me (reviewer/assignee) that matches any rule.
func IsUrgent(it forge.Item, u config.Urgent, now time.Time) (bool, string) {
	if !it.Requested && !it.Assigned {
		return false, ""
	}
	for _, want := range u.Labels {
		for _, l := range it.Labels {
			if strings.EqualFold(want, l) {
				return true, "label " + l
			}
		}
	}
	if d := u.OlderThan.D(); d > 0 && !it.CreatedAt.IsZero() && now.Sub(it.CreatedAt) > d {
		return true, "waiting longer than " + config.FormatDuration(d)
	}
	for _, pat := range u.TargetBranches {
		if ok, _ := path.Match(pat, it.TargetBranch); ok {
			return true, "targets branch " + it.TargetBranch
		}
	}
	return false, ""
}

// Guess returns a clone of key found in scan_dirs (cached scan), or "".
func (c *Collector) Guess(ctx context.Context, key string) string {
	if f := scan.Find(c.Clones(ctx), key); len(f) > 0 {
		return f[0].Dir
	}
	return ""
}

// Clones are the clones found in scan_dirs (scanned at most every ScanEvery).
func (c *Collector) Clones(ctx context.Context) []scan.Clone {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clones == nil || c.Now().Sub(c.scannedAt) > ScanEvery {
		var roots []string
		for _, d := range c.Cfg.ScanDirs {
			roots = append(roots, config.Expand(d))
		}
		c.clones = scan.Clones(ctx, roots)
		c.scannedAt = c.Now()
	}
	return c.clones
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// Split moves the entries for which done is true out of the snapshot
// (into history) and recounts the per-repo queued/urgent numbers.
func (s *Snapshot) Split(done func(Entry) bool) (active *Snapshot, history []Entry) {
	cp := *s
	cp.Entries = nil
	queued, urgent := map[string]int{}, map[string]int{}
	for _, e := range s.Entries {
		if done(e) {
			history = append(history, e)
			continue
		}
		cp.Entries = append(cp.Entries, e)
		k := strings.ToLower(e.Repo)
		queued[k]++
		if e.Urgent {
			urgent[k]++
		}
	}
	cp.Repos = make([]RepoStat, len(s.Repos))
	for i, r := range s.Repos {
		r.Queued, r.Urgent = queued[strings.ToLower(r.Key)], urgent[strings.ToLower(r.Key)]
		cp.Repos[i] = r
	}
	return &cp, history
}
