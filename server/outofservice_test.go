package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/oplog"
)

// Equipment out of service over HTTP (#715 PR 5): the routes, who may,
// and a redundant pair's log.

func oosEngine(t *testing.T, cond map[string]any) *alarm.Engine {
	t.Helper()
	eng, err := alarm.New(alarm.Options{
		Defs: []alarm.Def{
			{ID: "A.Fault", Tag: "ZZZ_A.Fault", Equipment: "Skid", AckRequired: true, AutoClear: true},
			{ID: "B.Fault", Tag: "AAA_B.Fault", Equipment: "Skid", AckRequired: true, AutoClear: true},
			{ID: "Z.Fault", Tag: "ZZZ_Z.Fault", AckRequired: true, AutoClear: true},
		},
		Read: func(p string) (any, bool) { v, ok := cond[p]; return v, ok },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	return eng
}

func TestOutOfServiceRoutes(t *testing.T) {
	s, eng := alarmServer(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false})
	eng.Evaluate()
	rec := do(t, s, "POST", "/api/alarms/out-of-service", `{"equipment":"HiTempAlm","seconds":3600,"reason":"probe swap","by":"op"}`)
	var a oosAnswer
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &a) != nil || !a.OutOfService || a.Reason != "probe swap" || !slices.Equal(a.Alarms, []string{"HiTemp"}) {
		t.Fatalf("out of service: %d %s", rec.Code, rec.Body)
	}
	if st := state(eng, "HiTemp"); st != alarm.Suppressed {
		t.Fatalf("HiTemp %s, want suppressed", st)
	}
	rec = do(t, s, "GET", "/api/alarms/out-of-service", "")
	var list []alarm.OutOfService
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list) != 1 || list[0].Equipment != "HiTempAlm" || list[0].By == "" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"equipment":"HiTempAlm","reason":"x"}`, http.StatusBadRequest},                                // no time
		{`{"equipment":"LoFlowAlm","seconds":60}`, http.StatusBadRequest},                                // no reason
		{`{"equipment":"LoFlowAlm","seconds":691200,"reason":"x"}`, http.StatusBadRequest},               // 8 days
		{`{"equipment":"Nope","seconds":60,"reason":"x"}`, http.StatusNotFound},                          // no such equipment
		{`{"seconds":60,"reason":"x"}`, http.StatusBadRequest},                                           // no equipment
		{`{"equipment":"LoFlowAlm","until":"2000-01-01T00:00:00Z","reason":"x"}`, http.StatusBadRequest}, // past
	} {
		if rec := do(t, s, "POST", "/api/alarms/out-of-service", c.body); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.body, rec.Code, rec.Body, c.want)
		}
	}
	if rec := do(t, s, "POST", "/api/alarms/in-service", `{"equipment":"LoFlowAlm"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("in service when not out: %d", rec.Code)
	}
	rec = do(t, s, "POST", "/api/alarms/in-service", `{"equipment":"HiTempAlm","by":"op"}`)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &a) != nil || a.OutOfService {
		t.Fatalf("in service: %d %s", rec.Code, rec.Body)
	}
	eng.Evaluate()
	if st := state(eng, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("HiTemp %s after back in service, want unack-active", st)
	}
}

// Out of service needs a fresh password; back in service does not, and
// needs only ack: an operator puts equipment back.
func TestOutOfServiceNeedsAFreshPasswordAndInServiceDoesNot(t *testing.T) {
	srv, _, _ := patServer(t)
	at := clock(srv)
	c := signIn(t, srv, "akhan")
	oos := func() int {
		return call(t, srv, "POST", "/api/alarms/out-of-service", `{"equipment":"HiTempAlm","seconds":600,"reason":"probe swap"}`, withCookie(c.Value), ownOrigin, asJSON).Code
	}
	at(20 * time.Minute)
	rec := call(t, srv, "POST", "/api/alarms/out-of-service", `{"equipment":"HiTempAlm","seconds":600,"reason":"probe swap"}`, withCookie(c.Value), ownOrigin, asJSON)
	if rec.Code != http.StatusUnauthorized || !elevateMarked(rec.Body.String()) {
		t.Fatalf("a stale password: %d %s, want 401 asking to elevate", rec.Code, rec.Body)
	}
	if len(srv.alarms.OutOfServiceList()) != 0 {
		t.Fatal("a refused out of service was applied")
	}
	if rec := elevate(t, srv, c.Value, "password-akhan"); rec.Code != http.StatusOK {
		t.Fatalf("elevate: %d", rec.Code)
	}
	if code := oos(); code != http.StatusOK {
		t.Fatalf("fresh: %d", code)
	}
	at(60 * time.Minute) // stale again
	if rec := call(t, srv, "POST", "/api/alarms/in-service", `{"equipment":"HiTempAlm"}`, withCookie(c.Value), ownOrigin, asJSON); rec.Code != http.StatusOK {
		t.Fatalf("in service with a stale password: %d %s, want 200", rec.Code, rec.Body)
	}
}

// One action hides every alarm of the equipment, so every one's area is
// checked: a principal fenced to one area may not hide an alarm of
// another by naming equipment that spans both.
func TestOutOfServiceChecksEveryCoveredAlarmsArea(t *testing.T) {
	eng := oosEngine(t, map[string]any{"ZZZ_A.Fault": true, "AAA_B.Fault": true, "ZZZ_Z.Fault": true})
	s := New(newTestRuntime(t), Options{Alarms: eng})
	eng.Evaluate()
	enforce(t, s) // fenced: an administrator over ZZZ_* only
	fenced := func(r *http.Request) { r.Header.Set("Authorization", "Bearer f-secret") }
	body := func(eq string) string { return `{"equipment":"` + eq + `","seconds":600,"reason":"work"}` }
	if rec := call(t, s, "POST", "/api/alarms/out-of-service", body("Skid"), fenced); rec.Code != http.StatusForbidden {
		t.Errorf("Skid (one alarm outside the area): %d %s, want 403", rec.Code, rec.Body)
	}
	if len(eng.OutOfServiceList()) != 0 {
		t.Fatal("a refused out of service was applied")
	}
	if rec := call(t, s, "POST", "/api/alarms/out-of-service", body("ZZZ_Z"), fenced); rec.Code != http.StatusOK {
		t.Errorf("ZZZ_Z (inside): %d %s", rec.Code, rec.Body)
	}
	if err := eng.TakeOutOfService("Skid", time.Now().Add(time.Hour), "admin", "work"); err != nil {
		t.Fatal(err)
	}
	if rec := call(t, s, "POST", "/api/alarms/in-service", `{"equipment":"Skid"}`, fenced); rec.Code != http.StatusForbidden {
		t.Errorf("in service of Skid fenced: %d, want 403", rec.Code)
	}
	viewer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer v-secret") }
	if rec := call(t, s, "POST", "/api/alarms/in-service", `{"equipment":"Skid"}`, viewer); rec.Code != http.StatusForbidden {
		t.Errorf("in service by a viewer: %d, want 403", rec.Code)
	}
	if rec := call(t, s, "GET", "/api/alarms/out-of-service", "", viewer); rec.Code != http.StatusOK {
		t.Errorf("list by a viewer: %d, want 200", rec.Code)
	}
}

func TestOutOfServiceThroughTheLog(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	s, eng, al, got := loggedAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false}, &lead)
	eng.Evaluate()
	if rec := do(t, s, "POST", "/api/alarms/out-of-service", `{"equipment":"HiTempAlm","seconds":600,"reason":"probe swap","by":"op"}`); rec.Code != http.StatusOK {
		t.Fatalf("out of service: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, s, "POST", "/api/alarms/out-of-service", `{"equipment":"HiTempAlm","seconds":600}`); rec.Code != http.StatusBadRequest || len(*got) != 1 {
		t.Fatalf("no reason: %d, %d proposed; want 400 and nothing", rec.Code, len(*got))
	}
	e := (*got)[0]
	if e.Kind != oplog.KindOutOfService || e.ID != "HiTempAlm" || e.Reason != "probe swap" || e.By != "op" || e.Until == 0 {
		t.Fatalf("proposed %+v", e)
	}
	if st := state(eng, "HiTemp"); st != alarm.Suppressed {
		t.Fatalf("HiTemp %s after its committed out of service", st)
	}
	if h := al.Held(true, nil)["HiTemp"]; h.OOSEquipment != "HiTempAlm" || h.OOSReason != "probe swap" || h.OOSUntilMs != e.Until {
		t.Fatalf("view %+v", h)
	}
	if rec := do(t, s, "POST", "/api/alarms/in-service", `{"equipment":"HiTempAlm","by":"op"}`); rec.Code != http.StatusOK {
		t.Fatalf("in service: %d %s", rec.Code, rec.Body)
	}
	if (*got)[1].Kind != oplog.KindInService || (*got)[1].ID != "HiTempAlm" {
		t.Fatalf("proposed %+v", (*got)[1])
	}
	if h := al.Held(true, nil)["HiTemp"]; h.OOSEquipment != "" {
		t.Fatalf("view after in-service %+v", h)
	}
}

// A standby keeps the window in its view; a synced takeover restores it,
// an unsynced one does not (a stale window would hide alarms) and says so.
func TestOutOfServiceAtATakeover(t *testing.T) {
	for _, synced := range []bool{true, false} {
		var lead atomic.Bool // a standby
		_, eng, al, _ := loggedAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false}, &lead)
		eng.Evaluate()
		until := time.Now().Add(time.Hour).UnixMilli()
		if _, err := al.Apply(oplog.Entry{Kind: oplog.KindOutOfService, ID: "HiTempAlm", Until: until, TS: time.Now().UnixMilli(), By: "op", Reason: "probe swap", Seq: 7}); err != nil {
			t.Fatal(err)
		}
		if st := state(eng, "HiTemp"); st != alarm.UnackActive {
			t.Fatalf("the standby's engine moved to %s", st)
		}
		lead.Store(true)
		PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, synced)
		eng.Evaluate()
		st := state(eng, "HiTemp")
		evs, _ := eng.Journal(time.Now().Add(-time.Hour), time.Now().Add(time.Hour), alarm.Filter{})
		noted := slices.ContainsFunc(evs, func(e alarm.Event) bool { return strings.Contains(e.Name, "out of service not carried") })
		switch {
		case synced && (st != alarm.Suppressed || noted):
			t.Errorf("synced: HiTemp %s, noted %v; want suppressed, no note", st, noted)
		case !synced && (st != alarm.UnackActive || !noted):
			t.Errorf("unsynced: HiTemp %s, noted %v; want unack-active and the note", st, noted)
		}
	}
}

// What an unsynced takeover did not carry, the leader ends in the log
// too, so a later takeover never brings back a hide nobody sees now.
func TestReconcileEndsAWindowTheEngineDoesNotHold(t *testing.T) {
	var lead atomic.Bool
	_, eng, al, got := loggedAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false}, &lead)
	eng.Evaluate()
	if _, err := al.Apply(oplog.Entry{Kind: oplog.KindOutOfService, ID: "HiTempAlm", Until: time.Now().Add(time.Hour).UnixMilli(), TS: time.Now().UnixMilli(), By: "op", Reason: "probe swap", Seq: 3}); err != nil {
		t.Fatal(err)
	}
	lead.Store(true)
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, false)
	al.Reconcile()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !slices.ContainsFunc(*got, func(e oplog.Entry) bool { return e.Kind == oplog.KindInService }) {
		time.Sleep(10 * time.Millisecond)
	}
	i := slices.IndexFunc(*got, func(e oplog.Entry) bool { return e.Kind == oplog.KindInService })
	if i < 0 || (*got)[i].ID != "HiTempAlm" || (*got)[i].By != "system" {
		t.Fatalf("proposed %+v; want an in-service for HiTempAlm by system", *got)
	}
	if h := al.Held(true, nil)["HiTemp"]; h.OOSEquipment != "" {
		t.Fatalf("the view kept the window: %+v", h)
	}
}

// A late out of service lapses (it would hide alarms nobody meant to); a
// return to service never does. A lapsed one never reaches the view, and
// a KindLapse naming one a replica applied drops it.
func TestOutOfServiceLapses(t *testing.T) {
	var lead atomic.Bool
	_, eng, al, _ := loggedAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false}, &lead)
	eng.Evaluate()
	old := time.Now().Add(-time.Hour)
	if !al.Lapse(oplog.Entry{Kind: oplog.KindOutOfService, TS: old.UnixMilli()}, time.Now()) {
		t.Error("a late out of service does not lapse")
	}
	if al.Lapse(oplog.Entry{Kind: oplog.KindInService, TS: old.UnixMilli()}, time.Now()) {
		t.Error("a late return to service lapses")
	}
	if al.Lapse(oplog.Entry{Kind: oplog.KindOutOfService, TS: time.Now().UnixMilli()}, time.Now()) {
		t.Error("a fresh out of service lapses")
	}
	until := time.Now().Add(time.Hour).UnixMilli()
	if _, err := al.Apply(oplog.Entry{Kind: oplog.KindOutOfService, ID: "HiTempAlm", Until: until, TS: old.UnixMilli(), By: "op", Reason: "x", Seq: 4, Lapsed: true}); err != nil {
		t.Fatal(err)
	}
	if h := al.Held(true, nil)["HiTemp"]; h.OOSEquipment != "" {
		t.Fatalf("a lapsed window reached the view: %+v", h)
	}
	if _, err := al.Apply(oplog.Entry{Kind: oplog.KindOutOfService, ID: "HiTempAlm", Until: until, TS: time.Now().UnixMilli(), By: "op", Reason: "x", Seq: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := al.Apply(oplog.Entry{Kind: oplog.KindLapse, Refs: []uint64{5}}); err != nil {
		t.Fatal(err)
	}
	if h := al.Held(true, nil)["HiTemp"]; h.OOSEquipment != "" {
		t.Fatalf("a KindLapse left the window in the view: %+v", h)
	}
}

// The new permission is a supervisor's, needs a fresh password, and is
// limited by areas.
func TestOutOfServicePermission(t *testing.T) {
	if !auth.Elevated(auth.OutOfService) || !auth.AreaScoped(auth.OutOfService) {
		t.Error("out-of-service must be elevated and area-scoped")
	}
	if actionPerm["alarm.out-of-service"] != auth.OutOfService || actionPerm["alarm.in-service"] != auth.Ack {
		t.Error("actionPerm: out-of-service needs its permission, in-service needs ack")
	}
}

// A superseded history's windows go with its acks and shelves.
func TestASupersededViewDropsItsWindows(t *testing.T) {
	var lead atomic.Bool
	_, eng, al, _ := loggedAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false}, &lead)
	eng.Evaluate()
	if _, err := al.Apply(oplog.Entry{Kind: oplog.KindOutOfService, ID: "HiTempAlm", Until: time.Now().Add(time.Hour).UnixMilli(), TS: time.Now().UnixMilli(), By: "op", Reason: "x", Seq: 2}); err != nil {
		t.Fatal(err)
	}
	al.dropView()
	if h := al.Held(true, nil)["HiTemp"]; h.OOSEquipment != "" {
		t.Fatalf("a dropped view kept its window: %+v", h)
	}
}
