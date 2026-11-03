package modbus

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// __Online means the device answers, not that TCP is up (issue #539). These
// tests pin that meaning: a dial alone never sets it, gateway no-answers
// (0x0A, 0x0B) clear it after gatewayMissesToOffline in a row, any other
// answer sets it. Stop never counts an
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

// A write-only source with a rewrite: keep-alive is offline until the
// first re-assert is confirmed, online after, and offline again (one
// outage) once its writes get gateway no-answers.
func TestOnlineWriteOnlySourceWithRewrite(t *testing.T) {
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
			t.Fatal("connected, keep-alive not yet answered: __Online went true on the dial")
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

// A write-only source with no rewrite: has nothing to ask the device until
// the program commands it, so it is online on connect (a program gating
// its commands on __Online must be able to send the first one). Its writes
// decide after that: gateway no-answers take it offline, an answer brings
// it back.
func TestOnlineWriteOnlySourceWithoutRewrite(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	u.SetHolding(8, 0)
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{{Name: "CMD", Source: "DEV", Table: TableHolding, Address: 8,
			Format: "uint16", Writable: true, WriteOnly: true}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	read := func() nio.Values { v, _ := d.ReadInputs(); return v }
	waitFor(t, "online on connect", func() bool { return read()["DEV__Online"] == true })
	// The first snapshot after Start is the baseline, not a command.
	if err := d.WriteOutputs(nio.Values{"CMD": int64(1)}); err != nil {
		t.Fatal(err)
	}

	// Three in a row, pinned here rather than read from the constant.
	const misses = 3
	u.InjectException(FCWriteSingleRegister, 0x0B)
	for i := range misses {
		if read()["DEV__Online"] != true {
			t.Fatalf("offline after %d gateway no-answers, want %d in a row first", i, misses)
		}
		if err := d.WriteOutputs(nio.Values{"CMD": int64(10 + i)}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "write refused", func() bool { return d.Health().Sources[0].Exceptions == uint64(i+1) })
	}
	if v := read(); v["DEV__Online"] != false || v["DEV__Outages"] != int64(1) {
		t.Fatalf("after %d gateway no-answers: __Online = %#v, __Outages = %#v, want false and int64(1)",
			misses, v["DEV__Online"], v["DEV__Outages"])
	}

	u.InjectException(FCWriteSingleRegister, 0)
	if err := d.WriteOutputs(nio.Values{"CMD": int64(20)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "back online on the confirmed write", func() bool {
		return srv.Unit(1).Holding(8) == 20 && read()["DEV__Online"] == true
	})
	if got := read()["DEV__Outages"]; got != int64(1) {
		t.Fatalf("coming back changed __Outages to %#v, want int64(1)", got)
	}
}

// On a polled source only reads decide: a keep-alive the gateway keeps
// confirming must not bring back a source whose reads all get 0x0B, or the
// source flaps at the rewrite rate and __Outages climbs with it.
func TestOnlinePolledSourceWritesDoNotDecide(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	u.SetHolding(0, 7)
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{
			{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "CMD", Source: "DEV", Table: TableHolding, Address: 8,
				Format: "uint16", Writable: true, WriteOnly: true, Rewrite: 20 * time.Millisecond},
		},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	read := func() nio.Values { v, _ := d.ReadInputs(); return v }
	if err := d.WriteOutputs(nio.Values{"CMD": int64(5)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "online, keep-alive written", func() bool {
		return read()["DEV__Online"] == true && srv.Unit(1).Holding(8) == 5
	})

	u.InjectException(FCReadHoldingRegisters, 0x0B)
	waitFor(t, "offline on gateway no-answers to the reads", func() bool { return read()["DEV__Online"] == false })
	writes := d.Health().Writes
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if read()["DEV__Online"] != false {
			t.Fatal("a confirmed keep-alive brought back a source whose reads get 0x0B")
		}
		time.Sleep(time.Millisecond)
	}
	if d.Health().Writes < writes+5 {
		t.Fatalf("the keep-alive did not keep writing: %d writes before, %d after", writes, d.Health().Writes)
	}
	if got := read()["DEV__Outages"]; got != int64(1) {
		t.Fatalf("__Outages = %#v, want int64(1)", got)
	}
}

// scriptedGateway is a Modbus TCP server that answers each FC3 request
// from a script: 0 answers one register holding 7, anything else is that
// exception code. Past the script it answers. At each request it records
// __Online as the driver sees it, which on one connection with one block
// is the verdict after the previous reply.
type scriptedGateway struct {
	t      *testing.T
	ln     net.Listener
	script []byte
	online func() bool

	mu   sync.Mutex
	seen []bool // __Online at request i+1, i.e. after reply i
	n    int
}

func newScriptedGateway(t *testing.T, script []byte) *scriptedGateway {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	g := &scriptedGateway{t: t, ln: ln, script: script}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
			go g.serve(c)
		}
	}()
	return g
}

func (g *scriptedGateway) serve(c net.Conn) {
	for {
		var hdr [7]byte
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			return
		}
		body := make([]byte, int(binary.BigEndian.Uint16(hdr[4:6]))-1)
		if _, err := io.ReadFull(c, body); err != nil {
			return
		}
		g.mu.Lock()
		if g.n > 0 && g.online != nil {
			g.seen = append(g.seen, g.online())
		}
		code := byte(0)
		if g.n < len(g.script) {
			code = g.script[g.n]
		}
		g.n++
		g.mu.Unlock()

		fc := body[0]
		var pdu []byte
		if code != 0 {
			pdu = []byte{fc | 0x80, code}
		} else {
			pdu = []byte{fc, 2, 0, 7}
		}
		resp := make([]byte, 7, 7+len(pdu))
		copy(resp, hdr[:4])
		binary.BigEndian.PutUint16(resp[4:6], uint16(len(pdu)+1))
		resp[6] = hdr[6]
		if _, err := c.Write(append(resp, pdu...)); err != nil {
			return
		}
	}
}

// One or two gateway no-answers in a row (a lone RS-485 timeout behind the
// gateway) do not take a source offline; three do, and count one outage.
func TestOnlineGatewayNoAnswerIsDebounced(t *testing.T) {
	const ok, miss = 0, 0x0B
	script := []byte{
		ok,
		miss, ok,
		miss, miss, ok,
		miss, miss, miss, ok,
	}
	g := newScriptedGateway(t, script)
	m := Manifest{
		Sources: []Source{fastSource("DEV", g.ln.Addr().String())},
		Tags:    []TagBinding{{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"}},
	}
	d, err := New(m, WithScanRate(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	g.online = func() bool { v, _ := d.ReadInputs(); return v["DEV__Online"] == true }
	g.mu.Unlock()
	startDriver(t, d)
	waitFor(t, "the script played out", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return len(g.seen) >= len(script)
	})
	g.mu.Lock()
	seen := append([]bool(nil), g.seen[:len(script)]...)
	g.mu.Unlock()
	want := []bool{
		true,
		true, true,
		true, true, true,
		true, true, false, true,
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("__Online after reply %d (script %v) = %v, want %v; all: %v", i+1, script[:i+1], seen[i], want[i], seen)
		}
	}
	if v, _ := d.ReadInputs(); v["DEV__Outages"] != int64(1) {
		t.Fatalf("__Outages = %#v, want int64(1): only the three-in-a-row run is an outage", v["DEV__Outages"])
	}
}
