package tiadtest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
	"github.com/joyautomation/nautilus/siemens/tiad/tiadtest"
)

const (
	cpuAddr = "192.0.2.10"
	gsdml   = "GSDML-V2.4-Example-Device-20261117.xml"
)

func intp(i int) *int { return &i }

func station(modules ...tiad.DeviceModule) tiad.Station {
	return tiad.Station{
		CPU: tiad.CPU{Name: "PLC_1", Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1",
			Address: cpuAddr, SubnetMask: "255.255.255.0", ProfinetName: "plc-1"},
		WebServer: &tiad.WebServer{Enabled: true, Users: []tiad.WebUser{{Name: "naut", Rights: []string{"read", "write", "mode"}}}},
		Profinet: &tiad.Profinet{UpdateTime: "16ms", Devices: []tiad.Device{{
			GSDML: gsdml, DAP: "DAP_1", Name: "io-device-1", Address: "192.0.2.20", Modules: modules,
		}}},
	}
}

var secrets = tiad.Secrets{WebUsers: map[string]string{"naut": "pw"}}

const programSCL = `FUNCTION_BLOCK "Main"
VERSION : 0.1
   VAR
      count : Int;
   END_VAR
BEGIN
   #count := #count + 1;
END_FUNCTION_BLOCK

DATA_BLOCK "Main_DB"
"Main"
BEGIN
END_DATA_BLOCK
`

// TestProfinetBenchFlow walks the PROFINET bench requirements P1-P3 and P5
// through the client against the fake: install the GSDML, create a
// project with the device, import software, compile, dry-run, download,
// compare, then change a module and download again.
func TestProfinetBenchFlow(t *testing.T) {
	ctx := context.Background()
	f := tiadtest.New()
	f.Token = "secret"
	f.Targets = []string{cpuAddr}
	stopped := false
	f.PreDownload = func(r tiad.DownloadRequest) []tiad.Question {
		if r.Hardware && !stopped {
			return []tiad.Question{
				{ID: tiad.QuestionStopCPU, Message: "The CPU is stopped for the hardware download"},
				{ID: tiad.QuestionStartAfter, Message: "Start the CPU after loading"},
			}
		}
		return nil
	}
	_, c := f.Serve(t)

	if p, err := c.Probe(ctx); err != nil || !p.Usable {
		t.Fatalf("probe: %+v %v", p, err)
	}

	// P1
	if _, err := c.PutFile(ctx, "gsd/"+gsdml, []byte("<ISO15745Profile/>")); err != nil {
		t.Fatal(err)
	}
	if r, err := c.InstallGSD(ctx, "gsd/"+gsdml); err != nil || r.AlreadyInstalled {
		t.Fatalf("install: %+v %v", r, err)
	}
	if r, err := c.InstallGSD(ctx, "gsd/"+gsdml); err != nil || !r.AlreadyInstalled {
		t.Fatalf("second install should report alreadyInstalled: %+v %v", r, err)
	}

	// P2
	mod := tiad.DeviceModule{Slot: 1, Subslot: 1, Module: "ID_Mod_In4Out2", InputStart: intp(100), OutputStart: intp(100)}
	s, err := c.CreateProject(ctx, "bench", station(mod), secrets)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	if _, err := c.PutFile(ctx, "src/Main.scl", []byte(programSCL)); err != nil {
		t.Fatal(err)
	}
	sw, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}, Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sw.Blocks) != 2 || sw.Blocks[0].Name != "Main" || sw.Blocks[0].Kind != "FB" || sw.Blocks[1].Kind != "DB" {
		t.Fatalf("blocks: %+v", sw.Blocks)
	}

	// P3: download before compiling is refused.
	req := tiad.DownloadRequest{Address: cpuAddr, Hardware: true, Software: true, Announced: true,
		Decisions: tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.Yes()}}
	if _, err := s.Download(ctx, req); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("download before compile: %v", err)
	}
	if r, err := s.Compile(ctx, true, true); err != nil || r.Result != "success" {
		t.Fatalf("compile: %+v %v", r, err)
	}
	dry := req
	dry.DryRun, dry.Decisions = true, tiad.Decisions{}
	dr, err := s.Download(ctx, dry)
	if err != nil || !dr.DryRun || dr.Downloaded || len(dr.Questions) != 2 {
		t.Fatalf("dry run: %+v %v", dr, err)
	}
	// Models the provisional contract: that a dry run loads nothing is
	// what tiad aims for, unproven against Openness until S2 (§3.5).
	if len(f.Downloads()) != 0 {
		t.Fatal("a dry run must not download")
	}
	res, err := s.Download(ctx, req)
	if err != nil || !res.Downloaded || res.State != "RUN" {
		t.Fatalf("download: %+v %v", res, err)
	}
	stopped = true
	if cmp, err := s.Compare(ctx); err != nil || !cmp.Equal {
		t.Fatalf("compare after download: %+v %v", cmp, err)
	}

	// P5: a changed module is a changed station; it must be compiled and
	// downloaded again, and compare says so until it is.
	mod.Module = "ID_Mod_In8Out8"
	ch, err := s.ApplyStation(ctx, station(mod), secrets)
	if err != nil || len(ch.Changes) != 1 || ch.Changes[0] != "device io-device-1 changed" {
		t.Fatalf("apply: %+v %v", ch, err)
	}
	if cmp, _ := s.Compare(ctx); cmp.Equal {
		t.Fatal("compare should differ before the changed station is downloaded")
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, req); err != nil {
		t.Fatal(err)
	}
	if n := len(f.Downloads()); n != 2 {
		t.Fatalf("downloads = %d, want 2", n)
	}
	for _, d := range f.Downloads() {
		if d.Request.Secrets != nil {
			t.Fatal("secrets were recorded")
		}
	}

	// Removing the device is reported as such.
	empty := station()
	empty.Profinet.Devices = nil
	if ch, err := s.ApplyStation(ctx, empty, secrets); err != nil || len(ch.Changes) != 1 || ch.Changes[0] != "device io-device-1 removed" {
		t.Fatalf("remove: %+v %v", ch, err)
	}
}

func TestDownloadRules(t *testing.T) {
	ctx := context.Background()
	f := tiadtest.New()
	f.Targets = []string{cpuAddr}
	f.PreDownload = func(tiad.DownloadRequest) []tiad.Question {
		return []tiad.Question{
			{ID: tiad.QuestionStopCPU, Message: "stop"},
			{ID: tiad.QuestionReinitialize, Message: "reinit", Objects: []string{"Main_DB"}},
		}
	}
	_, c := f.Serve(t)
	c.PutFile(ctx, "gsd/"+gsdml, []byte("x"))
	c.InstallGSD(ctx, "gsd/"+gsdml)
	s, err := c.CreateProject(ctx, "rules", station(), secrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}

	base := tiad.DownloadRequest{Address: cpuAddr, Software: true, Announced: true}

	_, err = s.Download(ctx, base)
	if tiad.Kind(err) != tiad.KindDecisionRequired || len(tiad.Questions(err)) != 2 {
		t.Fatalf("no decisions: %v %+v", err, tiad.Questions(err))
	}

	no := base
	no.Decisions = tiad.Decisions{StopCPU: tiad.No(), Reinitialize: []string{"Main_DB"}}
	_, err = s.Download(ctx, no)
	if tiad.Kind(err) != tiad.KindDeclined || tiad.Questions(err)[0].ID != tiad.QuestionStopCPU {
		t.Fatalf("declined stop: %v", err)
	}

	other := base
	other.Address = "192.0.2.99"
	if _, err := s.Download(ctx, other); tiad.Kind(err) != tiad.KindTargetRefused {
		t.Fatalf("unlisted target: %v", err)
	}

	f.Targets = append(f.Targets, "192.0.2.99")
	if _, err := s.Download(ctx, other); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "one station spec per CPU") {
		t.Fatalf("download to another CPU than the station's: %v", err)
	}

	yes := base
	yes.Decisions = tiad.Decisions{StopCPU: tiad.Yes(), Reinitialize: []string{"main_db"}}
	r, err := s.Download(ctx, yes)
	if err != nil || r.State != "STOP" {
		t.Fatalf("stopped without startAfter should stay in STOP: %+v %v", r, err)
	}
	if len(f.Downloads()) != 1 {
		t.Fatalf("downloads = %d, want 1 (refusals load nothing)", len(f.Downloads()))
	}

	// "Load and stay in STOP" is a decision, not a refusal.
	f.PreDownload = func(tiad.DownloadRequest) []tiad.Question {
		return []tiad.Question{{ID: tiad.QuestionStopCPU}, {ID: tiad.QuestionStartAfter}}
	}
	stay := base
	stay.Decisions = tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.No()}
	if r, err := s.Download(ctx, stay); err != nil || !r.Downloaded || r.State != "STOP" {
		t.Fatalf("load and stay in STOP: %+v %v", r, err)
	}
}

func TestCancelledContextKeepsItsError(t *testing.T) {
	f := tiadtest.New()
	_, c := f.Serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Health(ctx)
	if !errors.Is(err, context.Canceled) || tiad.Kind(err) != tiad.KindUnreachable {
		t.Fatalf("cancelled: %v (errors.Is canceled = %v)", err, errors.Is(err, context.Canceled))
	}
}

func TestProjectNameIsOneSafeSegment(t *testing.T) {
	ctx := context.Background()
	f := tiadtest.New()
	srv, c := f.Serve(t)
	c.PutFile(ctx, "gsd/"+gsdml, []byte("x"))
	c.InstallGSD(ctx, "gsd/"+gsdml)
	bad := []string{"", "CON", "nul.txt", "x.", "a\x01b", "..", "a/b", "a/../b", `a\b`, "c:x"}
	for _, n := range bad {
		if _, err := c.CreateProject(ctx, n, station(), secrets); tiad.Kind(err) != tiad.KindBadRequest {
			t.Errorf("client CreateProject(%q) = %v, want bad_request", n, err)
		}
	}
	// The agent refuses them too, for a client that skips the check.
	for _, n := range bad {
		body, _ := json.Marshal(map[string]any{"name": n, "station": station(), "secrets": secrets})
		resp, err := http.Post(srv.URL+"/v1/projects", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("agent POST /v1/projects name %q = HTTP %d, want 400", n, resp.StatusCode)
		}
	}
	if _, err := c.CreateProject(ctx, "bench-1", station(), secrets); err != nil {
		t.Fatalf("a plain name: %v", err)
	}
}

func TestEveryPathIsCheckedBeforeSending(t *testing.T) {
	ctx := context.Background()
	f := tiadtest.New()
	_, c := f.Serve(t)
	if _, err := c.InstallGSD(ctx, "../GSDML-x.xml"); tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("InstallGSD: %v", err)
	}
	if _, err := c.Open(ctx, "C:/p.ap21"); tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("Open: %v", err)
	}
	c.PutFile(ctx, "gsd/"+gsdml, []byte("x"))
	c.InstallGSD(ctx, "gsd/"+gsdml)
	s, err := c.CreateProject(ctx, "p", station(), secrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "g", Sources: []string{"aux.scl"}}); tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("Software: %v", err)
	}
	if _, err := s.Export(ctx, tiad.ExportRequest{Dir: "out.", Blocks: "scl"}); tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("Export: %v", err)
	}
}

func TestAgentRefusals(t *testing.T) {
	ctx := context.Background()
	f := tiadtest.New()
	f.Token = "secret"
	_, c := f.Serve(t)

	bad := tiad.New(c.BaseURL, "wrong")
	if _, err := bad.Health(ctx); tiad.Kind(err) != tiad.KindUnauthorized || !strings.Contains(err.Error(), "refused the token") {
		t.Fatalf("wrong token: %v", err)
	}

	for _, p := range []string{"../x", "a/../../x", "C:/x", `a\b`, "NUL", "a/con.txt", "Com1.scl", "x.", "dir /x", "a\x00b", "a|b", "a?b", "file.scl:stream"} {
		if _, err := c.PutFile(ctx, p, nil); tiad.Kind(err) != tiad.KindBadRequest {
			t.Errorf("PutFile(%q) = %v, want bad_request", p, err)
		}
	}

	// Device GSDML not installed.
	if _, err := c.CreateProject(ctx, "p", station(), secrets); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("uninstalled GSDML: %v", err)
	}
	// Web user without a password.
	c.PutFile(ctx, "gsd/"+gsdml, []byte("x"))
	c.InstallGSD(ctx, "gsd/"+gsdml)
	if _, err := c.CreateProject(ctx, "p", station(), tiad.Secrets{}); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "no password") {
		t.Fatalf("missing web password: %v", err)
	}
	// Invalid spec is refused by the client before it is sent.
	st := station()
	st.CPU.ProfinetName = "PLC 1"
	if _, err := c.CreateProject(ctx, "p", st, secrets); tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("invalid spec: %v", err)
	}

	s, err := c.CreateProject(ctx, "p", station(), secrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateProject(ctx, "p", station(), secrets); tiad.Kind(err) != tiad.KindBusy {
		t.Fatalf("second open of one project: %v", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatalf("second close: %v", err)
	}

	// A failing gate stops every TIA call but not probe.
	f.Gates = []tiad.Gate{{Name: "licence", OK: false, Detail: "trial expired"}}
	if p, err := c.Probe(ctx); err != nil || p.Usable {
		t.Fatalf("probe with a failed gate: %+v %v", p, err)
	}
	if _, err := c.Open(ctx, "projects/p/p.ap21"); tiad.Kind(err) != tiad.KindGate || !strings.Contains(err.Error(), "trial expired") {
		t.Fatalf("gate: %v", err)
	}
}

func TestCompileFailedCarriesResult(t *testing.T) {
	ctx := context.Background()
	f := tiadtest.New()
	f.CompileResult = tiad.CompileResult{Result: "error", Errors: 1,
		Messages: []tiad.CompileMessage{{Object: "PLC_1/Program blocks/Main", Severity: "error", Text: "Tag #x not defined"}}}
	_, c := f.Serve(t)
	c.PutFile(ctx, "gsd/"+gsdml, []byte("x"))
	c.InstallGSD(ctx, "gsd/"+gsdml)
	s, err := c.CreateProject(ctx, "p", station(), secrets)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Compile(ctx, true, true)
	r, ok := tiad.CompileErrors(err)
	if !ok || r.Errors != 1 || r.Messages[0].Text != "Tag #x not defined" {
		t.Fatalf("compile_failed: %v %+v", err, r)
	}
}

func TestFilesAndExport(t *testing.T) {
	ctx := context.Background()
	f := tiadtest.New()
	_, c := f.Serve(t)
	if _, err := c.PutFile(ctx, "a/b c.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	b, err := c.GetFile(ctx, "a/b c.txt")
	if err != nil || string(b) != "hello" {
		t.Fatalf("get: %q %v", b, err)
	}
	if _, err := c.GetFile(ctx, "nope"); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("missing file: %v", err)
	}
	c.PutFile(ctx, "gsd/"+gsdml, []byte("x"))
	c.InstallGSD(ctx, "gsd/"+gsdml)
	s, err := c.CreateProject(ctx, "p", station(), secrets)
	if err != nil {
		t.Fatal(err)
	}
	c.PutFile(ctx, "src/Main.scl", []byte(programSCL))
	s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}})
	ex, err := s.Export(ctx, tiad.ExportRequest{Dir: "out", Blocks: "scl", Hardware: true, GSDML: true})
	if err != nil || len(ex.Files) != 4 {
		t.Fatalf("export: %+v %v", ex, err)
	}
	if b, ok := f.File("out/station.json"); !ok || !strings.Contains(string(b), "io-device-1") {
		t.Fatalf("station export: %s", b)
	}
	p, err := s.Save(ctx, true)
	if err != nil || p != "projects/p/p.zap21" {
		t.Fatalf("save: %q %v", p, err)
	}
}
