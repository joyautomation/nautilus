package server

import (
	"encoding/json"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/checkout"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/runtime"
)

func ckSheet() *checkout.Sheet {
	return &checkout.Sheet{Title: "Test", Points: []checkout.Point{
		{ID: "SP", Tag: "SP", Kind: checkout.DO, Method: checkout.Observe, Device: "rio1", Area: "RCV"},
		{ID: "LVL", Tag: "Level", Kind: checkout.AI, Method: checkout.Observe, Device: "rio2", Area: "LOD"},
	}}
}

// ckServer is a server with auth, two people (akhan, an engineer; jsmith, an
// operator in one area) and a checkout.
func ckServer(t *testing.T) (*Server, *checkout.Store) {
	t.Helper()
	st, err := checkout.Open(filepath.Join(t.TempDir(), "checkout.jsonl"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, _, _ := patServerWith(t, Options{Checkout: &CheckoutOptions{Store: st, Sheet: ckSheet(), Signoff: "two-person"}})
	return srv, st
}

func ckAs(t *testing.T, s *Server, who string) func(*http.Request) {
	t.Helper()
	c := signIn(t, s, who)
	return func(r *http.Request) { withCookie(c.Value)(r); ownOrigin(r); asJSON(r) }
}

// A project with no checkout: section behaves as before: the routes answer
// that the checkout is not enabled, nothing is kept, and the other
// surfaces are untouched.
func TestCheckoutIsOffUnlessTheProjectEnablesIt(t *testing.T) {
	srv, _, _ := patServerWith(t, Options{})
	as := ckAs(t, srv, "akhan")
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/checkout", ""},
		{"GET", "/api/checkout/runs/x", ""},
		{"POST", "/api/checkout/runs", `{}`},
		{"POST", "/api/checkout/runs/x/results", `{"point":"SP","outcome":"pass"}`},
		{"POST", "/api/checkout/runs/x/abandon", `{"reason":"no"}`},
	} {
		rec := call(t, srv, c.method, c.path, c.body, as)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not enabled for this project") {
			t.Errorf("%s %s: %d %s, want 404 not enabled for this project", c.method, c.path, rec.Code, rec.Body)
		}
	}
	// /api/me shows nothing new: the two permissions are not listed, and not
	// among the elevated ones
	me := call(t, srv, "GET", "/api/me", "", as).Body.String()
	if strings.Contains(me, `"checkout"`) || strings.Contains(me, `"signoff"`) {
		t.Errorf("/api/me for a project without the checkout names its permissions: %s", me)
	}
	// and an ordinary write still works as before
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":71}`, as); rec.Code != http.StatusNoContent {
		t.Errorf("a tag write: %d %s", rec.Code, rec.Body)
	}
}

func TestCheckoutRunAndResults(t *testing.T) {
	srv, st := ckServer(t)
	eng := ckAs(t, srv, "akhan")

	// begin, once
	rec := call(t, srv, "POST", "/api/checkout/runs", `{"rehearsal":true}`, eng)
	if rec.Code != http.StatusCreated {
		t.Fatalf("begin: %d %s", rec.Code, rec.Body)
	}
	var run checkout.Run
	json.Unmarshal(rec.Body.Bytes(), &run)
	if !run.Rehearsal || run.By != "akhan" || run.Principal != "user:akhan" || run.SheetHash != ckSheet().Hash() {
		t.Errorf("run %+v", run)
	}
	if rec := call(t, srv, "POST", "/api/checkout/runs", `{}`, eng); rec.Code != http.StatusConflict {
		t.Errorf("a second open run: %d %s", rec.Code, rec.Body)
	}

	// a result carries what the controller read, never what the client says
	rec = call(t, srv, "POST", "/api/checkout/runs/"+run.ID+"/results", `{"point":"SP","outcome":"pass"}`, eng)
	if rec.Code != http.StatusCreated {
		t.Fatalf("result: %d %s", rec.Code, rec.Body)
	}
	var res checkout.Result
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Point != "SP" || res.Outcome != "pass" || res.By != "akhan" || res.Quality != "good" || res.Seen == nil {
		t.Errorf("result %+v: want the controller's reading and quality", res)
	}
	if rec := call(t, srv, "POST", "/api/checkout/runs/"+run.ID+"/results", `{"point":"SP","outcome":"pass","seen":99}`, eng); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that says what was seen: %d %s, want 400 (unknown field)", rec.Code, rec.Body)
	}
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"point":"NOPE","outcome":"pass"}`, http.StatusUnprocessableEntity},
		{`{"point":"SP","outcome":"fail"}`, http.StatusBadRequest}, // a fail says why
		{`{"point":"SP","outcome":"maybe","note":"x"}`, http.StatusBadRequest},
	} {
		if rec := call(t, srv, "POST", "/api/checkout/runs/"+run.ID+"/results", c.body, eng); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.body, rec.Code, rec.Body, c.want)
		}
	}
	if rec := call(t, srv, "POST", "/api/checkout/runs/nope/results", `{"point":"SP","outcome":"pass"}`, eng); rec.Code != http.StatusNotFound {
		t.Errorf("no such run: %d", rec.Code)
	}

	// the view: the sheet, the open run, progress by device and area
	rec = call(t, srv, "GET", "/api/checkout", "", eng)
	var v checkoutView
	json.Unmarshal(rec.Body.Bytes(), &v)
	if rec.Code != 200 || v.Run == nil || v.Run.ID != run.ID || len(v.Points) != 2 || v.Signoff != "two-person" || v.Hash != ckSheet().Hash() {
		t.Errorf("view %d %+v", rec.Code, v)
	}
	rec = call(t, srv, "GET", "/api/checkout/runs/"+run.ID, "", eng)
	var rv checkoutRunView
	json.Unmarshal(rec.Body.Bytes(), &rv)
	if rv.ByDevice["rio1"].Pass != 1 || rv.ByDevice["rio2"].Pending != 1 || rv.ByArea["RCV"].Total != 1 || rv.Latest["SP"].Outcome != "pass" {
		t.Errorf("run view %+v", rv)
	}

	// abandon: needs a reason, then results are refused
	if rec := call(t, srv, "POST", "/api/checkout/runs/"+run.ID+"/abandon", `{}`, eng); rec.Code != http.StatusBadRequest {
		t.Errorf("abandon with no reason: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, srv, "POST", "/api/checkout/runs/"+run.ID+"/abandon", `{"reason":"wrong sheet"}`, eng); rec.Code != http.StatusOK {
		t.Fatalf("abandon: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, srv, "POST", "/api/checkout/runs/"+run.ID+"/results", `{"point":"SP","outcome":"pass"}`, eng); rec.Code != http.StatusConflict {
		t.Errorf("a result for an abandoned run: %d", rec.Code)
	}
	if _, ok := st.Current(); ok {
		t.Error("an abandoned run is still current")
	}
}

// Who may record: the checkout permission, in the point's area; and a
// person, never a token.
func TestCheckoutWhoMayRecord(t *testing.T) {
	srv, st := ckServer(t)
	eng := ckAs(t, srv, "akhan")
	run, _ := st.Begin(false, ckSheet().Hash(), "Test", "akhan", "user:akhan")
	path := "/api/checkout/runs/" + run.ID + "/results"
	body := `{"point":"SP","outcome":"pass"}`

	// an operator (no checkout permission) is refused, and nothing is kept
	op := ckAs(t, srv, "jsmith")
	if rec := call(t, srv, "POST", path, body, op); rec.Code != http.StatusForbidden {
		t.Errorf("an operator: %d %s, want 403", rec.Code, rec.Body)
	}
	if rec := call(t, srv, "POST", "/api/checkout/runs", `{}`, op); rec.Code != http.StatusForbidden {
		t.Errorf("an operator beginning a run: %d", rec.Code)
	}
	if n := len(st.Results(run.ID)); n != 0 {
		t.Errorf("%d results kept after refusals", n)
	}
	// nobody signed in is refused
	if rec := call(t, srv, "POST", path, body); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Errorf("no credential: %d", rec.Code)
	}
	// the engineer records
	if rec := call(t, srv, "POST", path, body, eng); rec.Code != http.StatusCreated {
		t.Errorf("an engineer: %d %s", rec.Code, rec.Body)
	}
	// a token names nobody: refused with a pointer to signing in
	srv2, _, _ := patServerWith(t, Options{AuthToken: "s3cret", Checkout: &CheckoutOptions{Store: st, Sheet: ckSheet()}})
	rec := call(t, srv2, "POST", path, body, func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") })
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "as a person") {
		t.Errorf("a token: %d %s, want 401 sign in as a person", rec.Code, rec.Body)
	}
}

// A redundant pair refuses the whole surface until the results travel
// through the log.
func TestCheckoutRefusedOnAPair(t *testing.T) {
	st, _ := checkout.Open(filepath.Join(t.TempDir(), "c.jsonl"), nil)
	defer st.Close()
	srv, _, _ := patServerWith(t, Options{Checkout: &CheckoutOptions{Store: st, Sheet: ckSheet()}, Cluster: fakeCluster{status: leader.Status{Mode: "quorum", IsLeader: true}}})
	eng := ckAs(t, srv, "akhan")
	for _, p := range []string{"/api/checkout"} {
		if r := call(t, srv, "GET", p, "", eng); r.Code != http.StatusServiceUnavailable || !strings.Contains(r.Body.String(), "redundant pair") {
			t.Errorf("GET %s on a pair: %d %s", p, r.Code, r.Body)
		}
	}
}

// Recording is scoped to the point's tag: an engineer whose area holds one
// point records that point and not another, and cannot begin a run (that
// needs the whole controller).
func TestCheckoutRecordingIsScopedToThePointsArea(t *testing.T) {
	st, err := checkout.Open(filepath.Join(t.TempDir(), "c.jsonl"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, _, _ := patServerSettings(t, newTestRuntime(t), Options{Checkout: &CheckoutOptions{Store: st, Sheet: ckSheet()}}, auth.Settings{
		Areas: map[string][]string{"sp": {"SP"}},
		Users: map[string][]auth.Grant{
			"akhan":  {{Role: auth.Engineer}},
			"jsmith": {{Role: auth.Engineer, Areas: []string{"sp"}}},
		},
	})
	run, _ := st.Begin(false, ckSheet().Hash(), "Test", "akhan", "user:akhan")
	path := "/api/checkout/runs/" + run.ID + "/results"
	js := ckAs(t, srv, "jsmith")
	if rec := call(t, srv, "POST", path, `{"point":"SP","outcome":"pass"}`, js); rec.Code != http.StatusCreated {
		t.Errorf("a point in the engineer's area: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, srv, "POST", path, `{"point":"LVL","outcome":"pass"}`, js); rec.Code != http.StatusForbidden {
		t.Errorf("a point in another area: %d %s, want 403", rec.Code, rec.Body)
	}
	if rec := call(t, srv, "POST", "/api/checkout/runs/"+run.ID+"/abandon", `{"reason":"x"}`, js); rec.Code != http.StatusForbidden {
		t.Errorf("abandoning with one area: %d %s, want 403", rec.Code, rec.Body)
	}
	if n := len(st.Results(run.ID)); n != 1 {
		t.Errorf("%d results kept", n)
	}
}

// With the checkout on, /api/me lists the permissions a person holds for it.
func TestMeListsTheCheckoutPermissionsOnlyWhenEnabled(t *testing.T) {
	srv, _ := ckServer(t)
	me := call(t, srv, "GET", "/api/me", "", ckAs(t, srv, "akhan")).Body.String()
	if !strings.Contains(me, `"checkout"`) || !strings.Contains(me, `"signoff"`) || !strings.Contains(me, `"redundancy","signoff"]`) {
		t.Errorf("/api/me with the checkout on lacks its permissions: %s", me)
	}
}

// The controller's reading and its quality are looked up by the driver's own
// tag whatever the sheet's spelling; a tag the controller cannot read is
// refused, never recorded as good; the run is tied to its sheet; a NaN reading
// is recorded as text.
func TestCheckoutReadingsAreTheControllersOwn(t *testing.T) {
	drv := nio.NewMemory()
	rt, err := runtime.New(runtime.Options{Program: testProgram, Driver: drv, Inputs: []string{"Level"}, Outputs: []string{"Out"},
		Seed: nio.Values{"Level": 40.0, "SP": 65.0, "Out": 0.0}})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	st, err := checkout.Open(filepath.Join(t.TempDir(), "c.jsonl"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sheet := &checkout.Sheet{Title: "T", Points: []checkout.Point{
		{ID: "lvl", Tag: "level", Kind: checkout.AI, Method: checkout.Observe}, // another spelling
		{ID: "ghost", Tag: "Ghost", Kind: checkout.AI, Method: checkout.Observe},
	}}
	srv, _, _ := patServerRT(t, rt, Options{Checkout: &CheckoutOptions{Store: st, Sheet: sheet}})
	eng := ckAs(t, srv, "akhan")
	run, _ := st.Begin(false, sheet.Hash(), "T", "akhan", "user:akhan")
	path := "/api/checkout/runs/" + run.ID + "/results"

	drv.SetQuality("Level", nio.Stale)
	rec := call(t, srv, "POST", path, `{"point":"lvl","outcome":"pass"}`, eng)
	var res checkout.Result
	json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusCreated || res.Quality != "stale" || res.Seen != 40.0 {
		t.Errorf("a stale input by another spelling: %d %+v, want quality stale and the reading 40", rec.Code, res)
	}
	if rec := call(t, srv, "POST", path, `{"point":"ghost","outcome":"pass"}`, eng); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a tag the controller lacks: %d %s, want 422", rec.Code, rec.Body)
	}

	// a run begun against another sheet takes no results, and the view says so
	old, _ := func() (checkout.Run, error) {
		st.Abandon(run.ID, "new sheet", "akhan", "")
		return st.Begin(false, "an-older-sheet-hash", "T", "akhan", "user:akhan")
	}()
	if rec := call(t, srv, "POST", "/api/checkout/runs/"+old.ID+"/results", `{"point":"lvl","outcome":"pass"}`, eng); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "sheet changed") {
		t.Errorf("a result for a run of another sheet: %d %s, want 409", rec.Code, rec.Body)
	}
	var v checkoutView
	json.Unmarshal(call(t, srv, "GET", "/api/checkout", "", eng).Body.Bytes(), &v)
	if !v.SheetChanged {
		t.Errorf("the view does not say the sheet changed: %+v", v)
	}

	// a failed store is the controller's: 500, not 400
	st.Close()
	if rec := call(t, srv, "POST", "/api/checkout/runs/"+old.ID+"/abandon", `{"reason":"x"}`, eng); rec.Code != http.StatusInternalServerError {
		t.Errorf("a closed store: %d %s, want 500", rec.Code, rec.Body)
	}
}

func TestJSONSafeWritesNaNAsText(t *testing.T) {
	got := jsonSafe(map[string]any{"a": math.NaN(), "b": []any{math.Inf(1), 2.5}})
	if _, err := json.Marshal(got); err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["a"] != "NaN" || m["b"].([]any)[0] != "+Inf" || m["b"].([]any)[1] != 2.5 {
		t.Errorf("%v", got)
	}
}

// A NaN reading (an analog input nobody wired) is the very thing a checkout
// fails: it is recorded, as text, rather than refused as unencodable.
func TestCheckoutRecordsANaNReading(t *testing.T) {
	rt, err := runtime.New(runtime.Options{Program: testProgram, Driver: nio.NewMemory(), Inputs: []string{"Level"}, Outputs: []string{"Out"},
		Seed: nio.Values{"Level": math.NaN(), "SP": 65.0, "Out": 0.0}})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	st, _ := checkout.Open(filepath.Join(t.TempDir(), "c.jsonl"), nil)
	defer st.Close()
	sheet := &checkout.Sheet{Points: []checkout.Point{{ID: "lvl", Tag: "Level", Kind: checkout.AI, Method: checkout.Observe}}}
	srv, _, _ := patServerRT(t, rt, Options{Checkout: &CheckoutOptions{Store: st, Sheet: sheet}})
	run, _ := st.Begin(false, sheet.Hash(), "", "akhan", "user:akhan")
	rec := call(t, srv, "POST", "/api/checkout/runs/"+run.ID+"/results", `{"point":"lvl","outcome":"fail","note":"reads NaN: not wired"}`, ckAs(t, srv, "akhan"))
	var res checkout.Result
	json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusCreated || res.Seen != "NaN" {
		t.Errorf("a NaN reading: %d %s, want recorded as NaN text", rec.Code, rec.Body)
	}
}
