//go:build linux

// wakeup compares wake-up strategies for a 1 ms loop, outside the runtime:
// absolute slot lateness (t_n − (start + n·P)) for a Go timer on absolute
// deadlines, clock_nanosleep(TIMER_ABSTIME) on a locked OS thread, and the
// same with the thread's timer slack cut to 1 ns. It is the experiment
// behind runtime/sleep_linux.go; run it with "net" as the argument to hold
// a socket open so the Go poller is live, as under naut run.
//
//	go run ./tools/jitter/wakeup
//	go run ./tools/jitter/wakeup net
package main

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"syscall"
	"time"
	"unsafe"
)

const P = time.Millisecond

func report(name string, late []float64, missed int, n int) {
	sort.Float64s(late)
	q := func(p float64) float64 { return late[int(p*float64(len(late)-1))] }
	fmt.Printf("%-34s scans %5d (missed %4d) | slot lateness µs: p50 %6.1f p90 %6.1f p99 %7.1f p99.9 %7.1f max %8.1f\n",
		name, n, missed, q(.5), q(.9), q(.99), q(.999), late[len(late)-1])
}

// Go timer, absolute deadlines, catch-up within a period, skip beyond.
func goTimer(dur time.Duration) {
	start := time.Now()
	tm := time.NewTimer(time.Hour)
	var late []float64
	missed := 0
	for n := int64(1); ; n++ {
		next := start.Add(time.Duration(n) * P)
		w := time.Until(next)
		if w < 0 {
			if k := int64(-w / P); k > 0 {
				n += k
				missed += int(k)
				next = start.Add(time.Duration(n) * P)
				w = time.Until(next)
			}
		}
		if w > 0 {
			tm.Reset(w)
			<-tm.C
		}
		now := time.Now()
		if now.Sub(start) > dur {
			break
		}
		late = append(late, float64(now.Sub(next))/1e3)
	}
	report("Go Timer + abs deadline", late, missed, len(late))
}

// clock_nanosleep(CLOCK_MONOTONIC, TIMER_ABSTIME) on a locked thread.
func monotonicNow() int64 {
	var ts syscall.Timespec
	syscall.Syscall(syscall.SYS_CLOCK_GETTIME, 1 /*CLOCK_MONOTONIC*/, uintptr(unsafe.Pointer(&ts)), 0)
	return ts.Nano()
}
func sleepUntil(ts syscall.Timespec) {
	for {
		_, _, e := syscall.Syscall6(syscall.SYS_CLOCK_NANOSLEEP, 1, 1 /*TIMER_ABSTIME*/, uintptr(unsafe.Pointer(&ts)), 0, 0, 0)
		if e != syscall.EINTR {
			return
		}
	}
}
func nanosleep(dur time.Duration, slack bool) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if slack {
		// PR_SET_TIMERSLACK = 29: 1 ns for this thread (default is 50 µs).
		syscall.Syscall(syscall.SYS_PRCTL, 29, 1, 0)
	}
	start := monotonicNow()
	var late []float64
	missed := 0
	for n := int64(1); ; n++ {
		next := start + n*int64(P)
		now := monotonicNow()
		if now > next {
			if k := (now - next) / int64(P); k > 0 {
				n += k
				missed += int(k)
				next = start + n*int64(P)
			}
		}
		if now < next {
			sleepUntil(syscall.NsecToTimespec(next))
		}
		now = monotonicNow()
		if now-start > int64(dur) {
			break
		}
		late = append(late, float64(now-next)/1e3)
	}
	name := "clock_nanosleep ABSTIME, locked"
	if slack {
		name += ", slack 1ns"
	}
	report(name, late, missed, len(late))
}

func main() {
	dur := 5 * time.Second
	if len(os.Args) > 1 && os.Args[1] == "net" {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		defer ln.Close()
		fmt.Println("poller initialised")
	}
	goTimer(dur)
	nanosleep(dur, false)
	nanosleep(dur, true)
}
