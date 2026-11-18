package tiadtest_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad/tiadtest"
)

func TestContractGoFake(t *testing.T) {
	tiadtest.Contract(t, tiadtest.GoFake)
}

// fakeBin is tiad-fake (tools/tiad/build-fake.sh), from TIAD_FAKE_BIN.
// Unset, the tests that need it skip, unless NAUTILUS_REQUIRE_TIAD=1 (CI),
// which makes a missing binary a failure: a job that meant to test the C#
// agent must never pass by testing nothing.
func fakeBin(t *testing.T) string {
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

func tiadFake(t *testing.T) tiadtest.Starter {
	bin := fakeBin(t)
	return func(t *testing.T, cfg tiadtest.Config) tiadtest.Target {
		return tiadtest.StartProcess(t, bin, cfg)
	}
}

func TestContractTiadFake(t *testing.T) {
	tiadtest.Contract(t, tiadFake(t))
}

// TestRequireTiadFailsWithoutBinary runs TestContractTiadFake in a child
// test process: with NAUTILUS_REQUIRE_TIAD=1 and no binary it must fail,
// without it it must skip, and a binary that is named but missing fails
// either way.
func TestRequireTiadFailsWithoutBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the test binary again")
	}
	run := func(env ...string) (bool, string) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestContractTiadFake$", "-test.v")
		cmd.Env = append(tiadtest.AgentEnv("NAUTILUS_REQUIRE_TIAD=", "TIAD_FAKE_BIN="), env...)
		out, err := cmd.CombinedOutput()
		return err == nil, string(out)
	}
	if ok, out := run("NAUTILUS_REQUIRE_TIAD=1"); ok || !strings.Contains(out, "NAUTILUS_REQUIRE_TIAD=1") {
		t.Errorf("required and missing: passed=%v\n%s", ok, out)
	}
	if ok, out := run(); !ok || !strings.Contains(out, "--- SKIP: TestContractTiadFake") {
		t.Errorf("not required and missing: passed=%v\n%s", ok, out)
	}
	if ok, out := run("TIAD_FAKE_BIN=" + t.TempDir() + "/no-such-tiad-fake.exe"); ok || !strings.Contains(out, "TIAD_FAKE_BIN") {
		t.Errorf("named but missing: passed=%v\n%s", ok, out)
	}
}
