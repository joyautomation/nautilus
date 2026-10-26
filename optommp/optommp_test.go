package optommp

import (
	"encoding/binary"
	"io"
	"math"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/codegen"
)

// fakeDevice answers OptoMMP block reads and writes from a sparse memory
// map — the bench GRV-EPIC-PR1's chassis, as Scan saw it on 2026-10-23.
type fakeDevice struct {
	mem map[uint32]byte
}

func newBenchDevice() *fakeDevice {
	d := &fakeDevice{mem: map[uint32]byte{}}
	d.put(addrUnitDescription, []byte("GRV-EPIC-PR1"))
	d.put(addrFirmware, []byte{3, 6, 1, 0})
	module := func(slot int, mtype uint32, chans int, ctype uint32) {
		for i := 0; i < chans; i++ {
			row := addrChannelConfig + uint32(slot)*configModuleStride + uint32(i)*configChannelStride
			d.putU32(row, mtype)
			d.putU32(row+4, ctype)
		}
	}
	module(0, 0x50000005, 24, 0x50000000) // GRV-IACDCTTL-24
	module(1, 0x90000015, 12, 0x90000000) // GRV-ODCI-12
	module(2, 0x6000001F, 12, 0x60000031) // GRV-IICTD-12, 12 of 24 rows configured
	module(3, 0x60000002, 24, 0x60000024) // GRV-IV-24 at ±160 V
	// Presence bits for the digital modules; state: DI 3 on, DO 11 on.
	setBit := func(base uint32, bit int) {
		w := base + uint32(bit/32)*4
		v := binary.BigEndian.Uint32([]byte{d.mem[w], d.mem[w+1], d.mem[w+2], d.mem[w+3]})
		d.putU32(w, v|1<<(uint(bit)%32))
	}
	for i := 0; i < 24; i++ {
		setBit(addrPackedPresent, i)
	}
	for i := 0; i < 12; i++ {
		setBit(addrPackedPresent, 32+i)
	}
	setBit(addrPackedState, 3)
	setBit(addrPackedState, 32+11)
	// Values: slot 2 ch 0 = 20.19 °C, slot 3 ch 0 = 8.4 V, the rest NaN.
	for bit := 0; bit < MaxModules*MaxChannels; bit++ {
		d.putU32(addrPackedValues+uint32(bit)*4, math.Float32bits(float32(math.NaN())))
	}
	d.putU32(addrPackedValues+(2*32+0)*4, math.Float32bits(20.19))
	d.putU32(addrPackedValues+(3*32+0)*4, math.Float32bits(8.4))
	return d
}

func (d *fakeDevice) put(addr uint32, b []byte) {
	for i, v := range b {
		d.mem[addr+uint32(i)] = v
	}
}

func (d *fakeDevice) putU32(addr uint32, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	d.put(addr, b[:])
}

// serve answers one connection until it closes.
func (d *fakeDevice) serve(conn net.Conn) {
	defer conn.Close()
	for {
		var hdr [16]byte
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return
		}
		addr := binary.BigEndian.Uint32(hdr[8:12])
		n := int(binary.BigEndian.Uint16(hdr[12:14]))
		switch hdr[3] >> 4 {
		case tcodeReadBlockRequest:
			resp := make([]byte, 16+n)
			resp[3] = tcodeReadBlockResponse << 4
			binary.BigEndian.PutUint16(resp[12:14], uint16(n))
			for i := 0; i < n; i++ {
				resp[16+i] = d.mem[addr+uint32(i)]
			}
			conn.Write(resp)
		case tcodeWriteBlockRequest:
			data := make([]byte, n)
			io.ReadFull(conn, data)
			d.put(addr, data)
			resp := make([]byte, 12)
			resp[3] = tcodeWriteResponse << 4
			conn.Write(resp)
		}
	}
}

func dialFake(t *testing.T, d *fakeDevice) *Client {
	t.Helper()
	a, b := net.Pipe()
	go d.serve(b)
	c := NewClient(a, 2*time.Second)
	t.Cleanup(func() { c.Close() })
	return c
}

func TestScanBenchChassis(t *testing.T) {
	c := dialFake(t, newBenchDevice())
	ch, err := Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Part != "GRV-EPIC-PR1" || ch.Firmware != "3.6.1.0" {
		t.Fatalf("unit: %q %q", ch.Part, ch.Firmware)
	}
	if len(ch.Modules) != 4 {
		t.Fatalf("modules: %d", len(ch.Modules))
	}
	want := []struct {
		part  string
		chans int
		kind  Kind
	}{{"GRV-IACDCTTL-24", 24, KindDI}, {"GRV-ODCI-12", 12, KindDO}, {"GRV-IICTD-12", 12, KindAI}, {"GRV-IV-24", 24, KindAI}}
	for i, w := range want {
		m := ch.Modules[i]
		if m.Part != w.part || len(m.Channels) != w.chans || m.Channels[0].Kind != w.kind {
			t.Errorf("slot %d: %s %d %s, want %v", i, m.Part, len(m.Channels), m.Channels[0].Kind, w)
		}
	}
	if !ch.Modules[0].Channels[3].State || ch.Modules[0].Channels[2].State {
		t.Error("DI state bits misread")
	}
	if !ch.Modules[1].Channels[11].State {
		t.Error("DO 11 state misread")
	}
	if v := ch.Modules[2].Channels[0].Value; math.Abs(v-20.19) > 0.01 {
		t.Errorf("ICTD ch 0 = %g", v)
	}
	if !math.IsNaN(ch.Modules[2].Channels[1].Value) {
		t.Error("unwired ICTD channel should be NaN")
	}
	if ch.Modules[2].Channels[0].Unit != "°C" || ch.Modules[3].Channels[0].Desc != "±160 V" {
		t.Errorf("channel table: %q %q", ch.Modules[2].Channels[0].Unit, ch.Modules[3].Channels[0].Desc)
	}
	if !strings.Contains(ch.String(), "slot  2  GRV-IICTD-12") {
		t.Errorf("listing:\n%s", ch.String())
	}
}

func TestGenerateModbusMap(t *testing.T) {
	ch, err := Scan(dialFake(t, newBenchDevice()))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(ch, MapOptions{ID: "EPIC", Host: "192.0.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(out.Manifest.Tags); n != 24+12+12+24 {
		t.Fatalf("bindings: %d", n)
	}
	by := map[string]modbus.TagBinding{}
	for _, b := range out.Manifest.Tags {
		by[b.Name] = b
	}
	checks := []struct {
		name    string
		table   string
		addr    uint16
		format  string
		writabl bool
	}{
		{"EPIC_M0_DI00", modbus.TableDiscrete, 512, "bool", false},
		{"EPIC_M0_DI23", modbus.TableDiscrete, 535, "bool", false},
		{"EPIC_M1_DO11", modbus.TableCoil, 587, "bool", true},
		{"EPIC_M2_AI00", modbus.TableInput, 4864, "float32", false},
		{"EPIC_M3_AI00", modbus.TableInput, 4992, "float32", false},
		{"EPIC_M3_AI23", modbus.TableInput, 5038, "float32", false},
	}
	for _, c := range checks {
		b, ok := by[c.name]
		if !ok {
			t.Errorf("%s: missing", c.name)
			continue
		}
		if b.Table != c.table || b.Address != c.addr || b.Format != c.format || b.Writable != c.writabl {
			t.Errorf("%s: %+v, want %+v", c.name, b, c)
		}
	}
	// The manifest round-trips through the modbus renderer and parser, and
	// the driver builds a plan from it offline — what `naut check` does.
	yaml := codegen.ManifestYAML(out.Manifest, Command(MapOptions{ID: "EPIC", Host: "192.0.2.10"}))
	back, err := modbus.ParseManifest(yaml)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := modbus.BuildPlan(back, -1); err != nil {
		t.Fatal(err)
	}
	tags, err := TagsYAML(out, "naut optommp import --host 192.0.2.10", "tags/optommp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`- { name: EPIC_M1_DO11, role: output, init: false, desc: "GRV-ODCI-12 slot 1 ch 11 — Digital Output" }`,
		`- { name: EPIC_M2_AI00, role: input, unit: "°C", desc: "GRV-IICTD-12 slot 2 ch 0 — ICTD Temperature Probe" }`,
	} {
		if !strings.Contains(string(tags), want) {
			t.Errorf("tag file lacks %s\n%s", want, tags)
		}
	}
}

func TestWriteRoundTrip(t *testing.T) {
	d := newBenchDevice()
	c := dialFake(t, d)
	if err := c.Write(0xF0220000+1*0x1000+11*0x40, []byte{0, 0, 0, 1}); err != nil {
		t.Fatal(err)
	}
	if v, err := c.ReadUint32(0xF0220000 + 1*0x1000 + 11*0x40); err != nil || v != 1 {
		t.Fatalf("read back %d %v", v, err)
	}
}

func TestKindOf(t *testing.T) {
	cases := map[uint32]Kind{0: KindNone, 0x50000000: KindDI, 0x54000000: KindDI, 0x90000000: KindDO,
		0x94000078: KindDO, 0x60000031: KindAI, 0xA0000020: KindAO, 0x08000026: KindComm, 0x12345678: KindOther}
	for code, want := range cases {
		if got := KindOf(code); got != want {
			t.Errorf("KindOf(%08X) = %q, want %q", code, got, want)
		}
	}
}
