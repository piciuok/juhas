//go:build unix

package review

import (
	"os/exec"
	"syscall"
	"time"
)

// claude spawns subprocesses (subagents, gh, pnpm); cancel the whole group.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup asks the group to stop and kills what is left a few seconds
// later (exec's WaitDelay only kills the leader, not its children).
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid := cmd.Process.Pid
	err := syscall.Kill(-pgid, syscall.SIGTERM)
	time.AfterFunc(4*time.Second, func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
	return err
}
