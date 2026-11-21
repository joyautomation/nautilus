package tiadtest_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/tiad"
	"github.com/joyautomation/nautilus/siemens/tiad/tiadtest"
)

// These tests read tiad-fake's ITia call log: what reached TIA, on which
// thread, and when.

func callNames(cs []tiadtest.TiaCall) string {
	var n []string
	for _, c := range cs {
		n = append(n, c.Call)
	}
	return strings.Join(n, ",")
}

func installBench(t *testing.T, c *tiad.Client) {
	t.Helper()
	const g = "GSDML-V2.4-Example-Device-20261117.xml"
	if _, err := c.PutFile(ctx, "gsd/"+g, []byte("<ISO15745Profile/>")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InstallGSD(ctx, "gsd/"+g); err != nil {
		t.Fatal(err)
	}
}

// TestAgentStartsTIAOnlyWhenNeeded: health, probe and the files cost no
// trial time, because they never make the ITia (siemens-ide.md §3.4).
func TestAgentStartsTIAOnlyWhenNeeded(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	c := p.Client()
	if _, err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutFile(ctx, "a.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetFile(ctx, "a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListFiles(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteFile(ctx, "a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sessions(ctx); err != nil {
		t.Fatal(err)
	}
	// Refused before TIA: a failing gate, and a bad request.
	p.SetGates(t, []tiad.Gate{{Name: "licence", State: tiad.GateFailed}})
	if _, err := c.Open(ctx, "projects/p/p.ap21"); tiad.Kind(err) != tiad.KindGate {
		t.Fatalf("open with a failed gate: %v", err)
	}
	p.SetGates(t, nil)
	if _, err := c.Open(ctx, "projects/none/none.ap21"); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("open a missing project: %v", err)
	}
	if cs := p.Calls(t); len(cs) != 0 {
		t.Fatalf("TIA was reached: %s", callNames(cs))
	}
	installBench(t, c)
	if cs := p.Calls(t); callNames(cs) != "start,InstallGsd" {
		t.Fatalf("calls after the first TIA call = %s, want start,InstallGsd", callNames(cs))
	}
}

// TestAgentRunsTIAOnOneSTAThread: every ITia call, from callers on many
// threads, runs on the one STA thread.
func TestAgentRunsTIAOnOneSTAThread(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	c := p.Client()
	installBench(t, c)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := c.CreateProject(ctx, "p"+string(rune('a'+i)), tiadtest.BenchStation(), tiadtest.BenchSecrets)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := s.Compile(ctx, true, true); err != nil {
				t.Error(err)
			}
			if err := s.Close(ctx); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	cs := p.Calls(t)
	if len(cs) < 6*4 {
		t.Fatalf("only %d calls: %s", len(cs), callNames(cs))
	}
	for _, call := range cs {
		if call.Thread != cs[0].Thread || !call.STA {
			t.Fatalf("%s ran on thread %d (STA %v), the first call on %d", call.Call, call.Thread, call.STA, cs[0].Thread)
		}
	}
	if n := strings.Count(callNames(cs), "start"); n != 1 {
		t.Fatalf("TIA started %d times", n)
	}
}

// TestAgentSurvivesATIAFailure: TIA's own failure answers operation_failed
// with its text, and the TIA thread carries on.
func TestAgentSurvivesATIAFailure(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	c := p.Client()
	installBench(t, c)
	s, err := c.CreateProject(ctx, "p", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "Compile", Message: "The license is not valid"}}})
	_, err = s.Compile(ctx, true, true)
	if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "TIA: The license is not valid") {
		t.Fatalf("a failing TIA call: %v", err)
	}
	p.SetScript(t, tiadtest.Script{})
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("the next call after a failure: %v", err)
	}
	// A project that fails to create or to open leaves no session behind.
	p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "CreateProject", Message: "No licence for this CPU"}}})
	if _, err := c.CreateProject(ctx, "q", tiadtest.BenchStation(), tiadtest.BenchSecrets); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("a failing create: %v", err)
	}
	p.SetScript(t, tiadtest.Script{})
	if q, err := c.CreateProject(ctx, "q", tiadtest.BenchStation(), tiadtest.BenchSecrets); err != nil {
		t.Fatalf("create after a failed create (a reservation left behind?): %v", err)
	} else {
		_ = q.Close(ctx)
	}
	p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "OpenProject", Message: "The project is from a newer version"}}})
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Open(ctx, s.Project); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("a failing open: %v", err)
	}
	p.SetScript(t, tiadtest.Script{})
	if _, err := c.Open(ctx, s.Project); err != nil {
		t.Fatalf("open after a failed open (a reservation left behind?): %v", err)
	}
}

// TestAgentAnswersWhileTIAIsBusy: a long TIA call blocks only TIA calls.
func TestAgentAnswersWhileTIAIsBusy(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	c := p.Client()
	installBench(t, c)
	s, err := c.CreateProject(ctx, "p", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	p.SetScript(t, tiadtest.Script{Delays: []tiadtest.ScriptedCall{{Call: "Compile", MS: 3000}}})
	done := make(chan error, 1)
	go func() {
		_, err := s.Compile(ctx, true, true)
		done <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); !strings.HasSuffix(callNames(p.Calls(t)), "Compile"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the compile never reached TIA")
		}
	}
	begin := time.Now()
	if _, err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutFile(ctx, "b.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sessions(ctx); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(begin); d > 1500*time.Millisecond {
		t.Fatalf("health, files and sessions waited %v for TIA", d)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestAgentClosesIdleSessions: a forgotten session is closed, but never
// one with a call in flight, and the idle clock starts when a call ends.
func TestAgentClosesIdleSessions(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{}, "TIAD_SESSION_IDLE=1s")
	c := p.Client()
	installBench(t, c)
	s, err := c.CreateProject(ctx, "p", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	p.SetScript(t, tiadtest.Script{Delays: []tiadtest.ScriptedCall{{Call: "Compile", MS: 1500}}})
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("a call longer than the idle limit: %v", err)
	}
	// Half the idle limit after the call: idle since the call ended is
	// short of the limit; idle since it began is well past it.
	time.Sleep(500 * time.Millisecond)
	if ss, _ := c.Sessions(ctx); len(ss) != 1 {
		t.Fatalf("the session was reaped during or soon after a call: %+v", ss)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		ss, err := c.Sessions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(ss) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("an idle session is still open: %+v", ss)
		}
	}
	if !strings.HasSuffix(callNames(p.Calls(t)), "CloseProject") {
		t.Fatalf("the reaper did not close the project in TIA: %s", callNames(p.Calls(t)))
	}
	if _, err := s.Compile(ctx, true, true); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("a reaped session: %v", err)
	}
	if _, err := c.Open(ctx, s.Project); err != nil {
		t.Fatalf("reopen after the reaper: %v", err)
	}
}

// TestAgentKeepsSecretsOut: passwords reach neither the log nor the work
// dir.
func TestAgentKeepsSecretsOut(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	c := p.Client()
	installBench(t, c)
	sec := tiad.Secrets{WebUsers: map[string]string{"naut": "web-password-must-not-leak"}, Protection: "protection-must-not-leak",
		FullAccessPassword: "Full-access-must-not-leak-1"}
	s, err := c.CreateProject(ctx, "p", tiadtest.BenchStation(), sec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyStation(ctx, tiadtest.BenchStation(), sec); err != nil {
		t.Fatal(err)
	}
	bad := tiadtest.BenchStation()
	bad.CPU.Address = "x"
	if _, err := s.ApplyStation(ctx, bad, sec); err == nil {
		t.Fatal("a bad station was taken")
	} else if strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("an error carries a secret: %v", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	waitLog(t, p, "DELETE /v1/sessions/"+s.ID+" 200")
	if strings.Contains(p.Log(), "must-not-leak") {
		t.Fatalf("the log carries a secret:\n%s", p.Log())
	}
	_ = filepath.WalkDir(p.WorkDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(path); strings.Contains(string(b), "must-not-leak") {
				t.Errorf("%s carries a secret", path)
			}
		}
		return nil
	})
}

// TestAgentTakesOneCallPerSession: while a call is in flight on a
// session, another call on it is refused (busy), so the session's state
// only ever changes in call order. Other sessions are not held up beyond
// waiting their turn on the TIA thread, and a close is always taken.
func TestAgentTakesOneCallPerSession(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	c := p.Client()
	installBench(t, c)
	s, err := c.CreateProject(ctx, "p", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	q, err := c.CreateProject(ctx, "q", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	p.SetScript(t, tiadtest.Script{Delays: []tiadtest.ScriptedCall{{Call: "Compile", MS: 2000}}})
	done := make(chan error, 1)
	go func() {
		_, err := s.Compile(ctx, true, true)
		done <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); !strings.HasSuffix(callNames(p.Calls(t)), "Compile"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the compile never reached TIA")
		}
	}
	busy := map[string]func() error{
		"software": func() error { _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "g"}); return err },
		"station": func() error {
			_, err := s.ApplyStation(ctx, tiadtest.BenchStation(), tiadtest.BenchSecrets)
			return err
		},
		"compile": func() error { _, err := s.Compile(ctx, true, true); return err },
	}
	for name, call := range busy {
		if err := call(); tiad.Kind(err) != tiad.KindBusy {
			t.Errorf("%s on a session with a call in flight: %v, want busy", name, err)
		}
	}
	if _, err := q.ApplyStation(ctx, tiadtest.BenchStation(), tiadtest.BenchSecrets); err != nil {
		t.Errorf("a call on another session: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "g"}); err != nil {
		t.Fatalf("the session after its call: %v", err)
	}
	// A close is taken even with a call in flight; it runs after it.
	go func() {
		_, err := q.Compile(ctx, true, true)
		done <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); countCalls(p.Calls(t), "Compile") < 2; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the second compile never reached TIA")
		}
	}
	if err := q.Close(ctx); err != nil {
		t.Fatalf("close with a call in flight: %v", err)
	}
	<-done
}

// TestAgentKeepsASessionItCouldNotClose: when TIA fails to close a
// project, the session stays, broken, and can be closed again.
func TestAgentKeepsASessionItCouldNotClose(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	c := p.Client()
	installBench(t, c)
	s, err := c.CreateProject(ctx, "p", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "CloseProject", Message: "The project is locked"}}})
	if err := s.Close(ctx); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("a failing close: %v", err)
	}
	if ss, _ := c.Sessions(ctx); len(ss) != 1 {
		t.Fatalf("the session went away although TIA still holds the project: %+v", ss)
	}
	if _, err := s.Compile(ctx, true, true); tiad.Kind(err) != tiad.KindOperationFailed || !tiad.IsFatal(err) || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("a call on a broken session: %v", err)
	}
	if _, err := c.Open(ctx, s.Project); tiad.Kind(err) != tiad.KindBusy {
		t.Fatalf("open the project of a broken session: %v", err)
	}
	p.SetScript(t, tiadtest.Script{})
	if err := s.Close(ctx); err != nil {
		t.Fatalf("closing again: %v", err)
	}
	if n := countCalls(p.Calls(t), "CloseProject"); n != 2 {
		t.Fatalf("TIA was asked to close the project %d times, want 2 (the retry must reach TIA)", n)
	}
	if ss, _ := c.Sessions(ctx); len(ss) != 0 {
		t.Fatalf("sessions after the second close: %+v", ss)
	}
}

// TestAgentNeverHandsTIAABadGSDML: a GSDML with a DOCTYPE never reaches
// TIA's importer.
func TestAgentNeverHandsTIAABadGSDML(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{})
	c := p.Client()
	const g = "gsd/GSDML-V2.4-Bad-20261118.xml"
	if _, err := c.PutFile(ctx, g, []byte(`<!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]><x>&e;</x>`)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InstallGSD(ctx, g); tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("install: %v", err)
	}
	if n := countCalls(p.Calls(t), "InstallGsd"); n != 0 {
		t.Fatalf("TIA's importer got the file %d times", n)
	}
}

// downloadReady is tiad-fake with the bench project compiled.
func downloadReady(t *testing.T, env ...string) (*tiadtest.Process, *tiad.Session) {
	t.Helper()
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{Targets: []string{"192.0.2.10", "192.0.2.99"}}, env...)
	c := p.Client()
	installBench(t, c)
	s, err := c.CreateProject(ctx, "bench", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	return p, s
}

func countCalls(cs []tiadtest.TiaCall, name string) int {
	n := 0
	for _, c := range cs {
		if c.Call == name {
			n++
		}
	}
	return n
}

// TestAgentRefusesDownloadsBeforeTIA: every refusal the agent can make
// itself is made before TIA hears of the download.
func TestAgentRefusesDownloadsBeforeTIA(t *testing.T) {
	p, s := downloadReady(t)
	p.SetQuestions(t, []tiad.Question{{ID: tiad.QuestionStopCPU}})
	refused := []tiad.DownloadRequest{
		{Address: "192.0.2.77", Software: true, Announced: true}, // not a target
		{Address: "192.0.2.99", Software: true, Announced: true}, // not the station's CPU
	}
	for _, r := range refused {
		if _, err := s.Download(ctx, r); err == nil {
			t.Fatalf("%+v was not refused", r)
		}
	}
	if _, err := s.ApplyStation(ctx, tiadtest.BenchStation(tiad.DeviceModule{Slot: 1, Subslot: 1, Module: "M"}), tiadtest.BenchSecrets); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true}); tiad.Kind(err) != tiad.KindNotCompiled {
		t.Fatalf("not compiled: %v", err)
	}
	if n := countCalls(p.Calls(t), "Download"); n != 0 {
		t.Fatalf("TIA heard of %d refused downloads", n)
	}
	// A dry run and an unanswered download reach TIA and load nothing.
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, DryRun: true, Announced: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true}); tiad.Kind(err) != tiad.KindDecisionRequired {
		t.Fatalf("unanswered: %v", err)
	}
	if n := countCalls(p.Calls(t), "Download"); n != 2 {
		t.Fatalf("TIA heard of %d downloads, want 2", n)
	}
	if l := p.Loaded(t); len(l) != 0 {
		t.Fatalf("loaded %+v", l)
	}
}

// TestAgentSaysWhenTheOutcomeIsUnknown: whenever TIA's side may have
// loaded the CPU without the agent's approval, or fails once the download
// was decided, the answer is outcome_unknown: never success, never a
// plain refusal, and the session stays open so the next step, a compare on
// it, works.
func TestAgentSaysWhenTheOutcomeIsUnknown(t *testing.T) {
	p, s := downloadReady(t)
	dry := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, DryRun: true, Announced: true}
	real := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true}
	no := real
	no.Decisions = tiad.Decisions{StopCPU: tiad.No()}
	yes := real
	yes.Decisions = tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.No()}
	stop := []tiad.Question{{ID: tiad.QuestionStopCPU}}
	cases := []struct {
		name   string
		script tiadtest.Script
		req    tiad.DownloadRequest
	}{
		{"loads a dry run", tiadtest.Script{Questions: stop, LoadDespiteCancel: true}, dry},
		{"loads an unanswered download", tiadtest.Script{Questions: stop, LoadDespiteCancel: true}, real},
		{"loads a declined download", tiadtest.Script{Questions: stop, LoadDespiteCancel: true}, no},
		{"never asks, dry run", tiadtest.Script{DecideCalls: intp(0)}, dry},
		{"never asks", tiadtest.Script{DecideCalls: intp(0)}, yes},
		{"asks twice", tiadtest.Script{Questions: stop, DecideCalls: intp(2)}, yes},
		{"fails after an approval", tiadtest.Script{Questions: stop, FailAfterDecide: "The connection to the CPU was lost"}, yes},
		{"fails after a cancel", tiadtest.Script{Questions: stop, FailAfterDecide: "The connection to the CPU was lost"}, real},
		{"fails after a dry run", tiadtest.Script{FailAfterDecide: "The connection to the CPU was lost"}, dry},
		// TIA's side says its download had begun, though nothing was
		// decided yet (it threw before asking, or a cancel was swallowed).
		{"fails in doubt before asking", tiadtest.Script{FailInDoubt: "TIA went on after tiad cancelled the download"}, yes},
	}
	for _, tc := range cases {
		p.SetScript(t, tc.script)
		_, err := s.Download(ctx, tc.req)
		if tiad.Kind(err) != tiad.KindOutcomeUnknown || tiad.IsFatal(err) || !tiad.OutcomeUnknown(err) || !strings.Contains(err.Error(), "compare before anything else") {
			t.Errorf("%s: %v, want a non-fatal outcome_unknown", tc.name, err)
			continue
		}
		p.SetScript(t, tiadtest.Script{})
		if _, err := s.Compare(ctx); err != nil {
			t.Errorf("%s: compare on the same session: %v", tc.name, err)
		}
	}
	// A TIA failure before anything was decided is a plain failure.
	p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "Download", Message: "No route to the CPU"}}})
	if _, err := s.Download(ctx, yes); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("a failure before decide: %v", err)
	}
}

// TestAgentSaysUnknownWhenTIADiesInsideTheDownload: #789 stage 2. TIA
// died inside its download call before asking anything, and detaching
// tiad's handlers from its disposed objects threw too; that second error
// replaced the first and the agent answered operation_failed. Once TIA's
// download call was entered the CPU may or may not be loaded, whatever
// the failure, so the answer is outcome_unknown, dry run or not. A TIA
// that dies before its download call loaded nothing: operation_failed,
// naming the failure itself, not the one detaching. (Whether a session
// whose TIA died can still compare is not covered here: the fake keeps
// running.)
func TestAgentSaysUnknownWhenTIADiesInsideTheDownload(t *testing.T) {
	p, s := downloadReady(t)
	dry := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, DryRun: true, Announced: true}
	real := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true,
		Decisions: tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.No()}}
	for _, req := range []tiad.DownloadRequest{dry, real} {
		p.SetScript(t, tiadtest.Script{TiaDies: "downloading"})
		_, err := s.Download(ctx, req)
		if tiad.Kind(err) != tiad.KindOutcomeUnknown || tiad.IsFatal(err) || !tiad.OutcomeUnknown(err) ||
			!strings.Contains(err.Error(), "compare before anything else") || !strings.Contains(err.Error(), "DownloadProvider") {
			t.Errorf("dry run %v, TIA dies in its download call: %v, want a non-fatal outcome_unknown naming the download's failure", req.DryRun, err)
		}
		p.SetScript(t, tiadtest.Script{TiaDies: "preparing"})
		_, err = s.Download(ctx, req)
		if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "DownloadProvider") || strings.Contains(err.Error(), "ConnectionConfiguration") {
			t.Errorf("dry run %v, TIA dies before its download call: %v, want operation_failed naming the failure, not the detach", req.DryRun, err)
		}
	}
	if l := p.Loaded(t); len(l) != 0 {
		t.Fatalf("a TIA that died loaded %+v", l)
	}
}

// TestAgentDecidesEveryDryRun: decide runs exactly once even when TIA
// asks nothing, so a dry run with no questions still cancels; start-after
// is asked all the same.
func TestAgentDecidesEveryDryRun(t *testing.T) {
	p, s := downloadReady(t)
	r, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, DryRun: true, Announced: true})
	if err != nil || !r.DryRun || !r.Provisional || r.Downloaded || len(r.Questions) != 1 || r.Questions[0].ID != tiad.QuestionStartAfter {
		t.Fatalf("a dry run TIA asks nothing about: %+v %v", r, err)
	}
	if l := p.Loaded(t); len(l) != 0 {
		t.Fatalf("a dry run with no questions loaded %+v", l)
	}
}

// TestAgentGivesOneCPUToOneProject: two sessions racing to the same CPU,
// with TIA slow: exactly one loads.
func TestAgentGivesOneCPUToOneProject(t *testing.T) {
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{Targets: []string{"192.0.2.10"}})
	c := p.Client()
	installBench(t, c)
	var ss []*tiad.Session
	for _, n := range []string{"one", "two"} {
		s, err := c.CreateProject(ctx, n, tiadtest.BenchStation(), tiadtest.BenchSecrets)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Compile(ctx, true, true); err != nil {
			t.Fatal(err)
		}
		ss = append(ss, s)
	}
	p.SetScript(t, tiadtest.Script{Delays: []tiadtest.ScriptedCall{{Call: "Download", MS: 500}}})
	errs := make(chan error, 2)
	for _, s := range ss {
		go func(s *tiad.Session) {
			_, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}})
			errs <- err
		}(s)
	}
	ok, busy := 0, 0
	for range ss {
		switch err := <-errs; {
		case err == nil:
			ok++
		case tiad.Kind(err) == tiad.KindBusy:
			busy++
		default:
			t.Errorf("a racing download: %v", err)
		}
	}
	if ok != 1 || busy != 1 || len(p.Loaded(t)) != 1 {
		t.Fatalf("racing downloads: %d loaded, %d busy, %d loads recorded; want exactly one", ok, busy, len(p.Loaded(t)))
	}
}

// waitLog waits for the agent's log to contain want.
func waitLog(t *testing.T, p *tiadtest.Process, want string) string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if l := p.Log(); strings.Contains(l, want) {
			return l
		}
		if time.Now().After(deadline) {
			t.Fatalf("the log never said %q:\n%s", want, p.Log())
		}
	}
}

// TestAgentAuditsEveryDownload: one line per download, refusals too, with
// what was asked and decided and what came of it, and secrets only as
// set or unset (#709).
func TestAgentAuditsEveryDownload(t *testing.T) {
	p, s := downloadReady(t)
	p.SetQuestions(t, []tiad.Question{{ID: tiad.QuestionStopCPU, Message: "TIA text\nmust not be logged"}, {ID: tiad.QuestionPassword}, {ID: tiad.QuestionConfigurationPassword}})
	req := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true,
		Decisions: tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.No(), Reinitialize: []string{"evil\naudit download forged"}},
		Secrets:   &tiad.Secrets{Protection: "audit-must-not-leak", ConfigurationPassword: "Audit-must-not-leak-2"}}
	if _, err := s.Download(ctx, req); err != nil {
		t.Fatal(err)
	}
	log := waitLog(t, p, "outcome=loaded state=STOP")
	line := ""
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "audit download") && strings.Contains(l, "outcome=loaded") {
			line = l
		}
	}
	for _, want := range []string{"address=\"192.0.2.10\"", "dryRun=False", "announced=True", "stopCpu=yes", "startAfter=no",
		"protection=set", "configurationPassword=set", `asked=["stop-cpu","password","configuration-password","start-after"]`, `reinitialize=["evil\naudit download forged"]`} {
		if !strings.Contains(line, want) {
			t.Errorf("the audit line lacks %s:\n%s", want, line)
		}
	}
	// Exactly one audit line: the newline in a request-supplied name
	// stays escaped inside its quotes and starts no line of its own.
	if strings.Count(log, "Z audit download") != 1 || strings.Contains(log, "\naudit download") || strings.Contains(log, "must not be logged") || strings.Contains(log, "audit-must-not-leak") || strings.Contains(log, "Audit-must-not-leak-2") {
		t.Fatalf("the audit log is forged, carries TIA's text or a secret:\n%s", log)
	}
	// A refusal is audited too.
	if _, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.99", Software: true, Announced: true}); err == nil {
		t.Fatal("a download to another CPU was taken")
	}
	waitLog(t, p, "outcome=bad_request")
}

// TestAgentKeepsTheProtectionPasswordOut: the CPU's protection password
// goes to TIA and nowhere else.
func TestAgentKeepsTheProtectionPasswordOut(t *testing.T) {
	p, s := downloadReady(t)
	p.SetQuestions(t, []tiad.Question{{ID: tiad.QuestionPassword}})
	sec := &tiad.Secrets{Protection: "protection-must-not-leak"}
	if _, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Secrets: sec, Decisions: tiad.Decisions{StartAfter: tiad.No()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, DryRun: true, Announced: true, Secrets: sec}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, true); err != nil {
		t.Fatal(err)
	}
	waitLog(t, p, "outcome=dry-run")
	waitLog(t, p, "POST /v1/sessions/"+s.ID+"/save 200")
	if strings.Contains(p.Log(), "must-not-leak") {
		t.Fatalf("the log carries the password:\n%s", p.Log())
	}
	_ = filepath.WalkDir(p.WorkDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(path); strings.Contains(string(b), "must-not-leak") {
				t.Errorf("%s carries the password", path)
			}
		}
		return nil
	})
}

// TestAgentForgetsACompileWhenAChangeFails: a change that fails part-way
// in TIA leaves the project not downloadable, so a half-applied station
// or half-imported program never reaches the CPU on the strength of an
// earlier compile.
func TestAgentForgetsACompileWhenAChangeFails(t *testing.T) {
	p, s := downloadReady(t)
	dl := tiad.DownloadRequest{Address: "192.0.2.10", Hardware: true, Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}
	fail := func(call string) {
		t.Helper()
		p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: call, Message: call + " failed part-way"}}})
	}
	ok := func() {
		t.Helper()
		p.SetScript(t, tiadtest.Script{})
	}
	notDownloadable := func(why, want string) {
		t.Helper()
		_, err := s.Download(ctx, dl)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: download = %v, want a refusal naming %q", why, err, want)
		}
	}
	if _, err := s.Download(ctx, dl); err != nil {
		t.Fatalf("the compiled project: %v", err)
	}

	// A software import that fails.
	fail("ImportSoftware")
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus"}); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("import: %v", err)
	}
	ok()
	notDownloadable("after a failed import", "compile")
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}

	// A spec part this side cannot configure yet refuses before any
	// write: the session is as it was, and can still apply and save.
	p.SetScript(t, tiadtest.Script{CannotApply: "tiad cannot place rack modules yet"})
	if _, err := s.ApplyStation(ctx, tiadtest.BenchStation(), tiadtest.BenchSecrets); tiad.Kind(err) != tiad.KindOperationFailed ||
		!strings.Contains(err.Error(), "cannot place rack modules yet") {
		t.Fatalf("apply of what this side cannot configure: %v", err)
	}
	ok()
	if _, err := s.Download(ctx, dl); err != nil {
		t.Fatalf("still compiled and downloadable after a refusal before any write: %v", err)
	}
	if _, err := s.Save(ctx, false); err != nil {
		t.Fatalf("save after a refusal before any write: %v", err)
	}

	// A station that fails to apply: not compiled, and not described by
	// any spec until one applies.
	fail("ApplyStation")
	if _, err := s.ApplyStation(ctx, tiadtest.BenchStation(tiad.DeviceModule{Slot: 1, Subslot: 1, Module: "M"}), tiadtest.BenchSecrets); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("apply: %v", err)
	}
	ok()
	notDownloadable("after a failed apply", "no station spec")
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	notDownloadable("after a failed apply, compiled", "no station spec")
	// tiad never reuses a CPU an apply left part-way, and never saves
	// it: the session only closes, unsaved (#789).
	if _, err := s.ApplyStation(ctx, tiadtest.BenchStation(), tiadtest.BenchSecrets); tiad.Kind(err) != tiad.KindUncleanSession ||
		!strings.Contains(err.Error(), "close this session without saving") {
		t.Fatalf("an apply after a failed one: %v", err)
	}
	if _, err := s.Save(ctx, false); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("a save after a failed apply: %v", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := p.Client().CreateProject(ctx, "bench", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	notDownloadable("a new project", "compile")
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, dl); err != nil {
		t.Fatalf("after a good apply and compile: %v", err)
	}

	// Applying the same spec changes nothing and keeps the compile.
	if r, err := s.ApplyStation(ctx, tiadtest.BenchStation(), tiadtest.BenchSecrets); err != nil || len(r.Changes) != 0 {
		t.Fatalf("the same spec again: %+v %v", r, err)
	}
	if _, err := s.Download(ctx, dl); err != nil {
		t.Fatalf("after applying the same spec: %v", err)
	}

	// A compile that TIA fails, and one with errors, forget the last good
	// compile.
	fail("Compile")
	if _, err := s.Compile(ctx, true, true); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("compile: %v", err)
	}
	ok()
	notDownloadable("after a failed compile", "compile")
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	p.SetCompileResult(t, tiad.CompileResult{Result: "error", Errors: 1})
	if _, err := s.Compile(ctx, true, true); tiad.Kind(err) != tiad.KindCompileFailed {
		t.Fatalf("compile with errors: %v", err)
	}
	notDownloadable("after a compile with errors", "compile")

	// An upload replaces the program: compile again first.
	p.SetScript(t, tiadtest.Script{})
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Upload(ctx); err != nil {
		t.Fatal(err)
	}
	notDownloadable("after an upload", "no station spec")
	// The uploaded CPU has no access control (the S2a upload): keep it.
	uploaded := tiadtest.BenchStation()
	uploaded.CPU.AccessControl = tiad.AccessKeep
	if _, err := s.ApplyStation(ctx, uploaded, tiadtest.BenchSecrets); err != nil {
		t.Fatal(err)
	}
	notDownloadable("after an upload and a spec", "compile")
}

func intp(i int) *int { return &i }

// TestAgentRefusesADryRunTIACannotCancel: a TIA side that cannot promise a
// dry run loads nothing (tiad.Openness until S2b) gets no dry run: it is
// refused before ITia.Download, so nothing reaches the CPU, and no CPU is
// claimed. A real download still goes ahead.
func TestAgentRefusesADryRunTIACannotCancel(t *testing.T) {
	p, s := downloadReady(t)
	p.SetScript(t, tiadtest.Script{NoDryRun: true})
	_, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, DryRun: true, Announced: true})
	if tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "dry runs are refused") || !strings.Contains(err.Error(), "nothing was sent to the CPU") {
		t.Fatalf("a dry run TIA cannot cancel: %v, want bad_request: dry runs are refused", err)
	}
	if n := countCalls(p.Calls(t), "Download"); n != 0 {
		t.Fatalf("TIA heard of %d refused dry runs", n)
	}
	if r, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}); err != nil || !r.Downloaded {
		t.Fatalf("a real download: %+v %v", r, err)
	}
}

// TestAgentTrustsACertificateOnlyOnTheUploadsOptIn: a CPU that asks TLS
// trust going online fails an upload by default, naming the question; with
// the upload's explicit trustUnverifiedCertificate it is trusted, blindly,
// and the result and the audit log say so.
func TestAgentTrustsACertificateOnlyOnTheUploadsOptIn(t *testing.T) {
	p, s := downloadReady(t)
	p.SetScript(t, tiadtest.Script{UploadAsksTLS: true})
	err := s.Upload(ctx)
	if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "trust the TLS certificate of PLC_1") || !strings.Contains(err.Error(), "tiad trusts no CPU certificate") {
		t.Fatalf("an upload a CPU asks TLS trust for, no opt-in: %v, want operation_failed naming the question", err)
	}
	// A failed upload leaves the project part-way: no apply, no save,
	// until an upload goes through (#789).
	if _, err := s.ApplyStation(ctx, tiadtest.BenchStation(), tiadtest.BenchSecrets); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("an apply after a failed upload: %v", err)
	}
	if _, err := s.Save(ctx, false); tiad.Kind(err) != tiad.KindUncleanSession {
		t.Fatalf("a save after a failed upload: %v", err)
	}
	r, err := s.UploadWith(ctx, tiad.UploadRequest{TrustUnverifiedCertificate: true})
	if err != nil || len(r.TrustedBlindly) != 1 || !strings.Contains(r.TrustedBlindly[0], "trust the TLS certificate of PLC_1") {
		t.Fatalf("an upload with the opt-in: %+v %v, want the question trusted blindly", r, err)
	}
	line := waitLog(t, p, "audit upload")
	for _, want := range []string{"address=\"192.0.2.10\"", "trustUnverifiedCertificate=yes", "trustedBlindly=[\"trust the TLS certificate of PLC_1"} {
		if !strings.Contains(line, want) {
			t.Errorf("the audit line lacks %s:\n%s", want, line)
		}
	}
	// The upload that went through cleans the session.
	if _, err := s.Save(ctx, false); err != nil {
		t.Fatalf("a save after an upload went through: %v", err)
	}
	// Its CPU, named, is kept with its IO device, and the download's
	// audit line journals the device.
	st := tiadtest.BenchStation()
	st.Profinet = nil
	st.CPU.ConfigurationProtection = tiad.ProtectKeep
	st.CPU.AccessControl = tiad.AccessKeep
	if _, err := s.ApplyStation(ctx, st, tiadtest.BenchSecrets); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Hardware: true, Software: true, Announced: true,
		Decisions: tiad.Decisions{StartAfter: tiad.No(), KeptDevices: []string{"io-device-up"}}}); err != nil {
		t.Fatal(err)
	}
	line = waitLog(t, p, "audit download")
	for _, want := range []string{`kept=["io-device-up" profinetName="io-device-up" address="192.0.2.30"]`, `keptDevices=["io-device-up"]`} {
		if !strings.Contains(line, want) {
			t.Errorf("the download's audit line lacks %s:\n%s", want, line)
		}
	}
}
