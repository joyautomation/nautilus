package project

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/plcnext"
)

// The status row says the plcnext write gate is closed, and what that does
// to the outputs: held on gRPC (no safe state there, said plainly), driven
// to zero by the shim's watchdog on shm (#863).
func TestPlcnextStatusShowsTheClosedGate(t *testing.T) {
	up := plcnext.Health{Connected: true, Configured: true, Running: true, Modules: 3}
	if s := plcnextStatus(up); s.State != "connected" {
		t.Fatalf("open gate: %+v", s)
	}
	g := up
	g.Transport, g.GateClosed = plcnext.TransportGRPC, true
	if s := plcnextStatus(g); s.State != "error" || !strings.Contains(s.Message, "outputs held (no safe state on this route") {
		t.Fatalf("closed gate on gRPC: %q %q", s.State, s.Message)
	}
	g.Transport = plcnext.TransportShm
	if s := plcnextStatus(g); s.State != "error" || !strings.Contains(s.Message, "the shim's watchdog drives the outputs to zero") {
		t.Fatalf("closed gate on shm: %q %q", s.State, s.Message)
	}
}
