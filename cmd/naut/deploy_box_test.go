package main

import (
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// box.sh's systemd mode, run for real against a stand-in systemctl, so the
// failure paths #764's review found are tested in CI and not only by
// tools/deploy/check.sh on a bench box: a failed install, a redeploy of the
// current release while it is down, and the retained values a failed
// update saved over.

// fakeSystemctl runs current/controller as a plain child, like a
// Type=simple unit: restart starts it (and fails if it is gone half a
// second later), is-active and MainPID report it, stop kills it.
const fakeSystemctl = `#!/bin/sh
pidf=$FAKE_SD/pid
alive() { [ -f "$pidf" ] && kill -0 "$(cat "$pidf")" 2>/dev/null; }
case $1 in
is-active) alive ;;
show) if alive; then cat "$pidf"; else echo 0; fi ;;
restart)
	if alive; then kill "$(cat "$pidf")"; sleep 0.2; fi
	(cd "$NAUT_STATE" && exec "$NAUT_ROOT/current/controller" >>"$FAKE_SD/log" 2>&1) &
	echo $! >"$pidf"
	sleep 0.5
	alive ;;
stop) if alive; then kill "$(cat "$pidf")"; sleep 0.2; fi; rm -f "$pidf" ;;
*) echo "$*" >>"$FAKE_SD/calls" ;;
esac
`

type boxEnv struct {
	t                *testing.T
	root, state, sd  string
	bin, units       string
	env              []string
	good, good2, bad string
}

func newBoxEnv(t *testing.T) *boxEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("runs box.sh with its 3 s start checks")
	}
	if goruntime.GOOS != "linux" {
		t.Skip("box.sh needs a Linux shell and coreutils")
	}
	dir := t.TempDir()
	e := &boxEnv{t: t, root: filepath.Join(dir, "opt"), state: filepath.Join(dir, "state"), sd: filepath.Join(dir, "sd")}
	bin := filepath.Join(dir, "bin")
	units := filepath.Join(dir, "units")
	e.bin, e.units = bin, units
	for _, d := range []string{e.root, e.state, e.sd, bin, units} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(bin, "systemctl"), []byte(fakeSystemctl), 0o755)
	os.WriteFile(filepath.Join(bin, "journalctl"), []byte("#!/bin/sh\ncat \"$FAKE_SD/log\" 2>/dev/null\n"), 0o755)
	os.WriteFile(filepath.Join(e.root, "nautilus.service.in"), deployUnit, 0o644)
	e.env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"NAUT_ROOT="+e.root, "NAUT_STATE="+e.state, "NAUT_UNITDIR="+units, "FAKE_SD="+e.sd)
	// Controllers: two that run, and one that saves over the retained
	// values (it runs in their directory) and dies, as a release that
	// fails after its first save would.
	e.good = "#!/bin/sh\nexec sleep 300\n"
	e.good2 = "#!/bin/sh\n# release 2\nexec sleep 300\n"
	e.bad = "#!/bin/sh\necho broken >retain.json\nexit 3\n"
	t.Cleanup(e.stop)
	return e
}

// deploy uploads controller (if any) as id and runs box.sh <cmd> <id>.
func (e *boxEnv) deploy(cmd, id, controller string) (string, int) {
	if controller != "" {
		os.MkdirAll(filepath.Join(e.root, "incoming"), 0o755)
		os.WriteFile(filepath.Join(e.root, "incoming", id), []byte(controller), 0o755)
	}
	return e.box(cmd, id)
}

// stop takes the controller down, as a crash or a person would.
func (e *boxEnv) stop() {
	c := exec.Command("sh", "-c", "systemctl stop nautilus.service")
	c.Env = e.env
	c.Run()
}

func (e *boxEnv) box(args ...string) (string, int) {
	c := exec.Command("sh", append([]string{"-s", "--"}, args...)...)
	c.Env = e.env
	c.Stdin = strings.NewReader(deployBoxSh)
	out, err := c.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return string(out), 0
}

func (e *boxEnv) link(name string) string {
	l, err := os.Readlink(filepath.Join(e.root, name))
	if err != nil {
		return ""
	}
	return filepath.Base(l)
}

func (e *boxEnv) running() bool {
	b, err := os.ReadFile(filepath.Join(e.sd, "pid"))
	if err != nil {
		return false
	}
	var pid int
	for _, c := range strings.TrimSpace(string(b)) {
		pid = pid*10 + int(c-'0')
	}
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func (e *boxEnv) retain() string {
	b, err := os.ReadFile(filepath.Join(e.state, "retain.json"))
	if err != nil {
		return "<none>"
	}
	return string(b)
}

// A release that does not start at install leaves nothing installed, and
// the retained values as it found them, so install can simply run again.
func TestDeployBoxFailedInstallLeavesNothing(t *testing.T) {
	e := newBoxEnv(t)
	out, code := e.deploy("install", "bad", e.bad)
	if code != 1 {
		t.Fatalf("install of a release that dies: exit %d, want 1\n%s", code, out)
	}
	if l := e.link("current"); l != "" {
		t.Errorf("a failed install left current -> %s; a retried install would be refused", l)
	}
	if r := e.retain(); r != "<none>" {
		t.Errorf("a failed install left the retained values it saved: %q", r)
	}
	if out, code := e.deploy("install", "good", e.good); code != 0 || !e.running() {
		t.Fatalf("install after a failed install: exit %d, running %v\n%s", code, e.running(), out)
	}
}

// An update whose release dies puts back the retained values it saved
// over, along with the release before it (exit 3). The values put back are
// the old release's last save, made on its way out: the snapshot is taken
// after it stopped, not before.
func TestDeployBoxFailedUpdateRestoresRetain(t *testing.T) {
	e := newBoxEnv(t)
	const kept = `{"tags":{"SP":42.5}}`
	// Like a controller, it saves once more when stopped.
	saver := "#!/bin/sh\nv='" + kept + "'\ntrap 'echo \"$v\" >retain.json; kill $!; exit 0' TERM\nsleep 300 &\nwait\n"
	if out, code := e.deploy("install", "good", saver); code != 0 {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	os.WriteFile(filepath.Join(e.state, "retain.json"), []byte(`{"tags":{"SP":1.5}}`), 0o644)
	out, code := e.deploy("update", "bad", e.bad)
	if code != 3 {
		t.Fatalf("update to a release that dies: exit %d, want 3\n%s", code, out)
	}
	if e.link("current") != "good" || !e.running() {
		t.Errorf("after the rollback: current %q, running %v; want good, running", e.link("current"), e.running())
	}
	if r := strings.TrimSpace(e.retain()); r != kept {
		t.Errorf("retained values after the rollback: %q, want %q, the old release's last save", r, kept)
	}
}

// Deploying the release that is already current is a no-op only while it
// runs: down, it is started; unable to start, the release before it comes
// back and the update exits 3. A retried CD job must not pass with the
// controller stopped.
func TestDeployBoxRedeployWhileDown(t *testing.T) {
	e := newBoxEnv(t)
	if out, code := e.deploy("install", "one", e.good); code != 0 {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	if out, code := e.deploy("update", "two", e.good2); code != 0 {
		t.Fatalf("update: exit %d\n%s", code, out)
	}
	e.stop() // the controller goes down
	start := time.Now()
	out, code := e.deploy("update", "two", "")
	if code != 0 || !e.running() {
		t.Fatalf("redeploy of the current release while down: exit %d, running %v; want 0, running\n%s", code, e.running(), out)
	}
	if !strings.Contains(out, "not running; starting it") {
		t.Errorf("it should say it started the release:\n%s", out)
	}
	t.Logf("started in %s", time.Since(start).Round(time.Millisecond))

	// Now release two cannot start at all.
	e.stop()
	os.WriteFile(filepath.Join(e.root, "releases", "two", "controller"), []byte(e.bad), 0o755)
	out, code = e.deploy("update", "two", "")
	if code != 3 {
		t.Fatalf("redeploy of a current release that cannot start: exit %d, want 3\n%s", code, out)
	}
	if e.link("current") != "one" || !e.running() {
		t.Errorf("after it: current %q, running %v; want one, running", e.link("current"), e.running())
	}
}

// prepRestore installs a release, puts OLD in retain.json and uploads a
// backup whose retain.json is NEW, ready for box.sh restore.
func prepRestore(t *testing.T, e *boxEnv) {
	t.Helper()
	if out, code := e.deploy("install", "good", e.good); code != 0 {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	os.WriteFile(filepath.Join(e.state, "retain.json"), []byte("OLD"), 0o644)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "retain.json"), []byte("NEW"), 0o644)
	os.WriteFile(filepath.Join(src, "release"), []byte("good"), 0o644)
	tgz := filepath.Join(e.root, "incoming", "restore.tgz")
	os.MkdirAll(filepath.Dir(tgz), 0o755)
	if out, err := exec.Command("tar", "-C", src, "-czf", tgz, "retain.json", "release").CombinedOutput(); err != nil {
		t.Fatalf("tar: %v\n%s", err, out)
	}
}

// standIn puts a script named name first on box.sh's PATH. real is the
// absolute path of the real command, for the script to hand on to.
func standIn(t *testing.T, e *boxEnv, name, script string) {
	t.Helper()
	real, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("no %s", name)
	}
	body := strings.ReplaceAll(script, "REAL", real)
	if err := os.WriteFile(filepath.Join(e.bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A restore writes retain.json whole under another name, syncs, renames
// it over the old one and syncs again (#842): at no point is there no
// retain.json, or half of one, for a power cut to leave behind. A stand-in
// sync records what retain.json holds each time: the old values at the
// first sync, the new ones at the second. A stand-in rm refuses to remove
// retain.json, so an order that removes it before the rename (sync, rm,
// mv) fails here too.
func TestDeployBoxRestoreNeverLeavesNoRetainFile(t *testing.T) {
	e := newBoxEnv(t)
	prepRestore(t, e)
	syncs := filepath.Join(e.sd, "syncs")
	os.WriteFile(filepath.Join(e.bin, "sync"), []byte("#!/bin/sh\n{ printf 'retain.json='; cat \"$NAUT_STATE/retain.json\" 2>/dev/null || printf MISSING; echo; } >>\"$FAKE_SD/syncs\"\n"), 0o755)
	standIn(t, e, "rm", `for a; do case $a in */retain.json) echo "rm of retain.json: $*" >>"$FAKE_SD/rm"; exit 1 ;; esac; done
exec REAL "$@"
`)
	if out, code := e.box("restore"); code != 0 {
		t.Fatalf("restore: exit %d\n%s", code, out)
	}
	log, _ := os.ReadFile(syncs)
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) < 2 || lines[0] != "retain.json=OLD" || lines[1] != "retain.json=NEW" {
		t.Errorf("retain.json at each sync during restore: %q, want OLD then NEW", lines)
	}
	if strings.Contains(string(log), "MISSING") {
		t.Errorf("there was a moment with no retain.json: %q", lines)
	}
	if rms, _ := os.ReadFile(filepath.Join(e.sd, "rm")); len(rms) != 0 {
		t.Errorf("restore removed retain.json before the new one took its place: %s", rms)
	}
	if b, _ := os.ReadFile(filepath.Join(e.state, "retain.json")); string(b) != "NEW" {
		t.Errorf("retain.json after restore = %q, want NEW", b)
	}
	if left, _ := filepath.Glob(filepath.Join(e.state, ".naut-new.*")); len(left) != 0 {
		t.Errorf("restore left its temp file behind: %q", left)
	}
}

// With an mv that has no -T (or refuses it), statewrite falls back to
// remove-then-rename, which is not atomic, and says so; the restore still
// lands.
func TestDeployBoxStatewriteFallbackIsLogged(t *testing.T) {
	e := newBoxEnv(t)
	prepRestore(t, e)
	standIn(t, e, "mv", `for a; do case $a in -*T*) exit 1 ;; esac; done
exec REAL "$@"
`)
	out, code := e.box("restore")
	if code != 0 {
		t.Fatalf("restore: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "mv -T failed") {
		t.Errorf("the non-atomic fallback ran without a word:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(e.state, "retain.json")); string(b) != "NEW" {
		t.Errorf("retain.json after the fallback = %q, want NEW", b)
	}
}

// A temp file a power cut left behind (older than 10 minutes) is cleared by
// the next write; a fresh one, which may be a concurrent run's, is not.
func TestDeployBoxStatewriteClearsStaleTempFiles(t *testing.T) {
	e := newBoxEnv(t)
	prepRestore(t, e)
	stale := filepath.Join(e.state, ".naut-new.11111")
	fresh := filepath.Join(e.state, ".naut-new.22222")
	os.WriteFile(stale, []byte("x"), 0o644)
	os.WriteFile(fresh, []byte("y"), 0o644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(stale, old, old)
	if out, code := e.box("restore"); code != 0 {
		t.Fatalf("restore: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("the stale temp file is still there")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a fresh temp file (maybe a concurrent run's) was removed")
	}
}
