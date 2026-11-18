package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// install -supervisor cron (#768): nothing runs under sudo, the controller
// goes to <root>/incoming, no unit is uploaded, and box.sh is told the mode
// and root in its environment.
func TestDeployCronInstallRunsAsTheUser(t *testing.T) {
	log := fakeSSH(t, goMachine(), 0)
	if code := runDeploy([]string{"install", "admin@plc", "-supervisor", "cron", "-root", "/opt/apps/nautilus", deployProject(t)}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	calls := sshCalls(t, log)
	if len(calls) != 4 || !strings.Contains(calls[0].cmd, ".config/naut-deploy") || calls[1].cmd != "uname -sm" {
		t.Fatalf("want 4 ssh calls (mode, uname, controller, install), got %d: %+v", len(calls), calls)
	}
	calls = calls[2:]
	for _, c := range calls {
		if strings.Contains(c.cmd, "sudo") {
			t.Errorf("cron mode must not use sudo: %q", c.cmd)
		}
	}
	if !strings.Contains(calls[0].cmd, "/opt/apps/nautilus/incoming/plant-") {
		t.Errorf("the controller goes to <root>/incoming: %q", calls[0].cmd)
	}
	if !strings.Contains(calls[1].cmd, "NAUT_SUPERVISOR=cron NAUT_ROOT=/opt/apps/nautilus sh -s -- install plant-") {
		t.Errorf("box.sh runs install in cron mode: %q", calls[1].cmd)
	}
}

// Later commands take the mode from ~/.config/naut-deploy on the box.
func TestDeployCronModeIsDiscovered(t *testing.T) {
	log := fakeSSH(t, goMachine(), 0)
	t.Setenv("FAKE_MODE", `supervisor=cron\nroot=/opt/apps/nautilus\n`)
	if code := runDeploy([]string{"rollback", "admin@plc"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	calls := sshCalls(t, log)
	if len(calls) != 2 || !strings.Contains(calls[0].cmd, ".config/naut-deploy") {
		t.Fatalf("want the mode question then rollback, got %+v", calls)
	}
	if calls[1].cmd != "NAUT_SUPERVISOR=cron NAUT_ROOT=/opt/apps/nautilus sh -s -- rollback" {
		t.Errorf("rollback in cron mode, as the user: %q", calls[1].cmd)
	}

	// A mode file that names a root that could not have been installed is
	// refused rather than pasted into a shell command.
	t.Setenv("FAKE_MODE", `supervisor=cron\nroot=/opt/x;reboot\n`)
	if code := runDeploy([]string{"status", "admin@plc"}); code == 0 {
		t.Error("a root with ; in it must be refused")
	}
}

// install with a mode that disagrees with the one the box remembers is
// refused before anything is uploaded: a systemd install beside a cron one
// would run two controllers, and a second cron root would orphan the first.
func TestDeployInstallRefusesAnotherMode(t *testing.T) {
	for _, c := range []struct {
		args []string
		ok   bool
	}{
		{[]string{"-supervisor", "systemd"}, false},
		{[]string{"-supervisor", "cron", "-root", "/opt/b"}, false},
		{[]string{"-supervisor", "cron", "-root", "/opt/a/"}, true}, // the same root
	} {
		log := fakeSSH(t, goMachine(), 0)
		t.Setenv("FAKE_MODE", `supervisor=cron\nroot=/opt/a\n`)
		code := runDeploy(append([]string{"install", "admin@plc"}, append(c.args, deployProject(t))...))
		uploads := 0
		for _, call := range sshCalls(t, log) {
			if strings.Contains(call.cmd, "/incoming/") {
				uploads++
			}
		}
		if c.ok && code != 0 {
			t.Errorf("%q on the remembered mode: exit %d", c.args, code)
		}
		if !c.ok && (code != 2 || uploads != 0) {
			t.Errorf("%q on a box in cron mode under /opt/a: exit %d, %d uploads; want 2 and none", c.args, code, uploads)
		}
	}
}

func TestDeploySupervisorFlags(t *testing.T) {
	for _, c := range []struct {
		sup, root string
		ok        bool
	}{
		{"", "", true},
		{"systemd", "", true},
		{"cron", "/opt/plcnext/apps/nautilus", true},
		{"cron", "/opt/plcnext/apps/nautilus/", true},
		{"", "/opt/x", false},          // -root without cron
		{"cron", "", false},            // cron without -root
		{"cron", "opt/x", false},       // relative
		{"cron", "/", false},           // the whole box
		{"cron", "/opt/a b", false},    // would split in the shell
		{"cron", "/opt/x'y", false},    // would end a quote
		{"cron", "/opt/../etc", false}, // Clean leaves /etc: refused for the ..
		{"openrc", "/opt/x", false},
	} {
		err := checkSupervisor(c.sup, c.root)
		if (err == nil) != c.ok {
			t.Errorf("-supervisor %q -root %q: err %v, want ok=%v", c.sup, c.root, err, c.ok)
		}
	}
}

// box.sh's cron mode on this machine, end to end: install, update, an
// update that does not start (rolled back, exit 3), rollback, the hold the
// keepalive respects, status, uninstall. crontab is a fake on PATH; the
// controller is a small Go program that makes itself non-dumpable, as a
// controller started through naut-launch is, so box.sh must know it without
// reading /proc/<pid>/exe.
func TestDeployBoxCronMode(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("box.sh's cron mode reads /proc")
	}
	if testing.Short() {
		t.Skip("starts and stops controllers, ~30 s")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on PATH to build the stand-in controller")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "go.mod"), []byte("module sleeper\n"), 0o644)
	os.WriteFile(filepath.Join(src, "main.go"), []byte(`package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	// Not dumpable, like a controller naut-launch gave a capability: its
	// /proc/<pid>/exe is unreadable, even to its owner.
	syscall.RawSyscall(syscall.SYS_PRCTL, 4 /* PR_SET_DUMPABLE */, 0, 0)
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("sleeper")
		return
	}
	wd, _ := os.Getwd()
	holds, _ := filepath.Glob("../hold.*") // run in state/, under the root
	fmt.Println("sleeper up in", wd, "X="+os.Getenv("X"), "holds="+fmt.Sprint(len(holds)))
	time.Sleep(time.Hour)
}
`), 0o644)
	sleeper := filepath.Join(tmp, "sleeper")
	build := exec.Command(goBin, "build", "-o", sleeper, ".")
	build.Dir = src
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the stand-in controller: %v\n%s", err, out)
	}

	root := filepath.Join(tmp, "root")
	home := filepath.Join(tmp, "home")
	bin := filepath.Join(tmp, "bin")
	tab := filepath.Join(tmp, "crontab")
	for _, d := range []string{root, home, bin} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(bin, "crontab"), []byte(`#!/bin/sh
case $1 in
-l) [ -f "$FAKE_CRONTAB" ] && cat "$FAKE_CRONTAB" ;;
-) cat >"$FAKE_CRONTAB" ;;
*) exit 2 ;;
esac
`), 0o755)
	os.WriteFile(filepath.Join(root, "env"), []byte("X=from-env\n"), 0o644)

	box := func(args ...string) (string, int) {
		t.Helper()
		c := exec.Command("sh", append([]string{"-s", "--"}, args...)...)
		c.Stdin = strings.NewReader(deployBoxSh)
		c.Env = append(os.Environ(), "NAUT_SUPERVISOR=cron", "NAUT_ROOT="+root, "HOME="+home,
			"FAKE_CRONTAB="+tab, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		var out bytes.Buffer
		c.Stdout, c.Stderr = &out, &out
		err := c.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return out.String(), code
	}
	incoming := func(id string, body []byte) {
		t.Helper()
		os.MkdirAll(filepath.Join(root, "incoming"), 0o755)
		if err := os.WriteFile(filepath.Join(root, "incoming", id), body, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pid := func() int {
		b, _ := os.ReadFile(filepath.Join(root, "state", "nautilus.pid"))
		p, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return p
	}
	alive := func(p int) bool { return p > 0 && syscall.Kill(p, 0) == nil }
	link := func(name string) string {
		l, _ := os.Readlink(filepath.Join(root, name))
		return filepath.Base(l)
	}
	t.Cleanup(func() { // nothing left running, whatever failed
		if p := pid(); alive(p) {
			syscall.Kill(p, syscall.SIGKILL)
		}
	})
	sleeperBytes, _ := os.ReadFile(sleeper)

	// install
	incoming("r1", sleeperBytes)
	if out, code := box("install", "r1"); code != 0 {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	p1 := pid()
	if !alive(p1) || link("current") != "r1" {
		t.Fatalf("after install r1 should run, pid %d current %q", p1, link("current"))
	}
	if _, err := os.Readlink("/proc/" + strconv.Itoa(p1) + "/exe"); err == nil {
		t.Fatal("the stand-in controller should not be dumpable; the test proves nothing otherwise")
	}
	if cron, _ := os.ReadFile(tab); strings.Count(string(cron), "/keepalive.sh # naut deploy") != 2 {
		t.Errorf("crontab should start the keepalive at boot and every minute:\n%s", cron)
	}
	if mode, _ := os.ReadFile(filepath.Join(home, ".config", "naut-deploy")); string(mode) != "supervisor=cron\nroot="+root+"\n" {
		t.Errorf("~/.config/naut-deploy: %q", mode)
	}
	if h, _ := filepath.Glob(filepath.Join(root, "hold.*")); len(h) != 0 {
		t.Errorf("the hold must go when box.sh ends: %q", h)
	}
	logText := func() string { b, _ := os.ReadFile(filepath.Join(root, "state", "nautilus.log")); return string(b) }
	// box.sh starts the release while it still holds, so a keepalive tick
	// in that moment cannot start a second one.
	if !strings.Contains(logText(), "sleeper up in "+filepath.Join(root, "state")+" X=from-env holds=1") {
		t.Errorf("the controller runs in state/ with env's variables, started under box.sh's hold; log:\n%s", logText())
	}

	// update
	incoming("r2", sleeperBytes)
	if out, code := box("update", "r2"); code != 0 {
		t.Fatalf("update: exit %d\n%s", code, out)
	}
	p2 := pid()
	if alive(p1) || !alive(p2) || p2 == p1 || link("current") != "r2" || link("previous") != "r1" {
		t.Fatalf("after update r2 should run alone, previous r1: old alive %v, new %d, current %q, previous %q", alive(p1), p2, link("current"), link("previous"))
	}

	// an update that does not start: back to r2, exit 3
	incoming("bad", []byte("#!/bin/sh\necho bad release; exit 1\n"))
	if out, code := box("update", "bad"); code != 3 {
		t.Fatalf("a release that exits must roll back with exit 3, got %d\n%s", code, out)
	}
	if p := pid(); !alive(p) || link("current") != "r2" || link("previous") != "r1" {
		t.Fatalf("after the failed update r2 should run, current r2, previous r1: alive %v, %q %q", alive(p), link("current"), link("previous"))
	}

	// rollback
	if out, code := box("rollback"); code != 0 {
		t.Fatalf("rollback: exit %d\n%s", code, out)
	}
	if p := pid(); !alive(p) || link("current") != "r1" || link("previous") != "r2" {
		t.Fatalf("after rollback r1 should run, previous r2: alive %v, %q %q", alive(p), link("current"), link("previous"))
	}

	// the keepalive: restarts a dead controller, but not under a hold
	keep := filepath.Join(root, "keepalive.sh")
	p := pid()
	before0 := p
	syscall.Kill(p, syscall.SIGKILL)
	waitGone(t, p)
	// Another run's hold (an update mid-way, say): a status read meanwhile
	// must leave it, and the keepalive must wait for it.
	other := filepath.Join(root, "hold.99999")
	os.WriteFile(other, nil, 0o644)
	if out, code := box("status"); code != 0 {
		t.Fatalf("status: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("status must not lift another run's hold")
	}
	if out, err := exec.Command(keep).CombinedOutput(); err != nil {
		t.Fatalf("keepalive: %v\n%s", err, out)
	}
	if alive(pid()) {
		t.Fatal("the keepalive must not start a controller while a deploy holds it")
	}
	os.Remove(other)
	if out, err := exec.Command(keep).CombinedOutput(); err != nil {
		t.Fatalf("keepalive: %v\n%s", err, out)
	}
	time.Sleep(300 * time.Millisecond)
	if p := pid(); !alive(p) || p == before0 {
		t.Fatal("the keepalive should restart the current release")
	}
	before := pid()
	exec.Command(keep).Run() // running already: nothing to do
	if pid() != before {
		t.Error("the keepalive started a second controller")
	}

	// status
	if out, code := box("status"); code != 0 || !strings.Contains(out, "running:  pid "+strconv.Itoa(before)) {
		t.Errorf("status (exit %d):\n%s", code, out)
	}

	// uninstall: only what deploy made; naut-launch and root stay
	os.WriteFile(filepath.Join(root, "naut-launch"), []byte("x"), 0o755)
	if out, code := box("uninstall", "-purge"); code != 0 {
		t.Fatalf("uninstall: exit %d\n%s", code, out)
	}
	if alive(before) {
		t.Error("uninstall must stop the controller")
	}
	if cron, _ := os.ReadFile(tab); strings.Contains(string(cron), "naut deploy") {
		t.Errorf("uninstall must take its crontab lines out:\n%s", cron)
	}
	for _, gone := range []string{"releases", "current", "previous", "keepalive.sh", "state"} {
		if _, err := os.Lstat(filepath.Join(root, gone)); err == nil {
			t.Errorf("uninstall -purge left %s", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "naut-launch")); err != nil {
		t.Error("uninstall must keep naut-launch: its capability takes root to set again")
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "naut-deploy")); err == nil {
		t.Error("uninstall must remove ~/.config/naut-deploy")
	}
}

func waitGone(t *testing.T, p int) {
	t.Helper()
	for range 50 {
		// A killed child of no one of ours is reaped by init; until then
		// it is a zombie, which Kill(0) still finds.
		b, err := os.ReadFile("/proc/" + strconv.Itoa(p) + "/stat")
		if err != nil || strings.Contains(string(b), ") Z ") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("pid %d did not die", p)
}

// goMachine is `uname -m` for this machine, so the fake box matches it
// and deploy builds on this naut.
func goMachine() string {
	switch goruntime.GOARCH {
	case "arm64":
		return "aarch64"
	case "arm":
		return "armv7l"
	}
	return "x86_64"
}
