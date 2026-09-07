package runtime_test

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// The write generation is what every "has this changed?" question in the
// system is now answered with — the Sparkplug RBE fast path, the driver
// output push, the SSE frame memo. These pin the two properties all of them
// depend on: it moves on a real change, and it does NOT move on a write that
// stores the value already there.

func TestGenerationAdvancesOnlyOnChange(t *testing.T) {
	tags := runtime.NewTags()
	tags.SetReal("A", 1)
	g1 := tags.Generation()
	if g1 == 0 {
		t.Fatal("creating a tag must advance the generation")
	}

	tags.SetReal("A", 1) // same value
	if got := tags.Generation(); got != g1 {
		t.Errorf("re-writing the same value advanced the generation %d → %d", g1, got)
	}

	tags.SetReal("A", 2)
	g2 := tags.Generation()
	if g2 == g1 {
		t.Error("a changed value did not advance the generation")
	}

	// Per-tag generations track the same rule.
	tagGen, ok := tags.TagGeneration("A")
	if !ok || tagGen != g2 {
		t.Errorf("TagGeneration(A) = %d, %v; want %d, true", tagGen, ok, g2)
	}
	if _, ok := tags.TagGeneration("nope"); ok {
		t.Error("TagGeneration reported an unknown tag as present")
	}
}

// A struct written back identically is the case that matters: it is the
// shape a UDT driver re-delivers every scan, and deep-comparing it once on
// the write is what lets everything downstream skip comparing it at all.
func TestGenerationStructWrites(t *testing.T) {
	sd := &ir.StructDef{
		Name:       "Blk",
		Fields:     []ir.StructField{{Name: "A"}, {Name: "B"}},
		FieldIndex: map[string]int{"A": 0, "B": 1},
	}
	mk := func(a float64, b bool) ir.Value {
		return ir.Value{Kind: ir.TypeStruct, Struct: sd,
			Fld: []ir.Value{ir.RealVal(a), ir.BoolVal(b)}}
	}
	tags := runtime.NewTags()
	tags.Set("P", mk(1, false))
	g := tags.Generation()

	tags.Set("P", mk(1, false)) // equal, distinct storage
	if got := tags.Generation(); got != g {
		t.Errorf("an equal struct advanced the generation %d → %d", g, got)
	}

	tags.Set("P", mk(1, true)) // one member moved
	if tags.Generation() == g {
		t.Error("a changed member did not advance the generation")
	}
	g = tags.Generation()

	// A member write through SetPath is a change like any other.
	if err := tags.SetPath("P.A", 9.0); err != nil {
		t.Fatalf("SetPath: %v", err)
	}
	if tags.Generation() == g {
		t.Error("SetPath did not advance the generation")
	}
	if v, _ := tags.ReadPath("P.A"); v != 9.0 {
		t.Errorf("P.A = %v, want 9", v)
	}

	// A DIFFERENT definition with equal field values is a different tag
	// shape, not an equal value: keeping the old definition would silently
	// rename the members.
	g = tags.Generation()
	other := &ir.StructDef{
		Name:       "Blk",
		Fields:     []ir.StructField{{Name: "X"}, {Name: "Y"}},
		FieldIndex: map[string]int{"X": 0, "Y": 1},
	}
	cur, _ := tags.ReadGlobal("P")
	tags.Set("P", ir.Value{Kind: ir.TypeStruct, Struct: other, Fld: cur.Fld})
	if tags.Generation() == g {
		t.Error("a new StructDef with equal values was treated as unchanged")
	}
	if _, ok := tags.ReadPath("P.X"); !ok {
		t.Error("the new definition's member did not resolve — the old one was kept")
	}
}

// NameGeneration answers a different question from Generation: it moves when
// the tag SET changes and stays put while values churn. Consumers cache
// name-derived tables (sorted metric order, class rules) against it.
func TestNameGeneration(t *testing.T) {
	tags := runtime.NewTags()
	tags.SetReal("B", 1)
	tags.SetReal("A", 1)
	ng := tags.NameGeneration()

	tags.SetReal("A", 2) // value churn
	if got := tags.NameGeneration(); got != ng {
		t.Errorf("a value change moved NameGeneration %d → %d", ng, got)
	}
	tags.SetReal("C", 1) // new tag
	if tags.NameGeneration() == ng {
		t.Error("a new tag did not move NameGeneration")
	}

	names := tags.AppendNames(nil)
	if len(names) != 3 || names[0] != "A" || names[1] != "B" || names[2] != "C" {
		t.Errorf("AppendNames = %v, want [A B C]", names)
	}
	// The cache must not be reachable through the returned slice.
	names[0] = "clobbered"
	if again := tags.AppendNames(nil); again[0] != "A" {
		t.Errorf("AppendNames handed out its cache: %v", again)
	}
}

// SnapshotInto reuses the caller's map, so it must leave it describing the
// CURRENT store — carrying each tag's generation and dropping nothing stale.
func TestSnapshotInto(t *testing.T) {
	tags := runtime.NewTags()
	tags.SetReal("A", 1)
	tags.SetReal("B", 2)

	snap := tags.SnapshotInto(nil)
	if len(snap) != 2 {
		t.Fatalf("snapshot has %d tags, want 2", len(snap))
	}
	genA, _ := tags.TagGeneration("A")
	if snap["A"].Gen != genA || snap["A"].Value.F != 1 {
		t.Errorf("A = %+v, want {1 %d}", snap["A"], genA)
	}

	// A stale key in the caller's map is removed, not left to be published
	// forever after the tag it named is gone.
	snap["ghost"] = runtime.Sample{Value: ir.RealVal(99), Gen: 1}
	snap = tags.SnapshotInto(snap)
	if _, ok := snap["ghost"]; ok {
		t.Error("SnapshotInto left a key the store no longer has")
	}
	if len(snap) != 2 {
		t.Errorf("snapshot has %d tags after reuse, want 2", len(snap))
	}

	// And it agrees with Snapshot, which is still the simple API.
	plain := tags.Snapshot()
	if len(plain) != len(snap) {
		t.Fatalf("Snapshot has %d tags, SnapshotInto %d", len(plain), len(snap))
	}
	for k, v := range plain {
		if got := snap[k].Value; got.Kind != v.Kind || got.F != v.F {
			t.Errorf("%s: SnapshotInto %v vs Snapshot %v", k, got, v)
		}
	}
}
