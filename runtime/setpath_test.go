package runtime_test

import (
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// Member writes: an operator commanding one field of a UDT tag
// (Tags.SetPath), which is what a faceplate's Start button and every
// setpoint inside an AnalogInput actually do. The tag store holds whole
// tags, so each of these is a read-modify-write of the aggregate.

const setPathLib = `
TYPE
  Limits : STRUCT
    HSP : REAL;
    LSP : REAL;
  END_STRUCT;
  Motor : STRUCT
    START   : BOOL;
    Running : BOOL;
    Starts  : INT;
    Speed   : REAL;
    Name    : STRING;
    LVL     : Limits;
  END_STRUCT;
END_TYPE
`

const setPathProg = `
PROGRAM Main
VAR_EXTERNAL
    P101  : Motor;
    TempSP : REAL;
END_VAR
P101.Running := P101.START;
END_PROGRAM
`

func setPathRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program:   setPathProg,
		Libraries: []string{setPathLib},
		Driver:    nio.NewMemory(),
		Tags: []runtime.TagDef{
			runtime.Typed("P101", runtime.RoleState, "Motor",
				runtime.Init(map[string]any{"Speed": 42.0, "LVL": map[string]any{"HSP": 80.0, "LSP": 20.0}})),
			runtime.Setpoint("TempSP", 65.0),
		},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return rt
}

// member reads one dotted path out of the store, failing the test if it
// doesn't resolve — the read side is ir/fieldOf territory, this just keeps
// the assertions short.
func member(t *testing.T, rt *runtime.Runtime, path string) any {
	t.Helper()
	root, rest, _ := strings.Cut(path, ".")
	cur, ok := rt.Tags().All()[root]
	if !ok {
		t.Fatalf("no tag %s", root)
	}
	if rest == "" {
		return cur
	}
	for _, seg := range strings.Split(rest, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%s: %v is not a struct", path, cur)
		}
		cur, ok = m[seg]
		if !ok {
			t.Fatalf("%s: no member %s", path, seg)
		}
	}
	return cur
}

func TestSetPathScalarMember(t *testing.T) {
	rt := setPathRuntime(t)
	if err := rt.Tags().SetPath("P101.START", true); err != nil {
		t.Fatalf("SetPath: %v", err)
	}
	if got := member(t, rt, "P101.START"); got != true {
		t.Errorf("P101.START = %v, want true", got)
	}
	// The rest of the struct is untouched — a member write is not a replace.
	if got := member(t, rt, "P101.Speed"); got != 42.0 {
		t.Errorf("P101.Speed = %v, want 42 (unchanged)", got)
	}
	// And the program sees it on the next scan, which is the whole point.
	rt.Scan()
	if got := member(t, rt, "P101.Running"); got != true {
		t.Errorf("P101.Running = %v after a scan, want true", got)
	}
}

func TestSetPathNestedMember(t *testing.T) {
	rt := setPathRuntime(t)
	if err := rt.Tags().SetPath("P101.LVL.HSP", 95.5); err != nil {
		t.Fatalf("SetPath: %v", err)
	}
	if got := member(t, rt, "P101.LVL.HSP"); got != 95.5 {
		t.Errorf("P101.LVL.HSP = %v, want 95.5", got)
	}
	if got := member(t, rt, "P101.LVL.LSP"); got != 20.0 {
		t.Errorf("P101.LVL.LSP = %v, want 20 (unchanged)", got)
	}
}

// A map payload MERGES: unspecified members keep their current values. This
// is the one place a write deliberately differs from `init:` seeding, which
// zero-fills what its mapping omits.
func TestSetPathMapMergesAndKeepsOtherMembers(t *testing.T) {
	rt := setPathRuntime(t)
	err := rt.Tags().SetPath("P101", map[string]any{
		"START": true,
		"LVL":   map[string]any{"LSP": 10.0},
	})
	if err != nil {
		t.Fatalf("SetPath: %v", err)
	}
	if got := member(t, rt, "P101.START"); got != true {
		t.Errorf("P101.START = %v, want true", got)
	}
	if got := member(t, rt, "P101.LVL.LSP"); got != 10.0 {
		t.Errorf("P101.LVL.LSP = %v, want 10", got)
	}
	// Everything the map did not name survives — including inside the
	// nested struct the map only partly addressed.
	if got := member(t, rt, "P101.Speed"); got != 42.0 {
		t.Errorf("P101.Speed = %v, want 42 (not zeroed by a partial merge)", got)
	}
	if got := member(t, rt, "P101.LVL.HSP"); got != 80.0 {
		t.Errorf("P101.LVL.HSP = %v, want 80 (not zeroed by a partial merge)", got)
	}
}

func TestSetPathTypedMembers(t *testing.T) {
	rt := setPathRuntime(t)
	// An INT member takes JSON's one number kind when it carries no
	// fraction, and stays an INT.
	if err := rt.Tags().SetPath("P101.Starts", 7.0); err != nil {
		t.Fatalf("SetPath INT: %v", err)
	}
	if err := rt.Tags().SetPath("P101.Name", "P-101A"); err != nil {
		t.Fatalf("SetPath STRING: %v", err)
	}
	v, _ := rt.Tags().ReadGlobal("P101")
	i := v.Struct.FieldIndex["Starts"]
	if v.Fld[i].Kind != ir.TypeInt || v.Fld[i].I != 7 {
		t.Errorf("Starts = %v (%s), want INT 7", v.Fld[i].I, v.Fld[i].Kind)
	}
	if got := member(t, rt, "P101.Name"); got != "P-101A" {
		t.Errorf("P101.Name = %v", got)
	}
}

func TestSetPathUnknownMemberSuggests(t *testing.T) {
	rt := setPathRuntime(t)
	err := rt.Tags().SetPath("P101.STRAT", true)
	if err == nil {
		t.Fatal("a misspelled member was accepted")
	}
	want := "tag P101: unknown member STRAT (did you mean START?)"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	// A nested unknown member names the path it got to, not just the tag.
	err = rt.Tags().SetPath("P101.LVL.HSPX", 1.0)
	if err == nil || !strings.HasPrefix(err.Error(), "tag P101.LVL: unknown member HSPX") {
		t.Errorf("nested unknown member error = %v", err)
	}
}

func TestSetPathTypeMismatch(t *testing.T) {
	rt := setPathRuntime(t)
	err := rt.Tags().SetPath("P101.START", 1.0)
	if err == nil {
		t.Fatal("a number was accepted for a BOOL member")
	}
	if want := "tag P101.START: want BOOL, got a number"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	// The refused write left the value alone.
	if got := member(t, rt, "P101.START"); got != false {
		t.Errorf("P101.START = %v after a refused write, want false", got)
	}
	// A struct tag needs a mapping, not a bare value.
	if err := rt.Tags().SetPath("P101", 1.0); err == nil ||
		!strings.Contains(err.Error(), "Motor is a struct") {
		t.Errorf("scalar into a struct tag = %v", err)
	}
}

// A dotted path whose ROOT is a scalar is an error — and, critically, must
// not fall back to creating a top-level tag with a dot in its name. That
// junk tag is what the program never reads and a Sparkplug edge would
// publish as a bogus metric.
func TestSetPathOnNonStructRootCreatesNoJunkTag(t *testing.T) {
	rt := setPathRuntime(t)
	err := rt.Tags().SetPath("TempSP.Hi", 80.0)
	if err == nil {
		t.Fatal("a member of a REAL tag was accepted")
	}
	if want := "tag TempSP is a REAL, not a struct — it has no member Hi"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if _, ok := rt.Tags().All()["TempSP.Hi"]; ok {
		t.Error("refused member write created a tag named TempSP.Hi")
	}
	if got := rt.Tags().Real("TempSP"); got != 65.0 {
		t.Errorf("TempSP = %v, want 65 (unchanged)", got)
	}
}

func TestSetPathUnknownRoot(t *testing.T) {
	rt := setPathRuntime(t)
	err := rt.Tags().SetPath("NoSuchTag.START", true)
	if err == nil || err.Error() != "undefined tag NoSuchTag" {
		t.Fatalf("error = %v, want undefined tag NoSuchTag", err)
	}
	if _, ok := rt.Tags().All()["NoSuchTag.START"]; ok {
		t.Error("refused write created a tag named NoSuchTag.START")
	}
	if _, ok := rt.Tags().All()["NoSuchTag"]; ok {
		t.Error("refused write created a tag named NoSuchTag")
	}
	// A whole-tag SetPath is equally strict: it edits, it does not create.
	if err := rt.Tags().SetPath("NoSuchTag", 1.0); err == nil {
		t.Error("SetPath created an unknown tag")
	}
}

// Tags.Set has no error channel, so its contract is behavioural: a dotted
// name resolves to a member, or nothing happens at all. It must never leave
// a dotted tag behind — that path is how POST /api/tags used to poison the
// store.
func TestSetRejectsDottedNamesWithoutCreatingThem(t *testing.T) {
	rt := setPathRuntime(t)
	rt.Tags().Set("P101.START", true)
	if got := member(t, rt, "P101.START"); got != true {
		t.Errorf("Set on a real member = %v, want true", got)
	}

	rt.Tags().Set("P101.STRAT", true)   // misspelled member
	rt.Tags().Set("Nope.START", true)   // unknown root
	rt.Tags().Set("TempSP.Hi", 1.0)     // root is a scalar
	rt.Tags().Set("P101.Speed", "fast") // wrong type
	for _, junk := range []string{"P101.STRAT", "Nope.START", "Nope", "TempSP.Hi", "P101.Speed"} {
		if _, ok := rt.Tags().All()[junk]; ok {
			t.Errorf("Set(%q) created a tag", junk)
		}
	}
	if got := member(t, rt, "P101.Speed"); got != 42.0 {
		t.Errorf("P101.Speed = %v, want 42 (the bad write changed nothing)", got)
	}
	// A whole-tag map payload goes the same way as a dotted one.
	rt.Tags().Set("P101", map[string]any{"Starts": 3})
	v, _ := rt.Tags().ReadGlobal("P101")
	if got := v.Fld[v.Struct.FieldIndex["Starts"]].I; got != 3 {
		t.Errorf("Starts after a map Set = %d, want 3", got)
	}
}

// Concurrent member writes to the same tag must not lose each other: the
// read-modify-write happens under the store's lock, so the last writer sees
// the other's field. Run with -race.
func TestSetPathConcurrentMembersCompose(t *testing.T) {
	rt := setPathRuntime(t)
	done := make(chan error, 2)
	go func() { done <- rt.Tags().SetPath("P101.START", true) }()
	go func() { done <- rt.Tags().SetPath("P101.Speed", 12.5) }()
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("SetPath: %v", err)
		}
	}
	if got := member(t, rt, "P101.START"); got != true {
		t.Errorf("P101.START = %v — a concurrent member write was lost", got)
	}
	if got := member(t, rt, "P101.Speed"); got != 12.5 {
		t.Errorf("P101.Speed = %v — a concurrent member write was lost", got)
	}
}
