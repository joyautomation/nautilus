package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/audit"
)

// stageValidateApply drives one candidate through the three routes with
// mod on every request and returns the staged id.
func stageValidateApply(t *testing.T, srv *Server, mod ...func(*http.Request)) string {
	t.Helper()
	rec := call(t, srv, "POST", "/api/io/staged", `{"files":{"modbus.yaml":"x"},"driver":"modbus","by":"user:root","principal":"user:root"}`, mod...)
	if rec.Code != http.StatusCreated {
		t.Fatalf("stage: %d %s", rec.Code, rec.Body)
	}
	var id string
	if _, err := fmt.Sscanf(strings.SplitN(rec.Body.String(), `"id":"`, 2)[1], "%s", &id); err != nil {
		t.Fatal(err)
	}
	id = strings.SplitN(id, `"`, 2)[0]
	for _, step := range []string{"validate", "apply"} {
		if rec := call(t, srv, "POST", "/api/io/staged/"+id+"/"+step, `{"by":"user:root"}`, mod...); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", step, rec.Code, rec.Body)
		}
	}
	return id
}

func auditByAction(srv *Server, action string) []audit.Entry {
	var out []audit.Entry
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

// A signed-in user's principal reaches the engine (the candidate's by,
// the apply's by, the revert cancel's by) and the journal, whatever name
// the client claims.
func TestIOWritesCarryThePrincipal(t *testing.T) {
	f := &fakeIO{valid: true, drivers: []string{"modbus"}}
	srv, toks, _ := patServerWith(t, Options{IO: f.hook()})
	f.watch = &IOWatch{State: "watching", Revert: true}
	id := stageValidateApply(t, srv, bearer(toks["akhan"]))
	if f.stageBy != "user:akhan" || f.applyBy != "user:akhan" || f.applyID != id {
		t.Fatalf("engine told stage by %q, apply by %q id %q; want user:akhan and %s", f.stageBy, f.applyBy, f.applyID, id)
	}
	if rec := call(t, srv, "POST", "/api/io/revert/cancel", "", bearer(toks["akhan"])); rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	if f.cancelBy != "user:akhan" {
		t.Errorf("cancel by %q, want user:akhan", f.cancelBy)
	}
	for _, a := range []string{"config.stage", "config.validate", "config.apply", "config.revert.cancel"} {
		es := auditByAction(srv, a)
		if len(es) != 1 || es[0].User != "user:akhan" {
			t.Errorf("%s entries %+v, want one by user:akhan", a, es)
		}
	}
}

// With no credentials and no auth: block the principal is anonymous; a
// service token is its own, never anonymous.
func TestIOWritesAnonymousAndServiceToken(t *testing.T) {
	f := &fakeIO{valid: true, drivers: []string{"modbus"}}
	srv := New(newTestRuntime(t), ioOptions(f, true))
	stageValidateApply(t, srv)
	if f.applyBy != "anonymous" {
		t.Errorf("no credentials: apply by %q, want anonymous", f.applyBy)
	}
	f2 := &fakeIO{valid: true, drivers: []string{"modbus"}}
	o := ioOptions(f2, true)
	o.AuthToken = "s3cret"
	srv2 := New(newTestRuntime(t), o)
	stageValidateApply(t, srv2, bearer("s3cret"))
	if f2.applyBy != "token" {
		t.Errorf("bare service token: apply by %q, want token", f2.applyBy)
	}
	// A named service token is token:<name>.
	f3 := &fakeIO{valid: true, drivers: []string{"modbus"}}
	o3 := ioOptions(f3, true)
	o3.ServiceTokens = map[string]string{"edge": "edge-secret"}
	srv3 := New(newTestRuntime(t), o3)
	stageValidateApply(t, srv3, bearer("edge-secret"))
	if f3.applyBy != "token:edge" {
		t.Errorf("named service token: apply by %q, want token:edge", f3.applyBy)
	}
	if es := auditByAction(srv2, "config.apply"); len(es) != 1 || es[0].User != f2.applyBy {
		t.Errorf("journal %+v, want user %q", es, f2.applyBy)
	}
}

// An automatic revert-if-silent is the system's, never the applier's
// name; the apply it undid and who applied it are in the detail.
func TestIOAutoRevertIsJournalledAsTheSystem(t *testing.T) {
	f := &fakeIO{valid: true, drivers: []string{"modbus"}}
	srv, toks, _ := patServerWith(t, Options{IO: f.hook()})
	id := stageValidateApply(t, srv, bearer(toks["akhan"]))
	if f.onRevert == nil {
		t.Fatal("the server never registered for reverts")
	}
	f.onRevert(IOWatch{ID: id, By: f.applyBy, From: "upload", State: "reverted", Reason: "DEV_A did not read"})
	es := auditByAction(srv, "config.revert")
	if len(es) != 1 {
		t.Fatalf("entries %+v", es)
	}
	e := es[0]
	if e.User != "system" || e.Source != audit.SourceSystem || e.Result != audit.ResultOK ||
		e.Detail["note"] != "revert of "+id+" applied by user:akhan" || e.Detail["by"] != "user:akhan" || e.Target != id {
		t.Errorf("entry %+v", e)
	}
	f.onRevert(IOWatch{ID: id, By: "user:akhan", State: "dropped", Reason: "not carried"})
	if es := auditByAction(srv, "config.revert"); len(es) != 2 || es[0].User != "system" || es[0].Result != audit.ResultOK ||
		!strings.Contains(fmt.Sprint(es[0].Detail["note"]), "dropped at a restart or takeover") {
		t.Errorf("a dropped apply is journalled %+v, want system ok with the note", es)
	}
	f.onRevert(IOWatch{ID: id, By: "user:akhan", State: "revert-failed"})
	if es := auditByAction(srv, "config.revert"); es[0].Result != audit.ResultError {
		t.Errorf("a failed revert is journalled %q, want error", es[0].Result)
	}
}

// The apply's entry says what changed: from, and each unit's before and
// after hash, so an auditor can match it to the files.
func TestIOApplyEntryCarriesTheChange(t *testing.T) {
	f := &fakeIO{valid: true, drivers: []string{"modbus"}, applyBody: map[string]any{
		"changes": []any{map[string]any{"driver": "modbus", "device": "DEV_A", "before": "aaaa", "after": "bbbb"}},
		"kept":    true,
	}}
	srv := New(newTestRuntime(t), ioOptions(f, true))
	stageValidateApply(t, srv)
	es := auditByAction(srv, "config.apply")
	if len(es) != 1 {
		t.Fatalf("entries %+v", es)
	}
	after := fmt.Sprint(es[0].After)
	for _, want := range []string{"DEV_A", "aaaa", "bbbb", "applied", "upload"} {
		if !strings.Contains(after, want) {
			t.Errorf("after %s lacks %q", after, want)
		}
	}
}

// A failed apply's entry says where the candidate came from and the changes
// validation computed; with none, it has no changes key.
func TestIOFailedApplyEntryCarriesFromAndChanges(t *testing.T) {
	for _, withChanges := range []bool{true, false} {
		f := &fakeIO{valid: true, drivers: []string{"modbus"}, applyErr: fmt.Errorf("boom")}
		if withChanges {
			f.changes = []any{map[string]any{"driver": "modbus", "device": "DEV_A", "before": "aaaa", "after": "bbbb"}}
		}
		srv := New(newTestRuntime(t), ioOptions(f, true))
		call(t, srv, "POST", "/api/io/staged", `{"files":{"modbus.yaml":"x"},"driver":"modbus"}`)
		call(t, srv, "POST", "/api/io/staged/1/validate", "")
		if rec := call(t, srv, "POST", "/api/io/staged/1/apply", ""); rec.Code == http.StatusOK {
			t.Fatalf("apply succeeded: %s", rec.Body)
		}
		es := auditByAction(srv, "config.apply")
		if len(es) != 1 || es[0].Result == audit.ResultOK || es[0].Detail["from"] != "upload" {
			t.Fatalf("entries %+v, want one failed entry from upload", es)
		}
		ch, has := es[0].Detail["changes"]
		if withChanges != has || (has && !strings.Contains(fmt.Sprint(ch), "bbbb")) {
			t.Errorf("withChanges=%v: detail changes %v (present %v)", withChanges, ch, has)
		}
	}
}
