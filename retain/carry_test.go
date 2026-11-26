package retain

import (
	"reflect"
	"testing"
)

// carried and kept classify every field of State for a pair's standby
// (#710 S1, design §4.1): what Carry takes from the leader's image, and
// what stays the standby's own. A field added to State fails
// TestCarryClassifiesEveryField until it is put in one of them.
var (
	carried = []string{"Tags", "ParamBases", "ParamEdits", "ParamCommitted", "Locals", "Programs", "ProgramBases", "DiscardedPrograms", "Push", "Failback"}
	kept    = []string{"Alarms", "AlarmFlood", "Substitutes", "IO", "Stream"}
)

func TestCarryClassifiesEveryField(t *testing.T) {
	known := map[string]bool{}
	for _, f := range append(append([]string(nil), carried...), kept...) {
		if known[f] {
			t.Fatalf("%s is listed twice", f)
		}
		known[f] = true
	}
	ty := reflect.TypeOf(State{})
	for i := range ty.NumField() {
		if f := ty.Field(i).Name; !known[f] {
			t.Errorf("State.%s is neither carried nor kept by a pair's standby: decide, in Carry and here", f)
		}
		delete(known, ty.Field(i).Name)
	}
	for f := range known {
		t.Errorf("%s is not a field of State", f)
	}
}

// Carry takes every carried field from the leader, absent included, and
// keeps every other field as the standby had it: the operator intent its
// replicated log owns (acks, shelves, flood, substitutes), and the live
// I/O a pair refuses.
func TestCarry(t *testing.T) {
	own := State{
		Tags:              map[string]any{"SP": 1.0, "Gone": 2.0},
		ParamBases:        map[string]any{"SP": 1.0},
		ParamEdits:        map[string]ParamEdit{"SP": {By: "own"}},
		ParamCommitted:    map[string]any{"SP": 1.0},
		Locals:            map[string]map[string]any{"main": {"n": 1.0}},
		Programs:          map[string]string{"main": "own edit"},
		ProgramBases:      map[string]string{"main": "own base"},
		DiscardedPrograms: map[string]string{"main": "own discarded"},
		Push:              &PushState{Rows: []PushRow{{ID: "own"}}},
		Failback:          &FailbackState{Count: 1},
		Alarms:            map[string]AlarmRetain{"A1": {Acked: true, AckBy: "own"}},
		AlarmFlood:        &AlarmFlood{ID: "own"},
		Substitutes:       &SubstituteState{HighID: 7},
		IO:                &IOState{Units: map[string]IOUnit{"own": {}}},
		Stream:            &StreamPos{Term: 4, Seq: 2},
	}
	lead := State{
		Tags:        map[string]any{"SP": 3.0},
		ParamBases:  map[string]any{"SP": 0.0},
		ParamEdits:  map[string]ParamEdit{"SP": {By: "lead"}},
		Locals:      map[string]map[string]any{"main": {"n": 9.0}},
		Push:        &PushState{Rows: []PushRow{{ID: "lead"}}},
		Failback:    &FailbackState{Suspended: true, By: "user:op"},
		Alarms:      map[string]AlarmRetain{"A1": {Acked: false}},
		AlarmFlood:  &AlarmFlood{ID: "lead"},
		Substitutes: &SubstituteState{HighID: 99},
		IO:          &IOState{Units: map[string]IOUnit{"lead": {}}},
		Stream:      &StreamPos{Term: 5},
		// no online edits, no ParamCommitted: the standby's must go too
	}
	got := Carry(own, lead)
	g, o, l := reflect.ValueOf(got), reflect.ValueOf(own), reflect.ValueOf(lead)
	for _, f := range carried {
		if !reflect.DeepEqual(g.FieldByName(f).Interface(), l.FieldByName(f).Interface()) {
			t.Errorf("%s: got %v, want the leader's %v", f, g.FieldByName(f).Interface(), l.FieldByName(f).Interface())
		}
	}
	for _, f := range kept {
		if !reflect.DeepEqual(g.FieldByName(f).Interface(), o.FieldByName(f).Interface()) {
			t.Errorf("%s: got %v, want the standby's own %v", f, g.FieldByName(f).Interface(), o.FieldByName(f).Interface())
		}
	}
}
