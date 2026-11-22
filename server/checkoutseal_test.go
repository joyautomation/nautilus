//go:build !js

package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/checkout"
	"github.com/joyautomation/nautilus/runtime"
)

// ckSealServer is a checkout server where akhan (an engineer) records, jsmith
// (a supervisor: signoff, not checkout) signs off and ghost is an operator.
func ckSealServer(t *testing.T, mode string, evidence func(*checkout.Sheet) (checkout.Evidence, string)) (*Server, *checkout.Store, *CheckoutOptions) {
	t.Helper()
	st := newCkStore(t)
	co := &CheckoutOptions{Store: st, Sheet: ckSheet(), Signoff: mode, Evidence: evidence}
	rt, _, _ := newSubRuntime(t) // Level can be substituted
	srv, _, _ := patServerSettings(t, rt, ckOpts(Options{}, co), auth.Settings{
		Users: map[string][]auth.Grant{
			"akhan":  {{Role: auth.Engineer}},
			"jsmith": {{Role: auth.Supervisor}},
			"ghost":  {{Role: auth.Operator}},
		},
	})
	ckReg(srv, co)
	return srv, st, co
}

func realDevices(*checkout.Sheet) (checkout.Evidence, string) {
	return checkout.Evidence{Drivers: []checkout.DriverEvidence{{Name: "plc", Kind: "modbus"}},
		Devices: []checkout.DeviceEvidence{{Name: "rio1", Endpoint: "plc.example:502", Class: "network"}}}, ""
}

// passAll begins a run as akhan and passes every point of the sheet.
func passAll(t *testing.T, srv *Server, rehearsal bool) (string, func(*http.Request)) {
	t.Helper()
	eng := ckAs(t, srv, "akhan")
	body := `{}`
	if rehearsal {
		body = `{"rehearsal":true}`
	}
	var run checkout.Run
	rec := call(t, srv, "POST", "/api/checkout/runs", body, eng)
	if rec.Code != http.StatusCreated {
		t.Fatalf("begin: %d %s", rec.Code, rec.Body)
	}
	json.Unmarshal(rec.Body.Bytes(), &run)
	for _, p := range ckSheet().Points {
		if rec := call(t, srv, "POST", "/api/checkout/runs/"+run.ID+"/results", `{"point":"`+p.ID+`","outcome":"pass"}`, eng); rec.Code != http.StatusCreated {
			t.Fatalf("result %s: %d %s", p.ID, rec.Code, rec.Body)
		}
	}
	return run.ID, eng
}

func TestCheckoutSealSignsAndTheRecordVerifies(t *testing.T) {
	srv, st, _ := ckSealServer(t, "two-person", realDevices)
	id, _ := passAll(t, srv, false)
	sup := ckAs(t, srv, "jsmith")
	rec := call(t, srv, "POST", "/api/checkout/runs/"+id+"/seal", `{"note":"walked it down with the contractor"}`, sup)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seal: %d %s", rec.Code, rec.Body)
	}
	var v sealedView
	json.Unmarshal(rec.Body.Bytes(), &v)
	if v.Run != id || v.Fingerprint != st.KeyFingerprint() || len(v.Record.Payload) == 0 {
		t.Errorf("answer %+v", v)
	}
	// the record is served as the file, and it verifies against the controller's own results file
	got := call(t, srv, "GET", "/api/checkout/runs/"+id+"/sealed", "", sup)
	if got.Code != http.StatusOK || !strings.HasSuffix(got.Header().Get("Content-Disposition"), `-sealed.json"`) {
		t.Fatalf("sealed: %d %v", got.Code, got.Header())
	}
	chain, _ := os.ReadFile(filepath.Join(filepath.Dir(st.SealedPath(id)), "checkout.jsonl"))
	rep, err := checkout.Verify(got.Body.Bytes(), chain)
	if err != nil || !rep.ChainChecked || rep.Signer != "jsmith" || rep.Mode != "two-person" {
		t.Fatalf("verify: %+v %v", rep, err)
	}
	var pl checkout.SealPayload
	var sr checkout.SealedRecord
	json.Unmarshal(got.Body.Bytes(), &sr)
	json.Unmarshal(sr.Payload, &pl)
	if pl.Note != "walked it down with the contractor" || pl.SignerPrincipal != "user:jsmith" || len(pl.Evidence.Drivers) != 1 || len(pl.Recorders) != 1 || pl.Recorders[0] != "user:akhan" {
		t.Errorf("payload %+v", pl)
	}
	// the run says it is sealed and takes nothing more
	if run := call(t, srv, "GET", "/api/checkout/runs/"+id, "", sup).Body.String(); !strings.Contains(run, `"state":"sealed"`) {
		t.Errorf("run: %s", run)
	}
	if rec := call(t, srv, "POST", "/api/checkout/runs/"+id+"/results", `{"point":"SP","outcome":"fail","note":"late"}`, ckAs(t, srv, "akhan")); rec.Code != http.StatusConflict {
		t.Errorf("a result after the seal: %d %s", rec.Code, rec.Body)
	}
	// the export of a sealed run says it is sealed and not the signed bytes
	if ex := call(t, srv, "GET", "/api/checkout/runs/"+id+"/export", "", sup).Body.String(); !strings.Contains(ex, "not the signed bytes") {
		t.Errorf("export of a sealed run: %s", ex)
	}
}

func TestCheckoutSealRefusals(t *testing.T) {
	type c struct {
		name   string
		who    string // "" signs nobody in
		body   string
		mode   string
		ev     func(*checkout.Sheet) (checkout.Evidence, string)
		rehear bool
		edit   func(*testing.T, *Server, *CheckoutOptions, string)
		want   int
		says   string
	}
	virtual := func(*checkout.Sheet) (checkout.Evidence, string) {
		return checkout.Evidence{}, "device rio1 is a virtual device the project serves itself: a checkout against it proves nothing about the plant"
	}
	for _, tc := range []c{
		{name: "the recorder signs (two-person)", who: "akhan", mode: "two-person", ev: realDevices, want: 409, says: "recorded results"},
		{name: "an operator (no signoff)", who: "ghost", mode: "two-person", ev: realDevices, want: 403},
		{name: "no sign-in", who: "", mode: "two-person", ev: realDevices, want: 401},
		{name: "virtual devices", who: "jsmith", mode: "two-person", ev: virtual, want: 409, says: "virtual device"},
		{name: "a controller that cannot vouch for its devices", who: "jsmith", mode: "two-person", ev: nil, want: 409, says: "cannot say its devices are real"},
		{name: "a rehearsal run, whatever the body says", who: "jsmith", body: `{"rehearsal":false}`, mode: "two-person", ev: realDevices, rehear: true, want: 409, says: "rehearsal"},
		{name: "a body that tries to supply evidence", who: "jsmith", body: `{"evidence":{"drivers":[]}}`, mode: "two-person", ev: realDevices, want: 400},
		{name: "a sheet that changed", who: "jsmith", mode: "two-person", ev: realDevices, want: 409, says: "sheet changed",
			edit: func(t *testing.T, s *Server, co *CheckoutOptions, id string) {
				sh := ckSheet()
				sh.Points[0].Expect = "edited"
				co.Sheet = sh
			}},
		{name: "a checkout force starting or standing", who: "jsmith", mode: "two-person", ev: realDevices, want: 409, says: "force",
			edit: func(t *testing.T, s *Server, co *CheckoutOptions, id string) {
				co.ck.mu.Lock()
				co.ck.force = &ckForce{run: id, point: "OUT"}
				co.ck.mu.Unlock()
			}},
	} {
		srv, st, co := ckSealServer(t, tc.mode, tc.ev)
		id, eng := passAll(t, srv, tc.rehear)
		if tc.edit != nil {
			tc.edit(t, srv, co, id)
		}
		var who func(*http.Request)
		switch tc.who {
		case "akhan":
			who = eng
		case "":
			who = func(r *http.Request) { ownOrigin(r); asJSON(r) }
		default:
			who = ckAs(t, srv, tc.who)
		}
		body := tc.body
		if body == "" {
			body = `{}`
		}
		before, _ := os.ReadFile(filepath.Join(filepath.Dir(st.SealedPath(id)), "checkout.jsonl"))
		rec := call(t, srv, "POST", "/api/checkout/runs/"+id+"/seal", body, who)
		if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.says) {
			t.Errorf("%s: %d %s, want %d containing %q", tc.name, rec.Code, rec.Body, tc.want, tc.says)
		}
		after, _ := os.ReadFile(filepath.Join(filepath.Dir(st.SealedPath(id)), "checkout.jsonl"))
		if string(before) != string(after) {
			t.Errorf("%s: a refused seal wrote to the results file", tc.name)
		}
		if g, _ := st.Get(id); g.State == checkout.StateSealed {
			t.Errorf("%s: the run is sealed", tc.name)
		}
		if _, err := st.ReadSealed(id); err == nil {
			t.Errorf("%s: a sealed record exists", tc.name)
		}
	}
}

// Single-person sign-off: the recorder may sign, and the record says so.
func TestCheckoutSealSinglePersonSaysSo(t *testing.T) {
	srv, st, _ := ckSealServer(t, "single", realDevices)
	id, eng := passAll(t, srv, false)
	if rec := call(t, srv, "POST", "/api/checkout/runs/"+id+"/seal", `{}`, eng); rec.Code != http.StatusCreated {
		t.Fatalf("single seal: %d %s", rec.Code, rec.Body)
	}
	b, _ := st.ReadSealed(id)
	var sr checkout.SealedRecord
	var pl checkout.SealPayload
	json.Unmarshal(b, &sr)
	json.Unmarshal(sr.Payload, &pl)
	if pl.Mode != "single" || !strings.Contains(pl.Statement, "single-person") {
		t.Errorf("payload %+v", pl)
	}
}

func TestCheckoutSealedForAnUnsealedRunIs404(t *testing.T) {
	srv, _, _ := ckSealServer(t, "two-person", realDevices)
	id, eng := passAll(t, srv, false)
	if rec := call(t, srv, "GET", "/api/checkout/runs/"+id+"/sealed", "", eng); rec.Code != http.StatusNotFound {
		t.Errorf("an unsealed run: %d %s", rec.Code, rec.Body)
	}
}

func TestCheckoutReport(t *testing.T) {
	srv, st, _ := ckSealServer(t, "two-person", realDevices)
	id, eng := passAll(t, srv, false)
	// a run not yet sealed prints as a draft
	rec := call(t, srv, "GET", "/api/checkout/runs/"+id+"/report", "", eng)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), "DRAFT, NOT SIGNED OFF") {
		t.Fatalf("draft: %d %v %.200s", rec.Code, rec.Header(), rec.Body)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers %v", rec.Header())
	}
	sup := ckAs(t, srv, "jsmith")
	if r := call(t, srv, "POST", "/api/checkout/runs/"+id+"/seal", `{"note":"ok"}`, sup); r.Code != http.StatusCreated {
		t.Fatalf("seal: %d %s", r.Code, r.Body)
	}
	rec = call(t, srv, "GET", "/api/checkout/runs/"+id+"/report", "", sup)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "SEALED: signed off by jsmith") {
		t.Fatalf("sealed: %d %.300s", rec.Code, rec.Body)
	}
	if rec := call(t, srv, "GET", "/api/checkout/runs/none/report", "", sup); rec.Code != http.StatusNotFound {
		t.Errorf("no such run: %d", rec.Code)
	}
	// a sealed record that no longer verifies is not printed as signed
	p := st.SealedPath(id)
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), `"signer": "jsmith"`, `"signer": "eve"`, 1)), 0o600)
	if rec := call(t, srv, "GET", "/api/checkout/runs/"+id+"/report", "", sup); rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "does not verify") {
		t.Errorf("an altered record: %d %.200s", rec.Code, rec.Body)
	}
}

// The rehearsal flag is the client's, so the controller works out for itself
// when a run began whether real field devices were behind the points: a run
// begun against virtual devices says so, and no later change makes it signable.
func TestCheckoutRunBegunAgainstVirtualDevicesCanNeverBeSealed(t *testing.T) {
	virtual := func(*checkout.Sheet) (checkout.Evidence, string) {
		return checkout.Evidence{Devices: []checkout.DeviceEvidence{{Name: "rio1", Endpoint: "127.0.0.1:15020", Class: "loopback"}}}, "device rio1 is at a loopback address"
	}
	srv, _, co := ckSealServer(t, "two-person", virtual)
	id, _ := passAll(t, srv, false)
	sup := ckAs(t, srv, "jsmith")
	if run := call(t, srv, "GET", "/api/checkout/runs/"+id, "", sup).Body.String(); !strings.Contains(run, `"unreal":"device rio1 is at a loopback address"`) {
		t.Errorf("the run does not say why it cannot be signed: %s", run)
	}
	co.Evidence = realDevices // the project is repointed and restarted; the run stays unsignable
	if rec := call(t, srv, "POST", "/api/checkout/runs/"+id+"/seal", `{}`, sup); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not real field I/O") {
		t.Errorf("seal after repointing: %d %s", rec.Code, rec.Body)
	}
}

func TestCheckoutSealRefusesDevicesThatChangedSinceTheRunBegan(t *testing.T) {
	srv, _, co := ckSealServer(t, "two-person", realDevices)
	id, _ := passAll(t, srv, false)
	co.Evidence = func(*checkout.Sheet) (checkout.Evidence, string) {
		return checkout.Evidence{Drivers: []checkout.DriverEvidence{{Name: "other", Kind: "eip"}}}, ""
	}
	if rec := call(t, srv, "POST", "/api/checkout/runs/"+id+"/seal", `{}`, ckAs(t, srv, "jsmith")); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "changed since this run began") {
		t.Errorf("seal after the devices changed: %d %s", rec.Code, rec.Body)
	}
}

// A force or a substitute on a sheet tag (anything but this checkout's own
// force on the point) makes the reading not the wiring's: a pass is refused
// naming why, a fail is recorded with what stood, the checkout's own force on
// its output is the test and does not count, and a seal is refused while any
// override stands on a sheet tag.
func TestCheckoutOverridesOnSheetTags(t *testing.T) {
	srv, st, _ := ckSealServer(t, "two-person", realDevices)
	eng := ckAs(t, srv, "akhan")
	var run checkout.Run
	json.Unmarshal(call(t, srv, "POST", "/api/checkout/runs", `{}`, eng).Body.Bytes(), &run)
	path := "/api/checkout/runs/" + run.ID
	tags := srv.rt.Tags()

	// a plain runtime force on the point's tag
	if err := tags.Force("SP", 70.0); err != nil {
		t.Fatal(err)
	}
	rec := call(t, srv, "POST", path+"/results", `{"point":"SP","outcome":"pass"}`, eng)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "forced (SP)") {
		t.Errorf("a pass over a force: %d %s", rec.Code, rec.Body)
	}
	rec = call(t, srv, "POST", path+"/results", `{"point":"SP","outcome":"fail","note":"reads the forced value"}`, eng)
	var res checkout.Result
	json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusCreated || res.Override != "forced (SP)" {
		t.Errorf("a fail over a force: %d %s", rec.Code, rec.Body)
	}
	tags.UnforceAll()
	if rec := call(t, srv, "POST", path+"/results", `{"point":"SP","outcome":"pass"}`, eng); rec.Code != http.StatusCreated {
		t.Errorf("a pass once the force is gone: %d %s", rec.Code, rec.Body)
	}

	// a substitute (#707) on an input
	sub, err := srv.rt.Substitute("Level", runtime.SubstituteRequest{Value: 40.0, For: time.Hour, By: "op"})
	if err != nil {
		t.Fatal(err)
	}
	rec = call(t, srv, "POST", path+"/results", `{"point":"LVL","outcome":"pass"}`, eng)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "substituted (Level)") {
		t.Errorf("a pass over a substitute: %d %s", rec.Code, rec.Body)
	}

	// the seal is refused while either stands on a sheet tag
	for _, p := range []string{"OUT"} {
		call(t, srv, "POST", path+"/results", `{"point":"`+p+`","outcome":"pass"}`, eng)
	}
	srv.rt.Restore(sub.ID, "op")
	rec = call(t, srv, "POST", path+"/results", `{"point":"LVL","outcome":"pass"}`, eng)
	if rec.Code != http.StatusCreated {
		t.Fatalf("a pass once the substitute is gone: %d %s", rec.Code, rec.Body)
	}
	sup := ckAs(t, srv, "jsmith")
	if err := tags.Force("Level", 41.0); err != nil {
		t.Fatal(err)
	}
	rec = call(t, srv, "POST", path+"/seal", `{}`, sup)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "LVL forced (Level)") {
		t.Errorf("a seal over a force: %d %s", rec.Code, rec.Body)
	}
	tags.UnforceAll()
	sub, err = srv.rt.Substitute("Level", runtime.SubstituteRequest{Value: 40.0, For: time.Hour, By: "op"})
	if err != nil {
		t.Fatal(err)
	}
	rec = call(t, srv, "POST", path+"/seal", `{}`, sup)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "LVL substituted (Level)") {
		t.Errorf("a seal over a substitute: %d %s", rec.Code, rec.Body)
	}
	srv.rt.Restore(sub.ID, "op")
	if rec := call(t, srv, "POST", path+"/seal", `{}`, sup); rec.Code != http.StatusCreated {
		t.Errorf("a seal with nothing standing: %d %s", rec.Code, rec.Body)
	}
	if g, _ := st.Get(run.ID); g.State != checkout.StateSealed {
		t.Errorf("run %+v", g)
	}
}

// The checkout's own force on its output is the test: a pass on that point is
// recorded while it stands.
func TestCheckoutOwnForceIsNotAnOverride(t *testing.T) {
	r := newCkForceRig(t)
	r.oos(t)
	if rec := r.force(t, goodForce); rec.Code != http.StatusCreated {
		t.Fatalf("force: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, r.srv, "POST", r.path+"/results", `{"point":"OUT","outcome":"pass"}`, r.eng); rec.Code != http.StatusCreated {
		t.Errorf("a pass on the point the checkout itself forces: %d %s", rec.Code, rec.Body)
	}
	call(t, r.srv, "POST", r.path+"/force/release", `{}`, r.eng)
}
