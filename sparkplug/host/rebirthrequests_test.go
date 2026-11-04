package host

// Rebirth requests (#553). Sparkplug 3.0 §5 lets any host application send
// NCMD Node Control/Rebirth, primary or not, and recommends making it
// configurable. So a passive host (primary: false) asks for the births it
// missed like a primary one, and rebirth-requests: false
// (Config.NoRebirthRequests) stops every request either kind sends.

import (
	"fmt"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/internal/linkcut"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// requestWindow is how long a "no request" check listens. A request the host
// does send lands well inside it: the positive cases below wait on the same
// paths.
const requestWindow = 500 * time.Millisecond

func isRebirthNCMD(m recMsg) bool {
	if m.topic != "spBv1.0/G/NCMD/W6" {
		return false
	}
	p, err := sparkplug.DecodePayload(m.payload)
	if err != nil || len(p.Metrics) != 1 {
		return false
	}
	return p.Metrics[0].Name == RebirthMetric && p.Metrics[0].Value == true
}

// expectRebirth waits for an NCMD Rebirth to W6 after the observer's first
// `after` messages, or checks no NCMD at all follows them in requestWindow.
func expectRebirth(t *testing.T, obs *observer, after int, want bool, what string) {
	t.Helper()
	if want {
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			obs.mu.Lock()
			for _, m := range obs.msgs[after:] {
				if isRebirthNCMD(m) {
					obs.mu.Unlock()
					return
				}
			}
			obs.mu.Unlock()
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("%s: no NCMD Rebirth to W6", what)
	}
	time.Sleep(requestWindow)
	if n := obs.count(func(recMsg) bool { return true }) - after; n != 0 {
		t.Fatalf("%s: host published %d NCMD message(s); want none with rebirth-requests: false", what, n)
	}
}

func seen(obs *observer) int { return obs.count(func(recMsg) bool { return true }) }

type rebirthCase struct {
	primary  bool
	requests bool
}

func (c rebirthCase) name() string {
	return fmt.Sprintf("primary=%v/requests=%v", c.primary, c.requests)
}

func (c rebirthCase) config(addr, hostID string) Config {
	cfg := testConfig(addr, hostID)
	cfg.Primary = c.primary
	cfg.NoRebirthRequests = !c.requests
	return cfg
}

var rebirthCases = []rebirthCase{
	{primary: true, requests: true},
	{primary: false, requests: true},
	{primary: true, requests: false},
	{primary: false, requests: false},
}

// On connect a host asks every site to rebirth, passive or primary, unless
// rebirth requests are off.
func TestRebirthRequestsOnStart(t *testing.T) {
	for i, c := range rebirthCases {
		t.Run(c.name(), func(t *testing.T) {
			srv, addr := startBroker(t, "")
			t.Cleanup(func() { _ = closeBroker(srv) })
			obs := newObserver(t, addr, fmt.Sprintf("obs-rr-start-%d", i), "spBv1.0/G/NCMD/#")

			d := startDriver(t, c.config(addr, fmt.Sprintf("h-rr-start-%d", i)))
			waitConnected(t, d)
			expectRebirth(t, obs, 0, c.requests, "on start")
		})
	}
}

// Data before a birth asks for one, passive or primary, unless rebirth
// requests are off. With them off the host still goes online on a birth it
// did not ask for.
func TestRebirthRequestsOnDataBeforeBirth(t *testing.T) {
	for i, c := range rebirthCases {
		t.Run(c.name(), func(t *testing.T) {
			srv, addr := startBroker(t, "")
			t.Cleanup(func() { _ = closeBroker(srv) })

			cfg := c.config(addr, fmt.Sprintf("h-rr-data-%d", i))
			cfg.NoRebirthOnStart = true // only the data-before-birth path asks
			d := startDriver(t, cfg)
			waitConnected(t, d)
			obs := newObserver(t, addr, fmt.Sprintf("obs-rr-data-%d", i), "spBv1.0/G/NCMD/#")

			edge := edgePublisher(t, addr, fmt.Sprintf("edge-rr-data-%d", i))
			publishPayload(t, edge, "spBv1.0/G/NDATA/W6", sparkplug.Payload{
				Timestamp: uint64(time.Now().UnixMilli()), Seq: 5,
				Metrics: []sparkplug.Metric{dbl("Well/Level", 9)},
			})
			expectRebirth(t, obs, 0, c.requests, "on data before a birth")

			// The birth nobody (or somebody else) asked for is adopted.
			birthW6AndPLC1(t, d, edge, 1)
			vals := waitForValue(t, d, "W6__Online", func(v any) bool { return v == true })
			if vals["W6_Well_Level"] != 4.2 {
				t.Errorf("W6_Well_Level = %#v after the birth, want 4.2", vals["W6_Well_Level"])
			}
		})
	}
}

// On reconnect a host asks every site to rebirth, passive or primary, unless
// rebirth requests are off. Either way the broker loss takes the site offline
// and counts one outage, and the next birth brings it back.
func TestRebirthRequestsOnReconnect(t *testing.T) {
	for i, c := range rebirthCases {
		t.Run(c.name(), func(t *testing.T) {
			srv, addr := startBroker(t, "")
			t.Cleanup(func() { _ = closeBroker(srv) })
			px := linkcut.New(t, addr) // only the host goes through the cut link
			obs := newObserver(t, addr, fmt.Sprintf("obs-rr-reconnect-%d", i), "spBv1.0/G/NCMD/#")

			d := startDriver(t, c.config(px.Addr(), fmt.Sprintf("h-rr-reconnect-%d", i)))
			waitConnected(t, d)
			edge := edgePublisher(t, addr, fmt.Sprintf("edge-rr-reconnect-%d", i))
			birthW6AndPLC1(t, d, edge, 1)

			px.Cut()
			waitDisconnected(t, d)
			if vals := d.snapshot(); vals["W6__Online"] != false || vals["W6__Outages"] != int64(1) {
				t.Fatalf("broker lost: W6__Online = %#v, W6__Outages = %#v, want false and 1",
					vals["W6__Online"], vals["W6__Outages"])
			}

			before := seen(obs)
			px.Restore()
			waitConnected(t, d)
			expectRebirth(t, obs, before, c.requests, "on reconnect")

			birthW6AndPLC1(t, d, edge, 2)
			if vals := d.snapshot(); vals["W6__Online"] != true || vals["W6__Outages"] != int64(1) {
				t.Errorf("after the birth: W6__Online = %#v, W6__Outages = %#v, want true and still 1",
					vals["W6__Online"], vals["W6__Outages"])
			}
		})
	}
}

// With rebirth requests off the state machine's own reasons to ask (a
// sequence gap, an unknown alias, a stale node) ask for nothing and count
// nothing, and the operator's RequestRebirth is refused.
func TestRebirthRequestsOffAsksForNothing(t *testing.T) {
	d, rl := newTestDriver(t, testManifest(), Config{
		NoRebirthRequests: true, ReorderTimeout: 20 * time.Millisecond, StaleAfter: time.Millisecond})
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Level", 1))))

	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(2, dbl("Well/Level", 99)))) // seq 1 lost
	time.Sleep(100 * time.Millisecond)                                            // the gap expires
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(2, dbl("Well/Level", 1))))
	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(1, sparkplug.Metric{Alias: 77, Datatype: spb.DataType_Double, Value: 1.0})))
	d.sweepStale(time.Now().Add(time.Hour))
	rl.none(t, 100*time.Millisecond)

	d.mu.Lock()
	gaps, rebirths := d.stats.SeqGaps, d.stats.Rebirths
	d.mu.Unlock()
	if gaps != 1 || rebirths != 0 {
		t.Errorf("SeqGaps=%d Rebirths=%d, want 1/0 (a gap is still counted, no request is)", gaps, rebirths)
	}
	d.log = quietLogger()
	if err := d.RequestRebirth("W6"); err == nil {
		t.Error("RequestRebirth must be refused with rebirth requests off")
	}
}
