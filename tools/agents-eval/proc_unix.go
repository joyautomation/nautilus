//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the agent in its own process group and makes the
// context's cancel kill the whole group, so a `naut` the agent left
// running dies with it.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
