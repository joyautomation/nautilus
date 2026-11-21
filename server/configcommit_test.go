package server

// POST /api/config/commit and GET /api/config/patch (#711 slice 4) over a
// fake engine: the git work is cmd/naut's and tested there.

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func commitServer(t *testing.T, f *fakeIO) *Server {
	t.Helper()
	return New(newTestRuntime(t), ioOptions(f, true))
}

func okCommit(IOCommit) (IOCommitted, error) {
	return IOCommitted{Commit: "c0ffee", Branch: "main", Files: []string{"modbus.yaml"}}, nil
}

func TestConfigCommitRoute(t *testing.T) {
	f := &fakeIO{valid: true, drivers: []string{"modbus"}, commit: okCommit}
	srv := commitServer(t, f)
	ioStagedApplied(t, srv)
	rec := call(t, srv, "POST", "/api/config/commit", `{"id":"1","message":"raise setpoint"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"commit":"c0ffee"`) {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body)
	}
	if len(f.commits) != 1 || f.commits[0] != (IOCommit{By: "anonymous", ID: "1", Message: "raise setpoint"}) {
		t.Fatalf("engine asked %+v", f.commits)
	}
}

func TestConfigCommitRouteRefuses(t *testing.T) {
	boom := errors.New("disk on fire")
	for _, c := range []struct {
		name    string
		hook    func(*fakeIO)
		prep    func(*testing.T, *Server)
		body    string
		code    int
		want    string
		engaged bool // the engine must have been asked
	}{
		{"no worktree", func(f *fakeIO) { f.commit = nil }, ioStagedApplied, `{"id":"1"}`, 409, "/api/config/patch", false},
		{"not applied", nil, ioStaged(true), `{"id":"1"}`, 409, "only an applied candidate", false},
		{"unknown candidate", nil, nil, `{"id":"9"}`, 404, "no staged", false},
		{"no id", nil, ioStagedApplied, `{}`, 400, "body must be", false},
		{"not json", nil, ioStagedApplied, `nope`, 400, "body must be", false},
		{"multi-line message", nil, ioStagedApplied, `{"id":"1","message":"a\nb"}`, 400, "one line", false},
		{"long message", nil, ioStagedApplied, `{"id":"1","message":"` + strings.Repeat("x", 201) + `"}`, 400, "one line", false},
		{"the engine refuses", func(f *fakeIO) {
			f.commit = func(IOCommit) (IOCommitted, error) {
				return IOCommitted{}, errors.Join(ErrIOCommitRefused, errors.New("commit or stash them first"))
			}
		}, ioStagedApplied, `{"id":"1"}`, 409, "commit or stash them first", true},
		{"the engine fails", func(f *fakeIO) {
			f.commit = func(IOCommit) (IOCommitted, error) { return IOCommitted{}, boom }
		}, ioStagedApplied, `{"id":"1"}`, 500, "disk on fire", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeIO{valid: true, drivers: []string{"modbus"}, commit: okCommit}
			if c.hook != nil {
				c.hook(f)
			}
			srv := commitServer(t, f)
			if c.prep != nil {
				c.prep(t, srv)
			}
			rec := call(t, srv, "POST", "/api/config/commit", c.body)
			if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) {
				t.Fatalf("%d %s, want %d containing %q", rec.Code, rec.Body, c.code, c.want)
			}
			if (len(f.commits) > 0) != c.engaged {
				t.Fatalf("engine asked %d times, engaged want %v", len(f.commits), c.engaged)
			}
		})
	}
}

// A commit is in the principal's name, and elevated: the engine is told who
// asked, whatever author the client claims.
func TestConfigCommitCarriesThePrincipal(t *testing.T) {
	f := &fakeIO{valid: true, drivers: []string{"modbus"}, commit: okCommit}
	srv, toks, _ := patServerWith(t, Options{IO: f.hook(), OnlineEdits: true})
	id := stageValidateApply(t, srv, bearer(toks["akhan"]))
	if rec := call(t, srv, "POST", "/api/config/commit", `{"id":"`+id+`","by":"user:root"}`, bearer(toks["akhan"])); rec.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body)
	}
	if len(f.commits) != 1 || f.commits[0].By != "user:akhan" || f.commits[0].ID != id {
		t.Fatalf("engine asked %+v, want by user:akhan id %s", f.commits, id)
	}
	es := auditByAction(srv, "config.commit")
	if len(es) != 1 || es[0].User != "user:akhan" || es[0].Target != id {
		t.Fatalf("journal %+v, want one entry by user:akhan", es)
	}
	// An operator holds no config permission.
	if rec := call(t, srv, "POST", "/api/config/commit", `{"id":"`+id+`"}`, bearer(toks["jsmith"])); rec.Code != http.StatusForbidden {
		t.Fatalf("operator: %d %s, want 403", rec.Code, rec.Body)
	}
	if len(f.commits) != 1 {
		t.Fatal("a refused request reached the engine")
	}
}

func TestConfigPatchRoute(t *testing.T) {
	f := &fakeIO{}
	srv := commitServer(t, f)
	f.patch = func() (string, error) { return "--- a/x\n+++ b/x\n", nil }
	srv = commitServer(t, f)
	rec := call(t, srv, "GET", "/api/config/patch", "")
	if rec.Code != 200 || rec.Body.String() != "--- a/x\n+++ b/x\n" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/x-diff") {
		t.Fatalf("%d %q %q", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}
	f.patch = func() (string, error) { return "", nil }
	if rec := call(t, commitServer(t, f), "GET", "/api/config/patch", ""); rec.Code != 409 || !strings.Contains(rec.Body.String(), "nothing to patch") {
		t.Fatalf("empty: %d %s", rec.Code, rec.Body)
	}
	f.patch = func() (string, error) { return "", errors.Join(ErrIOCommitRefused, errors.New("ask the leader")) }
	if rec := call(t, commitServer(t, f), "GET", "/api/config/patch", ""); rec.Code != 409 || !strings.Contains(rec.Body.String(), "ask the leader") {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body)
	}
	f.patch = nil
	if rec := call(t, commitServer(t, f), "GET", "/api/config/patch", ""); rec.Code != 503 {
		t.Fatalf("no engine support: %d", rec.Code)
	}
}

// A commit saves what runs: only the candidate whose apply is in force. A
// later apply replaces it; a revert leaves none.
func TestConfigCommitOnlyWhatRuns(t *testing.T) {
	f := &fakeIO{valid: true, drivers: []string{"modbus"}, commit: okCommit}
	srv := commitServer(t, f)
	first := stageValidateApply(t, srv)
	second := stageValidateApply(t, srv)
	rec := call(t, srv, "POST", "/api/config/commit", `{"id":"`+first+`"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no longer what runs") || len(f.commits) != 0 {
		t.Fatalf("older apply: %d %s (engine asked %d)", rec.Code, rec.Body, len(f.commits))
	}
	if rec := call(t, srv, "POST", "/api/config/commit", `{"id":"`+second+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("latest apply: %d %s", rec.Code, rec.Body)
	}
	// An auto-revert puts the configuration before it back: nothing to commit.
	f.onRevert(IOWatch{ID: second, State: "reverted", By: "anonymous"})
	rec = call(t, srv, "POST", "/api/config/commit", `{"id":"`+second+`"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no longer what runs") || len(f.commits) != 1 {
		t.Fatalf("after a revert: %d %s (engine asked %d)", rec.Code, rec.Body, len(f.commits))
	}
}
