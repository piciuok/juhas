package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// relaunchScript waits for the old process ($1) to exit, so the new one can
// take the tray lock, then starts the new binary ($0).
const relaunchScript = `while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; exec "$0" tray --foreground`

// spawnTray starts a detached `exe tray` that waits for this process to
// exit. A fresh process (not exec in place, which keeps the pid) is what
// gets the tray icon registered again.
func spawnTray(exe string) error {
	cmd := exec.Command("/bin/sh", "-c", relaunchScript, exe, strconv.Itoa(os.Getpid()))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // outlives our process group
	return cmd.Start()
}

// detachTray starts `exe tray --foreground` in its own session, with output
// appended to the tray logs in logDir, and does not wait for it.
func detachTray(exe, logDir string) error {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	open := func(name string) (*os.File, error) {
		return os.OpenFile(filepath.Join(logDir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	}
	out, err := open("tray.out.log")
	if err != nil {
		return err
	}
	defer out.Close()
	errf, err := open("tray.err.log")
	if err != nil {
		return err
	}
	defer errf.Close()
	cmd := exec.Command(exe, "tray", "--foreground")
	cmd.Stdout, cmd.Stderr = out, errf // stdin stays /dev/null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
