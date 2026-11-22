package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/checkout"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
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
	if _, ok := r.st.Standing(); ok || ckOf(r.srv).forceView(r.srv.rt.Tags()) != nil {
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
	if r.forced("Out") || ckOf(r.srv).forceView(r.srv.rt.Tags()) != nil {
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
	if err := tags.Force("Out", 5.5); err != nil { // someone else's, at the SAME value: only the generation tells it from ours
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

// A checkout force on a typed output (an enumeration, a TIME, a member of a
// struct) is let go like any other: the force is identified by the table's
// generation, never by comparing the value it was sent with the value the
// table holds after coercion, which differ for these.
const ckTypesLib = `TYPE Mode : (Off, Run, Fault) := Off; Pump : STRUCT Run : BOOL; Speed : REAL; END_STRUCT; END_TYPE`
const ckTypesProg = `PROGRAM Test
VAR_EXTERNAL
  Level : REAL; SP : REAL; Out : REAL; M : Mode; T : TIME; P101 : Pump; Arr : ARRAY[0..2] OF REAL;
END_VAR
Out := SP - Level;
END_PROGRAM
`

func newCkTypesRig(t *testing.T) *ckForceRig {
	t.Helper()
	rt, err := runtime.New(runtime.Options{Program: ckTypesProg, Libraries: []string{ckTypesLib}, Driver: nio.NewMemory(),
		Tags: []runtime.TagDef{runtime.Typed("Level", runtime.RoleState, "REAL"), runtime.Typed("SP", runtime.RoleState, "REAL"),
			runtime.Typed("Out", runtime.RoleState, "REAL"), runtime.Typed("M", runtime.RoleState, "Mode"),
			runtime.Typed("T", runtime.RoleState, "TIME"), runtime.Typed("P101", runtime.RoleState, "Pump"),
			runtime.Typed("Arr", runtime.RoleState, "ARRAY[0..2] OF REAL")}})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	st := newCkStore(t)
	sh := ckSheet()
	for _, p := range []checkout.Point{
		{ID: "MODE", Tag: "M", Kind: checkout.DO, Method: checkout.Force},
		{ID: "TMR", Tag: "T", Kind: checkout.AO, Method: checkout.Force},
		{ID: "PSPD", Tag: "p101.speed", Kind: checkout.AO, Method: checkout.Force},
		{ID: "ARR", Tag: "Arr[ 1]", Kind: checkout.AO, Method: checkout.Force}, // an element, spelled as the table does not
	} {
		p.Equipment, p.Device, p.Area = "HiTempAlm", "rio1", "RCV"
		sh.Points = append(sh.Points, p)
	}
	co := &CheckoutOptions{Store: st, Sheet: sh}
	srv, _, _ := patServerRT(t, rt, ckOpts(Options{}, co))
	ckReg(srv, co)
	run, err := st.Begin(false, sh.Hash(), "T", "akhan", "user:akhan")
	if err != nil {
		t.Fatal(err)
	}
	return &ckForceRig{srv: srv, st: st, run: run, eng: ckAs(t, srv, "akhan"), path: "/api/checkout/runs/" + run.ID}
}

func TestCheckoutForceReleasesTypedOutputs(t *testing.T) {
	for _, c := range []struct{ point, tag, value string }{
		{"MODE", "M", `1`}, {"MODE", "M", `"run"`}, {"MODE", "M", `"Mode#Run"`},
		{"TMR", "T", `"T#5s"`}, {"PSPD", "P101.Speed", `7`}, {"ARR", "Arr[1]", `7`},
	} {
		r := newCkTypesRig(t)
		r.oos(t)
		rec := r.force(t, `{"point":"`+c.point+`","value":`+c.value+`,"seconds":1,"confirm":"`+c.point+`"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s <- %s: %d %s", c.tag, c.value, rec.Code, rec.Body)
		}
		if !r.forced(c.tag) {
			t.Fatalf("%s <- %s: not forced", c.tag, c.value)
		}
		deadline := time.Now().Add(5 * time.Second)
		for r.forced(c.tag) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if r.forced(c.tag) {
			t.Errorf("%s <- %s: still forced after its time: the expiry did not recognise its own force", c.tag, c.value)
		}
		if _, ok := r.st.Standing(); ok {
			t.Errorf("%s <- %s: the record still says it stands", c.tag, c.value)
		}
	}
}

// A force on the tag a member point sits in (or on the member, for a point on
// the tag) is somebody else's: not taken over.
func TestCheckoutForceRefusesAnOverlappingForce(t *testing.T) {
	r := newCkTypesRig(t)
	r.oos(t)
	if err := r.srv.rt.Tags().Force("P101.Run", true); err != nil { // another member of the struct
		t.Fatal(err)
	}
	if err := r.srv.rt.Tags().Force("P101", map[string]any{"Run": true, "Speed": 1.0}); err != nil {
		t.Skip("a whole-struct force is not accepted here:", err)
	}
	rec := r.force(t, `{"point":"PSPD","value":7,"seconds":5,"confirm":"PSPD"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already forced") {
		t.Errorf("a member point under a whole-tag force: %d %s, want 409", rec.Code, rec.Body)
	}
}

// The force ends the moment its equipment is back in service: it must not
// keep driving an output past its permissives while the alarms ring again.
func TestCheckoutForceEndsWhenTheEquipmentIsBackInService(t *testing.T) {
	r := newCkForceRig(t)
	r.oos(t)
	if rec := r.force(t, `{"point":"OUT","value":5.5,"seconds":300,"confirm":"OUT"}`); rec.Code != http.StatusCreated {
		t.Fatalf("force: %d %s", rec.Code, rec.Body)
	}
	if err := r.srv.alarms.ReturnToService("HiTempAlm", "akhan"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.forced("Out") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if r.forced("Out") {
		t.Fatal("the output is still forced after its equipment returned to service")
	}
	rec := call(t, r.srv, "POST", r.path+"/results", `{"point":"OUT","outcome":"note","note":"x"}`, r.eng)
	var res checkout.Result
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Force == nil || res.Force.Ended != checkout.ForceInterlock {
		t.Errorf("result: %+v, want the force ended by its interlock", res.Force)
	}
}

// A window that has run out counts as ended even before the next scan sweeps
// it: no force is started on it.
func TestCheckoutForceRefusesAnExpiredWindow(t *testing.T) {
	r := newCkForceRig(t)
	if err := r.srv.alarms.TakeOutOfService("HiTempAlm", time.Now().Add(80*time.Millisecond), "akhan", "brief"); err != nil {
		t.Skip("a window this short is not accepted:", err)
	}
	time.Sleep(200 * time.Millisecond)
	if rec := r.force(t, goodForce); rec.Code != http.StatusConflict {
		t.Errorf("a force on an expired window: %d %s, want 409", rec.Code, rec.Body)
	}
	if r.forced("Out") {
		t.Error("the output was forced")
	}
}

// Someone clearing the force the plain way takes the checkout's claim with it:
// the frame stops saying it stands, and the record is closed.
func TestCheckoutForceNoticesItsForceBeingCleared(t *testing.T) {
	r := newCkForceRig(t)
	r.oos(t)
	if rec := r.force(t, goodForce); rec.Code != http.StatusCreated {
		t.Fatalf("force: %d %s", rec.Code, rec.Body)
	}
	r.srv.rt.Tags().Unforce("Out")
	var f Frame
	json.Unmarshal(call(t, r.srv, "GET", "/api/state", "", r.eng).Body.Bytes(), &f)
	if f.CheckoutForce != nil {
		t.Errorf("the frame says a checkout force stands over a cleared output: %+v", f.CheckoutForce)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := r.st.Standing(); !ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the record still says the force stands after someone cleared it")
}

func TestCheckoutForceDefaultsAndConfirmation(t *testing.T) {
	r := newCkTypesRig(t)
	r.oos(t)
	if rec := r.force(t, `{"point":"MODE","value":1,"confirm":"yes"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a confirmation that is not the point's id: %d %s", rec.Code, rec.Body)
	}
	rec := r.force(t, `{"point":"MODE","value":1,"confirm":"MODE"}`)
	var fr checkout.ForceRec
	json.Unmarshal(rec.Body.Bytes(), &fr)
	if rec.Code != http.StatusCreated || fr.Seconds != 30 {
		t.Errorf("a force with no seconds: %d %+v, want 30 s", rec.Code, fr)
	}
	// one at a time: another point is refused while this one stands
	if rec := r.force(t, `{"point":"TMR","value":"T#5s","confirm":"TMR"}`); rec.Code != http.StatusConflict {
		t.Errorf("a second point while one stands: %d %s, want 409", rec.Code, rec.Body)
	}
	if r.forced("T") {
		t.Error("the second output was forced")
	}
}

// A token names nobody: it cannot force.
func TestCheckoutForceRefusesAToken(t *testing.T) {
	st := newCkStore(t)
	co := &CheckoutOptions{Store: st, Sheet: ckSheet()}
	srv, _, _ := patServerWith(t, ckOpts(Options{AuthToken: "s3cret"}, co))
	ckReg(srv, co)
	run, _ := st.Begin(false, ckSheet().Hash(), "T", "akhan", "user:akhan")
	srv.alarms.TakeOutOfService("HiTempAlm", time.Now().Add(time.Hour), "akhan", "x")
	rec := call(t, srv, "POST", "/api/checkout/runs/"+run.ID+"/force", goodForce, func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") })
	if rec.Code == http.StatusCreated || len(srv.rt.Tags().Forces()) != 0 {
		t.Errorf("a token forced an output: %d %s", rec.Code, rec.Body)
	}
}

// The standing force's state is read by every frame and written by release,
// expiry and the watch: run them against each other under -race.
func TestCheckoutForceStateIsRaceFree(t *testing.T) {
	r := newCkForceRig(t)
	r.oos(t)
	if rec := r.force(t, `{"point":"OUT","value":5.5,"seconds":1,"confirm":"OUT"}`); rec.Code != http.StatusCreated {
		t.Fatalf("force: %d %s", rec.Code, rec.Body)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				call(t, r.srv, "GET", "/api/state", "", r.eng)
				call(t, r.srv, "POST", r.path+"/force/release", `{}`, r.eng)
			}
		}()
	}
	time.Sleep(1500 * time.Millisecond)
	close(stop)
	wg.Wait()
	if r.forced("Out") {
		t.Error("the output is still forced")
	}
}

// A force the table spells another way is still found: an element typed
// "Arr[ 1]" is let go, and an operator's force on that element is not taken
// over.
func TestCheckoutForceCanonicalSpellings(t *testing.T) {
	r := newCkTypesRig(t)
	r.oos(t)
	if err := r.srv.rt.Tags().Force("Arr[1]", 1.0); err != nil {
		t.Fatal(err)
	}
	if rec := r.force(t, `{"point":"ARR","value":7,"seconds":5,"confirm":"ARR"}`); rec.Code != http.StatusConflict {
		t.Errorf("an operator's force under another spelling: %d %s, want 409", rec.Code, rec.Body)
	}
	if g, _ := r.srv.rt.Tags().ForceGen("Arr[1]"); g == 0 {
		t.Error("the operator's force was taken over")
	}
}

// A record that says a force stands when the controller holds none (a failed
// end) does not lock the checkout: the next force closes the stale one.
func TestCheckoutForceReconcilesAStaleRecord(t *testing.T) {
	r := newCkForceRig(t)
	r.oos(t)
	if _, err := r.st.StartForce(r.run.ID, "OUT", 1.0, 30, "akhan", "user:akhan"); err != nil { // as if its end was never written
		t.Fatal(err)
	}
	rec := r.force(t, goodForce)
	if rec.Code != http.StatusCreated {
		t.Fatalf("force over a stale record: %d %s", rec.Code, rec.Body)
	}
	if !r.forced("Out") {
		t.Error("not forced")
	}
	// and a second one while it stands is a 409, not a 500
	if rec := r.force(t, goodForce); rec.Code != http.StatusConflict {
		t.Errorf("a second force: %d, want 409", rec.Code)
	}
}

// Frames read the standing force while forces start, are released and expire:
// under -race this is the path every frame takes.
func TestCheckoutForceFramesRaceStartsAndEnds(t *testing.T) {
	r := newCkForceRig(t)
	r.oos(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				call(t, r.srv, "GET", "/api/state", "", r.eng)
				ckOf(r.srv).forceView(r.srv.rt.Tags())
			}
		}()
	}
	for i := 0; i < 15; i++ {
		if rec := r.force(t, `{"point":"OUT","value":5.5,"seconds":30,"confirm":"OUT"}`); rec.Code != http.StatusCreated {
			t.Fatalf("cycle %d: force %d %s", i, rec.Code, rec.Body)
		}
		call(t, r.srv, "POST", r.path+"/force/release", `{}`, r.eng)
	}
	close(stop)
	wg.Wait()
	if r.forced("Out") {
		t.Error("the output is still forced")
	}
}

// Abandoning with a reason too long to record is refused before anything is
// released.
func TestCheckoutAbandonRefusesALongReasonBeforeReleasing(t *testing.T) {
	r := newCkForceRig(t)
	r.oos(t)
	if rec := r.force(t, goodForce); rec.Code != http.StatusCreated {
		t.Fatalf("force: %d %s", rec.Code, rec.Body)
	}
	long := strings.Repeat("x", checkout.MaxReason+1)
	if rec := call(t, r.srv, "POST", r.path+"/abandon", `{"reason":"`+long+`"}`, r.eng); rec.Code != http.StatusBadRequest {
		t.Errorf("a long reason: %d, want 400", rec.Code)
	}
	if !r.forced("Out") {
		t.Error("a refused abandon released the force")
	}
}

// holdAt makes the checkout's hook wait at the named stage until released, and
// says when a request has reached it.
func holdAt(t *testing.T, stage string) (reached <-chan struct{}, release func()) {
	t.Helper()
	r, gate := make(chan struct{}, 4), make(chan struct{})
	old := ckHook
	ckHook = func(s string) {
		if s == stage {
			r <- struct{}{}
			<-gate
		}
	}
	var once sync.Once
	rel := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() { rel(); ckHook = old })
	return r, rel
}

// A run abandoned while a force is starting does not keep the force: the start
// finds out when it is applied, and lets go.
func TestCheckoutAbandonDuringAStartLetsTheForceGo(t *testing.T) {
	r := newCkForceRig(t)
	r.oos(t)
	reached, release := holdAt(t, "applied")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- r.force(t, goodForce) }()
	<-reached
	if rec := call(t, r.srv, "POST", r.path+"/abandon", `{"reason":"wrong sheet"}`, r.eng); rec.Code != http.StatusOK {
		t.Fatalf("abandon: %d %s", rec.Code, rec.Body)
	}
	release()
	rec := <-done
	if rec.Code != http.StatusConflict {
		t.Errorf("the start of a force on an abandoned run: %d %s, want 409", rec.Code, rec.Body)
	}
	if r.forced("Out") {
		t.Error("the output stays forced on an abandoned run")
	}
	if _, ok := r.st.Standing(); ok {
		t.Error("the record says a force stands on an abandoned run")
	}
	if ckOf(r.srv).forceView(r.srv.rt.Tags()) != nil {
		t.Error("a frame says a force stands")
	}
}

// While a force is being let go, another cannot start in the gap: the slot is
// held until the end is recorded, so the new force's record is not closed by
// the old force's end.
func TestCheckoutNoForceStartsWhileAnotherIsEnding(t *testing.T) {
	r := newCkForceRig(t)
	r.oos(t)
	if rec := r.force(t, goodForce); rec.Code != http.StatusCreated {
		t.Fatalf("force: %d %s", rec.Code, rec.Body)
	}
	reached, release := holdAt(t, "ending")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call(t, r.srv, "POST", r.path+"/force/release", `{}`, r.eng) }()
	<-reached
	if rec := r.force(t, goodForce); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already stands") {
		t.Errorf("a start while the last force is ending: %d %s, want 409 (a checkout force already stands), not a trip over the table or the record", rec.Code, rec.Body)
	}
	release()
	if rec := <-done; rec.Code != http.StatusOK {
		t.Fatalf("release: %d %s", rec.Code, rec.Body)
	}
	if rec := r.force(t, goodForce); rec.Code != http.StatusCreated {
		t.Fatalf("a start after it ended: %d %s", rec.Code, rec.Body)
	}
	// the new force's record stands, and the old one's says it was released
	if f, ok := r.st.Standing(); !ok || f.Seq == 0 {
		t.Error("the new force's record does not stand")
	}
	if !r.forced("Out") {
		t.Error("the new force is not applied")
	}
}
