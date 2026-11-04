// foreign_test.go is the foreign-implementation suite: the real Driver
// against a pymodbus server (modbus/testdata/sim/pymodbus_sim.py) instead of
// our own slave, so the decoder, the block planner, write coalescing and the
// exception path are checked against somebody else's encoder and datastore.
// The register map lives in the sim's docstring; the tables here are its Go
// half and must agree with it byte for byte.
//
// Gated on NAUTILUS_MODBUS_SIM=host:port (scripts/modbus-sim.sh locally, the
// modbus-sim CI job) exactly like sparkplug's TCK conformance test — normal
// `go test ./...` skips it. The timeout case additionally needs a second sim
// started with --latency-ms, named by NAUTILUS_MODBUS_SIM_SLOW.
package modbus

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// simAddr returns the address in env or skips the test.
func simAddr(t *testing.T, env string) string {
	t.Helper()
	addr := os.Getenv(env)
	if addr == "" {
		t.Skipf("set %s=host:port (see scripts/modbus-sim.sh) to run the foreign-stack test", env)
	}
	return addr
}

// simUnits mirrors the sim's UNITS table: one unit id per word/byte order.
var simUnits = []struct {
	unit                 uint8
	wordOrder, byteOrder string
}{
	{1, OrderBig, OrderBig},
	{2, OrderLittle, OrderBig},
	{3, OrderBig, OrderLittle},
	{4, OrderLittle, OrderLittle},
}

// simSource is one unit of the sim as a Source, with test-speed backoff.
func simSource(addr string, unit uint8, wordOrder, byteOrder string) Source {
	s := fastSource(fmt.Sprintf("U%d", unit), addr)
	s.UnitID, s.WordOrder, s.ByteOrder = unit, wordOrder, byteOrder
	return s
}

// waitWithin is waitFor with a caller-chosen deadline (the slow sim answers
// one request per second).
func waitWithin(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// simReadMap is the sim's seeded map as bindings on one source, with the
// engineering value each must decode to. Every bit of the two bit-field
// registers and every coil/discrete input is bound individually.
func simReadMap(src string) ([]TagBinding, nio.Values) {
	p := src + "_"
	tags := []TagBinding{
		{Name: p + "H_I16", Source: src, Table: TableHolding, Address: 0, Format: "int16"},
		{Name: p + "H_U16", Source: src, Table: TableHolding, Address: 1, Format: "uint16"},
		{Name: p + "H_I32", Source: src, Table: TableHolding, Address: 2, Format: "int32"},
		{Name: p + "H_U32", Source: src, Table: TableHolding, Address: 4, Format: "uint32"},
		{Name: p + "H_F32", Source: src, Table: TableHolding, Address: 6, Format: "float32"},
		{Name: p + "H_F64", Source: src, Table: TableHolding, Address: 8, Format: "float64"},
		{Name: p + "H_BOOL", Source: src, Table: TableHolding, Address: 13, Format: "bool"},
		{Name: p + "H_SCALED", Source: src, Table: TableHolding, Address: 14, Format: "int16", Scale: 0.5, Offset: -5},
		{Name: p + "I_I16", Source: src, Table: TableInput, Address: 0, Format: "int16"},
		{Name: p + "I_U16", Source: src, Table: TableInput, Address: 1, Format: "uint16"},
		{Name: p + "I_I32", Source: src, Table: TableInput, Address: 2, Format: "int32"},
		{Name: p + "I_U32", Source: src, Table: TableInput, Address: 4, Format: "uint32"},
		{Name: p + "I_F32", Source: src, Table: TableInput, Address: 6, Format: "float32"},
		{Name: p + "I_F64", Source: src, Table: TableInput, Address: 8, Format: "float64"},
		{Name: p + "I_BOOL", Source: src, Table: TableInput, Address: 13, Format: "bool"},
		{Name: p + "I_SCALED", Source: src, Table: TableInput, Address: 14, Format: "uint16", Scale: 0.25, Offset: 1},
	}
	want := nio.Values{
		p + "H_I16": int64(-1234), p + "H_U16": int64(54321),
		p + "H_I32": int64(-123456789), p + "H_U32": int64(3000000000),
		p + "H_F32": float64(-1.5), p + "H_F64": float64(6.02214076e23),
		p + "H_BOOL": true, p + "H_SCALED": float64(120),
		p + "I_I16": int64(3210), p + "I_U16": int64(65535),
		p + "I_I32": int64(2147483647), p + "I_U32": int64(4000000000),
		p + "I_F32": float64(3.25), p + "I_F64": float64(-2.5),
		p + "I_BOOL": false, p + "I_SCALED": float64(2.75),
	}
	const hBits, iBits = 0xA5C3, 0x5A3C // holding/input 12, coils/discrete 0..15
	for bit := 0; bit < 16; bit++ {
		hb, ib := hBits>>bit&1 == 1, iBits>>bit&1 == 1
		n := fmt.Sprintf("%s_H_BIT%d", src, bit)
		tags = append(tags, TagBinding{Name: n, Source: src, Table: TableHolding, Address: 12, Format: fmt.Sprintf("bit:%d", bit)})
		want[n] = hb
		n = fmt.Sprintf("%s_I_BIT%d", src, bit)
		tags = append(tags, TagBinding{Name: n, Source: src, Table: TableInput, Address: 12, Format: fmt.Sprintf("bit:%d", bit)})
		want[n] = ib
		n = fmt.Sprintf("%s_C%d", src, bit)
		tags = append(tags, TagBinding{Name: n, Source: src, Table: TableCoil, Address: uint16(bit)})
		want[n] = hb
		n = fmt.Sprintf("%s_D%d", src, bit)
		tags = append(tags, TagBinding{Name: n, Source: src, Table: TableDiscrete, Address: uint16(bit)})
		want[n] = ib
	}
	return tags, want
}

// Every seeded value decodes to the sim's engineering value on all four
// tables and in all four word/byte layouts — pymodbus's struct-packed
// registers and our decoder agree — and the planner turns each table into
// exactly one request.
func TestForeignDecodesEverySeededValue(t *testing.T) {
	addr := simAddr(t, "NAUTILUS_MODBUS_SIM")
	var m Manifest
	want := nio.Values{}
	for _, u := range simUnits {
		src := simSource(addr, u.unit, u.wordOrder, u.byteOrder)
		m.Sources = append(m.Sources, src)
		tags, w := simReadMap(src.ID)
		m.Tags = append(m.Tags, tags...)
		for k, v := range w {
			want[k] = v
		}
		want[src.OnlineTagName()] = true
		want[src.OutagesTagName()] = int64(0)
	}
	d, err := New(m, WithScanRate(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(d.Plan().Blocks); got != 4*len(simUnits) {
		t.Fatalf("plan has %d blocks, want one per table per unit (%d):\n%s", got, 4*len(simUnits), d.Plan())
	}
	startDriver(t, d)

	var got nio.Values
	waitFor(t, "every tag delivered from all four units", func() bool {
		v, err := d.ReadInputs()
		if err != nil {
			return false
		}
		got = v
		return len(v) == len(want)
	})
	for name, w := range want {
		if g := got[name]; g != w {
			t.Errorf("%s = %v (%T), want %v (%T)", name, g, g, w, w)
		}
	}
	if q := d.Quality(); len(q) != 0 {
		t.Errorf("all-Good expected, got %v", q)
	}
	for _, row := range d.Health().Sources {
		if row.State != "connected" || row.Exceptions != 0 || row.BadBlocks != 0 {
			t.Errorf("source %s: %+v", row.ID, row)
		}
	}
}

// Every writable format round-trips through pymodbus's datastore in every
// layout: WriteOutputs → FC16/FC6/FC5/FC15 on the wire → the driver's own
// poll reads the value back. Two rounds with different values so a stale
// datastore from an earlier run cannot satisfy the read-back.
func TestForeignWriteRoundTrip(t *testing.T) {
	addr := simAddr(t, "NAUTILUS_MODBUS_SIM")
	rec := &recorder{}
	var m Manifest
	for _, u := range simUnits {
		src := simSource(addr, u.unit, u.wordOrder, u.byteOrder)
		m.Sources = append(m.Sources, src)
		p := src.ID + "_"
		m.Tags = append(m.Tags,
			TagBinding{Name: p + "W_I16", Source: src.ID, Table: TableHolding, Address: 200, Format: "int16", Writable: true},
			TagBinding{Name: p + "W_U16", Source: src.ID, Table: TableHolding, Address: 201, Format: "uint16", Writable: true},
			TagBinding{Name: p + "W_I32", Source: src.ID, Table: TableHolding, Address: 202, Format: "int32", Writable: true},
			TagBinding{Name: p + "W_U32", Source: src.ID, Table: TableHolding, Address: 204, Format: "uint32", Writable: true},
			TagBinding{Name: p + "W_F32", Source: src.ID, Table: TableHolding, Address: 206, Format: "float32", Writable: true},
			TagBinding{Name: p + "W_F64", Source: src.ID, Table: TableHolding, Address: 208, Format: "float64", Writable: true},
			TagBinding{Name: p + "W_SCALED", Source: src.ID, Table: TableHolding, Address: 212, Format: "int16", Scale: 0.5, Offset: -5, Writable: true},
			TagBinding{Name: p + "W_BOOL", Source: src.ID, Table: TableHolding, Address: 213, Format: "bool", Writable: true},
			TagBinding{Name: p + "W_BIT3", Source: src.ID, Table: TableHolding, Address: 214, Format: "bit:3", Writable: true},
			TagBinding{Name: p + "W_BIT9", Source: src.ID, Table: TableHolding, Address: 214, Format: "bit:9", Writable: true},
			TagBinding{Name: p + "W_WORD", Source: src.ID, Table: TableHolding, Address: 214, Format: "uint16"},
			TagBinding{Name: p + "W_C0", Source: src.ID, Table: TableCoil, Address: 100, Writable: true},
			TagBinding{Name: p + "W_C1", Source: src.ID, Table: TableCoil, Address: 101, Writable: true},
			TagBinding{Name: p + "W_C5", Source: src.ID, Table: TableCoil, Address: 105, Writable: true},
		)
	}
	d, err := New(m, WithScanRate(30*time.Millisecond), WithDialer(recordingDialer(rec)))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "all units connected", func() bool {
		for _, row := range d.Health().Sources {
			if row.State != "connected" {
				return false
			}
		}
		return true
	})

	// perUnit fans one value set out to every source's tag names.
	perUnit := func(vals nio.Values) nio.Values {
		out := nio.Values{}
		for _, s := range m.Sources {
			for k, v := range vals {
				out[s.ID+"_"+k] = v
			}
		}
		return out
	}
	// The first snapshot after Start is a baseline: recorded, never sent.
	if err := d.WriteOutputs(perUnit(nio.Values{
		"W_I16": float64(0), "W_U16": float64(0), "W_I32": float64(0), "W_U32": float64(0),
		"W_F32": float64(0), "W_F64": float64(0), "W_SCALED": float64(-5), "W_BOOL": false,
		"W_BIT3": false, "W_BIT9": false, "W_C0": false, "W_C1": false, "W_C5": false,
	})); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	for _, fc := range []byte{FCWriteSingleRegister, FCWriteMultipleRegisters, FCWriteSingleCoil, FCWriteMultipleCoils} {
		if got := rec.writes(fc, -1); len(got) != 0 {
			t.Fatalf("baseline must write nothing; saw FC%d frames %v", fc, got)
		}
	}

	rounds := []struct {
		write nio.Values // engineering values handed to WriteOutputs
		want  nio.Values // what the poll must read back (W_WORD is the RMW'd register)
	}{
		{
			write: nio.Values{
				"W_I16": float64(-32768), "W_U16": float64(65535), "W_I32": float64(-2000000000),
				"W_U32": float64(4000000001), "W_F32": float64(2.5), "W_F64": float64(1234.5678),
				"W_SCALED": float64(33), "W_BOOL": true, "W_BIT3": true, "W_BIT9": true,
				"W_C0": true, "W_C1": true, "W_C5": true,
			},
			want: nio.Values{
				"W_I16": int64(-32768), "W_U16": int64(65535), "W_I32": int64(-2000000000),
				"W_U32": int64(4000000001), "W_F32": float64(2.5), "W_F64": float64(1234.5678),
				"W_SCALED": float64(33), "W_BOOL": true, "W_BIT3": true, "W_BIT9": true,
				"W_WORD": int64(1<<3 | 1<<9), "W_C0": true, "W_C1": true, "W_C5": true,
			},
		},
		{
			write: nio.Values{
				"W_I16": float64(32767), "W_U16": float64(1), "W_I32": float64(123456789),
				"W_U32": float64(7), "W_F32": float64(-0.75), "W_F64": float64(-9.75),
				"W_SCALED": float64(-4.5), "W_BOOL": false, "W_BIT3": false, "W_BIT9": true,
				"W_C0": false, "W_C1": false, "W_C5": false,
			},
			want: nio.Values{
				"W_I16": int64(32767), "W_U16": int64(1), "W_I32": int64(123456789),
				"W_U32": int64(7), "W_F32": float64(-0.75), "W_F64": float64(-9.75),
				"W_SCALED": float64(-4.5), "W_BOOL": false, "W_BIT3": false, "W_BIT9": true,
				"W_WORD": int64(1 << 9), "W_C0": false, "W_C1": false, "W_C5": false,
			},
		},
	}
	for i, r := range rounds {
		if err := d.WriteOutputs(perUnit(r.write)); err != nil {
			t.Fatal(err)
		}
		want := perUnit(r.want)
		waitFor(t, fmt.Sprintf("round %d read-back", i), func() bool {
			v, _ := d.ReadInputs()
			for k, w := range want {
				if v[k] != w {
					return false
				}
			}
			return true
		})
	}

	// The wire shapes, per unit. Round 1: 200..213 is one FC16 of 14
	// registers, the two bit:N writes are two FC6 read-modify-writes of 214,
	// coils 100+101 one FC15 of 2, coil 105 one FC5. Round 2 adds the same
	// again except the unchanged W_BIT9 (change-only).
	//
	// What is asserted is the SHAPE of each frame (never split, never
	// merged) and that every unit sent at least one, not an exact count of
	// rounds: the driver queues last-value-wins and re-sends a write whose
	// connection broke mid-flight, so how many frames carried the two rounds
	// depends on timing (a loaded runner has seen both more and fewer than
	// rounds*units). The read-back above already proves every value landed.
	n := len(simUnits)
	atLeast := func(what string, got []frameRec, perUnit int, count uint16) {
		t.Helper()
		if len(got) < perUnit*n {
			t.Errorf("%s: want at least %d frames (%d per unit), got %d: %v", what, perUnit*n, perUnit, len(got), got)
		}
		for _, f := range got {
			if count != 0 && f.count != count {
				t.Errorf("%s: a frame carried %d, want %d (split or merged): %v", what, f.count, count, got)
				break
			}
		}
	}
	atLeast("contiguous holding writes must be one FC16 of 14 registers",
		rec.writes(FCWriteMultipleRegisters, 200), 1, 14)
	atLeast("bit:N writes must be read-modify-write FC6",
		rec.writes(FCWriteSingleRegister, 214), 1, 0)
	atLeast("adjacent coils must be one FC15 of 2",
		rec.writes(FCWriteMultipleCoils, 100), 1, 2)
	atLeast("a lone coil must be FC5", rec.writes(FCWriteSingleCoil, 105), 1, 0)
	if got := rec.writes(FCWriteMultipleRegisters, -1); len(got) != len(rec.writes(FCWriteMultipleRegisters, 200)) {
		t.Errorf("an FC16 started somewhere other than 200: %v", got)
	}
	for _, row := range d.Health().Sources {
		if row.Exceptions != 0 || row.QueuedWrites != 0 {
			t.Errorf("source %s: %+v", row.ID, row)
		}
	}
}

// A block that lands on the sim's unimplemented range answers exception
// 0x02: that block's tags are Bad and the source's other blocks stay Good,
// online, and polling — the per-block failure, not a reconnect.
func TestForeignExceptionMarksOnlyThatBlock(t *testing.T) {
	addr := simAddr(t, "NAUTILUS_MODBUS_SIM")
	src := simSource(addr, 1, OrderBig, OrderBig)
	tags, want := simReadMap(src.ID)
	m := Manifest{
		Sources: []Source{src},
		Tags: append(tags,
			TagBinding{Name: "ABSENT_REG", Source: src.ID, Table: TableHolding, Address: 1000, Format: "uint16"},
			TagBinding{Name: "ABSENT_COIL", Source: src.ID, Table: TableCoil, Address: 1000},
		),
	}
	d, err := New(m, WithScanRate(30*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)

	waitFor(t, "good blocks delivered, absent blocks Bad", func() bool {
		v, _ := d.ReadInputs()
		q := d.Quality()
		// The tags plus the source's two companions, __Online and __Outages.
		return len(v) == len(want)+2 && q["ABSENT_REG"] == nio.Bad && q["ABSENT_COIL"] == nio.Bad
	})
	v, _ := d.ReadInputs()
	for name, w := range want {
		if v[name] != w {
			t.Errorf("%s = %v, want %v", name, v[name], w)
		}
	}
	if v[src.OnlineTagName()] != true {
		t.Error("an exception must not take the source offline")
	}
	if v[src.OutagesTagName()] != int64(0) {
		t.Errorf("an exception is not an outage: %s = %v", src.OutagesTagName(), v[src.OutagesTagName()])
	}
	if _, ok := v["ABSENT_REG"]; ok {
		t.Error("a never-answered tag must stay absent, not read as zero")
	}
	q := d.Quality()
	if len(q) != 2 {
		t.Errorf("only the two absent tags may be non-Good, got %v", q)
	}
	h := d.Health().Sources[0]
	if h.State != "connected" || h.BadBlocks != 2 || h.Exceptions < 2 || h.Retries != 0 ||
		!strings.Contains(h.LastError, "exception 0x02") {
		t.Errorf("Health = %+v", h)
	}
}

// Against a sim that delays every request (--latency-ms 1000): a source
// whose Timeout exceeds the latency still delivers, and one whose Timeout is
// below it never delivers — each request times out as a TRANSPORT error (not
// an exception), the loop drops the connection and re-dials, Retries and
// Errors climb, the tag stays absent rather than reading zero, and the
// source is never online (it never answered). Needs
// NAUTILUS_MODBUS_SIM_SLOW.
//
// The re-dial succeeds at once (the sim accepts TCP promptly), so the source
// oscillates connected → error → connected every Timeout; State, LastError
// and Quality are therefore phase-dependent and the assertions stick to the
// counters, which only ever climb.
func TestForeignLatencyAboveTimeoutReconnects(t *testing.T) {
	addr := simAddr(t, "NAUTILUS_MODBUS_SIM_SLOW")
	tag := TagBinding{Name: "PV", Source: "U1", Table: TableHolding, Address: 1, Format: "uint16"}

	t.Run("latency below timeout delivers", func(t *testing.T) {
		src := simSource(addr, 1, OrderBig, OrderBig)
		src.Timeout = 3 * time.Second
		d, err := New(Manifest{Sources: []Source{src}, Tags: []TagBinding{tag}}, WithScanRate(100*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		startDriver(t, d)
		waitWithin(t, 15*time.Second, "slow delivery", func() bool {
			v, _ := d.ReadInputs()
			return v["PV"] == int64(54321) && v["U1__Online"] == true
		})
		if h := d.Health().Sources[0]; h.Retries != 0 || h.RTTMs < 500 {
			t.Errorf("Health = %+v (want no retries and a ~1s round-trip)", h)
		}
	})

	t.Run("latency above timeout never delivers", func(t *testing.T) {
		src := simSource(addr, 1, OrderBig, OrderBig)
		src.Timeout = 200 * time.Millisecond
		src.RetryMin, src.RetryMax = 100*time.Millisecond, 400*time.Millisecond
		d, err := New(Manifest{Sources: []Source{src}, Tags: []TagBinding{tag}}, WithScanRate(100*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		startDriver(t, d)
		waitWithin(t, 15*time.Second, "repeated timeouts", func() bool {
			v, _ := d.ReadInputs()
			if _, ok := v["PV"]; ok {
				t.Fatalf("PV must never be delivered through a timed-out request, got %v", v["PV"])
			}
			// Connected is not online: the sim never answers in time (#539).
			if v["U1__Online"] == true {
				t.Fatal("U1__Online went true though no request was answered")
			}
			return d.Health().Sources[0].Retries >= 3
		})
		if v, _ := d.ReadInputs(); v["U1__Outages"] != int64(0) {
			t.Errorf("never answered, so never an outage: U1__Outages = %#v", v["U1__Outages"])
		}
		// Never delivered means NotConnected whichever phase of the
		// reconnect oscillation this lands in (socket up but unheard-from,
		// or down) — never Good, Stale or Bad.
		if q, ok := d.Quality()["PV"]; !ok || q != nio.NotConnected {
			t.Errorf("PV quality = %v (reported=%v); a never-delivered tag must be NotConnected", q, ok)
		}
		h := d.Health()
		row := h.Sources[0]
		if row.Exceptions != 0 || row.RTTMs != 0 || h.Errors < 3 || h.Reads != 0 {
			t.Errorf("Health = %+v / %+v (want ≥3 transport errors, no exceptions, no completed read)", h, row)
		}
	})
}
