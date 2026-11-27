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

	nio "github.com/joyautomation/nautilus/io"
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
	return startWith(t, Options{})
}

// startWith is start with options set beyond the client, roots and poll.
func startWith(t *testing.T, o Options) (*webapitest.Server, *Facade, string) {
	t.Helper()
	s, c := cpu(t)
	o.Client, o.Host, o.Poll = c, "cpu", 20*time.Millisecond
	if o.Roots == nil {
		o.Roots = roots
	}
	f, err := New(context.Background(), o)
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
	tags, _ := stateQ(t, base)
	return tags
}

// stateQ returns /api/state's values and its non-Good qualities.
func stateQ(t *testing.T, base string) (map[string]any, map[string]string) {
	t.Helper()
	res, err := http.Get(base + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var st struct {
		Tags    map[string]any    `json:"tags"`
		Quality map[string]string `json:"quality"`
	}
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st.Tags, st.Quality
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
		"Level":         `"Tags".Level REAL`,
		"Run":           `"Tags".Run BOOL`,
		"Speed":         `"Tags".Speed INT`,
		"Total":         `"Tags".Total DINT`,
		"Main.count":    `"Main_DB".count INT`,
		"Main.dwell.Q":  `"Main_DB".dwell.Q BOOL`,
		"Main.dwell.ET": `"Main_DB".dwell.ET TIME`,
	}
	if len(got) != len(want) {
		t.Errorf("bound %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	if len(skipped) != 0 {
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
	if res.StatusCode != http.StatusForbidden || !strings.Contains(string(b), "Siemens CPU") || !strings.Contains(string(b), "naut siemens deploy") {
		t.Fatalf("PUT /api/program: %d %s, want the facade's 403", res.StatusCode, b)
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

	// A write the CPU failed (busy, not a refusal) is a 502 and degrades
	// the status until a write succeeds again.
	s.FailWrites(webapi.CodeSystemIsBusy)
	if code, _ := post(t, base, "/api/tags", `{"name":"Level","value":1.0}`); code != http.StatusBadGateway {
		t.Fatalf("a write the CPU failed: %d, want 502", code)
	}
	if st := f.driverStatus()[0]; st.State != "degraded" || !strings.Contains(st.LastError, "write") {
		t.Fatalf("after a failed write: %+v", st)
	}
	s.FailWrites(0)
	if code, _ := post(t, base, "/api/tags", `{"name":"Level","value":2.0}`); code != http.StatusNoContent {
		t.Fatalf("write after the CPU recovered: %d", code)
	}
	if st := f.driverStatus()[0]; st.State != "connected" {
		t.Fatalf("a good write clears the degradation: %+v", st)
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
		{"REAL", 1e39, nil, true}, {"REAL", -1e39, nil, true}, {"LREAL", 1e39, 1e39, false},
	} {
		got, err := convertWrite(c.typ, c.in)
		if c.bad != (err != nil) || (!c.bad && got != c.want) {
			t.Errorf("convertWrite(%s, %v) = %v %v", c.typ, c.in, got, err)
		}
	}
}

// TestQualityFollowsTheReads: a tag whose read fails keeps its last value
// and is Bad (until a re-discovery re-binds without it, and again with it
// once it is back); when the CPU cannot be reached every held value is
// Stale. The quality is what the API says, not just the status message.
func TestQualityFollowsTheReads(t *testing.T) {
	s, _, base := startWith(t, Options{Rediscover: 2 * time.Second, Settle: time.Second})
	waitFor(t, "all good", func() bool {
		tags, q := stateQ(t, base)
		return tags["Main.count"] != nil && len(q) == 0
	})
	s.Put(`"Main_DB".count`, nil)
	waitFor(t, "Main.count bad", func() bool { _, q := stateQ(t, base); return q["Main.count"] == "bad" })
	tags, q := stateQ(t, base)
	if tags["Main.count"] != float64(4) {
		t.Fatalf("a failed read must hold the last value: %v", tags["Main.count"])
	}
	if q["Level"] != "" {
		t.Fatalf("the other tags stay good: %v", q)
	}
	s.Put(`"Main_DB".count`, &webapitest.Var{Datatype: "int", Value: 6})
	waitFor(t, "good again", func() bool {
		tags, q := stateQ(t, base)
		return tags["Main.count"] == float64(6) && q["Main.count"] == ""
	})
	s.StopAnswering()
	waitFor(t, "all stale", func() bool {
		_, q := stateQ(t, base)
		return q["Level"] == "stale" && q["Main.count"] == "stale"
	})
}

func TestNeverReadIsNotConnected(t *testing.T) {
	_, c := cpu(t)
	d := NewDriver(c, []Binding{
		{Name: "Level", Symbol: `"Tags".Level`, Type: "REAL"},
		{Name: "Gone", Symbol: `"Tags".Gone`, Type: "INT"},
	}, time.Second)
	if q := d.Quality(); q["Level"] != nio.NotConnected || q["Gone"] != nio.NotConnected {
		t.Fatalf("before any read: %v", q)
	}
	d.PollOnce(context.Background())
	if q := d.Quality(); q["Gone"] != nio.NotConnected || q["Level"] != nio.Good && len(q) != 1 {
		t.Fatalf("after a read: %v", q)
	}
	if _, said := d.Quality()["Level"]; said {
		t.Fatal("a good tag is not reported")
	}
}

// TestFailedPollsBackOff: a CPU that refuses the login (it restarted with
// other users, say) is not asked again on every poll.
func TestFailedPollsBackOff(t *testing.T) {
	s := webapitest.New()
	s.Users["naut"] = webapitest.User{Password: "pw", Permissions: []string{"read_value"}}
	s.Vars[`"Tags"`] = &webapitest.Var{Datatype: "struct"}
	s.Vars[`"Tags".Level`] = &webapitest.Var{Datatype: "real", Value: 1.0}
	url, pin := s.Start(t)
	c, err := webapi.New(url, webapi.Options{User: "naut", Password: "wrong", CertSHA256: pin})
	if err != nil {
		t.Fatal(err)
	}
	d := NewDriver(c, []Binding{{Name: "Level", Symbol: `"Tags".Level`, Type: "REAL"}}, 2*time.Millisecond)
	d.BackoffMin, d.BackoffMax = 20*time.Millisecond, 80*time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	d.Run(ctx)
	// Without backoff: ~250 polls, each a login. With it: 20, 40, 80, 80, …
	if n := s.LoginAttempts(); n < 3 || n > 12 {
		t.Fatalf("login attempts in 500 ms = %d, want a handful (backed off)", n)
	}
	// And the backoff resets once the CPU answers again.
	s.SetUser("naut", webapitest.User{Password: "wrong", Permissions: []string{"read_value"}})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	d.Run(ctx2)
	if v, err := d.ReadInputs(); err != nil || v["Level"] != 1.0 {
		t.Fatalf("after the CPU accepts the login: %v %v", v, err)
	}
}

// TestRefusedWriteDoesNotDegrade: a write the caller got wrong is a 400,
// not a sign that the write path is failing.
func TestRefusedWriteDoesNotDegrade(t *testing.T) {
	_, f, base := start(t)
	waitFor(t, "connected", func() bool { return f.driverStatus()[0].State == "connected" })
	if code, _ := post(t, base, "/api/tags", `{"name":"Total","value":1}`); code != http.StatusBadRequest {
		t.Fatalf("read-only write: %d", code)
	}
	if st := f.driverStatus()[0]; st.State != "connected" {
		t.Fatalf("a caller's mistake degraded the driver: %+v", st)
	}
}

// TestBackoffRestartsAfterARecovery: the wait after a failed poll doubles,
// and a successful poll resets it, so the next failure waits the minimum
// again rather than the last (long) backoff.
func TestBackoffRestartsAfterARecovery(t *testing.T) {
	s, c := cpu(t)
	d := NewDriver(c, []Binding{{Name: "Level", Symbol: `"Tags".Level`, Type: "REAL"}}, time.Second)
	d.BackoffMin, d.BackoffMax = time.Second, 8*time.Second
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	refuse := func() {
		s.SetUser("naut", webapitest.User{Password: "changed", Permissions: []string{"read_value"}})
		s.Expire() // the CPU restarted: the client must log in again, and cannot
	}
	refuse()
	d.PollOnce(context.Background())
	d.PollOnce(context.Background())
	if d.backoff != 2*time.Second {
		t.Fatalf("after two failures backoff = %s, want 2s", d.backoff)
	}
	s.SetUser("naut", webapitest.User{Password: "pw", Permissions: []string{"read_value"}})
	d.PollOnce(context.Background())
	if !d.due() {
		t.Fatal("a successful poll must clear the wait")
	}
	refuse()
	d.PollOnce(context.Background())
	if d.backoff != time.Second || d.nextTry != now.Add(time.Second) {
		t.Fatalf("the first failure after a recovery waits %s (until %s), want the 1s minimum", d.backoff, d.nextTry)
	}
}

// #819: the facade's own routes keep the server's Host rule. With no
// token, a request addressed to a rebound name is refused before the
// program endpoint or the tag routes answer; by IP it is answered.
func TestFacadeRefusesARebindableHost(t *testing.T) {
	_, _, base := start(t)
	for _, path := range []string{"/api/program", "/api/state"} {
		req, _ := http.NewRequest("GET", base+path, nil)
		req.Host = "evil.example:8080"
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s by a rebound name = %d, want 403", path, res.StatusCode)
		}
		ip, err := http.Get(base + path) // base is 127.0.0.1:<port>
		if err != nil {
			t.Fatal(err)
		}
		ip.Body.Close()
		if ip.StatusCode == http.StatusForbidden {
			t.Errorf("GET %s by IP = 403", path)
		}
	}
}
