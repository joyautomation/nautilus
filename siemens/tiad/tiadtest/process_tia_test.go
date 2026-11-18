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
	p.SetGates(t, []tiad.Gate{{Name: "licence", OK: false}})
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
	sec := tiad.Secrets{WebUsers: map[string]string{"naut": "web-password-must-not-leak"}, Protection: "protection-must-not-leak"}
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
	time.Sleep(200 * time.Millisecond)
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
