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

	"juhas/internal/config"
	"juhas/internal/forge"
	"juhas/internal/scan"
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
}

type Snapshot struct {
	At      time.Time  `json:"at"`
	Entries []Entry    `json:"entries"`
	Repos   []RepoStat `json:"repos"`
	Errors  []string   `json:"errors"`
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
	Cfg    *config.Config
	Forges map[string]forge.Forge // by lower-case host
	Now    func() time.Time

	mu        sync.Mutex
	clones    []scan.Clone
	scannedAt time.Time
}

// ScanEvery is how often scan_dirs are re-walked for auto-mapping.
var ScanEvery = 10 * time.Minute

func NewCollector(cfg *config.Config) *Collector {
	c := &Collector{Cfg: cfg, Forges: map[string]forge.Forge{}, Now: time.Now}
	for _, h := range cfg.GitHub {
		c.Forges[strings.ToLower(h.Host)] = forge.NewGitHub(h.Host)
	}
	for _, h := range cfg.GitLab {
		c.Forges[strings.ToLower(h.Host)] = forge.NewGitLab(h.Host)
	}
	return c
}

// Collect runs one poll. Errors on one host or repo are recorded in the
// snapshot and do not stop the others.
func (c *Collector) Collect(ctx context.Context) *Snapshot {
	snap := &Snapshot{At: c.Now()}
	byURL := map[string]*forge.Item{}
	var order []string
	add := func(it forge.Item) {
		if p, ok := byURL[it.URL]; ok {
			p.Requested = p.Requested || it.Requested
			p.Assigned = p.Assigned || it.Assigned
			p.ApprovedByMe = p.ApprovedByMe || it.ApprovedByMe
			return
		}
		cp := it
		byURL[it.URL] = &cp
		order = append(order, it.URL)
	}

	hosts := make([]string, 0, len(c.Forges))
	for h := range c.Forges {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, h := range hosts {
		items, err := c.Forges[h].Requested(ctx)
		if err != nil {
			snap.Errors = append(snap.Errors, err.Error())
			continue
		}
		for _, it := range items {
			add(it)
		}
	}

	openCount := map[string]int{}
	for _, key := range c.Cfg.RepoKeys() {
		s, err := c.Cfg.Effective(key)
		if err != nil || s.Mode != config.ModeOpen || s.OnNew == config.OnNewOff {
			continue
		}
		host, project := forge.SplitKey(key)
		f, ok := c.Forges[strings.ToLower(host)]
		if !ok {
			snap.Errors = append(snap.Errors, fmt.Sprintf("%s: host %s is not in github/gitlab in the config", key, host))
			continue
		}
		items, err := f.Open(ctx, project)
		if err != nil {
			snap.Errors = append(snap.Errors, err.Error())
			continue
		}
		openCount[strings.ToLower(key)] = len(items)
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
		st := &RepoStat{Key: key, Mode: s.Mode, Open: -1}
		if n, ok := openCount[lk]; ok {
			st.Open = n
		}
		stats[lk] = st
		return st
	}
	for _, key := range c.Cfg.RepoKeys() {
		if s, err := c.Cfg.Effective(key); err == nil && s.Mode == config.ModeOpen && s.OnNew != config.OnNewOff {
			stat(key, s)
		}
	}

	now := c.Now()
	for _, u := range order {
		it := *byURL[u]
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
		if st.Open < 0 && s.CountOpen && st.Queued > 0 {
			host, project := forge.SplitKey(st.Key)
			if f, ok := c.Forges[strings.ToLower(host)]; ok {
				if n, err := f.CountOpen(ctx, project); err == nil {
					st.Open = n
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
	if f := scan.Find(c.clones, key); len(f) > 0 {
		return f[0].Dir
	}
	return ""
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
