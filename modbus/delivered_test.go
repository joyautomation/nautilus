package modbus

import (
	"net"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// Delivered (io.DeliveryReporter) is what the runtime's start-up hold
// waits on: every enabled source has answered a read once. ReadInputs
// succeeds before that with only the __Online companions in it, which is
// not a delivery.
func TestDeliveredWaitsForEveryEnabledSource(t *testing.T) {
	srv := newSlave(t)
	srv.Unit(1).SetHolding(0, 9)

	// A port nothing listens on: the second source never answers.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	ln.Close()

	live := fastSource("LIVE", srv.Addr())
	gone := fastSource("GONE", dead)
	gone.Enable = "GONE_En"
	m := Manifest{
		Sources: []Source{live, gone},
		Tags: []TagBinding{
			{Name: "PV", Source: "LIVE", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "PV2", Source: "GONE", Table: TableHolding, Address: 0, Format: "uint16"},
		},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if d.Delivered() {
		t.Fatal("Delivered before Start")
	}
	startDriver(t, d)
	waitFor(t, "the live source", func() bool {
		v, _ := d.ReadInputs()
		return v["PV"] == int64(9)
	})
	// The read succeeds, with LIVE's tags and both companions — but GONE
	// has never answered.
	if v, err := d.ReadInputs(); err != nil || v["GONE__Online"] != false {
		t.Fatalf("read %v %v", v, err)
	}
	if d.Delivered() {
		t.Fatal("Delivered while an enabled source has never answered")
	}

	// Parking GONE (its enable tag false) means nothing will poll it:
	// nothing to wait for.
	if err := d.WriteOutputs(nio.Values{"GONE_En": false}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Delivered once GONE is parked", d.Delivered)
}
