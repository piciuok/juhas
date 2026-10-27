package platform

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// relaunchScript waits for the old process ($1) to exit, so the new one can
// take the tray lock, then starts the new binary ($0).
const relaunchScript = `while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; exec "$0" tray`

// spawnTray starts a detached `exe tray` that waits for this process to
// exit. A fresh process (not exec in place, which keeps the pid) is what
// gets the tray icon registered again.
func spawnTray(exe string) error {
	cmd := exec.Command("/bin/sh", "-c", relaunchScript, exe, strconv.Itoa(os.Getpid()))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // outlives our process group
	return cmd.Start()
}
