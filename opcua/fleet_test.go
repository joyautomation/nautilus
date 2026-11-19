package opcua

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// #704's "many sessions at once": sixteen servers at a 200 ms scan, one
// driver each, all power-cycled together, each coming back on its own.
// One driver's reconnect must not wait for, or be hidden by, another's:
// every driver goes offline with its values Stale, and every one returns.
func TestSixteenServersReconnectEachOnItsOwn(t *testing.T) {
	for _, mode := range []string{ReadModePoll, ReadModeSubscribe} {
		t.Run(mode, func(t *testing.T) {
			const n = 16
			ports := make([]int, n)
			servers := make([]*testServer, n)
			drivers := make([]*Driver, n)
			for i := range n {
				ports[i] = freePort(t)
				servers[i] = startServer(t, ports[i])
				d, err := New(servers[i].url, manifest(2), fastOpts(WithName(fmt.Sprintf("plant%02d", i)), WithScanRate(200*time.Millisecond), WithReadMode(mode))...)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				d.Start(ctx)
				t.Cleanup(func() { cancel(); d.Stop() })
				drivers[i] = d
			}
			all := func(what string, cond func(*Driver) bool) {
				t.Helper()
				waitFor(t, what, func() bool {
					for _, d := range drivers {
						if !cond(d) {
							return false
						}
					}
					return true
				})
			}
			// Teardown at once, drivers first: the per-server cleanups would
			// otherwise close the servers one after another, up to 10 s each.
			defer func() {
				for _, d := range drivers {
					d.Stop()
				}
				var closing sync.WaitGroup
				for _, s := range servers {
					closing.Go(func() { _ = s.srv.Close() })
				}
				closing.Wait()
			}()
			all("every driver online", func(d *Driver) bool { return d.Online() })

			cycles := 2
			if raceOn {
				cycles = 1 // sixteen sessions under the race detector: one power cycle
			}
			for cycle := 1; cycle <= cycles; cycle++ {
				// gopcua's Close waits up to 10 s for a server's channels to
				// exit, so the sixteen are closed at once, not in turn.
				var closing sync.WaitGroup
				for _, s := range servers {
					closing.Go(func() { _ = s.srv.Close() })
				}
				closing.Wait()
				all(fmt.Sprintf("cycle %d: every driver offline", cycle), func(d *Driver) bool { return !d.Online() })
				for i, d := range drivers {
					if q := d.Quality()["Speed"]; q != nio.Stale {
						t.Errorf("cycle %d: driver %d Speed quality %v while down, want stale", cycle, i, q)
					}
				}
				// Fifteen come back first. Each must recover on its own while the
				// sixteenth, whose server is still down, stays offline with its
				// values Stale: a recovery that waited for, or was hidden by,
				// the last one would show here.
				last := n - 1
				for i := range last {
					servers[i] = startServer(t, ports[i])
				}
				waitFor(t, fmt.Sprintf("cycle %d: the first %d drivers back with Speed good", cycle, last), func() bool {
					for _, d := range drivers[:last] {
						if !d.Online() || read(t, d)["Speed"] != 12.5 {
							return false
						}
					}
					return true
				})
				time.Sleep(500 * time.Millisecond) // the sixteenth has had retries to wrongly come back
				if drivers[last].Online() {
					t.Errorf("cycle %d: driver %d is online with its server down", cycle, last)
				}
				if q := drivers[last].Quality()["Speed"]; q != nio.Stale {
					t.Errorf("cycle %d: driver %d Speed quality %v while its server is down, want stale", cycle, last, q)
				}
				servers[last] = startServer(t, ports[last])
				all(fmt.Sprintf("cycle %d: every driver back online", cycle), func(d *Driver) bool { return d.Online() })
				all(fmt.Sprintf("cycle %d: every Speed good again", cycle), func(d *Driver) bool {
					return read(t, d)["Speed"] == 12.5
				})
			}
		})
	}
}
