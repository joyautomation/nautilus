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
	if len(calls) != 8 || !strings.Contains(calls[0].cmd, ".config/naut-deploy") || calls[1].cmd != "uname -sm" || !strings.HasSuffix(calls[2].cmd, "sh -s -- base") {
		t.Fatalf("want 8 ssh calls (mode, uname, base (what the box can unpack), partsize, append, partsize, assemble, install), got %d: %+v", len(calls), calls)
	}
	calls = calls[3:]
	for _, c := range calls {
		if strings.Contains(c.cmd, "sudo") {
			t.Errorf("cron mode must not use sudo: %q", c.cmd)
		}
	}
	if !strings.Contains(calls[1].cmd, "/opt/apps/nautilus/incoming/plant-") || !strings.Contains(calls[1].cmd, ".wire.part") {
		t.Errorf("the controller is appended under <root>/incoming: %q", calls[1].cmd)
	}
	if !strings.Contains(calls[3].cmd, "NAUT_SUPERVISOR=cron NAUT_ROOT=/opt/apps/nautilus sh -s -- assemble plant-") {
		t.Errorf("box.sh rebuilds the controller in cron mode: %q", calls[3].cmd)
	}
	if !strings.Contains(calls[4].cmd, "NAUT_SUPERVISOR=cron NAUT_ROOT=/opt/apps/nautilus sh -s -- install plant-") {
		t.Errorf("box.sh runs install in cron mode: %q", calls[4].cmd)
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
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	// SLOW_STOP=<s>: take that long to stop after TERM, as a controller
	// saving its state on the way out does.
	if d := os.Getenv("SLOW_STOP"); d != "" {
		n, _ := strconv.Atoi(d)
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		go func() { <-c; time.Sleep(time.Duration(n) * time.Second); os.Exit(0) }()
	}
	// Released as "late": start, then die 2.5 s in, as a release that
	// fails just after its start check does.
	if self, _ := filepath.EvalSymlinks(os.Args[0]); strings.Contains(self, "/releases/late/") {
		go func() { time.Sleep(2500 * time.Millisecond); os.Exit(1) }()
	}
	// Not dumpable, like a controller naut-launch gave a capability: its
	// /proc/<pid>/exe is unreadable, even to its owner.
	syscall.RawSyscall(syscall.SYS_PRCTL, 4 /* PR_SET_DUMPABLE */, 0, 0)
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("sleeper")
		return
	}
	wd, _ := os.Getwd()
	holds, _ := filepath.Glob("../hold.*") // run in state/, under the root
	fmt.Println("sleeper up in", wd, "X="+os.Getenv("X"), "holds="+fmt.Sprint(len(holds)), "STATE_DIRECTORY="+os.Getenv("STATE_DIRECTORY"))
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
	writeSleepStandIn(t, bin)
	os.WriteFile(filepath.Join(root, "env"), []byte("X=from-env\n"), 0o644)

	var settle string // FAKE_SETTLE for the next box runs (see writeSleepStandIn)
	box := func(args ...string) (string, int) {
		t.Helper()
		c := exec.Command("sh", append([]string{"-s", "--"}, args...)...)
		c.Stdin = strings.NewReader(deployBoxSh)
		c.Env = append(os.Environ(), "NAUT_SUPERVISOR=cron", "NAUT_ROOT="+root, "HOME="+home,
			"FAKE_CRONTAB="+tab, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		if settle != "" {
			c.Env = append(c.Env, "FAKE_SETTLE="+settle)
		}
		var out bytes.Buffer
		c.Stdout, c.Stderr = &out, &out
		err := c.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			return err.Error(), -1 // not t.Fatal: box also runs on another goroutine
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
	// in that moment cannot start a second one. STATE_DIRECTORY is the
	// state directory, as systemd's StateDirectory= sets it: the voter
	// state (pairStateDir) and the users file (#708) find it in cron mode
	// too, in the directory every deploy keeps.
	if !strings.Contains(logText(), "sleeper up in "+filepath.Join(root, "state")+" X=from-env holds=1 STATE_DIRECTORY="+filepath.Join(root, "state")) {
		t.Errorf("the controller runs in state/ with env's variables, started under box.sh's hold; log:\n%s", logText())
	}
	// What lives in STATE_DIRECTORY must outlast every deploy step below
	// (updates, a failed one, a late one, rollback), as in service mode
	// (TestDeployBoxKeepsTheVoterState): the voter state, the pair's log
	// and the users file.
	kept := map[string]string{"quorum.state": "voter state: promised 1042", "oplog.jsonl": `{"seq":1,"term":1002}`, "users.json": `{"version":1,"users":{}}`}
	for n, v := range kept {
		os.WriteFile(filepath.Join(root, "state", n), []byte(v), 0o600)
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

	// an update that starts and dies a moment later: the settle wait
	// catches it (it dies 2.5 s in, 1.5 s after the 1 s first look and
	// 1.5 s before a 3 s settle ends); back to r2, exit 3
	incoming("late", sleeperBytes)
	settle = "3"
	out, code := box("update", "late")
	settle = ""
	if code != 3 {
		t.Fatalf("a release that dies after starting must roll back with exit 3, got %d\n%s", code, out)
	}
	if p := pid(); !alive(p) || link("current") != "r2" || link("previous") != "r1" {
		t.Fatalf("after the late death r2 should run, current r2, previous r1: alive %v, %q %q", alive(p), link("current"), link("previous"))
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

	// An update whose old controller takes 2 s to stop, with a status read
	// and keepalive ticks meanwhile: the update's hold must stand until the
	// update ends, and cron must start nothing of its own.
	os.WriteFile(filepath.Join(root, "env"), []byte("X=from-env\nSLOW_STOP=2\n"), 0o644)
	if out, code := box("update", "r2"); code != 0 { // restarts with SLOW_STOP
		t.Fatalf("update to r2: exit %d\n%s", code, out)
	}
	cronStarts := func() int { return strings.Count(logText(), " for cron at ") }
	cron0 := cronStarts()
	holds := func() int { h, _ := filepath.Glob(filepath.Join(root, "hold.*")); return len(h) }
	incoming("r3", sleeperBytes)
	done := make(chan struct{})
	var upOut string
	var upCode int
	go func() { upOut, upCode = box("update", "r3"); close(done) }()
	for i := 0; holds() == 0; i++ {
		if i > 100 {
			t.Fatal("the update never took its hold")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // into its stop of the old controller
	if out, code := box("status"); code != 0 {
		t.Fatalf("status during the update: exit %d\n%s", code, out)
	}
	select {
	case <-done:
		t.Fatal("the update ended before the status read finished: the test proves nothing")
	default:
	}
	if holds() == 0 {
		t.Fatal("a status read during an update lifted the update's hold")
	}
	for ticking := true; ticking; {
		exec.Command(keep).Run() // a cron tick
		select {
		case <-done:
			ticking = false
		case <-time.After(50 * time.Millisecond):
		}
	}
	if upCode != 0 {
		t.Fatalf("update to r3: exit %d\n%s", upCode, upOut)
	}
	if n := cronStarts() - cron0; n != 0 {
		t.Errorf("cron started %d controller(s) during the update; log:\n%s", n, logText())
	}
	last := pid()
	if !alive(last) || link("current") != "r3" || holds() != 0 {
		t.Errorf("after the update r3 should run with no hold left: alive %v, current %q, holds %d", alive(last), link("current"), holds())
	}

	for n, v := range kept {
		if b, err := os.ReadFile(filepath.Join(root, "state", n)); err != nil || string(b) != v {
			t.Errorf("after the deploys, state/%s = %q, %v; want it untouched", n, b, err)
		}
	}

	// uninstall: only what deploy made; naut-launch and root stay
	os.WriteFile(filepath.Join(root, "naut-launch"), []byte("x"), 0o755)
	if out, code := box("uninstall", "-purge"); code != 0 {
		t.Fatalf("uninstall: exit %d\n%s", code, out)
	}
	if alive(before) || alive(last) {
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
