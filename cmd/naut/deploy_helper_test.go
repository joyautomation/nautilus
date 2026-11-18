package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The helper mode (#771) and the state directory's guards. The helper is
// the root boundary for an SSH user who may run nothing else as root, and
// that user may deploy a controller of their own, which runs as the owner
// of STATE. These tests run box.sh (as the helper, or plainly) under the
// stand-in systemctl of deploy_box_test.go, as the test user: enough to
// see a link being followed, a file outside being read or written, or an
// argument getting through.

// canary is a file outside ROOT and STATE that nothing may read, write or
// chown through a link.
func canary(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "canary")
	if err := os.WriteFile(p, []byte("CANARY-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func canaryIntact(t *testing.T, p string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "CANARY-SECRET" {
		t.Errorf("the canary outside STATE was changed: %q (%v)", b, err)
	}
}

// helperFile writes a helper pinned to e's directories and returns its path.
func (e *boxEnv) helperFile() string {
	e.t.Helper()
	tmpl := filepath.Join(e.root, "..", "unit-template")
	if err := os.WriteFile(tmpl, append([]byte("# TEMPLATE-UNIT\n"), deployUnit...), 0o644); err != nil {
		e.t.Fatal(err)
	}
	script, err := helperScript(helperPaths{root: e.root, state: e.state, unitDir: e.units, unitSrc: tmpl,
		path: e.bin + ":/usr/bin:/bin"})
	if err != nil {
		e.t.Fatal(err)
	}
	p := filepath.Join(e.root, "..", "naut-deploy-box")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// helper runs the helper with stdin, in an environment that tries to steer
// it elsewhere: none of it may count.
func (e *boxEnv) helper(h string, stdin []byte, args ...string) (string, int) {
	e.t.Helper()
	elsewhere := e.t.TempDir()
	c := exec.Command(h, args...)
	c.Env = append(append([]string{}, e.env...),
		"NAUT_ROOT="+elsewhere, "NAUT_STATE="+elsewhere, "NAUT_UNITDIR="+elsewhere,
		"NAUT_SUPERVISOR=cron", "NAUT_UNITSRC="+elsewhere+"/u", "NAUT_HELPER_VERSION=forged")
	c.Stdin = bytes.NewReader(stdin)
	out, err := c.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return string(out), 0
}

func TestHelperScriptIsBoxShWithAPinningPrologue(t *testing.T) {
	s, err := helperScript(defaultHelperPaths)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(s, deployBoxSh) {
		t.Fatal("the helper does not end with box.sh unchanged")
	}
	prologue := strings.TrimSuffix(s, deployBoxSh)
	for _, want := range []string{"#!/bin/sh\n", "\nNAUT_HELPER=1\n", "\nNAUT_HELPER_VERSION=" + helperVersion() + "\n",
		"\nNAUT_SUPERVISOR=systemd\n", "\nNAUT_ROOT=/opt/nautilus\n", "\nNAUT_STATE=/var/lib/nautilus\n",
		"\nNAUT_UNITDIR=/etc/systemd/system\n", "\nNAUT_UNITSRC=" + helperUnitPath + "\n",
		"\nPATH=/usr/sbin:/usr/bin:/sbin:/bin\n", "\nLC_ALL=C\n", "\nunset TMPDIR\n", "\numask 022\n"} {
		if !strings.Contains(prologue, want) {
			t.Errorf("the prologue does not pin %q", strings.TrimSpace(want))
		}
	}
	if deployRoot != defaultHelperPaths.root {
		t.Errorf("the helper's root %s is not naut deploy's %s", defaultHelperPaths.root, deployRoot)
	}
	if _, err := helperScript(helperPaths{root: "/opt/x; rm -rf /", state: "/s", unitDir: "/u", unitSrc: "/t", path: "/bin"}); err == nil {
		t.Error("a path with shell syntax was written into the helper")
	}
}

func TestDeployHelperRefusesWhatItIsNotFor(t *testing.T) {
	e := newBoxEnv(t)
	h := e.helperFile()
	for _, args := range [][]string{
		{"install", "../evil"}, {"install", ".hidden"}, {"install", "-rf"}, {"install", "a/b"},
		{"install", "x.part"}, {"install", "nautilus.service"}, {"update", "a", "b"}, {"update"},
		{"receive", "nautilus.service"}, {"receive", "../../etc/x"}, {"receive", "a b"},
		{"uninstall", "-rf"}, {"status", "extra"}, {"frobnicate"}, {"restore", "x"},
	} {
		out, code := e.helper(h, []byte("payload"), args...)
		if code == 0 || !strings.Contains(out, "refused") {
			t.Errorf("helper %v: exit %d, want refused:\n%s", args, code, out)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(e.root, "incoming")); len(entries) > 0 {
		t.Errorf("a refused receive left files in incoming/: %v", entries)
	}
	if out, _ := e.helper(h, nil, "helper-version"); strings.TrimSpace(out) != helperVersion() {
		t.Errorf("helper-version %q, want %s (an environment value must not count)", out, helperVersion())
	}
}

// A release through the helper: the upload by receive, the unit from the
// root-owned template whatever was uploaded, and status that does not run
// the uploaded controller as root.
func TestDeployHelperInstallsWithItsOwnUnitAndNeverRunsTheController(t *testing.T) {
	e := newBoxEnv(t)
	h := e.helperFile()
	marker := filepath.Join(t.TempDir(), "ran-as-root")
	ctl := "#!/bin/sh\n[ \"$1\" = version ] && touch " + marker + "\nexec sleep 300\n"
	if out, code := e.helper(h, []byte(ctl), "receive", "one"); code != 0 {
		t.Fatalf("receive: %d\n%s", code, out)
	}
	// An uploaded unit cannot be received; one planted in incoming/ anyway
	// must not be used.
	os.WriteFile(filepath.Join(e.root, "incoming", "nautilus.service"), []byte("[Service]\nUser=root\nExecStart=/bin/sh\n"), 0o644)
	if out, code := e.helper(h, nil, "install", "one"); code != 0 || !e.running() {
		t.Fatalf("install: %d, running %v\n%s", code, e.running(), out)
	}
	unit, _ := os.ReadFile(filepath.Join(e.units, "nautilus.service"))
	if !strings.HasPrefix(string(unit), "# TEMPLATE-UNIT\n") || strings.Contains(string(unit), "User=root") {
		t.Errorf("the installed unit is not the template:\n%s", unit)
	}
	if out, code := e.helper(h, nil, "status"); code != 0 || !strings.Contains(out, "current:  one") {
		t.Errorf("status: %d\n%s", code, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("status ran the uploaded controller (as root, on a real box)")
	}
}

// A controller that leaves links in STATE: retain.json to a file outside,
// and backups to a directory outside. backup must not read through them;
// restore must not write or chown through them. Every mode, not only the
// helper: a scoped user's controller is anyone's controller.
func TestDeployBoxNeverFollowsLinksInState(t *testing.T) {
	e := newBoxEnv(t)
	can := canary(t)
	if out, code := e.deploy("install", "good", e.good); code != 0 {
		t.Fatalf("install: %d\n%s", code, out)
	}
	os.Remove(filepath.Join(e.state, "retain.json"))
	if err := os.Symlink(can, filepath.Join(e.state, "retain.json")); err != nil {
		t.Fatal(err)
	}
	out, code := e.box("backup")
	if code == 0 || strings.Contains(out, "CANARY") {
		t.Errorf("backup through a retain.json link: exit %d, output carries the canary: %v\n%s", code, strings.Contains(out, "CANARY"), out)
	}
	// restore over the link: the link is replaced, the canary untouched.
	os.MkdirAll(filepath.Join(e.root, "incoming"), 0o755)
	os.WriteFile(filepath.Join(e.root, "incoming", "restore.tgz"), backupTGZ(t, `{"SP":7}`, "good", false), 0o644)
	if out, code := e.box("restore"); code != 0 {
		t.Fatalf("restore: %d\n%s", code, out)
	}
	canaryIntact(t, can)
	if fi, err := os.Lstat(filepath.Join(e.state, "retain.json")); err != nil || fi.Mode()&os.ModeSymlink != 0 || e.retain() != `{"SP":7}` {
		t.Errorf("after restore, retain.json is %q (link %v)", e.retain(), err == nil && fi.Mode()&os.ModeSymlink != 0)
	}
	// backups as a link to a directory outside: an update refuses it.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(e.state, "backups")); err != nil {
		t.Fatal(err)
	}
	if out, code := e.deploy("update", "two", e.good2); code == 0 || !strings.Contains(out, "backups is not a directory") {
		t.Errorf("update with backups a link: exit %d\n%s", code, out)
	}
	if entries, _ := os.ReadDir(outside); len(entries) > 0 {
		t.Errorf("a snapshot was written through the backups link: %v", entries)
	}
}

// A failed release that, before it dies, points retain.json and its own
// snapshot at a file outside: the rollback puts the values back from
// root's copy, through neither link.
func TestDeployBoxFailedUpdateRestoresThroughNoLink(t *testing.T) {
	e := newBoxEnv(t)
	can := canary(t)
	saver := "#!/bin/sh\necho '{\"SP\":1}' >retain.json\nexec sleep 300\n"
	if out, code := e.deploy("install", "good", saver); code != 0 {
		t.Fatalf("install: %d\n%s", code, out)
	}
	time.Sleep(300 * time.Millisecond)
	evil := "#!/bin/sh\nrm -f retain.json; ln -s " + can + " retain.json\n" +
		"for f in backups/*.json; do rm -f \"$f\"; ln -s " + can + " \"$f\"; done\nexit 3\n"
	out, code := e.deploy("update", "evil", evil)
	if code != 3 {
		t.Fatalf("update: exit %d, want 3 (rolled back)\n%s", code, out)
	}
	canaryIntact(t, can)
	if got := strings.TrimSpace(e.retain()); got != `{"SP":1}` {
		t.Errorf("retained values after the rollback: %q, want the snapshot's", got)
	}
}

// restore reads the archive's two members as data: a retain.json that is a
// link in the archive, or a release name outside the id characters, never
// becomes a path.
func TestDeployBoxRestoreTakesTheArchiveAsData(t *testing.T) {
	e := newBoxEnv(t)
	can := canary(t)
	if out, code := e.deploy("install", "good", e.good); code != 0 {
		t.Fatalf("install: %d\n%s", code, out)
	}
	os.MkdirAll(filepath.Join(e.root, "incoming"), 0o755)
	os.WriteFile(filepath.Join(e.root, "incoming", "restore.tgz"), backupTGZ(t, can, "good", true), 0o644)
	if out, code := e.box("restore"); code == 0 || strings.Contains(out, "CANARY") {
		t.Errorf("a retain.json link in the archive: exit %d\n%s", code, out)
	}
	canaryIntact(t, can)
	os.WriteFile(filepath.Join(e.root, "incoming", "restore.tgz"), backupTGZ(t, `{"SP":2}`, "$(touch /tmp/x)/../x", false), 0o644)
	out, code := e.box("restore")
	if code != 0 || !strings.Contains(out, "taken on release ?") {
		t.Errorf("a release name outside the id characters: exit %d, want it shown as ?\n%s", code, out)
	}
}

// backupTGZ is a backup as naut deploy backup writes it. With link, its
// retain.json is a symlink to retain (a path) instead of a file.
func backupTGZ(t *testing.T, retain, release string, link bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if link {
		tw.WriteHeader(&tar.Header{Name: "retain.json", Typeflag: tar.TypeSymlink, Linkname: retain, Mode: 0o777})
	} else {
		tw.WriteHeader(&tar.Header{Name: "retain.json", Mode: 0o644, Size: int64(len(retain))})
		tw.Write([]byte(retain))
	}
	tw.WriteHeader(&tar.Header{Name: "release", Mode: 0o644, Size: int64(len(release))})
	tw.Write([]byte(release))
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// naut deploy's side: on a box with the helper it uploads and runs through
// `sudo -n <helper>`, never `sh -s`, and sends no unit; a helper of
// another version stops it before anything is uploaded.
func TestDeployUsesTheHelper(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		wantCode      int
	}{
		{"same version", helperVersion(), 0},
		{"stale helper", "0123456789abcdef", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := fakeHelperSSH(t, tc.version)
			ctl := filepath.Join(t.TempDir(), "ctl")
			os.WriteFile(ctl, []byte("#!/bin/sh\n"), 0o755)
			code := runDeploy([]string{"update", "-controller", ctl, "-id", "rel1", "user@epic"})
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d", code, tc.wantCode)
			}
			calls := sshCalls(t, log)
			var cmds []string
			for _, c := range calls {
				cmds = append(cmds, c.cmd)
				if strings.Contains(c.cmd, "sh -s") || strings.Contains(c.cmd, "sh -c") {
					t.Errorf("a root shell on a helper box: %s", c.cmd)
				}
				if strings.Contains(c.cmd, "nautilus.service") {
					t.Errorf("a unit sent to a helper box: %s", c.cmd)
				}
			}
			if tc.wantCode != 0 {
				for _, c := range cmds {
					if strings.Contains(c, " receive ") || strings.Contains(c, " update ") {
						t.Errorf("a stale helper still got %q", c)
					}
				}
				return
			}
			want := []string{"sudo -n " + helperPath + " receive rel1", "sudo -n " + helperPath + " update rel1"}
			got := cmds[len(cmds)-2:]
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("last calls %q, want %q", got, want)
			}
			if calls[len(calls)-2].in != "#!/bin/sh\n" {
				t.Errorf("receive's stdin %q, want the controller", calls[len(calls)-2].in)
			}
		})
	}
}

// fakeHelperSSH is fakeSSH for a box whose user is not root and has the
// helper: the probe reports it, helper-version answers version.
func fakeHelperSSH(t *testing.T, version string) string {
	t.Helper()
	log := fakeSSH(t, "armv7l", 0)
	bin := filepath.Dir(strings.SplitN(os.Getenv("PATH"), string(os.PathListSeparator), 2)[0] + "/ssh")
	script := `#!/bin/sh
for a; do last=$a; done
case " $* " in *" -O exit "*) exit 0 ;; esac
n=$(ls "$FAKE_SSH_LOG" | grep -c '\.cmd$')
printf '%s' "$last" >"$FAKE_SSH_LOG/$n.cmd"
cat >"$FAKE_SSH_LOG/$n.in"
case $last in
"uname -sm") echo "Linux armv7l" ;;
*"helper-version") echo "$FAKE_HELPER_VERSION" ;;
*naut-deploy*) printf '\nhelper=1\n' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_HELPER_VERSION", version)
	return log
}

// A file in STATE that is not the controller's own, as a hard link to a
// root file would be (one the controller could make to a file it cannot
// read), is not read by root. A test cannot make a root-owned file, so a
// stand-in stat reports root as the owner of any file holding ROOTFILE.
func TestDeployBoxReadsOnlyTheControllersOwnFiles(t *testing.T) {
	e := newBoxEnv(t)
	real, err := exec.LookPath("stat")
	if err != nil {
		t.Skip("no stat")
	}
	fake := "#!/bin/sh\nfor a; do f=$a; done\n" +
		"if [ \"$1\" = -c ] && [ \"$2\" = %u ] && [ -f \"$f\" ] && grep -q ROOTFILE \"$f\" 2>/dev/null; then echo 0; exit 0; fi\n" +
		"exec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(e.bin, "stat"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, code := e.deploy("install", "good", e.good); code != 0 {
		t.Fatalf("install: %d\n%s", code, out)
	}
	os.WriteFile(filepath.Join(e.state, "retain.json"), []byte("ROOTFILE-SECRET"), 0o600)
	out, code := e.box("backup")
	if code == 0 || strings.Contains(out, "ROOTFILE") {
		t.Errorf("backup of a file that is not the controller's own: exit %d\n%s", code, out)
	}
}

// The copy of the retained values root keeps across an update is root's
// own and private: the controller's owner and mode are only for the check.
func TestDeployBoxRootsSnapshotIsPrivate(t *testing.T) {
	e := newBoxEnv(t)
	saver := "#!/bin/sh\necho '{\"SP\":1}' >retain.json\nchmod 644 retain.json\nexec sleep 300\n"
	if out, code := e.deploy("install", "one", saver); code != 0 {
		t.Fatalf("install: %d\n%s", code, out)
	}
	time.Sleep(300 * time.Millisecond)
	if out, code := e.deploy("update", "two", e.good2); code != 0 {
		t.Fatalf("update: %d\n%s", code, out)
	}
	fi, err := os.Stat(filepath.Join(e.root, "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m := fi.Mode().Perm(); m != 0o600 {
		t.Errorf("ROOT/snapshot.json mode %o, want 600", m)
	}
}

// uninstall -purge with STATE given as a link (DynamicUser's
// /var/lib/nautilus -> private/nautilus) removes the directory and the
// link, leaving nothing that points at nothing.
func TestDeployBoxPurgeRemovesAStateLink(t *testing.T) {
	e := newBoxEnv(t)
	real := filepath.Join(filepath.Dir(e.state), "private-state")
	os.RemoveAll(e.state)
	os.MkdirAll(real, 0o750)
	if err := os.Symlink(real, e.state); err != nil {
		t.Fatal(err)
	}
	if out, code := e.deploy("install", "one", e.good); code != 0 {
		t.Fatalf("install: %d\n%s", code, out)
	}
	os.WriteFile(filepath.Join(real, "retain.json"), []byte(`{"SP":1}`), 0o600)
	if out, code := e.box("uninstall", "-purge"); code != 0 {
		t.Fatalf("uninstall -purge: %d\n%s", code, out)
	}
	if _, err := os.Lstat(e.state); err == nil {
		t.Error("the STATE link is still there (pointing at nothing)")
	}
	if _, err := os.Stat(real); err == nil {
		t.Error("the state directory is still there")
	}
}
