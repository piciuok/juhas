package tray

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/piciuok/juhas/internal/platform"
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
	a.mu.Unlock()
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
	fresh := err == nil && newer && st.Notified != st.Latest
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
	// no new reviews from here on: one started now would be cut off by the restart
	a.holdReviews(true)
	failHeld := fail
	fail = func(err error) { a.holdReviews(false); failHeld(err) }
	for a.reviewsActive() || a.questionsOpen() {
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
	for a.reviewsActive() || a.questionsOpen() { // one may have slipped in before the hold
		select {
		case <-a.quit:
			return
		case <-time.After(15 * time.Second):
		}
	}
	a.logger.Printf("updated %s → %s, restarting", version, tag)
	platform.Notify(platform.Notification{Title: "Juhas updated to " + tag, Message: "Restarting…", Open: rel.URL})
	time.Sleep(time.Second)            // let the notification go out
	_ = os.Remove(WebURLFile(a.store)) // the new process serves on a new port
	if err := update.Restart(exe, "tray"); err != nil {
		fail(fmt.Errorf("installed, but the restart failed (%v) — quit and start Juhas again", err))
	}
}

func (a *App) holdReviews(on bool) {
	a.mu.Lock()
	r := a.runner
	a.mu.Unlock()
	if r != nil {
		r.Hold(on)
	}
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

// reviewsActive: a review is running or queued.
func (a *App) reviewsActive() bool {
	a.mu.Lock()
	r := a.runner
	a.mu.Unlock()
	if r == nil {
		return false
	}
	run, wait := r.Queue()
	return len(run)+len(wait) > 0
}
