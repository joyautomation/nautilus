package main

import (
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
// The scan here takes long on purpose, so the stop lands inside it and the
// gap between "port free" and "process gone" is wide enough to see.
func TestNautRunStopFreesHTTPPortBeforeLastScan(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	if goruntime.GOOS == "windows" {
		t.Skip("SIGTERM")
	}
	bin := buildNautCLI(t, t.TempDir(), nil)
	dir := t.TempDir()
	apiAddr := freeAddr(t)
	files := map[string]string{
		"nautilus.yaml": `name: slowstop
tasks:
  - program: main.st
    scan: 10ms
tags:
  - { name: Count, role: state, init: 0 }
server:
  addr: "` + apiAddr + `"
`,
		// A scan of a few hundred milliseconds: it overruns its 10 ms
		// period, so the loop is always mid-scan when the signal lands.
		"main.st": `PROGRAM Main
VAR_EXTERNAL
  Count : DINT;
END_VAR
VAR
  i : DINT;
END_VAR
FOR i := 1 TO 5000000 DO
  Count := Count + 1;
END_FOR;
END_PROGRAM
`,
	}
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bin, "run")
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NAUTILUS_ADDR=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-exited
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if resp, err := http.Get("http://" + apiAddr + "/api/state"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		select {
		case <-exited:
			t.Fatalf("naut run exited before serving; stderr:\n%s", stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("naut run never answered on %s; stderr:\n%s", apiAddr, stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // into the scan loop's stride

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	var freed time.Duration
	for freed == 0 {
		if ln, err := net.Listen("tcp", apiAddr); err == nil {
			freed = time.Since(start)
			ln.Close()
			break
		}
		select {
		case <-exited:
			t.Fatalf("naut run exited (after %v) with the HTTP port still bound by nobody's listener we could take", time.Since(start).Round(time.Millisecond))
		default:
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("the HTTP port %s was still bound 10 s after SIGTERM", apiAddr)
		}
		time.Sleep(2 * time.Millisecond)
	}
	select {
	case <-exited:
	case <-time.After(15 * time.Second):
		t.Fatalf("naut run did not exit within 15 s of SIGTERM; stderr:\n%s", stderr.String())
	}
	exit := time.Since(start)
	t.Logf("HTTP port free %v after SIGTERM, process gone after %v", freed.Round(time.Millisecond), exit.Round(time.Millisecond))
	if exit < 150*time.Millisecond {
		t.Skipf("the last scan took too little (exit %v) to tell the port's release from the exit", exit.Round(time.Millisecond))
	}
	// The port comes free well before the last scan ends: not after it.
	if freed > exit/2 {
		t.Errorf("the HTTP port came free %v after SIGTERM, the process exited at %v: the port was held through the last scan", freed.Round(time.Millisecond), exit.Round(time.Millisecond))
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("naut run exited %d; stderr:\n%s", code, stderr.String())
	}
}
