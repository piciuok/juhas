package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// agentNames are the processes a review runs; a pid is only signalled when it
// still is one of them (after a reboot the number may belong to anything).
var agentNames = []string{"claude", "codex", "node"}

// StopOrphan stops a review agent left by a previous juhas process and its
// process group, and reaps it if it became our child (after an in-place
// restart the new image inherits it).
func StopOrphan(pid int) {
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return // gone
	}
	name := filepath.Base(strings.TrimSpace(string(out)))
	ok := false
	for _, n := range agentNames {
		ok = ok || strings.HasPrefix(name, n)
	}
	if !ok {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM) // review agents run in their own group
	_ = syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 20; i++ {
		var ws syscall.WaitStatus
		if p, _ := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); p == pid {
			return
		}
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
	var ws syscall.WaitStatus
	_, _ = syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
}

// TrayLockPath is the lock a running tray holds.
func (s *Store) TrayLockPath() string { return filepath.Join(filepath.Dir(s.path), "tray.lock") }

// TrayRunning reports whether a juhas tray holds its lock.
func (s *Store) TrayRunning() bool {
	f, err := os.OpenFile(s.TrayLockPath(), os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}
