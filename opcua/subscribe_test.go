package opcua

import (
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

func TestSubscribeFollowsChangesWithoutPolling(t *testing.T) {
	ts := startServer(t, freePort(t))
	d := startDriver(t, ts.url, manifest(2), subscribed()...)
	waitFor(t, "online", d.Online)

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
	waitFor(t, "online", d.Online)
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
	waitFor(t, "online", d.Online)

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

func TestSubscriptionCounts(t *testing.T) {
	for _, c := range []struct {
		rate                time.Duration
		keepAlive, lifetime uint32
	}{
		{200 * time.Millisecond, 50, 300}, // keep-alive every 10 s, orphan gone after 60 s
		{time.Second, 10, 60},
		{30 * time.Second, 1, 3}, // lifetime is never under three keep-alives
		{2 * time.Minute, 1, 3},
	} {
		k, l := subscriptionCounts(c.rate)
		if k != c.keepAlive || l != c.lifetime {
			t.Errorf("rate %v: keep-alive %d lifetime %d, want %d %d", c.rate, k, l, c.keepAlive, c.lifetime)
		}
	}
}
