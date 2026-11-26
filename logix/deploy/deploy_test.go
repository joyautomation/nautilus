package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/joyautomation/nautilus/lang/l5x"
	"github.com/joyautomation/nautilus/logix/logixd"
	"github.com/joyautomation/nautilus/logix/writer"
)

// fakeAgent is just enough logixd for a deploy: a file store, a
// conversion that "runs" the SDK by handing back canned documents, and a
// controller whose running program is whatever was last imported or
// downloaded to it.
type fakeAgent struct {
	t       *testing.T
	files   map[string][]byte
	running []byte // the controller's project, as the lean L5X an upload+convert yields
	calls   []string
	imports []map[string]any
	// routineImports records whole-routine online imports (ST).
	routineImports []map[string]any
	mode           string
	// startState makes a download refuse unless it switches to Program
	// mode, as the SDK does with a controller in Run.
	startState bool
	// modeFails makes the mode read answer an error.
	modeFails bool
	// hook, when set, sees each request before it is answered (#1404:
	// a caller that goes away mid-call).
	hook func(r *http.Request)
	// callsMu guards calls: a call its caller abandoned is still being
	// answered while the deploy makes the next one.
	callsMu sync.Mutex
	// downloadKeeps makes a download leave the controller as it was, so
	// the verify after it finds it different.
	downloadKeeps bool
	// exportFails fails this many partial exports (a verify that cannot
	// read the controller).
	exportFails int
	// importFails makes the SDK answer a rung import with an error:
	// "refuse" (operation_failed, nothing taken), "refuse-took" (the same
	// answer, but the controller took the rungs all the same) or "fatal"
	// (operation_not_performed: the channel broke after the rungs went).
	importFails string
}

func newFakeAgent(t *testing.T, running []byte) (*fakeAgent, *logixd.Client) {
	f := &fakeAgent{t: t, files: map[string][]byte{}, running: running, mode: "Run"}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, logixd.New(srv.URL, "")
}

func (f *fakeAgent) serve(w http.ResponseWriter, r *http.Request) {
	f.callsMu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.callsMu.Unlock()
	if f.hook != nil {
		f.hook(r)
	}
	w.Header().Set("Content-Type", "application/json")
	ok := func(data any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data, "events": []any{}})
	}
	var body map[string]any
	if r.Method != http.MethodGet && !strings.HasPrefix(r.URL.Path, "/v1/files/") {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/files/"):
		name := strings.TrimPrefix(r.URL.Path, "/v1/files/")
		if r.Method == http.MethodPut {
			raw, _ := readAll(r)
			f.files[name] = raw
			ok(map[string]any{"path": name})
			return
		}
		raw, found := f.files[name]
		if !found {
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]any{"kind": "not_found", "message": name}})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(raw)
	case r.URL.Path == "/v1/convert":
		in, out := body["input"].(string), body["output"].(string)
		switch {
		case strings.HasSuffix(out, ".ACD"):
			// An import: the ACD "is" the L5X it came from.
			f.files[out] = f.files[in]
		default:
			// An export of an uploaded project: the controller's program.
			if string(f.files[in]) == "UPLOADED" {
				f.files[out] = f.running
			} else {
				f.files[out] = f.files[in]
			}
		}
		ok(map[string]any{"input": in, "output": out, "bytes": len(f.files[out])})
	case r.URL.Path == "/v1/upload-to-new":
		if f.running == nil {
			w.WriteHeader(500)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]any{
				"kind": "operation_failed", "message": "RxE_NOT_FOUND - Requested item could not be found.", "fatal": false}})
			return
		}
		f.files[body["output"].(string)] = []byte("UPLOADED")
		ok(map[string]any{})
	case r.URL.Path == "/v1/sessions" && r.Method == http.MethodPost:
		ok(map[string]any{"session": "S1", "project": body["project"]})
	case strings.HasSuffix(r.URL.Path, "/build"):
		ok(map[string]any{"target": "DefaultTarget", "elapsedMs": 7})
	case strings.HasSuffix(r.URL.Path, "/save"):
		ok(map[string]any{})
	case strings.HasSuffix(r.URL.Path, "/comm-path"), strings.HasSuffix(r.URL.Path, "/online"), strings.HasSuffix(r.URL.Path, "/offline"):
		ok(map[string]any{"connected": "Online", "commPath": "x"})
	case strings.HasSuffix(r.URL.Path, "/mode") && f.modeFails:
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]any{"kind": "operation_failed", "message": "mode unavailable", "fatal": false}})
	case strings.HasSuffix(r.URL.Path, "/mode"):
		ok(map[string]any{"mode": f.mode})
	case strings.HasSuffix(r.URL.Path, "/partial-import-with-target"):
		f.routineImports = append(f.routineImports, body)
		file := body["file"].(string)
		f.running = f.files[file]
		if full, ok := f.files[strings.TrimSuffix(file, ".routine.L5X")+".L5X"]; ok {
			f.running = full
		}
		ok(map[string]any{})
	case strings.HasSuffix(r.URL.Path, "/partial-import"):
		// A program imported over the skeleton: the project is now the
		// full generated document, which is what the deploy also staged.
		file := body["file"].(string)
		if full, ok := f.files[strings.TrimSuffix(file, ".program.L5X")+".L5X"]; ok {
			dir := file[:strings.LastIndex(file, "/")+1]
			f.files[dir+"merged.ACD"] = full
			for name := range f.files {
				if strings.HasSuffix(name, ".ACD") && strings.HasPrefix(name, dir) && !strings.Contains(name, "before") && !strings.Contains(name, "after") {
					f.files[name] = full
				}
			}
		}
		ok(map[string]any{})
	case strings.HasSuffix(r.URL.Path, "/partial-export") && f.exportFails > 0:
		f.exportFails--
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]any{"kind": "operation_failed", "message": "export failed", "fatal": false}})
	case strings.HasSuffix(r.URL.Path, "/partial-export"):
		f.files[body["output"].(string)] = f.running
		ok(map[string]any{})
	case strings.HasSuffix(r.URL.Path, "/import-rungs") && f.importFails != "":
		kind, fatal := "operation_failed", false
		if f.importFails == "fatal" {
			kind, fatal = "operation_not_performed", true
		}
		if f.importFails != "refuse" {
			f.running = f.files[body["file"].(string)]
		}
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]any{"kind": kind, "message": "rung 1 does not verify", "fatal": fatal}})
	case strings.HasSuffix(r.URL.Path, "/import-rungs"):
		f.imports = append(f.imports, body)
		// The controller now runs the imported rungs. The cold path staged
		// the whole generated project beside the rung partial, and a later
		// upload shows that; the warm path staged only the partial, which
		// carries the routine a later export shows.
		file := body["file"].(string)
		f.running = f.files[file]
		if full, ok := f.files[strings.TrimSuffix(file, ".rungs.L5X")+".L5X"]; ok {
			f.running = full
		}
		ok(map[string]any{"insertPosition": body["insertPosition"], "replaceCount": body["replaceCount"], "onlineOption": body["onlineOption"], "elapsedMs": 3})
	case strings.HasSuffix(r.URL.Path, "/download"):
		if f.startState && (f.mode == "Run" || body["ensureProgramMode"] == true) {
			// The SDK's answer, its status lines with it (on Echo, ~20).
			w.WriteHeader(500)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false,
				"error": map[string]any{"kind": "operation_failed", "type": "OperationFailedException",
					"message": "RxE_INVALID_START_STATE - Invalid start state.  Internal error. (C:\\work\\DemoLine.ACD)", "fatal": false},
				"events": []any{
					map[string]any{"kind": "status", "source": "Download", "message": "Preparing the download"},
					map[string]any{"kind": "error", "source": "Download", "message": "Failed to download to the controller"},
				}})
			return
		}
		if !f.downloadKeeps {
			f.running = f.generated()
		}
		f.mode = "Program"
		ok(map[string]any{})
	case r.Method == http.MethodDelete:
		ok(map[string]any{})
	default:
		f.t.Errorf("fake agent: unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(500)
	}
}

// generated finds the full project L5X the deploy staged.
// generated is the whole staged document — never a partial (rungs,
// routine, program) nor the skeleton of a two-step build, and never an
// upload. Map order is random, so every other candidate must be excluded
// by name or this picks the wrong file one run in three.
func (f *fakeAgent) generated() []byte {
	for name, raw := range f.files {
		if !strings.HasSuffix(name, ".L5X") {
			continue
		}
		base := name[strings.LastIndex(name, "/")+1:]
		if strings.Count(base, ".") != 1 || strings.Contains(name, "before") || strings.Contains(name, "after") {
			continue
		}
		return raw
	}
	return nil
}

func readAll(r *http.Request) ([]byte, error) {
	buf := make([]byte, 0, 1<<16)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}

func demoSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "writer", "testdata", "demoline.ld"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func target() Target {
	return Target{Controller: "DemoLine", Revision: "38.11", CommPath: `AB_ETH-1\1.2.3.4\Backplane\0`}
}

func TestBuildOnlyReportsWhatTheControllerNeeds(t *testing.T) {
	src := demoSource(t)
	// The controller runs the same logic.
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	_, c := newFakeAgent(t, same)
	rep, err := Run(context.Background(), src, Options{Client: c, Target: target(), Mode: BuildOnly})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Same || rep.Applied != BuildOnly || rep.Rungs != 2 || rep.Build == 0 {
		t.Errorf("report = %+v", rep)
	}

	// A rung change.
	edited := strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm ( RunCmd )", 1)
	rep, err = Run(context.Background(), edited, Options{Client: c, Target: target(), Mode: BuildOnly})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Same || rep.TagsChanged || len(rep.Diffs) != 1 || !strings.Contains(rep.Diffs[0], "XIO(HiLevelAlm)") {
		t.Errorf("report = %+v", rep)
	}
}

func TestOnlineEditReplacesTheRungsAndVerifies(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	edited := strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm ( RunCmd )", 1)
	rep, err := Run(context.Background(), edited, Options{Client: c, Target: target(), Mode: Online})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Applied != Online || !rep.Verified || rep.Replaced != 2 || rep.ModeAfter != "Run" {
		t.Errorf("report = %+v", rep)
	}
	if len(f.imports) != 1 || f.imports[0]["onlineOption"] != "FinalizeEdits" || f.imports[0]["replaceCount"] != float64(2) || f.imports[0]["insertPosition"] != float64(0) {
		t.Errorf("import-rungs = %v", f.imports)
	}
	if !strings.HasSuffix(f.imports[0]["file"].(string), "DemoLine.rungs.L5X") {
		t.Errorf("imported %v, want the rungs partial", f.imports[0]["file"])
	}
	for _, call := range f.calls {
		if strings.HasSuffix(call, "/download") {
			t.Error("an online edit must never download")
		}
	}
}

func TestOnlineEditRefusesATagChange(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	edited := strings.Replace(src, "    HiLevelAlm : BOOL;", "    HiLevelAlm : BOOL;\n    Extra : DINT;", 1)
	rep, err := Run(context.Background(), edited, Options{Client: c, Target: target(), Mode: Online})
	var nd *NeedsDownloadError
	if !errors.As(err, &nd) || !rep.TagsChanged {
		t.Fatalf("err = %v, report = %+v", err, rep)
	}
	if len(f.imports) != 0 {
		t.Error("the refused edit still imported rungs")
	}
}

func TestDownloadVerifies(t *testing.T) {
	src := demoSource(t)
	f, c := newFakeAgent(t, []byte(`<?xml version="1.0"?><RSLogix5000Content><Controller Name="Empty"><Programs/></Controller></RSLogix5000Content>`))
	rep, err := Run(context.Background(), src, Options{Client: c, Target: target(), Mode: Download, ProgramMode: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Applied != Download || !rep.Verified || !rep.RoutineMissing || rep.ModeAfter != "Program" {
		t.Errorf("report = %+v", rep)
	}
	if len(f.imports) != 0 {
		t.Error("a download must not also import rungs")
	}
}

func TestSubsetDiagnosticsStopEverything(t *testing.T) {
	f, c := newFakeAgent(t, nil)
	src := "PROGRAM P\nVAR X : BOOL; Y : BOOL; tp : TP; END_VAR\nLD\n  RUNG r X tp:TP(PT := T#1S) ( Y )\nEND_LD\nEND_PROGRAM\n"
	_, err := Run(context.Background(), src, Options{Client: c, Target: target(), Mode: Online})
	var de *DiagError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v", err)
	}
	// The block's own rule among them (a one-line VAR's declaration now
	// also gets its type rule, ahead of it).
	found := false
	for _, d := range de.Diags {
		found = found || d.Rule == "logix/fb"
	}
	if !found {
		t.Fatalf("err = %v, want a logix/fb diagnostic", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("the agent was called despite the diagnostic: %v", f.calls)
	}
}

// A controller that has never been downloaded to cannot be uploaded from;
// the deploy treats that as "needs a download", and after the download the
// verification upload works.
func TestEmptyControllerNeedsADownload(t *testing.T) {
	src := demoSource(t)
	f, c := newFakeAgent(t, nil)
	rep, err := Run(context.Background(), src, Options{Client: c, Target: target(), Mode: BuildOnly})
	if err != nil || !rep.RoutineMissing || rep.Same {
		t.Fatalf("err = %v, report = %+v", err, rep)
	}
	_, err = Run(context.Background(), src, Options{Client: c, Target: target(), Mode: Online})
	var nd *NeedsDownloadError
	if !errors.As(err, &nd) || !nd.RoutineMissing {
		t.Fatalf("online on an empty controller: err = %v", err)
	}
	rep, err = Run(context.Background(), src, Options{Client: c, Target: target(), Mode: Download})
	if err != nil || rep.Applied != Download || !rep.Verified {
		t.Fatalf("download: err = %v, report = %+v", err, rep)
	}
	if len(f.imports) != 0 {
		t.Error("a download must not import rungs")
	}
}

// A plain download to a controller in Run is refused by the SDK with
// RxE_INVALID_START_STATE; the error says what to do instead and keeps
// the SDK's own text underneath (#379). Asked for Program mode and still
// refused (a keyswitch in RUN), the hint would be wrong: the SDK's answer
// goes as it is, in doubt, since the agent may have switched the mode in
// the same call (#1404).
func TestDownloadInRunSaysToSwitchToProgramMode(t *testing.T) {
	src := demoSource(t)
	f, c := newFakeAgent(t, nil)
	f.startState = true
	_, err := Run(context.Background(), src, Options{Client: c, Target: target(), Mode: Download})
	var se *StartStateError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T)", err, err)
	}
	if se.Mode != "Run" || !strings.Contains(err.Error(), "the controller is in Run mode") ||
		!strings.Contains(err.Error(), `"Switch to Program Mode and Download"`) || !strings.Contains(err.Error(), "--program-mode") {
		t.Errorf("message = %q", err)
	}
	if strings.Contains(err.Error(), "RxE_") {
		t.Errorf("the message carries the SDK's code: %q", err)
	}
	if raw := se.Raw.Error(); !strings.Contains(raw, "RxE_INVALID_START_STATE") || !strings.Contains(raw, "Failed to download to the controller") {
		t.Errorf("raw = %q", raw)
	}
	if (&StartStateError{}).Error() == "" || !strings.Contains((&StartStateError{}).Error(), "is not in Program mode") {
		t.Errorf("no mode: %q", (&StartStateError{}).Error())
	}

	// Program mode asked for, and refused all the same.
	_, err = Run(context.Background(), src, Options{Client: c, Target: target(), Mode: Download, ProgramMode: true})
	var ou *OutcomeUnknownError
	if errors.As(err, &se) || !errors.As(err, &ou) || !strings.Contains(err.Error(), "RxE_INVALID_START_STATE") {
		t.Errorf("program mode, refused: %v (%T)", err, err)
	}
}

// The warm path: one session, several edits, one upload.
func TestSessionEditsWithoutReopening(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	s, err := Connect(context.Background(), Options{Client: c, Target: target()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	uploads := func() int {
		n := 0
		for _, call := range f.calls {
			if strings.HasSuffix(call, "/upload-to-new") {
				n++
			}
		}
		return n
	}
	if uploads() != 1 {
		t.Fatalf("%d uploads at connect", uploads())
	}
	rep, err := s.Edit(context.Background(), src)
	if err != nil || !rep.Same {
		t.Fatalf("same program: err=%v report=%+v", err, rep)
	}
	edited := strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm ( RunCmd )", 1)
	rep, err = s.Edit(context.Background(), edited)
	if err != nil || rep.Applied != Online || !rep.Verified || rep.Replaced != 2 {
		t.Fatalf("edit: err=%v report=%+v", err, rep)
	}
	// A second edit replaces what the first left, still on the same session.
	again := strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm /LevelPct ( RunCmd )", 1)
	again = strings.Replace(again, "    LevelPct   : REAL;", "    LevelPct   : REAL;", 1)
	rep, err = s.Edit(context.Background(), strings.Replace(edited, "RUNG alarm", "RUNG alarm2", 1))
	if err != nil || !rep.Same {
		// a rung rename changes no logic
		t.Fatalf("rename: err=%v report=%+v", err, rep)
	}
	rep, err = s.Edit(context.Background(), src)
	if err != nil || rep.Applied != Online || !rep.Verified {
		t.Fatalf("revert: err=%v report=%+v", err, rep)
	}
	if uploads() != 1 || len(f.imports) != 2 {
		t.Errorf("uploads=%d imports=%d, want 1 and 2", uploads(), len(f.imports))
	}
	_ = again
}

func TestSessionRefusesATagChange(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	_, c := newFakeAgent(t, same)
	s, err := Connect(context.Background(), Options{Client: c, Target: target()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	edited := strings.Replace(src, "    HiLevelAlm : BOOL;", "    HiLevelAlm : BOOL;\n    Extra : DINT;", 1)
	_, err = s.Edit(context.Background(), edited)
	var nd *NeedsDownloadError
	if !errors.As(err, &nd) {
		t.Fatalf("err = %v", err)
	}
}

func TestSessionOnAnEmptyControllerNeedsADownload(t *testing.T) {
	_, c := newFakeAgent(t, nil)
	_, err := Connect(context.Background(), Options{Client: c, Target: target()})
	var nd *NeedsDownloadError
	if !errors.As(err, &nd) || !nd.RoutineMissing {
		t.Fatalf("err = %v", err)
	}
}

// An ST program edits online as a whole-routine import and verifies by
// the routine's text.
func TestSessionEditsAnSTRoutine(t *testing.T) {
	src := "PROGRAM Calc\nVAR_EXTERNAL\n  A : REAL;\n  B : REAL;\nEND_VAR\nB := A * 2.0;\nEND_PROGRAM\n"
	same, _, _ := writer.WriteST(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	tgt := target()
	tgt.Language = "st"
	s, err := Connect(context.Background(), Options{Client: c, Target: tgt})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	rep, err := s.Edit(context.Background(), src)
	if err != nil || !rep.Same {
		t.Fatalf("same: err=%v report=%+v", err, rep)
	}
	edited := strings.Replace(src, "B := A * 2.0;", "B := A * 3.0;", 1)
	rep, err = s.Edit(context.Background(), edited)
	if err != nil || rep.Applied != Online || !rep.Verified {
		t.Fatalf("edit: err=%v report=%+v", err, rep)
	}
	if len(f.imports) != 0 || len(f.routineImports) != 1 || f.routineImports[0]["onlineOption"] != "FinalizeEdits" {
		t.Errorf("imports: rungs=%d routines=%v", len(f.imports), f.routineImports)
	}
}

// A program with user blocks is built in two steps: skeleton, then the
// program as a partial import.
func TestDeployBuildsAnAOIProjectInTwoSteps(t *testing.T) {
	motor, _ := os.ReadFile(filepath.Join("..", "writer", "testdata", "conformance", "aoi", "lib", "motor.ld"))
	src, _ := os.ReadFile(filepath.Join("..", "writer", "testdata", "conformance", "aoi", "Main.ld"))
	tgt := target()
	tgt.Libs = []string{string(motor)}
	f, c := newFakeAgent(t, nil)
	rep, err := Run(context.Background(), string(src), Options{Client: c, Target: tgt, Mode: Download})
	if err != nil || rep.Applied != Download || !rep.Verified {
		t.Fatalf("err=%v report=%+v", err, rep)
	}
	var partialImports, converts int
	for _, call := range f.calls {
		if strings.HasSuffix(call, "/partial-import") {
			partialImports++
		}
		if strings.HasSuffix(call, "/convert") {
			converts++
		}
	}
	if partialImports != 1 {
		t.Errorf("partial imports = %d, want 1 (the program over the skeleton)", partialImports)
	}
	if _, ok := f.files["deploy-"+""]; ok {
		t.Error("unexpected")
	}
	for name := range f.files {
		if strings.HasSuffix(name, ".skeleton.L5X") {
			if !strings.Contains(string(f.files[name]), "NOP()") || strings.Contains(string(f.files[name]), "MotorStarter(m101") {
				t.Error("the skeleton should carry a NOP routine and no call")
			}
		}
	}
}

// A project with side code has two programs with a MainRoutine: the
// program's and the side program "Nautilus". The session must edit the
// program's, whatever order the controller's routines come back in — a
// map — so this connects several times; before the fix about half the
// sessions picked Nautilus/MainRoutine and refused the edit as "does not
// run this program's routine yet" (found on Echo, #375).
func TestSessionEditsTheProgramNotTheSideProgram(t *testing.T) {
	src := demoSource(t)
	tgt := target()
	tgt.Side = writer.Side{Heartbeat: "Nautilus_Scan"}
	same, _, err := writer.Write(src, writer.Options{Controller: "DemoLine", Side: tgt.Side})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(same), `<Program Name="`+writer.SideProgram+`"`) {
		t.Fatal("fixture has no side program")
	}
	edited := strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm ( RunCmd )", 1)
	for i := 0; i < 12; i++ {
		_, c := newFakeAgent(t, same)
		s, err := Connect(context.Background(), Options{Client: c, Target: tgt})
		if err != nil {
			t.Fatal(err)
		}
		rep, err := s.Edit(context.Background(), edited)
		if err != nil || rep.Applied != Online || !rep.Verified || rep.Program != "MainProgram" {
			t.Fatalf("connect %d: err=%v report=%+v", i, err, rep)
		}
		// And the next edit compares with what the first left.
		rep, err = s.Edit(context.Background(), edited)
		if err != nil || !rep.Same {
			t.Fatalf("connect %d, again: err=%v report=%+v", i, err, rep)
		}
		_ = s.Close(context.Background())
	}
}

func keepTarget(t *testing.T) Target {
	o := keepOptions(t)
	tgt := target()
	tgt.PeriodMs, tgt.Libs, tgt.Inits, tgt.Tags, tgt.Descs, tgt.Side = o.PeriodMs, o.Libs, o.Inits, o.Tags, o.Descs, o.Side
	return tgt
}

// A download keeps the controller's values (#273): the upload comes before
// the import, and what is downloaded carries the controller's data.
func TestDownloadKeepsTheControllersValues(t *testing.T) {
	f, c := newFakeAgent(t, running(t))
	// A new rung, so there is something to download.
	src := strings.Replace(keepSource(t), "END_LD", "  RUNG extra\n    StopPB ( HiLevelAlm )\nEND_LD", 1)
	var lines []string
	rep, err := Run(context.Background(), src, Options{Client: c, Target: keepTarget(t), Mode: Download, ProgramMode: true,
		Log: func(format string, a ...any) { lines = append(lines, fmt.Sprintf(format, a...)) }})
	if err != nil || !rep.Verified {
		t.Fatalf("err = %v, report = %+v", err, rep)
	}
	if rep.Values == nil || len(rep.Values.Kept) != 12 {
		t.Fatalf("values = %+v", rep.Values)
	}
	upload, convert := -1, -1
	for i, call := range f.calls {
		if upload < 0 && strings.HasSuffix(call, "/upload-to-new") {
			upload = i
		}
		if convert < 0 && strings.HasSuffix(call, "/convert") {
			convert = i
		}
	}
	if upload < 0 || upload > convert {
		t.Errorf("the upload must come before the project's import: %v", f.calls)
	}
	uploads := 0
	for _, call := range f.calls {
		if strings.HasSuffix(call, "/upload-to-new") {
			uploads++
		}
	}
	if uploads != 2 {
		t.Errorf("%d uploads, want 2 (before, which also decides the values, and after)", uploads)
	}
	dl, _ := l5x.Parse(f.running)
	if v := tagIn(t, dl, "", "HiLevelSP").Value; v != 72.5 {
		t.Errorf("downloaded HiLevelSP = %v, want the controller's 72.5", v)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "tag values: 12 kept from the controller") {
		t.Errorf("log = %v", lines)
	}

	// --reset-values: the project's values, and no upload before the build.
	f, c = newFakeAgent(t, running(t))
	rep, err = Run(context.Background(), src, Options{Client: c, Target: keepTarget(t), Mode: Download, ResetValues: true, ProgramMode: true})
	if err != nil || rep.Values != nil {
		t.Fatalf("err = %v, values = %+v", err, rep.Values)
	}
	dl, _ = l5x.Parse(f.running)
	if v := tagIn(t, dl, "", "HiLevelSP").Value; v != 85.0 {
		t.Errorf("reset: HiLevelSP = %v, want the project's 85", v)
	}
}

// A build reports what a download would do with the values; an online
// edit does not look at them.
func TestBuildReportsTheValuePlan(t *testing.T) {
	_, c := newFakeAgent(t, running(t))
	o := keepTarget(t)
	o.Tags["SpareSP"], o.Inits["SpareSP"] = "REAL", 5.0
	src := strings.Replace(keepSource(t), "SpareSP    : DINT;", "SpareSP    : REAL;", 1)
	src = strings.Replace(src, "GE(SpareSP, 0)", "GE(SpareSP, 0.0)", 1)
	rep, err := Run(context.Background(), src, Options{Client: c, Target: o, Mode: BuildOnly})
	if err != nil || rep.Values == nil || len(rep.Values.Reset) != 1 || rep.Values.Reset[0] != "SpareSP: DINT → REAL" {
		t.Fatalf("err = %v, values = %+v", err, rep.Values)
	}
}

// A plain download to a controller in Run is refused as soon as the mode
// is read, before the import and build; the same answer as the SDK's
// late refusal (#391).
func TestDownloadInRunRefusedBeforeBuild(t *testing.T) {
	src := demoSource(t)
	for _, mode := range []string{"Run", "Remote Run"} {
		f, c := newFakeAgent(t, running(t))
		f.mode = mode
		f.startState = true
		rep, err := Run(context.Background(), src, Options{Client: c, Target: target(), Mode: Download})
		var se *StartStateError
		if !errors.As(err, &se) || se.Mode != mode {
			t.Fatalf("%s: err = %v (%T)", mode, err, err)
		}
		if rep == nil || rep.ModeBefore != mode {
			t.Errorf("%s: report = %+v", mode, rep)
		}
		for _, call := range f.calls {
			if strings.HasSuffix(call, "/build") || strings.HasSuffix(call, "/download") || strings.HasSuffix(call, "/save") {
				t.Errorf("%s: %s ran after the refusal; calls = %v", mode, call, f.calls)
			}
		}
		// Reset-values downloads are refused early too.
		if _, err := Run(context.Background(), src, Options{Client: c, Target: target(), Mode: Download, ResetValues: true}); !errors.As(err, &se) {
			t.Errorf("%s reset: err = %v", mode, err)
		}
	}
}

// Program mode asked for: no check, the download goes ahead. A mode that
// is not Run goes ahead too.
func TestDownloadProgramModeSkipsTheModeCheck(t *testing.T) {
	src := demoSource(t)
	f, c := newFakeAgent(t, running(t))
	f.startState = false
	rep, err := Run(context.Background(), src, Options{Client: c, Target: target(), Mode: Download, ProgramMode: true, ResetValues: true})
	if err != nil || rep.Applied != Download {
		t.Fatalf("err = %v, report = %+v", err, rep)
	}
	for _, call := range f.calls {
		if strings.HasSuffix(call, "/mode") && !strings.Contains(strings.Join(f.calls, " "), "/download") {
			t.Errorf("mode read before the download: %v", f.calls)
		}
	}
	// Program mode on the controller: a plain download proceeds.
	f2, c2 := newFakeAgent(t, running(t))
	f2.mode = "Program"
	if rep, err := Run(context.Background(), src, Options{Client: c2, Target: target(), Mode: Download}); err != nil || rep.Applied != Download {
		t.Fatalf("program mode: err = %v", err)
	}
}

// A mode read that fails falls through to today's behaviour: the download
// is tried, and the SDK's own late refusal still gets the same answer.
func TestDownloadModeReadFailureFallsThrough(t *testing.T) {
	src := demoSource(t)
	f, c := newFakeAgent(t, running(t))
	f.modeFails, f.startState = true, true
	_, err := Run(context.Background(), src, Options{Client: c, Target: target(), Mode: Download})
	var se *StartStateError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T)", err, err)
	}
	built := false
	for _, call := range f.calls {
		built = built || strings.HasSuffix(call, "/build")
	}
	if !built {
		t.Errorf("the download did not get as far as the build: %v", f.calls)
	}
	f.startState = false
	if rep, err := Run(context.Background(), src, Options{Client: c, Target: target(), Mode: Download}); err != nil || rep.Applied != Download {
		t.Fatalf("download with a failing mode read: err = %v", err)
	}
}
