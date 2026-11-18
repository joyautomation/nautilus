package facade

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/webapi"
	"github.com/joyautomation/nautilus/siemens/webapi/webapitest"
)

// cpu is a fake S7-1500 holding what siemens/writer generates for a small
// project: the tags DB, and the instance DB of program Main with a timer.
func cpu(t *testing.T) (*webapitest.Server, *webapi.Client) {
	t.Helper()
	s := webapitest.New()
	s.Users["naut"] = webapitest.User{Password: "pw", Permissions: []string{"read_value", "write_value"}}
	s.Vars[`"Tags"`] = &webapitest.Var{Datatype: "struct"}
	s.Vars[`"Tags".Level`] = &webapitest.Var{Datatype: "real", Value: 12.5}
	s.Vars[`"Tags".Run`] = &webapitest.Var{Datatype: "bool", Value: false}
	s.Vars[`"Tags".Speed`] = &webapitest.Var{Datatype: "int", Value: 3}
	s.Vars[`"Tags".Total`] = &webapitest.Var{Datatype: "dint", Value: 7, ReadOnly: true}
	s.Vars[`"Main_DB"`] = &webapitest.Var{Datatype: "struct"}
	s.Put(`"Main_DB".count`, &webapitest.Var{Datatype: "int", Value: 4})
	s.Vars[`"Main_DB".dwell`] = &webapitest.Var{Datatype: "struct"}
	s.Vars[`"Main_DB".dwell.Q`] = &webapitest.Var{Datatype: "bool", Value: true}
	s.Vars[`"Main_DB".dwell.ET`] = &webapitest.Var{Datatype: "time", Value: 1500}
	url, pin := s.Start(t)
	c, err := webapi.New(url, webapi.Options{User: "naut", Password: "pw", CertSHA256: pin})
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

var roots = []Root{{DB: "Tags"}, {DB: "Main_DB", Prefix: "Main"}}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// start runs a facade over the fake CPU and serves its API.
func start(t *testing.T) (*webapitest.Server, *Facade, string) {
	t.Helper()
	s, c := cpu(t)
	f, err := New(context.Background(), Options{Client: c, Host: "cpu", Roots: roots, Poll: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	api := httptest.NewServer(f.Handler())
	t.Cleanup(func() { api.Close(); cancel(); <-done })
	return s, f, api.URL
}

func state(t *testing.T, base string) map[string]any {
	t.Helper()
	res, err := http.Get(base + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var st struct {
		Tags map[string]any `json:"tags"`
	}
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st.Tags
}

func post(t *testing.T, base, path, body string) (int, string) {
	t.Helper()
	res, err := http.Post(base+path, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// TestDiscoverNamesLikeTheWriter: the bound names are the ones
// siemens/writer's Output.Symbol maps the other way, so the overlay on the
// nautilus source resolves. What cannot be bound is listed with why.
func TestDiscoverNamesLikeTheWriter(t *testing.T) {
	_, c := cpu(t)
	bs, skipped, err := Discover(context.Background(), c, roots)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, b := range bs {
		got[b.Name] = b.Symbol + " " + b.Type
	}
	want := map[string]string{
		"Level":        `"Tags".Level REAL`,
		"Run":          `"Tags".Run BOOL`,
		"Speed":        `"Tags".Speed INT`,
		"Total":        `"Tags".Total DINT`,
		"Main.count":   `"Main_DB".count INT`,
		"Main.dwell.Q": `"Main_DB".dwell.Q BOOL`,
	}
	if len(got) != len(want) {
		t.Errorf("bound %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	if len(skipped) != 1 || !strings.HasPrefix(skipped[0], "Main.dwell.ET: type time") {
		t.Errorf("skipped %v", skipped)
	}
}

func TestMissingRootIsAnError(t *testing.T) {
	_, c := cpu(t)
	if _, err := New(context.Background(), Options{Client: c, Roots: []Root{{DB: "Nope"}}}); err == nil {
		t.Fatal("a root the CPU does not have must be an error")
	}
}

// TestLiveValuesFollowTheCPU: values reach /api/state with the tag store's
// types, and follow the CPU when it changes them.
func TestLiveValuesFollowTheCPU(t *testing.T) {
	s, _, base := start(t)
	waitFor(t, "first values", func() bool { return state(t, base)["Main.count"] != nil })
	st := state(t, base)
	if st["Level"] != 12.5 || st["Run"] != false || st["Main.count"] != float64(4) || st["Main.dwell.Q"] != true {
		t.Fatalf("state %v", st)
	}
	s.Set(`"Main_DB".count`, 5) // the CPU's program counts on
	waitFor(t, "the new count", func() bool { return state(t, base)["Main.count"] == float64(5) })
}

// TestWriteGoesToTheCPU: a write lands on the CPU, and the poll brings it
// back.
func TestWriteGoesToTheCPU(t *testing.T) {
	s, _, base := start(t)
	waitFor(t, "first values", func() bool { return state(t, base)["Level"] != nil })
	if code, body := post(t, base, "/api/tags", `{"name":"Level","value":3.5}`); code != http.StatusNoContent {
		t.Fatalf("write: %d %s", code, body)
	}
	if s.Value(`"Tags".Level`) != 3.5 {
		t.Fatalf("the CPU holds %v", s.Value(`"Tags".Level`))
	}
	waitFor(t, "the written value read back", func() bool { return state(t, base)["Level"] == 3.5 })
	if code, _ := post(t, base, "/api/tags", `{"name":"Speed","value":-7}`); code != http.StatusNoContent {
		t.Fatalf("int write: %d", code)
	}
	if s.Value(`"Tags".Speed`) != float64(-7) {
		t.Fatalf("Speed on the CPU: %v", s.Value(`"Tags".Speed`))
	}
}

// TestWriteRefusals: a write that could never succeed is the caller's
// 400, and nothing reaches the CPU.
func TestWriteRefusals(t *testing.T) {
	s, _, base := start(t)
	waitFor(t, "first values", func() bool { return state(t, base)["Level"] != nil })
	for _, c := range []struct{ body, want string }{
		{`{"name":"Nope","value":1}`, "not a tag of this CPU"},
		{`{"name":"Level","value":"high"}`, "REAL takes a number"},
		{`{"name":"Run","value":1}`, "BOOL takes true or false"},
		{`{"name":"Speed","value":40000}`, "outside INT"},
		{`{"name":"Speed","value":1.5}`, "whole number"},
		{`{"name":"Total","value":1}`, "read-only"},
	} {
		code, body := post(t, base, "/api/tags", c.body)
		if code != http.StatusBadRequest || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %s, want 400 …%s…", c.body, code, body, c.want)
		}
	}
	if s.Value(`"Tags".Speed`) != 3 || s.Value(`"Tags".Level`) != 12.5 || s.Value(`"Tags".Run`) != false {
		t.Fatal("a refused write changed the CPU")
	}
}

// TestForceIsRefused: the Web API writes but does not force (D4).
func TestForceIsRefused(t *testing.T) {
	s, _, base := start(t)
	waitFor(t, "first values", func() bool { return state(t, base)["Level"] != nil })
	if code, _ := post(t, base, "/api/forces", `{"name":"Level","value":1}`); code != http.StatusNotImplemented {
		t.Fatalf("force: %d, want 501", code)
	}
	if s.Value(`"Tags".Level`) != 12.5 {
		t.Fatal("a refused force changed the CPU")
	}
}

func TestProgramEndpointsSayWhereTheProgramIs(t *testing.T) {
	_, _, base := start(t)
	res, err := http.Get(base + "/api/program")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound || !strings.Contains(string(b), "Siemens CPU") {
		t.Fatalf("GET /api/program: %d %s", res.StatusCode, b)
	}
	req, _ := http.NewRequest(http.MethodPut, base+"/api/program", strings.NewReader("PROGRAM x END_PROGRAM"))
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden || !strings.Contains(string(b), "naut siemens deploy") {
		t.Fatalf("PUT /api/program: %d %s, want 403 naming naut siemens deploy", res.StatusCode, b)
	}
}

// TestDriverStatus: connected while polls succeed, degraded when a symbol
// fails or the last write failed, error when the CPU is unreachable.
func TestDriverStatus(t *testing.T) {
	s, f, base := start(t)
	waitFor(t, "connected", func() bool { return f.driverStatus()[0].State == "connected" })

	s.Put(`"Main_DB".count`, nil) // a symbol the CPU no longer has
	waitFor(t, "degraded", func() bool { return f.driverStatus()[0].State == "degraded" })
	if st := f.driverStatus()[0]; !strings.Contains(st.Message, "1 failing") {
		t.Fatalf("status %+v", st)
	}
	// The other tags keep updating.
	s.Set(`"Tags".Level`, 9.0)
	waitFor(t, "Level still polled", func() bool { return state(t, base)["Level"] == 9.0 })

	s.Put(`"Main_DB".count`, &webapitest.Var{Datatype: "int", Value: 4})
	waitFor(t, "connected again", func() bool { return f.driverStatus()[0].State == "connected" })

	s.SetUser("naut", webapitest.User{Password: "pw", Permissions: []string{"read_value"}})
	if code, _ := post(t, base, "/api/tags", `{"name":"Level","value":1.0}`); code != http.StatusBadRequest {
		t.Fatalf("a denied write: %d", code)
	}
	if st := f.driverStatus()[0]; st.State != "degraded" || !strings.Contains(st.LastError, "write") {
		t.Fatalf("after a failed write: %+v", st)
	}

	s.StopAnswering()
	waitFor(t, "error", func() bool { return f.driverStatus()[0].State == "error" })
}

func TestReadInputsBeforeTheFirstPoll(t *testing.T) {
	_, c := cpu(t)
	d := NewDriver(c, []Binding{{Name: "Level", Symbol: `"Tags".Level`, Type: "REAL"}, {Name: "Speed", Symbol: `"Tags".Speed`, Type: "INT"}}, time.Second)
	if _, err := d.ReadInputs(); err == nil {
		t.Fatal("no values before the first poll")
	}
	d.PollOnce(context.Background())
	v, err := d.ReadInputs()
	if err != nil || v["Level"] != 12.5 {
		t.Fatalf("%v %v", v, err)
	}
	// An integer tag holds an int64 in the store, as the runtime's own
	// INT tags do, not a float64 that happens to be whole.
	if n, ok := v["Speed"].(int64); !ok || n != 3 {
		t.Fatalf("Speed = %#v, want int64(3)", v["Speed"])
	}
}

func TestConvertWrite(t *testing.T) {
	for _, c := range []struct {
		typ  string
		in   any
		want any
		bad  bool
	}{
		{"BOOL", true, true, false}, {"BOOL", 1.0, nil, true},
		{"REAL", 2.0, 2.0, false}, {"REAL", "2", nil, true},
		{"INT", 32767.0, int64(32767), false}, {"INT", 32768.0, nil, true}, {"INT", -32769.0, nil, true},
		{"USINT", -1.0, nil, true}, {"UDINT", 4294967295.0, int64(4294967295), false},
		{"LINT", 1e15, int64(1e15), false}, {"LINT", 1e17, nil, true}, {"ULINT", -1.0, nil, true},
		{"DINT", 1.5, nil, true},
		{"REAL", math.NaN(), nil, true}, {"LREAL", math.Inf(1), nil, true},
	} {
		got, err := convertWrite(c.typ, c.in)
		if c.bad != (err != nil) || (!c.bad && got != c.want) {
			t.Errorf("convertWrite(%s, %v) = %v %v", c.typ, c.in, got, err)
		}
	}
}
