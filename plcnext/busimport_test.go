package plcnext

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The bench's device list imports to the hand-corrected manifest in
// examples/plcnext-bench (the layout settled with eyes on 2026-10-27):
// the same modules, and every tag bound to the same bytes and bits.
// Descriptions and comments are the hand copy's own.
func TestImportBenchEqualsHandManifest(t *testing.T) {
	devs, err := os.ReadFile("../examples/plcnext-bench/plcnext_devices.yaml")
	if err != nil {
		t.Fatal(err)
	}
	hand, err := os.ReadFile("../examples/plcnext-bench/plcnext_manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l, err := ParseDeviceList(devs)
	if err != nil {
		t.Fatal(err)
	}
	imp, err := Import(l, ImportOptions{Transport: TransportShm})
	if err != nil {
		t.Fatal(err)
	}
	gen, err := ParseManifest(ImportedYAML(imp, "naut plcnext import plcnext_devices.yaml --transport shm"))
	if err != nil {
		t.Fatalf("the generated manifest does not parse: %v\n%s", err, ImportedYAML(imp, "x"))
	}
	want, err := ParseManifest(hand)
	if err != nil {
		t.Fatal(err)
	}
	if gen.Transport != want.Transport || gen.BusConductor != want.BusConductor || gen.ByteOrder != want.ByteOrder {
		t.Errorf("header: generated %s/%s/%s, hand %s/%s/%s", gen.Transport, gen.BusConductor, gen.ByteOrder, want.Transport, want.BusConductor, want.ByteOrder)
	}
	if len(gen.Modules) != len(want.Modules) {
		t.Fatalf("modules: generated %d, hand %d", len(gen.Modules), len(want.Modules))
	}
	for i, g := range gen.Modules {
		w := want.Modules[i]
		if g.Slot != w.Slot || g.Part != w.Part || g.In != w.In || g.Out != w.Out {
			t.Errorf("module %d: generated %+v, hand %+v", i, g, w)
		}
	}
	type bound struct {
		Dir, Format string
		Offset      int
		Init        any
		Scale, Bias float64
	}
	tags := func(m Manifest) map[string]bound {
		out := map[string]bound{}
		for _, b := range m.Tags {
			out[b.Name] = bound{b.Dir, b.Format, b.Offset, b.Init, b.Scale, b.Bias}
		}
		return out
	}
	g, w := tags(gen), tags(want)
	if len(g) != len(w) {
		t.Errorf("tags: generated %d, hand %d", len(g), len(w))
	}
	for name, wb := range w {
		if gb, ok := g[name]; !ok || gb != wb {
			t.Errorf("%s: generated %+v (present %t), hand %+v", name, gb, ok, wb)
		}
	}
	for _, mod := range imp.Modules {
		if !mod.Known {
			t.Errorf("slot %d not known", mod.Slot)
		}
	}
}

func TestImportUnknownDevicesAndMistakes(t *testing.T) {
	l, err := ParseDeviceList([]byte(`
devices:
  - { type: "00800002" }
  - { type: 0x00C00001, id: 0x00000D02, length: 16, desc: "K3.1" }
  - { part: "axl se di16/1" }
  - { type: 0x00400002, part: "AXL F DO16/3 1F" }
`))
	if err != nil {
		t.Fatal(err)
	}
	if l.Devices[0].Type != 0x00800002 {
		t.Errorf("quoted TIC-style hex read as %v", l.Devices[0].Type)
	}
	imp, err := Import(l, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	offs := []int{}
	for _, m := range imp.Modules {
		offs = append(offs, m.Offset)
	}
	if want := []int{0, 2, 4, 6}; len(offs) != 4 || offs[1] != want[1] || offs[2] != want[2] || offs[3] != want[3] {
		t.Errorf("offsets %v, want %v", offs, want)
	}
	if imp.Modules[1].Known || imp.Modules[1].In != 2 || imp.Modules[1].Out != 2 {
		t.Errorf("unknown module: %+v", imp.Modules[1])
	}
	if p := imp.Modules[3].Part; p != "AXL F DO16/3 1F" {
		t.Errorf("a part in the list names the module: %q", p)
	}
	y := string(ImportedYAML(imp, "naut plcnext import devs.yaml"))
	for _, want := range []string{
		"# slot 2 is not in naut's table: its 2 byte(s) are at offset 2 in both images.",
		`part: "unknown 0x00C00001", in: 2, out: 2, desc: "K3.1, 16 bits, not in naut's table: bind it by hand (device 0x00C00001, id 0x00000D02)"`,
		`# - { name: S2_IN_00, dir: in, offset: 2, format: "ch:1"`,
		// Two DI16s: names carry the slot.
		`name: S1_DI_00, dir: in, offset: 0, format: "ch:1"`,
		`name: S3_DI_15, dir: in, offset: 4, format: "ch:16"`,
		`name: DO_15, dir: out, offset: 6, format: "ch:16", init: false`,
	} {
		if !strings.Contains(y, want) {
			t.Errorf("generated manifest lacks %s:\n%s", want, y)
		}
	}
	m, err := ParseManifest([]byte(y))
	if err != nil {
		t.Fatalf("generated manifest: %v\n%s", err, y)
	}
	if m.Transport != TransportGRPC || m.BusConductor != DefaultComponent {
		t.Errorf("defaults: %s %s", m.Transport, m.BusConductor)
	}

	_, err = Import(DeviceList{Devices: []Device{
		{Type: 0x00400002},
		{Slot: 3, Type: 0x00800002},
		{Type: 0x12345678},
		{Type: 0x12345679, Length: ptr(12)},
		{},
	}}, ImportOptions{})
	if err == nil {
		t.Fatal("mistakes accepted")
	}
	for _, want := range []string{
		"device 2 says slot 3",
		"slot 3: 0x12345678 is not in the import's table; give its frame length in bits",
		"slot 4: length 12 bits is not a whole number of bytes",
		"slot 5: no type and no part",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if _, err := ParseDeviceList([]byte("devices:\n  - { type: 0xZZ }\n")); err == nil || !strings.Contains(err.Error(), "hexadecimal") {
		t.Errorf("bad hex: %v", err)
	}
	if _, err := ParseDeviceList([]byte("devices: []\n")); err == nil {
		t.Error("an empty list accepted")
	}
	if imp, _ := Import(DeviceList{Devices: []Device{{Type: 0x00400002}}}, ImportOptions{BusConductor: "none"}); imp.Manifest.BusConductor != "" {
		t.Errorf("--bus-conductor none: %q", imp.Manifest.BusConductor)
	}
}

func ptr(n int) *int { return &n }

// BusModules reads CONFIGURED and NUM_MODULES and writes nothing: an
// import never starts or reconfigures a bus.
func TestBusModulesReadsTheCountWithoutTouchingTheBus(t *testing.T) {
	f := newFakeGDS()
	addr := serve(t, f)
	ctx := context.Background()
	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := BusModules(ctx, c, ""); err == nil || !strings.Contains(err.Error(), "naut plcnext browse") {
		t.Errorf("an unconfigured bus: %v", err)
	}
	f.mu.Lock()
	f.bools["configured"] = true
	f.mu.Unlock()
	n, err := BusModules(ctx, c, DefaultComponent)
	if err != nil || n != 4 {
		t.Fatalf("NUM_MODULES = %d, %v", n, err)
	}
	if w := f.wrote(); w != "" {
		t.Errorf("BusModules wrote: %s", w)
	}
	bench := DeviceList{Devices: make([]Device, 4)}
	if err := bench.CheckCount(n); err != nil {
		t.Error(err)
	}
	if err := (DeviceList{Devices: make([]Device, 3)}).CheckCount(n); err == nil || !strings.Contains(err.Error(), "reports 4 module(s)") {
		t.Errorf("a short list: %v", err)
	}
}
