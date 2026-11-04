package modbus

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus/slave"
)

// gwManifest has one write of every shape a flush sends, each on its own
// address so an exception can be injected for it alone:
//
//	R1,R2  holding 100,101  one FC16 run
//	S      holding 200      FC6
//	B0,B1  holding 300      two bit RMWs (FC3 + FC6) on one register,
//	BX     holding 300      and a sibling bit commanded earlier
//	L      holding 400      FC6 that is never answered with an exception
//	K      coil 10          FC5
//	KA,KB  coils 20,21      one FC15 run
//	CTL    coil 99          sent last in every flush: when it lands, the
//	                        flush is over
func gwManifest(addr string) Manifest {
	return Manifest{
		Sources: []Source{fastSource("DEV", addr)},
		Tags: []TagBinding{
			{Name: "R1", Source: "DEV", Table: TableHolding, Address: 100, Format: "uint16", Writable: true},
			{Name: "R2", Source: "DEV", Table: TableHolding, Address: 101, Format: "uint16", Writable: true},
			{Name: "S", Source: "DEV", Table: TableHolding, Address: 200, Format: "uint16", Writable: true},
			{Name: "B0", Source: "DEV", Table: TableHolding, Address: 300, Format: "bit:0", Writable: true},
			{Name: "B1", Source: "DEV", Table: TableHolding, Address: 300, Format: "bit:1", Writable: true},
			{Name: "BX", Source: "DEV", Table: TableHolding, Address: 300, Format: "bit:2", Writable: true},
			{Name: "L", Source: "DEV", Table: TableHolding, Address: 400, Format: "uint16", Writable: true},
			{Name: "K", Source: "DEV", Table: TableCoil, Address: 10, Writable: true},
			{Name: "KA", Source: "DEV", Table: TableCoil, Address: 20, Writable: true},
			{Name: "KB", Source: "DEV", Table: TableCoil, Address: 21, Writable: true},
			{Name: "CTL", Source: "DEV", Table: TableCoil, Address: 99, Writable: true},
		},
	}
}

// gwInject answers code to every write in gwManifest except L's and CTL's,
// and to nothing else (the bit RMWs' FC3 reads are answered).
func gwInject(u *slave.Unit, code byte) {
	u.InjectExceptionAt(FCWriteMultipleRegisters, 100, code)
	u.InjectExceptionAt(FCWriteSingleRegister, 200, code)
	u.InjectExceptionAt(FCWriteSingleRegister, 300, code)
	u.InjectExceptionAt(FCWriteSingleCoil, 10, code)
	u.InjectExceptionAt(FCWriteMultipleCoils, 20, code)
}

// gwRound hands vals to the driver with CTL set to ctl and waits until the
// flush that carries them is over.
func gwRound(t *testing.T, d *Driver, u *slave.Unit, vals nio.Values, ctl bool) {
	t.Helper()
	out := nio.Values{"CTL": ctl}
	for k, v := range vals {
		out[k] = v
	}
	if err := d.WriteOutputs(out); err != nil {
		t.Fatal(err)
	}
	waitFor(t, fmt.Sprintf("the flush with CTL=%v", ctl), func() bool { return u.Coil(99) == ctl })
}

// gwSetup starts a driver on gwManifest with the recording dialer, records
// the all-zero baseline and lands the sibling bit BX.
func gwSetup(t *testing.T) (*Driver, *slave.Unit, *recorder) {
	t.Helper()
	srv := newSlave(t)
	u := srv.Unit(1)
	rec := &recorder{}
	d, err := New(gwManifest(srv.Addr()), WithScanRate(20*time.Millisecond), WithDialer(recordingDialer(rec)))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	base := nio.Values{"R1": float64(0), "R2": float64(0), "S": float64(0), "B0": false, "B1": false,
		"BX": false, "L": float64(0), "K": false, "KA": false, "KB": false, "CTL": false}
	if err := d.WriteOutputs(base); err != nil { // baseline: recorded, not sent
		t.Fatal(err)
	}
	gwRound(t, d, u, nio.Values{"BX": true}, true)
	if v := u.Holding(300); v != 0b100 {
		t.Fatalf("holding 300 = %#b after the sibling bit's command, want 0b100", v)
	}
	return d, u, rec
}

// gwCmd is the command snapshot of every test below: every tag but CTL
// changed from the baseline.
var gwCmd = nio.Values{"R1": float64(11), "R2": float64(12), "S": float64(21), "B0": true, "B1": true,
	"BX": true, "L": float64(41), "K": true, "KA": true, "KB": true}

// gwFrames counts the write frames per address that gwInject targets, plus
// L's.
type gwFrames struct{ fc16_100, fc6_200, fc6_300, fc6_400, fc5_10, fc15_20 int }

func gwCount(rec *recorder) gwFrames {
	return gwFrames{
		fc16_100: len(rec.writes(FCWriteMultipleRegisters, 100)),
		fc6_200:  len(rec.writes(FCWriteSingleRegister, 200)),
		fc6_300:  len(rec.writes(FCWriteSingleRegister, 300)),
		fc6_400:  len(rec.writes(FCWriteSingleRegister, 400)),
		fc5_10:   len(rec.writes(FCWriteSingleCoil, 10)),
		fc15_20:  len(rec.writes(FCWriteMultipleCoils, 20)),
	}
}

// A write a gateway answers with 0x0A (path unavailable) or 0x0B (target
// device failed to respond) never reached the device behind it (issue
// #596). It is dropped from the queue and forgotten, so the next
// WriteOutputs with the same value sends it again: an FC16 run, an FC6, an
// FC5, an FC15 and a bit RMW alike. Only the tags whose write was not
// delivered are forgotten: a write in the same flush that landed (L), and
// a sibling bit on the RMW'd register commanded earlier (BX), are not sent
// again. Each undelivered write is counted once as an exception.
func TestGatewayNoAnswerWriteIsResent(t *testing.T) {
	for _, code := range []byte{0x0A, 0x0B} {
		t.Run(fmt.Sprintf("%#02x", code), func(t *testing.T) {
			d, u, rec := gwSetup(t)
			excs0 := d.Health().Sources[0].Exceptions

			gwInject(u, code)
			gwRound(t, d, u, gwCmd, false)
			before := gwCount(rec)
			// 1 FC16 + 1 FC6@200 + 2 bit RMWs + 1 FC5 + 1 FC15.
			if got := d.Health().Sources[0].Exceptions - excs0; got != 6 {
				t.Errorf("exceptions counted for the undelivered writes = %d, want 6", got)
			}
			if u.Holding(400) != 41 {
				t.Fatalf("L must land in the same flush, holding 400 = %d", u.Holding(400))
			}

			// The gateway path is back; the program's values have not moved.
			gwInject(u, 0)
			gwRound(t, d, u, gwCmd, true)
			after := gwCount(rec)

			if after.fc16_100 != before.fc16_100+1 || u.Holding(100) != 11 || u.Holding(101) != 12 {
				t.Errorf("FC16 run at 100 not re-sent: %d → %d frames, holding = %d,%d",
					before.fc16_100, after.fc16_100, u.Holding(100), u.Holding(101))
			}
			if after.fc6_200 != before.fc6_200+1 || u.Holding(200) != 21 {
				t.Errorf("FC6 at 200 not re-sent: %d → %d frames, holding 200 = %d",
					before.fc6_200, after.fc6_200, u.Holding(200))
			}
			if after.fc5_10 != before.fc5_10+1 || !u.Coil(10) {
				t.Errorf("FC5 at coil 10 not re-sent: %d → %d frames, coil = %v",
					before.fc5_10, after.fc5_10, u.Coil(10))
			}
			if after.fc15_20 != before.fc15_20+1 || !u.Coil(20) || !u.Coil(21) {
				t.Errorf("FC15 run at coil 20 not re-sent: %d → %d frames, coils = %v,%v",
					before.fc15_20, after.fc15_20, u.Coil(20), u.Coil(21))
			}
			// B0 and B1 each get their RMW again; BX, landed earlier, does not.
			if after.fc6_300 != before.fc6_300+2 || u.Holding(300) != 0b111 {
				t.Errorf("bit RMWs at 300: %d → %d frames (want +2: B0 and B1, not BX), holding 300 = %#b (want 0b111)",
					before.fc6_300, after.fc6_300, u.Holding(300))
			}
			if after.fc6_400 != before.fc6_400 {
				t.Errorf("L landed and must not be re-sent: %d → %d frames", before.fc6_400, after.fc6_400)
			}
			if q := d.Health().Sources[0].QueuedWrites; q != 0 {
				t.Errorf("queue must drain, %d left", q)
			}

			// Delivered now: the same values again are not commands.
			gwRound(t, d, u, gwCmd, false)
			if again := gwCount(rec); again != after {
				t.Errorf("delivered values were sent again: %+v → %+v", after, again)
			}

			// A changed value goes out as always.
			gwRound(t, d, u, nio.Values{"R1": float64(13), "S": float64(23), "B0": false, "K": false}, true)
			if u.Holding(100) != 13 || u.Holding(200) != 23 || u.Holding(300) != 0b110 || u.Coil(10) {
				t.Errorf("changed values must land: holding 100=%d 200=%d 300=%#b coil 10=%v",
					u.Holding(100), u.Holding(200), u.Holding(300), u.Coil(10))
			}
		})
	}
}

// A write the device itself refuses (0x02 illegal address, 0x03 illegal
// value) is dropped and NOT re-sent while the program's value stands: the
// device answered, and sending it again would only be refused again
// (today's behaviour, kept by #596). A changed value still goes out.
func TestRefusedWriteIsNotResent(t *testing.T) {
	for _, code := range []byte{0x02, 0x03} {
		t.Run(fmt.Sprintf("%#02x", code), func(t *testing.T) {
			d, u, rec := gwSetup(t)

			gwInject(u, code)
			gwRound(t, d, u, gwCmd, false)
			before := gwCount(rec)

			gwInject(u, 0)
			gwRound(t, d, u, gwCmd, true)
			if after := gwCount(rec); after != before {
				t.Errorf("a refused write with an unchanged value was sent again: %+v → %+v", before, after)
			}
			if u.Holding(100) != 0 || u.Holding(200) != 0 || u.Holding(300) != 0b100 || u.Coil(10) || u.Coil(20) {
				t.Errorf("refused values must not have landed: holding 100=%d 200=%d 300=%#b coils 10=%v 20=%v",
					u.Holding(100), u.Holding(200), u.Holding(300), u.Coil(10), u.Coil(20))
			}

			// A changed value is a command; its unchanged neighbour in the
			// refused run (R2) still is not.
			gwRound(t, d, u, nio.Values{"R1": float64(13), "S": float64(23), "K": false}, false)
			if u.Holding(100) != 13 || u.Holding(101) != 0 || u.Holding(200) != 23 || u.Coil(10) {
				t.Errorf("changed values must land, R2 must not: holding 100=%d 101=%d 200=%d coil 10=%v",
					u.Holding(100), u.Holding(101), u.Holding(200), u.Coil(10))
			}
		})
	}
}

// A write that got 0x0B while the write gate then closes is a command again
// once the gate reopens, as a command #595's gate drop forgot is: nothing
// goes out while the gate is closed, and the first WriteOutputs after it
// reopens sends the program's value.
func TestGatewayNoAnswerWriteWaitsForTheGate(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	rec := &recorder{}
	d, err := New(gwManifest(srv.Addr()), WithScanRate(20*time.Millisecond), WithDialer(recordingDialer(rec)))
	if err != nil {
		t.Fatal(err)
	}
	var open atomic.Bool
	open.Store(true)
	d.SetWriteGate(open.Load)
	startDriver(t, d)
	if err := d.WriteOutputs(nio.Values{"S": float64(0), "CTL": false}); err != nil { // baseline
		t.Fatal(err)
	}

	u.InjectExceptionAt(FCWriteSingleRegister, 200, 0x0B)
	gwRound(t, d, u, nio.Values{"S": float64(21)}, true)
	u.InjectExceptionAt(FCWriteSingleRegister, 200, 0)
	sent := len(rec.writes(FCWriteSingleRegister, 200))

	open.Store(false)
	if err := d.WriteOutputs(nio.Values{"S": float64(21), "CTL": true}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // five scan periods with the gate closed
	if n := len(rec.writes(FCWriteSingleRegister, 200)); n != sent {
		t.Fatalf("a closed gate let the undelivered command through: %d → %d frames", sent, n)
	}

	open.Store(true)
	gwRound(t, d, u, nio.Values{"S": float64(21)}, false)
	if n := len(rec.writes(FCWriteSingleRegister, 200)); n != sent+1 || u.Holding(200) != 21 {
		t.Errorf("reopened gate: the undelivered command must go out once: %d → %d frames, holding 200 = %d",
			sent, n, u.Holding(200))
	}
}

// A rewrite: binding whose write gets 0x0B keeps its value: its own
// re-assert sends it again on its period, with no WriteOutputs call, and
// an unchanged WriteOutputs adds nothing on top of the re-asserts.
// Forgetting it would stop the keep-alive until the program changed it.
func TestGatewayNoAnswerKeepsRewriteAsserting(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	rec := &recorder{}
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{
			{Name: "KW", Source: "DEV", Table: TableHolding, Address: 500, Format: "uint16", Writable: true, Rewrite: 40 * time.Millisecond},
		},
	}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(recordingDialer(rec)))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	u.InjectExceptionAt(FCWriteSingleRegister, 500, 0x0B)
	if err := d.WriteOutputs(nio.Values{"KW": float64(5)}); err != nil { // a rewrite baseline is sent
		t.Fatal(err)
	}
	waitFor(t, "two undelivered re-asserts", func() bool { return len(rec.writes(FCWriteSingleRegister, 500)) >= 2 })
	u.InjectExceptionAt(FCWriteSingleRegister, 500, 0)
	waitFor(t, "the re-assert lands", func() bool { return u.Holding(500) == 5 })

	// An unchanged value between re-asserts is not one more command: over
	// ten periods there are about ten frames, not twenty.
	n0 := len(rec.writes(FCWriteSingleRegister, 500))
	for range 10 {
		if err := d.WriteOutputs(nio.Values{"KW": float64(5)}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(40 * time.Millisecond)
	}
	if n := len(rec.writes(FCWriteSingleRegister, 500)) - n0; n > 14 {
		t.Errorf("%d frames in ten re-assert periods: an unchanged value must not add to the re-asserts", n)
	}
}
