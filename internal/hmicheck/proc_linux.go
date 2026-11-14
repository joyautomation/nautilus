package hmicheck

import (
	"os/exec"
	"syscall"
)

// A browser naut started dies with naut, even when naut check is killed
// before it can close it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
