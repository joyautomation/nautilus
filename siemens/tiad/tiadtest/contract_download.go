package tiadtest

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The download half of the contract (siemens-ide.md §3.5): every
// question answered only from the request, refusals before TIA, and a
// dry run that loads nothing. TIA asks its checks one at a time and lists
// them nowhere (#789 S2b): each is answered on its own, the first not
// answered yes cancels there, a dry run cancels at the first, and a
// cancel after an acting answer (tiad.Acting) is outcome_unknown. That a
// cancel loads nothing is provisional until S2; this is the contract the
// agent aims for.

const otherCPU = "192.0.2.99"

// ResolveCases are the download rule (tiad.Resolve) as a table: TIA asks
// Questions, the request answers with Decisions (and a protection
// password or not), and the agent must refuse with the IDs in Unanswered
// (decision_required) or Declined (declined), or load.
var ResolveCases = []struct {
	Name       string
	Questions  []tiad.Question
	Decisions  tiad.Decisions
	Protection bool
	// ConfigurationPassword sends the configuration password.
	ConfigurationPassword bool
	Unanswered, Declined  string
}{
	{"nothing asked", nil, tiad.Decisions{}, false, false, "", ""},
	{"stop unanswered", []tiad.Question{qStop}, tiad.Decisions{}, false, false, "stop-cpu", ""},
	{"stop yes", []tiad.Question{qStop}, tiad.Decisions{StopCPU: tiad.Yes()}, false, false, "", ""},
	{"stop no", []tiad.Question{qStop}, tiad.Decisions{StopCPU: tiad.No()}, false, false, "", "stop-cpu"},
	{"overwrite unanswered", []tiad.Question{{ID: tiad.QuestionOverwrite}}, tiad.Decisions{StopCPU: tiad.Yes()}, false, false, "overwrite", ""},
	{"overwrite no", []tiad.Question{{ID: tiad.QuestionOverwrite}}, tiad.Decisions{Overwrite: tiad.No()}, false, false, "", "overwrite"},
	// TIA's ConsistentBlocksDownload: asked after stop-cpu by a real
	// software download (#789 walk-past window). Only its own decision
	// answers it, never overwrite.
	{"consistent-download unanswered", []tiad.Question{{ID: tiad.QuestionConsistentDownload}}, tiad.Decisions{StopCPU: tiad.Yes(), Overwrite: tiad.Yes()}, false, false, "consistent-download", ""},
	{"consistent-download no", []tiad.Question{{ID: tiad.QuestionConsistentDownload}}, tiad.Decisions{ConsistentDownload: tiad.No()}, false, false, "", "consistent-download"},
	{"stop, then consistent-download yes", []tiad.Question{qStop, {ID: tiad.QuestionConsistentDownload}, {ID: tiad.QuestionStartAfter}},
		tiad.Decisions{StopCPU: tiad.Yes(), ConsistentDownload: tiad.Yes(), StartAfter: tiad.Yes()}, false, false, "", ""},
	// The rest of the software-load family (#789 restart window): each
	// only by its own decision. The bench spec sets access control
	// (fullAccessUser), so keeping the CPU's users is refused there.
	{"alarm-texts unanswered", []tiad.Question{{ID: tiad.QuestionAlarmTexts}}, tiad.Decisions{ConsistentDownload: tiad.Yes(), AllBlocks: tiad.Yes()}, false, false, "alarm-texts", ""},
	{"alarm-texts yes", []tiad.Question{{ID: tiad.QuestionAlarmTexts}}, tiad.Decisions{AlarmTexts: tiad.Yes()}, false, false, "", ""},
	{"all-blocks unanswered", []tiad.Question{{ID: tiad.QuestionAllBlocks}}, tiad.Decisions{ConsistentDownload: tiad.Yes(), AlarmTexts: tiad.Yes()}, false, false, "all-blocks", ""},
	{"all-blocks no", []tiad.Question{{ID: tiad.QuestionAllBlocks}}, tiad.Decisions{AllBlocks: tiad.No()}, false, false, "", "all-blocks"},
	{"identification-data load nothing", []tiad.Question{{ID: tiad.QuestionIdentificationData}}, tiad.Decisions{IdentificationDataLoadNothing: tiad.Yes()}, false, false, "", ""},
	{"identification-data unanswered", []tiad.Question{{ID: tiad.QuestionIdentificationData}}, tiad.Decisions{StopCPU: tiad.Yes()}, false, false, "identification-data", ""},
	{"user-management keep refused when the spec sets access control", []tiad.Question{{ID: tiad.QuestionUserManagement}}, tiad.Decisions{UserManagementKeepOnline: tiad.Yes()}, false, false, "user-management", ""},
	{"user-management no", []tiad.Question{{ID: tiad.QuestionUserManagement}}, tiad.Decisions{UserManagementKeepOnline: tiad.No()}, false, false, "", "user-management"},
	// Resetting it to the project's: a software download is refused before
	// TIA (CheckUserManagementReset: it loads no hardware); a no declines,
	// keep's or reset's.
	{"user-management reset refused", []tiad.Question{{ID: tiad.QuestionUserManagement}}, tiad.Decisions{UserManagementResetToProject: tiad.Yes()}, false, false, "user-management", ""},
	{"user-management reset no", []tiad.Question{{ID: tiad.QuestionUserManagement}}, tiad.Decisions{UserManagementResetToProject: tiad.No()}, false, false, "", "user-management"},
	{"user-management keep no, reset no", []tiad.Question{{ID: tiad.QuestionUserManagement}}, tiad.Decisions{UserManagementKeepOnline: tiad.No(), UserManagementResetToProject: tiad.No()}, false, false, "", "user-management"},
	{"start-after no is stay in STOP", []tiad.Question{{ID: tiad.QuestionStartAfter}}, tiad.Decisions{StartAfter: tiad.No()}, false, false, "", ""},
	{"start-after unanswered", []tiad.Question{{ID: tiad.QuestionStartAfter}}, tiad.Decisions{}, false, false, "start-after", ""},
	{"reinit all agreed, any case", []tiad.Question{qReinit}, tiad.Decisions{Reinitialize: []string{"tags", "PROG_DB"}}, false, false, "", ""},
	{"reinit partly agreed", []tiad.Question{qReinit}, tiad.Decisions{Reinitialize: []string{"Tags"}}, false, false, "reinitialize", ""},
	{"reinit unanswered", []tiad.Question{qReinit}, tiad.Decisions{}, false, false, "reinitialize", ""},
	{"reinit naming nothing is never answered", []tiad.Question{{ID: tiad.QuestionReinitialize}}, tiad.Decisions{Reinitialize: []string{"Tags"}}, false, false, "reinitialize", ""},
	{"cert matches, any case", []tiad.Question{qCert}, tiad.Decisions{TrustCertificate: "ab12"}, false, false, "", ""},
	{"cert differs", []tiad.Question{qCert}, tiad.Decisions{TrustCertificate: "FFFF"}, false, false, "", "certificate"},
	{"cert unanswered", []tiad.Question{qCert}, tiad.Decisions{}, false, false, "certificate", ""},
	{"password from secret", []tiad.Question{{ID: tiad.QuestionPassword}}, tiad.Decisions{}, true, false, "", ""},
	{"password missing", []tiad.Question{{ID: tiad.QuestionPassword}}, tiad.Decisions{}, false, false, "password", ""},
	{"configuration password from its secret", []tiad.Question{qConfigPw}, tiad.Decisions{}, false, true, "", ""},
	{"configuration password missing", []tiad.Question{qConfigPw}, tiad.Decisions{}, false, false, "configuration-password", ""},
	// Each password answers its own question only, never the other's.
	{"protection password never answers configuration-password", []tiad.Question{qConfigPw}, tiad.Decisions{}, true, false, "configuration-password", ""},
	{"configuration password never answers password", []tiad.Question{{ID: tiad.QuestionPassword}}, tiad.Decisions{}, false, true, "password", ""},
	{"other never answered", []tiad.Question{{ID: tiad.QuestionOther, Message: "Something new"}}, tiad.Decisions{StopCPU: tiad.Yes(), Overwrite: tiad.Yes(), StartAfter: tiad.Yes()}, true, false, "other", ""},
	{"an unknown check is other", []tiad.Question{{ID: "new-in-v22"}}, tiad.Decisions{StopCPU: tiad.Yes()}, true, false, "other", ""},
	// TIA asks one check at a time: the first not answered yes decides.
	{"the first check decides: declined", []tiad.Question{qStop, {ID: tiad.QuestionOverwrite}}, tiad.Decisions{StopCPU: tiad.No()}, false, false, "", "stop-cpu"},
	{"the first check decides: unanswered", []tiad.Question{{ID: tiad.QuestionOverwrite}, qStop}, tiad.Decisions{StopCPU: tiad.No()}, false, false, "overwrite", ""},
	{"a password first, then a refusal", []tiad.Question{{ID: tiad.QuestionPassword}, qStop}, tiad.Decisions{StopCPU: tiad.No()}, true, false, "", "stop-cpu"},
	// Start-after answered no changes nothing, so a refusal after it is
	// still clean.
	{"start-after no first, then a refusal", []tiad.Question{{ID: tiad.QuestionStartAfter}, {ID: tiad.QuestionOverwrite}}, tiad.Decisions{StartAfter: tiad.No()}, false, false, "overwrite", ""},
	{"all answered", []tiad.Question{qStop, qReinit, {ID: tiad.QuestionOverwrite}, {ID: tiad.QuestionStartAfter}, qCert, {ID: tiad.QuestionPassword}},
		tiad.Decisions{StopCPU: tiad.Yes(), Overwrite: tiad.Yes(), StartAfter: tiad.Yes(), Reinitialize: []string{"Tags", "Prog_DB"}, TrustCertificate: "AB12"}, true, false, "", ""},
}

var (
	qStop   = tiad.Question{ID: tiad.QuestionStopCPU, Message: "The CPU must stop"}
	qReinit = tiad.Question{ID: tiad.QuestionReinitialize, Message: "Reinitialise", Objects: []string{"Tags", "Prog_DB"}}
	qCert   = tiad.Question{ID: tiad.QuestionCertificate, Detail: "AB12"}
	// qConfigPw is TIA's PlcMasterSecretPassword check.
	qConfigPw = tiad.Question{ID: tiad.QuestionConfigurationPassword, Message: "password for the confidential PLC configuration data", Detail: "PlcMasterSecretPassword"}
)

func contractDownload(t *testing.T, start Starter) {
	t.Run("DownloadFlow", func(t *testing.T) { contractDownloadFlow(t, start) })
	t.Run("DownloadRules", func(t *testing.T) { contractDownloadRules(t, start) })
	t.Run("Resolve", func(t *testing.T) { contractResolve(t, start) })
	t.Run("DownloadRequest", func(t *testing.T) { contractDownloadRequest(t, start) })
	t.Run("DownloadPrecedence", func(t *testing.T) { contractDownloadPrecedence(t, start) })
	t.Run("Compare", func(t *testing.T) { contractCompare(t, start) })
	t.Run("ExportUploadSave", func(t *testing.T) { contractExportUploadSave(t, start) })
	t.Run("S2aSequence", func(t *testing.T) { contractS2aSequence(t, start) })
	t.Run("OneCPUOneProject", func(t *testing.T) { contractOneCPU(t, start) })
}

// compiledBench is an agent with the bench project created, its program
// imported and fully compiled: ready to download.
func compiledBench(t *testing.T, start Starter, targets ...string) (Target, *tiad.Session) {
	t.Helper()
	return compiledBenchWith(t, start, Config{Targets: targets})
}

// compiledBenchWith is compiledBench on an agent started with cfg (its
// Targets the bench CPU when empty).
func compiledBenchWith(t *testing.T, start Starter, cfg Config) (Target, *tiad.Session) {
	t.Helper()
	if cfg.Targets == nil {
		cfg.Targets = []string{benchCPU}
	}
	a := start(t, cfg)
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

// contractHardwareGate: a real download that loads hardware is refused,
// before anything reaches the CPU, until the request opts in to loading
// it unverified and carries an attestation saying who and what (#789
// hardware gate). req loads hardware and answers everything else.
func contractHardwareGate(t *testing.T, a Target, s *tiad.Session, req tiad.DownloadRequest) {
	t.Helper()
	att := BenchAttestation
	for _, c := range []struct {
		name   string
		accept *bool
		att    *tiad.Attestation
		kind   string
		says   string
	}{
		{"no opt-in", nil, &att, tiad.KindDecisionRequired, "set decisions.acceptUnverifiedHardware"},
		{"opt-in, no attestation", tiad.Yes(), nil, tiad.KindDecisionRequired, "needs the operator's attestation"},
		{"opt-in, no who", tiad.Yes(), &tiad.Attestation{By: " ", Text: att.Text}, tiad.KindDecisionRequired, "needs the operator's attestation"},
		{"opt-in, no text", tiad.Yes(), &tiad.Attestation{By: att.By, Text: "\t"}, tiad.KindDecisionRequired, "needs the operator's attestation"},
		{"opt-in no", tiad.No(), &att, tiad.KindDeclined, "download the software only"},
	} {
		r := req
		r.Decisions.AcceptUnverifiedHardware, r.Attestation = c.accept, c.att
		_, err := s.Download(ctx, r)
		if tiad.Kind(err) != c.kind || !strings.Contains(err.Error(), c.says) || !strings.Contains(err.Error(), "nothing was sent to the CPU") ||
			questionIDs(tiad.Questions(err)) != tiad.QuestionUnverifiedHardware || len(tiad.ActingAnswers(err)) != 0 {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	// Too long to journal is a bad request, whatever else it says, from
	// the agent itself (the client checks first, so the body goes raw).
	// The limits are bytes: 1001 two-byte runes are over 2000.
	for _, long := range []tiad.Attestation{{By: strings.Repeat("\u00e9", tiad.MaxAttestationBy/2+1), Text: att.Text}, {By: att.By, Text: strings.Repeat("\u00e9", tiad.MaxAttestationText/2+1)}} {
		r := AcceptHardware(req)
		r.Attestation = &long
		if _, err := s.Download(ctx, r); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "bytes") {
			t.Fatalf("an attestation over the limit, client side: %v", err)
		}
		status, b := rawJSON(t, a.Client(), "POST", "/v1/sessions/"+s.ID+"/download", r)
		if status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest || !strings.Contains(string(b), "bytes") {
			t.Fatalf("an attestation over the limit: HTTP %d %s", status, b)
		}
	}
	// The time is tiad's alone: a request cannot send one.
	body := map[string]any{"address": req.Address, "hardware": true, "software": true, "announced": true,
		"decisions":   map[string]any{"acceptUnverifiedHardware": true, "stopCpu": true, "startAfter": true},
		"attestation": map[string]string{"by": att.By, "text": att.Text, "at": "2020-01-01T00:00:00.000Z"}}
	if status, b := rawJSON(t, a.Client(), "POST", "/v1/sessions/"+s.ID+"/download", body); status != http.StatusBadRequest || !strings.Contains(string(b), "unknown field") {
		t.Fatalf("an attestation with its own time: HTTP %d %s", status, b)
	}
	// At the limits it is taken (refused later only for start-after).
	r := AcceptHardware(req)
	r.Attestation = &tiad.Attestation{By: strings.Repeat("b", tiad.MaxAttestationBy), Text: strings.Repeat("\u00e9", tiad.MaxAttestationText/2)}
	r.Decisions.StartAfter = nil
	if _, err := s.Download(ctx, r); tiad.Kind(err) != tiad.KindDecisionRequired || !strings.Contains(err.Error(), "startAfter") {
		t.Fatalf("an attestation at the limits: %v", err)
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("the hardware gate let a download load: %+v", l)
	}
}

// contractDownloadFlow walks PROFINET's P1-P3 and P5: a dry run, a real
// download into RUN, compare, a changed module, and again.
func contractDownloadFlow(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	a.SetQuestions(t, []tiad.Question{qStop, {ID: tiad.QuestionStartAfter, Message: "Start the CPU after loading"}})
	req := tiad.DownloadRequest{Address: benchCPU, Hardware: true, Software: true, Announced: true,
		Decisions: tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.Yes()}, Secrets: &tiad.Secrets{Protection: "protection-must-not-be-recorded", FullAccessPassword: BenchFullAccessPassword}}

	// A dry run cancels at TIA's first check, so it sees only that one.
	dry := req
	dry.DryRun, dry.Decisions = true, tiad.Decisions{}
	// It lists tiad's own unverified-hardware question too, unanswered.
	dr, err := s.Download(ctx, dry)
	if err != nil || !dr.DryRun || !dr.Provisional || !dr.QuestionsIncomplete || dr.Downloaded || questionIDs(dr.Questions) != "stop-cpu,unverified-hardware" || dr.Questions[0].Message != "The CPU must stop" {
		t.Fatalf("dry run: %+v %v", dr, err)
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("a dry run loaded %+v", l)
	}
	if cmp, err := s.CompareWith(ctx, BenchCompare); err != nil || cmp.Equal {
		t.Fatalf("compare before any download: %+v %v", cmp, err)
	}
	contractHardwareGate(t, a, s, req)
	before := time.Now().UTC().Add(-time.Second)
	req = AcceptHardware(req)
	res, err := s.Download(ctx, req)
	if err != nil || !res.Downloaded || res.DryRun || res.State != "RUN" || res.StateSource != tiad.StateFromTIAMessage || len(res.Questions) != 2 {
		t.Fatalf("download: %+v %v", res, err)
	}
	// Success rests on an equal compare right after; the hardware it
	// loaded is said to be unverified, never silently green.
	if !res.CompareEqual || res.HardwareVerified == nil || *res.HardwareVerified || !strings.Contains(res.Warning, "hardware configuration is not verified") {
		t.Fatalf("download evidence: compareEqual %v hardwareVerified %v warning %q", res.CompareEqual, res.HardwareVerified, res.Warning)
	}
	// The attestation that stood in for the hardware check comes back as
	// journalled, stamped by tiad's clock, for the caller's deploy record.
	if at := res.Attestation; at == nil || at.By != BenchAttestation.By || at.Text != BenchAttestation.Text {
		t.Fatalf("download attestation: %+v", res.Attestation)
	} else if when, err := time.Parse("2006-01-02T15:04:05.000Z", at.At); err != nil || when.Before(before) || when.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("attestation time %q (%v), want tiad's UTC clock at the download", at.At, err)
	}
	if cmp, err := s.CompareWith(ctx, BenchCompare); err != nil || !cmp.Equal || len(cmp.Differences) != 0 {
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
	if cmp, _ := s.CompareWith(ctx, BenchCompare); cmp.Equal {
		t.Fatal("compare is equal after a station change")
	}
	if _, err := s.Download(ctx, req); tiad.Kind(err) != tiad.KindNotCompiled {
		t.Fatalf("download after a change, before compiling: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if cmp, _ := s.CompareWith(ctx, BenchCompare); cmp.Equal {
		t.Fatal("compare is equal after compiling a change that was never downloaded")
	}
	a.SetQuestions(t, nil)
	// TIA asked nothing, so it never stopped the CPU and says nothing of
	// its mode: tiad claims none (the fake CPU itself stays in RUN).
	if res, err := s.Download(ctx, req); err != nil || !res.Downloaded || res.State != "" || res.StateSource != "" {
		t.Fatalf("second download, CPU never stopped: %+v %v", res, err)
	}
	if cmp, _ := s.CompareWith(ctx, BenchCompare); !cmp.Equal {
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
	if _, err := s.Download(ctx, req); tiad.Kind(err) != tiad.KindNotCompiled {
		t.Fatalf("download after a software-only compile: %v", err)
	}
}

func contractDownloadRules(t *testing.T, start Starter) {
	a, s := compiledBench(t, start, benchCPU, otherCPU)
	a.SetQuestions(t, []tiad.Question{qStop, {ID: tiad.QuestionReinitialize, Message: "reinit", Objects: []string{"naut_Main_DB"}}})
	base := tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Software: true, Announced: true}

	// Whether the CPU starts after loading is decided before TIA is
	// called; then TIA's checks come one at a time.
	_, err := s.Download(ctx, base)
	if tiad.Kind(err) != tiad.KindDecisionRequired || questionIDs(tiad.Questions(err)) != "start-after" {
		t.Fatalf("no decisions: %v %+v", err, tiad.Questions(err))
	}
	first := base
	first.Decisions = tiad.Decisions{StartAfter: tiad.No()}
	if _, err := s.Download(ctx, first); tiad.Kind(err) != tiad.KindDecisionRequired || questionIDs(tiad.Questions(err)) != "stop-cpu" {
		t.Fatalf("only start-after decided: %v %+v", err, tiad.Questions(err))
	}
	// Stop answered yes, then a check the request does not answer: TIA
	// may already have stopped the CPU, so never a clean refusal.
	acted := base
	acted.Decisions = tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.No()}
	_, err = s.Download(ctx, acted)
	if tiad.Kind(err) != tiad.KindOutcomeUnknown || strings.Join(tiad.ActingAnswers(err), ",") != "stop-cpu" ||
		!strings.Contains(err.Error(), "TIA may already have stopped the CPU: check its mode and start it") || !strings.Contains(err.Error(), "compare before anything else") {
		t.Fatalf("a cancel after an acting answer: %v, want outcome_unknown naming stop-cpu", err)
	}
	no := base
	no.Decisions = tiad.Decisions{StopCPU: tiad.No(), Reinitialize: []string{"naut_Main_DB"}, StartAfter: tiad.No()}
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
	// Whether the CPU starts after loading is always decided before
	// loading, even when TIA would not ask.
	yes := base
	yes.Decisions = tiad.Decisions{StopCPU: tiad.Yes(), Reinitialize: []string{"naut_main_db"}}
	if _, err := s.Download(ctx, yes); tiad.Kind(err) != tiad.KindDecisionRequired || questionIDs(tiad.Questions(err)) != "start-after" {
		t.Fatalf("no startAfter decision: %v, want decision_required start-after", err)
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("an undecided start-after loaded %+v", l)
	}
	yes.Decisions.StartAfter = tiad.No()
	if r, err := s.Download(ctx, yes); err != nil || r.State != "STOP" || r.StateSource != tiad.StateFromTIAMessage {
		t.Fatalf("stopped with startAfter no stays in STOP: %+v %v", r, err)
	}
	a.SetQuestions(t, []tiad.Question{qStop, {ID: tiad.QuestionStartAfter}})
	stay := base
	stay.Decisions = tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.No()}
	if r, err := s.Download(ctx, stay); err != nil || !r.Downloaded || r.State != "STOP" || r.StateSource != tiad.StateFromTIAMessage {
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
		// The agent needs start-after before TIA is called; "no" (stay as
		// it is) leaves each case's outcome to its own questions.
		d := rc.Decisions
		if !strings.Contains(questionIDs(rc.Questions), "start-after") && d.StartAfter == nil {
			d.StartAfter = tiad.No()
		}
		body := map[string]any{"address": benchCPU, "software": true, "announced": true, "decisions": d}
		sec := map[string]string{"fullAccessPassword": BenchFullAccessPassword}
		if rc.Protection {
			sec["protection"] = "pw"
		}
		if rc.ConfigurationPassword {
			sec["configurationPassword"] = BenchConfigurationPassword
		}
		body["secrets"] = sec
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
		"decisions": map[string]any{"reinitialize": []string{"tags"}, "startAfter": false}, "secrets": benchLoginJSON})
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
		{"not announced", map[string]any{"address": benchCPU, "software": true}, "announce it first"},
		{"a dry run not announced", map[string]any{"address": benchCPU, "software": true, "dryRun": true}, "announce it first"},
		{"nothing selected", map[string]any{"address": benchCPU, "announced": true}, "neither hardware nor software"},
		{"no address", map[string]any{"software": true, "announced": true}, "not an IPv4 address"},
		{"a name for an address", map[string]any{"address": "plc-1", "software": true, "announced": true}, "not an IPv4 address"},
		{"a short address", map[string]any{"address": "192.0.2", "software": true, "announced": true}, "not an IPv4 address"},
		{"announced as a string", map[string]any{"address": benchCPU, "software": true, "announced": "true"}, ""},
		{"a decision as a string", map[string]any{"address": benchCPU, "software": true, "announced": true, "decisions": map[string]any{"stopCpu": "yes"}}, ""},
		{"an unknown decision", map[string]any{"address": benchCPU, "software": true, "announced": true, "decisions": map[string]any{"stopCPU": true}}, "unknown field"},
		{"an unknown field", map[string]any{"address": benchCPU, "software": true, "announced": true, "force": true}, "unknown field"},
		// A key twice must never be read as its last (or first) value.
		{"a decision twice", `{"address":"192.0.2.10","software":true,"announced":true,"decisions":{"stopCpu":false,"stopCpu":true}}`, "duplicate key"},
		{"a decision twice, escaped", `{"address":"192.0.2.10","software":true,"announced":true,"decisions":{"stopCpu":false,"stopCp\u0075":true}}`, "duplicate key"},
		{"announced twice", `{"address":"192.0.2.10","software":true,"announced":false,"announced":true}`, "duplicate key"},
	}
	for _, rc := range refused {
		status, b := rawJSON(t, c, "POST", path, rc.body)
		if status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest || !strings.Contains(errMessage(b), rc.want) {
			t.Errorf("%s: HTTP %d %s, want 400 %q", rc.name, status, b, rc.want)
		}
	}
	// Null decisions are no answers.
	a.SetQuestions(t, []tiad.Question{qStop})
	status, b := rawJSON(t, c, "POST", path, map[string]any{"address": benchCPU, "software": true, "announced": true, "decisions": map[string]any{"stopCpu": nil}, "secrets": benchLoginJSON})
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
	check("the compile before the questions", req(benchCPU, true), 409, tiad.KindNotCompiled, "compile")
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
	if cmp, err := s.CompareWith(ctx, BenchCompare); err != nil || cmp.Equal || len(cmp.Differences) == 0 || cmp.Differences[0].Object != "PLC_1" {
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
	if _, err := s2.CompareWith(ctx, BenchCompare); tiad.Kind(err) != tiad.KindTargetRefused {
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
	want := []string{"out/naut_Main.scl", "out/naut_Main_DB.scl", "out/tagtables.xml", "out/station.json", "out/GSDML-V2.4-fake-PLC_1.xml"}
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
	// An export never writes into an open project's directory.
	for _, dir := range []string{"projects/bench", "projects/bench/sub", "PROJECTS/Bench/x"} {
		if _, err := s.Export(ctx, tiad.ExportRequest{Dir: dir, Hardware: true}); tiad.Kind(err) != tiad.KindBusy {
			t.Errorf("export to %s while bench is open: %v, want busy", dir, err)
		}
	}
	if _, err := c.GetFile(ctx, "projects/bench/station.json"); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("an export wrote into the open project's directory: %v", err)
	}
	if err := s.Upload(ctx); err != nil {
		t.Fatalf("upload: %v", err)
	}
	// Only an upload and a fingerprint read take trustUnverifiedCertificate,
	// as an explicit opt-in; download and compare refuse it as an unknown
	// field.
	if r, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin, TrustUnverifiedCertificate: true}); err != nil || r.TrustedBlindly == nil || len(r.TrustedBlindly) != 0 {
		t.Fatalf("upload with the opt-in, no TLS question: %+v %v", r, err)
	}
	for _, call := range []struct {
		path string
		body map[string]any
	}{
		{"compare", map[string]any{"trustUnverifiedCertificate": true}},
		{"download", map[string]any{"address": benchCPU, "software": true, "announced": true, "trustUnverifiedCertificate": true}},
	} {
		status, b := rawJSON(t, c, "POST", "/v1/sessions/"+s.ID+"/"+call.path, call.body)
		if status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest || !strings.Contains(string(b), "trustUnverifiedCertificate") {
			t.Errorf("%s with trustUnverifiedCertificate: HTTP %d %s, want 400 bad_request naming it", call.path, status, b)
		}
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("a refused download loaded %+v", l)
	}
	// After an upload the project no longer matches its spec.
	if _, err := s.Download(ctx, tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Software: true, Announced: true}); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "no station spec") {
		t.Fatalf("download after an upload: %v", err)
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

// contractS2aSequence walks S2a, read-only towards the CPU: a project
// for the CPU, an upload from it, the hardware exported twice (the two
// exports must be equal), a compare. After the upload the project no
// longer matches the spec, so a download needs a spec and a compile
// again.
func contractS2aSequence(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "s2a", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Upload(ctx); err != nil {
		t.Fatalf("upload: %v", err)
	}
	var exports [2][]byte
	for i := range exports {
		dir := []string{"export/1", "export/2"}[i]
		ex, err := s.Export(ctx, tiad.ExportRequest{Dir: dir, Blocks: "scl", TagTables: true, Hardware: true})
		if err != nil {
			t.Fatalf("export %d after the upload: %v", i+1, err)
		}
		for _, f := range ex.Files {
			if strings.HasSuffix(f, "/station.json") {
				exports[i], _ = c.GetFile(ctx, f)
			}
		}
	}
	if len(exports[0]) == 0 || string(exports[0]) != string(exports[1]) {
		t.Fatalf("the two hardware exports differ or are missing:\n%s\n%s", exports[0], exports[1])
	}
	if _, err := s.CompareWith(ctx, BenchCompare); err != nil {
		t.Fatalf("compare after the upload: %v", err)
	}
	dl := AcceptHardware(tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Hardware: true, Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}})
	if _, err := s.Download(ctx, dl); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "no station spec") {
		t.Fatalf("download straight after the upload: %v", err)
	}
	// The uploaded CPU has no access control (the S2a upload): keep it,
	// and leave its web server as found.
	uploaded := BenchStation()
	uploaded.CPU.AccessControl = tiad.AccessKeep
	uploaded.WebServer = nil
	if _, err := s.ApplyStation(ctx, uploaded, BenchSecrets); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, dl); tiad.Kind(err) != tiad.KindNotCompiled {
		t.Fatalf("download after the spec, before a compile: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	// The upload's IO device was kept: its download needs a yes naming it.
	if _, err := s.Download(ctx, dl); tiad.Kind(err) != tiad.KindDecisionRequired || !strings.Contains(err.Error(), "name them in decisions.keptDevices") {
		t.Fatalf("download without a yes for the kept device: %v", err)
	}
	dl.Decisions.KeptDevices = []string{UploadedIODevice().Name}
	if _, err := s.Download(ctx, dl); err != nil {
		t.Fatalf("download after spec and compile: %v", err)
	}
}

// contractOneCPU: two projects for one CPU. The first download claims the
// CPU for its session (a dry run too); the other is refused until that
// session is closed.
func contractOneCPU(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	mk := func(name string) *tiad.Session {
		t.Helper()
		s, err := c.CreateProject(ctx, name, BenchStation(), BenchSecrets)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Compile(ctx, true, true); err != nil {
			t.Fatal(err)
		}
		return s
	}
	// TIA opens one project at a time (#616), so a second project for the
	// CPU cannot even open while the first is open: one CPU, one project.
	one := mk("one")
	dl := tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}
	if _, err := one.Download(ctx, dl); err != nil {
		t.Fatalf("the first project's download: %v", err)
	}
	if _, err := c.CreateProject(ctx, "two", BenchStation(), BenchSecrets); tiad.Kind(err) != tiad.KindBusy || !strings.Contains(err.Error(), "session "+one.ID) {
		t.Fatalf("a second project while the first is open: %v, want busy naming %s", err, one.ID)
	}
	if n := len(a.Loaded(t)); n != 1 {
		t.Fatalf("loaded %d times, want 1", n)
	}
	if err := one.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := mk("two").Download(ctx, dl); err != nil {
		t.Fatalf("the second project once the first is closed: %v", err)
	}
}
