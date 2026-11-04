//go:build !unix

package main

import "os/exec"

// setProcessGroup is a no-op off unix: the context kills the agent
// process itself, not its children.
func setProcessGroup(*exec.Cmd) {}
