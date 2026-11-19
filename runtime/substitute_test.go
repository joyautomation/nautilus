package runtime

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// Substitute values (#707): the store layering, the API's entry rules and
// quality. Expiry, companions, acknowledgement and events come in PR 1b.

func sub(t *testing.T, rt *Runtime, addr string, v any) Substitution {
	t.Helper()
	s, err := rt.Substitute(addr, SubstituteRequest{Value: v, For: time.Hour, By: "op"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A substituted input holds the substitute however often the driver
// re-delivers, the program runs on it, the entry reports the driver's own
// value as Live, and Restore puts the live value back at once (not on the
// next delivery).
func TestSubstituteInputOverridesDriver(t *testing.T) {
	rt, d := newForceRig(t, nil)
	rt.Scan()
	s := sub(t, rt, "Level", 95)
	if s.ID == 0 || s.Name != "Level" || s.Value != 95.0 || s.By != "op" || s.GrantedMs != time.Hour.Milliseconds() {
		t.Fatalf("Substitute returned %+v", s)
	}
	for i := 0; i < 3; i++ {
		rt.Scan() // the driver keeps delivering Level = 10
	}
	if got := rt.Tags().Real("Level"); got != 95 || !rt.Tags().Bool("Alarm") || d.output("Alarm") != true {
		t.Fatalf("Level = %v, Alarm = %v: the program did not run on the substitute", got, rt.Tags().Bool("Alarm"))
	}
	list := rt.Substitutes()
	if len(list) != 1 || list[0].Live != 10.0 || list[0].LiveQuality != "good" || list[0].ID != s.ID {
		t.Fatalf("Substitutes() = %+v; want Live 10 and LiveQuality good", list)
	}
	if _, ok := rt.Restore(s.ID); !ok {
		t.Fatal("Restore of the active entry reported none")
	}
	if got := rt.Tags().Real("Level"); got != 10 {
		t.Fatalf("Level = %v right after Restore; want the live 10 at once", got)
	}
	if n := rt.Tags().SubstituteCount(); n != 0 {
		t.Fatalf("%d substitutes after Restore", n)
	}
}

// The four orderings of a force and a substitute on one input. The force
// always wins; removing it returns the tag to the substitute while one is
// active; ending the substitute under a force leaves the force, and the
// force's Actual follows the substituted view, not the bad live reading.
func TestSubstituteUnderForce(t *testing.T) {
	steps := map[string]func(rt *Runtime, id *uint64){
		"force": func(rt *Runtime, _ *uint64) {
			if err := rt.Tags().Force("Level", 50); err != nil {
				panic(err)
			}
		},
		"unforce": func(rt *Runtime, _ *uint64) { rt.Tags().Unforce("Level") },
		"substitute": func(rt *Runtime, id *uint64) {
			s, err := rt.Substitute("Level", SubstituteRequest{Value: 95, For: time.Hour})
			if err != nil {
				panic(err)
			}
			*id = s.ID
		},
		"restore": func(rt *Runtime, id *uint64) { rt.Restore(*id) },
	}
	for _, tc := range []struct {
		order []string
		want  []float64 // Level after each step, before and after a scan; live is 10
	}{
		{[]string{"substitute", "force", "unforce", "restore"}, []float64{95, 50, 95, 10}},
		{[]string{"substitute", "force", "restore", "unforce"}, []float64{95, 50, 50, 10}},
		{[]string{"force", "substitute", "unforce", "restore"}, []float64{50, 50, 95, 10}},
		{[]string{"force", "substitute", "restore", "unforce"}, []float64{50, 50, 50, 10}},
	} {
		t.Run(strings.Join(tc.order, ","), func(t *testing.T) {
			rt, _ := newForceRig(t, nil)
			rt.Scan()
			var id uint64
			// While both hold, the force's Actual is the substitute, from
			// the moment the second one starts (not only after the next
			// delivery rewrites it). A force just applied reports no Actual
			// until something is written (force.go), so that step's
			// pre-scan check is skipped.
			actual := func(i int, prescan bool) {
				if prescan && tc.order[i] == "force" {
					return
				}
				if rt.Tags().ForceCount() == 1 && rt.Tags().SubstituteCount() == 1 {
					if f := rt.Tags().Forces(); f[0].Actual != 95.0 {
						t.Fatalf("step %d (%s, prescan %v): force Actual = %v; want the substitute 95", i, tc.order[i], prescan, f[0].Actual)
					}
				}
			}
			for i, name := range tc.order {
				steps[name](rt, &id)
				actual(i, true)
				if got := rt.Tags().Real("Level"); got != tc.want[i] {
					t.Fatalf("step %d (%s): Level = %v before a scan; want %v", i, name, got, tc.want[i])
				}
				rt.Scan()
				actual(i, false)
				if got := rt.Tags().Real("Level"); got != tc.want[i] {
					t.Fatalf("step %d (%s): Level = %v after a scan; want %v", i, name, got, tc.want[i])
				}
			}
		})
	}
}

// A member substitute on a struct input: the member holds, the rest of the
// struct follows the deliveries, a member write lands in the LIVE value
// (not baking the substitute in), the retain read is the live value, and
// Restore gives back what was delivered.
func TestSubstituteStructMember(t *testing.T) {
	sd := &ir.StructDef{
		Name:       "Pump",
		Fields:     []ir.StructField{{Name: "Run"}, {Name: "Speed"}},
		FieldIndex: map[string]int{"Run": 0, "Speed": 1},
	}
	mk := func(run bool, speed float64) ir.Value {
		return ir.Value{Kind: ir.TypeStruct, Struct: sd, Fld: []ir.Value{ir.BoolVal(run), ir.RealVal(speed)}}
	}
	tags := NewTags()
	tags.Set("P1", mk(false, 10))
	tags.mu.Lock()
	s, _, err := tags.substituteLocked("p1.speed", 55, subEntry{Substitution: Substitution{ID: 7}})
	tags.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "P1.Speed" {
		t.Fatalf("entry named %q; want the declared spelling P1.Speed", s.Name)
	}
	tags.Set("P1", mk(true, 20)) // a driver delivery
	if v, _ := tags.ReadPath("P1.Speed"); v != 55.0 {
		t.Fatalf("P1.Speed = %v; want the substitute 55", v)
	}
	if v, _ := tags.ReadPath("P1.Run"); v != true {
		t.Fatalf("P1.Run = %v; the unsubstituted member must follow the delivery", v)
	}
	if err := tags.SetPath("P1.Run", false); err != nil {
		t.Fatal(err)
	}
	live, _ := tags.readActual("P1")
	if live.Fld[1].F != 20 || live.Fld[0].B {
		t.Fatalf("live (retained) value = %+v; want Run false, Speed 20, no substitute baked in", live.Fld)
	}
	tags.mu.Lock()
	_, ok := tags.unsubstituteLocked(7)
	tags.mu.Unlock()
	if !ok {
		t.Fatal("unsubstitute found no entry 7")
	}
	if v, _ := tags.ReadPath("P1.Speed"); v != 20.0 {
		t.Fatalf("P1.Speed = %v after the end; want the delivered 20", v)
	}
	if v, _ := tags.ReadPath("P1.Run"); v != false {
		t.Fatalf("P1.Run = %v; the member write was lost", v)
	}
}

// A substitute started again on the same address is a new entry: the old
// ID is gone, so a late Restore naming it does not end the newer one.
func TestRestoreStaleIDKeepsTheNewerEntry(t *testing.T) {
	rt, _ := newForceRig(t, nil)
	rt.Scan()
	old := sub(t, rt, "Level", 90)
	newer := sub(t, rt, "Level", 95)
	if newer.ID == old.ID {
		t.Fatalf("a restart reused ID %d", old.ID)
	}
	if n := rt.Tags().SubstituteCount(); n != 1 {
		t.Fatalf("%d entries after re-substituting one address; want 1", n)
	}
	if _, ok := rt.Restore(old.ID); ok {
		t.Fatal("Restore of the replaced entry reported success")
	}
	rt.Scan()
	if got := rt.Tags().Real("Level"); got != 95 {
		t.Fatalf("Level = %v; a stale Restore ended the newer substitute", got)
	}
	if _, ok := rt.Restore(newer.ID); !ok {
		t.Fatal("Restore of the newer entry failed")
	}
}

func TestSubstituteRefusals(t *testing.T) {
	rt, _ := newForceRig(t, nil)
	rt.Scan()
	for _, tc := range []struct {
		name string
		addr string
		req  SubstituteRequest
	}{
		{"an output", "Motor", SubstituteRequest{Value: true, For: time.Hour}},
		{"an unknown tag", "Nope", SubstituteRequest{Value: true, For: time.Hour}},
		{"a value the type cannot hold", "Level", SubstituteRequest{Value: "high", For: time.Hour}},
		{"no duration", "Level", SubstituteRequest{Value: 5, For: 0}},
	} {
		if _, err := rt.Substitute(tc.addr, tc.req); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
	s := sub(t, rt, "StartPB", true)
	if _, err := rt.Substitute("StopPB", SubstituteRequest{Value: true, For: time.Hour, ID: s.ID}); err == nil {
		t.Error("an ID already active was accepted for another tag")
	}
	if n := rt.Tags().SubstituteCount(); n != 1 {
		t.Errorf("%d substitutes after the refusals; want 1", n)
	}
}

// A replicated entry brings its own ID; the next assigned one follows it.
func TestSubstituteSuppliedIDAdvancesTheSequence(t *testing.T) {
	rt, _ := newForceRig(t, nil)
	rt.Scan()
	if _, err := rt.Substitute("Level", SubstituteRequest{Value: 5, For: time.Hour, ID: 41}); err != nil {
		t.Fatal(err)
	}
	if s := sub(t, rt, "StartPB", true); s.ID != 42 {
		t.Fatalf("next ID = %d; want 42", s.ID)
	}
}

// Forces drop at a takeover; substitutes do not (they are intent meant to
// run the plant, and PR 3 replicates them).
func TestTakeoverKeepsSubstitutes(t *testing.T) {
	coord := &flag{lead: true}
	rt, _ := newForceRig(t, coord)
	rt.Scan()
	sub(t, rt, "Level", 95)
	_ = rt.Tags().Force("StartPB", true)
	coord.lead = false
	rt.Scan() // steps down
	coord.lead = true
	rt.Scan() // takeover
	if rt.Tags().ForceCount() != 0 {
		t.Fatal("setup: the takeover kept the force")
	}
	if got := rt.Tags().Real("Level"); got != 95 || rt.Tags().SubstituteCount() != 1 {
		t.Fatalf("Level = %v, %d substitutes after the takeover; want 95, 1", got, rt.Tags().SubstituteCount())
	}
}

// qualDriver is a field driver that reports quality and can fail its read.
type qualDriver struct {
	*fieldDriver
	mu   sync.Mutex
	q    map[string]nio.Quality
	fail bool
}

func (d *qualDriver) ReadInputs() (nio.Values, error) {
	d.mu.Lock()
	fail := d.fail
	d.mu.Unlock()
	if fail {
		return nil, errors.New("bus timeout")
	}
	return d.fieldDriver.ReadInputs()
}

func (d *qualDriver) Quality() map[string]nio.Quality {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]nio.Quality, len(d.q))
	for k, v := range d.q {
		out[k] = v
	}
	return out
}

// The quality of a substituted input, against a table written by hand from
// #707's rule (not derived from the code): on the leader a whole-tag
// substitute is Substituted whatever the driver or a failed read says, a
// member substitute only replaces Good (Bad, NotConnected, Stale outrank
// it); on a standby everything scan-fed is Stale, a driver's Substituted
// included, while a driver's Bad stands. Quality() and TagQuality() must
// agree on every row.
func TestSubstituteQualityTable(t *testing.T) {
	const lib = "TYPE Pump : STRUCT Run : BOOL; Speed : REAL; END_STRUCT; END_TYPE"
	const prog = `PROGRAM Q
VAR_EXTERNAL Level : REAL; P1 : Pump; Out : REAL; END_VAR
Out := Level + P1.Speed;
END_PROGRAM`
	const (
		G  = nio.Good
		B  = nio.Bad
		S  = nio.Stale
		SU = nio.Substituted
	)
	const silent = nio.Quality(255) // the driver says nothing about the tag
	type row struct {
		verdict nio.Quality // what the driver says about the tag
		fail    bool        // the last read failed
		standby bool
		whole   nio.Quality // expected for Level, substituted whole
		member  nio.Quality // expected for P1, P1.Speed substituted
		none    nio.Quality // expected for an unsubstituted tag with the same verdict
	}
	rows := []row{
		{silent, false, false, SU, SU, G},
		{silent, true, false, SU, S, S},
		{B, false, false, SU, B, B},
		{nio.NotConnected, false, false, SU, nio.NotConnected, nio.NotConnected},
		{SU, false, false, SU, SU, SU},
		{silent, false, true, S, S, S},
		{silent, true, true, S, S, S},
		{B, false, true, B, B, B},
		{SU, false, true, S, S, S},
	}
	for _, r := range rows {
		coord := &flag{lead: true}
		fd := &fieldDriver{in: nio.Values{"Level": 10.0, "Flow": 1.0}}
		qd := &qualDriver{fieldDriver: fd, q: map[string]nio.Quality{}}
		rt, err := New(Options{
			Program: prog, Libraries: []string{lib}, Driver: qd, Coordinator: coord,
			Tags: []TagDef{Input("Level"), Typed("P1", RoleInput, "Pump"), Input("Flow"), Output("Out", Init(0.0))},
		})
		if err != nil {
			t.Fatal(err)
		}
		pt := rt.tags.types[ir.NameKey("P1")]
		pv, err := ir.SetFieldTyped(ir.Zero(pt), pt, []string{"Speed"}, 20.0, "")
		if err != nil {
			t.Fatal(err)
		}
		fd.set("P1", pv)
		rt.Scan()
		sub(t, rt, "Level", 95)
		sub(t, rt, "P1.Speed", 55)
		if r.verdict != silent {
			qd.q = map[string]nio.Quality{"Level": r.verdict, "P1": r.verdict, "Flow": r.verdict}
		}
		qd.fail = r.fail
		rt.Scan()
		coord.lead = !r.standby
		q := rt.Quality()
		at := func(name string) nio.Quality {
			if v, ok := q[name]; ok {
				return v
			}
			return G
		}
		for _, c := range []struct {
			tag  string
			want nio.Quality
		}{{"Level", r.whole}, {"P1", r.member}, {"Flow", r.none}} {
			if got := at(c.tag); got != c.want {
				t.Errorf("%+v: Quality()[%s] = %s; want %s", r, c.tag, got, c.want)
			}
			if got := rt.TagQuality(c.tag); got != c.want {
				t.Errorf("%+v: TagQuality(%s) = %s; want %s", r, c.tag, got, c.want)
			}
		}
		if got := rt.TagQuality("P1.Run"); got != r.member {
			t.Errorf("%+v: TagQuality(P1.Run) = %s; want the root's %s", r, got, r.member)
		}
	}
}
