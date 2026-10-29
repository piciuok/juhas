package tray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"fyne.io/systray"
	"os"
	"path/filepath"
	"time"

	"github.com/piciuok/juhas/internal/platform"
	"github.com/piciuok/juhas/internal/review"
	"github.com/piciuok/juhas/internal/state"
	"github.com/piciuok/juhas/internal/update"
)

// updateInfo is what the menu and the panel show about releases.
type updateInfo struct {
	Current    string `json:"current"`
	Latest     string `json:"latest,omitempty"` // set only when newer than Current
	URL        string `json:"url,omitempty"`
	Installing bool   `json:"installing"`
	Dev        bool   `json:"dev"` // not a release build: never replaced
	Error      string `json:"error,omitempty"`
}

func (a *App) updateInfo() updateInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	u := updateInfo{Current: a.version, Installing: a.installing, Error: a.upd.Error}
	if _, err := update.ParseVersion(a.version); err != nil {
		u.Dev = true
		return u
	}
	if newer, _ := update.Newer(a.version, a.upd.Latest); newer {
		u.Latest, u.URL = a.upd.Latest, a.upd.URL
	}
	return u
}

// updateLoop checks for a new release shortly after start and then every
// updates.interval (the last check time survives restarts).
func (a *App) updateLoop() {
	a.mu.Lock()
	a.upd = update.LoadStatus(a.store.UpdatePath())
	from, version := a.upd.UpdatedFrom, a.version
	a.upd.UpdatedFrom = ""
	st := a.upd
	a.mu.Unlock()
	if from != "" {
		_ = update.SaveStatus(a.store.UpdatePath(), st)
		a.saveQueue()
		if from != version {
			// the icon went away for the restart: say it is back and done
			a.logger.Printf("running %s (updated from %s)", version, from)
			go platform.Info("Juhas", "Updated "+from+" → "+version+". Juhas is running again.")
		}
	}
	timer := time.NewTimer(30 * time.Second)
	for {
		select {
		case <-a.quit:
			return
		case <-timer.C:
		}
		a.mu.Lock()
		cfg, last := a.cfg, a.upd.CheckedAt
		a.mu.Unlock()
		if cfg.Updates.CheckOn() && time.Since(last) >= cfg.Updates.Interval.D() {
			a.checkUpdate(false)
		}
		timer.Reset(10 * time.Minute)
	}
}

// checkUpdate asks GitHub for the latest release. A manual check always
// reports its result; a background one notifies once per new version.
func (a *App) checkUpdate(manual bool) {
	a.mu.Lock()
	cfg, version := a.cfg, a.version
	a.mu.Unlock()
	note := func(msg string) {
		if manual {
			platform.Info("Juhas", msg) // asked for: a dialog, notifications may be muted
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := update.ParseVersion(version); err != nil {
		if !manual {
			return
		}
		latest := "unknown"
		if rel, err := (&update.Client{Version: version}).Latest(ctx, cfg.Updates.Prerelease); err == nil {
			latest = rel.Tag
		}
		note("This is a development build (" + version + "), so it is not updated automatically. The latest release is " + latest + ".\n\nTo switch to releases: juhas update --force")
		return
	}
	rel, err := (&update.Client{Version: version}).Latest(ctx, cfg.Updates.Prerelease)
	a.mu.Lock()
	st := a.upd
	st.CheckedAt = time.Now()
	st.Error = ""
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Latest, st.URL = rel.Tag, rel.URL
	}
	newer, _ := update.Newer(version, st.Latest)
	fresh := err == nil && newer && st.Notified != st.Latest && (manual || !a.offHoursLocked())
	if fresh {
		st.Notified = st.Latest
	}
	a.upd = st
	a.mu.Unlock()
	_ = update.SaveStatus(a.store.UpdatePath(), st)
	a.requestRedraw()

	switch {
	case err != nil:
		a.logger.Printf("update check: %v", err)
		note("Update check failed: " + err.Error())
	case !newer:
		note("You are on the latest version (" + version + ").")
	case cfg.Updates.AutoInstall:
		a.logger.Printf("update: %s available, installing when idle", st.Latest)
		go a.installUpdate()
	case manual:
		a.logger.Printf("update: %s available", st.Latest)
		note("Juhas " + st.Latest + " is available (you have " + version + ").\n\nChoose “⬆ Update” in the menu.")
	case fresh:
		a.logger.Printf("update: %s available", st.Latest)
		platform.Notify(platform.Notification{Title: "Juhas " + st.Latest + " is available",
			Message: "You have " + version + ". Choose “⬆ Update” in the menu, or click to read what's new.", Open: st.URL})
	}
}

// installUpdate waits until no review runs, swaps the binary and restarts
// the tray in place.
func (a *App) installUpdate() {
	a.mu.Lock()
	if a.installing {
		a.mu.Unlock()
		return
	}
	a.installing = true
	version, tag, pre := a.version, a.upd.Latest, a.cfg.Updates.Prerelease
	a.mu.Unlock()
	a.requestRedraw()
	fail := func(err error) {
		a.logger.Printf("update to %s: %v", tag, err)
		platform.Notify(platform.Notification{Title: "Juhas: update failed", Message: err.Error()})
		a.mu.Lock()
		a.installing = false
		a.mu.Unlock()
		a.requestRedraw()
	}
	if newer, err := update.Newer(version, tag); err != nil || !newer {
		fail(errors.New("no newer release to install"))
		return
	}
	// pending update install: queued reviews do not start (one started now
	// would be cut off by the restart) — they resume after it; only the
	// running ones are waited for
	a.pauseReviews(true)
	a.logger.Printf("update to %s: pending install — waiting for running reviews, the queue resumes after the restart", tag)
	failHeld := fail
	fail = func(err error) { a.pauseReviews(false); failHeld(err) }
	for a.reviewsRunning() || a.questionsOpen() {
		select {
		case <-a.quit:
			return
		case <-time.After(15 * time.Second):
		}
	}
	exe, err := os.Executable()
	if err != nil {
		fail(err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := &update.Client{Version: version}
	rel, err := c.Latest(ctx, pre)
	if err == nil && rel.Tag != tag {
		err = fmt.Errorf("release %s is no longer the latest (%s)", tag, rel.Tag)
	}
	if err == nil {
		err = c.Install(ctx, rel, exe)
	}
	if err != nil {
		fail(err)
		return
	}
	for a.reviewsRunning() || a.questionsOpen() { // one may have slipped in before the pause
		select {
		case <-a.quit:
			return
		case <-time.After(15 * time.Second):
		}
	}
	a.logger.Printf("updated %s → %s, restarting", version, tag)
	a.mu.Lock()
	a.upd.UpdatedFrom = version
	st := a.upd
	a.mu.Unlock()
	_ = update.SaveStatus(a.store.UpdatePath(), st)
	platform.Notify(platform.Notification{Title: "Juhas updated to " + tag, Message: "Restarting…", Open: rel.URL})
	time.Sleep(time.Second)            // let the notification go out
	_ = os.Remove(WebURLFile(a.store)) // the new process serves on a new port
	quit, err := platform.Relaunch(exe)
	if err != nil {
		fail(fmt.Errorf("installed, but the restart failed (%v) — quit and start Juhas again", err))
		return
	}
	if quit {
		systray.Quit() // the new process starts once this one is gone
	}
}

func (a *App) pauseReviews(on bool) {
	a.mu.Lock()
	r := a.runner
	a.mu.Unlock()
	if r != nil {
		r.Pause(on)
	}
}

// reviewsRunning: a review is in progress (queued ones wait for the restart).
func (a *App) reviewsRunning() bool {
	a.mu.Lock()
	r := a.runner
	a.mu.Unlock()
	if r == nil {
		return false
	}
	run, _ := r.Queue()
	return len(run) > 0
}

// queueFile carries the reviews still queued over an update's restart.
func (a *App) queueFile() string { return filepath.Join(filepath.Dir(a.store.Path()), "queue.json") }

func (a *App) saveQueue() {
	a.mu.Lock()
	r := a.runner
	a.mu.Unlock()
	if r == nil {
		return
	}
	q := r.Waiting()
	if len(q) == 0 {
		return
	}
	b, _ := json.Marshal(q)
	if err := os.WriteFile(a.queueFile(), b, 0o600); err != nil {
		a.logger.Printf("update: saving the queue: %v", err)
		return
	}
	a.logger.Printf("update: %d queued reviews carried over the restart", len(q))
}

// resumeQueue queues again what an update's restart carried over.
func (a *App) resumeQueue() {
	b, err := os.ReadFile(a.queueFile())
	if err != nil {
		return
	}
	_ = os.Remove(a.queueFile())
	var q []review.Queued
	if json.Unmarshal(b, &q) != nil {
		return
	}
	a.mu.Lock()
	r := a.runner
	a.mu.Unlock()
	if r == nil {
		return
	}
	for _, x := range q {
		r.Enqueue(x.Item, x.Dir, x.Agent)
	}
	a.logger.Printf("resumed %d queued reviews after the update", len(q))
}

// questionsOpen: a "Review it?" dialog is waiting for an answer, which a
// restart would lose.
func (a *App) questionsOpen() bool {
	st, err := a.store.Read()
	if err != nil {
		return false
	}
	for _, j := range st.Jobs {
		if j.Status == state.Asked {
			return true
		}
	}
	return false
}
