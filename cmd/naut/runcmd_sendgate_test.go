package main

import (
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

func (p pairCoord) IsLeader() bool                  { return true }
func (p pairCoord) SendGate() (bool, time.Duration) { return true, p.budget }

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
	if ok, b := d.send(); !ok || b != 1234*time.Millisecond {
		t.Fatalf("the send gate answered %v %s; want the elector's own", ok, b)
	}
	k := &sendGatedDriver{Driver: nio.NewMemory()}
	proj = &project.Project{}
	proj.Runtime.Driver = k
	wireRedundancy(proj, &flagCoordinator{})
	if k.write == nil || k.send != nil {
		t.Fatalf("Kubernetes Lease: write gate %v, send gate %v; want only the write gate", k.write != nil, k.send != nil)
	}
}
