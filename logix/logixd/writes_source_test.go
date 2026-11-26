package logixd_test

// #1409: the agent's controller writes run to their end once started. They
// take CancellationToken.None (through ControllerWrite), never the
// request's token, which ASP.NET cancels when the caller's connection goes.
// The C# side has no test project (it links Rockwell's SDK, which only a
// licensed Windows machine has), so this reads tools/logixd/Program.cs
// and pins the token each write call is given. The behaviour itself was
// checked on an emulated controller (the PR that closed #1409 says how).

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// programSource is tools/logixd/Program.cs.
func programSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "tools", "logixd", "Program.cs"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// route returns the body of one app.Map* route: from its own line up to the
// next route.
func route(t *testing.T, src, method, path string) string {
	t.Helper()
	head := `app.Map` + method + `("` + path + `"`
	i := strings.Index(src, head)
	if i < 0 {
		t.Fatalf("route %s %s not found in Program.cs", method, path)
	}
	body := src[i+len(head):]
	if j := strings.Index(body, "\napp.Map"); j >= 0 {
		body = body[:j]
	}
	return body
}

// lastArgs returns the last argument of every call to name in src.
func lastArgs(t *testing.T, src, name string) []string {
	t.Helper()
	var out []string
	for _, loc := range regexp.MustCompile(`\.`+name+`\(`).FindAllStringIndex(src, -1) {
		depth, start := 1, loc[1]
		last := start
		k := start
		for ; k < len(src) && depth > 0; k++ {
			switch src[k] {
			case '(':
				depth++
			case ')':
				depth--
			case ',':
				if depth == 1 {
					last = k + 1
				}
			}
		}
		if depth != 0 {
			t.Fatalf("unbalanced call to %s", name)
		}
		out = append(out, strings.TrimSpace(src[last:k-1]))
	}
	return out
}

// LGX-034: every SDK call that changes a controller gets the write token
// ControllerWrite hands it, never the request's ct; ControllerWrite hands
// out CancellationToken.None. Reads and the online/offline routes keep ct.
func TestControllerWritesIgnoreTheCallerHangingUp(t *testing.T) {
	src := programSource(t)

	// The helper: the write runs on CancellationToken.None.
	hi := strings.Index(src, "async Task<object?> ControllerWrite(")
	if hi < 0 {
		t.Fatal("ControllerWrite not found in Program.cs")
	}
	helper := src[hi:]
	if he := strings.Index(helper, "\n}"); he >= 0 {
		helper = helper[:he]
	}
	if !strings.Contains(helper, "await write(CancellationToken.None)") {
		t.Errorf("ControllerWrite must run the write on CancellationToken.None:\n%s", helper)
	}
	if strings.Contains(helper, "write(caller)") {
		t.Errorf("ControllerWrite hands the write the caller's token")
	}

	writes := []struct {
		method, path string
		calls        []string // SDK calls in this route that write the controller
	}{
		{"Post", "/v1/sessions/{id}/download", []string{"ChangeControllerModeAsync", "GoOfflineAsync", "DownloadAsync"}},
		{"Post", "/v1/sessions/{id}/mode", []string{"ChangeControllerModeAsync"}},
		{"Post", "/v1/sessions/{id}/import-rungs", []string{"PartialImportRungsFromXmlFileAsync"}},
		{"Post", "/v1/sessions/{id}/partial-import-with-target", []string{"PartialImportWithTargetFromXmlFileAsync"}},
	}
	for _, w := range writes {
		body := route(t, src, w.method, w.path)
		if !strings.Contains(body, "ControllerWrite(") {
			t.Errorf("%s %s does not run its write through ControllerWrite", w.method, w.path)
		}
		for _, call := range w.calls {
			args := lastArgs(t, body, call)
			if len(args) == 0 {
				t.Errorf("%s %s: no call to %s", w.method, w.path, call)
			}
			for _, a := range args {
				if a != "w" {
					t.Errorf("%s %s: %s gets %q; it must get the write token w from ControllerWrite, never the request's ct", w.method, w.path, call, a)
				}
			}
		}
	}

	// Anywhere in the file, these four only ever write: every call gets w,
	// whatever a new route names its request token.
	for _, call := range []string{"ChangeControllerModeAsync", "DownloadAsync", "PartialImportRungsFromXmlFileAsync", "PartialImportWithTargetFromXmlFileAsync"} {
		for _, a := range lastArgs(t, src, call) {
			if a != "w" {
				t.Errorf("%s is called with %q somewhere in Program.cs; a controller write gets w from ControllerWrite", call, a)
			}
		}
	}
}

// method returns the body of the C# member whose signature contains sig, up
// to its closing brace at the same indentation.
func method(t *testing.T, src, sig string) string {
	t.Helper()
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("%q not found", sig)
	}
	body := src[i:]
	if j := strings.Index(body, "\n    }\n"); j >= 0 {
		body = body[:j]
	}
	return body
}

// LGX-034, the close half: a session close waits for the operation in
// flight. On an emulated controller a DELETE during a download with
// ensureProgramMode disposed the project under it ("Invalid pointer") and
// left the controller stopped in Program mode with the old program.
func TestSessionCloseWaitsForTheWriteInFlight(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "tools", "logixd", "Sessions.cs"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	dispose := method(t, src, "public async ValueTask DisposeAsync()")
	wait := strings.Index(dispose, "await _gate.WaitAsync();")
	closeProject := strings.Index(dispose, "Project.Dispose();")
	if wait < 0 || closeProject < 0 || wait > closeProject {
		t.Errorf("Session.DisposeAsync must take the gate before it disposes the project:\n%s", dispose)
	}
	if strings.Contains(dispose, "_gate.Dispose()") {
		t.Errorf("Session.DisposeAsync disposes the gate; an operation queued behind the close would hit a disposed semaphore")
	}

	run := method(t, src, "public async Task<T> RunAsync<T>(")
	gate, closed := strings.Index(run, "await _gate.WaitAsync(ct);"), strings.Index(run, "if (_closed) throw")
	if gate < 0 || closed < 0 || closed < gate {
		t.Errorf("RunAsync must refuse a closed session, checked once it holds the gate:\n%s", run)
	}

	closeSession := method(t, src, "public async Task<bool> CloseAsync(string id)")
	disposed := strings.Index(closeSession, "await s.DisposeAsync();")
	removed := strings.Index(closeSession, "finally { _sessions.TryRemove(")
	if disposed < 0 || removed < 0 || removed < disposed {
		t.Errorf("CloseAsync must keep the session in the table until its project is closed, and drop it even if the close fails:\n%s", closeSession)
	}

	if !strings.Contains(src, "try { ReapAsync().GetAwaiter().GetResult(); } catch { }") {
		t.Errorf("the reaper's Timer callback must not let an exception end the process")
	}

	reap := method(t, src, "private async Task ReapAsync()")
	if !strings.Contains(reap, "!s.Busy") {
		t.Errorf("the reaper must never take a busy session:\n%s", reap)
	}
}
