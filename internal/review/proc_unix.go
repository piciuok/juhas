//go:build unix

package review

import (
	"os/exec"
	"syscall"
)

// claude spawns subprocesses (subagents, gh, pnpm); cancel the whole group.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}
