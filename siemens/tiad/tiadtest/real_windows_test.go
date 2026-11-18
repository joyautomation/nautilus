//go:build windows

package tiadtest_test

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/tiad"
	"github.com/joyautomation/nautilus/siemens/tiad/tiadtest"
)

// These tests run the real agent, tiad.exe as install-tiad.ps1 installed
// it (TIAD_BIN), on a TIA machine. They never start TIA: on a fresh clone
// the first TIA start starts the trial clock. Every agent but one runs
// with TIAD_NO_TIA=1, which fails the tia gate, and the agent and the
// suite both stay off any call that needs TIA. A tripwire fails a test
// if TIA Portal was running before it or is running after it.
//
// Build here and copy over:
//
//	GOOS=windows go test -c -o tiadtest.exe ./siemens/tiad/tiadtest
//	TIAD_BIN='C:\Program Files\tiad\tiad.exe' .\tiadtest.exe -test.run RealAgent -test.v

func realBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("TIAD_BIN")
	if bin == "" {
		t.Skip("TIAD_BIN is not set (the installed tiad.exe, on a TIA machine)")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("TIAD_BIN: %v", err)
	}
	return bin
}

// portalRunning reports whether a TIA Portal process is running.
func portalRunning(t testing.TB) bool {
	t.Helper()
	out, err := exec.Command("tasklist", "/FO", "CSV", "/NH", "/FI", "IMAGENAME eq Siemens.Automation.Portal.exe").Output()
	if err != nil {
		t.Fatalf("tasklist: %v", err)
	}
	return strings.Contains(strings.ToLower(string(out)), "siemens.automation.portal.exe")
}

// tripwire fails t if TIA Portal runs before t or after it.
func tripwire(t *testing.T) {
	t.Helper()
	if portalRunning(t) {
		t.Fatal("TIA Portal is running before the test; these tests must never be the first TIA start")
	}
	t.Cleanup(func() {
		if portalRunning(t) {
			t.Error("TIA Portal is running after the test: something started TIA")
		}
	})
}

// startReal starts bin with TIAD_NO_TIA=1, unless allowTIA, plus env.
func startReal(t *testing.T, bin string, allowTIA bool, env ...string) *tiadtest.Process {
	t.Helper()
	tripwire(t)
	if !allowTIA {
		env = append([]string{"TIAD_NO_TIA=1"}, env...)
	}
	return tiadtest.StartProcess(t, bin, tiadtest.Config{}, env...)
}

func TestRealAgentNoTIAContract(t *testing.T) {
	bin := realBin(t)
	tiadtest.ContractNoTIA(t, func(t *testing.T, cfg tiadtest.Config) tiadtest.Target {
		tripwire(t)
		return tiadtest.StartProcess(t, bin, cfg, "TIAD_NO_TIA=1")
	})
}

func gateStates(p tiad.Probe) map[string]tiad.Gate {
	m := map[string]tiad.Gate{}
	for _, g := range p.Gates {
		m[g.Name] = g
	}
	return m
}

// TestRealAgentProbe: on an installed TIA machine every gate the host
// computes holds, except tia, which TIAD_NO_TIA fails; the licence is
// unknown until TIA's first start.
func TestRealAgentProbe(t *testing.T) {
	c := startReal(t, realBin(t), false).Client()
	h, err := c.Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h.TIA != "V21" || h.Openness != "21.0.0.0" {
		t.Errorf("health = %+v, want TIA V21 and Openness 21.0.0.0", h)
	}
	p, err := c.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tiadtest.CheckGateShape(t, p)
	gs := gateStates(p)
	if g := gs["tia"]; g.State != tiad.GateFailed || !strings.Contains(g.Detail, "TIAD_NO_TIA=1") {
		t.Errorf("tia = %+v, want failed by TIAD_NO_TIA", g)
	}
	for _, n := range []string{"openness-group", "openness-allowlist", "workdir"} {
		if g := gs[n]; g.State != tiad.GateOK {
			t.Errorf("%s = %+v, want ok", n, g)
		}
	}
	if g := gs["licence"]; g.State != tiad.GateUnknown {
		t.Errorf("licence = %+v, want unknown", g)
	}
	if p.Usable {
		t.Error("usable with TIAD_NO_TIA, want not")
	}
}

// TestRealAgentGatesAllowTIA: without TIAD_NO_TIA the tia gate holds too,
// so probe says usable. Only health and probe run here, and neither ever
// makes the TIA side (the contract proves it against tiad-fake's call log).
func TestRealAgentGatesAllowTIA(t *testing.T) {
	c := startReal(t, realBin(t), true).Client()
	p, err := c.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tiadtest.CheckGateShape(t, p)
	if g := gateStates(p)["tia"]; g.State != tiad.GateOK {
		t.Errorf("tia = %+v, want ok", g)
	}
	if !p.Usable {
		t.Errorf("probe = %+v, want usable", p)
	}
}

// TestRealAgentRefusesTIACalls: with TIAD_NO_TIA a call that needs TIA
// answers 412 gate, naming tia.
func TestRealAgentRefusesTIACalls(t *testing.T) {
	c := startReal(t, realBin(t), false).Client()
	const g = "GSDML-V2.4-Example-Device-20261117.xml"
	if _, err := c.PutFile(ctx, "gsd/"+g, []byte("<ISO15745Profile/>")); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"gsd/install": func() error { _, err := c.InstallGSD(ctx, "gsd/"+g); return err },
		"projects": func() error {
			_, err := c.CreateProject(ctx, "p", tiadtest.BenchStation(), tiadtest.BenchSecrets)
			return err
		},
		"sessions": func() error { _, err := c.Open(ctx, "projects/p/p.ap21"); return err },
	} {
		err := call()
		var te *tiad.Error
		if !errors.As(err, &te) || te.Kind != tiad.KindGate || !strings.Contains(te.Message, "gate tia") {
			t.Errorf("%s: %v, want 412 gate tia", name, err)
		}
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestRealAgentCopyIsNotAllowlisted: the allowlist entry names the
// installed exe, so the same bytes elsewhere fail the gate, as TIA would
// refuse them.
func TestRealAgentCopyIsNotAllowlisted(t *testing.T) {
	bin := realBin(t)
	dir := t.TempDir()
	for _, f := range []string{filepath.Base(bin), "tiad-core.dll"} {
		copyFile(t, filepath.Join(filepath.Dir(bin), f), filepath.Join(dir, f))
	}
	c := startReal(t, filepath.Join(dir, filepath.Base(bin)), false).Client()
	p, err := c.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tiadtest.CheckGateShape(t, p)
	if g := gateStates(p)["openness-allowlist"]; g.State != tiad.GateFailed || !strings.Contains(g.Detail, "no entry names") {
		t.Errorf("openness-allowlist for a copy = %+v, want failed: no entry names it", g)
	}
}

// TestRealAgentStdin: under a scheduled task the agent's stdin may be
// closed from the start, so it exits on a closed stdin only when
// TIAD_EXIT_ON_STDIN=1 asks it to (as the suite does).
func TestRealAgentStdin(t *testing.T) {
	bin := realBin(t)
	p := startReal(t, bin, false, "TIAD_EXIT_ON_STDIN=")
	p.CloseStdin()
	if p.Exited(3 * time.Second) {
		t.Fatal("the agent exited when its stdin closed, without TIAD_EXIT_ON_STDIN")
	}
	if _, err := p.Client().Health(ctx); err != nil {
		t.Fatalf("health after stdin closed: %v", err)
	}
	q := startReal(t, bin, false)
	q.CloseStdin()
	if !q.Exited(10 * time.Second) {
		t.Fatal("with TIAD_EXIT_ON_STDIN=1 the agent outlived its stdin")
	}
}
