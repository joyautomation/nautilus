package main

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/virtdev"
)

const binFill = "../../virtdev/testdata/bin-fill"

// copyFixture copies the bin-fill project into a temp dir, applying edits
// (file -> old -> new) on the way.
func copyFixture(t *testing.T, edits map[string][2]string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(binFill, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(binFill, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if e, ok := edits[rel]; ok {
			if !strings.Contains(string(raw), e[0]) {
				t.Fatalf("%s: no %q to edit", rel, e[0])
			}
			raw = []byte(strings.Replace(string(raw), e[0], e[1], 1))
		}
		return os.WriteFile(out, raw, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// runDevicesSuite runs a project's fill_test.yaml against its virtual
// devices, as `naut test` does.
func runDevicesSuite(t *testing.T, dir string) []acceptance.Result {
	t.Helper()
	fsys := os.DirFS(dir)
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	set, err := virtdev.LoadSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	suite, err := acceptance.LoadSuite(fsys, "fill_test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	drivers := proj.Drivers()
	rs, err := acceptance.RunSuite(suite, proj.Runtime, acceptance.WithDevices(func(clk runtime.Clock) (acceptance.DeviceDriver, error) {
		return virtdev.NewInProcess(set, clk, drivers)
	}))
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestDevicesSequencePasses(t *testing.T) {
	if code := runTest([]string{binFill}); code != 0 {
		t.Fatalf("naut test %s exited %d", binFill, code)
	}
	if code := runCheck([]string{binFill}); code != 0 {
		t.Fatalf("naut check %s exited %d", binFill, code)
	}
	if code := runDevicesCheck([]string{binFill}); code != 0 {
		t.Fatalf("naut devices check %s exited %d", binFill, code)
	}
}

// Two runs, the same trajectory: the plant steps on the test's clock.
func TestDevicesSequenceDeterministic(t *testing.T) {
	a, b := runDevicesSuite(t, binFill), runDevicesSuite(t, binFill)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("two runs differ:\n%+v\n%+v", a, b)
	}
}

// What the memory stub cannot catch: a mistake in the project's own
// register map, node ids or scaling fails the sequence test.
func TestDevicesCatchManifestMistakes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   map[string][2]string
		reason string // in the failure of the first test
		test   int
	}{
		{
			name:   "status word at the wrong register",
			edit:   map[string][2]string{"modbus_manifest.yaml": {"table: holding, address: 3,   format: uint16 }", "table: holding, address: 9,   format: uint16 }"}},
			reason: "exception 02",
		},
		{
			name:   "a node id the master does not serve",
			edit:   map[string][2]string{"opcua_manifest.yaml": {"ns=1;s=IOLM/Port 1/SIO Input", "ns=1;s=IOLM/Port 1/SIO Inptu"}},
			reason: "serves no such node",
		},
		{
			name:   "the current scaled by a hundredth, not a tenth",
			edit:   map[string][2]string{"modbus_manifest.yaml": {"format: uint16, scale: 0.1", "format: uint16, scale: 0.01"}},
			reason: "Conv_Amps",
			test:   1,
		},
		{
			// The trap the baseline sets: the first output push is
			// recorded, not sent, so a start sequence that relies on its
			// first-scan 0x0476 reaching the drive never gets past
			// SWITCH-ON INHIBITED. The memory stub would pass this.
			name: "a start that relies on the first push reaching the drive",
			edit: map[string][2]string{"fill.st": {`    IF Conv_SW.0 OR Conv_SW.1 THEN
      Conv_CW := 16#047F;
    ELSE
      Conv_CW := 16#0476;
    END_IF;`, `    Conv_CW := 16#047F;`}},
			reason: "Step",
		},
		{
			name:   "the drive's unit id",
			edit:   map[string][2]string{"modbus_manifest.yaml": {"{ id: CONV, host: 10.77.1.101, port: 502, unitid: 1 }", "{ id: CONV, host: 10.77.1.101, port: 502, unitid: 2 }"}},
			reason: "no virtual device",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := copyFixture(t, tc.edit)
			if strings.Contains(tc.reason, "no virtual device") {
				// A source no device answers is refused before any scan.
				fsys := os.DirFS(dir)
				proj, err := project.Load(fsys, "")
				if err != nil {
					t.Fatal(err)
				}
				set, err := virtdev.LoadSet(dir)
				if err != nil {
					t.Fatal(err)
				}
				_, err = virtdev.NewInProcess(set, acceptance.NewClock(), proj.Drivers())
				if err == nil || !strings.Contains(err.Error(), tc.reason) {
					t.Fatalf("NewInProcess: %v, want %q", err, tc.reason)
				}
				return
			}
			rs := runDevicesSuite(t, dir)
			r := rs[tc.test]
			if r.Passed {
				t.Fatalf("%q passed with the mistake in place", r.Name)
			}
			got := r.Failure.Reason + " " + r.Failure.Detail
			if !strings.Contains(got, tc.reason) {
				t.Fatalf("failure %q does not mention %q", got, tc.reason)
			}
		})
	}
}

// The real drivers' companions arrive: logic gated on a source's __Online
// sees it true.
func TestDevicesDeliverCompanions(t *testing.T) {
	fsys := os.DirFS(binFill)
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	set, err := virtdev.LoadSet(binFill)
	if err != nil {
		t.Fatal(err)
	}
	ip, err := virtdev.NewInProcess(set, acceptance.NewClock(), proj.Drivers())
	if err != nil {
		t.Fatal(err)
	}
	vals, _ := ip.ReadInputs()
	for _, name := range []string{"CONV__Online", "RB1__Online"} {
		if vals[name] != true {
			t.Errorf("%s = %v, want true", name, vals[name])
		}
	}
	for _, name := range ip.Inputs() {
		if _, ok := vals[name]; !ok {
			t.Errorf("the drivers deliver %s, the devices did not", name)
		}
	}
	if err := ip.Err(); err != nil {
		t.Errorf("faults: %v", err)
	}
}

// The virtual devices are test fixtures: a built controller carries the
// project's programs and manifests, not its devices or their model.
func TestBuildLeavesTheVirtualDevicesOut(t *testing.T) {
	dir := copyFixture(t, nil)
	out := filepath.Join(t.TempDir(), "ctl")
	if output, code := buildIn(t, dir, "-o", out); code != 0 {
		t.Fatalf("build: %d\n%s", code, output)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	off, ok := embeddedOffset(f, st.Size())
	if !ok {
		t.Fatal("no embedded project")
	}
	zr, err := zip.NewReader(io.NewSectionReader(f, off, st.Size()-16-off), st.Size()-16-off)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, zf := range zr.File {
		names = append(names, zf.Name)
	}
	got := strings.Join(names, " ")
	for _, gone := range []string{virtdev.FileName, "devices/plant.st", "fill_test.yaml"} {
		if strings.Contains(got, gone) {
			t.Errorf("%s shipped in the controller: %s", gone, got)
		}
	}
	if !strings.Contains(got, "fill.st") || !strings.Contains(got, "modbus_manifest.yaml") {
		t.Errorf("the controller's own files are missing: %s", got)
	}
}
