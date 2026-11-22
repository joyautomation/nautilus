package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/runtime"
)

func importRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM P\nVAR_EXTERNAL SP : REAL; Fan : REAL; On : BOOL; N : INT; END_VAR\nEND_PROGRAM",
		Tags: []runtime.TagDef{
			runtime.Setpoint("SP", 65.0, runtime.Range(fptr(10), fptr(90))),
			runtime.Setpoint("Fan", 20.0, runtime.Range(nil, fptr(50))),
			runtime.Setpoint("On", true),
			runtime.Setpoint("N", int64(5)),
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

// A set the store would convert-refuse (an INT's width, a fraction to an
// INT) is refused WHOLE, by the real run and the dry run alike: the check and
// the write are one step, so nothing is half in.
func TestParameterImportConversionRefusalWritesNothing(t *testing.T) {
	for _, body := range []string{
		`{"version":1,"parameters":{"SP":70,"N":70000}}`,
		`{"version":1,"parameters":{"SP":70,"N":7.5}}`,
		`{"version":1,"dryRun":true,"parameters":{"SP":70,"N":70000}}`,
	} {
		rt := importRuntime(t)
		srv := New(rt)
		code, a, resp := importCall(t, srv, body)
		if code != http.StatusUnprocessableEntity || a.Applied {
			t.Fatalf("%s: %d %s", body, code, resp)
		}
		if liveParam(t, srv, "SP") != 65.0 || fmt.Sprint(liveParam(t, srv, "N")) != "5" {
			t.Fatalf("%s: a value was written (SP=%v N=%v)", body, liveParam(t, srv, "SP"), liveParam(t, srv, "N"))
		}
	}
}

// The answer's from/to and the journal are what the store held and holds:
// an unchanged value is not written and takes no attribution, and the entry's
// after is only what landed.
func TestParameterImportReportsWhatLanded(t *testing.T) {
	srv := auditServerRT(t, importRuntime(t), Options{})
	rec := call(t, srv, "POST", "/api/parameters/import", `{"version":1,"parameters":{"SP":70,"Fan":20,"N":6}}`)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var a ParamImportAnswer
	json.Unmarshal(rec.Body.Bytes(), &a)
	st := map[string]ParamImportResult{}
	for _, r := range a.Results {
		st[r.Name] = r
	}
	if st["SP"].Status != "applied" || st["SP"].From != 65.0 || st["SP"].To != 70.0 || st["Fan"].Status != "unchanged" || st["N"].To != 6.0 {
		t.Fatalf("results %+v", st)
	}
	for _, p := range srv.rt.Parameters() {
		if p.Name == "Fan" && p.EditedBy != "" {
			t.Errorf("an unchanged value took attribution: %+v", p)
		}
	}
	e := srv.audit.ring.Recent()[0]
	after, _ := e.After.(map[string]any)
	if e.Result != "ok" || len(after) != 2 || after["SP"] != 70.0 || after["N"] != 6.0 {
		t.Fatalf("journal after = %#v (%s)", e.After, e.Result)
	}
	// A dry run's entry keeps the set that was asked for.
	call(t, srv, "POST", "/api/parameters/import", `{"version":1,"dryRun":true,"parameters":{"SP":71}}`)
	if d, _ := srv.audit.ring.Recent()[0].After.(map[string]any); d["SP"] != 71.0 {
		t.Fatalf("dry run after = %#v", srv.audit.ring.Recent()[0].After)
	}
}

// A server that mirrors a controller it does not run holds no setpoint of its
// own: import is refused (501), as a force is.
func TestParameterImportRefusedOnAMirror(t *testing.T) {
	srv := New(importRuntime(t), Options{TagWriter: func(string, any) error { return nil }})
	if rec := call(t, srv, "POST", "/api/parameters/import", `{"version":1,"parameters":{"SP":70}}`); rec.Code != http.StatusNotImplemented || liveParam(t, srv, "SP") != 65.0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// A set over the cap is refused before anything is read.
func TestParameterImportSizeCap(t *testing.T) {
	srv := New(importRuntime(t))
	var sb strings.Builder
	sb.WriteString(`{"version":1,"parameters":{"SP":70`)
	for i := 0; i < paramImportMax; i++ {
		sb.WriteString(`,"x` + strconv.Itoa(i) + `":1`)
	}
	sb.WriteString(`}}`)
	if code, _, body := importCall(t, srv, sb.String()); code != http.StatusBadRequest || liveParam(t, srv, "SP") != 65.0 {
		t.Fatalf("%d %s", code, body)
	}
}

// A set where nothing differs writes nothing and does not claim it applied.
func TestParameterImportNothingToDo(t *testing.T) {
	srv := New(importRuntime(t))
	code, a, body := importCall(t, srv, `{"version":1,"parameters":{"SP":65,"Fan":20}}`)
	if code != 200 || a.Applied {
		t.Fatalf("%d %s", code, body)
	}
}
