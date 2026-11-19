package host

// The documented rule (guides/sparkplug-host.md, "After a site restarts"):
// after an edge node or device restarts, the host adopts the value it
// reports, and the program's held command is not re-sent until it changes.
// A deliberate design (the maintainer kept it; an opt-in re-assert per
// output may come later), pinned here so a change to it is deliberate too.
//
// The same holds for a command lost in a session the broker was closing:
// commands go out at QoS 0, so the host cannot tell it never arrived.

import (
	"strings"
	"sync/atomic"
	"testing"

	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

func TestHeldCommandIsNotReassertedAfterARestart(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	w := watchHost(t, addr, "watch-adopt", "h-adopt")
	host := startHostDriver(t, testManifest(), testConfig(addr, "h-adopt"))
	waitConnected(t, host)
	edge := edgePublisher(t, addr, "edge-adopt")
	birthW6AndPLC1(t, host, edge, 1)
	baselineScan(t, host)
	writeSpeed(t, host, 11)
	waitUntil(t, "the 11 command", func() bool { return len(w.commands(t)) == 1 })

	// The site restarts and comes back at 50; the program still holds 11.
	publishPayload(t, edge, "spBv1.0/G/NDEATH/W6", ndeath(1))
	waitForValue(t, host, "W6__Online", func(v any) bool { return v == false })
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", nbirth(2, dbl("Well/Level", 4.2)))
	publishPayload(t, edge, "spBv1.0/G/DBIRTH/W6/PLC1", data(1, boolean("Pump/Run", true), dbl("Pump/SpeedSP", 50)))
	waitForValue(t, host, "W6_PLC1__Online", func(v any) bool { return v == true })
	writeSpeed(t, host, 11) // the runtime hands the unchanged output again
	barrier(t, host, w)
	if c := w.commands(t); len(c) != 1 {
		t.Fatalf("commands = %v, want only the first 11: the restarted site's 50 is adopted and the held 11 is not re-sent", c)
	}

	// The next change goes out as usual.
	writeSpeed(t, host, 12)
	waitUntil(t, "the 12 command", func() bool { return len(w.commands(t)) == 2 })
	if c := w.commands(t); c[1] != 12.0 {
		t.Fatalf("commands = %v, want 12 after 11", c)
	}
}

// loseNextCommand, armed, drops the next DCMD a client publishes and closes
// that client's session: a QoS 0 command written into a session the broker
// is closing, gone without a trace.
type loseNextCommand struct {
	mqttsrv.HookBase
	armed, fired atomic.Bool
}

func (h *loseNextCommand) ID() string { return "lose-next-command" }

func (h *loseNextCommand) Provides(b byte) bool { return b == mqttsrv.OnPublish }

func (h *loseNextCommand) OnPublish(cl *mqttsrv.Client, pk packets.Packet) (packets.Packet, error) {
	if !strings.Contains(pk.TopicName, "/DCMD/") || !h.armed.CompareAndSwap(true, false) {
		return pk, nil
	}
	cl.Stop(packets.ErrNotAuthorized)
	h.fired.Store(true)
	return pk, packets.ErrRejectPacket
}

func TestCommandLostWithItsSessionIsNotResent(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	lose := new(loseNextCommand)
	if err := srv.AddHook(lose, nil); err != nil {
		t.Fatal(err)
	}
	w := watchHost(t, addr, "watch-lost", "h-lost")
	host := startHostDriver(t, testManifest(), testConfig(addr, "h-lost"))
	waitConnected(t, host)
	edge := edgePublisher(t, addr, "edge-lost")
	birthW6AndPLC1(t, host, edge, 1)
	baselineScan(t, host)
	writeSpeed(t, host, 11)
	waitUntil(t, "the 11 command", func() bool { return len(w.commands(t)) == 1 })

	// 12 is lost with the session; the host has lost its broker.
	lose.armed.Store(true)
	writeSpeed(t, host, 12)
	waitUntil(t, "the 12 command lost with its session", lose.fired.Load)
	waitForValue(t, host, "W6__Online", func(v any) bool { return v == false })
	waitConnected(t, host)

	// The site never saw 12: it births still holding 11, and that is
	// adopted. The program still holds 12, and it is not sent again.
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", nbirth(2, dbl("Well/Level", 4.2)))
	publishPayload(t, edge, "spBv1.0/G/DBIRTH/W6/PLC1", data(1, boolean("Pump/Run", true), dbl("Pump/SpeedSP", 11)))
	waitForValue(t, host, "W6_PLC1__Online", func(v any) bool { return v == true })
	writeSpeed(t, host, 12)
	barrier(t, host, w)
	if c := w.commands(t); len(c) != 1 {
		t.Fatalf("commands = %v, want only the first 11: the 12 lost with its session is not re-sent, and the site's 11 is adopted", c)
	}

	// The next change goes out as usual.
	writeSpeed(t, host, 13)
	waitUntil(t, "the 13 command", func() bool { return len(w.commands(t)) == 2 })
	if c := w.commands(t); c[1] != 13.0 {
		t.Fatalf("commands = %v, want 13 after 11", c)
	}
}
