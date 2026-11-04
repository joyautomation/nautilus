package host

// Rebirth requests (#553). Sparkplug 3.0 §5 lets any host application send
// NCMD Node Control/Rebirth, primary or not, and recommends making it
// configurable. So a passive host (primary: false) asks for the births it
// missed like a primary one, and rebirth-requests: false
// (Config.NoRebirthRequests) stops every request either kind sends.

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"

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

// The operator's <site>__Rebirth is a rebirth request, so a passive host
// sends it (#576) unless rebirth requests are off. Every other write stays
// refused: a passive host sends no NCMD/DCMD carrying a tag value.
func TestRebirthRequestsPassiveRebirthTag(t *testing.T) {
	for i, requests := range []bool{true, false} {
		t.Run(fmt.Sprintf("requests=%v", requests), func(t *testing.T) {
			srv, addr := startBroker(t, "")
			t.Cleanup(func() { _ = closeBroker(srv) })

			cfg := rebirthCase{primary: false, requests: requests}.config(addr, fmt.Sprintf("h-rr-tag-%d", i))
			cfg.NoRebirthOnStart = true // only the operator asks
			d := startDriver(t, cfg)
			waitConnected(t, d)
			birthW6AndPLC1(t, d, edgePublisher(t, addr, fmt.Sprintf("edge-rr-tag-%d", i)), 1)
			obs := newObserver(t, addr, fmt.Sprintf("obs-rr-tag-%d", i), "spBv1.0/G/NCMD/#", "spBv1.0/G/DCMD/#")
			baselineScan(t, d)

			// A tag write: refused, nothing on the wire.
			if err := d.WriteOutputs(outputSnapshot(d, map[string]any{"W6_PLC1_Pump_SpeedSP": 42.5})); err != nil {
				t.Fatalf("WriteOutputs: %v", err)
			}
			time.Sleep(requestWindow)
			if n := seen(obs); n != 0 {
				t.Fatalf("a passive host sent %d command(s) for a tag write; want none", n)
			}

			// The operator's rising edge on __Rebirth.
			if err := d.WriteOutputs(outputSnapshot(d, map[string]any{
				"W6_PLC1_Pump_SpeedSP": 42.5, "W6__Rebirth": true})); err != nil {
				t.Fatalf("WriteOutputs: %v", err)
			}
			expectRebirth(t, obs, 0, requests, "on the operator's __Rebirth")
			time.Sleep(requestWindow)
			if requests {
				if n := seen(obs); n != 1 {
					t.Fatalf("passive host sent %d command(s); want only the one NCMD Rebirth", n)
				}
			}
		})
	}
}

// A node that is online but waiting for a birth (an unfilled sequence gap
// unprimed it, or it sent an alias its birth never defined) has data that
// may be frozen, so it reads Stale, not Good, until the next NBIRTH. With
// rebirth-requests: false that wait has no end of its own, and the node's
// later data keeps it from going stale by silence.
func TestWaitingForABirthReadsStale(t *testing.T) {
	for _, requests := range []bool{true, false} {
		t.Run(fmt.Sprintf("gap/requests=%v", requests), func(t *testing.T) {
			d, _ := newTestDriver(t, testManifest(), Config{NoRebirthRequests: !requests,
				ReorderTimeout: 20 * time.Millisecond, StaleAfter: time.Minute})
			d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Level", 1))))
			if q := d.Quality()["W6_Well_Level"]; q != nio.Good {
				t.Fatalf("after the birth Quality = %v, want Good", q)
			}
			d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(2, dbl("Well/Level", 2)))) // seq 1 lost
			time.Sleep(100 * time.Millisecond)                                           // the gap expires
			for seq := uint64(3); seq < 50; seq++ {
				d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(seq, dbl("Well/Level", float64(seq)))))
			}
			d.sweepStale(time.Now().Add(30 * time.Second))
			vals := d.snapshot()
			if vals["W6__Online"] != true || vals["W6_Well_Level"] != 1.0 {
				t.Fatalf("W6__Online = %#v, W6_Well_Level = %#v; want true and the birth's 1",
					vals["W6__Online"], vals["W6_Well_Level"])
			}
			if q := d.Quality()["W6_Well_Level"]; q != nio.Stale {
				t.Errorf("waiting for a birth: Quality = %v, want Stale", q)
			}

			d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(2, dbl("Well/Level", 7))))
			if q := d.Quality()["W6_Well_Level"]; q != nio.Good {
				t.Errorf("after the next birth Quality = %v, want Good", q)
			}
		})
		t.Run(fmt.Sprintf("alias/requests=%v", requests), func(t *testing.T) {
			d, _ := newTestDriver(t, testManifest(), Config{NoRebirthRequests: !requests})
			d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Level", 1))))
			d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(1,
				sparkplug.Metric{Alias: 77, Datatype: spb.DataType_Double, Value: 5.0})))
			if q := d.Quality()["W6_Well_Level"]; q != nio.Stale {
				t.Errorf("after an unknown alias Quality = %v, want Stale", q)
			}
			d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(2, dbl("Well/Level", 7))))
			if q := d.Quality()["W6_Well_Level"]; q != nio.Good {
				t.Errorf("after the next birth Quality = %v, want Good", q)
			}
		})
	}
}

// With rebirth requests off, the wait for a birth is logged once per wait,
// not once per process: the next birth re-arms it. Nothing claims a
// request was sent.
func TestRebirthRequestsOffLogsOncePerWait(t *testing.T) {
	var buf syncBuffer
	d, _ := newTestDriver(t, testManifest(), Config{NoRebirthRequests: true, ReorderTimeout: 20 * time.Millisecond})
	d.log = slog.New(slog.NewTextHandler(&buf, nil))

	gap := func(bd int64) {
		t.Helper()
		d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(bd, dbl("Well/Level", 1))))
		d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(2, dbl("Well/Level", 2)))) // seq 1 lost
		time.Sleep(100 * time.Millisecond)
		for seq := uint64(3); seq < 6; seq++ {
			d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(seq, dbl("Well/Level", 3))))
		}
	}
	gap(1)
	if n := strings.Count(buf.String(), "waiting for a birth"); n != 1 {
		t.Fatalf("first wait logged %d times; want 1\n%s", n, buf.String())
	}
	gap(2)
	if n := strings.Count(buf.String(), "waiting for a birth"); n != 2 {
		t.Fatalf("second wait: %d lines in total; want 2\n%s", n, buf.String())
	}
	if strings.Contains(buf.String(), "requesting rebirth") {
		t.Errorf("a host with rebirth requests off logged a request:\n%s", buf.String())
	}
}

// syncBuffer is a bytes.Buffer safe for the gap timer's goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
