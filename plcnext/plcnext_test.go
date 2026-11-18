package plcnext

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/plcnext/plcnexttest"

	nio "github.com/joyautomation/nautilus/io"
	gdspb "github.com/joyautomation/nautilus/plcnext/gen/plc/gds"
)

// wrote is every write the fake took, space-separated, in order.
func wrote(g *plcnexttest.GDS) string { return strings.Join(g.Writes(), " ") }

const benchManifest = `
bus-conductor: BcComponent1
modules:
  - {slot: 1, part: "AXL F DI16/4 1F", in: 2}
  - {slot: 2, part: "AXL F DO16/3 1F", out: 2}
  - {slot: 4, part: "AXL F AI2 AO2 1H", in: 4, out: 4}
tags:
  - {name: DI_00, dir: in, offset: 0, format: "bit:0"}
  - {name: DI_15, dir: in, offset: 0, format: "bit:15"}
  - {name: AI_00, dir: in, offset: 2, format: int16, scale: 0.001, unit: V}
  - {name: DO_00, dir: out, offset: 0, format: "bit:0", init: false}
  - {name: DO_15, dir: out, offset: 0, format: "bit:15", init: false}
  - {name: AO_00, dir: out, offset: 2, format: int16, scale: 0.001, init: 0.0}
`

func TestManifestParsesWithDefaults(t *testing.T) {
	m, err := ParseManifest([]byte(benchManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Socket != DefaultSocket || m.Inputs != DefaultInputs || m.Outputs != DefaultOutputs || m.RunPort != DefaultRunPort || m.ByteOrder != "big" {
		t.Fatalf("defaults: %+v", m)
	}
	if got := m.InputNames(); len(got) != 3 || got[0] != "AI_00" {
		t.Fatalf("inputs: %v", got)
	}
	if in, out := m.span(DirIn), m.span(DirOut); in != 4 || out != 4 {
		t.Fatalf("spans in=%d out=%d, want 4 and 4", in, out)
	}
	if _, err := ParseManifest([]byte("tags:\n  - {name: X, dir: sideways, offset: 600, format: bit:19}\n")); err == nil {
		t.Fatal("bad manifest accepted")
	} else if s := err.Error(); !strings.Contains(s, "sideways") || !strings.Contains(s, "600") || !strings.Contains(s, "bit:19") {
		t.Fatalf("errors not all reported: %v", err)
	}
	if _, err := ParseManifest([]byte("tags:\n  - {name: X, dir: in, offset: 0, format: int16, nope: 1}\n")); err == nil {
		t.Fatal("unknown key accepted")
	}
	m2, err := ParseManifest([]byte("run-port: none\ntags:\n  - {name: X, dir: in, offset: 0, format: int16}\n"))
	if err != nil || m2.RunPort != "" {
		t.Fatalf("run-port: none should disable the check: %q %v", m2.RunPort, err)
	}
}

func TestCodec(t *testing.T) {
	img := make([]byte, 8)
	img[0], img[1] = 0x80, 0x01 // big-endian word 0x8001: bits 15 and 0 set
	img[2], img[3] = 0x04, 0xD2 // 1234
	for _, c := range []struct {
		b    Binding
		want any
	}{
		{Binding{Format: "bit:0", Offset: 0}, true},
		{Binding{Format: "bit:15", Offset: 0}, true},
		{Binding{Format: "bit:7", Offset: 0}, false},
		{Binding{Format: "int16", Offset: 2, Scale: 0.001}, 1.234},
		{Binding{Format: "uint16", Offset: 0}, 32769.0},
	} {
		got, err := decode(c.b, img, "big")
		if err != nil || got != c.want {
			t.Errorf("decode %s@%d = %v %v, want %v", c.b.Format, c.b.Offset, got, err, c.want)
		}
	}
	if v, _ := decode(Binding{Format: "int16", Offset: 2}, img, "little"); v != float64(int16(-11772)) { // 0xD204 as int16
		t.Errorf("little-endian int16 = %v", v)
	}
	out := make([]byte, 8)
	if err := encode(Binding{Format: "bit:15", Offset: 0}, true, out, "big"); err != nil || out[0] != 0x80 {
		t.Errorf("bit:15 → % x %v", out[:2], err)
	}
	if err := encode(Binding{Format: "bit:0", Offset: 0}, true, out, "big"); err != nil || out[1] != 0x01 || out[0] != 0x80 {
		t.Errorf("bit:0 merge → % x", out[:2])
	}
	if err := encode(Binding{Format: "bit:15", Offset: 0}, false, out, "big"); err != nil || out[0] != 0x00 {
		t.Errorf("bit:15 clear → % x", out[:2])
	}
	if err := encode(Binding{Format: "int16", Offset: 2, Scale: 0.001}, 5.0, out, "big"); err != nil || out[2] != 0x13 || out[3] != 0x88 {
		t.Errorf("int16 5.0/0.001 → % x", out[2:4])
	}
	if err := encode(Binding{Format: "uint8", Offset: 4}, 300.0, out, "big"); err != nil || out[4] != 255 {
		t.Errorf("uint8 clamp → %d", out[4])
	}
}

func TestDriverAgainstFakeFirmware(t *testing.T) {
	f := plcnexttest.New()
	addr := SocketTarget(f.Listen(t))
	m, err := ParseManifest([]byte(benchManifest))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m, WithScanRate(10*time.Millisecond), WithDialer(func(ctx context.Context) (dataAccess, error) {
		return Dial(ctx, addr)
	}))
	if err != nil {
		t.Fatal(err)
	}
	if q := d.Quality()["DI_00"]; q != nio.NotConnected {
		t.Fatalf("before Start: %v", q)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	f.SetInput(0, 0x80) // DI_15
	f.SetInput(1, 0x01) // DI_00
	f.SetInput(2, 0x04)
	f.SetInput(3, 0xD2) // AI_00 = 1.234 V
	var in nio.Values
	deadline := time.Now().Add(5 * time.Second)
	for {
		in, _ = d.ReadInputs()
		if in["DI_00"] == true && in["DI_15"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("inputs never arrived: %v (health %+v)", in, d.Health())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if v, _ := in["AI_00"].(float64); v < 1.2339 || v > 1.2341 {
		t.Errorf("AI_00 = %v", in["AI_00"])
	}
	if q := d.Quality()["AI_00"]; q != nio.Good {
		t.Errorf("quality %v", q)
	}
	h := d.Health()
	if !h.Connected || !h.Configured || !h.Running || h.Modules != 4 || h.InBytes != 4 || h.OutBytes != 4 {
		t.Errorf("health %+v", h)
	}
	// The handshake gave each request a held rising edge and left it high.
	joined := wrote(f)
	for _, p := range []string{"CONFIG_MUST_MATCH=", "CONFIG_REQ=", "START_IO_REQ="} {
		if !strings.Contains(joined, p) {
			t.Errorf("handshake never wrote %s: %s", p, joined)
		}
	}
	if lo, hi := strings.Index(joined, "START_IO_REQ=false"), strings.LastIndex(joined, "START_IO_REQ=true"); lo < 0 || hi < lo {
		t.Errorf("START_IO_REQ should go low, then high, and stay high: %s", joined)
	}

	// Outputs: DO_15 and AO_00 land in the output image on the next poll.
	if err := d.WriteOutputs(nio.Values{"DO_15": true, "AO_00": 5.0}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for f.Output(0) != 0x80 || f.Output(2) != 0x13 || f.Output(3) != 0x88 {
		if time.Now().After(deadline) {
			t.Fatalf("outputs not flushed: % x", []byte{f.Output(0), f.Output(1), f.Output(2), f.Output(3)})
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := d.WriteOutputs(nio.Values{"DO_15": false, "DO_00": true}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for f.Output(0) != 0x00 || f.Output(1) != 0x01 {
		if time.Now().After(deadline) {
			t.Fatalf("bit merge wrong: % x", []byte{f.Output(0), f.Output(1)})
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The bus stopping (RUN drops) makes every input Bad on a live socket;
	// it coming back makes them Good again.
	f.SetRun(false)
	deadline = time.Now().Add(2 * time.Second)
	for d.Quality()["DI_00"] != nio.Bad {
		if time.Now().After(deadline) {
			t.Fatalf("bus stop not reflected: %v", d.Quality())
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.SetRun(true)
	deadline = time.Now().Add(2 * time.Second)
	for d.Quality()["DI_00"] != nio.Good {
		if time.Now().After(deadline) {
			t.Fatalf("bus restart not reflected: %v", d.Quality())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDriverWithoutBusConductorAndMissingPort(t *testing.T) {
	f := plcnexttest.New()
	f.SetPortError(DefaultInputs, gdspb.DataAccessError_DAE_NotExists) // the connector was never made
	addr := SocketTarget(f.Listen(t))
	m, err := ParseManifest([]byte("bus-conductor: \"\"\nrun-port: none\ntags:\n  - {name: DI_00, dir: in, offset: 0, format: bit:0}\n"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m, WithScanRate(10*time.Millisecond), WithDialer(func(ctx context.Context) (dataAccess, error) { return Dial(ctx, addr) }))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for d.Health().Errors == 0 {
		if time.Now().After(deadline) {
			t.Fatal("missing port never reported")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if le := d.Health().LastError; !strings.Contains(le, "DAE_NotExists") || !strings.Contains(le, DefaultInputs) {
		t.Errorf("last error %q", le)
	}
	if q := d.Quality()["DI_00"]; q != nio.NotConnected {
		t.Errorf("quality %v, want NotConnected", q)
	}
}

// run-port: none survives New, which fills the defaults again: the RUN bit
// is never read, so a bus whose RUN port reads false (or is not there)
// still delivers Good inputs (#874).
func TestRunPortNoneSurvivesNew(t *testing.T) {
	f := plcnexttest.New() // its RUN port reads false: a check would make the inputs Bad
	addr := SocketTarget(f.Listen(t))
	m, err := ParseManifest([]byte("bus-conductor: \"\"\nrun-port: none\ntags:\n  - {name: DI_00, dir: in, offset: 0, format: bit:0}\n"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m, WithScanRate(10*time.Millisecond), WithDialer(func(ctx context.Context) (dataAccess, error) { return Dial(ctx, addr) }))
	if err != nil {
		t.Fatal(err)
	}
	if d.m.RunPort != "" {
		t.Fatalf("after New the driver reads RUN port %q, want none", d.m.RunPort)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for d.Quality()["DI_00"] != nio.Good {
		if time.Now().After(deadline) {
			t.Fatalf("inputs %v with run-port: none, want Good (health %+v)", d.Quality()["DI_00"], d.Health())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A reconnect to a bus that is already configured and running must not
// re-request either: no CONFIG_REQ edge, no START_IO_REQ edge.
func TestHandshakeIsIdempotent(t *testing.T) {
	f := plcnexttest.New()
	f.SetConfigured(true)
	f.SetRun(true)
	addr := SocketTarget(f.Listen(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	st, err := Handshake(ctx, c, DefaultComponent, DefaultRunPort)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Configured || !st.Running || st.Modules != 4 {
		t.Errorf("state %+v", st)
	}
	if w := wrote(f); strings.Contains(w, "CONFIG_REQ") || strings.Contains(w, "START_IO_REQ") {
		t.Errorf("a running bus was re-requested: %s", w)
	}
}
