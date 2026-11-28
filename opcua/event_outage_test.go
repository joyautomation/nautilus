package opcua

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"gopkg.in/yaml.v3"
)

// syncLog is a log sink the driver's goroutines may write while the test
// reads it.
type syncLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncLog) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncLog) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// eventManifest is manifest(2) with Start, a writable Boolean node, as an
// event command (event true) or a level one.
func eventManifest(event bool) Manifest {
	m := manifest(2)
	m.Tags = append(m.Tags, TagBinding{Name: "Start", NodeID: "ns=2;s=Start", Namespace: "urn:nautilus:test", Type: "Boolean", Writable: true, Event: event})
	return m
}

// An event command (TagBinding.Event: a struct-wiring `event: true`
// member, #1458) raised while the session is down is dropped when the
// session is re-established, never sent late: a Start pressed while the
// server was unreachable must not fire on the reconnect. The drop is logged
// once, naming the binding, and counted in Health. A level command queued
// the same way (Setpoint, the control) still goes out on the reconnect.
func TestEventCommandQueuedOfflineIsDroppedAtReconnect(t *testing.T) {
	port := freePort(t)
	ts := startServer(t, port)
	logs := &syncLog{}
	d := startDriver(t, ts.url, eventManifest(true), WithLogger(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))))
	waitFor(t, "online", func() bool { return read(t, d)["plant__Online"] == true })
	_ = d.WriteOutputs(nio.Values{"Start": false, "Setpoint": 0.0}) // baseline
	waitFor(t, "nothing queued", func() bool { return d.Health().QueuedWrites == 0 })

	_ = ts.srv.Close()
	waitFor(t, "offline", func() bool { return read(t, d)["plant__Online"] == false })
	_ = d.WriteOutputs(nio.Values{"Start": true, "Setpoint": 5.0}) // raised during the outage
	waitFor(t, "both queued", func() bool { return d.Health().QueuedWrites == 2 })

	ts2 := startServer(t, port) // fresh state: Start FALSE, Setpoint 0
	waitFor(t, "reconnect", func() bool { return read(t, d)["plant__Online"] == true })
	waitFor(t, "the level command queued during the outage goes out", func() bool { return ts2.get(t, "Setpoint") == 5.0 })
	time.Sleep(300 * time.Millisecond) // several flush rounds
	if ts2.get(t, "Start") == true {
		t.Fatal("the event command raised during the outage went out late on the reconnect")
	}
	if got := d.Health().DroppedEvents; got != 1 {
		t.Errorf("DroppedEvents = %d, want 1", got)
	}
	if q := d.Health().QueuedWrites; q != 0 {
		t.Errorf("QueuedWrites = %d after the reconnect, want 0", q)
	}
	if n := strings.Count(logs.String(), "event command dropped"); n != 1 {
		t.Errorf("logged the drop %d times, want once:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "tag=Start") {
		t.Errorf("the drop's log line does not name the binding:\n%s", logs.String())
	}

	// The binding's next change goes out as usual.
	_ = d.WriteOutputs(nio.Values{"Start": false, "Setpoint": 5.0})
	_ = d.WriteOutputs(nio.Values{"Start": true, "Setpoint": 5.0})
	waitFor(t, "the next change of the event command", func() bool { return ts2.get(t, "Start") == true })
}

// Validate refuses event without writable, and event on anything but a
// Boolean: a trigger's release is FALSE, the one value always safe to send
// late.
func TestEventValidate(t *testing.T) {
	m := manifest(2)
	m.Tags[2].Event = true // Run, a read-only Boolean
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "event without writable") {
		t.Errorf("Validate = %v, want event without writable", err)
	}
	m = manifest(2)
	m.Tags[4].Event = true // Setpoint, a writable Double
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "event on a Double binding") {
		t.Errorf("Validate = %v, want event on a Double binding", err)
	}
	if err := eventManifest(true).Validate(); err != nil {
		t.Errorf("a writable Boolean event command: %v", err)
	}
}

// event is no manifest key: struct wiring sets it, and the runtime's event
// rules in front of the driver are what close its other late paths. A
// hand-written manifest naming it is refused, not half-protected, and a
// generated manifest never writes it.
func TestEventIsNoManifestKey(t *testing.T) {
	_, err := ParseManifest([]byte("namespaces: [\"http://opcfoundation.org/UA/\"]\ntags:\n  - {name: S, nodeid: \"ns=0;i=1\", type: Boolean, writable: true, event: true}\n"))
	if err == nil || !strings.Contains(err.Error(), "event") {
		t.Fatalf("ParseManifest = %v, want the event key refused", err)
	}
	raw, err := yaml.Marshal(eventManifest(true))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "event") {
		t.Errorf("a manifest wrote the event key:\n%s", raw)
	}
}

// The release of a trigger is not dropped. An event command that went out
// (TRUE) and was then released (FALSE) while the server could not be
// reached sends the release on the reconnect: dropping it would leave the
// node asserted. And a press and its release both raised during one outage
// send no late press: the queue holds the latest value, the release, and it
// is the one write that goes out. (The restarted server starts FALSE, so the
// write counter, not the node, shows what went out.)
func TestEventReleaseQueuedOfflineStillGoesOut(t *testing.T) {
	for _, tc := range []struct {
		name   string
		outage []bool
	}{
		{"release", []bool{false}},
		{"press and release", []bool{true, false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			port := freePort(t)
			ts := startServer(t, port)
			d := startDriver(t, ts.url, eventManifest(true))
			waitFor(t, "online", func() bool { return read(t, d)["plant__Online"] == true })
			_ = d.WriteOutputs(nio.Values{"Start": false}) // baseline
			_ = d.WriteOutputs(nio.Values{"Start": true})
			waitFor(t, "the press", func() bool { return ts.get(t, "Start") == true })
			if tc.name == "press and release" {
				_ = d.WriteOutputs(nio.Values{"Start": false})
				waitFor(t, "the release", func() bool { return ts.get(t, "Start") == false })
			}

			_ = ts.srv.Close()
			waitFor(t, "offline", func() bool { return read(t, d)["plant__Online"] == false })
			for _, v := range tc.outage {
				_ = d.WriteOutputs(nio.Values{"Start": v})
			}
			waitFor(t, "queued", func() bool { return d.Health().QueuedWrites == 1 })
			before := d.Health().Writes

			ts2 := startServer(t, port)
			waitFor(t, "reconnect", func() bool { return read(t, d)["plant__Online"] == true })
			waitFor(t, "the release goes out", func() bool { return d.Health().Writes == before+1 && d.Health().QueuedWrites == 0 })
			time.Sleep(200 * time.Millisecond)
			if got := d.Health().Writes; got != before+1 {
				t.Errorf("%d writes after the reconnect, want exactly the release", got-before)
			}
			if ts2.get(t, "Start") != false {
				t.Errorf("Start %v after the reconnect, want FALSE (at rest)", ts2.get(t, "Start"))
			}
			if got := d.Health().DroppedEvents; got != 0 {
				t.Errorf("DroppedEvents = %d, want 0: a release is never dropped", got)
			}
		})
	}
}
