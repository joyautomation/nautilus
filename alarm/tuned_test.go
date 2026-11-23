package alarm

import (
	"strings"
	"testing"
	"time"
)

// tunedRig is a rig whose engine was built with a tuned alarm.
func tunedRig(t *testing.T, tuned []Tuned, defs ...Def) *rig {
	t.Helper()
	clk, tg, ring := newClock(), tags{}, NewRing(100)
	e, err := New(Options{Defs: defs, Read: tg.read, Now: clk.now, Journal: ring, Tuned: tuned})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return &rig{Engine: e, clk: clk, tags: tg, ring: ring}
}

func (r *rig) tunedRows(t *testing.T) []Event {
	t.Helper()
	evs, err := r.Journal(time.Time{}, time.Time{}, Filter{Kinds: []string{KindTuned}})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// One row at the first Evaluate, none on a steady scan, one more after a
// reset (a takeover's), and none from an engine that never evaluates (a
// standby): the row says under which delays each leadership ran.
func TestTunedRowAtTheFirstEvaluateOfEachLeadership(t *testing.T) {
	d := def("A", "A")
	d.OnDelay, d.OffDelay = 5*time.Second, 2*time.Second
	r := tunedRig(t, []Tuned{{ID: "A", Why: " seal weeping "}}, d, def("B", "B"))

	if n := len(r.tunedRows(t)); n != 0 {
		t.Fatalf("%d tuned rows before any Evaluate (a standby never evaluates), want none", n)
	}
	r.Evaluate()
	rows := r.tunedRows(t)
	if len(rows) != 1 {
		t.Fatalf("%d tuned rows after the first Evaluate, want 1", len(rows))
	}
	row := rows[0]
	if row.ID != "A" || row.Kind != KindTuned || row.By != "system" || row.Reason != "seal weeping" ||
		row.Name != "tuned: on-delay 5s, off-delay 2s" || row.Priority != Medium {
		t.Fatalf("row = %+v, want the alarm's own row with its delays and the trimmed why", row)
	}
	for i := 0; i < 5; i++ {
		r.clk.advance(time.Second)
		r.Evaluate()
	}
	if n := len(r.tunedRows(t)); n != 1 {
		t.Fatalf("%d tuned rows after steady scans, want still 1", n)
	}
	r.ResetOperatorState()
	r.Evaluate()
	if n := len(r.tunedRows(t)); n != 2 {
		t.Fatalf("%d tuned rows after a reset and a scan, want 2", n)
	}
}

// The row is journal-only: no notifier, no OnEvent, and a query that does not
// name it does not see it.
func TestTunedRowReachesNoOneButTheJournal(t *testing.T) {
	r := tunedRig(t, []Tuned{{ID: "A", Why: "x"}}, def("A", "A"))
	var seen []string
	r.OnEvent(func(e Event) {
		if e.Kind == KindTuned {
			seen = append(seen, e.Kind)
		}
	})
	r.tags["A"] = false
	r.Evaluate()
	if len(seen) != 0 {
		t.Fatalf("OnEvent saw %v for a tuned row, want nothing", seen)
	}
	if len(r.tunedRows(t)) != 1 {
		t.Fatal("no tuned row")
	}
}

func TestTunedNamingNoAlarmIsRefused(t *testing.T) {
	_, err := New(Options{Defs: []Def{def("A", "A")}, Read: tags{}.read, Tuned: []Tuned{{ID: "Z", Why: "x"}}})
	if err == nil || !strings.Contains(err.Error(), `"Z"`) {
		t.Fatalf("err = %v, want a refusal naming Z", err)
	}
}
