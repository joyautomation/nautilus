package facade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/logix/deploy"
)

// entriesFor reads the runtime audit journal (GET /api/audit) for one action.
func entriesFor(t *testing.T, url, action string) []map[string]any {
	t.Helper()
	res, err := http.Get(url + "/api/audit?action=" + action)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Entries
}

func waitEntries(t *testing.T, url, action string, n int) []map[string]any {
	t.Helper()
	var es []map[string]any
	for i := 0; i < 100; i++ {
		if es = entriesFor(t, url, action); len(es) >= n {
			time.Sleep(50 * time.Millisecond) // a second entry would show now
			return entriesFor(t, url, action)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return es
}

// A deploy through the facade is journalled once, with who, what and how
// it ended (#709): accepted, failed, and refused.
func TestDeployIsJournalled(t *testing.T) {
	var fail bool
	url := startPlaneWith(t, &ProgramPlane{
		Source: ladderA, Controller: "DemoLine",
		Deploy: func(_ context.Context, _ string, _ func(string)) (*deploy.Report, error) {
			if fail {
				return nil, errors.New("controller said no")
			}
			return &deploy.Report{Applied: deploy.Online, Verified: true}, nil
		},
	})
	if code, _ := postDeploy(t, url, map[string]any{"mode": "online", "source": ladderB}); code != 200 {
		t.Fatalf("deploy = %d", code)
	}
	es := waitEntries(t, url, "logix.deploy", 1)
	if len(es) != 1 || es[0]["result"] != "ok" || es[0]["target"] != "DemoLine" || es[0]["user"] == "" {
		t.Fatalf("a good deploy: %v", es)
	}
	if d, _ := es[0]["detail"].(map[string]any); d["mode"] != "online" || d["sha256"] == nil {
		t.Fatalf("detail = %v; want mode and the source's hash", es[0]["detail"])
	}
	fail = true
	postDeploy(t, url, map[string]any{"mode": "online", "source": ladderB})
	es = waitEntries(t, url, "logix.deploy", 2)
	if len(es) != 2 || es[0]["result"] != "error" {
		t.Fatalf("a deploy the controller refused streams an error line (status 200): the entry says %v", es)
	}
	postDeploy(t, url, map[string]any{"mode": "download", "source": ladderB}) // no "yes" (and no download here)
	es = waitEntries(t, url, "logix.deploy", 3)
	if len(es) != 3 || es[0]["result"] != "error" { // a 501: this server does not download
		t.Fatalf("a refused deploy: %v", es)
	}
}

// A client that hangs up mid-deploy does not stop the entry: the deploy's
// own outcome is journalled, once.
func TestDeployJournalledWhenTheClientHangsUp(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	url := startPlaneWith(t, &ProgramPlane{
		Source: ladderA, Controller: "DemoLine",
		Deploy: func(_ context.Context, _ string, progress func(string)) (*deploy.Report, error) {
			progress("working")
			close(started)
			<-release // outlives the client
			return &deploy.Report{Applied: deploy.Online, Verified: true}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	raw, _ := json.Marshal(map[string]any{"mode": "online", "source": ladderB})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/api/logix/deploy", bytes.NewReader(raw))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel() // hang up
	res.Body.Close()
	time.Sleep(100 * time.Millisecond)
	if es := entriesFor(t, url, "logix.deploy"); len(es) != 0 {
		t.Fatalf("an entry before the deploy ended: %v", es)
	}
	close(release)
	es := waitEntries(t, url, "logix.deploy", 1)
	if len(es) != 1 || es[0]["result"] != "ok" {
		t.Fatalf("after a hang-up: %v; want one entry with the deploy's own result", es)
	}
}

// A program edit through the facade is journalled, and so is a refused one
// (no project: the locked stubs).
func TestProgramEditIsJournalled(t *testing.T) {
	url := startPlaneWith(t, &ProgramPlane{
		Source: ladderA, Controller: "DemoLine",
		Deploy: func(_ context.Context, _ string, _ func(string)) (*deploy.Report, error) {
			return &deploy.Report{Applied: deploy.Online, Verified: true}, nil
		},
	})
	if code, _ := putProgram(t, url, ladderB, ""); code != 200 {
		t.Fatalf("put = %d", code)
	}
	es := waitEntries(t, url, "program.edit", 1)
	if len(es) != 1 || es[0]["result"] != "ok" || es[0]["target"] != "DemoLine" {
		t.Fatalf("program.edit: %v", es)
	}
	for _, p := range []struct{ method, path, action string }{
		{"POST", "/api/program/rollback", "program.rollback"},
		{"POST", "/api/program/activate", "program.activate"},
	} {
		req, _ := http.NewRequest(p.method, url+p.path, bytes.NewReader([]byte("{}")))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if es := waitEntries(t, url, p.action, 1); len(es) != 1 || es[0]["result"] != "refused" {
			t.Fatalf("%s: %v", p.action, es)
		}
	}
}

// A failure whose error line is far longer than any buffer the seam keeps
// (a controller's raw error text) is still journalled as a failure, and
// the journal holds no source and no client-chosen mode text.
func TestLongErrorLineIsStillAFailure(t *testing.T) {
	long := strings.Repeat("x", 40<<10)
	url := startPlaneWith(t, &ProgramPlane{
		Source: ladderA, Controller: "DemoLine",
		Deploy: func(_ context.Context, _ string, _ func(string)) (*deploy.Report, error) {
			return nil, errors.New(long)
		},
	})
	postDeploy(t, url, map[string]any{"mode": "online", "source": ladderB})
	postDeploy(t, url, map[string]any{"mode": "SECRET-MODE-TEXT", "source": ladderB})
	es := waitEntries(t, url, "logix.deploy", 2)
	if len(es) != 2 || es[1]["result"] != "error" {
		t.Fatalf("a long error line: %d entries, %v; want the first to be an error", len(es), es)
	}
	raw, _ := json.Marshal(es)
	if bytes.Contains(raw, []byte("SECRET-MODE-TEXT")) || bytes.Contains(raw, []byte("RUNG r")) {
		t.Fatalf("the journal holds the client's mode text or the source: %s", raw)
	}
}

// A deploy cancelled with its request (the client hung up and the deploy
// honours the context) ends in an error event, and is journalled as a
// failure, once: the entry matches what the controller was told.
func TestDeployCancelledWithTheRequestIsJournalledAsFailed(t *testing.T) {
	started := make(chan struct{})
	url := startPlaneWith(t, &ProgramPlane{
		Source: ladderA, Controller: "DemoLine",
		Deploy: func(ctx context.Context, _ string, progress func(string)) (*deploy.Report, error) {
			progress("working")
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	raw, _ := json.Marshal(map[string]any{"mode": "online", "source": ladderB})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/api/logix/deploy", bytes.NewReader(raw))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	res.Body.Close()
	es := waitEntries(t, url, "logix.deploy", 1)
	if len(es) != 1 || es[0]["result"] != "error" {
		t.Fatalf("after a cancelled deploy: %v; want one failed entry", es)
	}
}

// #1404: a deploy that failed after its write to the controller began is
// in doubt. The stream's error line says so ("outcome": "unknown"), and
// the entry is journalled "unknown", not "error": the controller may hold
// the new program. The same for the program edit route (PUT /api/program).
func TestDeployInDoubtIsJournalledUnknown(t *testing.T) {
	inDoubt := &deploy.OutcomeUnknownError{Step: "verify", Err: &deploy.VerifyError{Diffs: []string{"rung 1 differs"}}}
	url := startPlaneWith(t, &ProgramPlane{
		Source: ladderA, Controller: "DemoLine",
		Deploy: func(_ context.Context, _ string, _ func(string)) (*deploy.Report, error) {
			return &deploy.Report{}, inDoubt
		},
	})
	_, lines := postDeploy(t, url, map[string]any{"mode": "online", "source": ladderB})
	errs := events(lines, "error")
	if len(errs) != 1 || errs[0]["outcome"] != "unknown" || errs[0]["status"] != float64(http.StatusBadGateway) {
		t.Fatalf("error line = %v; want outcome unknown, 502", errs)
	}
	if msg, _ := errs[0]["error"].(string); !strings.Contains(msg, "OUTCOME UNKNOWN") || !strings.Contains(msg, "rung 1 differs") {
		t.Errorf("error = %q; want the doubt and what the verify found", msg)
	}
	if es := waitEntries(t, url, "logix.deploy", 1); len(es) != 1 || es[0]["result"] != "unknown" {
		t.Fatalf("logix.deploy entry: %v; want result unknown", es)
	}
	code, _ := putProgram(t, url, ladderB, "")
	if code != http.StatusBadGateway {
		t.Errorf("PUT /api/program = %d, want 502", code)
	}
	if es := waitEntries(t, url, "program.edit", 1); len(es) != 1 || es[0]["result"] != "unknown" {
		t.Fatalf("program.edit entry: %v; want result unknown", es)
	}
}

// A verified deploy whose mode could not be read after it is ok, and its
// done line and journal entry carry the warning (#1404).
func TestDeployModeWarningIsJournalled(t *testing.T) {
	warn := "MODE UNKNOWN after a download that switched the controller to Program mode: it is probably STOPPED; check it and put it back in Run"
	url := startPlaneWith(t, &ProgramPlane{
		Source: ladderA, Controller: "DemoLine",
		Deploy: func(_ context.Context, _ string, _ func(string)) (*deploy.Report, error) {
			return &deploy.Report{Applied: deploy.Online, Verified: true, ModeWarning: warn}, nil
		},
	})
	_, lines := postDeploy(t, url, map[string]any{"mode": "online", "source": ladderB})
	if done := events(lines, "done"); len(done) != 1 || done[0]["modeWarning"] != warn {
		t.Fatalf("done line = %v; want the mode warning", done)
	}
	es := waitEntries(t, url, "logix.deploy", 1)
	if d, _ := es[0]["detail"].(map[string]any); len(es) != 1 || es[0]["result"] != "ok" || d["warning"] != warn || d["mode"] != "online" {
		t.Fatalf("logix.deploy entry: %v; want ok with the warning in its detail", es)
	}
	res, _ := http.NewRequest(http.MethodPut, url+"/api/program", strings.NewReader(`{"source": "`+strings.ReplaceAll(ladderA, "\n", `\n`)+`"}`))
	resp, err := http.DefaultClient.Do(res)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Nautilus-Journal-Warning") != "" {
		t.Error("the journal's warning header reached the client")
	}
	es = waitEntries(t, url, "program.edit", 1)
	if d, _ := es[0]["detail"].(map[string]any); len(es) != 1 || es[0]["result"] != "ok" || d["warning"] != warn {
		t.Fatalf("program.edit entry: %v; want ok with the warning", es)
	}
}
