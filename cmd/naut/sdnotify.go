package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/leader"
)

// systemd's service notification protocol (sd_notify(3)), for a controller
// run as a Type=notify unit:
//
//   - READY=1 once the controller is up: its first scan has completed, or,
//     as a redundant replica, it has decided it is a standby. A restart or
//     an update therefore returns from `systemctl restart` only when the
//     new process scans, and a release that cannot start fails the restart.
//   - WATCHDOG=1 at half the unit's WatchdogSec=, and only while the scans
//     are live: the same liveness verdict the write gate and the
//     redundancy lease use (#621, #625), taken from the elector's own
//     checks rather than asked a second time. A scan that hangs stops the
//     heartbeat, and systemd kills and restarts the process.
//   - STOPPING=1 when an orderly stop begins.
//
// Without NOTIFY_SOCKET (a terminal, a container without systemd) none of
// this runs.

// notifier writes to systemd's notification socket.
type notifier struct {
	addr *net.UnixAddr
}

// newNotifier returns a notifier for $NOTIFY_SOCKET, or nil when the
// process was not started by systemd with notification on. An address
// starting with '@' is in the abstract namespace.
func newNotifier() *notifier {
	path := os.Getenv("NOTIFY_SOCKET")
	if path == "" {
		return nil
	}
	if path[0] == '@' {
		path = "\x00" + path[1:]
	}
	return &notifier{addr: &net.UnixAddr{Name: path, Net: "unixgram"}}
}

func (n *notifier) send(state string) error {
	c, err := net.DialUnix("unixgram", nil, n.addr)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write([]byte(state))
	return err
}

// watchdogInterval is systemd's WatchdogSec= for this process, from
// WATCHDOG_USEC, or 0 when the unit has none (or WATCHDOG_PID names
// another process, as it may for a child that inherited the environment).
func watchdogInterval() time.Duration {
	us, err := strconv.ParseInt(os.Getenv("WATCHDOG_USEC"), 10, 64)
	if err != nil || us <= 0 {
		return 0
	}
	if p := os.Getenv("WATCHDOG_PID"); p != "" && p != strconv.Itoa(os.Getpid()) {
		return 0
	}
	return time.Duration(us) * time.Microsecond
}

// liveWatch keeps the last liveness verdict the elector took, so the
// heartbeat reflects the same checks the write gate does. A verdict older
// than stale counts as not live: an elector that has stopped checking
// (its goroutine starved, or blocked) must not keep the heartbeat going.
type liveWatch struct {
	mu    sync.Mutex
	at    time.Time // when the last verdict was taken
	why   string    // "" while live
	stale time.Duration
}

// wrap returns h with every verdict recorded.
func (w *liveWatch) wrap(h leader.Health) leader.Health {
	return func(now time.Time, max time.Duration) error {
		err := h(now, max)
		w.mu.Lock()
		w.at, w.why = now, ""
		if err != nil {
			w.why = err.Error()
		}
		w.mu.Unlock()
		return err
	}
}

// live reports whether the last verdict was live and recent at now, and
// why not when it was not.
func (w *liveWatch) live(now time.Time) (bool, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.at.IsZero():
		return false, "no liveness check has run yet"
	case now.Sub(w.at) > w.stale:
		return false, fmt.Sprintf("no liveness check for %s", now.Sub(w.at).Round(time.Second))
	case w.why != "":
		return false, w.why
	}
	return true, ""
}

// minWatchdog is the shortest WatchdogSec= naut runs without a warning.
// A scan counts as stuck after runtime.StallBound (1 to 2 s with the 4 s
// lease), found on the elector's next check (every second), and the
// heartbeat goes quiet at the next beat: about 2 to 4 s from the start of
// the scan. Twice the lease leaves room for one slow scan, a busy
// machine's late checks and a beat that just missed, so that a scan that
// overruns but finishes does not get a healthy controller killed.
const minWatchdog = 2 * localGateLease

// systemdNotify runs the notification protocol for one controller until
// ctx is done, then sends STOPPING=1. ready reports whether the controller
// is up (READY=1); it is polled until it first answers true. Without
// NOTIFY_SOCKET it returns at once and does nothing.
func systemdNotify(ctx context.Context, n *notifier, w *liveWatch, ready func() bool) {
	if n == nil {
		return
	}
	poll := time.NewTicker(50 * time.Millisecond)
	for !ready() {
		select {
		case <-ctx.Done():
			poll.Stop()
			n.send("STOPPING=1")
			return
		case <-poll.C:
		}
	}
	poll.Stop()
	if err := n.send("READY=1\nSTATUS=scanning"); err != nil {
		slog.Error("systemd: READY=1 not delivered", "err", err)
	}

	interval := watchdogInterval()
	if interval == 0 {
		<-ctx.Done()
		n.send("STOPPING=1")
		return
	}
	if interval < minWatchdog {
		slog.Warn("systemd: WatchdogSec is shorter than a hung scan takes to detect; a slow scan may be killed", "watchdog", interval, "minimum", minWatchdog)
	}
	beat := time.NewTicker(interval / 2)
	defer beat.Stop()
	quiet := false
	for {
		select {
		case <-ctx.Done():
			n.send("STOPPING=1")
			return
		case now := <-beat.C:
			ok, why := w.live(now)
			switch {
			case ok:
				n.send("WATCHDOG=1")
				if quiet {
					quiet = false
					n.send("STATUS=scanning")
					slog.Info("systemd: scans are live again; watchdog heartbeat resumed")
				}
			case !quiet:
				quiet = true
				n.send("STATUS=scans not live: " + why)
				slog.Error("systemd: scans are not live; watchdog heartbeat withheld, systemd restarts the controller when WatchdogSec runs out", "reason", why, "watchdog", interval)
			}
		}
	}
}
