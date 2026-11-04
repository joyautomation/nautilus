package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// SIGTERM to `naut run` gives the HTTP port back at once, before the scan
// loop's last scan and retain save (nautilus#599). The orderly stop waits
// for those (nautilus#531), and the listener was closed by a defer that ran
// only after that wait, so the port stayed bound for the length of the
// scan in flight. A `naut run` started again meanwhile — the rig's smoke
// check 07 switching projects, a supervisor's quick restart — then lost the
// bind, printed "tag api: … address already in use (continuing without
// it)" and ran with no dashboard or tag API, while the old process exited.
//
// The scan here is long on purpose (about slowScan of wall time) and
// overruns its 10 ms period, so the stop lands inside a scan. ST has no
// wall clock to spin on, so the loop's length is calibrated on this
// machine first (scans per second at a known iteration count) rather than
// fixed: a fixed count falls under any threshold on fast enough hardware.
// The overrun also covers the Linux sleeper's past-deadline answer: if it
// stops looking at the stop again, the loop never ends and the exit takes
// the whole runStopTimeout.
func TestNautRunStopFreesHTTPPortBeforeLastScan(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	if goruntime.GOOS == "windows" {
		t.Skip("SIGTERM")
	}
	const (
		slowScan = 600 * time.Millisecond
		// promptly: the port must be free this soon after SIGTERM. Held
		// through the last scan it is free only at the exit, so a stop
		// landing more than this before the scan's end shows the bug.
		promptly = 100 * time.Millisecond
	)
	bin := buildNautCLI(t, t.TempDir(), nil)

	// Calibrate: the time one scan of calN iterations takes here.
	const calN = 1_000_000
	cal := startSlowRun(t, bin, calN)
	s0, t0 := cal.scans(t), time.Now()
	time.Sleep(time.Second)
	s1, t1 := cal.scans(t), time.Now()
	cal.kill()
	perScan := t1.Sub(t0)
	if s1 > s0 {
		perScan /= time.Duration(s1 - s0)
	}
	n := int64(float64(calN) * float64(slowScan) / float64(perScan))
	if n < calN {
		n = calN
	}
	if n > 2_000_000_000 { // DINT
		n = 2_000_000_000
	}
	t.Logf("calibration: %d iterations take %v; using %d for about %v", calN, perScan.Round(time.Millisecond), n, slowScan)

	// A stop landing in the last `promptly` of a scan cannot tell a held
	// port from a freed one: run again (each try lands at a random phase).
	for try := 1; ; try++ {
		freed, exit := stopSlowRun(t, startSlowRun(t, bin, n))
		t.Logf("try %d: HTTP port free %v after SIGTERM, process gone after %v", try, freed.Round(time.Millisecond), exit.Round(time.Millisecond))
		if exit >= runStopTimeout {
			t.Fatalf("naut run took %v to exit: the overrunning scan loop did not stop on SIGTERM and the stop waited out runStopTimeout (%v)", exit.Round(time.Millisecond), runStopTimeout)
		}
		if freed > promptly {
			t.Fatalf("the HTTP port came free %v after SIGTERM (process gone at %v); want under %v: the port was held through the last scan", freed.Round(time.Millisecond), exit.Round(time.Millisecond), promptly)
		}
		if exit > 2*promptly || try == 5 {
			return
		}
	}
}

type slowRun struct {
	cmd     *exec.Cmd
	addr    string
	exited  chan struct{}
	stderr  *lockedBuffer
	stopped bool
}

// startSlowRun starts `naut run` on a project whose one task loops n times
// a scan, and waits for its tag API.
func startSlowRun(t *testing.T, bin string, n int64) *slowRun {
	t.Helper()
	dir := t.TempDir()
	addr := freeAddr(t)
	files := map[string]string{
		"nautilus.yaml": `name: slowstop
tasks:
  - program: main.st
    scan: 10ms
tags:
  - { name: Count, role: state, init: 0 }
server:
  addr: "` + addr + `"
`,
		"main.st": fmt.Sprintf(`PROGRAM Main
VAR_EXTERNAL
  Count : DINT;
END_VAR
VAR
  i : DINT;
END_VAR
FOR i := 1 TO %d DO
  Count := Count + 1;
END_FOR;
END_PROGRAM
`, n),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := &slowRun{cmd: exec.Command(bin, "run"), addr: addr, exited: make(chan struct{}), stderr: &lockedBuffer{}}
	r.cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NAUTILUS_ADDR=") {
			r.cmd.Env = append(r.cmd.Env, kv)
		}
	}
	r.cmd.Stderr = r.stderr
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { r.cmd.Wait(); close(r.exited) }()
	t.Cleanup(r.kill)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if resp, err := http.Get("http://" + addr + "/api/state"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		select {
		case <-r.exited:
			t.Fatalf("naut run exited before serving; stderr:\n%s", r.stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("naut run never answered on %s; stderr:\n%s", addr, r.stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	return r
}

func (r *slowRun) kill() {
	if r.stopped {
		return
	}
	r.stopped = true
	r.cmd.Process.Kill()
	<-r.exited
}

// scans is the main task's scan count from /api/state.
func (r *slowRun) scans(t *testing.T) uint64 {
	t.Helper()
	resp, err := http.Get("http://" + r.addr + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st struct {
		Scans uint64 `json:"scans"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st.Scans
}

// stopSlowRun sends SIGTERM a while into the run and times two things: when
// the HTTP port takes a new bind, and when the process is gone.
func stopSlowRun(t *testing.T, r *slowRun) (freed, exit time.Duration) {
	t.Helper()
	time.Sleep(300 * time.Millisecond) // into the scan loop's stride
	start := time.Now()
	if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	for freed == 0 {
		if ln, err := net.Listen("tcp", r.addr); err == nil {
			freed = time.Since(start)
			ln.Close()
			break
		}
		if time.Since(start) > 2*runStopTimeout {
			t.Fatalf("the HTTP port %s was still bound %v after SIGTERM", r.addr, 2*runStopTimeout)
		}
		time.Sleep(2 * time.Millisecond)
	}
	select {
	case <-r.exited:
	case <-time.After(15 * time.Second):
		t.Fatalf("naut run did not exit within 15 s of SIGTERM; stderr:\n%s", r.stderr.String())
	}
	r.stopped = true
	exit = time.Since(start)
	if code := r.cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("naut run exited %d; stderr:\n%s", code, r.stderr.String())
	}
	return freed, exit
}
