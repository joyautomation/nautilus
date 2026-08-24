package runtime_test

import (
	"sort"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// ChangedSince is the delta mechanism the SSE stream is built on: a client's
// entire subscription state is the uint64 it hands back here. These pin the
// three properties the stream's correctness rests on — every change after
// gen is reported, nothing else is, and the returned generation is the one
// to ask with next time.

func names(cs []runtime.Change) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}

func eq(t *testing.T, got, want []string, what string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
}

func TestChangedSinceZeroIsEverything(t *testing.T) {
	tags := runtime.NewTags()
	tags.SetReal("A", 1)
	tags.SetReal("B", 2)
	tags.SetBool("C", true)

	cs, gen := tags.ChangedSince(0, nil)
	eq(t, names(cs), []string{"A", "B", "C"}, "ChangedSince(0)")
	if gen != tags.Generation() {
		t.Errorf("returned gen %d != Generation() %d", gen, tags.Generation())
	}
	// The values come with, so a consumer needs no second read.
	for _, c := range cs {
		if c.Name == "B" && c.Value.F != 2 {
			t.Errorf("B = %v, want 2", c.Value.F)
		}
		if c.Gen == 0 {
			t.Errorf("%s carries no generation", c.Name)
		}
	}
}

func TestChangedSinceReportsOnlyWhatMoved(t *testing.T) {
	tags := runtime.NewTags()
	tags.SetReal("A", 1)
	tags.SetReal("B", 2)
	_, gen := tags.ChangedSince(0, nil)

	// Nothing at all: the steady-state tick.
	cs, gen2 := tags.ChangedSince(gen, nil)
	if len(cs) != 0 {
		t.Errorf("quiet store reported %v", names(cs))
	}
	if gen2 != gen {
		t.Errorf("quiet store moved the generation %d → %d", gen, gen2)
	}

	// A write that stores the SAME value is not a change — the whole point
	// of write generations, and the reason a re-delivered driver reading
	// costs a delta stream nothing.
	tags.SetReal("A", 1)
	if cs, _ := tags.ChangedSince(gen, nil); len(cs) != 0 {
		t.Errorf("no-op write reported %v", names(cs))
	}

	tags.SetReal("A", 9)
	cs, gen3 := tags.ChangedSince(gen, nil)
	eq(t, names(cs), []string{"A"}, "after one change")
	if gen3 == gen {
		t.Error("a real change did not move the generation")
	}

	// Asking again from the NEW generation yields nothing: the consumer is
	// up to date, which is the state a stream spends most of its life in.
	if cs, _ := tags.ChangedSince(gen3, nil); len(cs) != 0 {
		t.Errorf("caught-up consumer got %v", names(cs))
	}
	// ...but asking from the OLD one still yields A, because a dropped
	// frame must be recoverable by simply not advancing.
	if cs, _ := tags.ChangedSince(gen, nil); len(names(cs)) != 1 {
		t.Errorf("re-asking from the stale gen lost the change: %v", names(cs))
	}
}

func TestChangedSinceAccumulatesAcrossTicks(t *testing.T) {
	tags := runtime.NewTags()
	tags.SetReal("A", 1)
	tags.SetReal("B", 1)
	tags.SetReal("C", 1)
	_, gen := tags.ChangedSince(0, nil)

	// A client that misses two ticks must get BOTH ticks' changes in the
	// next frame, not just the last one.
	tags.SetReal("A", 2)
	tags.SetReal("B", 2)
	cs, _ := tags.ChangedSince(gen, nil)
	eq(t, names(cs), []string{"A", "B"}, "two changes since gen")
}

func TestChangedSinceReusesTheCallersBuffer(t *testing.T) {
	tags := runtime.NewTags()
	for _, n := range []string{"A", "B", "C"} {
		tags.SetReal(n, 1)
	}
	buf := make([]runtime.Change, 0, 8)
	buf, gen := tags.ChangedSince(0, buf)
	if len(buf) != 3 {
		t.Fatalf("first sweep = %d", len(buf))
	}
	before := cap(buf)
	tags.SetReal("A", 7)
	buf, _ = tags.ChangedSince(gen, buf[:0])
	if len(buf) != 1 || buf[0].Name != "A" {
		t.Fatalf("second sweep = %v", names(buf))
	}
	if cap(buf) != before {
		t.Errorf("buffer reallocated: cap %d → %d", before, cap(buf))
	}
}

// A struct tag crosses the delta seam whole, and Plain must render it
// exactly as the whole-store snapshot does — otherwise a client's merged
// state would drift in shape from a resync's.
func TestPlainMatchesAllForEveryKind(t *testing.T) {
	sd := &ir.StructDef{
		Name:       "Motor",
		Fields:     []ir.StructField{{Name: "Run"}, {Name: "Speed"}},
		FieldIndex: map[string]int{"Run": 0, "Speed": 1},
	}
	tags := runtime.NewTags()
	tags.SetReal("R", 1.5)
	tags.SetBool("B", true)
	tags.Set("I", int64(7))
	tags.Set("S", "hello")
	tags.Set("M", ir.Value{Kind: ir.TypeStruct, Struct: sd,
		Fld: []ir.Value{ir.BoolVal(true), ir.RealVal(60)}})
	tags.Set("A", ir.Value{Kind: ir.TypeArray,
		Arr: []ir.Value{ir.RealVal(1), ir.RealVal(2)}})

	all := tags.All()
	cs, _ := tags.ChangedSince(0, nil)
	if len(cs) != len(all) {
		t.Fatalf("ChangedSince saw %d tags, All saw %d", len(cs), len(all))
	}
	for _, c := range cs {
		got, want := runtime.Plain(c.Value), all[c.Name]
		// Compare through the same shape All produces; maps and slices need
		// a structural check.
		if !sameJSON(got, want) {
			t.Errorf("Plain(%s) = %#v, All = %#v", c.Name, got, want)
		}
	}
}

func sameJSON(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !sameJSON(v, bv[k]) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !sameJSON(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}
