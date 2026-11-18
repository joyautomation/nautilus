//go:build !linux

package runtime

import "fmt"

// Pinning and real-time priority are Linux features (sched_setaffinity,
// SCHED_FIFO). Elsewhere a task that asks for them runs normally and says
// so in its stats, rather than silently ignoring the request.
func applySched(cpus []int, prio int, _ bool, _ func(string)) error {
	if len(cpus) > 0 || prio > 0 {
		return fmt.Errorf("cpu pinning / SCHED_FIFO priority: not supported on this OS; the task runs unpinned at normal priority")
	}
	return nil
}

func releaseSched() {}

func currentTid() int            { return 0 }
func demoteTid(int, string) bool { return false }
func holdsPriority(int) bool     { return false }
