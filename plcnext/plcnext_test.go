package plcnext

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	nio "github.com/joyautomation/nautilus/io"
	arptypes "github.com/joyautomation/nautilus/plcnext/gen/arptypes"
	gdspb "github.com/joyautomation/nautilus/plcnext/gen/plc/gds"
)

// fakeGDS is a PLCnext firmware's IDataAccessService with BusConductor
// behind it: CONFIG_REQ latches CONFIGURED after a moment, the images are
// 512-byte OctetstringN ports, unknown ports answer DAE_NotExists.
type fakeGDS struct {
	gdspb.UnimplementedIDataAccessServiceServer
	mu     sync.Mutex
	bools  map[string]bool
	nums   map[string]float64
	bytes  map[string][]byte
	writes []string
}

func newFakeGDS() *fakeGDS {
	f := &fakeGDS{bools: map[string]bool{}, nums: map[string]float64{}, bytes: map[string][]byte{}}
	f.bools["BcComponent1/BusConductor.CONFIG_REQ"] = false
	f.bools["BcComponent1/BusConductor.CONFIG_MUST_MATCH"] = false
	f.bools["BcComponent1/BusConductor.START_IO_REQ"] = false
	f.bools["BcComponent1/BusConductor.CONFIGURED"] = false
	f.nums["BcComponent1/BusConductor.NUM_MODULES"] = 4
	f.bytes[DefaultInputs] = make([]byte, 512)
	f.bytes[DefaultOutputs] = make([]byte, 512)
	return f
}

func (f *fakeGDS) ReadSingle(ctx context.Context, req *gdspb.IDataAccessServiceReadSingleRequest) (*gdspb.IDataAccessServiceReadSingleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item := &gdspb.ReadItem{Error: gdspb.DataAccessError_DAE_None}
	switch {
	case strings.HasSuffix(req.PortName, "CONFIGURED"):
		// Configured once CONFIG_REQ has been seen.
		item.Value = &arptypes.ObjectType{TypeCode: arptypes.CoreType_CT_Boolean, Value: &arptypes.ObjectType_BoolValue{BoolValue: f.bools["BcComponent1/BusConductor.CONFIG_REQ"] || f.bools["configured"]}}
	default:
		if v, ok := f.bools[req.PortName]; ok {
			item.Value = &arptypes.ObjectType{TypeCode: arptypes.CoreType_CT_Boolean, Value: &arptypes.ObjectType_BoolValue{BoolValue: v}}
		} else if v, ok := f.nums[req.PortName]; ok {
			item.Value = &arptypes.ObjectType{TypeCode: arptypes.CoreType_CT_Uint16, Value: &arptypes.ObjectType_Uint16Value{Uint16Value: uint32(v)}}
		} else if b, ok := f.bytes[req.PortName]; ok {
			item.Value = byteArray(b)
		} else {
			item.Error = gdspb.DataAccessError_DAE_NotExists
		}
	}
	return &gdspb.IDataAccessServiceReadSingleResponse{XReturnValue: item}, nil
}

func (f *fakeGDS) WriteSingle(ctx context.Context, req *gdspb.IDataAccessServiceWriteSingleRequest) (*gdspb.IDataAccessServiceWriteSingleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	port := req.GetData().GetPortName()
	f.writes = append(f.writes, port)
	v := req.GetData().GetValue()
	if _, ok := f.bools[port]; ok {
		f.bools[port] = v.GetBoolValue()
		if strings.HasSuffix(port, "CONFIG_REQ") && v.GetBoolValue() {
			f.bools["configured"] = true
		}
		return &gdspb.IDataAccessServiceWriteSingleResponse{XReturnValue: gdspb.DataAccessError_DAE_None}, nil
	}
	if _, ok := f.bytes[port]; ok {
		b, err := bytesOf(port, v)
		if err != nil {
			return nil, err
		}
		f.bytes[port] = b
		return &gdspb.IDataAccessServiceWriteSingleResponse{XReturnValue: gdspb.DataAccessError_DAE_None}, nil
	}
	return &gdspb.IDataAccessServiceWriteSingleResponse{XReturnValue: gdspb.DataAccessError_DAE_NotExists}, nil
}

func (f *fakeGDS) setInput(offset int, b byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bytes[DefaultInputs][offset] = b
}

func (f *fakeGDS) output(offset int) byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bytes[DefaultOutputs][offset]
}

func serve(t *testing.T, f *fakeGDS) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	gdspb.RegisterIDataAccessServiceServer(srv, f)
	go srv.Serve(ln)
	t.Cleanup(srv.Stop)
	return ln.Addr().String()
}

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
	if m.Socket != DefaultSocket || m.Inputs != DefaultInputs || m.Outputs != DefaultOutputs || m.ByteOrder != "big" {
		t.Fatalf("defaults: %+v", m)
	}
	if got := m.InputNames(); len(got) != 3 || got[0] != "AI_00" {
		t.Fatalf("inputs: %v", got)
	}
	if _, err := ParseManifest([]byte("tags:\n  - {name: X, dir: sideways, offset: 600, format: bit:19}\n")); err == nil {
		t.Fatal("bad manifest accepted")
	} else if s := err.Error(); !strings.Contains(s, "sideways") || !strings.Contains(s, "600") || !strings.Contains(s, "bit:19") {
		t.Fatalf("errors not all reported: %v", err)
	}
	if _, err := ParseManifest([]byte("tags:\n  - {name: X, dir: in, offset: 0, format: int16, nope: 1}\n")); err == nil {
		t.Fatal("unknown key accepted")
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
	f := newFakeGDS()
	addr := serve(t, f)
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

	f.setInput(0, 0x80) // DI_15
	f.setInput(1, 0x01) // DI_00
	f.setInput(2, 0x04)
	f.setInput(3, 0xD2) // AI_00 = 1.234 V
	var in nio.Values
	deadline := time.Now().Add(3 * time.Second)
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
	if !h.Connected || !h.Configured || h.Modules != 4 || h.InBytes != 512 || h.OutBytes != 512 {
		t.Errorf("health %+v", h)
	}
	// The handshake wrote CONFIG_MUST_MATCH, CONFIG_REQ, START_IO_REQ.
	joined := strings.Join(f.writes, " ")
	for _, p := range []string{"CONFIG_MUST_MATCH", "CONFIG_REQ", "START_IO_REQ"} {
		if !strings.Contains(joined, p) {
			t.Errorf("handshake never wrote %s: %s", p, joined)
		}
	}

	// Outputs: DO_15 and AO_00 land in the output image on the next poll.
	if err := d.WriteOutputs(nio.Values{"DO_15": true, "AO_00": 5.0}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for f.output(0) != 0x80 || f.output(2) != 0x13 || f.output(3) != 0x88 {
		if time.Now().After(deadline) {
			t.Fatalf("outputs not flushed: % x", []byte{f.output(0), f.output(1), f.output(2), f.output(3)})
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := d.WriteOutputs(nio.Values{"DO_15": false, "DO_00": true}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for f.output(0) != 0x00 || f.output(1) != 0x01 {
		if time.Now().After(deadline) {
			t.Fatalf("bit merge wrong: % x", []byte{f.output(0), f.output(1)})
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDriverWithoutBusConductorAndMissingPort(t *testing.T) {
	f := newFakeGDS()
	delete(f.bytes, DefaultInputs) // bus never configured: the port does not exist
	addr := serve(t, f)
	m, err := ParseManifest([]byte("bus-conductor: \"\"\ntags:\n  - {name: DI_00, dir: in, offset: 0, format: bit:0}\n"))
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
