//go:build !js

package runtime

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// tagsMain writes a state tag and an output, and reads an input; tagsAux
// writes another state tag. Mode is a state tag no program writes (only an
// operator does) and is not retained; SP is a setpoint, retained.
const tagsMain = `PROGRAM Main
VAR_EXTERNAL Level : REAL; Valve : BOOL; Latch : BOOL; Count : DINT; END_VAR
VAR n : DINT; END_VAR
n := n + 1;
Count := n;
IF Level > 5.0 THEN Latch := TRUE; END_IF;
Valve := Latch;
END_PROGRAM`

const tagsAux = `PROGRAM Aux
VAR_EXTERNAL Count : DINT; Seen : DINT; END_VAR
Seen := Count;
END_PROGRAM`

func tagsOptions(clk Clock, extra ...TagDef) Options {
	return Options{
		Program: tagsMain,
		Tags: append([]TagDef{
			Input("Level"),
			Output("Valve"),
			State("Latch", false),
			State("Count", int64(0)),
			State("Seen", int64(0)),
			State("Mode", int64(1)),
			Setpoint("SP", 2.5),
		}, extra...),
		Tasks:  []Task{{Name: "aux", Program: tagsAux, Scan: time.Hour}},
		Scan:   time.Hour,
		Clock:  clk,
		Retain: &memStore{},
	}
}

func newTagsRuntime(t *testing.T, clk Clock, extra ...TagDef) *Runtime {
	t.Helper()
	rt, err := New(tagsOptions(clk, extra...))
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Tags().WriteGlobal("Level", ir.RealVal(0)); err != nil { // the driver's first read
		t.Fatal(err)
	}
	return rt
}

// tagsIn is an image's carried tags by name, every section together.
func tagsIn(rt *Runtime, img *Image) map[string][]ir.Leaf {
	out := map[string][]ir.Leaf{}
	add := func(tv *TagValues) {
		for i, place := range tv.Idx {
			out[rt.carried.keys[place]] = append([]ir.Leaf(nil), tv.leavesOf(i)...)
		}
	}
	add(&img.Tags)
	for i := range img.Tasks {
		add(&img.Tasks[i].Tags)
	}
	return out
}

// An image carries every tag the field does not provide, whoever writes it
// (#710 S2, naut-lead's ruling): the state tags the programs write, in
// their task's section, and a setpoint only an operator writes, in the
// store's. Not the input (the field gives it back), not the output (the
// first scan recomputes it). A forced tag goes as its actual value.
func TestImageCarriesEveryTagTheFieldDoesNotProvide(t *testing.T) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	lead := newTagsRuntime(t, clk)
	keys, _ := lead.CarriedTags()
	if want := []string{ir.NameKey("Count"), ir.NameKey("Latch"), ir.NameKey("Mode"), ir.NameKey("Seen"), ir.NameKey("SP")}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("carried %v, want %v (no input, no output)", keys, want)
	}
	if err := lead.Tags().WriteGlobal("Level", ir.RealVal(9)); err != nil {
		t.Fatal(err)
	}
	if errs := lead.Tags().Write([]WriteOp{{Path: "Mode", Value: int64(3)}}); errs[0] != nil {
		t.Fatal(errs[0])
	}
	scanBoth(t, lead, clk, 4)
	if err := lead.Tags().Force("Count", int64(99)); err != nil {
		t.Fatal(err)
	}
	img := takeOne(t, lead, clk)
	got := tagsIn(lead, img)
	for _, k := range []string{"Level", "Valve"} {
		if _, ok := got[ir.NameKey(k)]; ok {
			t.Errorf("the image carries %s, which the field provides", k)
		}
	}
	if l := got[ir.NameKey("Mode")]; len(l) != 1 || l[0].Value().I != 3 {
		t.Errorf("Mode %v, want the operator's 3", l)
	}
	if l := got[ir.NameKey("Latch")]; len(l) != 1 || !l[0].Value().B {
		t.Errorf("Latch %v, want TRUE", l)
	}
	if l := got[ir.NameKey("Count")]; len(l) != 1 || l[0].Value().I != 6 {
		t.Errorf("Count %v, want its actual 6 (the program's), not the forced 99", l)
	}
	// Where each went: Count and Latch with the main task's frame, Seen with
	// aux's, Mode from the store. SP, retained, goes in the retain section
	// only, not twice.
	if !slices.Equal(img.Tags.Idx, []int{lead.carried.index[ir.NameKey("Mode")]}) {
		t.Errorf("the store's section %v, want Mode only (SP is in the retain section)", img.Tags.Idx)
	}
	if img.TagsLayout == "" || img.Format != imageFormat {
		t.Errorf("tags layout %q, format %d", img.TagsLayout, img.Format)
	}

	// A standby of the same project applies them, by name.
	stby := newTagsRuntime(t, &imgClock{t: time.Unix(1_600_000_000, 0)})
	sh := stby.NewShadow()
	img.Term = 2
	wire := mustEncode(t, nil, img)
	var dec Image
	if err := DecodeImage(wire, &dec); err != nil {
		t.Fatal(err)
	}
	rep, err := sh.Apply(&dec)
	if err != nil || !rep.InSync() {
		t.Fatalf("apply: %+v %v", rep, err)
	}
	st := sh.tagsOf()
	// Seen is 99: aux read the forced Count, as the logic sees a force.
	if st[ir.NameKey("Mode")].I != 3 || st[ir.NameKey("Count")].I != 6 || !st[ir.NameKey("Latch")].B || st[ir.NameKey("Seen")].I != 99 {
		t.Fatalf("shadow tags %v", st)
	}
}

// A task's tags are copied with its frame, at the end of the same scan: a
// fast task that scans on before the image is taken leaves its tag and its
// frame of one instant, never the tag a scan later (so a counter in the
// frame and the tag it set are never one count apart after a takeover).
func TestImageTagsOfOneInstantWithTheFrame(t *testing.T) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	lead := newTagsRuntime(t, clk)
	scanBoth(t, lead, clk, 3)
	lead.RequestImage()
	clk.step(100 * time.Millisecond)
	lead.Scan() // main copies here: n = Count = 4
	lead.Scan() // and runs on: n = Count = 5, no copy
	lead.Scan()
	if err := lead.ScanTask("aux"); err != nil {
		t.Fatal(err)
	}
	var img Image
	if lead.TakeImage(&img) {
		t.Fatal("an image before main copied again")
	}
	lead.Scan() // main's second copy, after aux's: n = Count = 7
	lead.Scan() // runs on: n = Count = 8, no copy
	if !lead.TakeImage(&img) {
		t.Fatal("no image")
	}
	sh := newTagsRuntime(t, clk).NewShadow()
	img.Term = 1
	if _, err := sh.Apply(&img); err != nil {
		t.Fatal(err)
	}
	f, _ := sh.frameOf(MainTaskName)
	n := slotOf(t, lead.prog, "n")
	if count := sh.tagsOf()[ir.NameKey("Count")].I; f.Slots[n].I != 7 || count != 7 {
		t.Fatalf("frame n %d, tag Count %d: want both 7, the copy's scan", f.Slots[n].I, count)
	}
}

func slotOf(t *testing.T, p *Program, name string) int {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, s := range p.prog.Slots {
		if s.Name == name {
			return i
		}
	}
	t.Fatalf("no slot %s", name)
	return -1
}

// A standby whose carried tags differ (another manifest) applies none of
// the leader's tags and is not in sync; nor is one given a format 1 image
// (a leader of an older build), which carries none.
func TestShadowRefusesAnotherTagsLayout(t *testing.T) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	lead := newTagsRuntime(t, clk)
	scanBoth(t, lead, clk, 2)
	img := takeOne(t, lead, clk)
	img.Term = 1

	other := newTagsRuntime(t, clk, State("Zzz", int64(0))).NewShadow()
	rep, err := other.Apply(img)
	if err != nil || !rep.TagsDiffer || rep.InSync() || other.tagsOf() != nil {
		t.Fatalf("another tags layout: %+v, %v, tags %v", rep, err, other.tagsOf())
	}

	same := newTagsRuntime(t, clk).NewShadow()
	wire, err := EncodeImageFormat(nil, img, 1)
	if err != nil {
		t.Fatal(err)
	}
	var old Image
	if err := DecodeImage(wire, &old); err != nil {
		t.Fatal(err)
	}
	rep, err = same.Apply(&old)
	if err != nil || !rep.TagsMissing || rep.InSync() || same.tagsOf() != nil || len(rep.Applied) != 2 {
		t.Fatalf("a format 1 image: %+v, %v", rep, err)
	}
	img.Seq++
	if rep, err = same.Apply(img); err != nil || !rep.InSync() || same.tagsOf() == nil {
		t.Fatalf("the same layout: %+v, %v", rep, err)
	}
}

// Each task's image names the source it ran: an online edit of the body
// alone keeps the layout and changes the source.
func TestImageSourceNamesTheRunningSource(t *testing.T) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	lead := newImgRuntime(t, clk, imgMain)
	scanBoth(t, lead, clk, 2)
	before := takeOne(t, lead, clk)
	if before.Tasks[0].Source != sourceHash(imgMain) {
		t.Fatalf("source %q, want %q", before.Tasks[0].Source, sourceHash(imgMain))
	}
	edited := replaceFirst(t, imgMain, "i := i + 1;", "i := i + 2;")
	if _, err := lead.prog.SwapWarm(edited); err != nil {
		t.Fatal(err)
	}
	after := takeOne(t, lead, clk)
	if after.Tasks[0].Layout != before.Tasks[0].Layout {
		t.Fatal("a body-only edit changed the layout: the test proves nothing")
	}
	if after.Tasks[0].Source != sourceHash(edited) || after.Tasks[1].Source == "" {
		t.Fatalf("after the edit: main source %q (want %q), aux %q", after.Tasks[0].Source, sourceHash(edited), after.Tasks[1].Source)
	}
}

// A tag two tasks write takes the copy with the later time base, whatever
// order the tasks are listed in.
func TestShadowTagWrittenByTwoTasksTakesTheLaterCopy(t *testing.T) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	rt := newTagsRuntime(t, clk)
	cs := rt.carried
	c := cs.index[ir.NameKey("Count")]
	one := func(v int64) TagValues {
		return TagValues{Idx: []int{c}, Ends: []int{1}, Leaves: leavesOf(ir.IntVal(v))}
	}
	img := &Image{Format: imageFormat, TagsLayout: cs.layout(), Tasks: []TaskImage{
		{Task: "main", NowMs: 200, Tags: one(2)},
		{Task: "aux", NowMs: 100, Tags: one(1)},
	}}
	st, err := applyTags(cs, img)
	if err != nil {
		t.Fatal(err)
	}
	if st.vals[c].I != 2 {
		t.Fatalf("Count %d, want 2 (the later copy)", st.vals[c].I)
	}
	img.Tasks[1].NowMs = 300
	if st, err = applyTags(cs, img); err != nil || st.vals[c].I != 1 {
		t.Fatalf("Count %v %v, want 1 (now the later copy)", st.vals[c].I, err)
	}
	// Leaves of another kind are refused, all or nothing.
	img.Tasks[0].Tags.Leaves = leavesOf(ir.StringVal("x"))
	if _, err := applyTags(cs, img); err == nil {
		t.Fatal("a STRING applied to a DINT tag")
	}
}

// The scan's copy of the tags it writes allocates nothing once warm, for
// every shape of tag: a STRUCT, an ARRAY, a STRING, an enumeration-free
// scalar (#710 S2a review).
func TestImageTagCopyAddsNoAllocationToTheScan(t *testing.T) {
	const src = `TYPE Pt : STRUCT x : REAL; n : INT; END_STRUCT; END_TYPE
PROGRAM Main
VAR_EXTERNAL P : Pt; A : ARRAY[0..3] OF REAL; S : STRING; K : DINT; END_VAR
VAR i : INT; END_VAR
i := i + 1;
P.x := i; P.n := i;
A[i MOD 4] := i;
S := 'abc';
K := i;
END_PROGRAM`
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	pt, arr := State("P", nil), State("A", nil)
	pt.Type, arr.Type = "Pt", "ARRAY[0..3] OF REAL"
	rt, err := New(Options{Program: src, Scan: time.Hour, Clock: clk, Tags: []TagDef{pt, arr}})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	keys, _ := rt.CarriedTags()
	if len(keys) != 4 || len(rt.prog.carryIdx) != 4 {
		t.Fatalf("carried %v, the program's %v: want P, A, S and K", keys, rt.prog.carryIdx)
	}
	var img Image
	for range 3 {
		rt.RequestImage()
		clk.step(100 * time.Millisecond)
		rt.Scan()
		rt.TakeImage(&img)
	}
	if img.Tasks[0].Tags.Len() != 4 {
		t.Fatalf("the image's task tags %v", img.Tasks[0].Tags.Idx)
	}
	plain := testing.AllocsPerRun(50, func() { rt.Scan() })
	asked := testing.AllocsPerRun(50, func() {
		rt.RequestImage()
		rt.Scan()
		rt.prog.takeImage(MainTaskName, &img.Tasks[0], imageStamp(rt.imgRound, 0))
	})
	if asked > plain {
		t.Fatalf("a scan that copies its frame and tags allocates %v, a plain scan %v", asked, plain)
	}
}

// A tag that holds a function block (an FB, or a STRUCT or ARRAY of one)
// is not carried, nor is a dt-tag the runtime writes every scan.
func TestCarrySetLeavesOutFBsAndDtTags(t *testing.T) {
	fb := ir.LookupFBType("TON")
	st := &ir.Type{Kind: ir.TypeStruct, Struct: &ir.StructDef{Name: "S", Fields: []ir.StructField{{Name: "t", Type: fb}}}}
	for name, ty := range map[string]*ir.Type{"an FB": fb, "an array of FBs": {Kind: ir.TypeArray, ArrLen: 2, Elem: fb}, "a struct holding one": st} {
		if !holdsFB(ty, 0) {
			t.Errorf("%s: not seen to hold an FB", name)
		}
	}
	if holdsFB(&ir.Type{Kind: ir.TypeArray, ArrLen: 2, Elem: ir.RealT}, 0) {
		t.Error("an array of REAL holds an FB")
	}
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	o := tagsOptions(clk)
	o.DtTag = "MainDt"
	o.Tasks[0].DtTag = "AuxDt"
	rt, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := rt.CarriedTags()
	for _, k := range keys {
		if k == ir.NameKey("MainDt") || k == ir.NameKey("AuxDt") {
			t.Errorf("the dt-tag %s is carried", k)
		}
	}
}
