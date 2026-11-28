package runtime

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// The expression stage (exprstage.go; one-struct-model §6): each rule has a
// test here, run against a real runtime and scan.

const gaugeLib = `
TYPE
  Gauge : STRUCT
    Raw   : INT;
    Idx   : INT;
    Div   : INT;
    Vals  : ARRAY[0..3] OF REAL;
    Pick  : REAL;
    Ratio : INT;
    Ok    : BOOL;
    Both  : BOOL;
    Twice : INT;
    Note  : REAL;
    Cmd   : BOOL;
  END_STRUCT;
END_TYPE
FUNCTION Double : INT
VAR_INPUT x : INT; END_VAR
Double := x * 2;
END_FUNCTION
FUNCTION RawOf : INT
VAR_INPUT g : Gauge; END_VAR
RawOf := g.Raw;
END_FUNCTION
`

// gaugeDef: Both reads Ok and Pick, and sorts before them, so it runs
// first only if the order is wrong.
func gaugeDef() InstanceDef {
	return InstanceDef{Members: map[string]MemberDef{
		"Raw":   {Kind: MemberFrom},
		"Idx":   {Kind: MemberFrom},
		"Div":   {Kind: MemberFrom},
		"Pick":  {Kind: MemberExpr, Expr: "Vals[Idx]"},
		"Ratio": {Kind: MemberExpr, Expr: "100 / Div"},
		"Ok":    {Kind: MemberExpr, Expr: "Raw > 0"},
		"Both":  {Kind: MemberExpr, Expr: "Ok AND Pick > 1.0"},
		"Twice": {Kind: MemberExpr, Expr: "Double(Raw)"},
		"Cmd":   {Kind: MemberTo},
	}}
}

// countDriver counts its WriteOutputs and remembers what they held.
type countDriver struct {
	instDriver
	wmu    sync.Mutex
	writes int
	keys   map[string]bool
}

func (d *countDriver) WriteOutputs(out nio.Values) error {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	d.writes++
	if d.keys == nil {
		d.keys = map[string]bool{}
	}
	for k := range out {
		d.keys[k] = true
	}
	return nil
}

func (d *countDriver) writeCount() int {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	return d.writes
}

func newGaugeRuntime(t testing.TB, body string, def InstanceDef, extra ...TagDef) (*Runtime, *countDriver) {
	t.Helper()
	d := &countDriver{}
	tags := append([]TagDef{InstanceTag("G1", "Gauge", def), State("Seen", false)}, extra...)
	r, err := New(Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL G1 : Gauge; Seen : BOOL; END_VAR\n" + body + "\nEND_PROGRAM",
		Libraries: []string{gaugeLib},
		Driver:    d,
		Tags:      tags,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, d
}

// deliver sets every from: member, so the expressions may run.
func deliver(d *countDriver, raw, idx, div int64) {
	d.set("G1.Raw", raw)
	d.set("G1.Idx", idx)
	d.set("G1.Div", div)
}

// Each expression is computed in the scan, in dependency order, before the
// program, which reads this scan's value; a FUNCTION of the project is
// called.
func TestExprStageComputesBeforeTheProgramInOrder(t *testing.T) {
	r, d := newGaugeRuntime(t, "Seen := G1.Both;", gaugeDef())
	write(t, r, "G1.Vals[1]", 5.0)
	deliver(d, 3, 1, 4)
	r.Scan()
	wantPath(t, r, "G1.Ok", true)
	wantPath(t, r, "G1.Pick", 5.0)
	wantPath(t, r, "G1.Ratio", int64(25))
	wantPath(t, r, "G1.Twice", int64(6))
	wantPath(t, r, "G1.Both", true)
	wantPath(t, r, "Seen", true)
	// A change is a new value in the store, its generation moved; a value
	// read before it is not changed under its reader.
	old, _ := r.Tags().ReadGlobal("G1")
	gen := r.Tags().Generation()
	d.set("G1.Raw", int64(0))
	r.Scan()
	wantPath(t, r, "G1.Both", false)
	wantPath(t, r, "Seen", false)
	if r.Tags().Generation() == gen {
		t.Error("an expression changed and the store's generation did not move")
	}
	in := r.tags.instanceOf("G1")
	for i, n := range in.root.names {
		if n == "Ok" && !old.Fld[i].B {
			t.Error("the stage changed a value a reader already held")
		}
	}
}

// An expression reading one that has not run yet does not run either: NOT
// of a FALSE it never computed would be TRUE.
func TestExprStageWaitsForTheExpressionsItReads(t *testing.T) {
	def := gaugeDef()
	def.Members["Both"] = MemberDef{Kind: MemberExpr, Expr: "NOT Ok"}
	r, d := newGaugeRuntime(t, "", def)
	r.Scan()
	wantPath(t, r, "G1.Both", false)
	d.set("G1.Raw", int64(0))
	r.Scan()
	wantPath(t, r, "G1.Both", true)
}

// Until what it reads has been delivered, an expression does not run: a
// BOOL is FALSE (here NOT of a zero would be TRUE) and its quality is
// NotConnected; once delivered it runs and is Good.
func TestExprStageWaitsForItsInputs(t *testing.T) {
	def := gaugeDef()
	def.Members["Ok"] = MemberDef{Kind: MemberExpr, Expr: "NOT (Raw > 0)"}
	r, d := newGaugeRuntime(t, "", def)
	r.Scan()
	wantPath(t, r, "G1.Ok", false)
	wantPath(t, r, "G1.Both", false) // reads Ok, which has not run
	if q := r.TagQuality("G1.Ok"); q != nio.NotConnected {
		t.Errorf("G1.Ok quality %v, want NotConnected", q)
	}
	d.set("G1.Raw", int64(0))
	r.Scan()
	wantPath(t, r, "G1.Ok", true)
	if q := r.TagQuality("G1.Ok"); q != nio.Good {
		t.Errorf("G1.Ok quality %v once delivered, want Good", q)
	}
}

// A forced expression is not evaluated: it, what reads it and the program
// see the force; unforced, it is computed again.
func TestExprStageForcedMemberIsSkipped(t *testing.T) {
	r, d := newGaugeRuntime(t, "Seen := G1.Ok;", gaugeDef())
	write(t, r, "G1.Vals[1]", 5.0)
	deliver(d, 0, 1, 1)
	r.Scan()
	wantPath(t, r, "G1.Ok", false)
	if err := r.Tags().Force("G1.Ok", true); err != nil {
		t.Fatal(err)
	}
	r.Scan()
	wantPath(t, r, "G1.Ok", true)
	wantPath(t, r, "G1.Both", true)
	wantPath(t, r, "Seen", true)
	r.Tags().Unforce("G1.Ok")
	r.Scan()
	wantPath(t, r, "G1.Ok", false)
	wantPath(t, r, "G1.Both", false)
}

// A force on a delivered member the device never sent counts as its value:
// the expression follows it, and the tag's actual value never holds what
// the force laid over it.
func TestExprStageFollowsAForcedInput(t *testing.T) {
	r, d := newGaugeRuntime(t, "", gaugeDef())
	_ = d
	if err := r.Tags().Force("G1.Raw", int64(4)); err != nil {
		t.Fatal(err)
	}
	r.Scan()
	wantPath(t, r, "G1.Ok", true)
	wantPath(t, r, "G1.Twice", int64(8))
	r.Tags().Unforce("G1.Raw")
	wantPath(t, r, "G1.Raw", int64(0))
	d.set("G1.Raw", int64(0))
	r.Scan()
	wantPath(t, r, "G1.Ok", false)
	wantPath(t, r, "G1.Twice", int64(0))
}

// logBuf captures a runtime's log.
func logBuf(r *Runtime) *bytes.Buffer {
	var b bytes.Buffer
	r.log = slog.New(slog.NewTextHandler(&b, nil))
	return &b
}

// A fault (an index out of range, a division by zero) keeps the member's
// last value with quality Bad, is logged when it starts and when it clears,
// never each scan, shows in Stats, and stops neither the other expressions
// nor the program.
func TestExprStageFaultKeepsTheLastValue(t *testing.T) {
	r, d := newGaugeRuntime(t, "Seen := G1.Ok;", gaugeDef())
	log := logBuf(r)
	write(t, r, "G1.Vals[2]", 7.0)
	deliver(d, 1, 2, 5)
	r.Scan()
	wantPath(t, r, "G1.Pick", 7.0)
	wantPath(t, r, "G1.Ratio", int64(20))
	d.set("G1.Idx", int64(9))
	d.set("G1.Div", int64(0))
	before := r.Tags().DivZeroCount()
	for i := 0; i < 5; i++ {
		r.Scan()
	}
	wantPath(t, r, "G1.Pick", 7.0)
	wantPath(t, r, "G1.Ratio", int64(20))
	wantPath(t, r, "G1.Twice", int64(2)) // the others run on
	wantPath(t, r, "Seen", true)         // and the program
	if r.Tags().DivZeroCount() == before {
		t.Error("the division by zero is not counted")
	}
	for _, m := range []string{"G1.Pick", "G1.Ratio"} {
		if q := r.TagQuality(m); q != nio.Bad {
			t.Errorf("%s quality %v while faulting, want Bad", m, q)
		}
		if q := r.Quality()[m]; q != nio.Bad {
			t.Errorf("Quality()[%s] = %v, want Bad", m, q)
		}
	}
	if q := r.TagQuality("G1"); q != nio.Bad {
		t.Errorf("G1 quality %v with a member faulting, want Bad", q)
	}
	if q := r.Quality()["G1"]; q != nio.Bad {
		t.Errorf("Quality()[G1] = %v with a member faulting, want Bad", q)
	}
	st := r.Stats().Exprs
	if st == nil || st.Members != 5 || len(st.Faulting) != 2 || st.Faulting[0].Member != "G1.Pick" || !strings.Contains(st.Faulting[0].Error, "out of bounds") ||
		st.Faulting[1].Error != "division by zero" || st.Faulting[0].Scans != 5 {
		t.Fatalf("Stats().Exprs = %+v", st)
	}
	if n := strings.Count(log.String(), "expression fault:"); n != 2 {
		t.Errorf("%d fault starts logged over 5 scans, want 2:\n%s", n, log)
	}
	d.set("G1.Idx", int64(0))
	d.set("G1.Div", int64(2))
	r.Scan()
	r.Scan()
	wantPath(t, r, "G1.Pick", 0.0)
	wantPath(t, r, "G1.Ratio", int64(50))
	if n := strings.Count(log.String(), "expression fault cleared"); n != 2 {
		t.Errorf("%d clears logged, want 2:\n%s", n, log)
	}
	if q := r.TagQuality("G1.Pick"); q != nio.Good {
		t.Errorf("G1.Pick quality %v once clear, want Good", q)
	}
	if st := r.Stats().Exprs; len(st.Faulting) != 0 {
		t.Errorf("still faulting: %+v", st.Faulting)
	}
}

// The stage is no program write: an expression changing every scan marks
// nothing written and moves no output generation, so nothing is pushed and
// the to: member is never handed over.
func TestExprStageSendsNothing(t *testing.T) {
	r, d := newGaugeRuntime(t, "", gaugeDef(), Output("Out"))
	deliver(d, 1, 0, 1)
	r.Scan()
	r.Scan()
	n := d.writeCount()
	for i := int64(2); i < 8; i++ {
		d.set("G1.Raw", i)
		r.Scan()
		wantPath(t, r, "G1.Twice", 2*i)
	}
	if got := d.writeCount(); got != n {
		t.Errorf("%d pushes while only an expression changed, want none", got-n)
	}
	if d.keys["G1.Cmd"] {
		t.Error("the to: member was handed over though nothing wrote it")
	}
}

// The commit lands only the expression leaves, onto the value the store
// holds then: a memory member written while the stage computes is kept.
func TestExprStageCommitKeepsAWriteMadeMeanwhile(t *testing.T) {
	r, d := newGaugeRuntime(t, "", gaugeDef())
	deliver(d, 1, 0, 1)
	exprBeforeCommit = func() { write(t, r, "G1.Note", 3.5) }
	defer func() { exprBeforeCommit = nil }()
	r.Scan()
	exprBeforeCommit = nil
	wantPath(t, r, "G1.Note", 3.5)
	wantPath(t, r, "G1.Twice", int64(2))
}

// An expression reads project tags and other instances, read-only; a tag
// the store does not hold yet keeps it from running, with no fault.
func TestExprStageReadsTagsAndOtherInstances(t *testing.T) {
	def := gaugeDef()
	def.Members["Ok"] = MemberDef{Kind: MemberExpr, Expr: "Limit > 2 AND G2.Ok"}
	g2 := gaugeDef()
	r, err := New(Options{
		Program: "PROGRAM Main\nEND_PROGRAM", Libraries: []string{gaugeLib}, Driver: &countDriver{},
		Tags:   []TagDef{InstanceTag("G1", "Gauge", def), InstanceTag("G2", "Gauge", g2), Typed("Limit", RoleInput, "INT")},
		Inputs: []string{"Limit"}, Substitutable: []string{"Limit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := r.driver.(*countDriver)
	log := logBuf(r)
	deliver(d, 0, 0, 1)
	d.set("G2.Raw", int64(1))
	d.set("G2.Idx", int64(0))
	d.set("G2.Div", int64(1))
	r.Scan()
	wantPath(t, r, "G1.Ok", false)
	if q := r.TagQuality("G1.Ok"); q != nio.NotConnected {
		t.Errorf("G1.Ok before Limit arrived: %v, want NotConnected", q)
	}
	// Only the tag changes, nothing is delivered to G1: the stage's commit
	// is a new value, never an edit of the one the store (and a reader)
	// holds.
	old, _ := r.Tags().ReadGlobal("G1")
	d.set("Limit", int64(3))
	r.Scan()
	wantPath(t, r, "G1.Ok", true)
	for i, n := range r.tags.instanceOf("G1").root.names {
		if n == "Ok" && old.Fld[i].B {
			t.Error("the stage changed a value a reader already held")
		}
	}
	if q := r.TagQuality("G1.Ok"); q != nio.Good {
		t.Errorf("G1.Ok once computed: %v, want Good", q)
	}
	// A substitute on the tag it reads: the expression is Substituted,
	// asked either way.
	if _, err := r.Substitute("Limit", SubstituteRequest{Value: int64(5), For: time.Hour, By: "op"}); err != nil {
		t.Fatal(err)
	}
	r.Scan()
	if q := r.TagQuality("G1.Ok"); q != nio.Substituted {
		t.Errorf("TagQuality(G1.Ok) reading a substituted tag: %v", q)
	}
	if q := r.Quality()["G1.Ok"]; q != nio.Substituted {
		t.Errorf("Quality()[G1.Ok] reading a substituted tag: %v", q)
	}
	if strings.Contains(log.String(), "fault") {
		t.Errorf("a tag not delivered yet faulted:\n%s", log)
	}
}

// What the stage would compile must compile, at New and at every install
// (Swap, SwapWarm, Check): a refusal names the member and the fix, and the
// running program and stage are untouched. A FUNCTION an expression calls
// that changes is recompiled with the install; Rollback brings back the
// stage that ran with the program it restores.
func TestExprStageFollowsInstalls(t *testing.T) {
	r, d := newGaugeRuntime(t, "", gaugeDef())
	deliver(d, 3, 0, 1)
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(6))
	src := r.prog.Source()
	triple := strings.Replace(src, "Double := x * 2;", "Double := x * 3;", 1)
	if _, err := r.prog.SwapWarm(triple); err != nil {
		t.Fatal(err)
	}
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(9))
	looped := strings.Replace(src, "Double := x * 2;", "WHILE x > 100 DO x := x - 1; END_WHILE;\nDouble := x * 2;", 1)
	for name, install := range map[string]func() error{
		"Check":    func() error { return r.prog.Check(looped) },
		"SwapWarm": func() error { _, err := r.prog.SwapWarm(looped); return err },
		"Swap":     func() error { return r.prog.Swap(looped) },
		"layoutOf": func() error { _, err := r.prog.layoutOf(looped); return err },
	} {
		err := install()
		if err == nil || !strings.Contains(err.Error(), "expr: member G1.Twice") || !strings.Contains(err.Error(), "WHILE loop") || !strings.Contains(err.Error(), "fix the function or the expr:") {
			t.Errorf("%s: %v", name, err)
		}
	}
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(9)) // the install was refused whole
	if _, err := r.prog.Rollback(); err != nil {
		t.Fatal(err)
	}
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(6))
}

// New refuses a stage that cannot run, naming the member: a cycle, a
// compile error, an impure function, an EN-gated call.
func TestExprStageRefusedAtNew(t *testing.T) {
	for name, c := range map[string]struct{ member, expr, want string }{
		"cycle":       {"Ok", "Both", "the expr: members G1.Both, G1.Ok read each other in a cycle"},
		"compile":     {"Ok", "Nope > 1", `expr: member G1.Ok (Nope > 1): does not compile: undeclared identifier "Nope"`},
		"narrowing":   {"Ok", "Raw", "expr: member G1.Ok (G1.Raw): does not compile: cannot assign INT to BOOL"},
		"system fn":   {"Ok", "FIRST_SCAN()", "calls the system function FIRST_SCAN()"},
		"EN":          {"Twice", "Double(EN := TRUE, x := Raw)", "EN/ENO"},
		"two exprs":   {"Ok", "TRUE; Raw := 1", "is not one expression"},
		"empty":       {"Ok", " ", "an expression with no Expr"},
		"not an expr": {"Raw", "1", "Expr is for a MemberExpr"},
	} {
		def := gaugeDef()
		m := def.Members[c.member]
		m.Expr = c.expr
		def.Members[c.member] = m
		_, err := New(Options{Program: "PROGRAM Main\nEND_PROGRAM", Libraries: []string{gaugeLib}, Driver: &countDriver{}, Tags: []TagDef{InstanceTag("G1", "Gauge", def)}})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// Quality is the worst of what an expression read, and the root rolls its
// expression members in.
func TestExprStageQualityIsItsInputs(t *testing.T) {
	r, d := newGaugeRuntime(t, "", gaugeDef())
	deliver(d, 1, 0, 1)
	r.Scan()
	if q := r.Quality(); len(q) != 0 {
		t.Fatalf("all delivered: Quality() = %v, want empty", q)
	}
	d.setQ("G1.Raw", nio.Stale)
	for _, m := range []string{"G1.Ok", "G1.Twice", "G1.Both"} { // Both reads Ok
		if q := r.TagQuality(m); q != nio.Stale {
			t.Errorf("TagQuality(%s) = %v, want Stale", m, q)
		}
		if q := r.Quality()[m]; q != nio.Stale {
			t.Errorf("Quality()[%s] = %v, want Stale", m, q)
		}
	}
	if q := r.TagQuality("G1.Ratio"); q != nio.Good {
		t.Errorf("G1.Ratio reads Div only: %v, want Good", q)
	}
	if q := r.TagQuality("G1"); q != nio.Stale {
		t.Errorf("G1 = %v, want Stale", q)
	}
	if q := r.Quality()["G1"]; q != nio.Stale {
		t.Errorf("Quality()[G1] = %v, want Stale", q)
	}
}

// A takeover's image never brings an expression's value: the new leader
// computes it on its first scan.
func TestExprStageIsNeverCarried(t *testing.T) {
	r, _ := newGaugeRuntime(t, "", gaugeDef())
	v, _ := r.Tags().ReadGlobal("G1")
	in := r.tags.instanceOf("G1")
	okAt, noteAt := -1, -1
	for i, n := range in.root.names {
		switch n {
		case "Ok":
			okAt = i
		case "Note":
			noteAt = i
		}
	}
	img := ir.CopyValue(v)
	img.Fld[okAt] = ir.BoolVal(true)
	img.Fld[noteAt] = ir.RealVal(2.5)
	r.tags.installCarried("G1", img)
	wantPath(t, r, "G1.Ok", false)
	wantPath(t, r, "G1.Note", 2.5)
}

// The steady state allocates nothing more with expressions than without,
// and nor does a member faulting every scan (the VM hands back the same
// index error, ir.IndexError, and the stage builds no text per scan).
func TestExprStageAllocatesNothing(t *testing.T) {
	plain := InstanceDef{Members: map[string]MemberDef{"Raw": {Kind: MemberFrom}, "Idx": {Kind: MemberFrom}, "Div": {Kind: MemberFrom}, "Cmd": {Kind: MemberTo}}}
	measure := func(def InstanceDef, idx int64) float64 {
		r, d := newGaugeRuntime(t, "Seen := G1.Ok;", def)
		write(t, r, "G1.Vals[1]", 5.0)
		deliver(d, 3, idx, 4)
		r.Scan()
		r.Scan()
		return testing.AllocsPerRun(100, r.Scan)
	}
	base := measure(plain, 1)
	if with := measure(gaugeDef(), 1); with != base {
		t.Errorf("a scan with 5 expressions: %v allocs, without: %v", with, base)
	}
	if faulting := measure(gaugeDef(), 9); faulting != base {
		t.Errorf("a scan with a member faulting every scan: %v allocs, without expressions: %v", faulting, base)
	}
}

// BenchmarkExprStage is the stage's cost per expression (five, one of them
// a FUNCTION call), on the steady path: go test -run X -bench ExprStage.
func BenchmarkExprStage(b *testing.B) {
	r, d := newGaugeRuntime(b, "", gaugeDef())
	if err := r.Tags().Write([]WriteOp{{Path: "G1.Vals[1]", Value: 5.0}})[0]; err != nil {
		b.Fatal(err)
	}
	deliver(d, 3, 1, 4)
	r.Scan()
	now := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.runExprs(now)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/5, "ns/expr")
}

// An expression reading a whole instance (passed to a FUNCTION) waits for
// every device member of it, and takes its quality.
func TestExprStageReadsAWholeInstance(t *testing.T) {
	def := gaugeDef()
	def.Members["Twice"] = MemberDef{Kind: MemberExpr, Expr: "RawOf(G2)"}
	d := &countDriver{}
	r, err := New(Options{
		Program: "PROGRAM Main\nEND_PROGRAM", Libraries: []string{gaugeLib}, Driver: d,
		Tags: []TagDef{InstanceTag("G1", "Gauge", def), InstanceTag("G2", "Gauge", gaugeDef())},
	})
	if err != nil {
		t.Fatal(err)
	}
	deliver(d, 1, 0, 1)
	d.set("G2.Raw", int64(7))
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(0)) // G2.Idx and G2.Div not delivered yet
	if q := r.TagQuality("G1.Twice"); q != nio.NotConnected {
		t.Errorf("G1.Twice before G2 arrived: %v, want NotConnected", q)
	}
	d.set("G2.Idx", int64(0))
	d.set("G2.Div", int64(1))
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(7))
	d.setQ("G2.Div", nio.Stale)
	if q := r.TagQuality("G1.Twice"); q != nio.Stale {
		t.Errorf("G1.Twice reading G2 whole, G2.Div Stale: %v", q)
	}
}

// Tags.Given counts an instance's device members as delivered (a test's
// given:), the whole instance or one member.
func TestTagsGivenCountsAsDelivered(t *testing.T) {
	r, _ := newGaugeRuntime(t, "", gaugeDef())
	r.Tags().Given("G1", []string{"Raw"})
	r.Tags().Set("G1.Raw", int64(3))
	r.Scan()
	wantPath(t, r, "G1.Twice", int64(6))
	wantPath(t, r, "G1.Ratio", int64(0)) // Div was not given
	r.Tags().Given("G1", []string{""})
	r.Tags().Set("G1.Div", int64(4))
	r.Scan()
	wantPath(t, r, "G1.Ratio", int64(25))
}
