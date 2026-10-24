package plcnext

import (
	"context"
	"net"
	"strconv"
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
// behind it, as measured on the bench (FW 2023.0): CONFIG_REQ latches
// CONFIGURED after a moment; START_IO_REQ raises the AXIO RUN bit; the
// images are 512-byte arrays that answer DAE_NotSupported whole and serve
// one element per `Port[i]` name; unknown ports answer DAE_NotExists.
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
	f.bools[DefaultRunPort] = false
	f.nums["BcComponent1/BusConductor.NUM_MODULES"] = 4
	f.bytes[DefaultInputs] = make([]byte, 512)
	f.bytes[DefaultOutputs] = make([]byte, 512)
	return f
}

// splitElement parses "Port[i]".
func splitElement(name string) (string, int, bool) {
	if !strings.HasSuffix(name, "]") {
		return name, 0, false
	}
	i := strings.LastIndex(name, "[")
	if i < 0 {
		return name, 0, false
	}
	n, err := strconv.Atoi(name[i+1 : len(name)-1])
	if err != nil {
		return name, 0, false
	}
	return name[:i], n, true
}

func boolValue(b bool) *arptypes.ObjectType {
	return &arptypes.ObjectType{TypeCode: arptypes.CoreType_CT_Boolean, Value: &arptypes.ObjectType_BoolValue{BoolValue: b}}
}

// readOne is the firmware's resolution of one name; the caller holds mu.
func (f *fakeGDS) readOne(name string) *gdspb.ReadItem {
	item := &gdspb.ReadItem{Error: gdspb.DataAccessError_DAE_None}
	if strings.HasSuffix(name, "CONFIGURED") {
		item.Value = boolValue(f.bools["BcComponent1/BusConductor.CONFIG_REQ"] || f.bools["configured"])
		return item
	}
	if v, ok := f.bools[name]; ok {
		item.Value = boolValue(v)
		return item
	}
	if v, ok := f.nums[name]; ok {
		item.Value = &arptypes.ObjectType{TypeCode: arptypes.CoreType_CT_Uint16, Value: &arptypes.ObjectType_Uint16Value{Uint16Value: uint32(v)}}
		return item
	}
	if _, ok := f.bytes[name]; ok {
		item.Error = gdspb.DataAccessError_DAE_NotSupported // whole arrays, as the real firmware
		return item
	}
	if port, i, ok := splitElement(name); ok {
		if b, ok := f.bytes[port]; ok {
			if i < 0 || i >= len(b) {
				item.Error = gdspb.DataAccessError_DAE_IndexOutOfRange
			} else {
				item.Value = uint8Value(b[i])
			}
			return item
		}
	}
	item.Error = gdspb.DataAccessError_DAE_NotExists
	return item
}

// writeOne is the firmware's handling of one write; the caller holds mu.
func (f *fakeGDS) writeOne(w *gdspb.WriteItem) gdspb.DataAccessError {
	port, v := w.GetPortName(), w.GetValue()
	if _, isBool := v.GetValue().(*arptypes.ObjectType_BoolValue); isBool {
		f.writes = append(f.writes, port+"="+strconv.FormatBool(v.GetBoolValue()))
	} else {
		f.writes = append(f.writes, port+"="+strconv.Itoa(int(numberOf(v))))
	}
	if _, ok := f.bools[port]; ok {
		f.bools[port] = v.GetBoolValue()
		if strings.HasSuffix(port, "CONFIG_REQ") && v.GetBoolValue() {
			f.bools["configured"] = true
		}
		if strings.HasSuffix(port, "START_IO_REQ") && v.GetBoolValue() && f.bools["configured"] {
			f.bools[DefaultRunPort] = true
		}
		return gdspb.DataAccessError_DAE_None
	}
	if _, ok := f.bytes[port]; ok {
		return gdspb.DataAccessError_DAE_NotSupported
	}
	if p, i, ok := splitElement(port); ok {
		if b, ok := f.bytes[p]; ok {
			if i < 0 || i >= len(b) {
				return gdspb.DataAccessError_DAE_IndexOutOfRange
			}
			b[i] = byte(numberOf(v))
			return gdspb.DataAccessError_DAE_None
		}
	}
	return gdspb.DataAccessError_DAE_NotExists
}

func (f *fakeGDS) ReadSingle(ctx context.Context, req *gdspb.IDataAccessServiceReadSingleRequest) (*gdspb.IDataAccessServiceReadSingleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &gdspb.IDataAccessServiceReadSingleResponse{XReturnValue: f.readOne(req.PortName)}, nil
}

func (f *fakeGDS) Read(ctx context.Context, req *gdspb.IDataAccessServiceReadRequest) (*gdspb.IDataAccessServiceReadResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*gdspb.ReadItem, len(req.PortNames))
	for i, n := range req.PortNames {
		out[i] = f.readOne(n)
	}
	return &gdspb.IDataAccessServiceReadResponse{XReturnValue: out}, nil
}

func (f *fakeGDS) WriteSingle(ctx context.Context, req *gdspb.IDataAccessServiceWriteSingleRequest) (*gdspb.IDataAccessServiceWriteSingleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &gdspb.IDataAccessServiceWriteSingleResponse{XReturnValue: f.writeOne(req.GetData())}, nil
}

func (f *fakeGDS) Write(ctx context.Context, req *gdspb.IDataAccessServiceWriteRequest) (*gdspb.IDataAccessServiceWriteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]gdspb.DataAccessError, len(req.Data))
	for i, w := range req.Data {
		out[i] = f.writeOne(w)
	}
	return &gdspb.IDataAccessServiceWriteResponse{XReturnValue: out}, nil
}

func (f *fakeGDS) setInput(offset int, b byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bytes[DefaultInputs][offset] = b
}

func (f *fakeGDS) setRun(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bools[DefaultRunPort] = v
}

func (f *fakeGDS) output(offset int) byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bytes[DefaultOutputs][offset]
}

func (f *fakeGDS) wrote() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.writes, " ")
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
	joined := f.wrote()
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

	// The bus stopping (RUN drops) makes every input Bad on a live socket;
	// it coming back makes them Good again.
	f.setRun(false)
	deadline = time.Now().Add(2 * time.Second)
	for d.Quality()["DI_00"] != nio.Bad {
		if time.Now().After(deadline) {
			t.Fatalf("bus stop not reflected: %v", d.Quality())
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.setRun(true)
	deadline = time.Now().Add(2 * time.Second)
	for d.Quality()["DI_00"] != nio.Good {
		if time.Now().After(deadline) {
			t.Fatalf("bus restart not reflected: %v", d.Quality())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDriverWithoutBusConductorAndMissingPort(t *testing.T) {
	f := newFakeGDS()
	delete(f.bytes, DefaultInputs) // the connector was never made: the port does not exist
	addr := serve(t, f)
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

// A reconnect to a bus that is already configured and running must not
// re-request either: no CONFIG_REQ edge, no START_IO_REQ edge.
func TestHandshakeIsIdempotent(t *testing.T) {
	f := newFakeGDS()
	f.bools["configured"] = true
	f.bools[DefaultRunPort] = true
	addr := serve(t, f)
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
	if w := f.wrote(); strings.Contains(w, "CONFIG_REQ") || strings.Contains(w, "START_IO_REQ") {
		t.Errorf("a running bus was re-requested: %s", w)
	}
}
