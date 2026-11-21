package tiadtest_test

import (
	"fmt"
	"io/fs"
	"net/http"
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
	// A download that does not say whether the CPU starts after loading
	// is refused before TIA hears of it.
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true}); tiad.Kind(err) != tiad.KindDecisionRequired || len(tiad.Questions(err)) != 1 || tiad.Questions(err)[0].ID != tiad.QuestionStartAfter {
		t.Fatalf("no start-after: %v", err)
	}
	if n := countCalls(p.Calls(t), "Download"); n != 0 {
		t.Fatalf("TIA heard of a download without start-after (%d)", n)
	}
	// So is one that loads hardware without the opt-in and the
	// attestation (#789 hardware gate), on this CPU as on any.
	hw := tiad.DownloadRequest{Address: "192.0.2.10", Hardware: true, Software: true, Announced: true, Decisions: tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.No()}}
	if _, err := s.Download(ctx, hw); tiad.Kind(err) != tiad.KindDecisionRequired || len(tiad.Questions(err)) != 1 || tiad.Questions(err)[0].ID != tiad.QuestionUnverifiedHardware {
		t.Fatalf("hardware without the opt-in: %v", err)
	}
	hw.Decisions.AcceptUnverifiedHardware = tiad.Yes()
	if _, err := s.Download(ctx, hw); tiad.Kind(err) != tiad.KindDecisionRequired || !strings.Contains(err.Error(), "attestation") {
		t.Fatalf("hardware without the attestation: %v", err)
	}
	if n := countCalls(p.Calls(t), "Download"); n != 0 {
		t.Fatalf("TIA heard of a hardware download without the opt-in or the attestation (%d)", n)
	}
	// A dry run and an unanswered download reach TIA and load nothing.
	if _, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, DryRun: true, Announced: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}); tiad.Kind(err) != tiad.KindDecisionRequired {
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
	real := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}
	no := real
	no.Decisions = tiad.Decisions{StopCPU: tiad.No(), StartAfter: tiad.No()}
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
		// A dry run TIA asks nothing about cannot be cancelled.
		{"never asks, dry run", tiadtest.Script{Questions: stop, AskFirst: intp(0)}, dry},
		{"asks twice", tiadtest.Script{Questions: stop, AskFirst: intp(2)}, yes},
		// Stop answered yes and written, then a check the request does
		// not answer: TIA may already have stopped the CPU.
		{"cancels after an acting answer", tiadtest.Script{Questions: []tiad.Question{{ID: tiad.QuestionStopCPU}, {ID: tiad.QuestionOverwrite}}}, yes},
		// TIA's side fails plainly (not in doubt) after asking a check:
		// the agent itself says outcome_unknown.
		{"fails plainly after a check", tiadtest.Script{CleanCancel: true, Questions: stop, FailAfterDecide: "The connection to the CPU was lost"}, yes},
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
		if tc.name == "cancels after an acting answer" && (strings.Join(tiad.ActingAnswers(err), ",") != "stop-cpu" || !strings.Contains(err.Error(), "TIA may already have stopped the CPU: check its mode and start it")) {
			t.Errorf("%s: %v (acting %v), want it to name stop-cpu and what to do", tc.name, err, tiad.ActingAnswers(err))
		}
		p.SetScript(t, tiadtest.Script{})
		if _, err := s.Compare(ctx); err != nil {
			t.Errorf("%s: compare on the same session: %v", tc.name, err)
		}
	}
	// A TIA side that reports a clean cancel after an acting answer
	// (breaking the ITia contract): the agent itself says outcome_unknown,
	// naming the answer.
	p.SetScript(t, tiadtest.Script{CleanCancel: true, Questions: []tiad.Question{{ID: tiad.QuestionStopCPU}, {ID: tiad.QuestionOverwrite}}})
	_, err := s.Download(ctx, yes)
	if tiad.Kind(err) != tiad.KindOutcomeUnknown || strings.Join(tiad.ActingAnswers(err), ",") != "stop-cpu" ||
		!strings.Contains(err.Error(), "at TIA's overwrite check") || !strings.Contains(err.Error(), "check its mode and start it") {
		t.Fatalf("a clean cancel after an acting answer: %v (acting %v)", err, tiad.ActingAnswers(err))
	}
	// A download TIA asks nothing about is approved by the request alone,
	// and succeeds only on evidence: TIA's result says loaded and an
	// online compare right after is equal (#789).
	p.SetScript(t, tiadtest.Script{AskFirst: intp(0)})
	compares := countCalls(p.Calls(t), "Compare")
	if r, err := s.Download(ctx, yes); err != nil || !r.Downloaded || !r.CompareEqual || r.HardwareVerified != nil {
		t.Fatalf("TIA asks nothing, compare equal: %+v %v", r, err)
	}
	if n := countCalls(p.Calls(t), "Compare"); n != compares+1 {
		t.Fatalf("the download compared %d times, want once", n-compares)
	}
	// TIA asks nothing and its result says the load failed: in doubt,
	// never loaded (#1154 review), even if a compare would be equal.
	p.SetScript(t, tiadtest.Script{AskFirst: intp(0), DownloadState: "Error"})
	if _, err := s.Download(ctx, yes); tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(err.Error(), "TIA's download ended Error") {
		t.Fatalf("TIA asks nothing, result Error: %v, want outcome_unknown naming it", err)
	}
	// A dry run TIA runs without asking never starts the CPU, even when
	// the request says start-after yes (#1154 review).
	p.SetScript(t, tiadtest.Script{AskFirst: intp(0)})
	dryStart := dry
	dryStart.Decisions = tiad.Decisions{StartAfter: tiad.Yes()}
	if _, err := s.Download(ctx, dryStart); tiad.Kind(err) != tiad.KindOutcomeUnknown {
		t.Fatalf("a dry run TIA asks nothing about: %v", err)
	}
	if l := p.Log(); !strings.Contains(l, "start-after written NoAction") || strings.Contains(l, "start-after written StartModule") {
		t.Fatalf("a dry run's after-download start-after: want NoAction only:\n%s", l)
	}
	// A real download that said start it and then fails in doubt names
	// start-after: TIA may have started the CPU after loading.
	start := real
	start.Decisions = tiad.Decisions{StartAfter: tiad.Yes()}
	p.SetScript(t, tiadtest.Script{AskFirst: intp(0), CompareDiffers: true})
	if _, err := s.Download(ctx, start); tiad.Kind(err) != tiad.KindOutcomeUnknown || strings.Join(tiad.ActingAnswers(err), ",") != "start-after" || !strings.Contains(err.Error(), "started the CPU") {
		t.Fatalf("start-after yes, then the compare differs: %v (acting %v)", err, tiad.ActingAnswers(err))
	}
	p.SetScript(t, tiadtest.Script{AskFirst: intp(0), CompareDiffers: true})
	if _, err := s.Download(ctx, yes); tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(err.Error(), "the compare right after it differs (PLC_1 different)") {
		t.Fatalf("TIA asks nothing, compare differs: %v, want outcome_unknown naming it", err)
	}
	p.SetScript(t, tiadtest.Script{AskFirst: intp(0), Failures: []tiadtest.ScriptedCall{{Call: "Compare", Message: "the CPU went offline"}}})
	if _, err := s.Download(ctx, yes); tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(err.Error(), "the compare right after it failed") || !strings.Contains(err.Error(), "the CPU went offline") {
		t.Fatalf("TIA asks nothing, compare fails: %v, want outcome_unknown naming it", err)
	}
	p.SetScript(t, tiadtest.Script{})
	// A TIA failure before anything was decided is a plain failure.
	p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "Download", Message: "No route to the CPU"}}})
	if _, err := s.Download(ctx, yes); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("a failure before decide: %v", err)
	}
}

// TestAgentAnswersConsistentDownload: #789 walk-past window. A real
// software download onto a CPU in RUN asks StopModules, then
// ConsistentBlocksDownload ("Download software to device"). Unanswered,
// tiad cancels there after the stop it wrote: outcome_unknown naming the
// check and stop-cpu. Answered yes (consistentDownload), the download
// loads and succeeds on evidence. Overwrite never answers it.
func TestAgentAnswersConsistentDownload(t *testing.T) {
	p, s := downloadReady(t)
	qs := []tiad.Question{{ID: tiad.QuestionStopCPU}, {ID: tiad.QuestionConsistentDownload, Message: "Download software to device"}}
	req := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true,
		Decisions: tiad.Decisions{StopCPU: tiad.Yes(), Overwrite: tiad.Yes(), StartAfter: tiad.Yes()}}
	p.SetScript(t, tiadtest.Script{Questions: qs})
	_, err := s.Download(ctx, req)
	if tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(err.Error(), "ConsistentBlocksDownload") ||
		!strings.Contains(strings.Join(tiad.ActingAnswers(err), ","), "stop-cpu") {
		t.Fatalf("stop yes, consistent download unanswered: %v (acting %v), want outcome_unknown naming the check and stop-cpu", err, tiad.ActingAnswers(err))
	}
	if l := p.Log(); !strings.Contains(l, `consistentDownload=unanswered`) || !strings.Contains(l, `asked=["stop-cpu","consistent-download"] acting=["stop-cpu","start-after"] outcome=outcome_unknown`) {
		t.Fatalf("the audit must show the unanswered check and only stop (and start-after) acting:\n%s", l)
	}
	req.Decisions.ConsistentDownload = tiad.Yes()
	p.SetScript(t, tiadtest.Script{Questions: qs})
	if r, err := s.Download(ctx, req); err != nil || !r.Downloaded || !r.CompareEqual {
		t.Fatalf("stop yes, consistent download yes: %+v %v, want a verified load", r, err)
	}
	// A CPU already in STOP: TIA asks consistent-download first, then a
	// check the request leaves unanswered. The yes was acting, so the
	// cancel is outcome_unknown naming it, never a clean refusal.
	p.SetScript(t, tiadtest.Script{Questions: []tiad.Question{{ID: tiad.QuestionConsistentDownload}, {ID: tiad.QuestionReinitialize, Objects: []string{"Tags"}}}})
	_, err = s.Download(ctx, req)
	if tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(strings.Join(tiad.ActingAnswers(err), ","), "consistent-download") ||
		!strings.Contains(err.Error(), "may already have loaded the software") {
		t.Fatalf("consistent-download yes, then a refusal: %v (acting %v), want outcome_unknown naming consistent-download", err, tiad.ActingAnswers(err))
	}
	p.SetScript(t, tiadtest.Script{Questions: qs})
	if _, err := s.Download(ctx, req); err != nil {
		t.Fatal(err)
	}
	if l := p.Log(); !strings.Contains(l, `consistentDownload=yes`) || !strings.Contains(l, `acting=["stop-cpu","consistent-download"] outcome=loaded`) {
		t.Fatalf("the audit must show consistent-download answered yes, acting, and loaded:\n%s", l)
	}
	p.SetScript(t, tiadtest.Script{})
}

// TestAgentAnswersTheSoftwareLoadFamily: #789 restart window. A real
// software download onto a CPU in RUN asks StopModules,
// ConsistentBlocksDownload, then AlarmTextLibrariesDownload, and may ask
// AllBlocksDownload, UserManagementDownload and LoadIdentificationData.
// Answered up front, it loads; keeping the CPU's online users is accepted
// only under a spec whose accessControl is keep, and the passive answers
// are never named as acting.
func TestAgentAnswersTheSoftwareLoadFamily(t *testing.T) {
	p, s := downloadReady(t)
	qs := []tiad.Question{{ID: tiad.QuestionStopCPU}, {ID: tiad.QuestionConsistentDownload}, {ID: tiad.QuestionAlarmTexts},
		{ID: tiad.QuestionAllBlocks}, {ID: tiad.QuestionUserManagement}, {ID: tiad.QuestionIdentificationData}}
	req := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true,
		Decisions: tiad.Decisions{StopCPU: tiad.Yes(), ConsistentDownload: tiad.Yes(), AlarmTexts: tiad.Yes(), AllBlocks: tiad.Yes(),
			UserManagementKeepOnline: tiad.Yes(), IdentificationDataLoadNothing: tiad.Yes(), StartAfter: tiad.Yes()}}
	// The bench spec sets access control: keeping the CPU's users is
	// refused at that check, after the loading answers were written.
	p.SetScript(t, tiadtest.Script{Questions: qs})
	_, err := s.Download(ctx, req)
	if tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(err.Error(), "UserManagementDownload") ||
		strings.Join(tiad.ActingAnswers(err), ",") != "stop-cpu,consistent-download,alarm-texts,all-blocks,start-after" {
		t.Fatalf("user management under a spec that sets access control: %v (acting %v)", err, tiad.ActingAnswers(err))
	}
	// Keep after fullAccessUser on the same session keeps what tiad set,
	// not what the project had: still refused (#1159 review).
	keep := tiadtest.BenchStation()
	keep.CPU.AccessControl = tiad.AccessKeep
	if _, err := s.ApplyStation(ctx, keep, tiadtest.BenchSecrets); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	p.SetScript(t, tiadtest.Script{Questions: qs})
	if _, err := s.Download(ctx, req); tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(err.Error(), "UserManagementDownload") {
		t.Fatalf("keep after fullAccessUser on one session: %v, want user management refused", err)
	}
	// A fresh session on the saved project that only ever keeps access
	// control: keeping the CPU's online users is accepted.
	if _, err := s.Save(ctx, false); err != nil {
		t.Fatal(err)
	}
	project := s.Project
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s, err = p.Client().Open(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyStation(ctx, keep, tiadtest.BenchSecrets); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	p.SetScript(t, tiadtest.Script{Questions: qs})
	if r, err := s.Download(ctx, req); err != nil || !r.Downloaded || !r.CompareEqual {
		t.Fatalf("the whole family answered up front under keep: %+v %v, want a verified load", r, err)
	}
	if l := p.Log(); !strings.Contains(l, `alarmTexts=yes allBlocks=yes userManagementKeepOnline=yes userManagementResetToProject=unanswered identificationDataLoadNothing=yes`) ||
		!strings.Contains(l, `acting=["stop-cpu","consistent-download","alarm-texts","all-blocks"] outcome=loaded`) {
		t.Fatalf("the audit must name each decision and only the loading answers as acting:\n%s", l)
	}
	// The same opened session then sets access control and keeps it
	// again: the apply set it, so keeping the CPU's users is refused.
	if _, err := s.ApplyStation(ctx, tiadtest.BenchStation(), tiadtest.BenchSecrets); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyStation(ctx, keep, tiadtest.BenchSecrets); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	p.SetScript(t, tiadtest.Script{Questions: qs})
	if _, err := s.Download(ctx, req); tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(err.Error(), "UserManagementDownload") {
		t.Fatalf("keep after an apply of fullAccessUser on an opened session: %v, want user management refused", err)
	}
	p.SetScript(t, tiadtest.Script{})
}

// TestAgentNeverCallsAnEmptyCompareEqual: #789. TIA's compare tree is
// equal only when its root says identical, at least one object below the
// root was compared, nothing anywhere in it differs, and every program
// block the project holds is in it, identical. A tree that compares
// nothing, hides a difference in a folder, or leaves out a block of the
// project (the stage 3 gate), or a project whose blocks were not read,
// is not equal:
// /compare says equal false and names why, and a download TIA reported
// loaded ends outcome_unknown naming it, and start-after, since TIA may
// have started the CPU.
func TestAgentNeverCallsAnEmptyCompareEqual(t *testing.T) {
	p, s := downloadReady(t)
	start := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true,
		Decisions: tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.Yes()}}
	// The bench-shaped equal tree passes, both ways.
	p.SetScript(t, tiadtest.Script{AskFirst: intp(0)})
	if r, err := s.Download(ctx, start); err != nil || !r.Downloaded || !r.CompareEqual {
		t.Fatalf("download, bench-shaped equal tree: %+v %v", r, err)
	}
	if r, err := s.Compare(ctx); err != nil || !r.Equal || len(r.Differences) != 0 {
		t.Fatalf("compare, bench-shaped equal tree: %+v %v", r, err)
	}
	for _, tc := range []struct{ tree, named string }{
		{"empty", "PLC_1 nothing compared, Main [OB1] not compared"},
		{"emptyFolders", "PLC_1 nothing compared, Main [OB1] not compared"},
		{"folderDiffers", "PLC tags contents differ"},
		{"irrelevantRoot", "PLC_1 not compared"},
		{"dropBlock", "Main [OB1] not compared"},
		{"noBlocks", "no program blocks in the project (or none read)"},
	} {
		p.SetScript(t, tiadtest.Script{AskFirst: intp(0), CompareTree: tc.tree})
		r, err := s.Compare(ctx)
		var named []string
		if err == nil {
			for _, d := range r.Differences {
				named = append(named, strings.TrimSpace(d.Object+" "+d.State))
			}
		}
		if err != nil || r.Equal || strings.Join(named, ", ") != tc.named {
			t.Errorf("compare, tree %s: %+v %v, want not equal naming %q", tc.tree, r, err, tc.named)
		}
		_, err = s.Download(ctx, start)
		if tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(err.Error(), "the compare right after it differs ("+tc.named+")") ||
			strings.Join(tiad.ActingAnswers(err), ",") != "start-after" {
			t.Errorf("download, tree %s: %v (acting %v), want outcome_unknown naming %q and start-after", tc.tree, err, tiad.ActingAnswers(err), tc.named)
		}
	}
	p.SetScript(t, tiadtest.Script{})
}

// TestAgentSeesTheBlockItSent: #789 stage 3 gate. A download that
// loaded an imported block succeeds only when the compare right after
// saw that block, in its user group, identical: the response lists it.
// A compare that leaves it out names it, on /compare and on a download
// TIA reported loaded (outcome_unknown, start-after named).
func TestAgentSeesTheBlockItSent(t *testing.T) {
	p, s := downloadReady(t)
	c := p.Client()
	if _, err := c.PutFile(ctx, "src/Pump.scl", []byte("FUNCTION_BLOCK \"Pump\"\r\nBEGIN\r\nEND_FUNCTION_BLOCK\r\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Pump.scl"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	start := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true,
		Decisions: tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.Yes()}}
	p.SetScript(t, tiadtest.Script{AskFirst: intp(0)})
	r, err := s.Download(ctx, start)
	if err != nil || !r.Downloaded || !r.CompareEqual || strings.Join(r.Blocks, ", ") != "Main [OB1], Pump [FB1]" {
		t.Fatalf("download of an imported block: %+v %v, want equal listing Main [OB1], Pump [FB1]", r, err)
	}
	if cr, err := s.Compare(ctx); err != nil || !cr.Equal || strings.Join(cr.Blocks, ", ") != "Main [OB1], Pump [FB1]" {
		t.Fatalf("compare after it: %+v %v", cr, err)
	}
	p.SetScript(t, tiadtest.Script{AskFirst: intp(0), CompareTree: "dropBlock"})
	if cr, err := s.Compare(ctx); err != nil || cr.Equal || len(cr.Differences) != 1 || cr.Differences[0].Object != "Pump [FB1]" || cr.Differences[0].State != "not compared" {
		t.Errorf("compare that leaves the block out: %+v %v, want Pump [FB1] not compared", cr, err)
	}
	_, err = s.Download(ctx, start)
	if tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(err.Error(), "the compare right after it differs (Pump [FB1] not compared)") ||
		strings.Join(tiad.ActingAnswers(err), ",") != "start-after" {
		t.Errorf("download whose compare leaves the block out: %v (acting %v)", err, tiad.ActingAnswers(err))
	}
	p.SetScript(t, tiadtest.Script{})
}

// TestAgentSaysUnknownWhenTIADiesInsideTheDownload: #789 stage 2. TIA
// died inside its download call before asking anything, and detaching
// tiad's handlers from its disposed objects threw too; that second error
// replaced the first and the agent answered operation_failed. Once TIA's
// download call was entered the CPU may or may not be loaded, whatever
// the failure, so the answer is outcome_unknown, dry run or not; before
// the call, operation_failed naming the failure itself, not the detach.
// Either way TIA is gone, and with it every project open on it, and the
// agent starts TIA only once: the answer ends the session (fatal) and
// names the fix (restart the agent, then compare in a fresh session).
// From then on every call that needs TIA is refused with that fix, the
// same session's compare and a new session included, probe shows the tia
// gate failed, and a close drops the session instead of keeping it broken.
func TestAgentSaysUnknownWhenTIADiesInsideTheDownload(t *testing.T) {
	const fix = "Restart the agent, then compare in a fresh session"
	dry := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, DryRun: true, Announced: true}
	real := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true,
		Decisions: tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.No()}}
	for _, dies := range []string{"downloading", "preparing"} {
		for _, req := range []tiad.DownloadRequest{dry, real} {
			name := fmt.Sprintf("TIA dies %s, dry run %v", dies, req.DryRun)
			p, s := downloadReady(t)
			c := p.Client()
			p.SetScript(t, tiadtest.Script{TiaDies: dies})
			_, err := s.Download(ctx, req)
			if dies == "downloading" {
				if tiad.Kind(err) != tiad.KindOutcomeUnknown || !tiad.OutcomeUnknown(err) || !tiad.IsFatal(err) ||
					!strings.Contains(err.Error(), "DownloadProvider") || !strings.Contains(err.Error(), fix) {
					t.Errorf("%s: %v, want a fatal outcome_unknown naming the download's failure and the fix", name, err)
				}
			} else if tiad.Kind(err) != tiad.KindOperationFailed || !tiad.IsFatal(err) || !strings.Contains(err.Error(), "DownloadProvider") ||
				strings.Contains(err.Error(), "ConnectionConfiguration") || !strings.Contains(err.Error(), fix) {
				t.Errorf("%s: %v, want a fatal operation_failed naming the failure (not the detach) and the fix", name, err)
			}
			pr, err := c.Probe(ctx)
			if g := gate(t, pr, "tia"); err != nil || pr.Usable || g.State != tiad.GateFailed || !strings.Contains(g.Detail, fix) || g.Remedy == "" {
				t.Errorf("%s: probe %+v %v, want the tia gate failed naming the fix", name, pr, err)
			}
			if _, err := s.Compare(ctx); tiad.Kind(err) != tiad.KindGate || !strings.Contains(err.Error(), fix) {
				t.Errorf("%s: compare on the same session: %v, want refused naming the fix", name, err)
			}
			if _, err := c.CreateProject(ctx, "after", tiadtest.BenchStation(), tiadtest.BenchSecrets); tiad.Kind(err) != tiad.KindGate || !strings.Contains(err.Error(), fix) {
				t.Errorf("%s: a new session: %v, want refused naming the fix", name, err)
			}
			if err := s.Close(ctx); err != nil {
				t.Errorf("%s: close: %v", name, err)
			}
			if l, err := c.Sessions(ctx); err != nil || len(l) != 0 {
				t.Errorf("%s: sessions after the close: %+v %v, want none (not kept broken)", name, l, err)
			}
			if l := p.Loaded(t); len(l) != 0 {
				t.Errorf("%s: a TIA that died loaded %+v", name, l)
			}
		}
	}
}

// TestAgentRefusesACallQueuedBehindTheOneTIADiedIn: a call that passed
// the gates while TIA was still alive, then waited on the TIA thread
// behind the download TIA died in, is refused naming the fix too; it
// never runs on the dead TIA.
func TestAgentRefusesACallQueuedBehindTheOneTIADiedIn(t *testing.T) {
	p, s := downloadReady(t)
	c := p.Client()
	other, err := c.CreateProject(ctx, "other", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	p.SetScript(t, tiadtest.Script{TiaDies: "downloading", Delays: []tiadtest.ScriptedCall{{Call: "Download", MS: 1500}}})
	done := make(chan error, 1)
	go func() {
		_, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, DryRun: true, Announced: true})
		done <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); countCalls(p.Calls(t), "Download") < 1; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the download never reached TIA's side")
		}
	}
	_, err = other.Compile(ctx, true, true)
	if tiad.Kind(err) != tiad.KindGate || !strings.Contains(err.Error(), "Restart the agent") {
		t.Errorf("a compile queued behind the dying download: %v, want refused naming the fix", err)
	}
	if err := <-done; tiad.Kind(err) != tiad.KindOutcomeUnknown || !tiad.IsFatal(err) {
		t.Errorf("the download TIA died in: %v", err)
	}
}

// TestAgentDecidesEveryDryRun: #789 stage-2 retry. TIA asks its checks
// one at a time, with no parent and nothing listing them; the dry run
// cancelled at the first as "cannot list", which came back
// outcome_unknown. A dry run now declines at the first check, cleanly:
// nothing loaded, that one question, marked incomplete, and TIA asked
// nothing more.
func TestAgentDecidesEveryDryRun(t *testing.T) {
	p, s := downloadReady(t)
	p.SetQuestions(t, []tiad.Question{{ID: tiad.QuestionStopCPU, Message: "The modules are stopped for downloading to device."}, {ID: tiad.QuestionOverwrite}, {ID: tiad.QuestionStartAfter}})
	r, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, DryRun: true, Announced: true})
	if err != nil || !r.DryRun || !r.Provisional || !r.QuestionsIncomplete || r.Downloaded || len(r.Questions) != 1 || r.Questions[0].ID != tiad.QuestionStopCPU {
		t.Fatalf("a dry run: %+v %v", r, err)
	}
	if l := p.Loaded(t); len(l) != 0 {
		t.Fatalf("a dry run loaded %+v", l)
	}
	log := waitLog(t, p, "outcome=dry-run")
	if !strings.Contains(log, `asked=["stop-cpu"] acting=[]`) {
		t.Fatalf("the audit line: want one check asked, nothing acting:\n%s", log)
	}
	// Decisions that answer every check yes never make a dry run load.
	r, err = s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, DryRun: true, Announced: true,
		Decisions: tiad.Decisions{StopCPU: tiad.Yes(), Overwrite: tiad.Yes(), StartAfter: tiad.Yes()}})
	if err != nil || !r.DryRun || r.Downloaded || len(r.Questions) != 1 {
		t.Fatalf("a dry run with every check answered yes: %+v %v", r, err)
	}
	if l := p.Loaded(t); len(l) != 0 {
		t.Fatalf("a dry run with every check answered yes loaded %+v", l)
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
	log := waitLog(t, p, "outcome=loaded state=STOP (tia-message)")
	line := ""
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "audit download") && strings.Contains(l, "outcome=loaded") {
			line = l
		}
	}
	for _, want := range []string{"address=\"192.0.2.10\"", "dryRun=False", "announced=True", "stopCpu=yes", "startAfter=no",
		"protection=set", "configurationPassword=set", `asked=["stop-cpu","password","configuration-password"]`, `acting=["stop-cpu"]`, `reinitialize=["evil\naudit download forged"]`} {
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

// TestAgentCarriesTheModeOfADoubtfulDownload: a download that loaded and
// then ends in doubt (the compare after it differs or fails) still says
// the CPU's mode TIA's messages gave, labelled as that evidence; one TIA
// never stopped says none.
func TestAgentCarriesTheModeOfADoubtfulDownload(t *testing.T) {
	p, s := downloadReady(t)
	stop := []tiad.Question{{ID: tiad.QuestionStopCPU}}
	req := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Decisions: tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.No()}}
	for _, sc := range []tiadtest.Script{{Questions: stop, CompareDiffers: true}, {Questions: stop, Failures: []tiadtest.ScriptedCall{{Call: "Compare", Message: "the CPU went offline"}}}} {
		p.SetScript(t, sc)
		_, err := s.Download(ctx, req)
		if state, source := tiad.StateOf(err); tiad.Kind(err) != tiad.KindOutcomeUnknown || state != "STOP" || source != tiad.StateFromTIAMessage ||
			strings.Join(tiad.ActingAnswers(err), ",") != "stop-cpu" {
			t.Fatalf("%+v: %v (state %q %q)", sc, err, state, source)
		}
	}
	p.SetScript(t, tiadtest.Script{CompareDiffers: true})
	if _, err := s.Download(ctx, req); tiad.Kind(err) != tiad.KindOutcomeUnknown {
		t.Fatalf("never stopped: %v", err)
	} else if state, source := tiad.StateOf(err); state != "" || source != "" {
		t.Fatalf("never stopped, yet state %q %q", state, source)
	}
}

// TestAgentJournalsTheHardwareAttestation: the operator's attestation that
// stands in for the hardware check (#789 hardware gate) is in the audit
// line on every outcome: who, what, and when by tiad's clock once it was
// accepted, each quoted so a request cannot forge a line.
func TestAgentJournalsTheHardwareAttestation(t *testing.T) {
	p, s := downloadReady(t)
	att := &tiad.Attestation{By: "op\naudit download forged", Text: "hardware as intended\r\u2028audit download forged"}
	req := tiad.DownloadRequest{Address: "192.0.2.10", Hardware: true, Software: true, Announced: true,
		Decisions: tiad.Decisions{StartAfter: tiad.No(), AcceptUnverifiedHardware: tiad.Yes()}, Attestation: att}
	quoted := `by="op\naudit download forged" at=20`
	text := `text="hardware as intended\r\u2028audit download forged" cpuAccessControl=unset}`
	auditLine := func(outcome string) string {
		t.Helper()
		log := waitLog(t, p, "outcome="+outcome)
		for _, l := range strings.Split(log, "\n") {
			if strings.Contains(l, "Z audit download") && strings.Contains(l, "outcome="+outcome) {
				return l
			}
		}
		t.Fatalf("no audit line with outcome=%s:\n%s", outcome, log)
		return ""
	}

	res, err := s.Download(ctx, req)
	if err != nil || res.Attestation == nil || res.Attestation.By != att.By || res.Attestation.Text != att.Text {
		t.Fatalf("download: %+v %v", res, err)
	}
	line := auditLine("loaded")
	for _, want := range []string{"hardware=True", "acceptUnverifiedHardware=yes", "attestation={" + quoted + res.Attestation.At[2:], text, "hardwareVerified=false"} {
		if !strings.Contains(line, want) {
			t.Errorf("the audit line lacks %s:\n%s", want, line)
		}
	}

	// A download that ends in doubt after loading hardware journals the
	// attestation too: that is where it matters most.
	p.SetScript(t, tiadtest.Script{CompareDiffers: true})
	if _, err := s.Download(ctx, req); tiad.Kind(err) != tiad.KindOutcomeUnknown {
		t.Fatalf("compare differs: %v", err)
	}
	if line := auditLine(tiad.KindOutcomeUnknown); !strings.Contains(line, "attestation={"+quoted) || !strings.Contains(line, text) {
		t.Errorf("the outcome_unknown audit line lacks the attestation:\n%s", line)
	}
	p.SetScript(t, tiadtest.Script{})

	// A refusal journals what was said, never accepted, so no time.
	refused := req
	refused.Decisions.AcceptUnverifiedHardware = nil
	if _, err := s.Download(ctx, refused); tiad.Kind(err) != tiad.KindDecisionRequired {
		t.Fatalf("no opt-in: %v", err)
	}
	if line := auditLine(tiad.KindDecisionRequired); !strings.Contains(line, `attestation={by="op\naudit download forged" at=unset `+text) || !strings.Contains(line, "acceptUnverifiedHardware=unanswered") {
		t.Errorf("the refusal's audit line:\n%s", line)
	}
	// So does a refusal after the gate passed: tiad accepts the
	// attestation only once nothing of its own refuses the download.
	late := req
	late.Decisions.StartAfter = nil
	if _, err := s.Download(ctx, late); tiad.Kind(err) != tiad.KindDecisionRequired || !strings.Contains(err.Error(), "startAfter") {
		t.Fatalf("no start-after: %v", err)
	}
	if line := auditLine(tiad.KindDecisionRequired + `:["start-after"]`); !strings.Contains(line, `attestation={by="op\naudit download forged" at=unset `+text) {
		t.Errorf("the start-after refusal's audit line stamps a time:\n%s", line)
	}
	// One over the limit is refused, and journalled by its size only, so
	// a request cannot flood the journal.
	// The forged lines start nowhere: every audit line is tiad's own.
	if n, m := strings.Count(p.Log(), "Z audit download"), strings.Count(p.Log(), "audit download"); n != m-8 {
		t.Fatalf("%d audit lines and %d mentions (want 8 forged mentions inside quotes):\n%s", n, m, p.Log())
	}
	huge := req
	huge.Attestation = &tiad.Attestation{By: "op", Text: strings.Repeat("flood ", 1000)}
	status, b := rawPost(t, p, "/v1/sessions/"+s.ID+"/download", huge)
	if status != http.StatusBadRequest {
		t.Fatalf("an attestation over the limit: HTTP %d %s", status, b)
	}
	if line := auditLine(tiad.KindBadRequest); !strings.Contains(line, `text=<6000 bytes, over the limit>`) || strings.Contains(line, "flood") {
		t.Errorf("the over-limit attestation's audit line:\n%s", line)
	}
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
	dl := tiadtest.AcceptHardware(tiad.DownloadRequest{Address: "192.0.2.10", Hardware: true, Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}})
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
	// The trusted upload left TIA's instance trusting: a download rides
	// on it, so it is refused before anything reaches the CPU until it
	// says decisions.acceptBlindTrust yes.
	dl := tiadtest.AcceptHardware(tiad.DownloadRequest{Address: "192.0.2.10", Hardware: true, Software: true, Announced: true,
		Decisions: tiad.Decisions{StartAfter: tiad.No(), KeptDevices: []string{"io-device-up"}}})
	if _, err := s.Download(ctx, dl); tiad.Kind(err) != tiad.KindDecisionRequired || tiad.BlindTrustOf(err) == nil || tiad.BlindTrustOf(err).Call != "upload" {
		t.Fatalf("a download in the instance the upload trusted: %v, want decision_required naming the upload's trust", err)
	}
	if l := p.Loaded(t); len(l) != 0 {
		t.Fatalf("a refused download loaded: %+v", l)
	}
	// Refused by the agent's own check, before the pin, the claim and TIA:
	// the audit line names the question.
	refused := lastLine(waitLog(t, p, "audit download"), "audit download")
	for _, want := range []string{`asked=["blind-trust"]`, `outcome=decision_required:["blind-trust"]`, "acceptBlindTrust=unanswered", `blindTrust={since="`} {
		if !strings.Contains(refused, want) {
			t.Errorf("the refused download's audit line lacks %s:\n%s", want, refused)
		}
	}
	dl.Decisions.AcceptBlindTrust = tiad.Yes()
	if _, err := s.Download(ctx, dl); err != nil {
		t.Fatal(err)
	}
	line = lastLine(waitLog(t, p, "outcome=loaded"), "audit download")
	for _, want := range []string{`kept=["io-device-up" profinetName="io-device-up" address="192.0.2.30"]`, `keptDevices=["io-device-up"]`, "acceptBlindTrust=yes",
		`blindTrust={since="`, `call="upload" session="s1" address="192.0.2.10" questions=["trust the TLS certificate of PLC_1`} {
		if !strings.Contains(line, want) {
			t.Errorf("the download's audit line lacks %s:\n%s", want, line)
		}
	}
}

// TestAgentRefusesADownloadQueuedBehindATrust: the blind-trust check runs
// again on the TIA thread, where trust is recorded, so a download whose
// first check passed while another session's fingerprint read was still
// in TIA is refused once that read trusted, before TIA's download is
// called.
func TestAgentRefusesADownloadQueuedBehindATrust(t *testing.T) {
	p, s := downloadReady(t)
	c := p.Client()
	st := tiadtest.BenchStation()
	st.CPU.Address = "192.0.2.99"
	s2, err := c.CreateProject(ctx, "other", st, tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	p.SetScript(t, tiadtest.Script{FingerprintAsksTLS: true, Delays: []tiadtest.ScriptedCall{{Call: "ReadFingerprint", MS: 1500}}})
	done := make(chan error, 1)
	go func() {
		_, err := s2.Fingerprint(ctx, tiad.FingerprintRequest{TrustUnverifiedCertificate: true})
		done <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); countCalls(p.Calls(t), "ReadFingerprint") == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the fingerprint read never reached TIA's side")
		}
	}
	_, err = s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}})
	if tiad.Kind(err) != tiad.KindDecisionRequired || tiad.BlindTrustOf(err) == nil || tiad.BlindTrustOf(err).Session != s2.ID {
		t.Fatalf("a download queued behind a trusting read: %v, want decision_required naming that read's trust", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the fingerprint read: %v", err)
	}
	if n := countCalls(p.Calls(t), "Download"); n != 0 {
		t.Fatalf("TIA's download was called %d times, want 0", n)
	}
	if l := p.Loaded(t); len(l) != 0 {
		t.Fatalf("loaded %+v", l)
	}
}

// lastLine is the last line of log that holds want.
func lastLine(log, want string) string {
	var last string
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, want) {
			last = l
		}
	}
	return last
}

// TestAgentJournalsTheFingerprintRead: every read is audited, refusals
// too, with each item TIA returned (quoted, so none forges a line), what
// it trusted, and the instance's trust before and after; a later compare
// says it rode on that trust. A read refused before TIA never reaches it.
func TestAgentJournalsTheFingerprintRead(t *testing.T) {
	p, s := downloadReady(t)
	p.SetFingerprint(t, tiadtest.FingerprintScript{AsksTLS: true})
	if _, err := s.Fingerprint(ctx, tiad.FingerprintRequest{}); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("a read without the opt-in: %v", err)
	}
	line := lastLine(waitLog(t, p, "audit fingerprint"), "audit fingerprint")
	for _, want := range []string{"trustUnverifiedCertificate=no", "trustedBlindly=[]", "blindTrustBefore=none blindTrust=none", "items=[]", "outcome=operation_failed"} {
		if !strings.Contains(line, want) {
			t.Errorf("the refused read's audit line lacks %s:\n%s", want, line)
		}
	}
	if _, err := s.Fingerprint(ctx, tiad.FingerprintRequest{TrustUnverifiedCertificate: true}); err != nil {
		t.Fatal(err)
	}
	log := waitLog(t, p, "outcome=ok")
	if !strings.Contains(log, "audit blind-trust set call=fingerprint read session=s1") {
		t.Errorf("the trust must be journalled when it is given:\n%s", log)
	}
	line = lastLine(log, "audit fingerprint")
	for _, want := range []string{"trustUnverifiedCertificate=yes", `trustedBlindly=["trust the TLS certificate of PLC_1`,
		"blindTrustBefore=none blindTrust={since=", `"Hardware"="fake-hardware-0"`, `"DownloadId"="fake-download-0"`} {
		if !strings.Contains(line, want) {
			t.Errorf("the trusted read's audit lacks %s:\n%s", want, line)
		}
	}
	p.SetFingerprint(t, tiadtest.FingerprintScript{Items: []tiad.FingerprintItem{{Identifier: "Hardware", Value: "x\n2026-01-01T00:00:00Z audit forged"}}})
	if _, err := s.Fingerprint(ctx, tiad.FingerprintRequest{}); err != nil {
		t.Fatal(err)
	}
	if l := p.Log(); strings.Contains(l, "\n2026-01-01T00:00:00Z audit forged") || !strings.Contains(l, `"Hardware"="x\n2026-01-01T00:00:00Z audit forged"`) {
		t.Errorf("an item's value must be quoted in the audit line:\n%s", l)
	}
	if !strings.Contains(p.Log(), "blindTrustBefore={since=") {
		t.Errorf("a read riding on earlier trust must say so:\n%s", p.Log())
	}
	if _, err := s.Compare(ctx); err != nil {
		t.Fatal(err)
	}
	if line := lastLine(waitLog(t, p, "audit compare"), "audit compare"); !strings.Contains(line, `blindTrust={since="`) || !strings.Contains(line, `call="fingerprint read"`) {
		t.Errorf("a compare in the trusting instance must journal the trust it rides on:\n%s", line)
	}
	// Refused before TIA: no ReadFingerprint call.
	before := 0
	for _, c := range p.Calls(t) {
		if c.Call == "ReadFingerprint" {
			before++
		}
	}
	status, b := rawPost(t, p, "/v1/sessions/"+s.ID+"/fingerprint", map[string]any{"address": "192.0.2.10"})
	if status != http.StatusBadRequest {
		t.Fatalf("a read with an unknown field: HTTP %d %s", status, b)
	}
	after := 0
	for _, c := range p.Calls(t) {
		if c.Call == "ReadFingerprint" {
			after++
		}
	}
	if after != before {
		t.Errorf("a refused read reached TIA's side: %d ReadFingerprint calls, want %d", after, before)
	}
}

// TestAgentJournalsTheUserManagementReset: #789 stage 4. The audit line
// says what came of a user-management reset the request asked: refused
// before TIA (the attestation unstamped), allowed but never asked by TIA,
// or given and by which route; and what the attestation said of the
// CPU's access control, only ever as one of its words.
func TestAgentJournalsTheUserManagementReset(t *testing.T) {
	const bench, other = "192.0.2.10", "192.0.2.99"
	p := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{Targets: []string{bench, other}})
	s := tiadtest.UploadedKeep(t, p, other)
	um := tiad.Question{ID: tiad.QuestionUserManagement, Message: "Delete user management data and disable access control in runtime", Detail: "UserManagementDownload"}
	line := func(outcome string) string {
		t.Helper()
		log := waitLog(t, p, "outcome="+outcome)
		var last string
		for _, l := range strings.Split(log, "\n") {
			if strings.Contains(l, "Z audit download") && strings.Contains(l, "outcome="+outcome) {
				last = l
			}
		}
		if last == "" {
			t.Fatalf("no audit line with outcome=%s:\n%s", outcome, log)
		}
		return last
	}
	want := func(l string, parts ...string) {
		t.Helper()
		for _, w := range parts {
			if !strings.Contains(l, w) {
				t.Errorf("the audit line lacks %s:\n%s", w, l)
			}
		}
	}

	// Refused before TIA: never uploaded from there, nothing attested.
	// tiad read the project, and neither went online nor called TIA's
	// download.
	p.SetScript(t, tiadtest.Script{Questions: []tiad.Question{um}})
	before := p.Calls(t)
	if _, err := s.Download(ctx, tiadtest.ResetLoad(other)); tiad.Kind(err) != tiad.KindDecisionRequired {
		t.Fatalf("no route: %v", err)
	}
	after := p.Calls(t)
	for call, want := range map[string]int{"ReadCpu": 1, "Download": 0, "Identify": 0} {
		if n := countCalls(after, call) - countCalls(before, call); n != want {
			t.Errorf("a refused reset called TIA's %s %d times, want %d", call, n, want)
		}
	}
	want(line(tiad.KindDecisionRequired+`:["user-management"]`), "userManagementResetToProject=yes", "at=unset", "cpuAccessControl=unset}", "userManagementReset=refused", `asked=["user-management"] acting=[]`)

	// Given by the attestation route, and acting.
	att := tiadtest.ResetLoad(other)
	att.Attestation.CPUAccessControl = tiad.AttestCPUAccessDisabled
	if res, err := s.Download(ctx, att); err != nil || res.UserManagementReset != tiad.ResetRouteAttestation {
		t.Fatalf("attested: %+v %v", res, err)
	}
	l := line("loaded")
	want(l, "cpuAccessControl=disabled}", "userManagementReset=given:attestation", `acting=["user-management"`)
	if strings.Contains(l, "at=unset") {
		t.Errorf("an accepted attestation is stamped:\n%s", l)
	}

	// Allowed, but TIA never asked: no reset is claimed.
	p.SetScript(t, tiadtest.Script{Questions: []tiad.Question{{ID: tiad.QuestionStopCPU}}})
	if _, err := s.Download(ctx, att); err != nil {
		t.Fatal(err)
	}
	want(line("loaded"), "userManagementReset=allowed:attestation", `acting=["stop-cpu"`)

	// A word outside the vocabulary is refused and journalled as invalid,
	// never as sent.
	odd := tiadtest.ResetLoad(other)
	odd.Attestation.CPUAccessControl = "off\naudit download forged"
	if status, b := rawPost(t, p, "/v1/sessions/"+s.ID+"/download", odd); status != 400 {
		t.Fatalf("an odd word: HTTP %d %s", status, b)
	}
	want(line(tiad.KindBadRequest), "cpuAccessControl=invalid}", "userManagementReset=unset")
	if n, m := strings.Count(p.Log(), "Z audit download"), strings.Count(p.Log(), "audit download"); n != m {
		t.Errorf("a forged audit line: %d of %d", n, m)
	}
}
