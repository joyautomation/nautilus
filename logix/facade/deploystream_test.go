package facade

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/logix/deploy"
)

// postDeploy sends POST /api/logix/deploy and returns the status and every
// NDJSON line of the answer (a plain JSON error is one line too).
func postDeploy(t *testing.T, url string, body map[string]any) (int, []map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	res, err := http.Post(url+"/api/logix/deploy", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var lines []map[string]any
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("not JSON: %q", sc.Text())
		}
		lines = append(lines, m)
	}
	return res.StatusCode, lines
}

func events(lines []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["event"] == kind {
			out = append(out, l)
		}
	}
	return out
}

// The editor's Push Online Edit: progress lines as the deploy logs them,
// then the report; GET /api/program serves the new source and says it is a
// Logix target, so the extension knows which flow it is in.
func TestDeployStreamOnlineEdit(t *testing.T) {
	url := startPlaneWith(t, &ProgramPlane{
		Source: ladderA, Controller: "DemoLine", CommPath: `AB_ETH-1\10.0.0.5\Backplane\0`,
		Deploy: func(_ context.Context, src string, progress func(string)) (*deploy.Report, error) {
			progress("session open and online")
			progress("online edit live and verified")
			return &deploy.Report{Applied: deploy.Online, Replaced: 2, Verified: true, ModeBefore: "Run", ModeAfter: "Run"}, nil
		},
	})
	info := getProgram(t, url)
	if info["target"] != "logix" || info["controller"] != "DemoLine" || info["canDownload"] != false || !strings.Contains(info["commPath"].(string), "10.0.0.5") {
		t.Fatalf("info = %v", info)
	}
	code, lines := postDeploy(t, url, map[string]any{"mode": "online", "source": ladderB, "baseHash": info["hash"]})
	if code != 200 {
		t.Fatalf("%d %v", code, lines)
	}
	prog := events(lines, "progress")
	if len(prog) != 3 || !strings.Contains(prog[0]["message"].(string), "online edit on AB_ETH-1") || prog[1]["message"] != "session open and online" {
		t.Errorf("progress = %v", prog)
	}
	done := events(lines, "done")
	if len(done) != 1 || done[0]["applied"] != "online edit" || done[0]["verified"] != true || done[0]["mode"] != "Run" {
		t.Fatalf("done = %v (all: %v)", done, lines)
	}
	if lines[len(lines)-1]["event"] != "done" {
		t.Error("the report is not the last line")
	}
	for _, k := range []string{"valuesKept", "resets"} {
		if _, has := done[0][k]; has {
			t.Errorf("an online edit's done carries the download-only field %q: %v", k, done[0])
		}
	}
	if after := getProgram(t, url); after["source"] != ladderB || after["hash"] != done[0]["hash"] {
		t.Errorf("after = %v", after)
	}
}

// A download stops the controller: refused without "yes", refused where
// the server has no download, and otherwise run with the Program-mode
// choice the editor sent.
func TestDeployStreamDownload(t *testing.T) {
	var calls int
	var gotMode, gotReset bool
	plane := &ProgramPlane{
		Source: ladderA, CommPath: `AB_ETH-1\10.0.0.5\Backplane\0`,
		Deploy: func(context.Context, string, func(string)) (*deploy.Report, error) {
			t.Error("a download ran the online-edit path")
			return &deploy.Report{}, nil
		},
		Download: func(_ context.Context, src string, o DownloadOptions, progress func(string)) (*deploy.Report, error) {
			calls++
			gotMode, gotReset = o.ProgramMode, o.ResetValues
			progress("imported and built")
			rep := &deploy.Report{Applied: deploy.Download, Verified: true, ModeBefore: "Run", ModeAfter: "Program"}
			if !o.ResetValues {
				rep.Values = &deploy.ValuePlan{Kept: []string{"HiLevelSP", "SpareSP"}, Reset: []string{"Spare2: DINT → REAL"}}
			}
			return rep, nil
		},
	}
	url := startPlaneWith(t, plane)
	if getProgram(t, url)["canDownload"] != true {
		t.Fatal("canDownload is false with a Download configured")
	}

	code, lines := postDeploy(t, url, map[string]any{"mode": "download", "source": ladderB})
	if code != 400 || !strings.Contains(lines[0]["error"].(string), "STOPS the controller") || calls != 0 {
		t.Fatalf("no yes: %d %v (calls %d)", code, lines, calls)
	}

	code, lines = postDeploy(t, url, map[string]any{"mode": "download", "source": ladderB, "yes": true, "programMode": true})
	if code != 200 || calls != 1 || !gotMode {
		t.Fatalf("download: %d %v (calls %d, programMode %v)", code, lines, calls, gotMode)
	}
	if p := events(lines, "progress"); len(p) < 2 || !strings.Contains(p[0]["message"].(string), "download to AB_ETH-1\\10.0.0.5") {
		t.Errorf("progress = %v", p)
	}
	done := events(lines, "done")
	if len(done) != 1 || done[0]["applied"] != "download" || done[0]["mode"] != "Program" || done[0]["modeBefore"] != "Run" {
		t.Fatalf("done = %v", lines)
	}
	if done[0]["valuesKept"] != float64(2) || fmt.Sprint(done[0]["resets"]) != "[Spare2: DINT → REAL]" || gotReset {
		t.Errorf("values: done = %v, resetValues %v", done[0], gotReset)
	}
	if getProgram(t, url)["source"] != ladderB {
		t.Error("a download did not become the served source")
	}
	_, lines = postDeploy(t, url, map[string]any{"mode": "download", "source": ladderA, "yes": true, "resetValues": true})
	if done := events(lines, "done"); !gotReset || len(done) != 1 || done[0]["valuesKept"] != float64(-1) {
		t.Errorf("resetValues: %v (passed %v)", lines, gotReset)
	}

	noDL := startPlaneWith(t, &ProgramPlane{Source: ladderA, Deploy: plane.Deploy})
	if code, lines := postDeploy(t, noDL, map[string]any{"mode": "download", "source": ladderB, "yes": true}); code != 501 {
		t.Errorf("no Download configured: %d %v", code, lines)
	}
}

// A refusal from the controller side arrives as the stream's error event,
// with the status the PUT would have answered; nothing changes.
func TestDeployStreamRefusal(t *testing.T) {
	url := startPlaneWith(t, &ProgramPlane{Source: ladderA,
		Deploy: func(_ context.Context, _ string, progress func(string)) (*deploy.Report, error) {
			progress("comparing with the controller")
			return &deploy.Report{}, &deploy.NeedsDownloadError{Diffs: []string{"tag X: only on the second side"}}
		}})
	code, lines := postDeploy(t, url, map[string]any{"mode": "online", "source": ladderB})
	if code != 200 {
		t.Fatalf("%d %v", code, lines)
	}
	errs := events(lines, "error")
	if len(errs) != 1 || errs[0]["status"] != float64(422) || !strings.Contains(errs[0]["error"].(string), "Download Project to Logix Controller") {
		t.Fatalf("lines = %v", lines)
	}
	if getProgram(t, url)["source"] != ladderA {
		t.Error("a refused edit changed the served source")
	}
}

// What needs no controller is answered before the stream: a bad mode, a
// stale base, a second deploy while one runs, and a serve with no project.
func TestDeployStreamUpfrontRefusals(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	url := startPlaneWith(t, &ProgramPlane{Source: ladderA,
		Deploy: func(context.Context, string, func(string)) (*deploy.Report, error) {
			started <- struct{}{}
			<-release
			return &deploy.Report{Applied: deploy.Online}, nil
		}})
	if code, _ := postDeploy(t, url, map[string]any{"mode": "build", "source": ladderB}); code != 400 {
		t.Errorf("bad mode: %d", code)
	}
	if code, lines := postDeploy(t, url, map[string]any{"mode": "online", "source": ladderB, "baseHash": "deadbeef0000"}); code != 409 || !strings.Contains(lines[0]["error"].(string), "changed since your base") {
		t.Errorf("stale base: %d %v", code, lines)
	}
	first := make(chan int, 1)
	go func() {
		raw, _ := json.Marshal(map[string]any{"mode": "online", "source": ladderB})
		res, err := http.Post(url+"/api/logix/deploy", "application/json", bytes.NewReader(raw))
		if err != nil {
			first <- 0
			return
		}
		_, _ = bufio.NewReader(res.Body).ReadString(0)
		res.Body.Close()
		first <- res.StatusCode
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first deploy never started")
	}
	if code, lines := postDeploy(t, url, map[string]any{"mode": "online", "source": ladderB}); code != 409 || !strings.Contains(lines[0]["error"].(string), "already in progress") {
		t.Errorf("second deploy: %d %v", code, lines)
	}
	close(release)
	if code := <-first; code != 200 {
		t.Errorf("the first deploy: %d", code)
	}

	bare := startPlaneWith(t, nil)
	if code, lines := postDeploy(t, bare, map[string]any{"mode": "online", "source": ladderB}); code != 404 || !strings.Contains(lines[0]["error"].(string), "--project") {
		t.Errorf("no project: %d %v", code, lines)
	}
}

// streamReader reads POST /api/logix/deploy's answer one line at a time,
// so a test can see what has arrived while the deploy is still running.
type streamReader struct {
	t   *testing.T
	res *http.Response
	sc  *bufio.Scanner
}

func openDeploy(t *testing.T, url string, body map[string]any) *streamReader {
	t.Helper()
	raw, _ := json.Marshal(body)
	res, err := http.Post(url+"/api/logix/deploy", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return &streamReader{t: t, res: res, sc: bufio.NewScanner(res.Body)}
}

// next is the next line, or nil at the end of the stream.
func (s *streamReader) next() map[string]any {
	s.t.Helper()
	got := make(chan map[string]any, 1)
	go func() {
		if !s.sc.Scan() {
			got <- nil
			return
		}
		var m map[string]any
		if err := json.Unmarshal(s.sc.Bytes(), &m); err != nil {
			m = map[string]any{"event": "not JSON", "raw": s.sc.Text()}
		}
		got <- m
	}()
	select {
	case m := <-got:
		return m
	case <-time.After(5 * time.Second):
		s.t.Fatal("no line within 5 s")
		return nil
	}
}

// A download's report goes out the moment it has verified; reopening the
// online session for the next edit follows on the same stream, and the
// plane takes the next deploy meanwhile (#379: the report arrived ~50 s
// late, after the session had reopened).
func TestDeployStreamReportsADownloadBeforeItsWarmUp(t *testing.T) {
	warmStarted, warmRelease := make(chan struct{}), make(chan struct{})
	url := startPlaneWith(t, &ProgramPlane{
		Source: ladderA, CommPath: `AB_ETH-1\10.0.0.5\Backplane\0`,
		Deploy: func(_ context.Context, _ string, progress func(string)) (*deploy.Report, error) {
			progress("edit while the session warms")
			return &deploy.Report{Applied: deploy.Online, Verified: true}, nil
		},
		Download: func(_ context.Context, _ string, _ DownloadOptions, progress func(string)) (*deploy.Report, error) {
			progress("verified")
			return &deploy.Report{Applied: deploy.Download, Verified: true, ModeAfter: "Program"}, nil
		},
		Warm: func(_ context.Context, progress func(string)) {
			close(warmStarted)
			<-warmRelease
			progress("session open and online")
		},
	})
	s := openDeploy(t, url, map[string]any{"mode": "download", "source": ladderB, "yes": true, "programMode": true})
	var done map[string]any
	for done == nil {
		l := s.next()
		if l == nil {
			t.Fatal("the stream ended without a report")
		}
		if l["event"] == "done" {
			done = l
		}
	}
	// The report is here and the warm-up has not finished.
	select {
	case <-warmStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the warm-up never started")
	}
	if done["applied"] != "download" || done["verified"] != true {
		t.Fatalf("done = %v", done)
	}
	if info := getProgram(t, url); info["error"] != nil || info["source"] != ladderB {
		t.Errorf("during the warm-up GET /api/program = %v", info)
	}
	if code, lines := postDeploy(t, url, map[string]any{"mode": "online", "source": ladderA}); code != 200 || len(events(lines, "done")) != 1 {
		t.Errorf("an edit during the warm-up: %d %v", code, lines)
	}
	close(warmRelease)
	if l := s.next(); l == nil || l["event"] != "progress" || l["message"] != "session open and online" {
		t.Errorf("after the report: %v", l)
	}
	if l := s.next(); l != nil {
		t.Errorf("more after the warm-up: %v", l)
	}
}

// An online edit and a plain PUT have no warm-up; a download without Warm
// ends with its report.
func TestDeployStreamWarmsOnlyAfterADownload(t *testing.T) {
	warms := 0
	plane := &ProgramPlane{
		Source: ladderA,
		Deploy: func(context.Context, string, func(string)) (*deploy.Report, error) {
			return &deploy.Report{Applied: deploy.Online, Verified: true}, nil
		},
		Download: func(context.Context, string, DownloadOptions, func(string)) (*deploy.Report, error) {
			return &deploy.Report{}, &deploy.VerifyError{Diffs: []string{"rung 0"}}
		},
		Warm: func(context.Context, func(string)) { warms++ },
	}
	url := startPlaneWith(t, plane)
	postDeploy(t, url, map[string]any{"mode": "online", "source": ladderB})
	postDeploy(t, url, map[string]any{"mode": "download", "source": ladderB, "yes": true}) // failed
	if warms != 0 {
		t.Errorf("warmed %d time(s) without a download that went through", warms)
	}
}

// The headers and a first line go out at once, before the deploy says
// anything: a warm edit's first word is otherwise its report (#379).
func TestDeployStreamFlushesAtOnce(t *testing.T) {
	release := make(chan struct{})
	url := startPlaneWith(t, &ProgramPlane{Source: ladderA, Controller: "DemoLine",
		Deploy: func(context.Context, string, func(string)) (*deploy.Report, error) {
			<-release
			return &deploy.Report{Applied: deploy.Online, Verified: true}, nil
		}})
	defer close(release)
	opened := make(chan *streamReader, 1)
	go func() { opened <- openDeploy(t, url, map[string]any{"mode": "online", "source": ladderB}) }()
	var s *streamReader
	select {
	case s = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("no headers while the deploy runs")
	}
	if l := s.next(); l["event"] != "progress" || l["message"] != "starting an online edit on DemoLine" {
		t.Errorf("first line = %v", l)
	}
}

// elapsedMs is the whole wait, from the request to the report — the
// session a first edit opens, the warm-up an edit waits behind — and
// deployMs the deploy's own part (#379: a 50 s first edit said 3.0 s).
func TestDeployStreamElapsedIsTheWholeWait(t *testing.T) {
	url := startPlaneWith(t, &ProgramPlane{Source: ladderA,
		Deploy: func(context.Context, string, func(string)) (*deploy.Report, error) {
			time.Sleep(150 * time.Millisecond) // the session opening
			return &deploy.Report{Applied: deploy.Online, Verified: true, Elapsed: 20 * time.Millisecond}, nil
		}})
	_, lines := postDeploy(t, url, map[string]any{"mode": "online", "source": ladderB})
	done := events(lines, "done")
	if len(done) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	if ms := done[0]["elapsedMs"].(float64); ms < 150 {
		t.Errorf("elapsedMs = %v, want the whole 150+ ms", ms)
	}
	if done[0]["deployMs"] != float64(20) {
		t.Errorf("deployMs = %v", done[0]["deployMs"])
	}
}

// A plain download refused because the controller is in Run: a 409 with
// the way out, needsProgramMode for the editor's button, and the SDK's
// raw text as detail (#379).
func TestDeployStreamStartStateRefusal(t *testing.T) {
	raw := fmt.Errorf("download: %w\n  status [Download]: Preparing the download", fmt.Errorf("logixd: operation_failed (OperationFailedException): RxE_INVALID_START_STATE - Invalid start state.  Internal error."))
	url := startPlaneWith(t, &ProgramPlane{Source: ladderA,
		Deploy: func(context.Context, string, func(string)) (*deploy.Report, error) { return &deploy.Report{}, nil },
		Download: func(context.Context, string, DownloadOptions, func(string)) (*deploy.Report, error) {
			return &deploy.Report{}, &deploy.StartStateError{Mode: "Run", Raw: raw}
		}})
	_, lines := postDeploy(t, url, map[string]any{"mode": "download", "source": ladderB, "yes": true})
	errs := events(lines, "error")
	if len(errs) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	e := errs[0]
	if e["status"] != float64(409) || e["needsProgramMode"] != true {
		t.Errorf("error = %v", e)
	}
	if msg := e["error"].(string); !strings.Contains(msg, "in Run mode") || !strings.Contains(msg, "Switch to Program Mode and Download") || strings.Contains(msg, "RxE_") {
		t.Errorf("message = %q", msg)
	}
	if d, _ := e["detail"].(string); !strings.Contains(d, "RxE_INVALID_START_STATE") || !strings.Contains(d, "Preparing the download") {
		t.Errorf("detail = %q", d)
	}
	if code, msg := deployError(&deploy.NeedsDownloadError{}); code != 422 || strings.Contains(msg, "Program Mode") {
		t.Errorf("other refusals changed: %d %q", code, msg)
	}
}
