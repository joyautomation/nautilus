//go:build !linux

package hmicheck

import "os/exec"

func detach(cmd *exec.Cmd) {}
