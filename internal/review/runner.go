package review

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"juhas/internal/config"
	"juhas/internal/forge"
	"juhas/internal/state"
)

// Runner executes reviews in the background, at most Concurrency at a time,
// and records progress in the state store.
type Runner struct {
	Cfg   *config.Config
	Store *state.Store
	// OnChange is called after every status change (refresh the tray).
	OnChange func()
	// OnDone is called when a review finishes (notify).
	OnDone func(j state.Job)
	// Echo, if set, receives the run log as it is written (CLI).
	Echo io.Writer

	once    sync.Once
	sem     chan struct{}
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	running map[string]forge.Item // in progress
	waiting []forge.Item          // queued, in order
	held    bool                  // no new reviews (an update is about to restart the app)
	wg      sync.WaitGroup
}

// Hold stops (or resumes) accepting new reviews; running ones go on.
func (r *Runner) Hold(on bool) {
	r.init()
	r.mu.Lock()
	r.held = on
	r.mu.Unlock()
}

// ErrElsewhere: another juhas process is already reviewing it.
var ErrElsewhere = fmt.Errorf("already being reviewed by another juhas process")

func (r *Runner) init() {
	r.once.Do(func() {
		n := r.Cfg.Concurrency
		if n < 1 {
			n = 1
		}
		r.sem = make(chan struct{}, n)
		r.cancels = map[string]context.CancelFunc{}
		r.running = map[string]forge.Item{}
	})
}

// LogPath is where claude's stderr for url goes.
func LogPath(store *state.Store, url string) string {
	h := sha1.Sum([]byte(url))
	return filepath.Join(store.LogDir(), hex.EncodeToString(h[:6])+".log")
}

func (r *Runner) dequeue(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, w := range r.waiting {
		if w.URL == url {
			r.waiting = append(r.waiting[:i], r.waiting[i+1:]...)
			return
		}
	}
}

// writeQueued starts the run log with where the review stands in the
// queue, so the live view is never blank; Run truncates it on start.
func (r *Runner) writeQueued(it forge.Item, logPath string) {
	r.mu.Lock()
	pos := len(r.waiting)
	var ahead []string
	for _, x := range r.running {
		ahead = append(ahead, x.Repo+" "+x.Ref()+" (running)")
	}
	for _, x := range r.waiting {
		if x.URL != it.URL {
			ahead = append(ahead, x.Repo+" "+x.Ref()+" (queued)")
		}
	}
	slots := cap(r.sem)
	r.mu.Unlock()
	if len(ahead) == 0 {
		return // starts right away
	}
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	msg := fmt.Sprintf("# %s %s — %s\n\n%s ⏳ queued (position %d, parallel: %d — change it in Settings → Parallel reviews)\n", it.Repo, it.Ref(), it.Title, time.Now().Format("15:04:05"), pos, slots)
	for _, a := range ahead {
		msg += "           waiting for: " + a + "\n"
	}
	_ = os.WriteFile(logPath, []byte(msg), 0o644)
}

// Queue reports what is running and what waits, in order.
func (r *Runner) Queue() (running, waiting []forge.Item) {
	r.init()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.running {
		running = append(running, x)
	}
	return running, append([]forge.Item{}, r.waiting...)
}

// Busy reports whether url is queued or running in this process.
func (r *Runner) Busy(url string) bool {
	r.init()
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.cancels[url]
	return ok
}

// Enqueue schedules a review of it in dir with agent ("" = the repo's
// default). It is a no-op if one is in flight here, while held, or when
// another juhas process runs it (both would write the same log and brief).
func (r *Runner) Enqueue(it forge.Item, dir, agent string) {
	r.init()
	if st, err := r.Store.Read(); err == nil && st.Job(it.URL).LiveOwner() {
		return
	}
	r.mu.Lock()
	if _, busy := r.cancels[it.URL]; busy || r.held {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancels[it.URL] = cancel
	r.waiting = append(r.waiting, it)
	r.mu.Unlock()

	logPath := LogPath(r.Store, it.URL)
	r.writeQueued(it, logPath)
	r.update(it, func(j *state.Job) {
		j.Status = state.Queued
		j.Error = ""
		j.Log = logPath
		j.Owner = os.Getpid()
	})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			delete(r.cancels, it.URL)
			r.mu.Unlock()
			cancel()
		}()
		select {
		case r.sem <- struct{}{}:
		case <-ctx.Done():
			r.dequeue(it.URL)
			r.update(it, func(j *state.Job) { j.Status = state.Failed; j.Error = "cancelled while queued" })
			return
		}
		r.dequeue(it.URL)
		r.mu.Lock()
		r.running[it.URL] = it
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			delete(r.running, it.URL)
			r.mu.Unlock()
			<-r.sem
		}()
		r.run(ctx, it, dir, agent)
	}()
}

func (r *Runner) run(ctx context.Context, it forge.Item, dir, agent string) {
	s, err := r.Cfg.Effective(it.Repo)
	if err != nil {
		r.update(it, func(j *state.Job) { j.Status = state.Failed; j.Error = err.Error() })
		return
	}
	logPath := LogPath(r.Store, it.URL)
	r.update(it, func(j *state.Job) {
		j.Status = state.Running
		j.StartedAt = time.Now()
		j.Log = logPath
		if agent == "" {
			agent = s.Agent
		}
		j.Method = (Result{Agent: agent, Method: ResolveFor(agent, dir, s.Review)}).Label()
	})
	res, err := Run(ctx, r.Cfg, s, it, dir, logPath, func(pid int) {
		r.update(it, func(j *state.Job) { j.PID = pid })
	}, r.Echo, agent)
	var final state.Job
	r.update(it, func(j *state.Job) {
		j.PID, j.Owner = 0, 0
		j.FinishedAt = time.Now()
		j.Method = res.Label()
		if err != nil {
			j.Status = state.Failed
			j.Error = err.Error()
		} else {
			j.Status = state.Done
			j.Error = ""
			j.Brief = res.Brief
			j.BriefSHA = res.SHA
		}
		final = *j
	})
	if r.OnDone != nil {
		r.OnDone(final)
	}
}

func (r *Runner) update(it forge.Item, fn func(*state.Job)) {
	_ = r.Store.Update(func(st *state.State) error {
		j := st.Job(it.URL)
		if j == nil {
			j = &state.Job{URL: it.URL}
			st.Put(j)
		}
		j.Repo, j.Number, j.Title = it.Repo, it.Number, it.Title
		if it.HeadSHA != "" {
			j.SeenSHA = it.HeadSHA
		}
		fn(j)
		return nil
	})
	if r.OnChange != nil {
		r.OnChange()
	}
}

// Cancel stops a queued or running review.
func (r *Runner) Cancel(url string) {
	r.init()
	r.mu.Lock()
	c := r.cancels[url]
	r.mu.Unlock()
	if c != nil {
		c()
	}
}

// Stop cancels everything and waits for the processes to exit.
func (r *Runner) Stop() {
	r.init()
	r.mu.Lock()
	for _, c := range r.cancels {
		c()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

// Wait blocks until all queued reviews finish.
func (r *Runner) Wait() { r.wg.Wait() }
