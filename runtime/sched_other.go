//go:build !linux

package runtime

import "fmt"

// Pinning and real-time priority are Linux features (sched_setaffinity,
// SCHED_FIFO). Elsewhere a task that asks for them runs normally and says
// so in its stats, rather than silently ignoring the request.
func applySched(cpus []int, prio int) error {
	if len(cpus) > 0 || prio > 0 {
		return fmt.Errorf("cpu pinning / SCHED_FIFO priority: not supported on this OS; the task runs unpinned at normal priority")
	}
	return nil
}
