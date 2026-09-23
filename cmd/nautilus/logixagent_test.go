package main

// Tests for the agent-backed `nautilus logix` verbs, against a fake agent.
//
// The ones that matter are the refusals. Two of these verbs can change a
// running controller, and the guardrails are in the flag parsing — so a
// guardrail that silently stopped working would be invisible until the day
// it mattered.

import (
	"encoding/json"
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
	// importRungs records the body of the rung import, which is where the
	// online-edit option lives.
	importRungs map[string]any
	// uploadToNew records the body of an upload-from-controller, which is
	// how an online edit gets the project it edits.
	uploadToNew map[string]any
	probeUsable bool
}

func newFakeLogixd(t *testing.T) (*fakeLogixd, string, func()) {
	f := &fakeLogixd{t: t, files: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.seen = append(f.seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		ok := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data, "events": []any{}})
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/files/"):
			name := strings.TrimPrefix(r.URL.Path, "/v1/files/")
			switch r.Method {
			case http.MethodPut:
				buf := make([]byte, 1<<20)
				n, _ := r.Body.Read(buf)
				f.files[name] = append([]byte(nil), buf[:n]...)
				ok(map[string]any{"path": name, "bytes": n})
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
					map[string]any{"name": "live-create-project", "ok": f.probeUsable, "detail": "no activation"},
				},
				"hint": "check FTACmdUtility listAvailable",
			})
		case r.URL.Path == "/v1/convert":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			out, _ := body["output"].(string)
			f.files[out] = []byte("<?xml version=\"1.0\"?><RSLogix5000Content/>")
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
			ok(map[string]any{"mode": "Run"})
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
	code := runLogixAgent([]string{"--agent", "http://127.0.0.1:1"})
	if code == 0 {
		t.Error("an unreachable agent should be an error")
	}
}
