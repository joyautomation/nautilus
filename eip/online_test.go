package eip

import (
	"context"
	"net"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// The online companion and the read contract that goes with it — modbus's
// and the Sparkplug host's: before Start a read errors; once started it never
// does. A controller that is down delivers the companion FALSE and no data
// tag (the runtime holds a protocol input at its init/zero meanwhile), and
// Quality says NotConnected for what has never arrived. Once the controller
// answers and every scan class has polled, the companion is TRUE and the
// tags are Good.
func TestOnlineCompanionAndQuality(t *testing.T) {
	// A port nothing listens on: the controller is down.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	downPort := l.Addr().(*net.TCPAddr).Port
	l.Close()

	d, err := New("127.0.0.1", testManifest(), WithPort(downPort), WithOnlineTag("eip__Online"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ReadInputs(); err == nil {
		t.Fatal("an unstarted driver must error, not deliver")
	}
	names := map[string]bool{}
	for _, n := range d.InputNames() {
		names[n] = true
	}
	if !names["eip__Online"] {
		t.Errorf("InputNames %v lacks the companion", d.InputNames())
	}
	if got := d.InputType("eip__Online"); got != "BOOL" {
		t.Errorf("InputType(companion) = %q", got)
	}
	if got, want := d.InputType("TRS"), "Plt_Type"; got != want {
		t.Errorf("InputType(TRS) = %q, want %q", got, want)
	}
	if got := d.InputType("Speed"); got != "REAL" {
		t.Errorf("InputType(Speed) = %q, want REAL", got)
	}

	d.Start(context.Background())
	t.Cleanup(d.Stop)
	time.Sleep(50 * time.Millisecond) // let the first connect fail
	v, err := d.ReadInputs()
	if err != nil {
		t.Fatalf("a started driver errored while the controller is down: %v", err)
	}
	if on, ok := v["eip__Online"]; !ok || on != false {
		t.Errorf("companion = %v (present %v), want present and false", on, ok)
	}
	if _, ok := v["Speed"]; ok {
		t.Error("a data tag was delivered from a controller that never answered")
	}
	if q := d.Quality(); q["Speed"] != nio.NotConnected || q["TRS"] != nio.NotConnected {
		t.Errorf("quality while down = %v, want NotConnected on never-delivered tags", q)
	}
	if _, said := d.Quality()["eip__Online"]; said {
		t.Error("the companion must always be Good")
	}

	// The controller is up: a fresh driver against the emulator.
	host, port, store := startEmulator(t)
	store.UpdateValue("Speed", 3.0)
	up, err := New(host, testManifest(), WithPort(port), WithScanRate(20*time.Millisecond), WithOnlineTag("eip__Online"))
	if err != nil {
		t.Fatal(err)
	}
	up.Start(context.Background())
	t.Cleanup(up.Stop)
	waitFor(t, "online", func() bool {
		v, err := up.ReadInputs()
		return err == nil && v["eip__Online"] == true
	})
	v, _ = up.ReadInputs()
	if v["Speed"] != 3.0 {
		t.Errorf("Speed = %v once online, want 3", v["Speed"])
	}
	if q := up.Quality(); len(q) != 0 {
		t.Errorf("quality once online = %v, want all Good (empty)", q)
	}
}

// A companion that collides with a binding is refused at New.
func TestOnlineCompanionCollision(t *testing.T) {
	if _, err := New("h", testManifest(), WithOnlineTag("Speed")); err == nil {
		t.Fatal("a companion named after a binding was accepted")
	}
}
