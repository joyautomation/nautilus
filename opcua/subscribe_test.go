package opcua

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// The subscription read mode against gopcua's in-process server. The same
// scenarios run against open62541 and Milo in the server matrix
// (matrix_test.go); these run everywhere, with no docker.

func subscribed(extra ...Option) []Option {
	return append([]Option{WithReadMode(ReadModeSubscribe)}, extra...)
}

// settled waits until a subscribed driver against gopcua's server is on
// its lasting session. That server has no SetPublishingMode: the first
// probe's refusal stops gopcua's publish loop, so the first session is
// given up after about a second and the next, probed by a Read, lasts.
// Its own server also races a Write against the initial notification it
// sends from CreateMonitoredItems (a data race inside gopcua's server),
// so tests write only after this.
func settled(t *testing.T, d *Driver) {
	t.Helper()
	waitFor(t, "the lasting subscribed session", func() bool {
		return d.Online() && d.Health().Probe == probeRead
	})
}

func TestSubscribeFollowsChangesWithoutPolling(t *testing.T) {
	ts := startServer(t, freePort(t))
	d := startDriver(t, ts.url, manifest(2), subscribed()...)
	settled(t, d)

	want := nio.Values{"Speed": 12.5, "Count": int64(7), "Run": true, "Name": "pump-1", "Setpoint": 0.0, "plant__Online": true}
	if got := read(t, d); !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot = %v, want %v", got, want)
	}
	if q := d.Quality(); len(q) != 0 {
		t.Errorf("quality on a healthy session = %v, want empty", q)
	}
	primed := d.Health().Reads

	ts.set(t, "Setpoint", 42.5)
	waitFor(t, "the change notified", func() bool { return read(t, d)["Setpoint"] == 42.5 })
	// Several scan periods later, still no polling: values arrive as
	// notifications after the one priming Read per class.
	time.Sleep(300 * time.Millisecond)
	h := d.Health()
	if h.ReadMode != ReadModeSubscribe || h.Subscriptions != 1 || h.Notifications == 0 {
		t.Errorf("health = %+v", h)
	}
	if h.Reads != primed {
		t.Errorf("subscribed driver polled: %d reads after priming", h.Reads-primed)
	}
	if h.RevisedMs["default"] <= 0 {
		t.Errorf("revised publishing interval not recorded: %v", h.RevisedMs)
	}
}

func TestSubscribeBadNodeIsBadNotFatal(t *testing.T) {
	ts := startServer(t, freePort(t))
	m := manifest(2)
	m.Tags = append(m.Tags, TagBinding{Name: "Ghost", NodeID: "ns=2;s=Ghost", Type: "Double"})
	d := startDriver(t, ts.url, m, subscribed()...)
	settled(t, d)
	if q := d.Quality(); q["Ghost"] != nio.Bad || len(q) != 1 {
		t.Errorf("quality = %v, want only Ghost bad", q)
	}
	ts.set(t, "Setpoint", 7.0)
	waitFor(t, "the other tags still followed", func() bool { return read(t, d)["Setpoint"] == 7.0 })
	if h := d.Health(); h.BadTags != 1 || !strings.Contains(h.LastError, "Ghost") {
		t.Errorf("health = %+v", h)
	}
}

// gopcua's server answers SetPublishingMode with Bad_ServiceUnsupported,
// so this also proves the probe falls back to a Read: without a probe a
// subscribed driver never notices the server is gone.
func TestSubscribeReconnectAndStale(t *testing.T) {
	port := freePort(t)
	ts := startServer(t, port)
	d := startDriver(t, ts.url, manifest(2), subscribed()...)
	settled(t, d)

	_ = ts.srv.Close()
	waitFor(t, "offline", func() bool { return read(t, d)["plant__Online"] == false })
	if v := read(t, d)["Speed"]; v != 12.5 {
		t.Errorf("value did not hold across the outage: %v", v)
	}
	if q := d.Quality(); q["Speed"] != nio.Stale {
		t.Errorf("Speed quality during outage = %v, want stale", q["Speed"])
	}
	_ = d.WriteOutputs(nio.Values{"Setpoint": 0.0}) // baseline
	_ = d.WriteOutputs(nio.Values{"Setpoint": 3.0})

	ts2 := startServer(t, port)
	waitFor(t, "reconnect", func() bool { return read(t, d)["plant__Online"] == true })
	if d.Health().Probe != probeRead {
		// Remembered across sessions: no second SetPublishingMode refusal.
		waitFor(t, "the Read probe on the new session", func() bool { return d.Health().Probe == probeRead })
	}
	waitFor(t, "queued write flushed", func() bool { return ts2.get(t, "Setpoint") == 3.0 })
	if q := d.Quality(); len(q) != 0 {
		t.Errorf("quality after reconnect = %v", q)
	}
	// The new session's subscription delivers, not just its prime.
	ts2.set(t, "Setpoint", 4.0)
	waitFor(t, "a change on the new session", func() bool { return read(t, d)["Setpoint"] == 4.0 })
}

func TestReadModeIsChecked(t *testing.T) {
	if _, err := New("opc.tcp://127.0.0.1:4840", manifest(2), WithReadMode("push")); err == nil || !strings.Contains(err.Error(), "read-mode") {
		t.Errorf("New with read-mode push: %v, want a read-mode error", err)
	}
	for _, mode := range []string{"", ReadModePoll, ReadModeSubscribe} {
		if _, err := New("opc.tcp://127.0.0.1:4840", manifest(2), WithReadMode(mode)); err != nil {
			t.Errorf("read-mode %q: %v", mode, err)
		}
	}
}

func TestSubscriptionParams(t *testing.T) {
	for _, c := range []struct {
		rate, timeout       time.Duration
		interval            time.Duration
		keepAlive, lifetime uint32
	}{
		{200 * time.Millisecond, 5 * time.Second, 200 * time.Millisecond, 12, 300},
		{time.Second, 5 * time.Second, time.Second, 2, 60},
		// A slow class is sampled at its rate but published often enough
		// that its keep-alive stays inside the request timeout.
		{time.Minute, 5 * time.Second, 1250 * time.Millisecond, 2, 48},
		{50 * time.Millisecond, 2 * time.Second, 50 * time.Millisecond, 20, 1200},
	} {
		i, k, l := subscriptionParams(c.rate, c.timeout)
		if i != c.interval || k != c.keepAlive || l != c.lifetime {
			t.Errorf("rate %v timeout %v: interval %v keep-alive %d lifetime %d, want %v %d %d", c.rate, c.timeout, i, k, l, c.interval, c.keepAlive, c.lifetime)
		}
	}
	for _, rate := range []time.Duration{10 * time.Millisecond, 200 * time.Millisecond, 3 * time.Second, time.Hour} {
		for _, timeout := range []time.Duration{time.Second, 5 * time.Second, 30 * time.Second} {
			i, k, l := subscriptionParams(rate, timeout)
			if window := time.Duration(k) * i; window > timeout/2 {
				t.Errorf("rate %v timeout %v: keep-alive window %v is over half the timeout", rate, timeout, window)
			}
			if l < 3*k {
				t.Errorf("rate %v timeout %v: lifetime %d under three keep-alives (%d)", rate, timeout, l, k)
			}
		}
	}
}

// A value that sits still across many keep-alives, then changes once, is
// delivered: the keep-alive window is inside the Publish deadline, so no
// Publish is abandoned while the subscription is quiet.
func TestSubscribeQuietValueStillArrives(t *testing.T) {
	ts := startServer(t, freePort(t))
	d := startDriver(t, ts.url, manifest(2), subscribed()...)
	settled(t, d)
	primed := d.Health().Reads
	time.Sleep(4 * time.Second) // four keep-alive windows with a 2 s timeout
	ts.set(t, "Setpoint", 5.5)
	waitFor(t, "the one change after a quiet spell", func() bool { return read(t, d)["Setpoint"] == 5.5 })
	if h := d.Health(); h.Reads != primed {
		t.Errorf("delivered by a read, not a notification: %d reads after priming", h.Reads-primed)
	}
}

// Several scan classes are several subscriptions on one notification
// channel; a slow class sorting first does not hold up the others, and
// monitored items are created in chunks of the server's per-call limit.
func TestSubscribeSeveralClasses(t *testing.T) {
	for _, slowSetpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "change in the fast class", true: "change in the slow class"}[slowSetpoint], func(t *testing.T) {
			ts := startServer(t, freePort(t))
			pattern := "S*" // Speed, Setpoint
			if !slowSetpoint {
				pattern = "Speed"
			}
			t0 := time.Now()
			d := startDriver(t, ts.url, manifest(2), subscribed(
				WithMaxNodes(2), // 5 bindings → chunks of 2 per CreateMonitoredItems
				WithScanClass("aslow", time.Second),
				WithTagClass("aslow", pattern, "Count"),
			)...)
			waitFor(t, "online", d.Online)
			if took := time.Since(t0); took > 3*time.Second {
				t.Errorf("online after %v with a 1 s class first", took)
			}
			settled(t, d)
			if h := d.Health(); h.Subscriptions != 2 || h.Classes != 2 {
				t.Errorf("health = %+v, want two classes, two subscriptions", h)
			}
			if q := d.Quality(); len(q) != 0 {
				t.Errorf("quality = %v", q)
			}
			ts.set(t, "Setpoint", 8.25)
			waitFor(t, "the change", func() bool { return read(t, d)["Setpoint"] == 8.25 })
		})
	}
}

// Each class is also read in full now and then, so a notification lost
// without the session failing costs at most that long.
func TestSubscribeRefreshReads(t *testing.T) {
	ts := startServer(t, freePort(t))
	d, err := New(ts.url, manifest(2), fastOpts(subscribed()...)...)
	if err != nil {
		t.Fatal(err)
	}
	d.refreshFloor = 300 * time.Millisecond // refreshEvery: max(300 ms, 10 × 50 ms) = 500 ms
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	t.Cleanup(func() { cancel(); d.Stop() })
	settled(t, d)
	primed := d.Health().Reads
	waitFor(t, "two refresh reads", func() bool { return d.Health().Reads >= primed+2 })
	if refreshEvery(50*time.Millisecond, defaultRefreshFloor) != 30*time.Second || refreshEvery(10*time.Second, defaultRefreshFloor) != 100*time.Second {
		t.Error("refreshEvery: want max(30 s, ten periods)")
	}
}
