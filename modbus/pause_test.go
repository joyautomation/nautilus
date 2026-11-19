//go:build quorumtest

package modbus

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
)

// Test plan §3.4, on Modbus over a real socket: a pause at each of the
// three points between the gate check and the send. Longer than the
// send's budget, nothing reaches the device, at any point: the deadline is
// computed from the gate's read, checked after the pause, and set on the
// socket, which refuses a write past it before the system call. Shorter,
// the write goes out. (go test -tags quorumtest ./modbus/)
func TestModbusPauseBetweenCheckAndSend(t *testing.T) {
	const budget = 150 * time.Millisecond
	for _, c := range []struct {
		name  string
		point fence.Point
	}{{"after the check", fence.AfterCheck}, {"after the deadline", fence.AfterDeadline}, {"right before the send", fence.RightBeforeSend}} {
		for _, p := range []struct {
			pause time.Duration
			late  bool
		}{{2 * budget, true}, {budget / 5, false}} {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			got := make(chan int64, 1)
			go func() {
				pc, err := ln.Accept()
				if err != nil {
					got <- -1
					return
				}
				defer pc.Close()
				_ = pc.SetReadDeadline(time.Now().Add(3 * time.Second))
				n, _ := io.Copy(io.Discard, pc)
				got <- n
			}()
			d, s := fencedDriver(t)
			s.pending = map[string]any{"SP1": float64(1)}
			d.SetSendGate(func() (bool, time.Duration) { return true, budget })
			tc, err := dialTCP(context.Background(), ln.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			fence.Pause(c.point, p.pause)
			d.flushWrites(context.Background(), s, tc)
			fence.Pause(c.point, 0)
			tc.Close()
			n := <-got
			ln.Close()
			switch {
			case p.late && n != 0:
				t.Errorf("%s, paused %s past a %s budget: the device received %d bytes; want nothing", c.name, p.pause, budget, n)
			case !p.late && n == 0:
				t.Errorf("%s, paused %s within a %s budget: nothing reached the device; want the write", c.name, p.pause, budget)
			}
		}
	}
}
