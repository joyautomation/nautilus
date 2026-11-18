package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/virtdev"
)

// libProject writes a project that declares a drive by profile; the
// profile is not vendored yet.
func libProject(t *testing.T, program string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("nautilus.yaml", `name: lib
tasks: [{ program: main.st }]
devices:
  - { name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.11, desc: Conveyor drive }
tags:
  - { name: Tripped, role: state, type: BOOL }
`)
	write("main.st", program)
	return dir
}

const libProgram = `PROGRAM Main
Tripped := CONV.StatusWord.3;
IF Tripped THEN
  CONV_ControlWord := 16#0080;
END_IF;
END_PROGRAM
`

func TestDevicesAddVendors(t *testing.T) {
	dir := libProject(t, libProgram)
	if err := addProfile(dir, "abb-acs355-fmbt21", "", io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"abb-acs355-fmbt21.yaml", "abb-drives.st"} {
		if _, err := os.Stat(filepath.Join(dir, "profiles", f)); err != nil {
			t.Errorf("profiles/%s not vendored: %v", f, err)
		}
	}
	// The copy is the pin: adding again does not overwrite it.
	pinned := filepath.Join(dir, "profiles", "abb-acs355-fmbt21.yaml")
	if err := os.WriteFile(pinned, append(mustRead(t, pinned), []byte("# edited here\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := addProfile(dir, "abb-acs355-fmbt21", "", io.Discard); err == nil || !strings.Contains(err.Error(), "already in profiles/") {
		t.Errorf("second add: %v", err)
	}
	if !bytes.HasSuffix(mustRead(t, pinned), []byte("# edited here\n")) {
		t.Error("the second add overwrote the project's copy")
	}
	// A profile sharing the simulator file adds beside it ...
	if err := addProfile(dir, "abb-acs550-reta01", "", io.Discard); err != nil {
		t.Errorf("a profile sharing the same simulator: %v", err)
	}
	// ... unless the project's copy of that file differs: then nothing is
	// written at all.
	if err := os.WriteFile(filepath.Join(dir, "profiles", "abb-drives.st"), []byte("(* changed *)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := addProfile(dir, "abb-acs580-fmbt21", "", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "differs") {
		t.Errorf("a different simulator of the same name: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "profiles", "abb-acs580-fmbt21.yaml")); err == nil {
		t.Error("the refused add wrote the profile anyway")
	}
	// A team's own folder of profiles.
	team := t.TempDir()
	raw := strings.Replace(string(mustRead(t, "../../devprofile/profiles/asi-275m.yaml")), "profile: asi-275m", "profile: our-board", 1)
	raw = strings.Replace(raw, "simulator:", "#simulator:", 1)
	if err := os.WriteFile(filepath.Join(team, "our-board.yaml"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := addProfile(dir, "our-board", team, io.Discard); err != nil {
		t.Errorf("--from: %v", err)
	}
	if err := addProfile(dir, "nope", "", io.Discard); err == nil || !strings.Contains(err.Error(), "no built-in profile") {
		t.Errorf("unknown profile: %v", err)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// naut check knows a device's type: a project using its members checks
// clean once the profile is vendored, and a misspelt member is an error.
func TestDevicesProjectChecks(t *testing.T) {
	dir := libProject(t, libProgram)
	if code := runCheck([]string{dir}); code == 0 {
		t.Error("check passed with the profile not vendored")
	}
	if err := addProfile(dir, "abb-acs355-fmbt21", "", io.Discard); err != nil {
		t.Fatal(err)
	}
	var code int
	out := captureStdout(t, func() int { code = runCheck([]string{dir}); return code })
	if code != 0 {
		t.Fatalf("check exited %d:\n%s", code, out)
	}
	if strings.Contains(out, "warning") {
		t.Errorf("a device's unwritten command tags are not dead outputs:\n%s", out)
	}
	bad := libProject(t, strings.Replace(libProgram, "StatusWord", "StatusWrod", 1))
	if err := addProfile(bad, "abb-acs355-fmbt21", "", io.Discard); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() int { code = runCheck([]string{bad}); return code })
	if code == 0 || !strings.Contains(out, `"StatusWrod" not found on ABB_ACS355_FMBT21`) {
		t.Errorf("a misspelt member: exit %d\n%s", code, out)
	}
}

func TestDevicesRender(t *testing.T) {
	dir := libProject(t, libProgram)
	if err := addProfile(dir, "abb-acs355-fmbt21", "", io.Discard); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if code := runDevicesRender([]string{dir}, &b); code != 0 {
		t.Fatalf("render exited %d", code)
	}
	for _, want := range []string{"ABB_ACS355_FMBT21 : STRUCT", "CONV_ControlWord  output", "CONV.StatusWord   read", "CONV_ControlWord  write"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("render lacks %q:\n%s", want, b.String())
		}
	}
}

// A built controller carries the vendored profile (it loads its devices
// from it) but not the simulator beside it, and loads from its own archive.
func TestBuildShipsVendoredProfiles(t *testing.T) {
	dir := libProject(t, libProgram)
	if err := addProfile(dir, "abb-acs355-fmbt21", "", io.Discard); err != nil {
		t.Fatal(err)
	}
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
	names := map[string]bool{}
	for _, zf := range zr.File {
		names[zf.Name] = true
	}
	if !names["profiles/abb-acs355-fmbt21.yaml"] {
		t.Errorf("the vendored profile did not ship: %v", names)
	}
	if names["profiles/abb-drives.st"] {
		t.Error("the simulator shipped in the controller")
	}
	if _, err := project.Load(zr, ""); err != nil {
		t.Errorf("the built controller's project does not load: %v", err)
	}
}

// Until virtual devices are built from devices: rows, a suite that asks
// for virtual devices in a project that declares devices: is refused by
// name, not run with inputs that never arrive.
func TestDevicesWithVirtualDevicesRefused(t *testing.T) {
	dir := libProject(t, libProgram)
	if err := addProfile(dir, "abb-acs355-fmbt21", "", io.Discard); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"virtual-devices.yaml": "devices: [{ name: SIM, profile: asi-275m, endpoint: 10.0.0.12 }]\n",
		"main_test.yaml":       "devices: true\ntests:\n  - name: t\n    steps:\n      - advance: 1s\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code := runTest([]string{dir}); code == 0 {
		t.Fatal("naut test passed a devices: project against virtual devices it cannot build yet")
	}
	proj, err := project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	set, err := virtdev.LoadSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := virtdev.NewInProcess(set, acceptance.NewClock(), proj.Drivers()); err == nil || !strings.Contains(err.Error(), "carries devices: declared in the manifest") {
		t.Errorf("NewInProcess: %v", err)
	}
}

// add --from never writes outside profiles/: an id or a simulator source
// with a directory in it (both reproduced in review) is refused, and
// nothing lands anywhere.
func TestDevicesAddStaysInProfiles(t *testing.T) {
	base := strings.Replace(string(mustRead(t, "../../devprofile/profiles/asi-275m.yaml")), "simulator:", "#simulator:", 1)
	for name, tc := range map[string]struct{ id, file, yaml string }{
		"simulator source": {"evil", "evil.yaml", strings.Replace(base, "profile: asi-275m", "profile: evil\nsimulator: { block: ASI_275M_SIM, source: ../evil_injected.st }", 1)},
		"profile id":       {"../escaped", "escaped.yaml", strings.Replace(base, "profile: asi-275m", "profile: ../escaped\nudt: ESC", 1)},
	} {
		t.Run(name, func(t *testing.T) {
			dir := libProject(t, libProgram)
			team := t.TempDir()
			for _, f := range []string{tc.file, "evil_injected.st"} {
				if err := os.WriteFile(filepath.Join(team, f), []byte(tc.yaml), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadDir(dir)
			if err := addProfile(filepath.Join(dir), tc.id, team, io.Discard); err == nil {
				t.Fatal("added")
			}
			after, _ := os.ReadDir(dir)
			if len(after) != len(before) {
				t.Errorf("files appeared in the project: %d -> %d", len(before), len(after))
			}
			if _, err := os.Stat(filepath.Join(dir, devprofileDir)); err == nil {
				t.Error("profiles/ was created for a refused add")
			}
		})
	}
	for _, name := range []string{"../x.st", "/abs.st", "sub/x.st", ".."} {
		if insideDir("/p/profiles", name) == nil {
			t.Errorf("insideDir(%q): no error", name)
		}
	}
	if err := insideDir("/p/profiles", "abb-drives.st"); err != nil {
		t.Errorf("insideDir(abb-drives.st): %v", err)
	}
}

const devprofileDir = "profiles"

// A point the row does not select is not a member: reading it fails check
// instead of reading zero, Good, forever.
func TestDevicesUnselectedPointIsNotAMember(t *testing.T) {
	dir := libProject(t, "PROGRAM Main\nTripped := CONV.StatusWord.3;\nCONV_ControlWord := CONV.Actual1;\nEND_PROGRAM\n")
	raw := strings.Replace(string(mustRead(t, filepath.Join(dir, "nautilus.yaml"))), "desc: Conveyor drive", "desc: Conveyor drive, points: [ControlWord, StatusWord]", 1)
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := addProfile(dir, "abb-acs355-fmbt21", "", io.Discard); err != nil {
		t.Fatal(err)
	}
	var code int
	out := captureStdout(t, func() int { code = runCheck([]string{dir}); return code })
	if code == 0 || !strings.Contains(out, `"Actual1" not found on ABB_ACS355_FMBT21`) {
		t.Errorf("reading an unselected point: exit %d\n%s", code, out)
	}
}
