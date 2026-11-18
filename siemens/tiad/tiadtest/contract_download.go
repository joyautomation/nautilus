package tiadtest

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The download half of the contract (siemens-ide.md §3.5): every
// question answered only from the request, refusals before TIA, and a
// dry run that loads nothing. Whether Openness can list the checks and
// cancel with nothing loaded is provisional until S2; this is the
// contract the agent aims for.

const otherCPU = "192.0.2.99"

// ResolveCases are the download rule (tiad.Resolve) as a table: TIA asks
// Questions, the request answers with Decisions (and a protection
// password or not), and the agent must refuse with the IDs in Unanswered
// (decision_required) or Declined (declined), or load.
var ResolveCases = []struct {
	Name                 string
	Questions            []tiad.Question
	Decisions            tiad.Decisions
	Protection           bool
	Unanswered, Declined string
}{
	{"nothing asked", nil, tiad.Decisions{}, false, "", ""},
	{"stop unanswered", []tiad.Question{qStop}, tiad.Decisions{}, false, "stop-cpu", ""},
	{"stop yes", []tiad.Question{qStop}, tiad.Decisions{StopCPU: tiad.Yes()}, false, "", ""},
	{"stop no", []tiad.Question{qStop}, tiad.Decisions{StopCPU: tiad.No()}, false, "", "stop-cpu"},
	{"overwrite unanswered", []tiad.Question{{ID: tiad.QuestionOverwrite}}, tiad.Decisions{StopCPU: tiad.Yes()}, false, "overwrite", ""},
	{"overwrite no", []tiad.Question{{ID: tiad.QuestionOverwrite}}, tiad.Decisions{Overwrite: tiad.No()}, false, "", "overwrite"},
	{"start-after no is stay in STOP", []tiad.Question{{ID: tiad.QuestionStartAfter}}, tiad.Decisions{StartAfter: tiad.No()}, false, "", ""},
	{"start-after unanswered", []tiad.Question{{ID: tiad.QuestionStartAfter}}, tiad.Decisions{}, false, "start-after", ""},
	{"reinit all agreed, any case", []tiad.Question{qReinit}, tiad.Decisions{Reinitialize: []string{"tags", "PROG_DB"}}, false, "", ""},
	{"reinit partly agreed", []tiad.Question{qReinit}, tiad.Decisions{Reinitialize: []string{"Tags"}}, false, "reinitialize", ""},
	{"reinit unanswered", []tiad.Question{qReinit}, tiad.Decisions{}, false, "reinitialize", ""},
	{"reinit naming nothing is never answered", []tiad.Question{{ID: tiad.QuestionReinitialize}}, tiad.Decisions{Reinitialize: []string{"Tags"}}, false, "reinitialize", ""},
	{"cert matches, any case", []tiad.Question{qCert}, tiad.Decisions{TrustCertificate: "ab12"}, false, "", ""},
	{"cert differs", []tiad.Question{qCert}, tiad.Decisions{TrustCertificate: "FFFF"}, false, "", "certificate"},
	{"cert unanswered", []tiad.Question{qCert}, tiad.Decisions{}, false, "certificate", ""},
	{"password from secret", []tiad.Question{{ID: tiad.QuestionPassword}}, tiad.Decisions{}, true, "", ""},
	{"password missing", []tiad.Question{{ID: tiad.QuestionPassword}}, tiad.Decisions{}, false, "password", ""},
	{"other never answered", []tiad.Question{{ID: tiad.QuestionOther, Message: "Something new"}}, tiad.Decisions{StopCPU: tiad.Yes(), Overwrite: tiad.Yes(), StartAfter: tiad.Yes()}, true, "other", ""},
	{"unknown id is other", []tiad.Question{{ID: "new-in-v22"}}, tiad.Decisions{StopCPU: tiad.Yes()}, true, "new-in-v22", ""},
	{"unanswered wins over declined", []tiad.Question{qStop, {ID: tiad.QuestionOverwrite}}, tiad.Decisions{StopCPU: tiad.No()}, false, "overwrite", "stop-cpu"},
	{"all answered", []tiad.Question{qStop, qReinit, {ID: tiad.QuestionOverwrite}, {ID: tiad.QuestionStartAfter}, qCert, {ID: tiad.QuestionPassword}},
		tiad.Decisions{StopCPU: tiad.Yes(), Overwrite: tiad.Yes(), StartAfter: tiad.Yes(), Reinitialize: []string{"Tags", "Prog_DB"}, TrustCertificate: "AB12"}, true, "", ""},
}

var (
	qStop   = tiad.Question{ID: tiad.QuestionStopCPU, Message: "The CPU must stop"}
	qReinit = tiad.Question{ID: tiad.QuestionReinitialize, Message: "Reinitialise", Objects: []string{"Tags", "Prog_DB"}}
	qCert   = tiad.Question{ID: tiad.QuestionCertificate, Detail: "AB12"}
)

func contractDownload(t *testing.T, start Starter) {
	t.Run("DownloadFlow", func(t *testing.T) { contractDownloadFlow(t, start) })
	t.Run("DownloadRules", func(t *testing.T) { contractDownloadRules(t, start) })
	t.Run("Resolve", func(t *testing.T) { contractResolve(t, start) })
	t.Run("DownloadRequest", func(t *testing.T) { contractDownloadRequest(t, start) })
	t.Run("DownloadPrecedence", func(t *testing.T) { contractDownloadPrecedence(t, start) })
	t.Run("Compare", func(t *testing.T) { contractCompare(t, start) })
	t.Run("ExportUploadSave", func(t *testing.T) { contractExportUploadSave(t, start) })
}

// compiledBench is an agent with the bench project created, its program
// imported and fully compiled: ready to download.
func compiledBench(t *testing.T, start Starter, targets ...string) (Target, *tiad.Session) {
	t.Helper()
	if targets == nil {
		targets = []string{benchCPU}
	}
	a := start(t, Config{Targets: targets})
	c := a.Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "bench", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutFile(ctx, "src/Main.scl", []byte(programSCL)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}, Prune: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	return a, s
}

func questionIDs(qs []tiad.Question) string {
	var s []string
	for _, q := range qs {
		s = append(s, q.ID)
	}
	return strings.Join(s, ",")
}

// contractDownloadFlow walks PROFINET's P1-P3 and P5: a dry run, a real
// download into RUN, compare, a changed module, and again.
func contractDownloadFlow(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	a.SetQuestions(t, []tiad.Question{qStop, {ID: tiad.QuestionStartAfter, Message: "Start the CPU after loading"}})
	req := tiad.DownloadRequest{Address: benchCPU, Hardware: true, Software: true, Announced: true,
		Decisions: tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.Yes()}, Secrets: &tiad.Secrets{Protection: "protection-must-not-be-recorded"}}

	dry := req
	dry.DryRun, dry.Announced, dry.Decisions = true, false, tiad.Decisions{}
	dr, err := s.Download(ctx, dry)
	if err != nil || !dr.DryRun || dr.Downloaded || questionIDs(dr.Questions) != "stop-cpu,start-after" || dr.Questions[0].Message != "The CPU must stop" {
		t.Fatalf("dry run: %+v %v", dr, err)
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("a dry run loaded %+v", l)
	}
	if cmp, err := s.Compare(ctx); err != nil || cmp.Equal {
		t.Fatalf("compare before any download: %+v %v", cmp, err)
	}
	res, err := s.Download(ctx, req)
	if err != nil || !res.Downloaded || res.DryRun || res.State != "RUN" || len(res.Questions) != 2 {
		t.Fatalf("download: %+v %v", res, err)
	}
	if cmp, err := s.Compare(ctx); err != nil || !cmp.Equal || len(cmp.Differences) != 0 {
		t.Fatalf("compare after the download: %+v %v", cmp, err)
	}
	want := []Loaded{{Address: benchCPU, Hardware: true, Software: true, State: "RUN"}}
	if l := a.Loaded(t); !reflect.DeepEqual(l, want) {
		t.Fatalf("loaded %+v, want %+v", l, want)
	}

	// P5: a changed module differs from the CPU, must compile before it
	// downloads, and is equal again after.
	mod := tiad.DeviceModule{Slot: 1, Subslot: 1, Module: "ID_Mod_In8Out8"}
	if _, err := s.ApplyStation(ctx, BenchStation(mod), BenchSecrets); err != nil {
		t.Fatal(err)
	}
	if cmp, _ := s.Compare(ctx); cmp.Equal {
		t.Fatal("compare is equal after a station change")
	}
	if _, err := s.Download(ctx, req); tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "compile") {
		t.Fatalf("download after a change, before compiling: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if cmp, _ := s.Compare(ctx); cmp.Equal {
		t.Fatal("compare is equal after compiling a change that was never downloaded")
	}
	a.SetQuestions(t, nil)
	if res, err := s.Download(ctx, req); err != nil || !res.Downloaded || res.State != "RUN" {
		t.Fatalf("second download: %+v %v", res, err)
	}
	if cmp, _ := s.Compare(ctx); !cmp.Equal {
		t.Fatal("compare after the second download")
	}
	if n := len(a.Loaded(t)); n != 2 {
		t.Fatalf("loaded %d times, want 2", n)
	}
	// A compile only of the software does not make a changed project
	// downloadable.
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, false, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, req); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("download after a software-only compile: %v", err)
	}
}

func contractDownloadRules(t *testing.T, start Starter) {
	a, s := compiledBench(t, start, benchCPU, otherCPU)
	a.SetQuestions(t, []tiad.Question{qStop, {ID: tiad.QuestionReinitialize, Message: "reinit", Objects: []string{"Main_DB"}}})
	base := tiad.DownloadRequest{Address: benchCPU, Software: true, Announced: true}

	_, err := s.Download(ctx, base)
	if tiad.Kind(err) != tiad.KindDecisionRequired || questionIDs(tiad.Questions(err)) != "stop-cpu,reinitialize" {
		t.Fatalf("no decisions: %v %+v", err, tiad.Questions(err))
	}
	no := base
	no.Decisions = tiad.Decisions{StopCPU: tiad.No(), Reinitialize: []string{"Main_DB"}}
	if _, err := s.Download(ctx, no); tiad.Kind(err) != tiad.KindDeclined || questionIDs(tiad.Questions(err)) != "stop-cpu" {
		t.Fatalf("declined stop: %v", err)
	}
	for _, addr := range []string{"192.0.2.77", "192.0.2.1"} {
		other := base
		other.Address = addr
		if _, err := s.Download(ctx, other); tiad.Kind(err) != tiad.KindTargetRefused {
			t.Fatalf("a CPU not in TIAD_TARGETS (%s): %v", addr, err)
		}
	}
	other := base
	other.Address = otherCPU
	if _, err := s.Download(ctx, other); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "one station spec per CPU") {
		t.Fatalf("a listed CPU that is not the station's: %v", err)
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("refusals loaded %+v", l)
	}
	yes := base
	yes.Decisions = tiad.Decisions{StopCPU: tiad.Yes(), Reinitialize: []string{"main_db"}}
	if r, err := s.Download(ctx, yes); err != nil || r.State != "STOP" {
		t.Fatalf("stopped without startAfter stays in STOP: %+v %v", r, err)
	}
	a.SetQuestions(t, []tiad.Question{qStop, {ID: tiad.QuestionStartAfter}})
	stay := base
	stay.Decisions = tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.No()}
	if r, err := s.Download(ctx, stay); err != nil || !r.Downloaded || r.State != "STOP" {
		t.Fatalf("load and stay in STOP: %+v %v", r, err)
	}
	if n := len(a.Loaded(t)); n != 2 {
		t.Fatalf("loaded %d, want 2", n)
	}
}

// contractResolve runs ResolveCases over the wire, past the client.
func contractResolve(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	path := "/v1/sessions/" + s.ID + "/download"
	c := a.Client()
	loads := 0
	for _, rc := range ResolveCases {
		a.SetQuestions(t, rc.Questions)
		body := map[string]any{"address": benchCPU, "software": true, "announced": true, "decisions": rc.Decisions}
		if rc.Protection {
			body["secrets"] = map[string]string{"protection": "pw"}
		}
		status, b := rawJSON(t, c, "POST", path, body)
		var env struct {
			Data struct {
				Downloaded bool            `json:"downloaded"`
				Questions  []tiad.Question `json:"questions"`
			} `json:"data"`
		}
		_ = json.Unmarshal(b, &env)
		switch {
		case rc.Unanswered != "":
			if status != http.StatusConflict || envelopeKind(b) != tiad.KindDecisionRequired || questionIDs(env.Data.Questions) != rc.Unanswered {
				t.Errorf("%s: HTTP %d %s, want 409 decision_required %s", rc.Name, status, b, rc.Unanswered)
			}
		case rc.Declined != "":
			if status != http.StatusConflict || envelopeKind(b) != tiad.KindDeclined || questionIDs(env.Data.Questions) != rc.Declined {
				t.Errorf("%s: HTTP %d %s, want 409 declined %s", rc.Name, status, b, rc.Declined)
			}
		default:
			loads++
			if status != http.StatusOK || !env.Data.Downloaded {
				t.Errorf("%s: HTTP %d %s, want a download", rc.Name, status, b)
			}
		}
		if n := len(a.Loaded(t)); n != loads {
			t.Fatalf("%s: %d downloads loaded, want %d (a refusal must load nothing)", rc.Name, n, loads)
		}
	}
	// A partly agreed reinitialize names the DBs not agreed.
	a.SetQuestions(t, []tiad.Question{qReinit})
	_, b := rawJSON(t, c, "POST", path, map[string]any{"address": benchCPU, "software": true, "announced": true,
		"decisions": map[string]any{"reinitialize": []string{"tags"}}})
	if !strings.Contains(string(b), "(not agreed: Prog_DB)") {
		t.Errorf("a partly agreed reinitialize: %s", b)
	}
}

func contractDownloadRequest(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	path := "/v1/sessions/" + s.ID + "/download"
	refused := []struct {
		name string
		body any
		want string
	}{
		{"not announced", map[string]any{"address": benchCPU, "software": true}, "must be announced"},
		{"nothing selected", map[string]any{"address": benchCPU, "announced": true}, "neither hardware nor software"},
		{"no address", map[string]any{"software": true, "announced": true}, "not an IPv4 address"},
		{"a name for an address", map[string]any{"address": "plc-1", "software": true, "announced": true}, "not an IPv4 address"},
		{"a short address", map[string]any{"address": "192.0.2", "software": true, "announced": true}, "not an IPv4 address"},
		{"announced as a string", map[string]any{"address": benchCPU, "software": true, "announced": "true"}, ""},
		{"a decision as a string", map[string]any{"address": benchCPU, "software": true, "announced": true, "decisions": map[string]any{"stopCpu": "yes"}}, ""},
		{"an unknown decision", map[string]any{"address": benchCPU, "software": true, "announced": true, "decisions": map[string]any{"stopCPU": true}}, "unknown field"},
		{"an unknown field", map[string]any{"address": benchCPU, "software": true, "announced": true, "force": true}, "unknown field"},
	}
	for _, rc := range refused {
		status, b := rawJSON(t, c, "POST", path, rc.body)
		if status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest || !strings.Contains(errMessage(b), rc.want) {
			t.Errorf("%s: HTTP %d %s, want 400 %q", rc.name, status, b, rc.want)
		}
	}
	// A dry run needs no announcement, and null decisions are no answers.
	a.SetQuestions(t, []tiad.Question{qStop})
	status, b := rawJSON(t, c, "POST", path, map[string]any{"address": benchCPU, "software": true, "dryRun": true})
	if status != http.StatusOK {
		t.Fatalf("an unannounced dry run: HTTP %d %s", status, b)
	}
	status, b = rawJSON(t, c, "POST", path, map[string]any{"address": benchCPU, "software": true, "announced": true, "decisions": map[string]any{"stopCpu": nil}})
	if status != http.StatusConflict || envelopeKind(b) != tiad.KindDecisionRequired {
		t.Fatalf("a null decision: HTTP %d %s, want decision_required", status, b)
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("refusals loaded %+v", l)
	}
}

// contractDownloadPrecedence: a download wrong in two ways fails the way
// the earlier check says, and every one of them loads nothing.
func contractDownloadPrecedence(t *testing.T, start Starter) {
	a, s := compiledBench(t, start, benchCPU, otherCPU)
	c := a.Client()
	a.SetQuestions(t, []tiad.Question{qStop})
	path := "/v1/sessions/" + s.ID + "/download"
	req := func(addr string, announced bool) map[string]any {
		return map[string]any{"address": addr, "software": true, "announced": announced}
	}
	check := func(name string, body any, status int, kind, msg string) {
		t.Helper()
		got, b := rawJSON(t, c, "POST", path, body)
		if got != status || envelopeKind(b) != kind || !strings.Contains(errMessage(b), msg) {
			t.Errorf("%s: HTTP %d %s, want %d %s %q", name, got, b, status, kind, msg)
		}
	}
	check("the request before the target", req("192.0.2.77", false), 400, tiad.KindBadRequest, "announced")
	check("the target before the station", req("192.0.2.77", true), 403, tiad.KindTargetRefused, "TIAD_TARGETS")
	// Not compiled: change the project.
	if _, err := s.ApplyStation(ctx, BenchStation(tiad.DeviceModule{Slot: 1, Subslot: 1, Module: "M"}), BenchSecrets); err != nil {
		t.Fatal(err)
	}
	check("the station before the compile", req(otherCPU, true), 400, tiad.KindBadRequest, "one station spec per CPU")
	check("the compile before the questions", req(benchCPU, true), 500, tiad.KindOperationFailed, "compile")
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("refusals loaded %+v", l)
	}
	// A project opened from a path has no station spec to download.
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	o, err := c.Open(ctx, "projects/bench/bench.ap21")
	if err != nil {
		t.Fatal(err)
	}
	got, b := rawJSON(t, c, "POST", "/v1/sessions/"+o.ID+"/download", req(benchCPU, true))
	if got != http.StatusBadRequest || !strings.Contains(errMessage(b), "no station spec") {
		t.Errorf("download without a station: HTTP %d %s", got, b)
	}
	for _, ex := range []tiad.ExportRequest{{Dir: "out", Hardware: true}, {Dir: "out", GSDML: true}} {
		if _, err := o.Export(ctx, ex); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "no station spec") {
			t.Errorf("export %+v without a station: %v", ex, err)
		}
	}
	if err := o.Upload(ctx); tiad.Kind(err) != tiad.KindTargetRefused {
		t.Errorf("upload without a station: %v", err)
	}
}

func contractCompare(t *testing.T, start Starter) {
	_, s := compiledBench(t, start)
	if cmp, err := s.Compare(ctx); err != nil || cmp.Equal || len(cmp.Differences) == 0 || cmp.Differences[0].Object != "PLC_1" {
		t.Fatalf("compare before a download: %+v %v", cmp, err)
	}
	// A CPU not in TIAD_TARGETS is never gone online to.
	a2 := start(t, Config{})
	c2 := a2.Client()
	installGSD(t, c2)
	s2, err := c2.CreateProject(ctx, "p", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Compare(ctx); tiad.Kind(err) != tiad.KindTargetRefused {
		t.Fatalf("compare with an unlisted CPU: %v", err)
	}
	if err := s2.Upload(ctx); tiad.Kind(err) != tiad.KindTargetRefused {
		t.Fatalf("upload from an unlisted CPU: %v", err)
	}
}

func contractExportUploadSave(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	ex, err := s.Export(ctx, tiad.ExportRequest{Dir: "out", Blocks: "scl", TagTables: true, Hardware: true, GSDML: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"out/Main.scl", "out/Main_DB.scl", "out/tagtables.xml", "out/station.json", "out/GSDML-V2.4-fake-PLC_1.xml"}
	if !reflect.DeepEqual(ex.Files, want) {
		t.Fatalf("export = %v, want %v", ex.Files, want)
	}
	b, err := c.GetFile(ctx, "out/station.json")
	var st tiad.Station
	if err != nil || json.Unmarshal(b, &st) != nil || !reflect.DeepEqual(st, BenchStation()) {
		t.Fatalf("exported station: %s %v", b, err)
	}
	if ex, err := s.Export(ctx, tiad.ExportRequest{Dir: "out2"}); err != nil || len(ex.Files) != 0 || ex.Files == nil {
		t.Fatalf("an empty export: %+v %v", ex, err)
	}
	if _, err := s.Export(ctx, tiad.ExportRequest{Dir: "out", Blocks: "pdf"}); tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("blocks pdf: %v", err)
	}
	for _, p := range append(append([]string{}, RefusedPaths...), "../out") {
		status, b := rawJSON(t, c, "POST", "/v1/sessions/"+s.ID+"/export", map[string]any{"dir": p, "blocks": "scl"})
		if status != http.StatusBadRequest {
			t.Errorf("export to %q: HTTP %d %s", p, status, b)
		}
	}
	if err := s.Upload(ctx); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if _, err := s.Download(ctx, tiad.DownloadRequest{Address: benchCPU, Software: true, Announced: true}); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("download after an upload changed the project, before compiling: %v", err)
	}
	if p, err := s.Save(ctx, false); err != nil || p != "projects/bench/bench.ap21" {
		t.Fatalf("save: %q %v", p, err)
	}
	if p, err := s.Save(ctx, true); err != nil || p != "projects/bench/bench.zap21" {
		t.Fatalf("archive: %q %v", p, err)
	}
	if _, err := c.GetFile(ctx, "projects/bench/bench.zap21"); err != nil {
		t.Fatalf("the archive is not in the work dir: %v", err)
	}
}
