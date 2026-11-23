package main

import (
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	nio "github.com/joyautomation/nautilus/io"
)

// sendGatedDriver records the gates naut run installs.
type sendGatedDriver struct {
	nio.Driver
	write func() bool
	send  nio.SendGate
}

func (d *sendGatedDriver) SetWriteGate(g func() bool) { d.write = g }
func (d *sendGatedDriver) SetSendGate(g nio.SendGate) { d.send = g }

// pairCoord is a pair's elector as naut run sees it: IsLeader plus the
// send budget.
type pairCoord struct{ budget time.Duration }

func (p pairCoord) IsLeader() bool                          { return true }
func (p pairCoord) SendGate() (bool, time.Duration, uint64) { return true, p.budget, 7 }

// Design §3.4: naut run hands a pair's send gate, with its budget, to a
// driver that checks before each send; a coordinator without one (the
// Kubernetes Lease) leaves the plain gate only.
func TestWireRedundancyInstallsThePairsSendGate(t *testing.T) {
	d := &sendGatedDriver{Driver: nio.NewMemory()}
	proj := &project.Project{}
	proj.Runtime.Driver = d
	wireRedundancy(proj, pairCoord{budget: 1234 * time.Millisecond})
	if d.write == nil || d.send == nil {
		t.Fatalf("write gate %v, send gate %v; want both installed", d.write != nil, d.send != nil)
	}
	if ok, b, term := d.send(); !ok || b != 1234*time.Millisecond || term != 7 {
		t.Fatalf("the send gate answered %v %s term %d; want the elector's own", ok, b, term)
	}
	k := &sendGatedDriver{Driver: nio.NewMemory()}
	proj = &project.Project{}
	proj.Runtime.Driver = k
	wireRedundancy(proj, &flagCoordinator{})
	if k.write == nil || k.send != nil {
		t.Fatalf("Kubernetes Lease: write gate %v, send gate %v; want only the write gate", k.write != nil, k.send != nil)
	}
}

// timedDriver reports per-source write timeouts, as modbus, snmp and
// redfish do.
type timedDriver struct {
	nio.Driver
	t map[string]time.Duration
}

func (d timedDriver) WriteTimeouts() map[string]time.Duration { return d.t }
func (d timedDriver) InputNames() []string                    { return []string{"PV"} }
func (d timedDriver) OutputNames() []string                   { return []string{"SP"} }

// #1040 review: naut check warns, on a pair, for each source that takes
// writes whose request timeout is longer than the send budget (as little
// as 3/4 lease minus the net delay: 1.45 s at a 2 s lease, 0.7 s at the
// default 1 s), through a Multi too; not without a pair.
func TestSendBudgetWarnings(t *testing.T) {
	slow := timedDriver{nio.NewMemory(), map[string]time.Duration{"plc1": 3 * time.Second, "plc2": time.Second}}
	m, err := nio.NewMulti(nio.NamedDriver{Name: "field", Driver: slow})
	if err != nil {
		t.Fatal(err)
	}
	proj := &project.Project{Redundancy: &project.RedundancyConfig{Peers: map[string]string{"a": "a:1", "b": "b:1"}, Witness: "w:1", Experimental: true, Lease: "2s"}}
	proj.Runtime.Driver = m
	w := sendBudgetWarnings(proj)
	if len(w) != 1 || !strings.Contains(w[0], "field: source plc1") || !strings.Contains(w[0], "1.45s") {
		t.Fatalf("warnings %q; want one, for plc1 (3 s against 1.45 s), naming its driver", w)
	}
	proj.Redundancy.Lease = "" // the default 1 s: plc2's 1 s is over its 0.7 s budget too
	if w := sendBudgetWarnings(proj); len(w) != 2 || !strings.Contains(w[1], "source plc2") || !strings.Contains(w[1], "700ms") {
		t.Fatalf("at the default lease: %q; want plc1 and plc2 against 700ms", w)
	}
	proj.Redundancy = nil
	if w := sendBudgetWarnings(proj); len(w) != 0 {
		t.Fatalf("without a pair: %q; want none", w)
	}
}
