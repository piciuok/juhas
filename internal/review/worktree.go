package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/piciuok/juhas/internal/config"
	"github.com/piciuok/juhas/internal/forge"
)

// A worktree is a detached checkout of one PR/MR, kept from its review to
// the chat about it (and the next review). It goes when the PR/MR is merged
// or closed, after worktrees.keep_for without use, or by hand in the Panel.

// Worktree is one entry of the registry.
type Worktree struct {
	ID       string    `json:"id"` // the job id (run log name)
	URL      string    `json:"url"`
	Platform string    `json:"platform"`
	Account  string    `json:"account,omitempty"`
	Repo     string    `json:"repo"`
	Ref      string    `json:"ref"`
	Title    string    `json:"title"`
	Clone    string    `json:"clone"` // the main clone it was added to
	Path     string    `json:"path"`
	SHA      string    `json:"sha"`
	Bytes    int64     `json:"bytes"`
	Created  time.Time `json:"created"`
	Used     time.Time `json:"used"`
	Checked  time.Time `json:"checked,omitzero"` // last asked the forge whether it is still open
}

// WorktreeCheckEvery: how often a worktree whose PR/MR left the lists is
// looked up on the forge.
var WorktreeCheckEvery = time.Hour

func worktreeRoot() string     { return filepath.Join(cacheDir(), "worktrees") }
func worktreeRegistry() string { return filepath.Join(cacheDir(), "worktrees.json") }
func runDir(id string) string  { return filepath.Join(cacheDir(), "runs", id) }

// updateWorktrees is a locked read-modify-write of the registry; the tray
// and `juhas review` may both touch it.
func updateWorktrees(fn func(map[string]*Worktree) error) error {
	if err := os.MkdirAll(cacheDir(), 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(worktreeRegistry()+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	reg := map[string]*Worktree{}
	if b, err := os.ReadFile(worktreeRegistry()); err == nil {
		_ = json.Unmarshal(b, &reg)
	}
	if err := fn(reg); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(reg, "", "  ")
	tmp := worktreeRegistry() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, worktreeRegistry())
}

// Worktrees lists the kept worktrees, most recently used first.
func Worktrees() []Worktree {
	var out []Worktree
	_ = updateWorktrees(func(reg map[string]*Worktree) error {
		for id, w := range reg {
			if !isDirPath(w.Path) {
				delete(reg, id) // removed behind our back
				continue
			}
			out = append(out, *w)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Used.After(out[j].Used) })
	return out
}

// WorktreeOf returns the kept worktree of a job, if any.
func WorktreeOf(id string) (Worktree, bool) {
	for _, w := range Worktrees() {
		if w.ID == id {
			return w, true
		}
	}
	return Worktree{}, false
}

// TouchWorktree marks a worktree as used now.
func TouchWorktree(id string) {
	_ = updateWorktrees(func(reg map[string]*Worktree) error {
		if w := reg[id]; w != nil {
			w.Used = time.Now()
		}
		return nil
	})
}

// fetchHead fetches the PR/MR head (and its base branch) into the clone and
// returns the commit to check out.
func fetchHead(ctx context.Context, dir string, it forge.Item) (string, error) {
	refs := []string{forge.HeadRef(it)}
	if it.TargetBranch != "" {
		refs = append(refs, it.TargetBranch+":refs/remotes/origin/"+it.TargetBranch)
	}
	if out, err := gitIn(ctx, dir, append([]string{"fetch", "--no-tags", "origin"}, refs...)...); err != nil {
		return "", fmt.Errorf("git fetch: %s", oneLine(out, 200))
	}
	if it.HeadSHA != "" {
		return it.HeadSHA, nil
	}
	return gitIn(ctx, dir, "rev-parse", "FETCH_HEAD")
}

// EnsureWorktree puts the job's worktree at the PR/MR head (adding it, or
// moving the kept one to the new head) and returns its path.
func EnsureWorktree(ctx context.Context, it forge.Item, dir, id string, log io.Writer) (string, error) {
	fmt.Fprintf(log, "%s ● worktree at head %s (%s)\n", stamp(), short(it.HeadSHA), forge.HeadRef(it))
	sha, err := fetchHead(ctx, dir, it)
	if err != nil {
		return "", err
	}
	wt := filepath.Join(worktreeRoot(), id)
	prev, kept := WorktreeOf(id)
	moved := false
	if kept && prev.Clone == dir && isDirPath(wt) {
		if out, err := gitIn(ctx, wt, "checkout", "--detach", "--force", sha); err == nil {
			moved = true
		} else {
			fmt.Fprintf(log, "%s   ! moving the kept worktree: %s — adding it again\n", stamp(), oneLine(out, 200))
		}
	}
	if !moved {
		_, _ = gitIn(ctx, dir, "worktree", "remove", "--force", wt) // leftover from an interrupted run
		_ = os.RemoveAll(wt)
		_, _ = gitIn(ctx, dir, "worktree", "prune")
		if err := os.MkdirAll(worktreeRoot(), 0o700); err != nil {
			return "", err
		}
		if out, err := gitIn(ctx, dir, "worktree", "add", "--detach", wt, sha); err != nil {
			return "", fmt.Errorf("git worktree: %s", oneLine(out, 200))
		}
	}
	now := time.Now()
	size := dirSize(wt)
	err = updateWorktrees(func(reg map[string]*Worktree) error {
		w := reg[id]
		if w == nil || !moved {
			w = &Worktree{ID: id, Created: now}
			reg[id] = w
		}
		w.URL, w.Platform, w.Account, w.Repo, w.Ref, w.Title = it.URL, it.Platform, it.Account, it.Repo, it.Ref(), it.Title
		w.Clone, w.Path, w.SHA, w.Bytes, w.Used = dir, wt, sha, size, now
		return nil
	})
	fmt.Fprintf(log, "%s   ↳ %s (kept for the chat until the PR/MR is merged or closed)\n", stamp(), config.Contract(wt))
	return wt, err
}

// RemoveWorktree deletes a kept worktree and the fetched context of its runs.
func RemoveWorktree(ctx context.Context, id string) error {
	w, ok := WorktreeOf(id)
	_ = os.RemoveAll(runDir(id))
	if !ok {
		return updateWorktrees(func(reg map[string]*Worktree) error { delete(reg, id); return nil })
	}
	if w.Path != filepath.Join(worktreeRoot(), id) {
		return errors.New("not a Juhas worktree: " + w.Path) // never delete anything else
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if isDirPath(w.Clone) {
		_, _ = gitIn(ctx, w.Clone, "worktree", "remove", "--force", w.Path)
	}
	if err := os.RemoveAll(w.Path); err != nil {
		return err
	}
	if isDirPath(w.Clone) {
		_, _ = gitIn(ctx, w.Clone, "worktree", "prune")
	}
	return updateWorktrees(func(reg map[string]*Worktree) error { delete(reg, id); return nil })
}

// SweepWorktrees removes the worktrees that are done with: unused for keep,
// or whose PR/MR is merged or closed. open holds the URLs the last poll saw
// open (those are not looked up); busy ones (a review or a chat in them)
// stay. lookup asks the forge about one PR/MR. It returns what it removed.
func SweepWorktrees(ctx context.Context, keep time.Duration, open map[string]bool, busy func(Worktree) bool,
	lookup func(context.Context, Worktree) (forge.Item, error)) []Swept {
	var gone []Swept
	now := time.Now()
	for _, w := range Worktrees() {
		if busy != nil && busy(w) {
			continue
		}
		why := ""
		switch {
		case keep > 0 && now.Sub(w.Used) > keep:
			why = "unused for " + config.FormatDuration(keep)
		case open[w.URL] || now.Sub(w.Checked) < WorktreeCheckEvery || lookup == nil:
			continue
		default:
			it, err := lookup(ctx, w)
			_ = updateWorktrees(func(reg map[string]*Worktree) error {
				if r := reg[w.ID]; r != nil {
					r.Checked = now
				}
				return nil
			})
			if err != nil || !it.Gone() {
				continue
			}
			why = it.State
		}
		if RemoveWorktree(ctx, w.ID) == nil {
			gone = append(gone, Swept{w, why})
		}
	}
	return gone
}

// Swept is a worktree SweepWorktrees removed, and why.
type Swept struct {
	Worktree
	Reason string // merged | closed | unused for 168h
}

func dirSize(p string) int64 {
	var n int64
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

func isDirPath(p string) bool {
	st, err := os.Stat(p)
	return p != "" && err == nil && st.IsDir()
}
