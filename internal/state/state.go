// Package state persists per-PR/MR job status in
// ~/.local/state/juhas/state.json. Every change is a locked
// read-modify-write, so the tray app and CLI commands can share the file.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"juhas/internal/config"
)

const (
	Pending = "pending" // seen, nothing decided
	Asked   = "asked"   // question shown, no answer yet
	Skipped = "skipped" // "Skip": do not ask again for this SHA
	Queued  = "queued"
	Running = "running"
	Done    = "done"
	Failed  = "failed"
)

type Job struct {
	URL        string    `json:"url"`
	Repo       string    `json:"repo"`
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	SeenSHA    string    `json:"seen_sha,omitempty"`  // head when last asked/seen
	AskedSHA   string    `json:"asked_sha,omitempty"` // head the user was last asked about
	BriefSHA   string    `json:"brief_sha,omitempty"` // head the brief was made for
	Brief      string    `json:"brief,omitempty"`
	Log        string    `json:"log,omitempty"` // stderr of the last run
	Error      string    `json:"error,omitempty"`
	Method     string    `json:"method,omitempty"` // skill:<name> | builtin | claude-code-review
	PID        int       `json:"pid,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// Reviewed: a brief exists for the current head — the item belongs to history.
func (j *Job) Reviewed(headSHA string) bool {
	return j != nil && j.Status == Done && j.Brief != "" && !j.Stale(headSHA)
}

// Stale reports whether new commits landed after the brief was made.
func (j *Job) Stale(headSHA string) bool {
	return j.Status == Done && j.BriefSHA != "" && headSHA != "" && j.BriefSHA != headSHA
}

type State struct {
	// Initialized is set after the first poll, which records the baseline
	// without asking about every already-open PR/MR.
	Initialized bool            `json:"initialized"`
	UrgentAcked []string        `json:"urgent_acked,omitempty"`  // URLs whose flashing was acknowledged
	UrgentZaps  []string        `json:"urgent_zapped,omitempty"` // URLs that already got the screen "kick"
	Jobs        map[string]*Job `json:"jobs"`
}

func Dir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "juhas")
	}
	return filepath.Join(config.Home(), ".local", "state", "juhas")
}

type Store struct {
	path string
}

func Open() *Store                       { return &Store{path: filepath.Join(Dir(), "state.json")} }
func OpenAt(path string) *Store          { return &Store{path: path} }
func (s *Store) Path() string            { return s.path }
func (s *Store) LogDir() string          { return filepath.Join(filepath.Dir(s.path), "jobs") }
func (s *Store) PollLog() string         { return filepath.Join(filepath.Dir(s.path), "poll.log") }
func (s *Store) UpdatePath() string      { return filepath.Join(filepath.Dir(s.path), "update.json") }
func (s *Store) SnapshotPath() string    { return filepath.Join(filepath.Dir(s.path), "snapshot.json") }
func (s *Store) lockPath() string        { return s.path + ".lock" }
func (s *Store) ensureDir() error        { return os.MkdirAll(filepath.Dir(s.path), 0o755) }
func (s *Store) Read() (*State, error)   { return s.read() }
func newState() *State                   { return &State{Jobs: map[string]*Job{}} }
func (st *State) Job(url string) *Job    { return st.Jobs[url] }
func (st *State) Has(url string) bool    { _, ok := st.Jobs[url]; return ok }
func (st *State) Put(j *Job)             { st.Jobs[j.URL] = j }
func (st *State) Delete(url string)      { delete(st.Jobs, url) }
func (st *State) Acked(url string) bool  { return contains(st.UrgentAcked, url) }
func (st *State) Ack(urls ...string)     { st.UrgentAcked = appendNew(st.UrgentAcked, urls...) }
func (st *State) Zapped(url string) bool { return contains(st.UrgentZaps, url) }
func (st *State) Zap(urls ...string)     { st.UrgentZaps = appendNew(st.UrgentZaps, urls...) }

func (s *Store) read() (*State, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return newState(), nil
	}
	if err != nil {
		return nil, err
	}
	st := newState()
	if err := json.Unmarshal(b, st); err != nil {
		return nil, err
	}
	if st.Jobs == nil {
		st.Jobs = map[string]*Job{}
	}
	return st, nil
}

// Update applies fn under an exclusive file lock and saves the result.
func (s *Store) Update(fn func(*State) error) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	lf, err := os.OpenFile(s.lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	st, err := s.read()
	if err != nil {
		return err
	}
	if err := fn(st); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// RecoverInterrupted marks jobs left running/queued by a dead process as
// failed, and unanswered questions as pending.
func (s *Store) RecoverInterrupted() error {
	return s.Update(func(st *State) error {
		for _, j := range st.Jobs {
			if j.Status == Asked {
				j.Status = Pending // the dialog died with the previous process
				continue
			}
			if (j.Status == Running || j.Status == Queued) && !alive(j.PID) {
				j.Status = Failed
				j.Error = "interrupted (the app was closed or the computer went to sleep)"
				j.PID = 0
			}
		}
		return nil
	})
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func appendNew(xs []string, add ...string) []string {
	for _, a := range add {
		if !contains(xs, a) {
			xs = append(xs, a)
		}
	}
	return xs
}
