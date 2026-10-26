//go:build linux

package optommp

// rt_linux.go is the driver's real-time thread (manifest priority:): the
// loop locked to one OS thread at SCHED_FIFO, sleeping with
// clock_nanosleep and talking to the processor with blocking socket calls
// made on that thread. Go's network poller and timers would otherwise wake
// the driver through a normal-priority thread, which is exactly what a
// loaded CPU delays. The scheduling calls are the runtime's own
// (runtime/sched_linux.go, runtime/sleep_linux.go), unexported there.

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	goruntime "runtime"
	"syscall"
	"time"
	"unsafe"
)

const (
	clockMonotonic  = 1
	timerAbstime    = 1
	prSetTimerslack = 29
	timerSlackNs    = 1000
	schedFIFO       = 1
)

// rtThread locks the calling goroutine to its thread for good, cuts the
// thread's timer slack to 1 µs and moves it to SCHED_FIFO at prio. The
// lock is never undone: when the goroutine returns, Go ends the thread
// rather than handing it back to the pool at real-time priority. A refusal
// leaves the thread locked at normal priority and is returned.
func rtThread(prio int) error {
	goruntime.LockOSThread()
	_, _, _ = syscall.Syscall(syscall.SYS_PRCTL, prSetTimerslack, timerSlackNs, 0)
	param := struct{ prio int32 }{int32(prio)}
	if _, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_SETSCHEDULER, 0, schedFIFO, uintptr(unsafe.Pointer(&param))); errno != 0 {
		if errors.Is(errno, syscall.EPERM) {
			return fmt.Errorf("SCHED_FIFO priority %d: %w — needs CAP_SYS_NICE (setcap cap_sys_nice+ep on the binary, or run as root) or an rtprio rlimit ≥ %d; the driver runs at normal priority", prio, errno, prio)
		}
		return fmt.Errorf("SCHED_FIFO priority %d: %w", prio, errno)
	}
	return nil
}

func monotonicNs() int64 {
	var ts syscall.Timespec
	_, _, _ = syscall.Syscall(syscall.SYS_CLOCK_GETTIME, clockMonotonic, uintptr(unsafe.Pointer(&ts)), 0)
	return ts.Nano()
}

// nap sleeps d in the kernel against an absolute CLOCK_MONOTONIC deadline.
func nap(d time.Duration) {
	ts := syscall.NsecToTimespec(monotonicNs() + int64(d))
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_CLOCK_NANOSLEEP, clockMonotonic, timerAbstime,
			uintptr(unsafe.Pointer(&ts)), 0, 0, 0)
		if errno != syscall.EINTR {
			return
		}
	}
}

// blockingFd duplicates c's descriptor and puts it in blocking mode. The
// mode is shared with the original descriptor, so the original must only
// be closed after this, never read or written. ok is false for a conn
// with no descriptor (a test's net.Pipe), which is then used as it is.
func blockingFd(c any) (fd int, ok bool) {
	sc, isSC := c.(syscall.Conn)
	if !isSC {
		return -1, false
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return -1, false
	}
	var derr error
	if err := rc.Control(func(s uintptr) { fd, derr = syscall.Dup(int(s)) }); err != nil || derr != nil {
		return -1, false
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		syscall.Close(fd)
		return -1, false
	}
	return fd, true
}

// fdConn is a TCP connection read and written with blocking syscalls on
// the calling thread; deadlines become SO_RCVTIMEO / SO_SNDTIMEO.
type fdConn struct {
	net.Conn // the original: addresses, and closed with this
	fd       int
}

// blockingConn returns c as an fdConn, or c itself when it has no
// descriptor.
func blockingConn(c net.Conn) net.Conn {
	fd, ok := blockingFd(c)
	if !ok {
		return c
	}
	return &fdConn{Conn: c, fd: fd}
}

func (c *fdConn) Read(b []byte) (int, error) {
	for {
		n, err := syscall.Read(c.fd, b)
		switch {
		case err == syscall.EINTR:
			continue
		case err == syscall.EAGAIN:
			return 0, os.ErrDeadlineExceeded
		case err != nil:
			return 0, err
		case n == 0 && len(b) > 0:
			return 0, io.EOF
		}
		return n, nil
	}
}

func (c *fdConn) Write(b []byte) (int, error) {
	done := 0
	for done < len(b) {
		n, err := syscall.Write(c.fd, b[done:])
		switch {
		case err == syscall.EINTR:
			continue
		case err == syscall.EAGAIN:
			return done, os.ErrDeadlineExceeded
		case err != nil:
			return done, err
		}
		done += n
	}
	return done, nil
}

func (c *fdConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *fdConn) SetReadDeadline(t time.Time) error {
	return syscall.SetsockoptTimeval(c.fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, timeval(t))
}

func (c *fdConn) SetWriteDeadline(t time.Time) error {
	return syscall.SetsockoptTimeval(c.fd, syscall.SOL_SOCKET, syscall.SO_SNDTIMEO, timeval(t))
}

func (c *fdConn) Close() error {
	syscall.Close(c.fd)
	return c.Conn.Close()
}

// timeval is a deadline as a socket timeout; zero means none, and one
// already past waits a microsecond.
func timeval(t time.Time) *syscall.Timeval {
	if t.IsZero() {
		return &syscall.Timeval{}
	}
	d := max(time.Until(t), time.Microsecond)
	tv := syscall.NsecToTimeval(int64(d))
	return &tv
}

// udpReceiver reads datagrams off uc with blocking recvfrom on the calling
// thread, waiting at most wait for one: (0, nil, nil) when none came.
// ok is false when uc has no descriptor to borrow.
func udpReceiver(uc *net.UDPConn, wait time.Duration) (recv func([]byte) (int, net.IP, error), closeFn func(), ok bool) {
	fd, ok := blockingFd(uc)
	if !ok {
		return nil, nil, false
	}
	tv := syscall.NsecToTimeval(int64(wait))
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		syscall.Close(fd)
		return nil, nil, false
	}
	recv = func(b []byte) (int, net.IP, error) {
		for {
			n, from, err := syscall.Recvfrom(fd, b, 0)
			switch {
			case err == syscall.EINTR:
				continue
			case err == syscall.EAGAIN:
				return 0, nil, nil
			case err != nil:
				return 0, nil, err
			}
			var ip net.IP
			if a, ok := from.(*syscall.SockaddrInet4); ok {
				ip = net.IP(a.Addr[:])
			}
			return n, ip, nil
		}
	}
	return recv, func() { syscall.Close(fd) }, true
}
