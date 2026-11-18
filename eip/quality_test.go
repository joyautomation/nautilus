package eip

import (
	"context"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// A read that fails while the session stays up is Bad, with its last good
// value held, until a read succeeds again (#862). It used to stay Good: the
// snapshot simply kept the old value and nothing marked it.
func TestFailedReadIsBadUntilAGoodRead(t *testing.T) {
	host, port, store, srv, _ := startEmulatorFull(t, false)
	store.UpdateValue("Speed", 3.0)
	d, err := New(host, testManifest(), WithPort(port), WithScanRate(20*time.Millisecond), WithOnlineTag("eip__Online"))
	if err != nil {
		t.Fatal(err)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	waitFor(t, "online with Speed", func() bool {
		v, _ := d.ReadInputs()
		return v["eip__Online"] == true && v["Speed"] == 3.0
	})
	if q := d.Quality(); len(q) != 0 {
		t.Fatalf("quality once online = %v, want all Good", q)
	}

	srv.FailReads("Speed", true)
	store.UpdateValue("Speed", 7.0)
	waitFor(t, "Speed Bad", func() bool { return d.Quality()["Speed"] == nio.Bad })
	v, _ := d.ReadInputs()
	if v["Speed"] != 3.0 {
		t.Errorf("Speed = %v during failed reads, want the last good 3 held", v["Speed"])
	}
	if v["eip__Online"] != true {
		t.Error("the session is up: the companion must stay true")
	}
	if q := d.Quality(); q["TRS"] != nio.Good {
		t.Errorf("TRS = %v: only the failing tag is Bad", q["TRS"])
	}

	srv.FailReads("Speed", false)
	waitFor(t, "Speed Good again with the new value", func() bool {
		v, _ := d.ReadInputs()
		_, said := d.Quality()["Speed"]
		return !said && v["Speed"] == 7.0
	})
}

// The same for a struct polled member by member (leaf mode): a failed
// member read makes the whole tag Bad, the last good struct held, and the
// next complete read clears it.
func TestFailedLeafReadIsBadUntilAGoodRead(t *testing.T) {
	host, port, _, srv, _ := startEmulatorFull(t, true) // struct roots refused: leaf mode
	d, err := New(host, testManifest(), WithPort(port), WithScanRate(20*time.Millisecond), WithOnlineTag("eip__Online"))
	if err != nil {
		t.Fatal(err)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	waitFor(t, "online, TRS delivered", func() bool {
		v, _ := d.ReadInputs()
		_, has := v["TRS"]
		return v["eip__Online"] == true && has && d.Health().LeafMode > 0
	})
	if q := d.Quality(); len(q) != 0 {
		t.Fatalf("quality once online = %v, want all Good", q)
	}

	srv.FailReads("TRS.Count", true)
	waitFor(t, "TRS Bad", func() bool { return d.Quality()["TRS"] == nio.Bad })
	if v, _ := d.ReadInputs(); v["TRS"] == nil {
		t.Error("TRS's last good value must be held")
	}
	srv.FailReads("TRS.Count", false)
	waitFor(t, "TRS Good again", func() bool { _, said := d.Quality()["TRS"]; return !said })
}

// A dropped session: what was delivered is Stale (its last value held, the
// io.Quality contract and modbus's rule), the companion goes false.
func TestDroppedSessionIsStale(t *testing.T) {
	host, port, store, _, stop := startEmulatorFull(t, false)
	store.UpdateValue("Speed", 3.0)
	d, err := New(host, testManifest(), WithPort(port), WithScanRate(20*time.Millisecond), WithOnlineTag("eip__Online"))
	if err != nil {
		t.Fatal(err)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	waitFor(t, "online", func() bool { v, _ := d.ReadInputs(); return v["eip__Online"] == true && v["Speed"] == 3.0 })

	stop()
	waitFor(t, "Stale after the drop", func() bool {
		v, _ := d.ReadInputs()
		return v["eip__Online"] == false && d.Quality()["Speed"] == nio.Stale && d.Quality()["TRS"] == nio.Stale
	})
	if v, _ := d.ReadInputs(); v["Speed"] != 3.0 {
		t.Errorf("Speed = %v after the drop, want the last value held", v["Speed"])
	}
}
