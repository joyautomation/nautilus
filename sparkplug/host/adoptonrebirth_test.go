package host

// The documented rule (guides/sparkplug-host.md, "After a site restarts"):
// after an edge node or device restarts, the host adopts the value it
// reports, and the program's held command is not re-sent until it changes.
// A deliberate design (the maintainer kept it; an opt-in re-assert per
// output may come later), pinned here so a change to it is deliberate too.

import "testing"

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
