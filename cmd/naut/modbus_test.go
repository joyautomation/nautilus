package main

// The generator's semantics are tested in modbus/codegen. What is pinned
// here is the COMMAND and the artifacts: the golden files a re-run must
// reproduce byte for byte (testdata/modbus, refresh with -update), that the
// generated manifest loads through the core's strict decoder, and that
// `serve` actually answers the core's own client with the values it was
// seeded — byte order included.

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/modbus"
)

var update = flag.Bool("update", false, "rewrite the testdata golden files")

func modbusTestdata(name string) string {
	return filepath.Join("testdata", "modbus", name)
}

// captureModbus runs runModbus with stdout captured.
func captureModbus(t *testing.T, args ...string) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := runModbus(args)
	w.Close()
	os.Stdout = old
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r.Close()
	return sb.String(), code
}

// goldenCompare asserts got against the committed golden, or rewrites it
// under -update.
func goldenCompare(t *testing.T, golden string, got []byte) {
	t.Helper()
	path := modbusTestdata(golden)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test -run Modbus -update ./cmd/naut` to create it)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s drifted from the golden:\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
	}
}

func importInto(t *testing.T, dir string, extra ...string) (manifest, tags string, stdout string) {
	t.Helper()
	args := append([]string{"import", "--map", modbusTestdata("devices.yaml"), "--out", dir}, extra...)
	out, code := captureModbus(t, args...)
	if code != 0 {
		t.Fatalf("import failed (%d):\n%s", code, out)
	}
	return read(t, filepath.Join(dir, "modbus_manifest.yaml")),
		read(t, filepath.Join(dir, "tags", "modbus.yaml")), out
}

func TestModbusImportGolden(t *testing.T) {
	manifest, tags, out := importInto(t, t.TempDir(), "--plan")
	goldenCompare(t, "modbus_manifest.yaml", []byte(manifest))
	goldenCompare(t, "tags_modbus.yaml", []byte(tags))

	// The stdout is the plan followed by the "wrote ..." lines, which carry
	// the temp dir; the golden is everything before the first one.
	plan := out
	if i := strings.Index(out, "wrote "); i >= 0 {
		plan = out[:i]
	}
	goldenCompare(t, "plan.txt", []byte(plan))

	// Byte-identical on re-run, from another directory.
	m2, t2, _ := importInto(t, t.TempDir())
	if m2 != manifest || t2 != tags {
		t.Errorf("a second import differs from the first")
	}
}

// The generated manifest must decode through the core's strict loader —
// KnownFields, duration parsing, validation, plan — because that is what
// `naut check` will do to it on the driver branch.
func TestModbusImportLoadsThroughCore(t *testing.T) {
	manifest, _, _ := importInto(t, t.TempDir())
	m, err := modbus.ParseManifest([]byte(manifest))
	if err != nil {
		t.Fatalf("generated manifest does not load: %v", err)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("generated manifest invalid: %v", err)
	}
	if _, err := modbus.BuildPlan(m, -1); err != nil {
		t.Errorf("generated manifest does not plan: %v", err)
	}
	if len(m.Sources) != 4 {
		t.Errorf("expected 4 sources, got %d", len(m.Sources))
	}
}

// `modbus tags --map` must reproduce the import's tag file byte for byte —
// the CI re-derivation path.
func TestModbusTagsRederives(t *testing.T) {
	dir := t.TempDir()
	_, tags, _ := importInto(t, dir)

	again := filepath.Join(dir, "again.yaml")
	out, code := captureModbus(t, "tags", "-o", again,
		"--map", modbusTestdata("devices.yaml"),
		filepath.Join(dir, "modbus_manifest.yaml"))
	if code != 0 {
		t.Fatalf("tags failed (%d):\n%s", code, out)
	}
	if got := read(t, again); got != tags {
		t.Errorf("re-derived tag file differs:\n%s", got)
	}

	// Without --map the names and roles survive; the map-only columns drop.
	bare := filepath.Join(dir, "bare.yaml")
	if _, code := captureModbus(t, "tags", "-o", bare,
		filepath.Join(dir, "modbus_manifest.yaml")); code != 0 {
		t.Fatalf("tags without --map failed (%d)", code)
	}
	body := read(t, bare)
	if !strings.Contains(body, "- { name: TC_A_Tsp, role: output }") {
		t.Errorf("bare tags lost the role:\n%s", body)
	}
}

// serve, in process: seed a value through the manifest bindings, read it
// back with the core's client, and prove the source's word order was
// honoured on the wire.
func TestModbusServeSeedSmoke(t *testing.T) {
	m := modbus.Manifest{
		Sources: []modbus.Source{
			{ID: "GAS", Host: "10.0.0.51", UnitID: 3, WordOrder: "little"},
			{ID: "TC", Host: "10.0.0.10", UnitID: 1},
		},
		Tags: []modbus.TagBinding{
			{Name: "GAS_CH1", Source: "GAS", Table: "holding", Address: 0, Format: "float32"},
			{Name: "TC_Tsp", Source: "TC", Table: "holding", Address: 262, Format: "int32", Scale: 0.1, Writable: true},
			{Name: "TC_Run", Source: "TC", Table: "coil", Address: 5, Format: "bool"},
		},
	}
	srv, stop, err := startModbusServe(m, "127.0.0.1:0",
		map[string]any{"GAS_CH1": 12.5, "TC_Tsp": 180.0, "TC_Run": true}, false, &strings.Builder{})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer stop()

	ctx := context.Background()
	cl, err := modbus.Dial(ctx, srv.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cl.Close()

	f32, _ := modbus.ParseFormat("float32")
	regs, err := cl.ReadRegisters(ctx, 3, "holding", 0, 2)
	if err != nil {
		t.Fatalf("read GAS: %v", err)
	}
	if v, _ := modbus.Decode(f32, regs, "little", "", 0, 0); v != 12.5 {
		t.Errorf("GAS_CH1 read back %v, want 12.5", v)
	}
	// Word order respected: the same registers decoded big-endian must NOT
	// be 12.5.
	if v, _ := modbus.Decode(f32, regs, "big", "", 0, 0); v == 12.5 {
		t.Errorf("word order was ignored when seeding")
	}

	i32, _ := modbus.ParseFormat("int32")
	regs, err = cl.ReadRegisters(ctx, 1, "holding", 262, 2)
	if err != nil {
		t.Fatalf("read TC: %v", err)
	}
	if v, _ := modbus.Decode(i32, regs, "", "", 0.1, 0); v != 180.0 {
		t.Errorf("TC_Tsp read back %v, want 180 (scale inverted on seed)", v)
	}

	bits, err := cl.ReadBits(ctx, 1, "coil", 5, 1)
	if err != nil {
		t.Fatalf("read coil: %v", err)
	}
	if !bits[0] {
		t.Errorf("TC_Run coil not set")
	}
}

func TestModbusServeRefusesUnitCollision(t *testing.T) {
	m := modbus.Manifest{
		Sources: []modbus.Source{
			{ID: "A", Host: "10.0.0.1", UnitID: 1},
			{ID: "B", Host: "10.0.0.2", UnitID: 1},
		},
		Tags: []modbus.TagBinding{
			{Name: "A_X", Source: "A", Table: "holding", Address: 0, Format: "uint16"},
			{Name: "B_X", Source: "B", Table: "holding", Address: 100, Format: "uint16"},
		},
	}
	_, stop, err := startModbusServe(m, "127.0.0.1:0", nil, false, &strings.Builder{})
	if err == nil {
		stop()
		t.Fatal("two sources on unit 1, both holding, were served on one listener")
	}
	for _, want := range []string{"A", "B", "unit id 1", "holding"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("collision error missing %q: %v", want, err)
		}
	}

	// Same unit id on DIFFERENT tables is fine — nothing to confuse.
	m.Tags[1].Table = "input"
	srv, stop, err := startModbusServe(m, "127.0.0.1:0", nil, false, &strings.Builder{})
	if err != nil {
		t.Fatalf("disjoint tables should serve: %v", err)
	}
	_ = srv
	stop()
}

func TestModbusServeRejectsUnknownSeed(t *testing.T) {
	m := modbus.Manifest{
		Sources: []modbus.Source{{ID: "A", Host: "10.0.0.1", UnitID: 1}},
		Tags:    []modbus.TagBinding{{Name: "A_X", Source: "A", Table: "holding", Address: 0, Format: "uint16"}},
	}
	_, stop, err := startModbusServe(m, "127.0.0.1:0", map[string]any{"Nope": 1.0}, false, &strings.Builder{})
	if err == nil {
		stop()
		t.Fatal("an unknown seed tag passed silently")
	}
	if !strings.Contains(err.Error(), "Nope") {
		t.Errorf("seed error does not name the tag: %v", err)
	}
}
