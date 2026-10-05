package main

// The generator's semantics are tested in snmp/codegen and snmp/profiles.
// What is pinned here is the COMMAND and its artifacts: the golden files a
// re-run must reproduce byte for byte (testdata/snmp, refresh with
// `go test -run Snmp -update ./cmd/naut`), that the generated hw_types.st
// compiles, that the manifest loads through the driver's strict decoder,
// that `tags` re-derives the tag file from the manifest alone — and that a
// LIVE import of `serve`, and an import of `browse --record`'s file, write
// the same bytes as the import of the walk they both came from.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/snmp/walk"
)

func snmpTestdata(name string) string { return filepath.Join("testdata", "snmp", name) }

func captureSnmp(t *testing.T, args ...string) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()
	code := runSnmp(args)
	w.Close()
	os.Stdout = old
	out := <-done
	r.Close()
	return out, code
}

func snmpGolden(t *testing.T, golden string, got []byte) {
	t.Helper()
	path := snmpTestdata(golden)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test -run Snmp -update ./cmd/naut` to create it)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s drifted from the golden:\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
	}
}

type snmpImport struct{ manifest, tags, types, stdout string }

func snmpImportInto(t *testing.T, dir string, args ...string) snmpImport {
	t.Helper()
	out, code := captureSnmp(t, append([]string{"import", "--out", dir}, args...)...)
	if code != 0 {
		t.Fatalf("import failed (%d):\n%s", code, out)
	}
	return snmpImport{
		manifest: read(t, filepath.Join(dir, "snmp_manifest.yaml")),
		tags:     read(t, filepath.Join(dir, "tags", "snmp.yaml")),
		types:    read(t, filepath.Join(dir, "hw_types.st")),
		stdout:   out,
	}
}

func TestSnmpImportGolden(t *testing.T) {
	args := []string{"--walk", snmpTestdata("switch.snmpwalk"), "--tag", "SW1", "--host", "192.0.2.2", "--plan"}
	got := snmpImportInto(t, t.TempDir(), args...)
	snmpGolden(t, "snmp_manifest.yaml", []byte(got.manifest))
	snmpGolden(t, "tags_snmp.yaml", []byte(got.tags))
	snmpGolden(t, "hw_types.st", []byte(got.types))
	plan := got.stdout
	if i := strings.Index(plan, "profile switch:"); i >= 0 {
		plan = plan[:i]
	}
	snmpGolden(t, "plan.txt", []byte(plan))

	// Byte-identical on re-run, from another directory.
	again := snmpImportInto(t, t.TempDir(), args[:6]...)
	if again.manifest != got.manifest || again.tags != got.tags || again.types != got.types {
		t.Error("a second import differs from the first")
	}

	// The UPS too: the second profile family's golden.
	ups := snmpImportInto(t, t.TempDir(), "--walk", snmpTestdata("ups.snmpwalk"), "--tag", "UPS1", "--host", "192.0.2.9")
	snmpGolden(t, "ups_manifest.yaml", []byte(ups.manifest))
	if ups.types != got.types {
		t.Error("hw_types.st must not depend on the device")
	}
}

// hw_types.st compiles — and the contract types are usable from a program.
func TestSnmpTypesCompile(t *testing.T) {
	got := snmpImportInto(t, t.TempDir(), "--walk", snmpTestdata("ups.snmpwalk"), "--tag", "UPS1", "--host", "192.0.2.9")
	prog, err := st.Parse(got.types + "\nPROGRAM P\nVAR_EXTERNAL SW1_Port01 : SwitchPort; UPS1 : UPS; END_VAR\nVAR down : BOOL; END_VAR\ndown := SW1_Port01.Down AND NOT UPS1.OnBattery;\nEND_PROGRAM\n")
	if err != nil {
		t.Fatalf("hw_types.st does not parse: %v", err)
	}
	if _, err := st.Lower(prog); err != nil {
		t.Fatalf("hw_types.st does not lower: %v", err)
	}
}

// The generated manifest decodes through the driver's strict loader and
// builds a driver offline — what `naut check` will do to it.
func TestSnmpImportLoadsThroughCore(t *testing.T) {
	got := snmpImportInto(t, t.TempDir(), "--walk", snmpTestdata("switch.snmpwalk"), "--tag", "SW1", "--host", "192.0.2.2")
	m, err := snmp.ParseManifest([]byte(got.manifest))
	if err != nil {
		t.Fatalf("generated manifest does not load: %v", err)
	}
	d, err := snmp.New(m)
	if err != nil {
		t.Fatalf("generated manifest does not build a driver: %v", err)
	}
	if n := len(d.InputNames()); n != 33 { // 31 struct tags + __Online + __LastPollMs
		t.Errorf("InputNames = %d", n)
	}
}

func TestSnmpTagsRederives(t *testing.T) {
	dir := t.TempDir()
	got := snmpImportInto(t, dir, "--walk", snmpTestdata("switch.snmpwalk"), "--tag", "SW1", "--host", "192.0.2.2")
	again := filepath.Join(dir, "again.yaml")
	if out, code := captureSnmp(t, "tags", "-o", again, filepath.Join(dir, "snmp_manifest.yaml")); code != 0 {
		t.Fatalf("tags failed (%d):\n%s", code, out)
	}
	if read(t, again) != got.tags {
		t.Error("re-derived tag file differs from the import's")
	}
	skipped := filepath.Join(dir, "skipped.yaml")
	if _, code := captureSnmp(t, "tags", "-o", skipped, "--skip", "SW1_Temp_*", filepath.Join(dir, "snmp_manifest.yaml")); code != 0 {
		t.Fatal("tags --skip failed")
	}
	if strings.Contains(read(t, skipped), "SW1_Temp_CPU,") {
		t.Error("--skip did not skip")
	}
}

// The commissioning loop, end to end on loopback: serve the recording,
// import LIVE from it, browse --record it, import the recording — the two
// imports are the same bytes, and the recording is the walk it served.
func TestSnmpLiveEqualsRecording(t *testing.T) {
	raw, err := os.ReadFile(snmpTestdata("switch.snmpwalk"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := walk.ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	a, stop, err := startSnmpServe(w, "127.0.0.1:0", "public", false, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	host, portStr, _ := strings.Cut(a.Addr(), ":")
	port, _ := strconv.Atoi(portStr)
	t.Setenv("SNMP_SW1_COMMUNITY", "public")
	t.Setenv("SNMP_COMMUNITY", "public")

	live := snmpImportInto(t, t.TempDir(), "--tag", "SW1", "--host", host, "--port", strconv.Itoa(port))

	rec := filepath.Join(t.TempDir(), "rec.snmpwalk")
	out, code := captureSnmp(t, "browse", "--host", host, "--port", strconv.Itoa(port), "--record", rec)
	if code != 0 {
		t.Fatalf("browse failed (%d):\n%s", code, out)
	}
	if !strings.Contains(out, "ifHCInOctets.1 = ") || !strings.Contains(out, `sysName.0 = "sw1-lab" (STRING)`) {
		t.Errorf("browse does not name what the profiles know:\n%.600s", out)
	}
	if read(t, rec) != string(w.Bytes()) {
		t.Error("the recording differs from the walk that was served")
	}
	fromRec := snmpImportInto(t, t.TempDir(), "--walk", rec, "--tag", "SW1", "--host", host, "--port", strconv.Itoa(port))
	if live.manifest != fromRec.manifest || live.tags != fromRec.tags || live.types != fromRec.types {
		t.Errorf("live and recorded imports differ:\n--- live ---\n%.400s\n--- recorded ---\n%.400s", live.manifest, fromRec.manifest)
	}

	// A wrong community: browse fails in words, not a hang.
	t.Setenv("SNMP_COMMUNITY", "wrong")
	_, code = captureSnmp(t, "browse", "--host", host, "--port", strconv.Itoa(port), "--timeout", "100ms", "--oid", "1.3.6.1.2.1.1")
	if code != 1 {
		t.Errorf("browse with a wrong community exited %d", code)
	}
}

// serve --ramp makes the recorded octet counters move: the 10 up ports and
// the up VLAN/loopback interfaces, in and out — 24.
func TestSnmpServeRamp(t *testing.T) {
	raw, err := os.ReadFile(snmpTestdata("switch.snmpwalk"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := walk.ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	a, stop, err := startSnmpServe(w, "127.0.0.1:0", "public", true, &log)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if !strings.Contains(log.String(), "ramping 24 octet counter(s)") {
		t.Errorf("serve output: %s", log.String())
	}
	before, _ := a.Value("1.3.6.1.2.1.31.1.1.1.6.1")
	time.Sleep(50 * time.Millisecond)
	after, _ := a.Value("1.3.6.1.2.1.31.1.1.1.6.1")
	if after.Uint <= before.Uint {
		t.Errorf("ifHCInOctets.1 did not move: %d → %d", before.Uint, after.Uint)
	}
	if down, _ := a.Value("1.3.6.1.2.1.31.1.1.1.6.7"); down.Uint != 0 {
		t.Errorf("a down port's counter moved: %d", down.Uint)
	}
}

func TestSnmpUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"frobnicate"},
		{"import", "--walk", "x"},
		{"browse"},
		{"serve"},
		{"serve", "--walk", "x", "--from", "http://127.0.0.1:8087"}, // --from without --manifest
		{"tags"},
	} {
		if _, code := captureSnmp(t, args...); code != 2 {
			t.Errorf("%v exited %d, want 2", args, code)
		}
	}
}

// --manifest/--source pick the source a plant-fed agent stands in for, and
// its address is where the monitoring project polls.
func TestSnmpServePlantSource(t *testing.T) {
	p, err := loadServePlant(snmpTestdata("snmp_manifest.yaml"), "")
	if err != nil {
		t.Fatal(err)
	}
	if p.source != "SW1" || p.addr == "" {
		t.Fatalf("source %q addr %q", p.source, p.addr)
	}
	if _, err := loadServePlant(snmpTestdata("snmp_manifest.yaml"), "SW9"); err == nil {
		t.Fatal("an unknown --source must be an error")
	}
	two := filepath.Join(t.TempDir(), "two.yaml")
	raw, _ := os.ReadFile(snmpTestdata("snmp_manifest.yaml"))
	s := strings.Replace(string(raw), "sources:\n", "sources:\n  - id: SW2\n    host: 127.0.0.1\n    community-env: X\n", 1)
	if err := os.WriteFile(two, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadServePlant(two, ""); err == nil || !strings.Contains(err.Error(), "--source") {
		t.Fatalf("two sources and no --source: %v", err)
	}
}

// read is the manifest applied to a walk with no device: the same values
// the driver delivers from the switch the walk was recorded on.
func TestSnmpReadOffline(t *testing.T) {
	raw, err := os.ReadFile(snmpTestdata("switch.snmpwalk"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := walk.ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := loadServePlant(snmpTestdata("snmp_manifest.yaml"), "")
	if err != nil {
		t.Fatal(err)
	}
	tags, err := readSnmpOffline(w, p.m, p.source)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := tags["SW1"].(map[string]any)
	if root["Name"] != "sw1-lab" || root["Serial"] != "SN-TEST-0001" {
		t.Fatalf("SW1 = %v", root)
	}
	if _, ok := tags["SW1_Port01"].(map[string]any)["OperUp"]; !ok {
		t.Fatalf("SW1_Port01 = %v", tags["SW1_Port01"])
	}
}

// An import never silently replaces another device's manifest or a hand
// edit: a second switch into the same directory is refused, nothing
// written; the same device again is fine; --force replaces.
func TestSnmpImportRefusesToReplace(t *testing.T) {
	dir := t.TempDir()
	walkPath := snmpTestdata("switch.snmpwalk")
	sw1 := snmpImportInto(t, dir, "--walk", walkPath, "--tag", "SW1", "--host", "192.0.2.2")
	if out, code := captureSnmp(t, "import", "--out", dir, "--walk", walkPath, "--tag", "SW2", "--host", "192.0.2.3"); code == 0 {
		t.Fatalf("SW2 over SW1 was not refused:\n%s", out)
	}
	if got := read(t, filepath.Join(dir, "snmp_manifest.yaml")); got != sw1.manifest {
		t.Fatal("a refused import wrote the manifest anyway")
	}
	if got := read(t, filepath.Join(dir, "tags", "snmp.yaml")); got != sw1.tags {
		t.Fatal("a refused import wrote the tags anyway")
	}
	snmpImportInto(t, dir, "--walk", walkPath, "--tag", "SW1", "--host", "192.0.2.2") // the same bytes: fine
	sw2 := snmpImportInto(t, dir, "--walk", walkPath, "--tag", "SW2", "--host", "192.0.2.3", "--force")
	if sw2.manifest == sw1.manifest || !strings.Contains(sw2.manifest, "SW2") {
		t.Fatal("--force did not replace")
	}
}
