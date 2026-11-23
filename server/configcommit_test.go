package server

// POST /api/config/commit and GET /api/config/patch (#711 slice 4) over a
// fake engine: the git work is cmd/naut's and tested there.

import (
	"encoding/json"
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

// A candidate that loads but changes what is fixed while the runtime runs
// (a device added) is saved as a commit for the next restart (#711): the
// files are the candidate's, the commit is the principal's, and the answer
// says it is pending a restart.
func TestConfigSaveRoute(t *testing.T) {
	files := map[string]string{"nautilus.yaml": "devices: [x]\n"}
	f := &fakeIO{valid: false, drivers: []string{"modbus"},
		restart: func(any) (bool, map[string]string) { return true, files },
		save: func(IOSave) (IOCommitted, error) {
			return IOCommitted{Commit: "c0ffee", Branch: "main", Files: []string{"nautilus.yaml"}}, nil
		}}
	srv, toks, _ := patServerWith(t, Options{IO: f.hook(), OnlineEdits: true})
	if rec := call(t, srv, "POST", "/api/io/staged", `{"files":{"modbus.yaml":"x"},"driver":"modbus"}`, bearer(toks["akhan"])); rec.Code != http.StatusCreated {
		t.Fatalf("stage: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, srv, "POST", "/api/io/staged/1/validate", ``, bearer(toks["akhan"])); rec.Code != http.StatusOK {
		t.Fatalf("validate: %d %s", rec.Code, rec.Body)
	}
	rec := call(t, srv, "POST", "/api/config/save", `{"id":"1","message":"add a device"}`, bearer(toks["akhan"]))
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	var got IOSaved
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Commit != "c0ffee" || got.By != "user:akhan" || !got.Pending || got.At.IsZero() {
		t.Fatalf("answer %s (%v), want the commit, who saved it and pending", rec.Body, err)
	}
	if len(f.saves) != 1 || f.saves[0].By != "user:akhan" || f.saves[0].ID != "1" || f.saves[0].Message != "add a device" || f.saves[0].Files["nautilus.yaml"] != "devices: [x]\n" {
		t.Fatalf("engine asked %+v, want the principal, the candidate and its files", f.saves)
	}
	// who saved it stays on the candidate
	if st := call(t, srv, "GET", "/api/io/staged/1", ``, bearer(toks["akhan"])); !strings.Contains(st.Body.String(), `"saved"`) || !strings.Contains(st.Body.String(), `"user:akhan"`) {
		t.Fatalf("the staged candidate does not say who saved it: %s", st.Body)
	}
	if es := auditByAction(srv, "config.save"); len(es) != 1 || es[0].User != "user:akhan" || es[0].Target != "1" {
		t.Fatalf("journal %+v, want one config.save by user:akhan", es)
	}
	// an operator holds no config permission
	if rec := call(t, srv, "POST", "/api/config/save", `{"id":"1"}`, bearer(toks["jsmith"])); rec.Code != http.StatusForbidden {
		t.Fatalf("operator: %d, want 403", rec.Code)
	}
	if len(f.saves) != 1 {
		t.Fatal("a refused request reached the engine")
	}
}

// Only a validated candidate that a restart would apply is saved this way.
func TestConfigSaveRouteRefuses(t *testing.T) {
	boom := errors.New("disk on fire")
	needs := func(f *fakeIO) {
		f.restart = func(any) (bool, map[string]string) { return true, map[string]string{"nautilus.yaml": "x"} }
	}
	for _, c := range []struct {
		name    string
		hook    func(*fakeIO)
		prep    func(*testing.T, *Server)
		body    string
		code    int
		want    string
		engaged bool
	}{
		{"no worktree", func(f *fakeIO) { needs(f); f.save = nil }, ioStaged(true), `{"id":"1"}`, 409, "cannot commit", false},
		{"a valid candidate is applied, not saved", needs, ioStaged(true), `{"id":"1"}`, 409, "is valid", false},
		{"not validated", needs, ioStaged(false), `{"id":"1"}`, 409, "is pending", false},
		{"invalid for another reason", func(f *fakeIO) {
			f.valid = false
			f.restart = func(any) (bool, map[string]string) { return false, nil }
		}, ioStaged(true), `{"id":"1"}`, 409, "a restart would not fix", false},
		{"unknown candidate", needs, nil, `{"id":"9"}`, 404, "no staged", false},
		{"no id", needs, ioStaged(true), `{}`, 400, "body must be", false},
		{"multi-line message", needs, ioStaged(true), `{"id":"1","message":"a\nb"}`, 400, "one line", false},
		{"the engine refuses", func(f *fakeIO) {
			needs(f)
			f.save = func(IOSave) (IOCommitted, error) {
				return IOCommitted{}, errors.Join(ErrIOCommitRefused, errors.New("commit or stash them first"))
			}
		}, ioStaged(true), `{"id":"1"}`, 409, "commit or stash them first", true},
		{"the engine fails", func(f *fakeIO) {
			needs(f)
			f.save = func(IOSave) (IOCommitted, error) { return IOCommitted{}, boom }
		}, ioStaged(true), `{"id":"1"}`, 500, "disk on fire", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeIO{valid: false, drivers: []string{"modbus"}, save: func(IOSave) (IOCommitted, error) { return IOCommitted{Commit: "c0ffee"}, nil }}
			if c.name == "a valid candidate is applied, not saved" {
				f.valid = true
			}
			if c.hook != nil {
				c.hook(f)
			}
			srv := commitServer(t, f)
			if c.prep != nil {
				c.prep(t, srv)
			}
			rec := call(t, srv, "POST", "/api/config/save", c.body)
			if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) {
				t.Fatalf("%d %s, want %d containing %q", rec.Code, rec.Body, c.code, c.want)
			}
			if (len(f.saves) > 0) != c.engaged {
				t.Fatalf("engine asked %d times, engaged want %v", len(f.saves), c.engaged)
			}
		})
	}
}

// A validate that lands between the check and the commit means what was
// checked is not what would be saved: refuse, and ask for another validate.
func TestConfigSaveRefusesACandidateThatMovedWhileSaving(t *testing.T) {
	var srv *Server
	f := &fakeIO{valid: false, drivers: []string{"modbus"}}
	f.restart = func(any) (bool, map[string]string) {
		// the candidate is validated again while the save looks at it
		call(t, srv, "POST", "/api/io/staged/1/validate", ``)
		return true, map[string]string{"nautilus.yaml": "x"}
	}
	f.save = func(IOSave) (IOCommitted, error) { return IOCommitted{Commit: "c0ffee"}, nil }
	srv = commitServer(t, f)
	ioStaged(true)(t, srv)
	rec := call(t, srv, "POST", "/api/config/save", `{"id":"1"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "changed while it was being saved") {
		t.Fatalf("%d %s, want a 409 naming the move", rec.Code, rec.Body)
	}
	if len(f.saves) != 0 {
		t.Fatal("a moved candidate was committed")
	}
}
