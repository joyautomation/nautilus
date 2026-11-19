package main

// SPH-036 end to end (#325): a manifest with `redundancy:` and a
// sparkplug-host driver, loaded by project.Load and wired by the same
// electorFor/wireRedundancy `naut run` uses, against an in-process broker.
// Only the leader of a pair declares itself the primary host (STATE online)
// or sends NCMD/DCMD; a failover moves both; and a manifest without
// `redundancy:` — every live deployment today — is primary from the first
// connect, exactly as before.

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/joyautomation/nautilus/internal/project"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/sparkplug"
	sphost "github.com/joyautomation/nautilus/sparkplug/host"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

const spSetpoint = "Edge1_ZONE1_SP_001"

// hostProject is a central host consuming group Plant, with or without a
// redundancy: section.
func hostProject(url string, redundant bool) fstest.MapFS {
	red := ""
	if redundant {
		red = "redundancy: {}\n"
	}
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`name: central-host
` + red + `tasks:
  - program: main.st
    scan: 100ms
tags:
  - { name: Spare, role: setpoint, init: 0.0 }
driver:
  type: sparkplug-host
  broker: "` + url + `"
  group-id: Plant
  host-id: central
  manifest: sparkplug_manifest.yaml
  rebirth-on-start: false
`)},
		"main.st": &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR_EXTERNAL Spare : REAL; END_VAR
Spare := Spare;
END_PROGRAM
`)},
		"sparkplug_manifest.yaml": &fstest.MapFile{Data: []byte(`group: Plant
nodes:
    - edgenode: Edge1
tags:
    - { name: ` + spSetpoint + `, node: Edge1, device: "", metric: Zone1/SP_001, type: Double, arraylen: 0, writable: true, init: 0.0 }
`)},
	}
}

// startReplica loads the project and wires it the way runProject does, for
// a pod named pod, and starts its driver. coord nil means "electorFor": the
// real elector (standalone off-cluster), or nothing when the manifest has no
// redundancy: section.
func startReplica(t *testing.T, files fstest.MapFS, pod string, coord *flagCoordinator) (*project.Project, *sphost.Driver) {
	t.Helper()
	t.Setenv("POD_NAME", pod)
	proj, err := project.Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	if coord != nil {
		if proj.Redundancy == nil {
			t.Fatal("the manifest's redundancy: section did not reach the project")
		}
		wireRedundancy(proj, coord)
	} else if _, _, err := electorFor(proj, "127.0.0.1:0", ""); err != nil {
		t.Fatal(err)
	}
	drv, ok := proj.Runtime.Driver.(*sphost.Driver)
	if !ok {
		t.Fatalf("driver = %T, want *host.Driver", proj.Runtime.Driver)
	}
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	waitTrue(t, pod+" to connect", func() bool { return drv.Status().Connected })
	return proj, drv
}

// flagCoordinator stands in for a cluster elector (which needs a Kubernetes
// API server): the test decides who leads.
type flagCoordinator struct{ lead atomic.Bool }

func (f *flagCoordinator) IsLeader() bool { return f.lead.Load() }

func waitTrue(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// hostWatch records the central host's STATE certificates and the NCMDs to
// Edge1, in arrival order. It also stands in for Edge1 (births it).
type hostWatch struct {
	cli mqtt.Client
	mu  sync.Mutex
	st  []struct {
		Online    bool
		Timestamp int64
	}
	cmds []float64
}

func watchCentral(t *testing.T, url string) *hostWatch {
	t.Helper()
	w := &hostWatch{}
	w.cli = mqtt.NewClient(mqtt.NewClientOptions().AddBroker(url).SetClientID("edge1-sim").
		SetCleanSession(true).SetOrderMatters(true).
		SetDefaultPublishHandler(func(_ mqtt.Client, m mqtt.Message) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if m.Topic() == "spBv1.0/STATE/central" {
				var s struct {
					Online    bool  `json:"online"`
					Timestamp int64 `json:"timestamp"`
				}
				if err := json.Unmarshal(m.Payload(), &s); err == nil {
					w.st = append(w.st, struct {
						Online    bool
						Timestamp int64
					}{s.Online, s.Timestamp})
				}
				return
			}
			p, err := sparkplug.DecodePayload(m.Payload())
			if err != nil {
				return
			}
			for _, mm := range p.Metrics {
				if v, ok := mm.Value.(float64); ok && mm.Name == "Zone1/SP_001" {
					w.cmds = append(w.cmds, v)
				}
			}
		}))
	if tok := w.cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("watch connect: %v", tok.Error())
	}
	t.Cleanup(func() { w.cli.Disconnect(50) })
	for _, f := range []string{"spBv1.0/STATE/central", "spBv1.0/Plant/NCMD/Edge1"} {
		if tok := w.cli.Subscribe(f, 1, nil); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
			t.Fatalf("watch subscribe %s: %v", f, tok.Error())
		}
	}
	return w
}

func (w *hostWatch) states() []struct {
	Online    bool
	Timestamp int64
} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append(w.st[:0:0], w.st...)
}

func (w *hostWatch) commands() []float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]float64(nil), w.cmds...)
}

func (w *hostWatch) birth(t *testing.T) {
	t.Helper()
	body, err := sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		Metrics: []sparkplug.Metric{
			{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: int64(1)},
			{Name: "Zone1/SP_001", Datatype: spb.DataType_Double, Value: 0.0},
		},
	}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if tok := w.cli.Publish("spBv1.0/Plant/NBIRTH/Edge1", 1, false, body); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("NBIRTH: %v", tok.Error())
	}
}

// command is one operator move, the way the runtime hands it over: a
// baseline snapshot first (outputs are commands only when they change), then
// the moved value.
func command(t *testing.T, d *sphost.Driver, v float64) {
	t.Helper()
	if err := d.WriteOutputs(nio.Values{spSetpoint: v, "Edge1__Rebirth": false}); err != nil {
		t.Fatal(err)
	}
}

func online(t *testing.T, d *sphost.Driver) {
	t.Helper()
	waitTrue(t, "Edge1 online", func() bool {
		v, err := d.ReadInputs()
		return err == nil && v["Edge1__Online"] == true
	})
	command(t, d, 0) // the baseline: the site already holds 0
}

// settle gives a command that is NOT supposed to go out every chance to:
// several command-coalesce windows.
func settle() { time.Sleep(600 * time.Millisecond) }

// SPH-036 end to end: two replicas of one manifest on one broker. Only the
// leader is primary and commands; a failover hands both over — the old
// leader stops commanding and declares itself offline with its own session
// timestamp, the new one declares itself online with a newer one — and the
// retained STATE a late-joining edge reads names the new leader online.
func TestRedundantSparkplugHostOnlyTheLeaderIsPrimary(t *testing.T) {
	url := spBroker(t)
	files := hostProject(url, true)
	w := watchCentral(t, url)

	a, b := &flagCoordinator{}, &flagCoordinator{}
	a.lead.Store(true)
	projA, drvA := startReplica(t, files, "central-0", a)
	if projA.Runtime.Coordinator != a {
		t.Fatal("the runtime does not answer to the elector")
	}
	waitTrue(t, "the leader's STATE online", func() bool {
		s := w.states()
		return len(s) == 1 && s[0].Online
	})
	_, drvB := startReplica(t, files, "central-1", b)

	w.birth(t)
	online(t, drvA)
	online(t, drvB)

	// Both replicas are handed an operator move; only the leader's leaves.
	command(t, drvA, 7)
	command(t, drvB, 8)
	waitTrue(t, "the leader's command", func() bool { return len(w.commands()) >= 1 })
	settle()
	if c := w.commands(); len(c) != 1 || c[0] != 7 {
		t.Fatalf("commands = %v, want only the leader's 7", c)
	}
	s := w.states()
	if len(s) != 1 {
		t.Fatalf("STATE certificates = %+v, want the leader's birth alone (a standby declared itself, or the replicas took each other's MQTT sessions over)", s)
	}
	birthA := s[0]
	if !drvA.Status().Connected || !drvB.Status().Connected {
		t.Fatal("both replicas must stay connected side by side")
	}

	// Failover.
	a.lead.Store(false)
	b.lead.Store(true)
	// The old leader declares itself offline with its own session timestamp;
	// the new one declares itself online with a newer one (and, if the old
	// certificate lands after its birth, repeats those same bytes).
	var birthB int64
	waitTrue(t, "the hand-over certificates", func() bool {
		var death bool
		for _, c := range w.states()[1:] {
			if !c.Online && c.Timestamp == birthA.Timestamp {
				death = true
			}
			if c.Online && c.Timestamp > birthA.Timestamp {
				birthB = c.Timestamp
			}
		}
		return death && birthB != 0
	})
	settle()
	for _, c := range w.states()[1:] {
		if c.Online != (c.Timestamp == birthB) || (!c.Online && c.Timestamp != birthA.Timestamp) {
			t.Fatalf("STATE certificates = %+v, want only the old leader offline at %d and the new leader online at %d",
				w.states(), birthA.Timestamp, birthB)
		}
	}

	// The demoted replica's commands stay home; the new leader's go out (its
	// held 8 first, then the 9 it is handed now).
	waitTrue(t, "the new leader's held command", func() bool { return len(w.commands()) >= 2 })
	command(t, drvA, 5)
	command(t, drvB, 9)
	waitTrue(t, "the new leader's command", func() bool { return len(w.commands()) >= 3 })
	settle()
	if c := w.commands(); len(c) != 3 || c[1] != 8 || c[2] != 9 {
		t.Fatalf("commands = %v, want [7 8 9]: nothing from the demoted replica", c)
	}

	// What a site that connects now reads: the new leader, online.
	waitTrue(t, "the retained STATE to name the new leader online", func() bool {
		r := retainedCentral(t, url)
		return r.Online && r.Timestamp == birthB
	})
}

// A redundancy: section off-cluster: the real elector is standalone and
// always the leader, so the host is primary and commands, as on a bench.
func TestRedundantSparkplugHostOnABenchIsPrimary(t *testing.T) {
	url := spBroker(t)
	w := watchCentral(t, url)
	proj, drv := startReplica(t, hostProject(url, true), "bench", nil)
	if proj.Runtime.Coordinator == nil {
		t.Fatal("electorFor did not wire the elector")
	}
	waitTrue(t, "STATE online", func() bool {
		s := w.states()
		return len(s) == 1 && s[0].Online
	})
	w.birth(t)
	online(t, drv)
	command(t, drv, 7)
	waitTrue(t, "the command", func() bool { return len(w.commands()) == 1 })
}

// The single-host guarantee: no redundancy: section, so nothing is wired and
// the host is its own leader — STATE online from the first connect, commands
// out. (That its MQTT client ID carries no replica suffix is asserted at the
// driver: TestNoWriteGateSingleHostIsAlwaysPrimary.)
func TestSingleSparkplugHostWithoutRedundancyIsAlwaysPrimary(t *testing.T) {
	url := spBroker(t)
	w := watchCentral(t, url)
	proj, drv := startReplica(t, hostProject(url, false), "single", nil)
	if proj.Runtime.Coordinator != nil {
		t.Fatal("a manifest without redundancy: got an elector")
	}
	waitTrue(t, "STATE online", func() bool {
		s := w.states()
		return len(s) == 1 && s[0].Online
	})
	w.birth(t)
	online(t, drv)
	command(t, drv, 7)
	waitTrue(t, "the command", func() bool { return len(w.commands()) == 1 })

}

// retainedCentral is what a late-joining edge reads from the broker.
func retainedCentral(t *testing.T, url string) (s struct {
	Online    bool  `json:"online"`
	Timestamp int64 `json:"timestamp"`
}) {
	t.Helper()
	got := make(chan []byte, 1)
	cli := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(url).SetClientID("late-edge").SetCleanSession(true))
	if tok := cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("late edge connect: %v", tok.Error())
	}
	defer cli.Disconnect(50)
	if tok := cli.Subscribe("spBv1.0/STATE/central", 1, func(_ mqtt.Client, m mqtt.Message) {
		select {
		case got <- append([]byte(nil), m.Payload()...):
		default:
		}
	}); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("late edge subscribe: %v", tok.Error())
	}
	select {
	case b := <-got:
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no retained STATE")
	}
	return s
}
