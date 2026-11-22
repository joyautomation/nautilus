package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/runtime"
)

func importRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM P\nVAR_EXTERNAL SP : REAL; Fan : REAL; On : BOOL; END_VAR\nEND_PROGRAM",
		Tags: []runtime.TagDef{
			runtime.Setpoint("SP", 65.0, runtime.Range(fptr(10), fptr(90))),
			runtime.Setpoint("Fan", 20.0, runtime.Range(nil, fptr(50))),
			runtime.Setpoint("On", true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	return rt
}

func paramImportSrv(t *testing.T) *Server { return auditServerRT(t, importRuntime(t), Options{}) }

func liveParam(t *testing.T, srv *Server, name string) any {
	t.Helper()
	for _, p := range srv.rt.Parameters() {
		if p.Name == name {
			return p.Value
		}
	}
	t.Fatalf("no parameter %s", name)
	return nil
}

func importCall(t *testing.T, srv *Server, body string) (int, ParamImportAnswer, string) {
	t.Helper()
	rec := call(t, srv, "POST", "/api/parameters/import", body)
	var a ParamImportAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	return rec.Code, a, rec.Body.String()
}

func TestParameterImportApplies(t *testing.T) {
	srv := New(importRuntime(t))
	code, a, body := importCall(t, srv, `{"version":1,"parameters":{"sp":70,"Fan":20,"On":false}}`)
	if code != http.StatusOK || !a.Applied {
		t.Fatalf("%d %s", code, body)
	}
	got := map[string]string{}
	for _, r := range a.Results {
		got[r.Name] = r.Status
	}
	if got["SP"] != "applied" || got["Fan"] != "unchanged" || got["On"] != "applied" {
		t.Fatalf("results %v", got)
	}
	if liveParam(t, srv, "SP") != 70.0 || liveParam(t, srv, "On") != false {
		t.Fatal("values not written")
	}
	// The write is attributed to the importer, so the write-back commits it as theirs.
	for _, p := range srv.rt.Parameters() {
		if p.Name == "SP" && p.EditedBy != "anonymous" {
			t.Errorf("SP editedBy = %q", p.EditedBy)
		}
	}
}

// One refusal stops the set: nothing is written, and there is no way past
// min/max, a force, a kind, an unknown name or a non-parameter.
func TestParameterImportIsAllOrNothing(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"max":     {`{"version":1,"parameters":{"SP":70,"Fan":51}}`, "50"},
		"min":     {`{"version":1,"parameters":{"SP":70,"SP2":1,"Fan":5}}`, "not a parameter"},
		"below":   {`{"version":1,"parameters":{"SP":9}}`, "[10, 90]"},
		"kind":    {`{"version":1,"parameters":{"SP":70,"On":1}}`, "holds a bool"},
		"unknown": {`{"version":1,"parameters":{"SP":70,"Nope":1}}`, "not a parameter"},
		"input":   {`{"version":1,"parameters":{"SP":70,"Level":1}}`, "not a parameter"},
		"dup":     {`{"version":1,"parameters":{"SP":70,"sp":71}}`, "same parameter"},
		"nested":  {`{"version":1,"parameters":{"SP":[1]}}`, "number, a bool or text"},
	} {
		srv := New(importRuntime(t))
		code, a, body := importCall(t, srv, tc.body)
		if code != http.StatusUnprocessableEntity || a.Applied || !strings.Contains(body, tc.want) {
			t.Errorf("%s: %d %s", name, code, body)
		}
		if liveParam(t, srv, "SP") != 65.0 {
			t.Errorf("%s: a value was written despite the refusal", name)
		}
	}
	// A forced parameter refuses the set.
	srv := New(importRuntime(t))
	if err := srv.rt.Tags().Force("Fan", 33.0); err != nil {
		t.Fatal(err)
	}
	code, _, body := importCall(t, srv, `{"version":1,"parameters":{"SP":70,"Fan":30}}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "forced") || liveParam(t, srv, "SP") != 65.0 {
		t.Errorf("forced: %d %s", code, body)
	}
}

func TestParameterImportBodyRules(t *testing.T) {
	srv := New(importRuntime(t))
	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"not json":   {`nope`, 400},
		"no version": {`{"parameters":{"SP":70}}`, 400},
		"empty":      {`{"version":1,"parameters":{}}`, 400},
	} {
		if code, _, body := importCall(t, srv, tc.body); code != tc.code {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	// A dry run reports and writes nothing.
	code, a, _ := importCall(t, srv, `{"version":1,"dryRun":true,"parameters":{"SP":70}}`)
	if code != 200 || a.Applied || !a.DryRun || a.Results[0].Status != "change" || liveParam(t, srv, "SP") != 65.0 {
		t.Fatalf("dry run: %d %+v", code, a)
	}
}

// A controller that is not leading does not import; a request to a standby
// goes to the leader, which does.
func TestParameterImportLeaderOnly(t *testing.T) {
	srv := New(importRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{Mode: "cluster", Pod: "a", Leader: "b", IsLeader: false}}})
	// Called straight at the handler (not through the standby's proxy).
	rec := call(t, srv, "POST", "/api/parameters/import", `{"version":1,"parameters":{"SP":70}}`)
	if rec.Code == http.StatusOK || liveParam(t, srv, "SP") != 65.0 {
		t.Fatalf("a non-leader imported: %d %s", rec.Code, rec.Body)
	}
	lead := New(importRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{Mode: "cluster", Pod: "a", Leader: "a", IsLeader: true}}})
	if code, _, body := importCall(t, lead, `{"version":1,"parameters":{"SP":70}}`); code != 200 {
		t.Fatalf("leader: %d %s", code, body)
	}
}

// A principal with config but not write on a tag cannot import it: the whole
// set is refused with a 403 naming the tags, and nothing is written. An
// area-fenced administrator is refused the tags outside its area the same way.
func TestParameterImportNeedsWriteOnEveryTag(t *testing.T) {
	for _, c := range []struct {
		tok, who string
		denied   bool
	}{
		{"only-" + PermConfig, "config only", true},
		{"pair-" + PermConfig, "config and write", false},
	} {
		srv := New(importRuntime(t))
		enforceExactly(t, srv)
		rec := call(t, srv, "POST", "/api/parameters/import", `{"version":1,"parameters":{"SP":70,"Fan":30}}`, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+c.tok)
		})
		if c.denied {
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Fan, SP") || !strings.Contains(rec.Body.String(), "Nothing was written") {
				t.Errorf("%s: %d %s", c.who, rec.Code, rec.Body)
			}
			if liveParam(t, srv, "SP") != 65.0 || liveParam(t, srv, "Fan") != 20.0 {
				t.Errorf("%s: a value was written", c.who)
			}
		} else if rec.Code != http.StatusOK || liveParam(t, srv, "SP") != 70.0 {
			t.Errorf("%s: %d %s", c.who, rec.Code, rec.Body)
		}
	}
	// Area-scoped: write only over SP's area; a set that also names Fan is refused whole.
	srv := New(importRuntime(t))
	p, err := auth.Compile(auth.Settings{
		Areas:         map[string][]string{"sp": {"SP"}},
		ServiceTokens: map[string][]auth.Grant{"sp": {{Role: auth.Administrator, Areas: []string{"sp"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.policy, srv.services = p, map[string]string{"sp": "sp-secret"}
	asSP := func(r *http.Request) { r.Header.Set("Authorization", "Bearer sp-secret") }
	rec := call(t, srv, "POST", "/api/parameters/import", `{"version":1,"parameters":{"SP":70,"Fan":30}}`, asSP)
	if rec.Code == http.StatusOK || liveParam(t, srv, "SP") != 65.0 {
		t.Fatalf("area-fenced import went through: %d %s", rec.Code, rec.Body)
	}
}

// If a write fails after the set validated, the answer and the journal entry
// say exactly which values are in and which are not.
func TestParameterImportPartialWriteIsReported(t *testing.T) {
	srv := auditServerRT(t, importRuntime(t), Options{})
	old := paramWrite
	defer func() { paramWrite = old }()
	paramWrite = func(t *runtime.Tags, ops []runtime.WriteOp, by string) []error {
		errs := t.WriteAs(ops[:1], by) // the first op lands
		for range ops[1:] {
			errs = append(errs, errors.New("driver said no"))
		}
		return errs
	}
	rec := call(t, srv, "POST", "/api/parameters/import", `{"version":1,"parameters":{"Fan":30,"SP":70}}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got struct {
		Answer ParamImportAnswer `json:"answer"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	st := map[string]string{}
	for _, r := range got.Answer.Results {
		st[r.Name] = r.Status
	}
	if st["Fan"] != "applied" || st["SP"] != "failed" || got.Answer.Applied {
		t.Fatalf("results %v: %s", st, rec.Body)
	}
	if liveParam(t, srv, "Fan") != 30.0 || liveParam(t, srv, "SP") != 65.0 {
		t.Fatal("what the answer says is not what the controller holds")
	}
	es := srv.audit.ring.Recent()
	e := es[0]
	w, _ := e.Detail["written"].(map[string]any)
	nw, _ := e.Detail["notWritten"].(map[string]string)
	if e.Action != "param.import" || w["Fan"] != 30.0 || nw["SP"] != "driver said no" || len(w) != 1 || len(nw) != 1 {
		t.Fatalf("journal entry: %+v", e)
	}
}
