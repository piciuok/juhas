package main

import (
	"fmt"
	"os"

	"juhas/internal/app"
	"juhas/internal/config"
	"juhas/internal/platform"
	"juhas/internal/queue"
	"juhas/internal/review"
	"juhas/internal/state"
	"juhas/internal/tray"
)

func init() {
	// `poll` without --dry-run acts like one tray cycle: ask, run, wait.
	pollActions = func(cfg *config.Config, snap *queue.Snapshot) error {
		store := state.Open()
		if store.TrayRunning() {
			// two runners would fight over the same jobs and dialogs
			return fmt.Errorf("the Juhas tray is running — use \"Check now\" in its menu (or poll --dry-run)")
		}
		_ = store.RecoverInterrupted()
		r := &review.Runner{Cfg: cfg, Store: store, OnDone: func(j state.Job) {
			if j.Status == state.Done {
				fmt.Println("brief:", j.Brief)
			} else {
				fmt.Println("error:", j.URL, j.Error)
			}
		}}
		core := &app.Core{Cfg: cfg, Store: store, Runner: r, Ask: platform.Ask, Notify: platform.Notify,
			Logf: func(f string, a ...any) { fmt.Printf(f+"\n", a...) }}
		p := core.HandleNew(snap, true) // a CLI poll is a manual check
		fmt.Printf("\nnew: ask %d, auto %d, silent %d, no clone %d\n", len(p.Ask), len(p.Auto), len(p.Silent), len(p.NoClone))
		core.Wait()
		return nil
	}
}

func cmdTray(args []string) error {
	return tray.Run(tray.Options{DoctorOptions: doctorOptions(false), Version: version})
}

func cmdInstall(args []string) error {
	for _, m := range platform.MigrateLegacy() {
		fmt.Println("migration:", m)
	}
	exe, err := tray.InstallBinary()
	if err != nil {
		return err
	}
	if err := platform.AutostartInstall(exe); err != nil {
		return err
	}
	fmt.Println("Installed", exe, "— start at login enabled.")
	if _, err := os.Stat(config.DefaultPath()); err != nil {
		fmt.Println("No config — create it: juhas config init")
	}
	fmt.Println("Start it now in the background:", exe, "tray &")
	return nil
}

func cmdUninstall(args []string) error {
	if err := platform.AutostartUninstall(); err != nil {
		return err
	}
	fmt.Println("Start at login disabled. The binary ~/.local/bin/juhas, config and state are kept.")
	return nil
}
