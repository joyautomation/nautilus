package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/devprofile"
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
	names, zr := buildArchive(t, dir)
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

// buildArchive runs naut build on dir and opens the project it embeds.
func buildArchive(t *testing.T, dir string) (map[string]bool, *zip.Reader) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "ctl")
	if output, code := buildIn(t, dir, "-o", out); code != 0 {
		t.Fatalf("build: %d\n%s", code, output)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
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
	return names, zr
}

// driveProject declares a drive by profile, vendors it, and runs it up to
// a reference: the ABB Drives profile's start sequence (fena pp. 91-94:
// 16#0476 to READY TO SWITCH ON, 16#047F to OPERATION ENABLED; status bit
// 2 OPERATION ENABLED, bit 3 TRIPPED). It has no virtual-devices.yaml: the
// devices: row is the virtual device.
func driveProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("nautilus.yaml", `name: drive
tasks: [{ program: main.st }]
devices:
  - { name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.11, unit-id: 3 }
tags:
  - { name: Start, role: state, type: BOOL }
  - { name: Speed, role: state, type: INT }
  - { name: Running, role: state, type: BOOL }
  - { name: Tripped, role: state, type: BOOL }
`)
	// The first value written is the driver's start-up baseline, never
	// sent; the sequence starts from the change after it, as on the wire.
	write("main.st", `PROGRAM Main
VAR Up : BOOL; END_VAR
IF NOT Up THEN
  CONV_ControlWord := 0;
  CONV_Reference1 := 0;
  Up := TRUE;
ELSIF Start AND CONV.StatusWord.0 THEN
  CONV_ControlWord := 16#047F;   (* READY TO SWITCH ON reached: run *)
  CONV_Reference1 := 10000;
ELSE
  CONV_ControlWord := 16#0476;   (* OFF1: to READY TO SWITCH ON *)
  CONV_Reference1 := 10000;
END_IF;
Speed := CONV.Actual1;
Running := CONV.StatusWord.2;
Tripped := CONV.StatusWord.3;
END_PROGRAM
`)
	write("main_test.yaml", `devices: true
tests:
  - name: the drive runs up to its reference
    steps:
      - advance: 1s
        expect: { Running: false, Speed: 0 }
      - given: { Start: true }
        until: 30s
        expect: { Running: true, Speed: 10000 }
  - name: a trip reads back
    steps:
      - given: { Start: true }
        until: 30s
        expect: { Running: true }
      - plant: { CONV_Trip: true }
        advance: 500ms
        expect: { Tripped: true, Running: false }
`)
	if err := addProfile(dir, "abb-acs355-fmbt21", "", io.Discard); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A project's devices: rows are its virtual devices (brief §3.6): naut
// test runs a devices: true suite against them with no
// virtual-devices.yaml, and the program reads each device as its struct
// and commands it through its command tags; naut check and naut devices
// check compile the same plant.
func TestDevicesRowsAreVirtualDevices(t *testing.T) {
	dir := driveProject(t)
	if code := runTest([]string{dir}); code != 0 {
		t.Fatalf("naut test exited %d", code)
	}
	if code := runCheck([]string{dir}); code != 0 {
		t.Fatalf("naut check exited %d", code)
	}
	if code := runDevicesCheck([]string{dir}); code != 0 {
		t.Fatalf("naut devices check exited %d", code)
	}
	// The suite is not vacuous: a speed the drive never reaches fails it.
	suite := mustRead(t, filepath.Join(dir, "main_test.yaml"))
	if err := os.WriteFile(filepath.Join(dir, "main_test.yaml"), bytes.Replace(suite, []byte("Speed: 10000"), []byte("Speed: 12000"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runTest([]string{dir}); code == 0 {
		t.Fatal("naut test passed a speed the drive never reaches")
	}
}

// The virtual device simulates the project's vendored copy, the same one
// the controller derives its bindings from: a copy that swapped Actual 1
// and Actual 2 still agrees with its virtual device (with the built-in
// profile the controller's Actual1 would read the drive's Actual 2, 0).
func TestDevicesVirtualDeviceIsTheVendoredCopy(t *testing.T) {
	dir := driveProject(t)
	pf := filepath.Join(dir, "profiles", "abb-acs355-fmbt21.yaml")
	raw := mustRead(t, pf)
	moved := bytes.Replace(raw, []byte("type: INT,  table: holding, address: 4,"), []byte("type: INT,  table: holding, address: 9,"), 1)
	moved = bytes.Replace(moved, []byte("type: INT,  table: holding, address: 5,"), []byte("type: INT,  table: holding, address: 4,"), 1)
	moved = bytes.Replace(moved, []byte("type: INT,  table: holding, address: 9,"), []byte("type: INT,  table: holding, address: 5,"), 1)
	if bytes.Equal(moved, raw) || !bytes.Contains(moved, []byte("Actual1,     type: INT,  table: holding, address: 5,")) {
		t.Fatal("the fixture edit did not apply")
	}
	if err := os.WriteFile(pf, moved, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runTest([]string{dir}); code != 0 {
		t.Fatalf("naut test exited %d", code)
	}
}

// naut check compiles the plant of a project whose only virtual devices
// are its devices: rows: a vendored profile whose simulator block is not
// in its source fails check, as one in virtual-devices.yaml would. (The
// source on its own still compiles: only the plant shows the mistake.)
func TestCheckCompilesTheRowsPlant(t *testing.T) {
	dir := driveProject(t)
	if code := runCheck([]string{dir}); code != 0 {
		t.Fatalf("naut check exited %d", code)
	}
	pf := filepath.Join(dir, "profiles", "abb-acs355-fmbt21.yaml")
	raw := mustRead(t, pf)
	wrong := bytes.Replace(raw, []byte("block: ABB_DRIVES_SIM,"), []byte("block: ABB_DRIVES_SIMX,"), 1)
	if bytes.Equal(wrong, raw) {
		t.Fatal("the fixture edit did not apply")
	}
	if err := os.WriteFile(pf, wrong, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runCheck([]string{dir}); code == 0 {
		t.Fatal("naut check passed a vendored profile whose simulator block does not exist")
	}
}

// virtual-devices.yaml adds what a simulation needs to a declared device
// (init here) and the process model; every command reads it the same way.
func TestDevicesRowsWithVirtualDevicesFile(t *testing.T) {
	dir := driveProject(t)
	if err := os.WriteFile(filepath.Join(dir, virtdev.FileName), []byte("devices:\n  - { name: CONV, init: { Trip: true } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main_test.yaml"), []byte(`devices: true
tests:
  - name: a drive that powers up tripped never runs
    steps:
      - given: { Start: true }
        advance: 5s
        expect: { Tripped: true, Running: false }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func([]string) int{"test": runTest, "check": runCheck, "devices check": runDevicesCheck} {
		if code := run([]string{dir}); code != 0 {
			t.Errorf("naut %s exited %d", name, code)
		}
	}
	// An entry there that says what the device is, again, is refused by each.
	if err := os.WriteFile(filepath.Join(dir, virtdev.FileName), []byte("devices:\n  - { name: CONV, endpoint: 10.0.0.99 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func([]string) int{"test": runTest, "check": runCheck, "devices check": runDevicesCheck} {
		if code := run([]string{dir}); code == 0 {
			t.Errorf("naut %s accepted a virtual-devices.yaml entry that repeats a devices: row's endpoint", name)
		}
	}
}

// naut build ships the vendored profile a devices: row needs (the
// controller derives the device from it at load) even though the row is a
// virtual device too; the simulator and the process model, test fixtures,
// stay behind.
func TestBuildShipsVendoredProfileOfAVirtualDevice(t *testing.T) {
	dir := driveProject(t)
	if err := os.WriteFile(filepath.Join(dir, virtdev.FileName), []byte("devices:\n  - { name: CONV, init: { Actual2: 0 } }\nmodel: { program: plant/process.st }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "plant"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plant", "process.st"), []byte("PROGRAM Process\nCONV_Running := CONV_Running;\nEND_PROGRAM\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runDevicesCheck([]string{dir}); code != 0 {
		t.Fatalf("naut devices check exited %d", code)
	}
	names, zr := buildArchive(t, dir)
	if !names["profiles/abb-acs355-fmbt21.yaml"] {
		t.Errorf("the vendored profile did not ship: %v", names)
	}
	for _, f := range []string{"profiles/abb-drives.st", virtdev.FileName, "main_test.yaml", "plant/process.st"} {
		if names[f] {
			t.Errorf("the fixture %s shipped in the controller", f)
		}
	}
	if _, err := project.Load(zr, ""); err != nil {
		t.Errorf("the built controller's project does not load: %v", err)
	}
}

// add --from never writes outside profiles/: an id or a simulator source
// with a directory in it (both reproduced in review) is refused, and
// nothing lands anywhere. The fixtures sit where "../" lands from the team
// folder, so each name would really resolve to a file.
func TestDevicesAddStaysInProfiles(t *testing.T) {
	base := strings.Replace(string(mustRead(t, "../../devprofile/profiles/asi-275m.yaml")), "simulator:", "#simulator:", 1)
	for name, tc := range map[string]struct{ id, file, yaml, want string }{
		// the parser's guard (devprofile's own tests pin it alone)
		"simulator source": {"evil", "team/evil.yaml", strings.Replace(base, "profile: asi-275m", "profile: evil\nsimulator: { block: ASI_275M_SIM, source: ../evil_injected.st }", 1), "must be a file name beside the profile"},
		// add's own guard, before it reads anything
		"profile id": {"../escaped", "escaped.yaml", strings.Replace(base, "profile: asi-275m", "profile: ../escaped\nudt: ESC", 1), "is not a profile id"},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "proj")
			if err := os.Rename(libProject(t, libProgram), dir); err != nil {
				t.Fatal(err)
			}
			team := filepath.Join(root, "team")
			if err := os.Mkdir(team, 0o755); err != nil {
				t.Fatal(err)
			}
			for f, body := range map[string]string{tc.file: tc.yaml, "evil_injected.st": "PROGRAM X\nEND_PROGRAM\n"} {
				if err := os.WriteFile(filepath.Join(root, f), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadDir(dir)
			err := addProfile(dir, tc.id, team, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("add: %v, want an error saying %q", err, tc.want)
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
}

// Whatever name reaches the writer, it writes only a plain file name in
// profiles/ itself: the last guard, behind the parser's and add's.
func TestDevicesAddWriterStaysInDir(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "profiles")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../x.st", "sub/x.st", "..", "/abs.st"} {
		if _, err := existingCopy(dst, name, []byte("x")); err == nil {
			t.Errorf("existingCopy(%q): no error", name)
		}
		if err := createInDir(dst, name, []byte("x")); err == nil {
			t.Errorf("createInDir(%q): no error", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "x.st")); err == nil {
		t.Error("createInDir wrote ../x.st")
	}
	if err := createInDir(dst, "abb-drives.st", []byte("x")); err != nil {
		t.Errorf("createInDir(abb-drives.st): %v", err)
	}
	// A link planted after add looked (a race) is not followed either.
	symlink(t, filepath.Join(root, "planted.st"), filepath.Join(dst, "late.st"))
	if err := createInDir(dst, "late.st", []byte("x")); err == nil {
		t.Error("createInDir wrote through a dangling symlink")
	}
	if _, err := os.Lstat(filepath.Join(root, "planted.st")); err == nil {
		t.Error("createInDir created the symlink's target outside profiles/")
	}
}

// A symlink planted in profiles/, or profiles/ itself a symlink, never
// carries add's copy outside the project (reproduced in review): add
// refuses, and nothing appears where the link points.
func TestDevicesAddRefusesSymlinks(t *testing.T) {
	builtinSim, err := devprofile.BuiltinFile("abb-drives.st")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		plant func(t *testing.T, dir, outside string)
		want  string
	}{
		"dangling simulator link": {func(t *testing.T, dir, outside string) {
			mkdir(t, filepath.Join(dir, devprofileDir))
			symlink(t, filepath.Join(outside, "planted.st"), filepath.Join(dir, devprofileDir, "abb-drives.st"))
		}, "is a symlink"},
		"simulator link to an identical file": {func(t *testing.T, dir, outside string) {
			if err := os.WriteFile(filepath.Join(outside, "planted.st"), builtinSim, 0o644); err != nil {
				t.Fatal(err)
			}
			mkdir(t, filepath.Join(dir, devprofileDir))
			symlink(t, filepath.Join(outside, "planted.st"), filepath.Join(dir, devprofileDir, "abb-drives.st"))
		}, "is a symlink"},
		"dangling profile link": {func(t *testing.T, dir, outside string) {
			mkdir(t, filepath.Join(dir, devprofileDir))
			symlink(t, filepath.Join(outside, "planted.yaml"), filepath.Join(dir, devprofileDir, "abb-acs355-fmbt21.yaml"))
		}, "already in profiles/"},
		"profiles is a link": {func(t *testing.T, dir, outside string) {
			symlink(t, outside, filepath.Join(dir, devprofileDir))
		}, "not a folder"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := libProject(t, libProgram)
			outside := t.TempDir()
			tc.plant(t, dir, outside)
			before, _ := os.ReadDir(outside)
			err := addProfile(dir, "abb-acs355-fmbt21", "", io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("add: %v, want an error saying %q", err, tc.want)
			}
			after, _ := os.ReadDir(outside)
			if len(after) != len(before) {
				t.Errorf("add wrote through the link: %d -> %d files outside the project", len(before), len(after))
			}
		})
	}
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
}

// Flags may follow the profile, as the brief writes the command.
func TestDevicesAddFlagAfterProfile(t *testing.T) {
	dir := libProject(t, libProgram)
	team := t.TempDir()
	if err := os.WriteFile(filepath.Join(team, "asi-275m.yaml"), mustRead(t, "../../devprofile/profiles/asi-275m.yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(team, "asi-275m.st"), mustRead(t, "../../devprofile/profiles/asi-275m.st"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(team, "asi-275m.yaml"), append(mustRead(t, filepath.Join(team, "asi-275m.yaml")), []byte("# the team's copy\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runDevicesAdd([]string{"asi-275m", dir, "--from", team}, io.Discard); code != 0 {
		t.Fatalf("add <profile> <dir> --from team: exit %d", code)
	}
	if !bytes.HasSuffix(mustRead(t, filepath.Join(dir, devprofileDir, "asi-275m.yaml")), []byte("# the team's copy\n")) {
		t.Error("--from after the profile was ignored: the built-in copy was vendored")
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
