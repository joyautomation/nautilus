package main

// Tests for the agent-backed `naut logix` verbs, against a fake agent.
//
// The ones that matter are the refusals. Two of these verbs can change a
// running controller, and the guardrails are in the flag parsing — so a
// guardrail that silently stopped working would be invisible until the day
// it mattered.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLogixd serves just enough for the CLI to get through a verb.
type fakeLogixd struct {
	t     *testing.T
	files map[string][]byte
	seen  []string // method+path, in order
	auth  []string // each request's Authorization header, in order
	// importRungs records the body of the rung import, which is where the
	// online-edit option lives.
	importRungs map[string]any
	// uploadToNew records the body of an upload-from-controller, which is
	// how an online edit gets the project it edits.
	uploadToNew map[string]any
	// convert records the body of the last convert, where drift's
	// detailedL5x choice travels.
	convert     map[string]any
	probeUsable bool
	// controllerL5X, when set, is the export a convert produces: what the
	// controller "holds" for drift.
	controllerL5X string
	// mode is the controller's mode; a mode change sets it.
	mode string
	// linxXML is the FT Linx configuration /v1/linx-config returns.
	linxXML string
	// uploadEntered, when set, is told of each upload-to-new as it
	// starts, and uploadHold holds it until closed: a session opening that
	// takes as long as a test needs.
	uploadEntered chan struct{}
	uploadHold    chan struct{}
	// hook, when set, sees each request before it is answered.
	hook func(r *http.Request)
}

func newFakeLogixd(t *testing.T) (*fakeLogixd, string, func()) {
	f := &fakeLogixd{t: t, files: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.seen = append(f.seen, r.Method+" "+r.URL.Path)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if f.hook != nil {
			f.hook(r)
		}
		w.Header().Set("Content-Type", "application/json")
		ok := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data, "events": []any{}})
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/files/"):
			name := strings.TrimPrefix(r.URL.Path, "/v1/files/")
			switch r.Method {
			case http.MethodPut:
				// One Read may return part of the body; read all of it.
				buf, _ := io.ReadAll(r.Body)
				f.files[name] = buf
				ok(map[string]any{"path": name, "bytes": len(buf)})
			case http.MethodGet:
				body, found := f.files[name]
				if !found {
					w.WriteHeader(404)
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": false,
						"error": map[string]any{"kind": "not_found", "message": name}})
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write(body)
			default:
				ok(map[string]any{"deleted": name})
			}
		case r.URL.Path == "/v1/health":
			ok(map[string]any{"service": "logixd", "version": "test", "sdkClient": "2.2.1109.0"})
		case r.URL.Path == "/v1/upload-to-new":
			if f.uploadEntered != nil {
				f.uploadEntered <- struct{}{}
			}
			if f.uploadHold != nil {
				<-f.uploadHold
			}
			_ = json.NewDecoder(r.Body).Decode(&f.uploadToNew)
			if dest, _ := f.uploadToNew["projectFilePath"].(string); dest != "" {
				f.files[dest] = []byte("UPLOADED-FROM-CONTROLLER")
			}
			ok(map[string]any{"project": f.uploadToNew["projectFilePath"]})
		case r.URL.Path == "/v1/workdir":
			ok(map[string]any{"workDir": `C:\logixd-work`})
		case r.URL.Path == "/v1/sessions" && r.Method == http.MethodPost:
			ok(map[string]any{"session": "S1", "project": "p"})
		case r.URL.Path == "/v1/sessions" && r.Method == http.MethodGet:
			ok([]any{})
		case r.URL.Path == "/v1/probe":
			ok(map[string]any{
				"usable": f.probeUsable,
				"gates": []any{
					map[string]any{"name": "sdk-service", "ok": true, "detail": "running"},
					map[string]any{"name": "live-create-project", "ok": f.probeUsable, "detail": "no activation",
						"remedy": "Check the FactoryTalk Local Directory before suspecting a licence."},
					// A gate after the failing one, so "underneath" is not just "last".
					map[string]any{"name": "interactive-session", "ok": true, "detail": "a user is logged on"},
				},
				"hint": "check FTACmdUtility listAvailable",
			})
		case r.URL.Path == "/v1/convert":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.convert = body
			out, _ := body["output"].(string)
			f.files[out] = []byte("<?xml version=\"1.0\"?><RSLogix5000Content/>")
			if f.controllerL5X != "" {
				f.files[out] = []byte(f.controllerL5X)
			}
			ok(map[string]any{"input": body["input"], "output": out, "bytes": len(f.files[out])})
		case strings.HasSuffix(r.URL.Path, "/import-rungs"):
			_ = json.NewDecoder(r.Body).Decode(&f.importRungs)
			ok(map[string]any{
				"xpath": f.importRungs["xpath"], "insertPosition": f.importRungs["insertPosition"],
				"replaceCount": f.importRungs["replaceCount"],
				"onlineOption": f.importRungs["onlineOption"], "elapsedMs": 7,
			})
		case strings.HasSuffix(r.URL.Path, "/comm-path"):
			ok(map[string]any{"commPath": "fake"})
		case strings.HasSuffix(r.URL.Path, "/online"), strings.HasSuffix(r.URL.Path, "/offline"):
			ok(map[string]any{"connected": "Online"})
		case strings.HasSuffix(r.URL.Path, "/mode"):
			if f.mode == "" {
				f.mode = "Run"
			}
			if r.Method == http.MethodPost {
				var body struct{ Mode string }
				_ = json.NewDecoder(r.Body).Decode(&body)
				f.mode = body.Mode
			}
			ok(map[string]any{"mode": f.mode})
		case r.URL.Path == "/v1/linx-config":
			ok(map[string]any{"dir": `C:\ProgramData\Rockwell\RSLinx Enterprise`,
				"files": []any{map[string]any{"name": "RSLinxNG.xml", "xml": f.linxXML}}})
		case strings.HasSuffix(r.URL.Path, "/download"):
			ok(map[string]any{"downloaded": "p", "elapsedMs": 9})
		case strings.HasSuffix(r.URL.Path, "/build"):
			ok(map[string]any{"target": "EchoController", "elapsedMs": 11})
		case strings.HasSuffix(r.URL.Path, "/save"):
			ok(map[string]any{"saved": "p"})
		default:
			ok(map[string]any{})
		}
	}))
	return f, srv.URL, srv.Close
}

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// --accept and --finalize change a RUNNING controller. Without a comm path
// the import is offline and the SDK IGNORES the option — the edit would
// land as pending edits nobody accepted, and the command would report
// success. Refusing is the only safe behaviour.
func TestLogixPushRefusesOnlineOptionsWithoutACommPath(t *testing.T) {
	_, url, stop := newFakeLogixd(t)
	defer stop()
	proj := writeTemp(t, "p.ACD", "x")
	rungs := writeTemp(t, "r.L5X", "<x/>")

	for _, flag := range []string{"--accept", "--finalize"} {
		code := runLogixPush([]string{
			"--agent", url, "--program", "MainProgram", "--routine", "MainRoutine",
			flag, proj, rungs,
		})
		if code == 0 {
			t.Errorf("%s without --comm-path was accepted", flag)
		}
	}
}

func TestLogixPushRefusesBothOnlineOptions(t *testing.T) {
	_, url, stop := newFakeLogixd(t)
	defer stop()
	proj := writeTemp(t, "p.ACD", "x")
	rungs := writeTemp(t, "r.L5X", "<x/>")
	if code := runLogixPush([]string{
		"--agent", url, "--program", "P", "--routine", "R",
		"--accept", "--finalize", "--comm-path", "c", proj, rungs,
	}); code == 0 {
		t.Error("--accept and --finalize together were accepted")
	}
}

func TestLogixPushRequiresProgramAndRoutine(t *testing.T) {
	_, url, stop := newFakeLogixd(t)
	defer stop()
	proj := writeTemp(t, "p.ACD", "x")
	rungs := writeTemp(t, "r.L5X", "<x/>")
	if code := runLogixPush([]string{"--agent", url, proj, rungs}); code == 0 {
		t.Error("a push with no routine named was accepted")
	}
}

// The online option must reach the agent exactly as chosen — a silent
// downgrade to LeaveEdits would leave the change pending on the controller
// while the command reported success.
func TestLogixPushSendsTheChosenOnlineOption(t *testing.T) {
	cases := []struct{ flag, want string }{
		{"--finalize", "FinalizeEdits"},
		{"--accept", "AcceptEdits"},
		{"", "LeaveEdits"},
	}
	for _, tc := range cases {
		f, url, stop := newFakeLogixd(t)
		proj := writeTemp(t, "p.ACD", "x")
		rungs := writeTemp(t, "r.L5X", "<x/>")
		args := []string{"--agent", url, "--program", "MainProgram", "--routine", "MainRoutine",
			"--at", "3", "--replace", "2"}
		online := tc.flag != ""
		if online {
			args = append(args, tc.flag, "--comm-path", "backplane\\0")
		}
		// An online edit edits the RUNNING program, so it takes no
		// project file; an offline import edits a project on disk.
		if online {
			args = append(args, rungs)
		} else {
			args = append(args, proj, rungs)
		}
		code := runLogixPush(args)
		stop()
		if code != 0 {
			t.Fatalf("%s: exit %d", tc.want, code)
		}
		if got := f.importRungs["onlineOption"]; got != tc.want {
			t.Errorf("onlineOption = %v, want %s", got, tc.want)
		}
		if f.importRungs["insertPosition"] != float64(3) || f.importRungs["replaceCount"] != float64(2) {
			t.Errorf("%s: position/replace = %v/%v", tc.want,
				f.importRungs["insertPosition"], f.importRungs["replaceCount"])
		}
		want := `Controller/Programs/Program[@Name='MainProgram']/Routines/Routine[@Name='MainRoutine']`
		if f.importRungs["xpath"] != want {
			t.Errorf("xpath = %v", f.importRungs["xpath"])
		}
		// The contract that matters: online, the project comes from the
		// CONTROLLER. A project file on disk cannot go online, so sending
		// one fails with an error about physical addressing that says
		// nothing about the cause.
		if online {
			if f.uploadToNew == nil {
				t.Errorf("%s: online edit did not upload from the controller", tc.want)
			} else if got := f.uploadToNew["commPath"]; got != `backplane\0` {
				t.Errorf("%s: uploaded from commPath %v", tc.want, got)
			}
		} else if f.uploadToNew != nil {
			t.Errorf("offline import should not touch the controller")
		}
	}
}

// convert has to move the file both ways: a local input the agent cannot
// see, and a result the caller cannot see. Forgetting either half turns
// into "it worked but there is no output file".
func TestLogixConvertStagesAndFetches(t *testing.T) {
	f, url, stop := newFakeLogixd(t)
	defer stop()
	in := writeTemp(t, "demo.ACD", "ACD-BYTES")
	out := filepath.Join(t.TempDir(), "demo.L5X")

	if code := runLogixConvert([]string{"--agent", url, in, out}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("convert reported success but wrote no output: %v", err)
	}
	if !strings.Contains(string(raw), "RSLogix5000Content") {
		t.Errorf("output = %q", raw)
	}
	var uploaded bool
	for name, body := range f.files {
		if strings.HasSuffix(name, "demo.ACD") && string(body) == "ACD-BYTES" {
			uploaded = true
		}
	}
	if !uploaded {
		t.Error("the input was never uploaded to the agent")
	}
}

// probe exits non-zero when the SDK is unusable, so CI and a shell script
// can both branch on it — but it must still PRINT the gates, because the
// failing gate is the whole point.
func TestLogixProbeExitCodeFollowsUsability(t *testing.T) {
	f, url, stop := newFakeLogixd(t)
	defer stop()

	f.probeUsable = false
	if code := runLogixProbe([]string{"--agent", url}); code != 1 {
		t.Errorf("unusable SDK: exit %d, want 1", code)
	}
	f.probeUsable = true
	if code := runLogixProbe([]string{"--agent", url}); code != 0 {
		t.Errorf("usable SDK: exit %d, want 0", code)
	}
}

func TestLogixBuildRejectsAnUnknownTarget(t *testing.T) {
	_, url, stop := newFakeLogixd(t)
	defer stop()
	proj := writeTemp(t, "p.ACD", "x")
	if code := runLogixBuild([]string{"--agent", url, "--target", "nonsense", proj}); code == 0 {
		t.Error("an unknown build target was accepted")
	}
	if code := runLogixBuild([]string{"--agent", url, "--target", "echo", proj}); code != 0 {
		t.Error("--target echo should be accepted")
	}
}

// A download stops a controller and resets its tags. The only thing
// standing between a recalled shell command and an outage is this refusal,
// so it is worth more test than the code it guards.
func TestLogixDownloadRefusesWithoutConfirmation(t *testing.T) {
	f, url, stop := newFakeLogixd(t)
	defer stop()
	proj := writeTemp(t, "p.ACD", "x")

	if code := runLogixDownload([]string{
		"--agent", url, "--comm-path", "backplane\\0", proj,
	}); code == 0 {
		t.Fatal("a download without --yes was accepted")
	}
	// And it must not have touched the agent at all.
	for _, seen := range f.seen {
		if strings.Contains(seen, "download") {
			t.Errorf("the refusal still reached the agent: %s", seen)
		}
	}
}

func TestLogixDownloadRequiresACommPath(t *testing.T) {
	_, url, stop := newFakeLogixd(t)
	defer stop()
	proj := writeTemp(t, "p.ACD", "x")
	if code := runLogixDownload([]string{"--agent", url, "--yes", proj}); code == 0 {
		t.Error("a download with no --comm-path was accepted")
	}
}

// A dead agent is the most common failure in practice, and the message has
// to say where nautilus looked and what to start.
func TestLogixAgentReportsAnUnreachableAgentHelpfully(t *testing.T) {
	out, code := captureStderr(t, func() int { return runLogixAgent([]string{"--agent", "http://127.0.0.1:1"}) })
	if code == 0 {
		t.Error("an unreachable agent should be an error")
	}
	// The wording the guide quotes, the URL it tried, and what to start.
	for _, want := range []string{"logixd: unreachable", "127.0.0.1:1", "refused", "Is logixd running?"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr missing %q:\n%s", want, out)
		}
	}
}

// A failing gate has to tell the reader what to DO. The detail says what is
// wrong; without the remedy beside it they go looking through a guide, which
// is exactly where this stack has burned the most time.
func TestLogixProbePrintsTheRemedyForAFailingGate(t *testing.T) {
	f, url, stop := newFakeLogixd(t)
	defer stop()
	f.probeUsable = false

	out := captureStdout(t, func() int { return runLogixProbe([]string{"--agent", url}) })

	if !strings.Contains(out, "FAIL") {
		t.Fatalf("expected a failing gate in:\n%s", out)
	}
	if !strings.Contains(out, "FactoryTalk Local Directory") {
		t.Errorf("the remedy should print under the gate that failed:\n%s", out)
	}
	// Underneath it: the line after the failing gate is the remedy,
	// indented under it.
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "FAIL") {
			continue
		}
		if !strings.Contains(l, "live-create-project") {
			t.Errorf("unexpected failing gate %q", l)
		}
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "        Check the FactoryTalk") {
			t.Errorf("the line under the failing gate is not its remedy:\n%s", out)
		}
	}
	// A gate that PASSED must not carry advice — noise on a healthy probe
	// trains people to ignore the output.
	sdkLine := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "sdk-service") {
			sdkLine = l
		}
	}
	if sdkLine == "" || !strings.HasPrefix(sdkLine, "ok") {
		t.Errorf("sdk-service should have passed: %q", sdkLine)
	}
}

// Drift compares the controller's export with the repo file, and the two
// are only comparable when they are the same KIND of export: a detailed one
// carries megabytes of product-defined types a basic one omits. So the
// export drift asks the agent for must follow the repo file's own kind.
func TestLogixDriftAsksForTheRepoFilesExportKind(t *testing.T) {
	const base = "References NoRawData L5KData DecoratedData Context Dependencies ForceProtectedEncoding AllProjDocTrans"
	for _, c := range []struct {
		name, options string
		want          bool
	}{
		{"basic repo file", base, false},
		{"detailed repo file", base + " ProductDefinedTypes", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, url, stop := newFakeLogixd(t)
			defer stop()
			repo := writeTemp(t, "repo.L5X", `<?xml version="1.0"?><RSLogix5000Content SchemaRevision="1.0" TargetType="Controller" ExportOptions="`+c.options+`"><Controller Name="C"/></RSLogix5000Content>`)
			captureStdout(t, func() int {
				return runLogixDrift([]string{"--agent", url, "--comm-path", "AB_ETH-1/1.2.3.4/Backplane/0", repo})
			})
			if got, ok := f.convert["detailedL5x"].(bool); !ok || got != c.want {
				t.Errorf("convert asked for detailedL5x = %v, want %v (body %v)", f.convert["detailedL5x"], c.want, f.convert)
			}
		})
	}
}
