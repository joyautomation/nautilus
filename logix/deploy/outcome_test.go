package deploy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/logix/logixd"
	"github.com/joyautomation/nautilus/logix/writer"
)

// #1404: whether the controller-side work started decides whether it
// finishes, not whether the caller stayed.

// emptyController is a controller with no program yet: a download is due.
const emptyController = `<?xml version="1.0"?><RSLogix5000Content><Controller Name="Empty"><Programs/></Controller></RSLogix5000Content>`

// hangUpAt makes the fake agent's call to path the moment the caller goes
// away: the caller's context is cancelled, and the agent answers only
// after the client has dropped the call or a grace has passed. A call made
// on the caller's context is dropped before it is answered; one past the
// point of no return is not, and gets its answer.
func hangUpAt(f *fakeAgent, path string, cancel context.CancelFunc) {
	done := false
	f.hook = func(r *http.Request) {
		if done || !strings.HasSuffix(r.URL.Path, path) {
			return
		}
		done = true
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// holdPast makes the fake agent's first call to path outlast the
// caller's deadline: it is answered only when the test ends.
func holdPast(t *testing.T, f *fakeAgent, path string) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // before the agent's own Close
	var held atomic.Bool
	f.hook = func(r *http.Request) {
		if strings.HasSuffix(r.URL.Path, path) && held.CompareAndSwap(false, true) {
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
		}
	}
}

func called(f *fakeAgent, suffix string) int {
	f.callsMu.Lock()
	defer f.callsMu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasSuffix(c, suffix) {
			n++
		}
	}
	return n
}

// A caller that goes away while the download is under way does not stop
// it: the download, the mode read and the verify all run to their end.
func TestDownloadFinishesWhenTheCallerGoesAway(t *testing.T) {
	f, c := newFakeAgent(t, []byte(emptyController))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hangUpAt(f, "/download", cancel)
	rep, err := Run(ctx, demoSource(t), Options{Client: c, Target: target(), Mode: Download, ProgramMode: true})
	if err != nil {
		t.Fatalf("a download the caller left mid-call: %v", err)
	}
	if rep.Applied != Download || !rep.Verified || rep.ModeAfter != "Program" {
		t.Errorf("report = %+v; want the download applied, verified, and the mode read", rep)
	}
}

// A cancel before the write stops the deploy, and nothing reaches the
// controller. The cancel lands on the mode read just before the download,
// whose failure the deploy shrugs off: only the check before the write
// stops it.
func TestDownloadStopsWhenCancelledBeforeTheWrite(t *testing.T) {
	f, c := newFakeAgent(t, []byte(emptyController))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hangUpAt(f, "/mode", cancel)
	// Program mode and reset values: no upload of values and no mode check
	// before the build, so the first mode read is the one before the write.
	_, err := Run(ctx, demoSource(t), Options{Client: c, Target: target(), Mode: Download, ProgramMode: true, ResetValues: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want the caller's cancel", err)
	}
	var ou *OutcomeUnknownError
	if errors.As(err, &ou) {
		t.Errorf("a deploy stopped before its write is in doubt: %v", err)
	}
	if n := called(f, "/download"); n != 0 {
		t.Errorf("%d download(s) after the cancel", n)
	}
}

// The deadline still stops a deploy past its write, and the failure says
// the outcome is unknown, not that nothing changed.
func TestDownloadDeadlineAfterTheWriteIsInDoubt(t *testing.T) {
	f, c := newFakeAgent(t, []byte(emptyController))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	holdPast(t, f, "/download")
	started := time.Now()
	_, err := Run(ctx, demoSource(t), Options{Client: c, Target: target(), Mode: Download, ProgramMode: true})
	if waited := time.Since(started); waited > 8*time.Second {
		t.Fatalf("the deadline did not stop the download (%s)", waited)
	}
	var ou *OutcomeUnknownError
	if !errors.As(err, &ou) || ou.Step != "download" {
		t.Fatalf("err = %v; want OUTCOME UNKNOWN at the download", err)
	}
	if !strings.Contains(err.Error(), "may hold the new program") || !strings.Contains(err.Error(), "naut logix deploy") {
		t.Errorf("the message does not say what to do: %v", err)
	}
}

// A verify that finds the controller different after a download is in
// doubt too, and keeps what it found.
func TestDownloadVerifyMismatchIsInDoubt(t *testing.T) {
	f, c := newFakeAgent(t, []byte(emptyController))
	f.downloadKeeps = true
	_, err := Run(context.Background(), demoSource(t), Options{Client: c, Target: target(), Mode: Download, ProgramMode: true})
	var ou *OutcomeUnknownError
	var ve *VerifyError
	if !errors.As(err, &ou) || ou.Step != "verify" || !errors.As(err, &ve) || len(ve.Diffs) == 0 {
		t.Fatalf("err = %v; want OUTCOME UNKNOWN at the verify, with its differences", err)
	}
}

// The SDK refusing a plain download to a controller in Run is not in
// doubt: it refused before it began.
func TestDownloadStartStateRefusalIsNotInDoubt(t *testing.T) {
	f, c := newFakeAgent(t, []byte(emptyController))
	f.startState = true
	f.modeFails = true // the mode check before the build cannot refuse it
	_, err := Run(context.Background(), demoSource(t), Options{Client: c, Target: target(), Mode: Download})
	var se *StartStateError
	var ou *OutcomeUnknownError
	if !errors.As(err, &se) || errors.As(err, &ou) {
		t.Fatalf("err = %v; want a StartStateError, not in doubt", err)
	}
}

// The cold path's online edit (`naut logix deploy --online`) finishes too.
func TestOnlineEditFinishesWhenTheCallerGoesAway(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hangUpAt(f, "/import-rungs", cancel)
	edited := strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm ( RunCmd )", 1)
	rep, err := Run(ctx, edited, Options{Client: c, Target: target(), Mode: Online})
	if err != nil || rep.Applied != Online || !rep.Verified || rep.ModeAfter != "Run" {
		t.Fatalf("an online edit the caller left mid-import: err=%v report=%+v", err, rep)
	}
}

// The warm path: an online edit whose caller goes away mid-import still
// finishes and verifies.
func TestSessionEditFinishesWhenTheCallerGoesAway(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	s, err := Connect(context.Background(), Options{Client: c, Target: target()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hangUpAt(f, "/import-rungs", cancel)
	edited := strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm ( RunCmd )", 1)
	rep, err := s.Edit(ctx, edited)
	if err != nil || rep.Applied != Online || !rep.Verified {
		t.Fatalf("an edit the caller left mid-import: err=%v report=%+v", err, rep)
	}
}

// A cancel before the import stops the edit, and nothing is imported. The
// cancel lands on the mode read just before the import, whose failure the
// edit shrugs off: only the check before the write stops it.
func TestSessionEditStopsWhenCancelledBeforeTheImport(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	s, err := Connect(context.Background(), Options{Client: c, Target: target()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hangUpAt(f, "/mode", cancel)
	edited := strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm ( RunCmd )", 1)
	_, err = s.Edit(ctx, edited)
	var ou *OutcomeUnknownError
	if !errors.Is(err, context.Canceled) || errors.As(err, &ou) {
		t.Fatalf("err = %v; want the edit stopped, not in doubt", err)
	}
	if len(f.imports) != 0 {
		t.Errorf("%d import(s) after the cancel", len(f.imports))
	}
}

// An edit whose verify fails after the import leaves the routine in
// doubt: the next edit must not replace the rung count the session held
// before (the controller may run the new rungs). It reopens the session
// and reads the controller first.
func TestSessionEditInDoubtReadsTheControllerBeforeTheNextEdit(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	s, err := Connect(context.Background(), Options{Client: c, Target: target()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	// A third rung: the controller goes from two rungs to three.
	three := strings.Replace(src, "END_LD", "  RUNG third\n    StartPB ( HiLevelAlm )\nEND_LD", 1)
	f.exportFails = 1
	_, err = s.Edit(context.Background(), three)
	var ou *OutcomeUnknownError
	if !errors.As(err, &ou) || ou.Step != "verify" {
		t.Fatalf("err = %v; want OUTCOME UNKNOWN at the verify", err)
	}
	if len(f.imports) != 1 || f.imports[0]["replaceCount"] != float64(2) {
		t.Fatalf("first import = %v", f.imports)
	}
	// The controller now runs three rungs. The next edit goes back to two.
	uploads := called(f, "/upload-to-new")
	rep, err := s.Edit(context.Background(), src)
	if err != nil || !rep.Verified {
		t.Fatalf("the edit after the doubt: err=%v report=%+v", err, rep)
	}
	if called(f, "/upload-to-new") != uploads+1 {
		t.Errorf("the edit after the doubt did not read the controller first: %v", f.calls)
	}
	if len(f.imports) != 2 || f.imports[1]["replaceCount"] != float64(3) {
		t.Errorf("second import = %v; want it to replace the 3 rungs the controller runs", f.imports[len(f.imports)-1])
	}
}

// An online import that gets no answer before the deadline is in doubt
// (the controller may run the new rungs), on the cold path too.
func TestOnlineEditImportPastTheDeadlineIsInDoubt(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	holdPast(t, f, "/import-rungs")
	edited := strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm ( RunCmd )", 1)
	_, err := Run(ctx, edited, Options{Client: c, Target: target(), Mode: Online})
	var ou *OutcomeUnknownError
	if !errors.As(err, &ou) || ou.Step != "online rung import" {
		t.Fatalf("err = %v; want OUTCOME UNKNOWN at the import", err)
	}
}

// The warm path: an import with no answer before the deadline is in
// doubt, and the next edit reads the controller first.
func TestSessionImportPastTheDeadlineIsInDoubt(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	s, err := Connect(context.Background(), Options{Client: c, Target: target()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	holdPast(t, f, "/import-rungs")
	edited := strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm ( RunCmd )", 1)
	_, err = s.Edit(ctx, edited)
	var ou *OutcomeUnknownError
	if !errors.As(err, &ou) || ou.Step != "online rung import" {
		t.Fatalf("err = %v; want OUTCOME UNKNOWN at the import", err)
	}
	uploads := called(f, "/upload-to-new")
	if _, err := s.Edit(context.Background(), edited); err != nil {
		t.Fatalf("the edit after the doubt: %v", err)
	}
	if called(f, "/upload-to-new") != uploads+1 {
		t.Errorf("the edit after the doubt did not read the controller first: %v", f.calls)
	}
}

// An import the SDK answered with a refusal changed nothing: a plain
// error, not in doubt, and the session (and its rungs) is kept, so the
// next edit makes no new upload.
func TestSessionImportRefusedIsNotInDoubt(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	s, err := Connect(context.Background(), Options{Client: c, Target: target()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	f.importFails = "refuse"
	edited := strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm ( RunCmd )", 1)
	_, err = s.Edit(context.Background(), edited)
	var ou *OutcomeUnknownError
	if err == nil || errors.As(err, &ou) || !strings.Contains(err.Error(), "rung 1 does not verify") {
		t.Fatalf("err = %v; want the SDK's refusal, not in doubt", err)
	}
	f.importFails = ""
	uploads := called(f, "/upload-to-new")
	rep, err := s.Edit(context.Background(), edited)
	if err != nil || !rep.Verified || rep.Replaced != 2 {
		t.Fatalf("the edit after a refusal: err=%v report=%+v", err, rep)
	}
	if called(f, "/upload-to-new") != uploads {
		t.Errorf("a refused import dropped the session: %v", f.calls)
	}
}

// An import the agent answered as fatal (the SDK or its channel broke,
// an SDK timeout) did not settle what it did: in doubt, and the next edit
// reads the controller first.
func TestSessionImportFatalAnswerIsInDoubt(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	s, err := Connect(context.Background(), Options{Client: c, Target: target()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	three := strings.Replace(src, "END_LD", "  RUNG third\n    StartPB ( HiLevelAlm )\nEND_LD", 1)
	f.importFails = "fatal"
	_, err = s.Edit(context.Background(), three)
	var ou *OutcomeUnknownError
	if !errors.As(err, &ou) || ou.Step != "online rung import" {
		t.Fatalf("err = %v; want OUTCOME UNKNOWN at the import", err)
	}
	f.importFails = ""
	uploads := called(f, "/upload-to-new")
	if _, err := s.Edit(context.Background(), src); err != nil {
		t.Fatalf("the edit after the doubt: %v", err)
	}
	if called(f, "/upload-to-new") != uploads+1 || len(f.imports) != 1 || f.imports[0]["replaceCount"] != float64(3) {
		t.Errorf("the edit after a fatal import answer: uploads %d -> %d, imports %v; want a re-read and 3 rungs replaced",
			uploads, called(f, "/upload-to-new"), f.imports)
	}
}

// The cold path classifies a fatal import answer the same way.
func TestOnlineEditFatalImportAnswerIsInDoubt(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	f.importFails = "fatal"
	edited := strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm ( RunCmd )", 1)
	_, err := Run(context.Background(), edited, Options{Client: c, Target: target(), Mode: Online})
	var ou *OutcomeUnknownError
	if !errors.As(err, &ou) {
		t.Fatalf("err = %v; want OUTCOME UNKNOWN", err)
	}
}

// After an import the SDK refused, the session reads the routine back
// rather than trust its rungs: had the controller taken them all the
// same, the next edit replaces the count it really runs.
func TestSessionImportRefusedReadsTheRoutineBack(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	s, err := Connect(context.Background(), Options{Client: c, Target: target()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	three := strings.Replace(src, "END_LD", "  RUNG third\n    StartPB ( HiLevelAlm )\nEND_LD", 1)
	f.importFails = "refuse-took"
	_, err = s.Edit(context.Background(), three)
	var ou *OutcomeUnknownError
	if err == nil || errors.As(err, &ou) {
		t.Fatalf("err = %v; want the SDK's refusal", err)
	}
	f.importFails = ""
	uploads := called(f, "/upload-to-new")
	if _, err := s.Edit(context.Background(), src); err != nil {
		t.Fatalf("the edit after the refusal: %v", err)
	}
	if called(f, "/upload-to-new") != uploads || len(f.imports) != 1 || f.imports[0]["replaceCount"] != float64(3) {
		t.Errorf("after a refusal: uploads %d -> %d, imports %v; want no reopen and the 3 rungs the controller runs replaced",
			uploads, called(f, "/upload-to-new"), f.imports)
	}
}

// A mode read that fails after the write does not fail a verified deploy,
// but the report says the mode is unknown: the verify proves the program,
// not the mode. After a download that switched to Program mode it says the
// controller is probably stopped.
func TestModeUnreadAfterTheWriteWarns(t *testing.T) {
	for _, programMode := range []bool{true, false} {
		f, c := newFakeAgent(t, []byte(emptyController))
		f.modeFails = true
		rep, err := Run(context.Background(), demoSource(t), Options{Client: c, Target: target(), Mode: Download, ProgramMode: programMode})
		if err != nil || !rep.Verified {
			t.Fatalf("program mode %v: err=%v report=%+v; a verified download stays ok", programMode, err, rep)
		}
		want := "mode unknown: check whether the controller is in Run"
		if programMode {
			want = "probably STOPPED"
		}
		if !strings.Contains(rep.ModeWarning, want) {
			t.Errorf("program mode %v: ModeWarning = %q, want %q", programMode, rep.ModeWarning, want)
		}
	}
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	s, err := Connect(context.Background(), Options{Client: c, Target: target()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	f.modeFails = true
	rep, err := s.Edit(context.Background(), strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm ( RunCmd )", 1))
	if err != nil || !rep.Verified || rep.ModeWarning == "" {
		t.Errorf("online edit: err=%v report=%+v; want ok with a mode warning", err, rep)
	}
}

// #1410: a session the agent reaped (not_found on a session route) is
// reopened once, at the mode read before the import or at the import
// itself (the agent's word that nothing was imported).
func TestSessionEditReopensAReapedSession(t *testing.T) {
	for _, route := range []string{"/mode", "/import-rungs"} {
		t.Run(route, func(t *testing.T) {
			src := demoSource(t)
			same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
			f, c := newFakeAgent(t, same)
			s, err := Connect(context.Background(), Options{Client: c, Target: target()})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close(context.Background())
			f.gone = map[string]int{route: 1}
			uploads := called(f, "/upload-to-new")
			edited := strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm ( RunCmd )", 1)
			rep, err := s.Edit(context.Background(), edited)
			if err != nil || !rep.Verified {
				t.Fatalf("edit after the reap: err=%v report=%+v", err, rep)
			}
			if got := called(f, "/upload-to-new"); got != uploads+1 {
				t.Errorf("uploads = %d, want one reopen (%d)", got, uploads+1)
			}
			if len(f.imports) != 1 {
				t.Errorf("%d import(s), want 1", len(f.imports))
			}
		})
	}
}

// A second not_found after the reopen is not retried again.
func TestSessionEditReapedTwiceDoesNotLoop(t *testing.T) {
	src := demoSource(t)
	same, _, _ := writer.Write(src, writer.Options{Controller: "DemoLine"})
	f, c := newFakeAgent(t, same)
	s, err := Connect(context.Background(), Options{Client: c, Target: target()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	f.gone = map[string]int{"/mode": 1000, "/import-rungs": 1000}
	uploads := called(f, "/upload-to-new")
	edited := strings.Replace(src, "/StopPB ( RunCmd )", "/StopPB /HiLevelAlm ( RunCmd )", 1)
	_, err = s.Edit(context.Background(), edited)
	if logixd.Kind(err) != "not_found" {
		t.Fatalf("err = %v; want the not_found surfaced", err)
	}
	if got := called(f, "/upload-to-new"); got != uploads+1 {
		t.Errorf("uploads = %d, want exactly one reopen", got-uploads)
	}
}
