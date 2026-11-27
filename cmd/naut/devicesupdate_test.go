package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/devprofile"
)

const driveID = "abb-acs355-fmbt21"

// srcDir is a --from folder: the built-in profiles named, with edits
// (file -> old -> new) applied.
func srcDir(t *testing.T, files []string, edits map[string][2]string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range files {
		data, err := devprofile.BuiltinFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if e, ok := edits[name]; ok {
			if !bytes.Contains(data, []byte(e[0])) {
				t.Fatalf("%s has no %q to edit", name, e[0])
			}
			data = bytes.Replace(data, []byte(e[0]), []byte(e[1]), 1)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func slurp(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func runUpdate(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	code := runDevicesUpdate(args, &out)
	return out.String(), code
}

func vendorDrive(t *testing.T, program string) string {
	t.Helper()
	dir := libProject(t, program)
	if err := addProfile(dir, driveID, "", io.Discard); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReleasedProfiles(t *testing.T) {
	ps, err := devprofile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		files, err := readProfileFiles(p.ID, devprofile.BuiltinFile)
		if err != nil {
			t.Fatal(err)
		}
		h := files.hash(p.ID)
		if !releasedHashes(p.ID)[h] {
			t.Errorf("built-in %s (version %d) is not in devices_released.txt: add the line `%s %d %s`; if the profile already has a line at version %d, bump version: with the change", p.ID, p.Version, p.ID, p.Version, h, p.Version)
		}
	}
	// One hash per (profile, version), apart from lines marked history.
	seen := map[string]string{}
	for _, line := range strings.Split(releasedProfiles, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || strings.HasPrefix(line, "#") || (len(f) > 3 && f[3] == "history") {
			continue
		}
		k := f[0] + " " + f[1]
		if prev, ok := seen[k]; ok && prev != f[2] {
			t.Errorf("%s has two released hashes: bump its version: when its files change", k)
		}
		seen[k] = f[2]
	}
}

func TestUpdateUpToDate(t *testing.T) {
	dir := vendorDrive(t, libProgram)
	out, code := runUpdate(t, dir)
	if code != 0 || !strings.Contains(out, driveID+": up to date") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestUpdateShowsWhatChangesAndYesReplaces(t *testing.T) {
	dir := vendorDrive(t, libProgram)
	src := srcDir(t, []string{driveID + ".yaml", "abb-drives.st"}, map[string][2]string{
		driveID + ".yaml": {"address: 3,", "address: 7,"},
	})
	// Not edited: say the source differs and nothing else.
	before := slurp(t, dir, "profiles", driveID+".yaml")
	out, code := runUpdate(t, "--from", src, dir)
	if code != 0 || !strings.Contains(out, "- ") || !strings.Contains(out, "+ ") || !strings.Contains(out, "nothing was replaced") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "StatusWord") {
		t.Errorf("the diff does not name the point whose address moved:\n%s", out)
	}
	if got := slurp(t, dir, "profiles", driveID+".yaml"); got != before {
		t.Fatal("update without --yes changed the copy")
	}
	out, code = runUpdate(t, "--from", src, "--yes", dir)
	if code != 0 || !strings.Contains(out, "replaced profiles/"+driveID+".yaml") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := slurp(t, dir, "profiles", driveID+".yaml"); got != slurp(t, src, driveID+".yaml") {
		t.Fatal("--yes did not put the source's file in")
	}
	if out, code = runUpdate(t, "--from", src, dir); code != 0 || !strings.Contains(out, "up to date") {
		t.Fatalf("after the update: exit %d\n%s", code, out)
	}
}

func TestUpdateNeverLosesAHandEditSilently(t *testing.T) {
	dir := vendorDrive(t, libProgram)
	copyPath := filepath.Join(dir, "profiles", driveID+".yaml")
	edited := strings.Replace(slurp(t, copyPath), "address: 3,", "address: 9,", 1)
	if err := os.WriteFile(copyPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runUpdate(t, dir)
	if code != 0 || !strings.Contains(out, "HAND-EDITED") || !strings.Contains(out, "--discard-edits") {
		t.Fatalf("report: exit %d\n%s", code, out)
	}
	out, code = runUpdate(t, "--yes", dir)
	if code == 0 || !strings.Contains(out, "HAND-EDITED") {
		t.Fatalf("--yes alone replaced a hand-edited copy: exit %d\n%s", code, out)
	}
	if got := slurp(t, copyPath); got != edited {
		t.Fatal("--yes alone changed a hand-edited copy")
	}
	// --discard-edits alone is an error: it goes with --yes.
	if _, code = runUpdate(t, "--discard-edits", dir); code != 2 {
		t.Errorf("--discard-edits without --yes: exit %d, want 2", code)
	}
	out, code = runUpdate(t, "--yes", "--discard-edits", dir)
	if code != 0 || !strings.Contains(out, "replaced") {
		t.Fatalf("--discard-edits: exit %d\n%s", code, out)
	}
	want, _ := devprofile.BuiltinFile(driveID + ".yaml")
	if got := slurp(t, copyPath); got != string(want) {
		t.Fatal("--discard-edits did not restore the source")
	}
}

// A copy that is byte-for-byte an earlier release is not an edit, whatever
// differs from today's source.
func TestUpdateAnEarlierReleaseIsNotAnEdit(t *testing.T) {
	dir := vendorDrive(t, libProgram)
	copyPath := filepath.Join(dir, "profiles", driveID+".yaml")
	old := strings.Replace(slurp(t, copyPath), "address: 3,", "address: 9,", 1)
	if err := os.WriteFile(copyPath, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := readProfileFiles(driveID, func(n string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, "profiles", n)) })
	if err != nil {
		t.Fatal(err)
	}
	was := releasedProfiles
	releasedProfiles += driveID + " 1 " + files.hash(driveID) + " history\n"
	defer func() { releasedProfiles = was }()
	out, code := runUpdate(t, "--yes", dir)
	if code != 0 || strings.Contains(out, "HAND-EDITED") || !strings.Contains(out, "replaced") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestUpdateSharedSimulatorMovesTogether(t *testing.T) {
	dir := libProject(t, libProgram)
	for _, id := range []string{driveID, "abb-acs580-fmbt21"} {
		if err := addProfile(dir, id, "", io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	src := srcDir(t, []string{driveID + ".yaml", "abb-acs580-fmbt21.yaml", "abb-drives.st"}, map[string][2]string{
		"abb-drives.st": {"FUNCTION_BLOCK", "(* a newer simulator *)\nFUNCTION_BLOCK"},
	})
	simPath := filepath.Join(dir, "profiles", "abb-drives.st")
	before := slurp(t, simPath)
	out, code := runUpdate(t, "--from", src, "--yes", driveID, dir)
	if code == 0 || !strings.Contains(out, "abb-drives.st is the simulator of "+driveID+" and of abb-acs580-fmbt21") {
		t.Fatalf("one of two profiles sharing a simulator moved alone: exit %d\n%s", code, out)
	}
	if slurp(t, simPath) != before {
		t.Fatal("a refused update changed the shared simulator")
	}
	if out, code = runUpdate(t, "--from", src, "--yes", dir); code != 0 {
		t.Fatalf("both together: exit %d\n%s", code, out)
	}
	if slurp(t, simPath) != slurp(t, src, "abb-drives.st") {
		t.Fatal("the shared simulator was not replaced")
	}
}

func TestUpdateRefusedWhileAProgramReadsARemovedMember(t *testing.T) {
	dir := vendorDrive(t, libProgram)
	src := srcDir(t, []string{driveID + ".yaml", "abb-drives.st"}, map[string][2]string{
		driveID + ".yaml": {"name: StatusWord,", "name: StatusWordX,"},
	})
	// the derived point named the old name: follow the rename
	data, _ := os.ReadFile(filepath.Join(src, driveID+".yaml"))
	data = bytes.ReplaceAll(data, []byte("from: StatusWord,"), []byte("from: StatusWordX,"))
	if err := os.WriteFile(filepath.Join(src, driveID+".yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	before := slurp(t, dir, "profiles", driveID+".yaml")
	out, code := runUpdate(t, "--from", src, "--yes", dir)
	if code == 0 || !strings.Contains(out, "refused: the project's check fails") || !strings.Contains(out, `"StatusWord" not found`) || !strings.Contains(out, "main.st") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if got := slurp(t, dir, "profiles", driveID+".yaml"); got != before {
		t.Fatal("a refused update changed the copy")
	}
}

func TestLineDiff(t *testing.T) {
	got := strings.Join(lineDiff("a\nb\nc\n", "a\nc\nd\n"), "|")
	if got != "- b|+ d" {
		t.Fatalf("lineDiff = %q", got)
	}
	if d := lineDiff("x\n", "x\n"); len(d) != 0 {
		t.Fatalf("same text differs: %v", d)
	}
}

// A copy that is a symlink is neither read through nor written through.
func TestUpdateRefusesASymlinkedCopy(t *testing.T) {
	dir := vendorDrive(t, libProgram)
	target := filepath.Join(t.TempDir(), "elsewhere.yaml")
	copyPath := filepath.Join(dir, "profiles", driveID+".yaml")
	if err := os.Rename(copyPath, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, copyPath); err != nil {
		t.Skip("no symlinks here")
	}
	before := slurp(t, target)
	out, code := runUpdate(t, "--yes", dir)
	if code == 0 || !strings.Contains(out, "symlink") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if slurp(t, target) != before {
		t.Fatal("update wrote through a symlink")
	}
}

// A profile of the project's own, updated from a --from folder: no release
// list says its copy is unedited, so it is treated as edited.
func TestUpdateFromFolderOfAProfileOfYourOwnNeedsDiscardEdits(t *testing.T) {
	dir := vendorDrive(t, libProgram)
	own := slurp(t, dir, "profiles", driveID+".yaml")
	own = strings.Replace(own, "profile: "+driveID, "profile: mine", 1)
	sim := slurp(t, dir, "profiles", "abb-drives.st")
	mk := func(address string) string {
		d := t.TempDir()
		os.WriteFile(filepath.Join(d, "mine.yaml"), []byte(strings.Replace(own, "address: 3,", "address: "+address+",", 1)), 0o644)
		os.WriteFile(filepath.Join(d, "abb-drives.st"), []byte(sim), 0o644)
		return d
	}
	pd := filepath.Join(dir, "profiles")
	os.WriteFile(filepath.Join(pd, "mine.yaml"), []byte(own), 0o644)
	src := mk("8")
	before := slurp(t, pd, "mine.yaml")
	out, code := runUpdate(t, "--from", src, "--yes", "mine", dir)
	if code == 0 || !strings.Contains(out, "HAND-EDITED, as far as naut can tell") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if slurp(t, pd, "mine.yaml") != before {
		t.Fatal("--yes alone replaced a copy no release list vouches for")
	}
	if out, code = runUpdate(t, "--from", src, "--yes", "--discard-edits", "mine", dir); code != 0 {
		t.Fatalf("--discard-edits: exit %d\n%s", code, out)
	}
}

// What a source calls its simulator is the source's to say, but it may not
// name a file of the project's.
func TestUpdateSourceCannotNameAnotherFileAsItsSimulator(t *testing.T) {
	dir := vendorDrive(t, libProgram)
	for _, bad := range []string{"nautilus.yaml", "notes.md"} {
		src := srcDir(t, []string{driveID + ".yaml", "abb-drives.st"}, map[string][2]string{
			driveID + ".yaml": {"source: abb-drives.st", "source: " + bad},
		})
		os.WriteFile(filepath.Join(src, bad), []byte("x"), 0o644)
		os.WriteFile(filepath.Join(dir, "profiles", "notes.md"), []byte("my notes"), 0o644)
		before := slurp(t, dir, "profiles", driveID+".yaml")
		out, code := runUpdate(t, "--from", src, "--yes", "--discard-edits", driveID, dir)
		if code == 0 || !strings.Contains(out, "cannot compare") {
			t.Fatalf("%s: exit %d\n%s", bad, code, out)
		}
		if slurp(t, dir, "profiles", driveID+".yaml") != before || slurp(t, dir, "profiles", "notes.md") != "my notes" {
			t.Fatalf("%s: a file was changed", bad)
		}
	}
	// the project's files live outside profiles/, but a name that exists in
	// it is refused too
	src := srcDir(t, []string{driveID + ".yaml", "abb-drives.st"}, map[string][2]string{
		driveID + ".yaml": {"source: abb-drives.st", "source: other.st"},
	})
	os.WriteFile(filepath.Join(src, "other.st"), []byte("FUNCTION_BLOCK X\nEND_FUNCTION_BLOCK\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "profiles", "other.st"), []byte("mine"), 0o644)
	if out, code := runUpdate(t, "--from", src, "--yes", driveID, dir); code == 0 || !strings.Contains(out, "would replace a file") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if slurp(t, dir, "profiles", "other.st") != "mine" {
		t.Fatal("the project's own file was replaced")
	}
}

// A project whose check already fails may still be updated, but not into
// new failures.
func TestUpdateOfAProjectAlreadyFailingMayNotAddFailures(t *testing.T) {
	prog := libProgram + "\nPROGRAM Other\nX := Nope1;\nEND_PROGRAM\n"
	dir := vendorDrive(t, strings.Replace(libProgram, "END_PROGRAM", "END_PROGRAM", 1))
	os.WriteFile(filepath.Join(dir, "other.st"), []byte("PROGRAM Other\nTripped := Nope1;\nEND_PROGRAM\n"), 0o644)
	_ = prog
	m := slurp(t, dir, "nautilus.yaml")
	os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(strings.Replace(m, "tasks: [{ program: main.st }]", "tasks: [{ program: main.st }, { program: other.st }]", 1)), 0o644)
	if _, code, _ := runCheckCaptured(dir); code == 0 {
		t.Fatal("the fixture's check should already fail")
	}
	renamed := srcDir(t, []string{driveID + ".yaml", "abb-drives.st"}, map[string][2]string{
		driveID + ".yaml": {"name: StatusWord,", "name: StatusWordX,"},
	})
	data, _ := os.ReadFile(filepath.Join(renamed, driveID+".yaml"))
	os.WriteFile(filepath.Join(renamed, driveID+".yaml"), bytes.ReplaceAll(data, []byte("from: StatusWord,"), []byte("from: StatusWordX,")), 0o644)
	before := slurp(t, dir, "profiles", driveID+".yaml")
	out, code := runUpdate(t, "--from", renamed, "--yes", dir)
	if code == 0 || !strings.Contains(out, `"StatusWord" not found`) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if strings.Contains(out, "Nope1") {
		t.Errorf("the refusal blames a failure the project already had:\n%s", out)
	}
	if slurp(t, dir, "profiles", driveID+".yaml") != before {
		t.Fatal("a refused update changed the copy")
	}
}
