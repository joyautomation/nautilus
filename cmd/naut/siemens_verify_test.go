package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// verify runs naut siemens verify on the rig's project with args (flags),
// through the real flag parser, with env over the rig's environment.
func (r *deployRig) verify(env map[string]string, args ...string) (string, int) {
	r.t.Helper()
	var out bytes.Buffer
	o := siemensVerifyOptions{stdout: &out, stderr: &out, getenv: func(k string) string {
		if v, ok := env[k]; ok {
			return v
		}
		return r.env()[k]
	}}
	if err := parseSiemensVerifyFlags(&o, append(append([]string{"--agent", r.url}, args...), r.dir), &out); err != nil {
		r.t.Fatalf("flags %v: %v\n%s", args, err, out.String())
	}
	code := siemensVerify(context.Background(), o)
	return out.String(), code
}

// deployed is the rig after a deploy that loaded the CPU: its record id.
func (r *deployRig) deployed() string {
	r.t.Helper()
	r.adopt()
	r.fake.SetQuestions([]tiad.Question{{ID: tiad.QuestionStopCPU, Message: "stop the CPU"}})
	if out, code := r.run("", false, "--download", "--yes", "--allow-stop", "--reset-data"); code != 0 {
		r.t.Fatalf("deploy: exit %d:\n%s", code, out)
	}
	list, err := tiad.New(r.url, r.token).Deploys(context.Background(), deployCPU)
	if err != nil || len(list.Deploys) == 0 {
		r.t.Fatalf("deploys: %+v %v", list, err)
	}
	return list.Deploys[0].ID
}

// TestSiemensVerifyEqualAfterADeploy: right after a deploy the CPU runs
// it: exit 0, the deploy and TIA's own compare named, the blocks listed,
// no session left for the next call (an editor show opens one).
func TestSiemensVerifyEqualAfterADeploy(t *testing.T) {
	r := newDeployRig(t)
	id := r.deployed()
	out, code := r.verify(nil)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"verifying the CPU at " + deployCPU + " against deploy " + id + " (loaded,",
		"TIA Portal's own online compare, through Openness on the agent: read-only",
		"  block naut_main [FB", "EQUAL: the CPU at " + deployCPU + " runs deploy " + id} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	ss, err := tiad.New(r.url, r.token).Sessions(context.Background())
	if err != nil || len(ss) != 0 {
		t.Fatalf("sessions after verify: %+v %v", ss, err)
	}
	if len(r.downloads()) != 1 {
		t.Fatalf("verify loaded something: %+v", r.downloads())
	}
}

// TestSiemensVerifySendsThePassword: the rig's CPU asks credentials going
// online (its Full access user), so the compare needs the password from
// NAUTILUS_SIEMENS_FULL_ACCESS_PASSWORD; without it the agent refuses and
// verify says so, exit 1, never equal.
func TestSiemensVerifySendsThePassword(t *testing.T) {
	r := newDeployRig(t)
	r.deployed()
	out, code := r.verify(map[string]string{"NAUTILUS_SIEMENS_FULL_ACCESS_PASSWORD": ""})
	if code != 1 || strings.Contains(out, "EQUAL") || !strings.Contains(out, "fullAccessPassword") {
		t.Fatalf("without the password: exit %d:\n%s", code, out)
	}
}

// TestSiemensVerifyDifferentWhenChangedOutside: a load the agent did not
// make: exit 1, DIFFERENT, and since it is the CPU's latest deploy, a
// change outside nautilus.
func TestSiemensVerifyDifferentWhenChangedOutside(t *testing.T) {
	r := newDeployRig(t)
	r.deployed()
	r.fake.LoadOutside(deployCPU)
	out, code := r.verify(nil)
	if code != 1 || !strings.Contains(out, "DIFFERENT: the CPU at "+deployCPU) || !strings.Contains(out, "changed outside nautilus") ||
		strings.Contains(out, "EQUAL") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// TestSiemensVerifyNamesItsDeploy: --deploy compares with that record; an
// older one the CPU no longer runs is DIFFERENT and names the latest; an
// unknown one is refused.
func TestSiemensVerifyNamesItsDeploy(t *testing.T) {
	r := newDeployRig(t)
	first := r.deployed()
	out, code := r.verify(nil, "--deploy", first)
	if code != 0 || !strings.Contains(out, "against deploy "+first) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	r.write("50ms")
	if out, code := r.run("", false, "--download", "--yes", "--allow-stop", "--reset-data"); code != 0 {
		t.Fatalf("second deploy: exit %d:\n%s", code, out)
	}
	out, code = r.verify(nil, "--deploy", first)
	if code != 1 || !strings.Contains(out, "DIFFERENT") || !strings.Contains(out, "the CPU's latest deploy is d-") || strings.Contains(out, "changed outside") {
		t.Fatalf("an older deploy: exit %d:\n%s", code, out)
	}
	if out, code := r.verify(nil); code != 0 || strings.Contains(out, "against deploy "+first) {
		t.Fatalf("the latest deploy: exit %d:\n%s", code, out)
	}
	out, code = r.verify(nil, "--deploy", "d-none")
	if code != 1 || !strings.Contains(out, "no deploy d-none for the CPU at "+deployCPU) {
		t.Fatalf("an unknown deploy: exit %d:\n%s", code, out)
	}
}

// TestSiemensVerifyRefusals: nothing to compare with, an open session
// (one project at a time: the refusal names the session to close), and
// no token.
func TestSiemensVerifyRefusals(t *testing.T) {
	r := newDeployRig(t)
	out, code := r.verify(nil)
	if code != 1 || !strings.Contains(out, "no deploy record for the CPU at "+deployCPU) {
		t.Fatalf("no record: exit %d:\n%s", code, out)
	}
	r.deployed()
	s, err := tiad.New(r.url, r.token).Open(context.Background(), "projects/s7_demo/s7_demo.ap21")
	if err != nil {
		t.Fatal(err)
	}
	out, code = r.verify(nil)
	if code != 1 || !strings.Contains(out, s.ID) || strings.Contains(out, "EQUAL") {
		t.Fatalf("an open session: exit %d:\n%s", code, out)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	out, code = r.verify(map[string]string{"NAUTILUS_TIAD_TOKEN": ""})
	if code != 2 || !strings.Contains(out, "NAUTILUS_TIAD_TOKEN") {
		t.Fatalf("no token: exit %d:\n%s", code, out)
	}
}

// TestSiemensVerifySkipsARecordThatLoadedNothing: a later deploy TIA's
// check refused journals a record that loaded nothing; the CPU still runs
// the one before, which verify compares with.
func TestSiemensVerifySkipsARecordThatLoadedNothing(t *testing.T) {
	r := newDeployRig(t)
	loaded := r.deployed()
	r.write("50ms")
	// No --allow-stop: TIA's stop check goes unanswered, nothing loads.
	if out, code := r.run("", false, "--download", "--yes"); code == 0 {
		t.Fatalf("the unanswered stop loaded:\n%s", out)
	}
	list, err := tiad.New(r.url, r.token).Deploys(context.Background(), deployCPU)
	if err != nil || len(list.Deploys) < 2 || list.Deploys[0].Status != tiad.DeployNotLoaded {
		t.Fatalf("deploys: %+v %v, want a not-loaded record first", list, err)
	}
	out, code := r.verify(nil)
	if code != 0 || !strings.Contains(out, "against deploy "+loaded) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// TestSiemensVerifyFailsWhenItsSessionStaysOpen: the verdict stands, but a
// session the verify could not close blocks the next deploy: exit 1,
// naming the session to close.
func TestSiemensVerifyFailsWhenItsSessionStaysOpen(t *testing.T) {
	r := newDeployRig(t)
	r.deployed()
	r.fake.VerifyCloseError = "TIA is busy"
	out, code := r.verify(nil)
	if code != 1 || !strings.Contains(out, "EQUAL") || !strings.Contains(out, "left its session") || !strings.Contains(out, "DELETE /v1/sessions/") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
