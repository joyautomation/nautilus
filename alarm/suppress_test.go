package alarm

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// Suppression by state (#715 PR 5): out of service, suppress-when, and a
// substituted signal. Every rule fails toward showing the alarm.

func pumpRig(t *testing.T, extra ...Def) *rig {
	t.Helper()
	defs := append([]Def{def("P1.Fault", "Pump_1.Fault"), def("P1.Overload", "Pump_1.Overload"), def("P2.Fault", "Pump_2.Fault")}, extra...)
	r := newRig(t, defs...)
	r.tags["Pump_1.Fault"], r.tags["Pump_1.Overload"], r.tags["Pump_2.Fault"] = false, false, false
	r.Evaluate()
	return r
}

// systemRows is the journal's system rows of kind, oldest first.
func (r *rig) systemRows(t *testing.T, kind string) []Event {
	t.Helper()
	evs, err := r.Journal(time.Time{}, time.Time{}, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].ID == SystemAlarmsID && evs[i].Kind == kind {
			out = append(out, evs[i])
		}
	}
	return out
}

func TestEquipmentDefaultsToTheConditionsTag(t *testing.T) {
	r := pumpRig(t, def("Flat", "LSH_101"), def("Own", "Pump_1.Seal"), Def{ID: "nautilus.audit-journal", Tag: "__nautilus.audit-journal-down", AckRequired: true})
	r.tags["Pump_1.Seal"] = false
	for id, want := range map[string]string{"P1.Fault": "Pump_1", "P1.Overload": "Pump_1", "P2.Fault": "Pump_2", "Flat": "LSH_101", "nautilus.audit-journal": "nautilus.audit-journal"} {
		if got := r.rec(t, id).Equipment; got != want {
			t.Errorf("%s: equipment %q, want %q", id, got, want)
		}
	}
	d := def("Named", "Pump_3.Fault")
	d.Equipment = "Skid_A"
	r2 := newRig(t, d)
	if got := r2.rec(t, "Named").Equipment; got != "Skid_A" {
		t.Errorf("an explicit equipment: %q, want Skid_A", got)
	}
}

func TestOutOfServiceHidesEveryAlarmOfTheEquipmentAndNoOther(t *testing.T) {
	r := pumpRig(t)
	r.tags["Pump_1.Fault"], r.tags["Pump_2.Fault"] = true, true
	r.Evaluate()
	until := r.clk.at(4 * time.Hour)
	if err := r.TakeOutOfService("Pump_1", until, "alice", "seal change"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"P1.Fault", "P1.Overload"} {
		rec := r.rec(t, id)
		if rec.State != Suppressed || rec.Suppression != SuppressOutOfService || rec.SuppressedBy != "alice" ||
			rec.SuppressUntilMs != until.UnixMilli() || !strings.Contains(rec.Reason, "seal change") {
			t.Errorf("%s: %+v, want suppressed out of service by alice until %v with the reason", id, rec, until)
		}
	}
	r.Evaluate()
	if st := r.state(t, "P1.Fault"); st != Suppressed {
		t.Errorf("P1.Fault after a scan with its condition true: %v, want suppressed", st)
	}
	if st := r.state(t, "P2.Fault"); st != UnackActive {
		t.Errorf("P2.Fault (other equipment): %v, want unack-active", st)
	}
	if got := r.systemRows(t, KindOutOfService); len(got) != 1 || got[0].By != "alice" || !strings.Contains(got[0].Name, "Pump_1") {
		t.Errorf("journal: %+v, want one out-of-service row by alice naming Pump_1", got)
	}
	evs, _ := r.Journal(time.Time{}, time.Time{}, Filter{IDs: []string{"P1.Fault"}})
	if len(evs) == 0 || evs[0].Kind != KindSuppress || evs[0].By != "alice" {
		t.Errorf("P1.Fault's newest row %+v, want a suppress by alice", evs)
	}
}

func TestOutOfServiceRefusals(t *testing.T) {
	r := pumpRig(t, Def{ID: "nautilus.users-file", Tag: "__nautilus.users-file-down", AckRequired: true, Equipment: "Shared"},
		Def{ID: "Mine", Tag: "Pump_9.Fault", Equipment: "Shared", AckRequired: true})
	now := r.clk.now()
	for _, c := range []struct {
		name, eq, reason string
		until            time.Time
		want             string
	}{
		{"past", "Pump_1", "x", now.Add(-time.Second), "not in the future"},
		{"now", "Pump_1", "x", now, "not in the future"},
		{"over a week", "Pump_1", "x", now.Add(MaxOutOfService + time.Minute), "at most"},
		{"no reason", "Pump_1", "  ", now.Add(time.Hour), "needs a reason"},
		{"two lines", "Pump_1", "a\nb", now.Add(time.Hour), "one line"},
		{"too long", "Pump_1", strings.Repeat("x", MaxOutOfServiceReason+1), now.Add(time.Hour), "longer than"},
		{"no such equipment", "Pump_7", "x", now.Add(time.Hour), "no alarm belongs"},
		{"a system alarm", "Shared", "x", now.Add(time.Hour), "never taken out of service"},
	} {
		err := r.TakeOutOfService(c.eq, c.until, "alice", c.reason)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error containing %q", c.name, err, c.want)
		}
		if cerr := r.CheckOutOfService(c.eq, c.until, c.reason); (cerr == nil) != (err == nil) {
			t.Errorf("%s: CheckOutOfService %v disagrees with TakeOutOfService %v", c.name, cerr, err)
		}
	}
	if err := r.TakeOutOfService("Pump_7", now.Add(time.Hour), "a", "x"); !errors.Is(err, ErrNoEquipment) {
		t.Errorf("unknown equipment: %v, want ErrNoEquipment", err)
	}
	if err := r.TakeOutOfService("Pump_1", now.Add(MaxOutOfService), "alice", "a week"); err != nil {
		t.Errorf("exactly a week: %v", err)
	}
	if len(r.OutOfServiceList()) != 1 {
		t.Errorf("list %+v, want only the accepted one", r.OutOfServiceList())
	}
}

// An expired window ends in the pass that passes it, journalled, and a
// condition still present annunciates at once, as a new unacknowledged
// occurrence: the ack from before does not carry.
func TestOutOfServiceExpiresAndReannunciates(t *testing.T) {
	r := pumpRig(t)
	r.tags["Pump_1.Fault"] = true
	r.Evaluate()
	before := r.rec(t, "P1.Fault").Occurrence
	if _, err := r.Ack([]string{"P1.Fault"}, "op"); err != nil {
		t.Fatal(err)
	}
	if err := r.TakeOutOfService("Pump_1", r.clk.at(time.Hour), "alice", "seal change"); err != nil {
		t.Fatal(err)
	}
	r.clk.advance(time.Hour - time.Millisecond)
	r.Evaluate()
	if st := r.state(t, "P1.Fault"); st != Suppressed {
		t.Fatalf("a millisecond before the end: %v, want suppressed", st)
	}
	r.clk.advance(time.Millisecond)
	r.Evaluate()
	rec := r.rec(t, "P1.Fault")
	if rec.State != UnackActive || rec.Occurrence == before || rec.Occurrence == "" || rec.AckBy != "" {
		t.Errorf("at the end: %+v, want unack-active on a new occurrence, no ack", rec)
	}
	if rec.Suppression != "" || rec.SuppressedBy != "" || rec.SuppressUntilMs != 0 {
		t.Errorf("suppression fields left on a live alarm: %+v", rec)
	}
	rows := r.systemRows(t, KindInService)
	if len(rows) != 1 || rows[0].By != "system" || !strings.Contains(rows[0].Name, "returned to service: expired") {
		t.Errorf("journal %+v, want one in-service row by system, expired", rows)
	}
	if len(r.OutOfServiceList()) != 0 {
		t.Errorf("list after expiry: %+v", r.OutOfServiceList())
	}
}

func TestReturnToServiceEndsTheWindowEarly(t *testing.T) {
	r := pumpRig(t)
	r.tags["Pump_1.Overload"] = true
	if err := r.TakeOutOfService("Pump_1", r.clk.at(time.Hour), "alice", "seal change"); err != nil {
		t.Fatal(err)
	}
	r.Evaluate()
	if err := r.CheckInService("Pump_2"); err == nil {
		t.Error("CheckInService of equipment in service: no error")
	}
	if err := r.ReturnToService("Pump_2", "bob"); err == nil {
		t.Error("ReturnToService of equipment in service: no error")
	}
	if err := r.ReturnToService("Pump_1", "bob"); err != nil {
		t.Fatal(err)
	}
	r.Evaluate()
	if st := r.state(t, "P1.Overload"); st != UnackActive {
		t.Errorf("after return to service: %v, want unack-active", st)
	}
	if st := r.state(t, "P1.Fault"); st != Normal {
		t.Errorf("P1.Fault: %v, want normal", st)
	}
	if rows := r.systemRows(t, KindInService); len(rows) != 1 || rows[0].By != "bob" {
		t.Errorf("journal %+v, want one in-service row by bob", rows)
	}
}

func TestOutOfServiceListIsSoonestFirst(t *testing.T) {
	r := pumpRig(t, def("F", "Fan.Fault"))
	r.tags["Fan.Fault"] = false
	for eq, d := range map[string]time.Duration{"Pump_1": 3 * time.Hour, "Pump_2": time.Hour, "Fan": 2 * time.Hour} {
		if err := r.TakeOutOfService(eq, r.clk.at(d), "alice", "work on "+eq); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, o := range r.OutOfServiceList() {
		got = append(got, o.Equipment)
	}
	if strings.Join(got, ",") != "Pump_2,Fan,Pump_1" {
		t.Errorf("order %v, want Pump_2,Fan,Pump_1", got)
	}
	if l := r.OutOfServiceList(); strings.Join(l[2].Alarms, ",") != "P1.Fault,P1.Overload" || l[2].Reason != "work on Pump_1" || l[2].By != "alice" {
		t.Errorf("Pump_1 row %+v", l[2])
	}
}

// A restart keeps a window (retain), never longer than was granted (a
// clock set back), never one that has ended, never on an alarm that moved
// to other equipment.
func TestOutOfServiceSurvivesARestartAndNeverGrows(t *testing.T) {
	r := pumpRig(t)
	if err := r.TakeOutOfService("Pump_1", r.clk.at(2*time.Hour), "alice", "seal change"); err != nil {
		t.Fatal(err)
	}
	saved := r.RetainedAlarms()
	if got := saved["P1.Fault"]; got.OOSEquipment != "Pump_1" || got.OOSBy != "alice" || got.OOSReason != "seal change" || got.OOSLeftMs != (2*time.Hour).Milliseconds() {
		t.Fatalf("retained %+v", got)
	}
	restart := func(at time.Time, defs ...Def) *rig {
		n := newRig(t, defs...)
		n.clk.t = at
		n.tags["Pump_1.Fault"], n.tags["Pump_1.Overload"], n.tags["Pump_2.Fault"] = true, true, false
		n.RestoreAlarms(saved)
		n.Evaluate()
		return n
	}
	defs := []Def{def("P1.Fault", "Pump_1.Fault"), def("P1.Overload", "Pump_1.Overload"), def("P2.Fault", "Pump_2.Fault")}

	n := restart(r.clk.at(time.Hour), defs...)
	if rec := n.rec(t, "P1.Fault"); rec.State != Suppressed || rec.SuppressUntilMs != r.clk.at(2*time.Hour).UnixMilli() {
		t.Errorf("an hour on: %+v, want suppressed to the same end", rec)
	}

	// The clock set back a day: the window ends no later than its grant.
	back := r.clk.at(-24 * time.Hour)
	n = restart(back, defs...)
	if rec := n.rec(t, "P1.Fault"); rec.State != Suppressed || rec.SuppressUntilMs != back.Add(2*time.Hour).UnixMilli() {
		t.Errorf("clock set back: until %v, want %v (now + granted)", time.UnixMilli(rec.SuppressUntilMs), back.Add(2*time.Hour))
	}

	n = restart(r.clk.at(3*time.Hour), defs...)
	if st := n.state(t, "P1.Fault"); st != UnackActive {
		t.Errorf("after its end: %v, want unack-active", st)
	}
	if rows := n.systemRows(t, KindInService); len(rows) != 0 {
		t.Errorf("a window that ended before the restart was restored (and expired): %+v", rows)
	}

	moved := def("P1.Fault", "Pump_1.Fault")
	moved.Equipment = "Skid_A"
	n = restart(r.clk.at(time.Hour), moved, def("P1.Overload", "Pump_1.Overload"))
	if st := n.state(t, "P1.Fault"); st != UnackActive {
		t.Errorf("an alarm moved to other equipment: %v, want unack-active", st)
	}
	if st := n.state(t, "P1.Overload"); st != Suppressed {
		t.Errorf("the one that stayed: %v, want suppressed", st)
	}
}

func TestResetOperatorStateForgetsOutOfService(t *testing.T) {
	r := pumpRig(t)
	if err := r.TakeOutOfService("Pump_1", r.clk.at(time.Hour), "alice", "seal change"); err != nil {
		t.Fatal(err)
	}
	r.ResetOperatorState()
	r.tags["Pump_1.Fault"] = true
	r.Evaluate()
	if st := r.state(t, "P1.Fault"); st != UnackActive {
		t.Errorf("after a reset: %v, want unack-active", st)
	}
	if len(r.OutOfServiceList()) != 0 {
		t.Errorf("list after a reset: %+v", r.OutOfServiceList())
	}
}

func TestSuppressWhenSuppressesOnlyOnAReadableTrue(t *testing.T) {
	d := def("LowFlow", "P1.LowFlow")
	d.SuppressWhen = "P1.Stopped"
	withReason := def("LowPress", "P1.LowPress")
	withReason.SuppressWhen, withReason.SuppressReason = "P1.Stopped", "pump 1 is stopped"
	r := newRig(t, d, withReason)
	r.tags["P1.LowFlow"], r.tags["P1.LowPress"] = true, true

	r.tags["P1.Stopped"] = true
	r.Evaluate()
	if rec := r.rec(t, "LowFlow"); rec.State != Suppressed || rec.Suppression != SuppressDesigned || rec.Reason != "P1.Stopped is true" || rec.Warning != "" {
		t.Errorf("stopped: %+v, want suppressed by design, reason P1.Stopped is true", rec)
	}
	if rec := r.rec(t, "LowPress"); rec.Reason != "pump 1 is stopped" {
		t.Errorf("a suppress-reason: %q", rec.Reason)
	}

	r.tags["P1.Stopped"] = false
	r.Evaluate()
	if st := r.state(t, "LowFlow"); st != UnackActive {
		t.Errorf("running: %v, want unack-active", st)
	}

	// Fails toward showing: unreadable suppresses nothing, and says so.
	for _, c := range []struct {
		name string
		v    any
		want string
	}{{"absent", nil, "is not in this project"}, {"not a BOOL", "yes", "is not a BOOL"}} {
		if c.v == nil {
			delete(r.tags, "P1.Stopped")
		} else {
			r.tags["P1.Stopped"] = c.v
		}
		r.Evaluate()
		rec := r.rec(t, "LowFlow")
		if rec.State != UnackActive || !strings.Contains(rec.Warning, c.want) {
			t.Errorf("%s: %+v, want shown (unack-active) with a warning containing %q", c.name, rec, c.want)
		}
	}
	r.tags["P1.Stopped"] = false
	r.Evaluate()
	if rec := r.rec(t, "LowFlow"); rec.Warning != "" {
		t.Errorf("warning left once readable: %q", rec.Warning)
	}
}

// A designed suppression ends the occurrence: the ack does not survive
// it, and a condition still present afterwards is a new one.
func TestSuppressWhenEndsTheOccurrence(t *testing.T) {
	d := def("LowFlow", "P1.LowFlow")
	d.SuppressWhen = "P1.Stopped"
	r := newRig(t, d)
	r.tags["P1.LowFlow"], r.tags["P1.Stopped"] = true, false
	r.Evaluate()
	first := r.rec(t, "LowFlow").Occurrence
	if _, err := r.Ack(nil, "op"); err != nil {
		t.Fatal(err)
	}
	r.tags["P1.Stopped"] = true
	r.Evaluate()
	r.tags["P1.Stopped"] = false
	r.Evaluate()
	rec := r.rec(t, "LowFlow")
	if rec.State != UnackActive || rec.Occurrence == first {
		t.Errorf("after the suppression: %+v, want unack-active on a new occurrence", rec)
	}
}

func TestASubstitutedSignalSuppressesItsAlarm(t *testing.T) {
	subs := map[string]string{}
	clk, tg := newClock(), tags{"LSH_101": true, "TT_101.HH": true}
	e, err := New(Options{Defs: []Def{def("LSH", "LSH_101"), def("TTHH", "TT_101.HH")}, Read: tg.read, Now: clk.now,
		Substituted: func(p string) (string, bool) { a, ok := subs[p]; return a, ok }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	r := &rig{Engine: e, clk: clk, tags: tg}
	subs["LSH_101"] = "LSH_101"
	r.Evaluate()
	if rec := r.rec(t, "LSH"); rec.State != Suppressed || rec.Suppression != SuppressSubstituted || rec.Reason != "signal substituted: LSH_101" {
		t.Errorf("substituted: %+v", rec)
	}
	if st := r.state(t, "TTHH"); st != UnackActive {
		t.Errorf("the other alarm: %v, want unack-active", st)
	}
	delete(subs, "LSH_101")
	r.Evaluate()
	if st := r.state(t, "LSH"); st != UnackActive {
		t.Errorf("substitute ended: %v, want unack-active", st)
	}
}

// Out of service wins over the other suppressions, so the row says who
// hid it and until when.
func TestOutOfServiceIsTheReasonShownOverOthers(t *testing.T) {
	d := def("LowFlow", "P1.LowFlow")
	d.SuppressWhen = "P1.Stopped"
	r := newRig(t, d)
	r.tags["P1.LowFlow"], r.tags["P1.Stopped"] = true, true
	r.Evaluate()
	if err := r.TakeOutOfService("P1", r.clk.at(time.Hour), "alice", "seal change"); err != nil {
		t.Fatal(err)
	}
	r.Evaluate()
	if rec := r.rec(t, "LowFlow"); rec.Suppression != SuppressOutOfService || rec.SuppressedBy != "alice" {
		t.Errorf("%+v, want out of service by alice", rec)
	}
}

func TestRulesTemplateSuppressWhenAndEquipment(t *testing.T) {
	rules := []Rule{{Match: Match{Type: "AnalogInput", Member: "HH"}, SuppressWhen: "{tag}.LOS", SuppressReason: "{desc} lost signal"},
		{Match: Match{Type: "AnalogInput", Member: "LL"}, Equipment: "{site}_skid"}}
	got, err := Expand(rules, nil, fleet())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Def{}
	for _, d := range got {
		by[d.ID] = d
	}
	hh := by["RTU9_FIT_001.HH"]
	if hh.SuppressWhen != "RTU9_FIT_001.LOS" || hh.SuppressReason != "RTU9 FIT_001 lost signal" || hh.Equipment != "RTU9_FIT_001" {
		t.Errorf("HH: %+v", hh)
	}
	if ll := by["RTU12_PIT_002.LL"]; ll.Equipment != "RTU12_skid" {
		t.Errorf("LL equipment %q, want RTU12_skid", ll.Equipment)
	}
}

// The window is retained in the alarm funnel's own shape: a pair's Held
// and a file both go through AlarmRetain.
func TestRetainedOutOfServiceIsOnEveryCoveredAlarm(t *testing.T) {
	r := pumpRig(t)
	if err := r.TakeOutOfService("Pump_1", r.clk.at(time.Hour), "alice", "seal change"); err != nil {
		t.Fatal(err)
	}
	m := r.RetainedAlarms()
	for _, id := range []string{"P1.Fault", "P1.Overload"} {
		if m[id].OOSEquipment != "Pump_1" {
			t.Errorf("%s retained %+v", id, m[id])
		}
	}
	if _, ok := m["P2.Fault"]; ok {
		t.Errorf("P2.Fault retained %+v", m["P2.Fault"])
	}
	var zero retain.AlarmRetain
	if m["P1.Fault"] == zero {
		t.Error("zero")
	}
}

// A scan with suppression in play allocates nothing: an out-of-service
// window standing, a readable suppress-when, a substitute check.
func TestEvaluateWithSuppressionAllocatesNothing(t *testing.T) {
	d := def("LowFlow", "P1.LowFlow")
	d.SuppressWhen = "P1.Stopped"
	clk, tg := newClock(), tags{"P1.LowFlow": true, "P1.Stopped": false, "Pump_1.Fault": true}
	e, err := New(Options{Defs: []Def{d, def("P1.Fault", "Pump_1.Fault")}, Read: tg.read, Now: clk.now, Journal: NewRing(16),
		Substituted: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	e.Evaluate()
	if err := e.TakeOutOfService("Pump_1", clk.at(time.Hour), "alice", "seal change"); err != nil {
		t.Fatal(err)
	}
	e.Evaluate()
	if n := testing.AllocsPerRun(100, e.Evaluate); n != 0 {
		t.Errorf("Evaluate allocates %v per run, want 0", n)
	}
	// And with the suppressions standing: suppress-when true, a substitute.
	sw := def("LowFlow", "P1.LowFlow")
	sw.SuppressWhen = "P1.Stopped"
	tg2 := tags{"P1.LowFlow": true, "P1.Stopped": true, "LSH": true}
	e2, err := New(Options{Defs: []Def{sw, def("LSH", "LSH")}, Read: tg2.read, Now: clk.now, Journal: NewRing(16),
		Substituted: func(p string) (string, bool) { return p, p == "LSH" }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e2.Close() })
	e2.Evaluate()
	if n := testing.AllocsPerRun(100, e2.Evaluate); n != 0 {
		t.Errorf("Evaluate with suppressions standing allocates %v per run, want 0", n)
	}
}

// An occurrence carried across a restart that is suppressed before its
// condition is read ends there (as enable false ends one): a condition
// present after the suppression is a new occurrence, and the carried ack
// does not ride it.
func TestSuppressionEndsACarriedOccurrence(t *testing.T) {
	for _, how := range []string{"out-of-service", "suppress-when"} {
		d := def("LowFlow", "P1.LowFlow")
		d.SuppressWhen = "P1.Stopped"
		r := newRig(t, d)
		start := r.clk.at(-time.Hour)
		r.RestoreAlarms(map[string]retain.AlarmRetain{"LowFlow": {Occurrence: "00000000000000aa", ActiveMs: start.UnixMilli(),
			Acked: true, AckBy: "op", AckMs: start.UnixMilli()}})
		r.tags["P1.LowFlow"], r.tags["P1.Stopped"] = true, false
		if how == "out-of-service" {
			if err := r.TakeOutOfService("P1", r.clk.at(time.Minute), "alice", "work"); err != nil {
				t.Fatal(err)
			}
			r.Evaluate()
			r.clk.advance(time.Minute)
		} else {
			r.tags["P1.Stopped"] = true
			r.Evaluate()
			r.tags["P1.Stopped"] = false
		}
		r.Evaluate()
		rec := r.rec(t, "LowFlow")
		if rec.State != UnackActive || rec.Occurrence == "00000000000000aa" || rec.AckBy != "" {
			t.Errorf("%s: %+v, want unack-active on a new occurrence, not the carried ack", how, rec)
		}
	}
}

// A retained window for equipment that no longer holds any alarm (every
// one moved, or was removed from the manifest) is not restored: nothing
// lists a window that hides nothing.
func TestARetainedWindowForGoneEquipmentIsDropped(t *testing.T) {
	r := pumpRig(t)
	r.RestoreAlarms(map[string]retain.AlarmRetain{"P1.Fault": {OOSEquipment: "Pump_9", OOSUntilMs: r.clk.at(time.Hour).UnixMilli(),
		OOSLeftMs: time.Hour.Milliseconds(), OOSBy: "alice", OOSReason: "gone"}})
	r.Evaluate()
	if l := r.OutOfServiceList(); len(l) != 0 {
		t.Errorf("list %+v, want nothing", l)
	}
	if st := r.state(t, "P1.Fault"); st == Suppressed {
		t.Error("P1.Fault suppressed by another equipment's window")
	}
}

// Only a BOOL's TRUE suppresses: a number (a speed, a status word) named
// as suppress-when never hides the alarm, nor does a suppress-when that is
// itself substituted (Fable review of #1222).
func TestSuppressWhenNeedsABoolThatIsNotSubstituted(t *testing.T) {
	d := def("LowFlow", "P1.LowFlow")
	d.SuppressWhen = "P1.Speed"
	subs := map[string]bool{}
	clk, tg := newClock(), tags{"P1.LowFlow": true}
	e, err := New(Options{Defs: []Def{d}, Read: tg.read, Now: clk.now,
		Substituted: func(p string) (string, bool) { return p, subs[p] }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	r := &rig{Engine: e, clk: clk, tags: tg}
	for _, v := range []any{int64(3), 1.5, int(1), ir.Value{Kind: ir.TypeInt, I: 7}, ir.Value{Kind: ir.TypeReal, F: 2}} {
		tg["P1.Speed"] = v
		r.Evaluate()
		if rec := r.rec(t, "LowFlow"); rec.State == Suppressed || !strings.Contains(rec.Warning, "is not a BOOL") {
			t.Errorf("suppress-when reading %#v: %+v, want shown with a not-a-BOOL warning", v, rec)
		}
	}
	tg["P1.Speed"] = ir.Value{Kind: ir.TypeBool, B: true}
	r.Evaluate()
	if st := r.state(t, "LowFlow"); st != Suppressed {
		t.Errorf("a BOOL ir.Value TRUE: %v, want suppressed", st)
	}
	subs["P1.Speed"] = true
	r.Evaluate()
	if rec := r.rec(t, "LowFlow"); rec.State == Suppressed || !strings.Contains(rec.Warning, "is substituted") {
		t.Errorf("a substituted suppress-when: %+v, want shown with a warning", rec)
	}
}

// A clock set back while a window runs ends it (beyond ClockSlack), and
// a small step back keeps it; a window never outlasts what was left of it.
func TestAClockSetBackNeverLengthensAWindow(t *testing.T) {
	r := pumpRig(t)
	if err := r.TakeOutOfService("Pump_1", r.clk.at(time.Hour), "alice", "seal change"); err != nil {
		t.Fatal(err)
	}
	r.clk.advance(-30 * time.Second)
	r.Evaluate()
	if len(r.OutOfServiceList()) != 1 {
		t.Fatal("a 30 s step back ended the window")
	}
	r.clk.advance(30*time.Second + 40*time.Minute) // 40 min on
	r.Evaluate()
	r.clk.advance(-2 * time.Hour) // set back two hours
	r.Evaluate()
	if len(r.OutOfServiceList()) != 0 {
		t.Fatalf("a clock set back two hours left %+v", r.OutOfServiceList())
	}
	rows := r.systemRows(t, KindInService)
	if len(rows) != 1 || !strings.Contains(rows[0].Name, "clock set back") {
		t.Errorf("journal %+v, want returned to service: clock set back", rows)
	}
}

// A restart restores no more than was left when the window was saved,
// whatever the clock says (Fable review: the full grant was restored).
func TestARestartRestoresWhatWasLeft(t *testing.T) {
	r := pumpRig(t)
	if err := r.TakeOutOfService("Pump_1", r.clk.at(time.Hour), "alice", "seal change"); err != nil {
		t.Fatal(err)
	}
	r.clk.advance(50 * time.Minute)
	saved := r.RetainedAlarms()
	if got := saved["P1.Fault"].OOSLeftMs; got != (10 * time.Minute).Milliseconds() {
		t.Fatalf("saved %d ms left, want 10 min", got)
	}
	n := newRig(t, def("P1.Fault", "Pump_1.Fault"), def("P1.Overload", "Pump_1.Overload"))
	n.clk.t = r.clk.at(-50 * time.Minute) // the clock set back 50 minutes
	n.tags["Pump_1.Fault"], n.tags["Pump_1.Overload"] = true, true
	n.RestoreAlarms(saved)
	n.Evaluate()
	if l := n.OutOfServiceList(); len(l) != 1 || l[0].UntilMs != n.clk.at(10*time.Minute).UnixMilli() {
		t.Fatalf("restored %+v, want it to end 10 min from now", l)
	}
}

// An ack retained from before a restart (no occurrence: an old store)
// does not ride an out-of-service window or a designed suppression to the
// next activation.
func TestARetainedAckDoesNotSurviveASuppression(t *testing.T) {
	for _, how := range []string{"out-of-service", "suppress-when"} {
		d := def("P1.Fault", "Pump_1.Fault")
		d.SuppressWhen = "Pump_1.Stopped"
		r := newRig(t, d)
		r.RestoreAlarms(map[string]retain.AlarmRetain{"P1.Fault": {Acked: true, AckBy: "op", AckMs: r.clk.now().UnixMilli()}})
		r.tags["Pump_1.Fault"], r.tags["Pump_1.Stopped"] = true, false
		if how == "out-of-service" {
			if err := r.TakeOutOfService("Pump_1", r.clk.at(time.Minute), "alice", "work"); err != nil {
				t.Fatal(err)
			}
			r.Evaluate()
			r.clk.advance(time.Minute)
		} else {
			r.tags["Pump_1.Stopped"] = true
			r.Evaluate()
			r.tags["Pump_1.Stopped"] = false
		}
		r.Evaluate()
		if rec := r.rec(t, "P1.Fault"); rec.State != UnackActive || rec.AckBy != "" {
			t.Errorf("%s: %+v, want unack-active, no ack", how, rec)
		}
	}
}

// Equipment names compare as tag names do: case aside.
func TestEquipmentNamesIgnoreCase(t *testing.T) {
	r := newRig(t, def("A", "p101.Fault"), def("B", "P101.Overload"))
	r.tags["p101.Fault"], r.tags["P101.Overload"] = true, true
	if err := r.TakeOutOfService("P101", r.clk.at(time.Hour), "alice", "work"); err != nil {
		t.Fatal(err)
	}
	r.Evaluate()
	for _, id := range []string{"A", "B"} {
		if st := r.state(t, id); st != Suppressed {
			t.Errorf("%s: %v, want suppressed", id, st)
		}
	}
	if err := r.ReturnToService("p101", "bob"); err != nil {
		t.Errorf("in service by another case: %v", err)
	}
}
