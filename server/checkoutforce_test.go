package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/checkout"
)

// ckForceRig is a checkout server with a run begun and an engineer signed in.
type ckForceRig struct {
	srv  *Server
	st   *checkout.Store
	run  checkout.Run
	eng  func(*http.Request)
	path string
}

func newCkForceRig(t *testing.T) *ckForceRig {
	t.Helper()
	srv, st := ckServer(t)
	run, err := st.Begin(false, ckSheet().Hash(), "Test", "akhan", "user:akhan")
	if err != nil {
		t.Fatal(err)
	}
	return &ckForceRig{srv: srv, st: st, run: run, eng: ckAs(t, srv, "akhan"), path: "/api/checkout/runs/" + run.ID}
}

func (r *ckForceRig) oos(t *testing.T) {
	t.Helper()
	if err := r.srv.alarms.TakeOutOfService("HiTempAlm", time.Now().Add(time.Hour), "akhan", "I/O checkout"); err != nil {
		t.Fatal(err)
	}
}

func (r *ckForceRig) force(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, r.srv, "POST", r.path+"/force", body, r.eng)
}

const goodForce = `{"point":"OUT","value":5.5,"seconds":30,"confirm":"OUT"}`

func (r *ckForceRig) forced(name string) bool {
	for _, f := range r.srv.rt.Tags().Forces() {
		if strings.EqualFold(f.Name, name) {
			return true
		}
	}
	return false
}

// Every condition of a checkout force is checked, in the open: nothing is
// forced until the equipment is out of service, the id is typed, the point is
// one the sheet forces, and the time is sane.
func TestCheckoutForceIsHeldToItsInterlock(t *testing.T) {
	r := newCkForceRig(t)
	for _, c := range []struct {
		name, body string
		want       int
		says       string
	}{
		{"equipment in service", goodForce, http.StatusConflict, "take HiTempAlm out of service first"},
		{"no confirmation", `{"point":"OUT","value":5.5,"seconds":30}`, http.StatusUnprocessableEntity, "type the point's id"},
		{"a point the sheet observes", `{"point":"SP","value":1,"confirm":"SP"}`, http.StatusUnprocessableEntity, "observed, not forced"},
		{"a point the sheet lacks", `{"point":"NOPE","value":1,"confirm":"NOPE"}`, http.StatusUnprocessableEntity, "no point"},
		{"no value", `{"point":"OUT","confirm":"OUT"}`, http.StatusBadRequest, "needs a value"},
	} {
		rec := r.force(t, c.body)
		if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.says) {
			t.Errorf("%s: %d %s, want %d and %q", c.name, rec.Code, rec.Body, c.want, c.says)
		}
		if r.forced("Out") {
			t.Fatalf("%s: the output was forced", c.name)
		}
	}
	r.oos(t)
	for _, secs := range []string{"301", "-1"} {
		if rec := r.force(t, `{"point":"OUT","value":5.5,"seconds":`+secs+`,"confirm":"OUT"}`); rec.Code != http.StatusBadRequest {
			t.Errorf("seconds %s: %d %s, want 400", secs, rec.Code, rec.Body)
		}
	}
	if r.forced("Out") {
		t.Fatal("the output was forced by a refused request")
	}
	// a value the tag cannot hold is refused by the force table, and nothing stands
	if rec := r.force(t, `{"point":"OUT","value":"high","confirm":"OUT"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a text value for a number: %d %s", rec.Code, rec.Body)
	}
	if _, ok := r.st.Standing(); ok || ckOf(r.srv).forceView() != nil {
		t.Error("a force stands after a refused value")
	}
}

func TestCheckoutForceStandsThenExpiresByItself(t *testing.T) {
	r := newCkForceRig(t)
	r.oos(t)
	rec := r.force(t, `{"point":"OUT","value":5.5,"seconds":1,"confirm":"OUT"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("force: %d %s", rec.Code, rec.Body)
	}
	if !r.forced("Out") {
		t.Fatal("the output is not forced")
	}
	// every frame says so while it stands
	var f Frame
	json.Unmarshal(call(t, r.srv, "GET", "/api/state", "", r.eng).Body.Bytes(), &f)
	if f.CheckoutForce == nil || f.CheckoutForce.Point != "OUT" || f.CheckoutForce.By != "akhan" || f.CheckoutForce.RemainingMs <= 0 || f.CheckoutForce.RemainingMs > 1000 {
		t.Errorf("frame's checkoutForce %+v", f.CheckoutForce)
	}
	// a second one, on the same point, is refused while it stands
	if rec := r.force(t, goodForce); rec.Code != http.StatusConflict {
		t.Errorf("a second force: %d %s, want 409", rec.Code, rec.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.forced("Out") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if r.forced("Out") {
		t.Fatal("the force did not let go by itself")
	}
	if _, ok := r.st.Standing(); ok {
		t.Error("the record still says it stands")
	}
	f = Frame{}
	json.Unmarshal(call(t, r.srv, "GET", "/api/state", "", r.eng).Body.Bytes(), &f)
	if f.CheckoutForce != nil {
		t.Errorf("the frame still says a checkout force stands: %+v", f.CheckoutForce)
	}
	// the controller's own end of it is journalled, as the system
	var ended bool
	for _, e := range r.srv.audit.ring.Recent() {
		if e.Action == "checkout.force-end" && e.User == "system" && e.Target == "Out" && e.Detail["how"] == "expired" {
			ended = true
		}
	}
	if !ended {
		t.Error("the expiry left no audit entry")
	}
	// and a result recorded after it says how it ended
	rec = call(t, r.srv, "POST", r.path+"/results", `{"point":"OUT","outcome":"pass"}`, r.eng)
	var res checkout.Result
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Force == nil || res.Force.Ended != checkout.ForceExpired || res.Force.Seconds != 1 {
		t.Errorf("result after expiry: %+v", res.Force)
	}
}

func TestCheckoutForceReleaseAndAbandon(t *testing.T) {
	r := newCkForceRig(t)
	r.oos(t)
	if rec := r.force(t, goodForce); rec.Code != http.StatusCreated {
		t.Fatalf("force: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, r.srv, "POST", r.path+"/force/release", `{}`, r.eng); rec.Code != http.StatusOK || r.forced("Out") {
		t.Errorf("release: %d %s, forced %v", rec.Code, rec.Body, r.forced("Out"))
	}
	if rec := call(t, r.srv, "POST", r.path+"/force/release", `{}`, r.eng); rec.Code != http.StatusConflict {
		t.Errorf("a second release: %d, want 409", rec.Code)
	}
	// abandoning the run lets a standing force go first
	if rec := r.force(t, goodForce); rec.Code != http.StatusCreated {
		t.Fatalf("force again: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, r.srv, "POST", r.path+"/abandon", `{"reason":"wrong sheet"}`, r.eng); rec.Code != http.StatusOK {
		t.Fatalf("abandon: %d %s", rec.Code, rec.Body)
	}
	if r.forced("Out") || ckOf(r.srv).forceView() != nil {
		t.Error("abandoning the run left the output forced")
	}
}

// A force someone else put on the output meanwhile is theirs: the checkout's
// expiry leaves it alone, and the record says it was replaced.
func TestCheckoutForceDoesNotRemoveAnotherForce(t *testing.T) {
	r := newCkForceRig(t)
	r.oos(t)
	if rec := r.force(t, `{"point":"OUT","value":5.5,"seconds":1,"confirm":"OUT"}`); rec.Code != http.StatusCreated {
		t.Fatalf("force: %d %s", rec.Code, rec.Body)
	}
	tags := r.srv.rt.Tags()
	tags.Unforce("Out")
	time.Sleep(5 * time.Millisecond)
	if err := tags.Force("Out", 9.0); err != nil { // someone else's, at another value
		t.Fatal(err)
	}
	time.Sleep(1300 * time.Millisecond)
	if !r.forced("Out") {
		t.Fatal("the checkout's expiry removed a force that was not its own")
	}
	if _, ok := r.st.Standing(); ok {
		t.Error("the record still says the checkout force stands")
	}
	rec := call(t, r.srv, "POST", r.path+"/results", `{"point":"OUT","outcome":"note","note":"someone forced it"}`, r.eng)
	var res checkout.Result
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Force == nil || res.Force.Ended != checkout.ForceReplaced {
		t.Errorf("result: %+v, want the force recorded as replaced", res.Force)
	}
}

// A force already on the output (an operator's) is not taken over.
func TestCheckoutForceRefusesAnOutputAlreadyForced(t *testing.T) {
	r := newCkForceRig(t)
	r.oos(t)
	if err := r.srv.rt.Tags().Force("Out", 1.0); err != nil {
		t.Fatal(err)
	}
	if rec := r.force(t, goodForce); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already forced") {
		t.Errorf("%d %s, want 409 already forced", rec.Code, rec.Body)
	}
}

// Who may force: the checkout permission and the force permission, both, in
// the point's area, and never a token.
func TestCheckoutForceNeedsBothPermissions(t *testing.T) {
	st := newCkStore(t)
	co := &CheckoutOptions{Store: st, Sheet: ckSheet()}
	srv, _, _ := patServerSettings(t, newTestRuntime(t), ckOpts(Options{}, co), auth.Settings{
		Areas: map[string][]string{"out": {"Out"}, "sp": {"SP"}},
		Roles: map[string]auth.RoleDef{"checker": {Inherits: auth.Viewer, Allow: []string{auth.Checkout}}, "forcer": {Inherits: auth.Viewer, Allow: []string{auth.Force}}},
		Users: map[string][]auth.Grant{
			"akhan":  {{Role: auth.Engineer}},                                           // both, everywhere
			"ghost":  {{Role: "checker"}},                                               // checkout only
			"jsmith": {{Role: "checker"}, {Role: auth.Engineer, Areas: []string{"sp"}}}, // checkout everywhere, force in another area only
		},
	})
	ckReg(srv, co)
	run, _ := st.Begin(false, ckSheet().Hash(), "T", "akhan", "user:akhan")
	srv.alarms.TakeOutOfService("HiTempAlm", time.Now().Add(time.Hour), "akhan", "x")
	path := "/api/checkout/runs/" + run.ID + "/force"
	for _, who := range []string{"ghost", "jsmith"} {
		rec := call(t, srv, "POST", path, goodForce, ckAs(t, srv, who))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", who, rec.Code, rec.Body)
		}
		for _, f := range srv.rt.Tags().Forces() {
			t.Errorf("%s: %s was forced", who, f.Name)
		}
	}
	if rec := call(t, srv, "POST", path, goodForce, ckAs(t, srv, "akhan")); rec.Code != http.StatusCreated {
		t.Errorf("an engineer: %d %s", rec.Code, rec.Body)
	}
}
