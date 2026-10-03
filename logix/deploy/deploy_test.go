package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	mode    string
}

func newFakeAgent(t *testing.T, running []byte) (*fakeAgent, *logixd.Client) {
	f := &fakeAgent{t: t, files: map[string][]byte{}, running: running, mode: "Run"}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, logixd.New(srv.URL, "")
}

func (f *fakeAgent) serve(w http.ResponseWriter, r *http.Request) {
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
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
	case strings.HasSuffix(r.URL.Path, "/mode"):
		ok(map[string]any{"mode": f.mode})
	case strings.HasSuffix(r.URL.Path, "/import-rungs"):
		f.imports = append(f.imports, body)
		f.running = f.generated()
		ok(map[string]any{"insertPosition": body["insertPosition"], "replaceCount": body["replaceCount"], "onlineOption": body["onlineOption"], "elapsedMs": 3})
	case strings.HasSuffix(r.URL.Path, "/download"):
		f.running = f.generated()
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
func (f *fakeAgent) generated() []byte {
	for name, raw := range f.files {
		if strings.HasSuffix(name, ".L5X") && !strings.HasSuffix(name, ".rungs.L5X") && !strings.Contains(name, "before") && !strings.Contains(name, "after") {
			return raw
		}
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
	if !errors.As(err, &de) || de.Diags[0].Rule != "logix/fb" {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("the agent was called despite the diagnostic: %v", f.calls)
	}
}
