package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/tiad"
	"github.com/joyautomation/nautilus/siemens/tiad/tiadtest"
	swriter "github.com/joyautomation/nautilus/siemens/writer"
)

// The CPU of the deploy tests: a documentation address (RFC 5737).
const deployCPU = "192.0.2.10"

const siemensDeployManifest = `name: s7 demo
tasks:
  - program: main.st
    scan: SCAN
tags:
  - {name: Level, role: input, type: REAL}
target:
  siemens:
    station:
      cpu: {name: PLC_1, order: 6ES7 510-1DK03-0AB0, firmware: V3.1, address: 192.0.2.10, subnet-mask: 255.255.255.0, profinet-name: plc-1}
`

// The program: line 4 is the statement a test's compile error is on.
const siemensDeployProgram = "PROGRAM main\nVAR x : REAL; n : DINT; END_VAR\nn := n + 1;\nx := Level;\nEND_PROGRAM\n"

// deployRig is a project on disk and a fake tiad serving it.
type deployRig struct {
	t      *testing.T
	dir    string
	fake   *tiadtest.Fake
	url    string
	commit string
	// mangleDownload replaces a real download's reply with an unreadable
	// one, after the fake handled it.
	mangleDownload bool
}

func newDeployRig(t *testing.T) *deployRig {
	t.Helper()
	r := &deployRig{t: t, dir: t.TempDir(), fake: tiadtest.New()}
	r.fake.Targets = []string{deployCPU}
	// In front of the fake: a real download's reply can be replaced by
	// one the client cannot read (a proxy's 502 after the agent loaded).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		body, _ := io.ReadAll(q.Body)
		q.Body = io.NopCloser(bytes.NewReader(body))
		if r.mangleDownload && strings.HasSuffix(q.URL.Path, "/download") && !strings.Contains(string(body), `"dryRun":true`) {
			r.fake.ServeHTTP(httptest.NewRecorder(), q)
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, "<html>bad gateway</html>")
			return
		}
		r.fake.ServeHTTP(w, q)
	}))
	t.Cleanup(srv.Close)
	r.url = srv.URL
	r.write("100ms")
	return r
}

func (r *deployRig) write(scan string) {
	r.t.Helper()
	for name, body := range map[string]string{
		"nautilus.yaml": strings.Replace(siemensDeployManifest, "SCAN", scan, 1),
		"main.st":       siemensDeployProgram,
	} {
		if err := os.WriteFile(filepath.Join(r.dir, name), []byte(body), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
}

// run deploys with args (flags) and the operator's stdin; it returns
// stdout+stderr and the exit code.
func (r *deployRig) run(stdin string, terminal bool, args ...string) (string, int) {
	r.t.Helper()
	var out bytes.Buffer
	o := siemensDeployOptions{getenv: func(k string) string { return r.env()[k] },
		stdout: &out, stderr: &out, stdin: strings.NewReader(stdin), terminal: terminal, by: "test operator",
		gitCommit: func(string) string { return r.commit }, timeout: time.Minute}
	// Through the real flag parser, so a flag bound to the wrong field
	// fails here. A download names --start unless the test chose.
	if slices.Contains(args, "--download") && !slices.Contains(args, "--start") && !slices.Contains(args, "--no-start") {
		args = append(args, "--start")
	}
	if err := parseSiemensDeployFlags(&o, append(append([]string{"--agent", r.url}, args...), r.dir), &out); err != nil {
		r.t.Fatalf("flags %v: %v\n%s", args, err, out.String())
	}
	code := siemensDeploy(context.Background(), o)
	return out.String(), code
}

// env is the deploy's environment: the agent's token and the station's
// secrets.
func (r *deployRig) env() map[string]string {
	return map[string]string{
		"NAUTILUS_TIAD_TOKEN":                   r.fake.Token,
		"NAUTILUS_SIEMENS_CONFIG_PASSWORD":      "Config-Pass-1234",
		"NAUTILUS_SIEMENS_FULL_ACCESS_PASSWORD": tiadtest.BenchFullAccessPassword,
	}
}

// generated is what the writer makes of the rig's project.
func (r *deployRig) generated() swriter.Output {
	r.t.Helper()
	in, err := siemensWriterInput(r.dir, "")
	if err != nil {
		r.t.Fatal(err)
	}
	out, diags, err := swriter.Write(in)
	if err != nil || len(diags) > 0 {
		r.t.Fatal(err, diags)
	}
	return out
}

// tiaBodyLine is the line TIA names for the first generated line of file
// holding want: 1 is BEGIN (TIA V21, #789).
func tiaBodyLine(t *testing.T, out swriter.Output, file, want string) string {
	t.Helper()
	for _, f := range out.Files {
		if f.Path != file {
			continue
		}
		begin := 0
		for i, l := range strings.Split(string(f.Data), "\n") {
			if l == "BEGIN" {
				begin = i + 1
			} else if begin > 0 && strings.Contains(l, want) {
				return strconv.Itoa(i + 1 - begin + 1)
			}
		}
	}
	t.Fatalf("%q not in the body of %s", want, file)
	return ""
}

// TestSiemensDeployMapsTIACompileErrorsToTheSourceLine: a compile error,
// as TIA V21 reports it (the body line in the leaf, the block in its
// parents), is printed against main.st's line, and nothing downloads.
func TestSiemensDeployMapsTIACompileErrorsToTheSourceLine(t *testing.T) {
	r := newDeployRig(t)
	line := tiaBodyLine(t, r.generated(), "naut_main.scl", `#x := "naut_Tags".Level;`)
	r.fake.SetCompileResult(tiad.CompileResult{Result: "error", Errors: 1, Messages: []tiad.CompileMessage{
		{Object: line, Severity: "error", Text: "Tag \"naut_Tags\".Level not defined.", Path: []string{"PLC_1", "Program blocks", "nautilus", "naut_main (FB1)"}},
		{Object: "", Severity: "error", Text: "Compiling finished (errors: 1; warnings: 0)"},
	}})
	out, code := r.run("", false, "--download", "--yes", "--allow-stop")
	want := filepath.Join(r.dir, "main.st") + ":4:1: error: TIA: Tag \"naut_Tags\".Level not defined. [naut_main]"
	if code != 1 || !strings.Contains(out, want) || !strings.Contains(out, "TIA's compile: 1 error(s), 0 warning(s); nothing downloaded") ||
		strings.Contains(out, "PLAN") {
		t.Fatalf("exit %d, want 1 with %q, stopping at the compile, in\n%s", code, want, out)
	}
	if len(r.fake.Downloads()) != 0 {
		t.Fatalf("a project that did not compile was downloaded: %+v", r.fake.Downloads())
	}
}

// TestSiemensDeployBuildsOnlyWithoutDownload: no flag compiles and stops,
// with nothing loaded and the project not saved.
func TestSiemensDeployBuildsOnlyWithoutDownload(t *testing.T) {
	r := newDeployRig(t)
	out, code := r.run("", false)
	if code != 0 || !strings.Contains(out, "built only") || !strings.Contains(out, "naut_OB_main: created") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if len(r.fake.Downloads()) != 0 {
		t.Fatal("build-only downloaded")
	}
	if _, ok := r.fake.File("projects/s7_demo/s7_demo.ap21"); !ok {
		t.Fatal("the project was not created")
	}
	// Not saved: the next run opens the project as it was created, with
	// none of the first run's blocks, so its OB is made again.
	out, _ = r.run("", false)
	if !strings.Contains(out, "naut_OB_main: created") {
		t.Fatalf("build-only saved the project:\n%s", out)
	}
}

// TestSiemensDeployAsksBeforeItLoads: the plan (OB steps, the dry run's
// question, every answer) is printed, and without a yes nothing loads.
func TestSiemensDeployAsksBeforeItLoads(t *testing.T) {
	r := newDeployRig(t)
	r.fake.SetQuestions([]tiad.Question{{ID: tiad.QuestionStopCPU, Message: "stop the CPU"}})
	for _, c := range []struct {
		name, stdin string
		terminal    bool
		says        string
	}{
		{"no terminal, no --yes", "", false, "re-run with --yes"},
		{"answered no", "no\n", true, "not confirmed"},
		{"answered y", "y\n", true, "not confirmed"},
	} {
		out, code := r.run(c.stdin, c.terminal, "--download", "--dry-run", "--allow-stop")
		if code != 1 || !strings.Contains(out, c.says) || strings.Contains(out, "TIA asks stop-cpu") {
			t.Fatalf("%s: exit %d:\n%s", c.name, code, out)
		}
		plan := out[strings.Index(out, "PLAN"):]
		for _, want := range []string{
			"no deploy record for this CPU",
			"cyclic interrupt OB naut_OB_main: created",
			"after your yes: a dry run first (--dry-run)",
			"announced: you have told everyone who shares this CPU",
			"user management: not answered",
			"stop the CPU if TIA needs to: yes (--allow-stop)",
			"reinitialize a DB: no. If TIA asks, the download is cancelled there: before the CPU was stopped that loads nothing, but after a stop it ends IN DOUBT",
			"start the CPU after the load: yes (--start), also a CPU that was in STOP before",
			"hardware: NOT loaded, although it may differ from the CPU's",
		} {
			if !strings.Contains(plan, want) {
				t.Errorf("%s: the plan lacks %q:\n%s", c.name, want, out)
			}
		}
		if len(r.fake.Downloads()) != 0 {
			t.Fatalf("%s: downloaded without a yes", c.name)
		}
	}
}

// TestSiemensDeployRefusesAStopItWasNotAllowed: when the dry run shows TIA
// stops the CPU, a deploy without --allow-stop refuses before asking,
// and a reinitialize of a DB not named in --reinit too.
func TestSiemensDeployRefusesAStopItWasNotAllowed(t *testing.T) {
	r := newDeployRig(t)
	r.fake.SetQuestions([]tiad.Question{{ID: tiad.QuestionStopCPU, Message: "stop the CPU"}})
	out, code := r.run("", false, "--download", "--dry-run", "--yes")
	if code != 1 || !strings.Contains(out, "the dry run says TIA stops the CPU to load this: pass --allow-stop") ||
		strings.Contains(out, "downloading to") {
		t.Fatalf("exit %d, downloads %d:\n%s", code, len(r.fake.Downloads()), out)
	}
	r.fake.SetQuestions([]tiad.Question{{ID: tiad.QuestionReinitialize, Message: "reinitialize", Objects: []string{"naut_Tags", "naut_main_DB"}}})
	out, code = r.run("", false, "--download", "--dry-run", "--yes", "--allow-stop", "--reinit=naut_Tags")
	if code != 1 || !strings.Contains(out, "TIA reinitializes naut_main_DB") || len(r.fake.Downloads()) != 0 {
		t.Fatalf("exit %d, downloads %d:\n%s", code, len(r.fake.Downloads()), out)
	}
}

// TestSiemensDeployEndToEnd: one command loads, the record and the saved
// project follow; a second deploy with a new scan opens that project,
// shows the OB recreated with its old and new cycle before it asks, and
// checks the CPU still runs the first deploy.
func TestSiemensDeployEndToEnd(t *testing.T) {
	r := newDeployRig(t)
	r.fake.SetQuestions([]tiad.Question{{ID: tiad.QuestionStopCPU, Message: "stop the CPU"}})
	r.commit = strings.Repeat("ab", 20)
	out, code := r.run("", false, "--download", "--yes", "--allow-stop")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"created projects/s7_demo/s7_demo.ap21", "downloaded; the online compare right after is equal",
		"deploy record d-", "source " + r.commit + " sha256:", "saved projects/s7_demo/s7_demo.ap21"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	dl := r.fake.Downloads()
	if len(dl) != 1 || dl[0].Request.Hardware || !dl[0].Request.Software || !dl[0].Request.Announced || dl[0].Request.OutsideChanges != nil ||
		dl[0].Request.Decisions.StopCPU == nil || !*dl[0].Request.Decisions.StopCPU || len(dl[0].Request.Decisions.Reinitialize) != 0 ||
		dl[0].Request.Decisions.ConsistentDownload == nil || !*dl[0].Request.Decisions.ConsistentDownload {
		t.Fatalf("downloads: %+v", dl)
	}

	r.write("50ms")
	out, code = r.run("", false, "--download", "--allow-stop")
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	plan := out[strings.Index(out, "PLAN"):]
	for _, want := range []string{"the CPU still compares equal to it", "naut_OB_main: RECREATED, was", "100 ms", "50 ms",
		"hardware: NOT loaded, although it may differ from the CPU's (no record of the station spec the last hardware load loaded)"} {
		if !strings.Contains(plan, want) {
			t.Errorf("the plan lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "opened projects/s7_demo/s7_demo.ap21") {
		t.Fatalf("the second deploy did not open the saved project:\n%s", out)
	}
	if len(r.fake.Downloads()) != 1 {
		t.Fatal("downloaded without a yes")
	}
	out, code = r.run("", false, "--download", "--yes", "--allow-stop")
	if code != 0 || len(r.fake.Downloads()) != 2 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// TestSiemensDeployRefusesACPUChangedOutside: the CPU no longer runs its
// last deploy, so a deploy would replace someone's change; refused
// without --accept-outside-changes, which the plan then names.
func TestSiemensDeployRefusesACPUChangedOutside(t *testing.T) {
	r := newDeployRig(t)
	r.fake.SetQuestions([]tiad.Question{{ID: tiad.QuestionStopCPU, Message: "stop the CPU"}})
	if out, code := r.run("", false, "--download", "--yes", "--allow-stop"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	r.fake.LoadOutside(deployCPU)
	out, code := r.run("", false, "--download", "--yes", "--allow-stop")
	if code != 1 || !strings.Contains(out, "compares DIFFERENT") || !strings.Contains(out, "--accept-outside-changes") || len(r.fake.Downloads()) != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	out, code = r.run("", false, "--download", "--yes", "--allow-stop", "--accept-outside-changes")
	if code != 0 || !strings.Contains(out, "replacing it anyway (--accept-outside-changes): the deploy record journals your accept, by test operator") ||
		len(r.fake.Downloads()) != 2 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// The accept is on the record, naming the deploy whose verify found
	// the change.
	list, err := tiad.New(r.url, r.fake.Token).Deploys(context.Background(), deployCPU)
	if err != nil || len(list.Deploys) != 2 {
		t.Fatalf("%+v %v", list, err)
	}
	want := tiad.OutsideChangesRecord{By: "test operator", Verdict: tiad.OutsideDifferent, Deploy: list.Deploys[1].ID, ClaimedBy: "caller"}
	if oc := list.Deploys[0].OutsideChanges; oc == nil || *oc != want || list.Deploys[1].OutsideChanges != nil ||
		!strings.Contains(out, "outside change replaced, accepted by test operator (verdict different, deploy "+want.Deploy+")") {
		t.Fatalf("the record's accept: %+v, want %+v\n%s", list.Deploys[0].OutsideChanges, want, out)
	}
}

// TestSiemensDeployOutcomeUnknownComparesOnTheSession: a load whose
// compare is not equal ends in doubt; the deploy compares again on the
// same session and says what TIA may have done, never "done".
func TestSiemensDeployOutcomeUnknownComparesOnTheSession(t *testing.T) {
	r := newDeployRig(t)
	r.fake.SetQuestions([]tiad.Question{{ID: tiad.QuestionStopCPU, Message: "stop the CPU"}})
	r.fake.CompareResult = &tiad.CompareResult{Equal: false, Differences: []tiad.Difference{{Object: "naut_main [FB1]", State: "different"}}}
	out, code := r.run("", false, "--download", "--yes", "--allow-stop")
	if code != 1 || !strings.Contains(out, "OUTCOME UNKNOWN") || !strings.Contains(out, "the compare after it is DIFFERENT: naut_main [FB1] (different)") ||
		strings.Contains(out, "saved projects/") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// TestSiemensDeploySecretsAreNamedByVariable: a missing secret is refused
// naming the variable to set, before anything reaches the agent.
func TestSiemensDeploySecretsAreNamedByVariable(t *testing.T) {
	st := tiad.Station{CPU: tiad.CPU{ConfigurationProtection: tiad.ProtectPassword, AccessControl: tiad.AccessFullAccessUser},
		WebServer: &tiad.WebServer{Enabled: true, Users: []tiad.WebUser{{Name: "naut", Rights: []string{"read"}}, {Name: "ops-1", Rights: []string{"read"}}}}}
	_, err := siemensDeploySecrets(st, "naut", func(string) string { return "" })
	for _, want := range []string{"NAUTILUS_SIEMENS_CONFIG_PASSWORD", "NAUTILUS_SIEMENS_FULL_ACCESS_PASSWORD",
		"web user naut has no password: set NAUTILUS_SIEMENS_PASSWORD", "web user ops-1 has no password: set NAUTILUS_SIEMENS_WEB_PASSWORD_OPS_1"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	env := map[string]string{"NAUTILUS_SIEMENS_CONFIG_PASSWORD": "Config-Pass-1234", "NAUTILUS_SIEMENS_FULL_ACCESS_PASSWORD": "Full-Access-5678",
		"NAUTILUS_SIEMENS_PASSWORD": "Web-Naut-Pass-9", "NAUTILUS_SIEMENS_WEB_PASSWORD_OPS_1": "Web-Ops-Pass-10"}
	sec, err := siemensDeploySecrets(st, "naut", func(k string) string { return env[k] })
	if err != nil || sec.WebUsers["naut"] != "Web-Naut-Pass-9" || sec.WebUsers["ops-1"] != "Web-Ops-Pass-10" || sec.FullAccessPassword != "Full-Access-5678" {
		t.Fatalf("%+v %v", sec, err)
	}
}

// TestSiemensDeployStopsWhenTheDryRunLoaded: the dry run comes only after
// the operator's yes, and one TIA ran without a check to cancel at may
// have loaded the CPU (siemens-ide.md §3.5): the deploy compares on the
// session, says so and goes no further, with no real download.
func TestSiemensDeployStopsWhenTheDryRunLoaded(t *testing.T) {
	r := newDeployRig(t)
	out, code := r.run("yes\n", true, "--download", "--dry-run", "--allow-stop")
	if code != 1 || !strings.Contains(out, "dry run: OUTCOME UNKNOWN") || strings.Index(out, "Type yes") > strings.Index(out, "dry run: OUTCOME") ||
		strings.Contains(out, "downloading to") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// TestSiemensDeployAnswersOnlyWhatItWasAllowed: without --allow-stop and
// --reinit, the download says no to a stop and names no DB, so a check
// the dry run could not show is refused by TIA's answer, not given.
func TestSiemensDeployAnswersOnlyWhatItWasAllowed(t *testing.T) {
	r := newDeployRig(t)
	r.fake.SetQuestions([]tiad.Question{{ID: tiad.QuestionAlarmTexts, Message: "load the alarm texts"}})
	out, code := r.run("", false, "--download", "--yes", "--no-start")
	if code != 0 || !strings.Contains(out, "stop the CPU: no. If TIA asks, the download is cancelled there and loads nothing") ||
		!strings.Contains(out, "start the CPU after the load: no, it stays in STOP (--no-start)") ||
		!strings.Contains(out, "no dry run (--dry-run asks") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	dl := r.fake.Downloads()
	if len(dl) != 1 {
		t.Fatalf("downloads: %+v", dl)
	}
	d := dl[0].Request.Decisions
	if d.StopCPU == nil || *d.StopCPU || len(d.Reinitialize) != 0 || d.StartAfter == nil || *d.StartAfter ||
		d.AcceptUnverifiedHardware != nil || d.UserManagementResetToProject != nil || d.Overwrite != nil {
		t.Fatalf("decisions: %+v", d)
	}
}

// TestSiemensDeployInDoubtAfterAStop: a check the answers do not cover,
// asked after the stop they allowed, cancels the download in doubt; the
// deploy says TIA may have stopped the CPU and never says done.
func TestSiemensDeployInDoubtAfterAStop(t *testing.T) {
	r := newDeployRig(t)
	r.fake.SetQuestions([]tiad.Question{{ID: tiad.QuestionStopCPU, Message: "stop the CPU"},
		{ID: tiad.QuestionReinitialize, Message: "reinitialize", Objects: []string{"naut_Tags"}}})
	out, code := r.run("", false, "--download", "--yes", "--allow-stop")
	if code != 1 || !strings.Contains(out, "download: OUTCOME UNKNOWN") || !strings.Contains(out, "TIA may already have acted on: stop-cpu") ||
		strings.Contains(out, "saved projects/") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// TestSiemensDeployLoadsHardwareOnlyAttested: --hardware loads the
// hardware configuration with the operator's attestation and says it is
// unverified; a user-management reset is answered only with the observed
// access control.
func TestSiemensDeployLoadsHardwareOnlyAttested(t *testing.T) {
	r := newDeployRig(t)
	r.fake.SetQuestions([]tiad.Question{{ID: tiad.QuestionStopCPU, Message: "stop the CPU"}})
	out, code := r.run("", false, "--download", "--yes", "--allow-stop", "--hardware",
		"--attest-hardware=the CPU's hardware is what I intend to load", "--cpu-access-control=disabled")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"hardware: LOADED (--hardware), unverified",
		`load the HARDWARE configuration, UNVERIFIED (tiad cannot compare hardware online), replacing the CPU's system data; attested by test operator: "the CPU's hardware is what I intend to load"`,
		"user management: RESET the CPU's to the project's (deletes the CPU's users), resting on your observation that its access control is disabled now",
		"WARNING: the hardware configuration was loaded UNVERIFIED"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if !strings.Contains(out, "station: a new TIA project, built from the spec") {
		t.Errorf("the plan does not say the project is new:\n%s", out)
	}
	dl := r.fake.Downloads()
	if len(dl) != 1 {
		t.Fatalf("downloads: %+v", dl)
	}
	q := dl[0].Request
	if !q.Hardware || q.Attestation == nil || q.Attestation.By != "test operator" || q.Attestation.Text != "the CPU's hardware is what I intend to load" ||
		q.Attestation.CPUAccessControl != "disabled" || q.Decisions.AcceptUnverifiedHardware == nil || !*q.Decisions.AcceptUnverifiedHardware ||
		q.Decisions.Overwrite == nil || !*q.Decisions.Overwrite || q.Decisions.UserManagementResetToProject == nil || !*q.Decisions.UserManagementResetToProject {
		t.Fatalf("request: %+v", q)
	}
}

// TestSiemensDeployFlagsGoTogether: an acting flag without --download, a
// hardware load without the operator's statement, and an access control
// that is not one of the two words are refused before anything is read.
func TestSiemensDeployFlagsGoTogether(t *testing.T) {
	dl := func(o siemensDeployOptions) siemensDeployOptions { o.download, o.start = true, true; return o }
	for i, c := range []struct {
		o    siemensDeployOptions
		says string
	}{
		{siemensDeployOptions{yes: true}, "go with --download"},
		{siemensDeployOptions{dryRun: true}, "go with --download"},
		{siemensDeployOptions{allowStop: true}, "go with --download"},
		{siemensDeployOptions{start: true}, "go with --download"},
		{siemensDeployOptions{reinit: []string{"x"}}, "go with --download"},
		{siemensDeployOptions{acceptOutside: true}, "go with --download"},
		{siemensDeployOptions{download: true}, "--download needs --start or --no-start"},
		{siemensDeployOptions{download: true, start: true, noStart: true}, "--download needs --start or --no-start"},
		{dl(siemensDeployOptions{attest: "x"}), "go with --hardware"},
		{dl(siemensDeployOptions{cpuAccess: "disabled"}), "go with --hardware"},
		{dl(siemensDeployOptions{hardware: true}), "--hardware needs --attest-hardware"},
		{dl(siemensDeployOptions{hardware: true, attest: "  "}), "--hardware needs --attest-hardware"},
		{dl(siemensDeployOptions{hardware: true, attest: "x", cpuAccess: "off", by: "op"}), `--cpu-access-control "off"`},
		{dl(siemensDeployOptions{hardware: true, attest: strings.Repeat("x", tiad.MaxAttestationText+1), by: "op"}), "over 2000"},
		{dl(siemensDeployOptions{acceptOutside: true, by: " "}), "the operator's name"},
		{dl(siemensDeployOptions{hardware: true, attest: "x"}), "the operator's name"},
	} {
		if err := c.o.flagsError(); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("case %d: %v, want %q", i, err, c.says)
		}
	}
	for i, o := range []siemensDeployOptions{{}, dl(siemensDeployOptions{yes: true, dryRun: true, allowStop: true}),
		{download: true, noStart: true}, dl(siemensDeployOptions{hardware: true, attest: "x", cpuAccess: "enabled", by: "op"}),
		dl(siemensDeployOptions{acceptOutside: true, by: "op"})} {
		if err := o.flagsError(); err != nil {
			t.Errorf("ok case %d: %v", i, err)
		}
	}
}

// TestGitHeadNamesNoCommitForADirtyProject: the deploy record claims a
// commit only when the project's files are that commit.
func TestGitHeadNamesNoCommitForADirtyProject(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if gitHead(dir) != "" {
		t.Fatal("a directory outside git named a commit")
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "main.st"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "x")
	if c := gitHead(dir); len(c) != 40 {
		t.Fatalf("clean: %q", c)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.st"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := gitHead(dir); c != "" {
		t.Fatalf("dirty: %q", c)
	}
}

// TestSiemensDeployNeverDryRunsUnasked: without --dry-run nothing reaches
// the CPU before the confirmation. Here TIA asks no check, so a dry run
// would have loaded (and ended in doubt); the one download is the real one.
func TestSiemensDeployNeverDryRunsUnasked(t *testing.T) {
	r := newDeployRig(t)
	out, code := r.run("", false, "--download", "--yes", "--allow-stop")
	if code != 0 || strings.Contains(out, "OUTCOME UNKNOWN") || !strings.Contains(out, "no dry run (--dry-run asks") || len(r.fake.Downloads()) != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// TestSiemensDeployDownloadsOnATypedYes: the operator's typed yes is the
// confirmation, as --yes is.
func TestSiemensDeployDownloadsOnATypedYes(t *testing.T) {
	r := newDeployRig(t)
	out, code := r.run("yes\n", true, "--download", "--allow-stop")
	if code != 0 || !strings.Contains(out, "Type yes") || len(r.fake.Downloads()) != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// TestSiemensDeployInDoubtOnAnUnreadableReply: a download whose reply the
// client cannot read (a proxy's 502 after the agent loaded) is in doubt,
// never "nothing was loaded": the deploy compares on the session and
// saves nothing.
func TestSiemensDeployInDoubtOnAnUnreadableReply(t *testing.T) {
	r := newDeployRig(t)
	r.mangleDownload = true
	out, code := r.run("", false, "--download", "--yes", "--allow-stop")
	if code != 1 || !strings.Contains(out, "download: OUTCOME UNKNOWN") || strings.Contains(out, "nothing was loaded") ||
		!strings.Contains(out, "the compare after it is EQUAL") || strings.Contains(out, "saved projects/") || len(r.fake.Downloads()) != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// TestSiemensDeployRefusesABrokenRecord: the last deploy's archive is not
// the one its record names, so nothing says what the CPU runs: refused
// without --accept-outside-changes.
func TestSiemensDeployRefusesABrokenRecord(t *testing.T) {
	r := newDeployRig(t)
	if out, code := r.run("", false, "--download", "--yes", "--allow-stop"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	r.write("50ms")
	if out, code := r.run("", false, "--download", "--yes", "--allow-stop"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	list, err := tiad.New(r.url, r.fake.Token).Deploys(context.Background(), deployCPU)
	if err != nil || len(list.Deploys) < 2 {
		t.Fatalf("%+v %v", list, err)
	}
	r.fake.SwapArchive(list.Deploys[0].Archive.File, list.Deploys[1].Archive.File)
	out, code := r.run("", false, "--download", "--yes", "--allow-stop")
	if code != 1 || !strings.Contains(out, "its archive is broken") || !strings.Contains(out, "--accept-outside-changes") || len(r.fake.Downloads()) != 2 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	out, code = r.run("", false, "--download", "--yes", "--allow-stop", "--accept-outside-changes")
	dl := r.fake.Downloads()
	if code != 0 || len(dl) != 3 || dl[2].Request.OutsideChanges == nil ||
		*dl[2].Request.OutsideChanges != (tiad.OutsideChanges{By: "test operator", Verdict: tiad.OutsideBroken, Deploy: list.Deploys[0].ID}) {
		t.Fatalf("exit %d, request %+v:\n%s", code, dl[len(dl)-1].Request.OutsideChanges, out)
	}
}

// TestSiemensDeployPrintsNoSecret: no password reaches the output, the
// station record or a source in the agent's work dir.
func TestSiemensDeployPrintsNoSecret(t *testing.T) {
	r := newDeployRig(t)
	out, code := r.run("", false, "--download", "--yes", "--allow-stop", "--hardware", "--attest-hardware=as intended")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	raw, ok := r.fake.File(siemensStationRecord("s7_demo"))
	if !ok {
		t.Fatal("no station record")
	}
	for k, v := range r.env() {
		if k == "NAUTILUS_TIAD_TOKEN" {
			continue
		}
		if strings.Contains(out, v) || strings.Contains(string(raw), v) {
			t.Errorf("%s's value is in the output or the station record", k)
		}
		for _, f := range r.generated().Files {
			if b, _ := r.fake.File(siemensSourceDir + "/s7_demo/" + f.Path); strings.Contains(string(b), v) {
				t.Errorf("%s's value is in %s", k, f.Path)
			}
		}
	}
	if strings.Contains(out, r.fake.Token) {
		t.Error("the agent's token is in the output")
	}
}

// TestSiemensDeploySaysWhetherTheStationChanged: after a hardware load,
// the next plan says the spec is the one loaded, or that it changed and
// the hardware is not loaded unless asked.
func TestSiemensDeploySaysWhetherTheStationChanged(t *testing.T) {
	r := newDeployRig(t)
	r.fake.SetQuestions([]tiad.Question{{ID: tiad.QuestionStopCPU, Message: "stop the CPU"}})
	if out, code := r.run("", false, "--download", "--yes", "--allow-stop", "--hardware", "--attest-hardware=as intended"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	out, code := r.run("", false, "--download", "--allow-stop")
	if code != 1 || !strings.Contains(out, "station: the spec is the one the last hardware load loaded") || !strings.Contains(out, "  hardware: not loaded\n") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	m, _ := os.ReadFile(filepath.Join(r.dir, "nautilus.yaml"))
	if err := os.WriteFile(filepath.Join(r.dir, "nautilus.yaml"), []byte(strings.Replace(string(m), "subnet-mask: 255.255.255.0", "subnet-mask: 255.255.255.0, router: 192.0.2.1", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code = r.run("", false, "--download", "--allow-stop")
	if code != 1 || !strings.Contains(out, "station: the spec CHANGED since the last hardware load") ||
		!strings.Contains(out, "hardware: NOT loaded, although it may differ from the CPU's (the station spec changed since the last hardware load)") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// TestSiemensDeployAcceptsAnUnreadableJournalOnlyOnRecord: journal lines
// the agent cannot read may hide a newer deploy: refused without the
// accept, and the accept goes out as verdict unreadable, naming no deploy.
func TestSiemensDeployAcceptsAnUnreadableJournalOnlyOnRecord(t *testing.T) {
	r := newDeployRig(t)
	r.fake.SetUnreadableLines(1)
	out, code := r.run("", false, "--download", "--yes", "--allow-stop")
	if code != 1 || !strings.Contains(out, "1 journal line(s) the agent cannot read") || len(r.fake.Downloads()) != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	out, code = r.run("", false, "--download", "--yes", "--allow-stop", "--accept-outside-changes")
	dl := r.fake.Downloads()
	if code != 0 || len(dl) != 1 || dl[0].Request.OutsideChanges == nil ||
		*dl[0].Request.OutsideChanges != (tiad.OutsideChanges{By: "test operator", Verdict: tiad.OutsideUnreadable}) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
