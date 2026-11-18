//go:build linux

package main

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// The backstop: a kernel timer SIGKILLs the process at the deadline,
// whatever its threads are doing (here, sleeping far past it).
func TestKillTimer(t *testing.T) {
	if os.Getenv("JITTER_KILLTIMER_CHILD") == "1" {
		if err := armKillTimer(100 * time.Millisecond); err != nil {
			println("ARM", err.Error())
			os.Exit(1)
		}
		time.Sleep(5 * time.Second)
		os.Exit(0) // not reached when the timer works
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestKillTimer$", "-test.count=1")
	cmd.Env = append(os.Environ(), "JITTER_KILLTIMER_CHILD=1")
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		ee, ok := err.(*exec.ExitError)
		ws, ok2 := ee.Sys().(syscall.WaitStatus)
		if !ok || !ok2 || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
			t.Fatalf("child ended with %v, want SIGKILL from its timer", err)
		}
		if el := time.Since(start); el > 2*time.Second {
			t.Fatalf("killed after %s, want ~100 ms", el)
		}
	case <-time.After(3 * time.Second):
		cmd.Process.Kill()
		t.Fatal("the kill timer did not fire")
	}
}
