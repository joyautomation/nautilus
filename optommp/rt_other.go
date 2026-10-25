//go:build !linux

package optommp

import (
	"fmt"
	"net"
	goruntime "runtime"
	"time"
)

// SCHED_FIFO is Linux. Elsewhere a driver that asks for a priority keeps
// its locked thread, says so on its status row, and runs normally.
func rtThread(prio int) error {
	goruntime.LockOSThread()
	return fmt.Errorf("SCHED_FIFO priority %d: not supported on this OS; the driver runs at normal priority", prio)
}

func nap(d time.Duration) { time.Sleep(d) }

func blockingConn(c net.Conn) net.Conn { return c }

func udpReceiver(uc *net.UDPConn, wait time.Duration) (func([]byte) (int, net.IP, error), func(), bool) {
	return nil, nil, false
}
