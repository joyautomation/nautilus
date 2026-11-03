package modbus

import (
	"net"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// __Online means the device answers, not that TCP is up (issue #539). These
// tests pin that meaning: a dial alone never sets it, gateway no-answers
// (0x0A, 0x0B) clear it, any other answer sets it. Stop never counts an
// outage: TestOutagesStopIsNotAnOutage.

// A gateway that accepts TCP and never replies is never online, not even
// for the moment between the dial and the read timing out.
func TestOnlineSilentGatewayNeverGoesOnline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	accepted := 0
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			accepted++
			mu.Unlock()
			t.Cleanup(func() { _ = c.Close() }) // read nothing, answer nothing
		}
	}()

	src := fastSource("DEV", ln.Addr().String())
	src.Timeout = 50 * time.Millisecond
	m := Manifest{
		Sources: []Source{src},
		Tags:    []TagBinding{{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if v, _ := d.ReadInputs(); v["DEV__Online"] == true {
			t.Fatal("a gateway that never answered: __Online went true")
		}
		mu.Lock()
		n := accepted
		mu.Unlock()
		if n >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for three connections to the silent gateway")
		}
		time.Sleep(time.Millisecond)
	}
	if v, _ := d.ReadInputs(); v["DEV__Outages"] != int64(0) {
		t.Fatalf("__Outages = %#v, want int64(0)", v["DEV__Outages"])
	}
}

// A gateway answering every read with 0x0B (or 0x0A) has a dead field
// device behind it: the source goes offline while the TCP link to the
// gateway stays up, that fall is one outage, and the next good answer
// brings it back online.
func TestOnlineGatewayNoAnswerIsAnOutage(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	u.SetHolding(0, 7)
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags:    []TagBinding{{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	read := func() nio.Values { v, _ := d.ReadInputs(); return v }
	waitFor(t, "first read", func() bool { v := read(); return v["DEV__Online"] == true && v["PV"] == int64(7) })

	for i, code := range []byte{0x0B, 0x0A} {
		want := int64(i + 1)
		u.InjectException(FCReadHoldingRegisters, code)
		waitFor(t, "source offline on gateway no-answers", func() bool { return read()["DEV__Online"] == false })
		if got := read()["DEV__Outages"]; got != want {
			t.Fatalf("exception 0x%02X every cycle: __Outages = %#v, want int64(%d)", code, got, want)
		}
		if h := d.Health().Sources[0]; h.State != "connected" {
			t.Errorf("the gateway link is still up: state = %q, want connected", h.State)
		}
		if q := d.Quality()["PV"]; q != nio.Stale {
			t.Errorf("PV quality with the device dark = %v, want Stale", q)
		}
		u.InjectException(FCReadHoldingRegisters, 0)
		u.SetHolding(0, uint16(10+i))
		waitFor(t, "back online on the next answer", func() bool {
			v := read()
			return v["DEV__Online"] == true && v["PV"] == int64(10+i)
		})
		if got := read()["DEV__Outages"]; got != want {
			t.Fatalf("coming back changed __Outages to %#v, want int64(%d)", got, want)
		}
	}
}

// A unit the gateway never reaches (0x0B from the first read) is never
// online, so it has had no outage.
func TestOnlineGatewayNoAnswerFromTheStart(t *testing.T) {
	srv := newSlave(t) // unit 2 is never created: every request answers 0x0B
	src := fastSource("DEV", srv.Addr())
	src.UnitID = 2
	m := Manifest{
		Sources: []Source{src},
		Tags:    []TagBinding{{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "several gateway no-answers", func() bool { return d.Health().Sources[0].Exceptions >= 3 })
	v, _ := d.ReadInputs()
	if v["DEV__Online"] != false || v["DEV__Outages"] != int64(0) {
		t.Fatalf("never answered: __Online = %#v, __Outages = %#v, want false and int64(0)", v["DEV__Online"], v["DEV__Outages"])
	}
	if q := d.Quality()["PV"]; q != nio.NotConnected {
		t.Errorf("PV quality = %v, want NotConnected", q)
	}
}

// Any other exception is an answer: the device is alive and refuses one
// span. A source whose only block answers 0x02 is online with that tag
// Bad, and so is a source where one block gets 0x0B and another answers.
func TestOnlineOtherExceptionsAreAnswers(t *testing.T) {
	t.Run("illegal address", func(t *testing.T) {
		srv := newSlave(t)
		srv.Unit(1).InjectException(FCReadHoldingRegisters, 0x02)
		m := Manifest{
			Sources: []Source{fastSource("DEV", srv.Addr())},
			Tags:    []TagBinding{{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"}},
		}
		d, err := New(m, WithScanRate(15*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		startDriver(t, d)
		waitFor(t, "online with PV Bad", func() bool {
			v, _ := d.ReadInputs()
			return v["DEV__Online"] == true && d.Quality()["PV"] == nio.Bad
		})
		waitFor(t, "block parked", func() bool { return d.Health().Sources[0].Exceptions >= exceptionsToPark+1 })
		if v, _ := d.ReadInputs(); v["DEV__Online"] != true || v["DEV__Outages"] != int64(0) {
			t.Fatalf("0x02 every cycle: __Online = %#v, __Outages = %#v, want true and int64(0)", v["DEV__Online"], v["DEV__Outages"])
		}
	})
	t.Run("one block answers", func(t *testing.T) {
		srv := newSlave(t)
		u := srv.Unit(1)
		u.SetInput(0, 6)
		u.InjectException(FCReadHoldingRegisters, 0x0B)
		m := Manifest{
			Sources: []Source{fastSource("DEV", srv.Addr())},
			Tags: []TagBinding{
				{Name: "GONE", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"},
				{Name: "OK", Source: "DEV", Table: TableInput, Address: 0, Format: "uint16"},
			},
		}
		d, err := New(m, WithScanRate(15*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		startDriver(t, d)
		waitFor(t, "OK delivered", func() bool { v, _ := d.ReadInputs(); return v["OK"] == int64(6) })
		waitFor(t, "GONE refused a few times", func() bool { return d.Health().Sources[0].Exceptions >= exceptionsToPark+1 })
		if v, _ := d.ReadInputs(); v["DEV__Online"] != true || v["DEV__Outages"] != int64(0) {
			t.Fatalf("one block answering: __Online = %#v, __Outages = %#v, want true and int64(0)", v["DEV__Online"], v["DEV__Outages"])
		}
		if q := d.Quality(); q["GONE"] != nio.Bad || q["OK"] != nio.Good {
			t.Errorf("quality = %v, want GONE Bad, OK Good", q)
		}
	})
}

// A write-only source (a keep-alive word, nothing polled) is offline until
// its first write is confirmed, online after, and offline again (one
// outage) when the gateway answers its writes with 0x0B.
func TestOnlineWriteOnlySource(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	u.SetHolding(8, 0)
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{{Name: "CMD", Source: "DEV", Table: TableHolding, Address: 8,
			Format: "uint16", Writable: true, WriteOnly: true, Rewrite: 20 * time.Millisecond}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	read := func() nio.Values { v, _ := d.ReadInputs(); return v }
	waitFor(t, "connected", func() bool { return d.Health().Sources[0].State == "connected" })
	for range 10 {
		if read()["DEV__Online"] != false {
			t.Fatal("connected, nothing written yet: __Online went true on the dial")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := d.WriteOutputs(nio.Values{"CMD": int64(5)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "online on the confirmed write", func() bool {
		return srv.Unit(1).Holding(8) == 5 && read()["DEV__Online"] == true
	})

	u.InjectException(FCWriteSingleRegister, 0x0B)
	waitFor(t, "offline on gateway no-answers to the re-asserts", func() bool { return read()["DEV__Online"] == false })
	if got := read()["DEV__Outages"]; got != int64(1) {
		t.Fatalf("__Outages = %#v, want int64(1)", got)
	}
	u.InjectException(FCWriteSingleRegister, 0)
	waitFor(t, "back online on the next re-assert", func() bool { return read()["DEV__Online"] == true })
	if got := read()["DEV__Outages"]; got != int64(1) {
		t.Fatalf("coming back changed __Outages to %#v, want int64(1)", got)
	}
}
