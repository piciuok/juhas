// Package tray is the status-bar / system-tray app (macOS menu bar, Linux
// StatusNotifierItem): it polls, shows the queue with counters, flashes on
// urgent items and runs reviews on demand.
package tray

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/systray"

	"github.com/piciuok/juhas/internal/app"
	"github.com/piciuok/juhas/internal/config"
	"github.com/piciuok/juhas/internal/configui"
	"github.com/piciuok/juhas/internal/doctor"
	"github.com/piciuok/juhas/internal/effects"
	"github.com/piciuok/juhas/internal/forge"
	"github.com/piciuok/juhas/internal/limits"
	"github.com/piciuok/juhas/internal/platform"
	"github.com/piciuok/juhas/internal/queue"
	"github.com/piciuok/juhas/internal/review"
	"github.com/piciuok/juhas/internal/scan"
	"github.com/piciuok/juhas/internal/state"
	"github.com/piciuok/juhas/internal/update"
)

type App struct {
	cfgPath    string
	store      *state.Store
	logger     *log.Logger
	doctorOpts doctor.Options

	mu         sync.Mutex
	cfg        *config.Config
	cfgMod     time.Time
	collector  *queue.Collector
	core       *app.Core
	runner     *review.Runner
	snap       *queue.Snapshot
	report     *doctor.Report
	polling    bool
	lastErr    string
	animating  bool // urgent animation running
	asking     bool // question-mark animation: a proposal waits for me
	version    string
	upd        update.Status
	installing bool // an update is downloading or waiting for reviews to finish
	frame      int
	menuSig    string
	limits     limitsLine // cached: Codex is read from its session files
	ui         *configui.Server
	uiURL      string

	pollNow chan bool // true = the user asked ("Check now")
	redraw  chan struct{}
	quit    chan struct{}
}

// Options configure Run.
type Options struct {
	DoctorOptions doctor.Options
	Version       string // of this binary ("dev" or a git describe outside releases)
}

// Run starts the tray app and blocks until Quit.
func Run(opt Options) error {
	migrated := platform.MigrateLegacy()
	store := state.Open()
	lock, err := singleInstance(store)
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := os.MkdirAll(filepath.Dir(store.PollLog()), 0o755); err != nil {
		return err
	}
	lf, err := os.OpenFile(store.PollLog(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()

	a := &App{
		cfgPath:    config.DefaultPath(),
		store:      store,
		logger:     log.New(lf, "", log.LstdFlags),
		doctorOpts: opt.DoctorOptions,
		version:    opt.Version,
		pollNow:    make(chan bool, 1),
		redraw:     make(chan struct{}, 1),
		quit:       make(chan struct{}),
	}
	if err := a.loadConfig(); err != nil {
		return err
	}
	_ = store.RecoverInterrupted()
	a.logger.Printf("start (pid %d, %s)", os.Getpid(), runtime.GOOS)
	// logout / launchctl stop / kill: quit through onExit so running agents
	// are stopped instead of left behind without anyone reading their output
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() { <-sig; systray.Quit() }()
	for _, m := range migrated {
		a.logger.Printf("migration: %s", m)
	}
	systray.Run(a.onReady, a.onExit)
	return nil
}

// singleInstance takes an exclusive lock so a second tray exits right away.
func singleInstance(store *state.Store) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(store.TrayLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("juhas tray is already running")
	}
	return f, nil
}

// loadConfig (re)reads the config when the file changed.
func (a *App) loadConfig() error {
	st, err := os.Stat(a.cfgPath)
	if err != nil {
		return fmt.Errorf("%w\nCreate it: juhas config init", err)
	}
	a.mu.Lock()
	same := a.cfg != nil && st.ModTime().Equal(a.cfgMod)
	a.mu.Unlock()
	if same {
		return nil
	}
	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg, a.cfgMod = cfg, st.ModTime()
	a.collector = queue.NewCollector(cfg)
	if a.runner == nil {
		a.runner = &review.Runner{Cfg: cfg, Store: a.store, OnChange: a.requestRedraw, OnDone: a.onReviewDone}
	} else {
		a.runner.Cfg = cfg
	}
	a.core = &app.Core{Cfg: cfg, Store: a.store, Runner: a.runner, Ask: platform.Ask, Notify: platform.Notify, Logf: a.logger.Printf}
	return nil
}

func (a *App) onReady() {
	systray.SetTooltip("Juhas")
	a.setIcon()
	a.rebuild()
	go a.pollLoop()
	go a.flashLoop()
	go a.redrawLoop()
	go a.runDoctor()
	go a.autoMapLoop()
	go a.updateLoop()
	go func() { _, _, _ = a.web() }() // serve the panel from the start
}

// autoMapLoop maps new clones from scan_dirs into the config file, at start
// and then every queue.ScanEvery.
func (a *App) autoMapLoop() {
	for {
		a.autoMap()
		select {
		case <-a.quit:
			return
		case <-time.After(queue.ScanEvery):
		}
	}
}

func (a *App) autoMap() {
	var changes []scan.Change
	errNothing := errors.New("nothing to map")
	err := config.Modify(a.cfgPath, func(cfg *config.Config) error { // fresh from disk, under the write lock
		if !cfg.AutoMapOn() {
			return errNothing
		}
		var err error
		if changes, err = scan.AutoMap(context.Background(), cfg); err == nil && len(changes) == 0 {
			err = errNothing
		}
		return err
	})
	if errors.Is(err, errNothing) {
		return
	}
	if err != nil {
		a.logger.Printf("auto_map: %v", err)
		return
	}
	var added []string
	for _, c := range changes {
		if c.Old != "" {
			a.logger.Printf("auto_map: %s moved %s → %s", c.Key, config.Contract(c.Old), config.Contract(c.Dir))
		} else {
			a.logger.Printf("auto_map: added %s → %s", c.Key, config.Contract(c.Dir))
			added = append(added, app.ShortRepo(c.Key))
		}
	}
	if len(added) > 0 {
		platform.Notify(platform.Notification{Title: fmt.Sprintf("Juhas: added %d repos from the projects folder", len(added)), Message: trunc(strings.Join(added, ", "), 200)})
	}
	a.requestPoll()
}

// WebURLFile holds the running tray's panel URL (with its token).
func WebURLFile(store *state.Store) string {
	return filepath.Join(filepath.Dir(store.Path()), "panel.url")
}

func (a *App) onExit() {
	_ = os.Remove(WebURLFile(a.store))
	close(a.quit)
	if a.runner != nil {
		a.runner.Stop()
	}
	a.logger.Printf("stop")
}

func (a *App) requestRedraw() {
	select {
	case a.redraw <- struct{}{}:
	default:
	}
}

func (a *App) requestPoll() { a.poke(false) }

// requestManualPoll is "Check now": it may re-ask skipped items.
func (a *App) requestManualPoll() { a.poke(true) }

func (a *App) poke(manual bool) {
	select {
	case a.pollNow <- manual:
	default:
	}
}

func (a *App) pollLoop() {
	manual := false
	for {
		a.poll(manual)
		a.mu.Lock()
		every := a.cfg.PollInterval.D()
		a.mu.Unlock()
		select {
		case <-a.quit:
			return
		case manual = <-a.pollNow:
		case <-time.After(every):
			manual = false
		}
	}
}

// split separates what still needs me from history: reviewed items whose
// head has not moved since the brief.
func split(snap *queue.Snapshot, st *state.State) (*queue.Snapshot, []queue.Entry) {
	if snap == nil {
		return nil, nil
	}
	if st == nil {
		return snap, nil
	}
	return snap.Split(func(e queue.Entry) bool { return done(st.Job(e.URL), e.HeadSHA) })
}

// done: reviewed at this head and its comment proposal fully decided.
func done(j *state.Job, head string) bool {
	return j.Reviewed(head) && review.ProposalResolved(j.Log)
}

func (a *App) poll(manual bool) {
	if err := a.loadConfig(); err != nil {
		a.mu.Lock()
		a.lastErr = "config: " + err.Error()
		a.mu.Unlock()
		a.logger.Printf("config: %v", err)
		a.requestRedraw()
		return
	}
	a.mu.Lock()
	a.polling = true
	c, core := a.collector, a.core
	a.mu.Unlock()
	a.requestRedraw()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	snap := c.Collect(ctx)
	cancel()
	for _, e := range snap.Errors {
		a.logger.Printf("poll: %s", e)
	}
	a.logger.Printf("poll: %d queued, %d urgent", len(snap.Entries), snap.UrgentCount())
	saveSnapshot(a.store, snap)

	a.mu.Lock()
	a.snap, a.polling, a.lastErr = snap, false, ""
	a.mu.Unlock()
	st, _ := a.store.Read()
	active, _ := split(snap, st)
	a.kick(active)
	core.HandleNew(snap, manual) // the full list: reviewed items may need a recheck
	a.requestRedraw()
}

func (a *App) runDoctor() { a.doctorNow(false) }

// doctorNow runs the checks (withAI: also a tiny agent call) and keeps the
// report for the menu and the diagnostics page.
func (a *App) doctorNow(withAI bool) *doctor.Report {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	opt := a.doctorOpts
	opt.WithClaude = opt.WithClaude || withAI
	rep := doctor.Run(context.Background(), cfg, opt)
	writeReport(a.store, rep)
	a.mu.Lock()
	a.report = rep
	a.mu.Unlock()
	if rep.Worst() == doctor.Fail {
		platform.Notify(platform.Notification{Title: "Juhas: configuration problem", Message: fmt.Sprintf("%d errors — see Diagnostics in the menu", rep.Count(doctor.Fail))})
	}
	a.requestRedraw()
	return rep
}

// openDoctor shows the diagnostics page (the text report if the page
// cannot start).
func (a *App) openDoctor() {
	if s, _, err := a.web(); err == nil {
		_ = platform.Open(s.DoctorURL())
		return
	}
	_ = platform.Open(reportPath(a.store))
}

// flashLoop animates the bolt while an urgent item is unacknowledged, or
// the question mark while a proposal waits for my decision.
func (a *App) flashLoop() {
	for {
		a.mu.Lock()
		every := a.cfg.FlashInterval.D() / 3
		a.mu.Unlock()
		if every < 120*time.Millisecond {
			every = 120 * time.Millisecond
		}
		select {
		case <-a.quit:
			return
		case <-time.After(every):
		}
		st, _ := a.store.Read()
		a.mu.Lock()
		was := a.animating || a.asking
		a.animating = a.shouldFlash(st)
		a.asking = !a.animating && a.shouldAsk(st)
		if a.animating || a.asking {
			a.frame++
		}
		redraw := a.animating || a.asking || was
		a.mu.Unlock()
		if redraw {
			a.setIcon()
		}
	}
}

// kick: the first time an urgent item shows up (and is not acknowledged),
// zap the screen and send a loud notification. Remembered in state, so a
// restart does not repeat it.
func (a *App) kick(snap *queue.Snapshot) {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	var fresh []queue.Entry
	_ = a.store.Update(func(st *state.State) error {
		for _, e := range snap.Entries {
			if e.Urgent && !st.Acked(e.URL) && !st.Zapped(e.URL) {
				st.Zap(e.URL)
				fresh = append(fresh, e)
			}
		}
		return nil
	})
	if len(fresh) == 0 {
		return
	}
	if cfg.ScreenEffectOn() {
		effects.Zap()
	}
	e := fresh[0]
	title := fmt.Sprintf("⚡ Urgent: %s %s", app.ShortRepo(e.Repo), e.Ref())
	msg := e.Title + " — " + e.UrgentWhy
	if len(fresh) > 1 {
		title = fmt.Sprintf("⚡ %d urgent PR/MRs", len(fresh))
		var parts []string
		for _, f := range fresh {
			parts = append(parts, app.ShortRepo(f.Repo)+" "+f.Ref())
		}
		msg = strings.Join(parts, ", ")
	}
	platform.Notify(platform.Notification{Title: title, Message: msg, Open: e.URL, Urgent: true})
	a.logger.Printf("kick: %d new urgent", len(fresh))
}

// shouldFlash: there is an urgent entry, not acknowledged, in a repo with flash on.
func (a *App) shouldFlash(st *state.State) bool {
	if a.snap == nil || st == nil {
		return false
	}
	active, _ := split(a.snap, st)
	for _, e := range active.Entries {
		if !e.Urgent || st.Acked(e.URL) {
			continue
		}
		if s, err := a.cfg.Effective(e.Repo); err == nil && s.Flash {
			return true
		}
	}
	return false
}

// shouldAsk: a proposal in the queue waits for me, in a repo with
// flash_decisions on.
func (a *App) shouldAsk(st *state.State) bool {
	if a.snap == nil || st == nil {
		return false
	}
	active, _ := split(a.snap, st)
	for _, e := range active.Entries {
		if _, ok := proposalOf(st.Job(e.URL)); !ok {
			continue
		}
		if s, err := a.cfg.Effective(e.Repo); err == nil && s.FlashDecide {
			return true
		}
	}
	return false
}

func (a *App) redrawLoop() {
	for {
		select {
		case <-a.quit:
			return
		case <-a.redraw:
		}
		a.setIcon()
		a.rebuild()
		time.Sleep(300 * time.Millisecond) // coalesce bursts of state changes
	}
}

func (a *App) onReviewDone(j state.Job) {
	if j.Status == state.Done {
		open, title := j.Brief, "Brief ready: "
		if j.Log != "" {
			if p, err := review.LoadProposal(review.ProposalPath(j.Log)); err == nil && len(p.All()) > 0 {
				if s, _, err := a.web(); err == nil {
					open, title = s.ReviewURL(j.Log), fmt.Sprintf("Review proposal (%d): ", len(p.All()))
				}
			}
		}
		platform.Notify(platform.Notification{Title: title + app.ShortRepo(j.Repo) + " " + ref(j), Message: j.Title + " — accept, edit or reject", Open: open})
	} else {
		platform.Notify(platform.Notification{Title: "AI review: error " + app.ShortRepo(j.Repo) + " " + ref(j), Message: j.Error, Open: j.Log})
	}
	a.requestRedraw()
}

func ref(j state.Job) string {
	if strings.Contains(j.URL, "/-/merge_requests/") {
		return fmt.Sprintf("!%d", j.Number)
	}
	return fmt.Sprintf("#%d", j.Number)
}

// --- icon + title --------------------------------------------------------

func (a *App) setIcon() {
	st, _ := a.store.Read()
	a.mu.Lock()
	s := IconState{Template: runtime.GOOS == "darwin", Badge: runtime.GOOS != "darwin", Flash: a.animating, Ask: a.asking, Frame: a.frame}
	if a.snap != nil {
		active, _ := split(a.snap, st)
		s.Queued = len(active.Entries)
		s.Urgent = active.UrgentCount()
		s.Decide = len(awaitingDecision(active, st))
		s.Problem = len(a.snap.Errors) > 0
	}
	if a.lastErr != "" || (a.report != nil && a.report.Worst() == doctor.Fail) {
		s.Problem = true
	}
	if st != nil {
		for _, j := range st.Jobs {
			if j.Status == state.Running {
				s.Running = true
			}
		}
	}
	a.mu.Unlock()

	icon := Render(s)
	if s.Template && !s.Flash {
		systray.SetTemplateIcon(icon, icon)
	} else {
		systray.SetIcon(icon)
	}
	tip := fmt.Sprintf("Juhas — %d to review", s.Queued)
	if s.Urgent > 0 {
		tip += fmt.Sprintf(", %d urgent", s.Urgent)
	}
	if s.Decide > 0 {
		tip += fmt.Sprintf(", %d proposals awaiting decision", s.Decide)
	}
	if s.Running {
		tip += ", review in progress"
	}
	systray.SetTooltip(tip)
	if runtime.GOOS == "darwin" {
		title := ""
		if s.Queued > 0 {
			title = fmt.Sprint(s.Queued)
		}
		if s.Decide > 0 {
			q := " ❓" // blinks and sparks while the reminder is on
			if s.Ask && s.Frame%2 == 1 {
				q = " ✦❔"
			} else if s.Ask {
				q = " ✧❓"
			}
			title += q + fmt.Sprint(s.Decide)
		}
		if s.Urgent > 0 {
			bolt := "⚡"
			if s.Flash && s.Frame%2 == 1 {
				bolt = "✦" // the title sparks along with the icon
			}
			title += " " + bolt + fmt.Sprint(s.Urgent)
		}
		systray.SetTitle(strings.TrimSpace(title))
	}
}

// --- menu ----------------------------------------------------------------

// rebuild recreates the whole menu from the snapshot and state.
func (a *App) rebuild() {
	st, err := a.store.Read()
	if err != nil {
		st = &state.State{Jobs: map[string]*state.Job{}}
	}
	u := a.updateInfo()
	lim := a.limits.get()
	a.mu.Lock()
	snap, rep, cfg, polling, lastErr, runner := a.snap, a.report, a.cfg, a.polling, a.lastErr, a.runner
	sig := menuSignature(snap, st, rep, polling, lastErr, runner) + fmt.Sprintf("|upd:%s:%v|lim:%s", u.Latest, u.Installing, lim.text)
	if sig == a.menuSig {
		a.mu.Unlock()
		return // unchanged: do not close a menu the user may have open
	}
	a.menuSig = sig
	a.mu.Unlock()

	systray.ResetMenu()

	full := snap
	snap, history := split(full, st)
	head := "Juhas"
	switch {
	case polling && snap == nil:
		head += " — checking…"
	case snap != nil:
		head += fmt.Sprintf(" — %d to review · checked %s", len(snap.Entries), snap.At.Format("15:04"))
		if polling {
			head += " ⟳"
		}
	}
	systray.AddMenuItem(head, "").Disable()
	switch {
	case u.Installing:
		systray.AddMenuItem("⬆ Installing "+u.Latest+" (waits for running reviews)…", "").Disable()
	case u.Latest != "":
		item(nil, fmt.Sprintf("⬆ Update to %s (you have %s)", u.Latest, u.Current), func() { go a.installUpdate() })
		item(nil, "    📰 What's new in "+u.Latest, func() { _ = platform.Open(u.URL) })
	}
	item(nil, "🗂 Panel (stays open while you click)…", a.openPanel)

	if lastErr != "" {
		item(nil, "⚠ "+trunc(lastErr, 80), func() { _ = platform.Open(a.cfgPath) })
	}
	if snap != nil && len(snap.Errors) > 0 { // errors are the same in both views
		m := systray.AddMenuItem(fmt.Sprintf("⚠ Fetch errors (%d)", len(snap.Errors)), "")
		for _, e := range snap.Errors {
			m.AddSubMenuItem(trunc(e, 100), "").Disable()
		}
		item(m, "📜 Show log", func() { _ = platform.Open(a.store.PollLog()) })
	}
	if rep != nil && rep.Worst() != doctor.OK {
		label := fmt.Sprintf("⚠ Diagnostics: %d errors, %d warnings", rep.Count(doctor.Fail), rep.Count(doctor.Warn))
		item(nil, label, a.openDoctor)
	}

	if snap != nil {
		systray.AddSeparator()
		urgentUnacked := []string{}
		byRepo := map[string][]queue.Entry{}
		for _, e := range snap.Entries {
			byRepo[strings.ToLower(e.Repo)] = append(byRepo[strings.ToLower(e.Repo)], e)
			if e.Urgent && !st.Acked(e.URL) {
				urgentUnacked = append(urgentUnacked, e.URL)
			}
		}
		repos := append([]queue.RepoStat{}, snap.Repos...)
		sort.SliceStable(repos, func(i, j int) bool {
			if (repos[i].Urgent > 0) != (repos[j].Urgent > 0) {
				return repos[i].Urgent > 0
			}
			return repos[i].Queued > repos[j].Queued
		})
		if len(snap.Entries) == 0 {
			systray.AddMenuItem("✓ Nothing is waiting for your review", "").Disable()
		}
		if waiting := awaitingDecision(snap, st); len(waiting) > 0 {
			systray.AddMenuItem(fmt.Sprintf("📝 Proposals awaiting your decision (%d)", len(waiting)), "").Disable()
			for _, w := range waiting {
				w := w
				item(nil, fmt.Sprintf("    → %s %s — %s", w.Ref, trunc(w.Title, 44), w.label()), func() { a.openReview(w.Log) })
			}
			systray.AddSeparator()
		}
		for _, r := range repos {
			if len(byRepo[strings.ToLower(r.Key)]) == 0 {
				continue // everything reviewed: it lives in history
			}
			a.addRepo(r, byRepo[strings.ToLower(r.Key)], st, cfg, runner)
		}
		if n := len(history) + closedReviewed(st, full); n > 0 {
			systray.AddSeparator()
			item(nil, fmt.Sprintf("📚 Reviewed history (%d) — in the Panel", n), a.openPanelHistory)
		}
		if len(urgentUnacked) > 0 {
			systray.AddSeparator()
			item(nil, fmt.Sprintf("🔕 Acknowledge urgent (%d) — stop sparking", len(urgentUnacked)), func() {
				_ = a.store.Update(func(st *state.State) error { st.Ack(urgentUnacked...); return nil })
				a.requestRedraw()
			})
		}
	}

	systray.AddSeparator()
	item(nil, "🔄 Check now (also re-asks skipped)", a.requestManualPoll)
	item(nil, "📁 Briefs folder", func() {
		d := config.Expand(cfg.BriefsDir)
		_ = os.MkdirAll(d, 0o755)
		_ = platform.Open(d)
	})
	item(nil, "⚙️ Settings…", a.openSettings)
	item(nil, "🧾 Config file (JSON)", func() { _ = platform.Open(a.cfgPath) })
	item(nil, "🩺 Diagnostics (CLI, login, repos)…", a.openDoctor)
	item(nil, "📜 Log", func() { _ = platform.Open(a.store.PollLog()) })
	item(nil, "🔍 Check for updates ("+a.version+")", func() { go a.checkUpdate(true) })
	zap := systray.AddMenuItemCheckbox("⚡ On-screen kick for urgent items", "", cfg.ScreenEffectOn())
	go func() {
		for range zap.ClickedCh {
			a.setScreenEffect(!cfg.ScreenEffectOn())
		}
	}()
	auto := systray.AddMenuItemCheckbox("🚀 Start at login", "", platform.AutostartEnabled())
	go func() {
		for range auto.ClickedCh {
			if platform.AutostartEnabled() {
				_ = platform.AutostartUninstall()
			} else if exe, err := InstallBinary(); err == nil {
				_ = platform.AutostartInstall(exe)
			} else {
				platform.Notify(platform.Notification{Title: "Juhas", Message: "Autostart: " + err.Error()})
			}
			a.requestRedraw()
		}
	}()
	if lim.text != "" {
		systray.AddSeparator()
		systray.AddMenuItem(lim.text, lim.detail).Disable()
	}
	item(nil, "🚪 Quit", systray.Quit)
}

func (a *App) addRepo(r queue.RepoStat, entries []queue.Entry, st *state.State, cfg *config.Config, runner *review.Runner) {
	open := ""
	if r.Open >= 0 {
		open = fmt.Sprintf("%d open · ", r.Open)
	}
	label := fmt.Sprintf("📦 %s — %s%d for me", app.ShortRepo(r.Key), open, r.Queued)
	if r.Urgent > 0 {
		label += fmt.Sprintf(" · 🔴%d", r.Urgent)
	}
	item(nil, label, func() { _ = platform.Open(listURL(r.Key, entries)) })
	for _, e := range entries {
		a.addEntry(e, st.Job(e.URL), cfg, runner)
	}
	if len(r.All) > 0 {
		queued := map[string]bool{}
		for _, e := range entries {
			queued[e.URL] = true
		}
		all := systray.AddMenuItem(fmt.Sprintf("    📋 Show all PR/MRs (%d)", len(r.All)), "Every open PR/MR of the repo")
		for _, o := range r.All {
			url := o.URL
			item(all, allLabel(o, queued[o.URL]), func() { _ = platform.Open(url) })
		}
		all.AddSeparator()
		item(all, "🌐 Open the list in the browser", func() { _ = platform.Open(listURL(r.Key, entries)) })
	}
}

// limitsLine is the one-line plan usage of the agents, re-read at most
// once a minute.
type limitsLine struct {
	mu           sync.Mutex
	at           time.Time
	text, detail string
}

func (l *limitsLine) get() limitsLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.at) >= time.Minute {
		now := time.Now()
		var parts, details []string
		for _, ag := range []struct {
			name string
			read func() (limits.Usage, bool)
		}{{"Claude", limits.Claude}, {"Codex", limits.Codex}} {
			u, ok := ag.read()
			if short := u.Short(now); ok && short != "" {
				parts = append(parts, ag.name+" "+short)
				details = append(details, u.Detail(ag.name))
			} else if ag.name == "Claude" {
				// Claude reports its limits only inside a run
				parts = append(parts, "Claude — after its first review")
				details = append(details, "Claude: its limits come with a review run")
			}
		}
		l.at, l.text, l.detail = now, "📊 Limits used: "+strings.Join(parts, "   ·   "), strings.Join(details, "\n")
	}
	return limitsLine{text: l.text, detail: l.detail}
}

// allLabel is one line of "Show all": "• !883 title · 🟣 author", the dot
// marking those waiting for me.
func allLabel(o queue.Open, queued bool) string {
	mark := "   "
	if queued {
		mark = "•  "
	}
	draft := ""
	if o.Draft {
		draft = "[draft] "
	}
	return fmt.Sprintf("%s%s %s%s%s", mark, o.Ref, draft, trunc(o.Title, 60), byAuthor(o.Author))
}

// authorBadges are the colours an author gets; red is left out, it marks urgent.
var authorBadges = []string{"🟠", "🟡", "🟢", "🔵", "🟣", "🟤", "⚫", "🟧", "🟨", "🟩", "🟦", "🟪", "🟫"}

// authorBadge is a colour derived from the username, the same on every poll.
func authorBadge(user string) string {
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(user)))
	return authorBadges[h.Sum32()%uint32(len(authorBadges))]
}

// byAuthor is the "  · 🟣 author" tail of a menu line ("" without one).
func byAuthor(user string) string {
	if user == "" {
		return ""
	}
	return "  · " + authorBadge(user) + " " + user
}

// listURL is the repo's PR/MR list page.
func listURL(key string, entries []queue.Entry) string {
	host, project := forge.SplitKey(key)
	if len(entries) > 0 && entries[0].Platform == forge.GitHub || strings.Contains(host, "github") {
		return "https://" + host + "/" + project + "/pulls"
	}
	return "https://" + host + "/" + project + "/-/merge_requests"
}

func (a *App) addEntry(e queue.Entry, j *state.Job, cfg *config.Config, runner *review.Runner) {
	dir := app.Dir(e)
	status := ""
	if j != nil {
		status = j.Status
	}
	busy := runner != nil && runner.Busy(e.URL)
	icon := "•"
	switch {
	case busy || status == state.Running || status == state.Queued:
		icon = "⏳"
	case status == state.Done && j.Stale(e.HeadSHA):
		icon = "⟳"
	case status == state.Done:
		icon = "📝"
	case status == state.Failed:
		icon = "❌"
	case status == state.Skipped:
		icon = "🔕"
	case dir == "":
		icon = "❓"
	}
	if e.Urgent {
		icon = "🔴" + icon
	}
	m := systray.AddMenuItem(entryLabel(icon, e), e.Author)

	if pr, ok := proposalOf(j); ok && !busy {
		logPath := j.Log
		item(m, "📝 Proposed comments — "+pr.label()+"…", func() { a.openReview(logPath) })
	}
	item(m, "🌐 Open in browser", func() { _ = platform.Open(e.URL) })
	s, _ := cfg.Effective(e.Repo)
	method := review.ResolveFor(s.Agent, dir, s.Review)
	switch {
	case busy:
		logPath := review.LogPath(a.store, e.URL)
		item(m, "👁 Live view", func() { a.openLog(logPath) })
		item(m, "⏹ Cancel review", func() { runner.Cancel(e.URL) })
	case dir == "":
		m.AddSubMenuItem("❓ No local clone — set path in config", "").Disable()
	case method.Kind == review.KindNone:
		m.AddSubMenuItem("⚠ No review skill (fallback = none)", "").Disable()
	default:
		verb := "▶ Review"
		if status == state.Done || status == state.Failed {
			verb = "▶ Review again"
		}
		// the repo's default agent first, then the other one
		agents := []string{s.Agent}
		for _, ag := range config.Agents {
			if ag != s.Agent {
				agents = append(agents, ag)
			}
		}
		for i, ag := range agents {
			ag := ag
			m2 := review.ResolveFor(ag, dir, s.Review)
			label := fmt.Sprintf("%s — %s (%s)", verb, config.AgentLabel(ag), m2)
			if i > 0 {
				label = fmt.Sprintf("▷ … or in %s (%s)", config.AgentLabel(ag), m2)
			}
			item(m, label, func() { runner.Enqueue(e.Item, dir, ag) })
		}
	}
	if j != nil && j.Brief != "" {
		l := "📄 Open brief"
		if j.Stale(e.HeadSHA) {
			l += " (new commits since the brief)"
		}
		brief := j.Brief
		item(m, l, func() { _ = platform.Open(brief) })
	}
	if j != nil && j.Status == state.Failed {
		m.AddSubMenuItem("❌ Error: "+trunc(j.Error, 90), "").Disable()
	}
	if j != nil && j.Log != "" && !busy { // busy entries got "Live view" above
		logPath := j.Log
		item(m, "📜 Run log", func() { a.openLog(logPath) })
	}
	if e.Path == "" && e.PathGuess != "" {
		guess := e.PathGuess
		item(m, "🔗 Save mapping → "+config.Contract(guess), func() { a.saveMapping(e.Repo, guess) })
	}
	if status != state.Skipped && !busy {
		item(m, "🔕 Skip (don't ask until new commits)", func() {
			_ = a.store.Update(func(st *state.State) error {
				jj := st.Job(e.URL)
				if jj == nil {
					jj = &state.Job{URL: e.URL, Repo: e.Repo, Number: e.Number, Title: e.Title}
					st.Put(jj)
				}
				jj.Status, jj.SeenSHA = state.Skipped, e.HeadSHA
				return nil
			})
			a.requestRedraw()
		})
	}
	if e.Urgent {
		m.AddSubMenuItem("🔴 Urgent: "+e.UrgentWhy, "").Disable()
	}
}

// web returns the tray's settings/log server, starting it on first use.
func (a *App) web() (*configui.Server, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ui != nil {
		return a.ui, a.uiURL, nil
	}
	s, url, err := configui.StartPersistent(a.cfgPath, a.requestPoll)
	if err != nil {
		return nil, "", err
	}
	s.Panel = a
	s.OnReview = a.requestRedraw
	s.Doctor = a.doctorNow
	s.LastDoctor = func() *doctor.Report { a.mu.Lock(); defer a.mu.Unlock(); return a.report }
	a.ui, a.uiURL = s, url
	// `juhas panel` / `juhas config ui` reuse this server
	_ = os.WriteFile(WebURLFile(a.store), []byte(s.PanelURL()+"\n"), 0o600)
	return s, url, nil
}

// openPanel shows the queue in a browser tab that stays open while you
// click (the status-bar menu closes on every click).
func (a *App) openPanel() {
	s, _, err := a.web()
	if err != nil {
		platform.Notify(platform.Notification{Title: "Juhas", Message: "Panel: " + err.Error()})
		return
	}
	_ = platform.Open(s.PanelURL())
}

func (a *App) openPanelHistory() {
	s, _, err := a.web()
	if err != nil {
		return
	}
	_ = platform.Open(s.PanelURL() + "#history")
}

// closedReviewed counts reviewed jobs whose PR/MR left the queue (merged,
// closed or no longer asking for me).
func closedReviewed(st *state.State, snap *queue.Snapshot) int {
	if st == nil || snap == nil {
		return 0
	}
	in := map[string]bool{}
	for _, e := range snap.Entries {
		in[e.URL] = true
	}
	n := 0
	for url, j := range st.Jobs {
		if !in[url] && j.Status == state.Done && j.Brief != "" {
			n++
		}
	}
	return n
}

func (a *App) openSettings() {
	_, url, err := a.web()
	if err != nil {
		platform.Notify(platform.Notification{Title: "Juhas", Message: "Settings: " + err.Error()})
		return
	}
	_ = platform.Open(url)
}

// openLog shows the live run log of a review in the browser.
func (a *App) openLog(logPath string) {
	s, _, err := a.web()
	if err != nil {
		_ = platform.Open(logPath) // fall back to the raw file
		return
	}
	_ = platform.Open(s.LogURL(logPath))
}

// setScreenEffect flips screen_effect in the config file.
func (a *App) setScreenEffect(on bool) {
	err := config.Modify(a.cfgPath, func(cfg *config.Config) error { cfg.ScreenEffect = &on; return nil })
	if err != nil {
		platform.Notify(platform.Notification{Title: "Juhas", Message: "Setting not saved: " + err.Error()})
		return
	}
	_ = a.loadConfig()
	a.mu.Lock()
	a.menuSig = "" // force the checkbox to redraw
	a.mu.Unlock()
	a.requestRedraw()
}

func (a *App) saveMapping(key, dir string) {
	err := config.Modify(a.cfgPath, func(cfg *config.Config) error { return cfg.SetRepoPath(key, dir) })
	if err != nil {
		platform.Notify(platform.Notification{Title: "Juhas", Message: "Mapping not saved: " + err.Error()})
		return
	}
	a.requestPoll()
}

// menuSignature captures everything the menu shows.
func menuSignature(snap *queue.Snapshot, st *state.State, rep *doctor.Report, polling bool, lastErr string, runner *review.Runner) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%v|%s|%v|", polling, lastErr, platform.AutostartEnabled())
	if rep != nil {
		fmt.Fprintf(&b, "doc%d/%d|", rep.Count(doctor.Fail), rep.Count(doctor.Warn))
	}
	if snap != nil {
		fmt.Fprintf(&b, "%s|%d|", snap.At.Format("15:04"), len(snap.Errors))
		for _, r := range snap.Repos {
			fmt.Fprintf(&b, "%s:%d:%d:%d|", r.Key, r.Open, r.Queued, r.Urgent)
			for _, o := range r.All {
				fmt.Fprintf(&b, "%s:%s:%s:%v|", o.URL, o.Title, o.Author, o.Draft)
			}
		}
		for _, e := range snap.Entries {
			fmt.Fprintf(&b, "%s:%s:%s:%v:%s:%v:%s|", e.URL, e.Title, e.HeadSHA, e.Urgent, e.PathGuess, runner != nil && runner.Busy(e.URL), e.Files.String())
			if j := st.Job(e.URL); j != nil {
				fmt.Fprintf(&b, "%s:%s:%s:%v|", j.Status, j.Brief, j.BriefSHA, st.Acked(e.URL))
				if pr, ok := proposalOf(j); ok {
					fmt.Fprintf(&b, "p%d/%d|", pr.Pending, pr.Accepted)
				}
			}
		}
	}
	return b.String()
}

// entryLabel is "    • !883 title…   +1 ~2 · 🟣 author". The systray library
// has no right-aligned column or smaller font, so the counter closes the
// label after a fixed-width title, which lines the counters up closely
// enough, and the author follows it.
func entryLabel(icon string, e queue.Entry) string {
	const width = 56
	title := []rune(trunc(e.Title, width))
	pad := ""
	if f := e.Files.String(); f != "" {
		pad = strings.Repeat(" ", width-len(title)+2) + "  " + f
	}
	pad += byAuthor(e.Author)
	return fmt.Sprintf("    %s %s %s%s", icon, e.Ref(), string(title), pad)
}

// item adds a clickable entry (to the root when parent is nil).
func item(parent *systray.MenuItem, title string, fn func()) *systray.MenuItem {
	var m *systray.MenuItem
	if parent == nil {
		m = systray.AddMenuItem(title, "")
	} else {
		m = parent.AddSubMenuItem(title, "")
	}
	go func() {
		for range m.ClickedCh { // closed by ResetMenu
			fn()
		}
	}()
	return m
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// proposalCounts summarises a run's comment proposal.
type proposalCounts struct {
	Ref, Title, Log          string
	Pending, Accepted, Total int
}

func (p proposalCounts) label() string {
	switch {
	case p.Pending > 0 && p.Accepted > 0:
		return fmt.Sprintf("%d awaiting decision, %d to publish", p.Pending, p.Accepted)
	case p.Pending > 0:
		return fmt.Sprintf("%d awaiting decision", p.Pending)
	default:
		return fmt.Sprintf("%d accepted, unpublished", p.Accepted)
	}
}

// proposalOf reports a finished run's proposal that still needs the user
// (something pending, accepted but unpublished, or failed).
func proposalOf(j *state.Job) (proposalCounts, bool) {
	if j == nil || j.Status != state.Done || j.Log == "" {
		return proposalCounts{}, false
	}
	p, err := review.LoadProposal(review.ProposalPath(j.Log))
	if err != nil || p.Resolved() {
		return proposalCounts{}, false
	}
	pend, acc, _ := p.Counts()
	for _, c := range p.All() {
		if c.Status == review.CommentFailed {
			acc++ // needs another publish attempt
		}
	}
	return proposalCounts{Ref: ref(*j), Title: j.Title, Log: j.Log, Pending: pend, Accepted: acc, Total: len(p.All())}, true
}

// awaitingDecision lists the queue's PR/MRs whose proposal waits for me.
func awaitingDecision(snap *queue.Snapshot, st *state.State) []proposalCounts {
	if snap == nil || st == nil {
		return nil
	}
	var out []proposalCounts
	for _, e := range snap.Entries {
		if pr, ok := proposalOf(st.Job(e.URL)); ok {
			out = append(out, pr)
		}
	}
	return out
}

// openReview opens the proposal page of a run.
func (a *App) openReview(logPath string) {
	s, _, err := a.web()
	if err != nil {
		platform.Notify(platform.Notification{Title: "Juhas", Message: "Proposal: " + err.Error()})
		return
	}
	_ = platform.Open(s.ReviewURL(logPath))
}
