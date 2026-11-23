package main

import (
	"os"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad/tiadtest"
)

// tiadFakeBin is tiad-fake (tools/tiad/build-fake.sh), the agent's own C#
// core under Mono, from TIAD_FAKE_BIN. Unset, the test skips, unless
// NAUTILUS_REQUIRE_TIAD=1 (the siemens-tiad CI job): a job that meant to
// test the C# agent must never pass by testing nothing.
func tiadFakeBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("TIAD_FAKE_BIN")
	if bin == "" {
		if os.Getenv("NAUTILUS_REQUIRE_TIAD") == "1" {
			t.Fatal("TIAD_FAKE_BIN is not set and NAUTILUS_REQUIRE_TIAD=1: build tools/tiad/build-fake.sh and point TIAD_FAKE_BIN at out/tiad-fake.exe")
		}
		t.Skip("TIAD_FAKE_BIN is not set (tools/tiad/build-fake.sh builds tiad-fake)")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("TIAD_FAKE_BIN: %v", err)
	}
	return bin
}

// TestSiemensDeployAdoptsOnTheCSharpAgent is TestSiemensDeployAdoptsTheCPUsProject
// against the agent's own core (#1272): the CPU runs a project of its own;
// the first deploy adopts that project's archive, finds the station
// unchanged and the project equal to the CPU, loads software only,
// compares equal and saves; the second builds on it; a second --adopt is
// refused.
func TestSiemensDeployAdoptsOnTheCSharpAgent(t *testing.T) {
	p := tiadtest.StartProcess(t, tiadFakeBin(t), tiadtest.Config{Targets: []string{deployCPU}})
	r := &deployRig{t: t, dir: t.TempDir(), url: p.URL, token: p.Client().Token}
	r.write("100ms")
	f := r.cpuRuns(false)
	before := len(p.Loaded(t))
	out, code := r.run("", false, "--download", "--yes", "--allow-stop", "--reset-data", "--adopt", f)
	if code != 0 {
		t.Fatalf("exit %d:\n%s\nagent:\n%s", code, out, p.Log())
	}
	at := 0
	for _, want := range []string{
		"adopted the project archive Customer Line 3.zap21 as projects/s7_demo/s7_demo.ap21",
		"its CPU: PLC_1 6ES7 510-1DK03-0AB0",
		"equal: the adopted project is what the CPU runs",
		"station: no change",
		"station: as adopted: the spec changes nothing in the adopted project's station, which is taken as the CPU's",
		"  hardware: not loaded\n",
		"downloaded; the online compare right after is equal",
		"saved projects/s7_demo/s7_demo.ap21 for the next deploy",
	} {
		i := strings.Index(out[at:], want)
		if i < 0 {
			t.Fatalf("missing, or out of order, %q in\n%s", want, out)
		}
		at += i
	}
	loads := p.Loaded(t)
	if len(loads) != before+1 || loads[len(loads)-1].Hardware || !loads[len(loads)-1].Software {
		t.Fatalf("loads: %+v", loads[before:])
	}
	r.write("50ms")
	out, code = r.run("", false, "--download", "--yes", "--allow-stop", "--reset-data")
	if code != 0 || !strings.Contains(out, "opened projects/s7_demo/s7_demo.ap21 as last saved") || strings.Contains(out, "comparing the adopted project") ||
		!strings.Contains(out, "project: adopted from the project archive Customer Line 3.zap21") {
		t.Fatalf("the second deploy: exit %d:\n%s", code, out)
	}
	if out, code := r.run("", false, "--adopt", f); code != 1 || !strings.Contains(out, "tiad holds this deploy's saved project already") {
		t.Fatalf("--adopt over a downloaded adoption: exit %d:\n%s", code, out)
	}
}

// TestSiemensDeployRefusesOnTheCSharpAgent: on the agent's own core, a
// stale archive is refused before anything is imported, and a CPU whose
// last deploy loaded another project refuses the next deploy.
func TestSiemensDeployRefusesOnTheCSharpAgent(t *testing.T) {
	bin := tiadFakeBin(t)
	p := tiadtest.StartProcess(t, bin, tiadtest.Config{Targets: []string{deployCPU}})
	r := &deployRig{t: t, dir: t.TempDir(), url: p.URL, token: p.Client().Token}
	r.write("100ms")
	stale := r.cpuRuns(true)
	before := len(p.Loaded(t))
	out, code := r.run("", false, "--download", "--yes", "--allow-stop", "--reset-data", "--adopt", stale)
	if code != 1 || !strings.Contains(out, "is not what the CPU runs") || len(p.Loaded(t)) != before {
		t.Fatalf("a stale archive: exit %d:\n%s", code, out)
	}
	if out, code := r.run("", false, "--download", "--yes", "--allow-stop", "--reset-data", "--adopt", "last-deploy"); code != 0 {
		t.Fatalf("--adopt last-deploy: exit %d:\n%s", code, out)
	}
	r.cpuRuns(false)
	before = len(p.Loaded(t))
	out, code = r.run("", false, "--download", "--yes", "--allow-stop", "--reset-data")
	if code != 1 || !strings.Contains(out, "loaded another project (projects/customer/customer.ap21)") || len(p.Loaded(t)) != before {
		t.Fatalf("the CPU's last deploy is another project: exit %d:\n%s", code, out)
	}
}
