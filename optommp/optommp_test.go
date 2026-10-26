package optommp

import (
	"math"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/codegen"
	"github.com/joyautomation/nautilus/optommp/optommptest"
)

// fakeDevice is the bench chassis in-process (optommptest).
type fakeDevice = optommptest.Device

func newBenchDevice() *fakeDevice { return optommptest.NewBench() }

func dialFake(t *testing.T, d *fakeDevice) *Client {
	t.Helper()
	a, b := net.Pipe()
	go d.Serve(b)
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
