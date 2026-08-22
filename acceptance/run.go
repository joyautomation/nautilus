package acceptance

// Running a suite. Each test gets a FRESH resource — recompiled, reseeded,
// on a clock stopped at Epoch — so tests cannot leak state into each other
// and can be read in any order.
//
// The fixture is never restated. It comes from the manifest that deploys,
// so a retuned gain or a renamed tag cannot drift away from what the tests
// verify: there is only one declaration of it.

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// Result is one test's outcome.
type Result struct {
	Suite   string        `json:"suite"`
	Name    string        `json:"name"`
	Line    int           `json:"line"`
	Passed  bool          `json:"passed"`
	Elapsed time.Duration `json:"-"` // virtual time the test covered
	Scans   int           `json:"scans"`
	Failure *Failure      `json:"failure,omitempty"`
}

// Failure says what went wrong, where, and — for anything time-dependent —
// what the process was actually doing when it did.
type Failure struct {
	Step   int           `json:"step"` // 1-based
	Line   int           `json:"line"`
	At     time.Duration `json:"-"`
	AtMs   float64       `json:"atMs"`
	Reason string        `json:"reason"`
	Detail string        `json:"detail,omitempty"`
	Trace  []TraceTag    `json:"trace,omitempty"`
}

// TraceTag is one tag's trajectory over the failing step: the answer to
// "what did it actually do", which is the only question worth asking when
// a settling or delay assertion fails.
type TraceTag struct {
	Name    string    `json:"name"`
	Unit    string    `json:"unit,omitempty"`
	Desc    string    `json:"desc,omitempty"`
	At      []float64 `json:"atMs"`
	Values  []string  `json:"values"`
	stepped bool
}

// TB is the slice of *testing.T the Go tier needs. Kept as an interface so
// this package — which the CLI links — never imports testing.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Logf(format string, args ...any)
}

// Run executes every suite under fsys against opts and reports failures to
// tb. This is the Go tier's entry point:
//
//	func TestAcceptance(t *testing.T) {
//	    proj, _ := project.Load(os.DirFS("."), "")
//	    acceptance.Run(t, os.DirFS("."), proj.Runtime)
//	}
func Run(tb TB, fsys fs.FS, opts runtime.Options) {
	tb.Helper()
	results, err := RunDir(fsys, opts)
	if err != nil {
		tb.Errorf("acceptance: %v", err)
		return
	}
	for _, r := range results {
		if r.Passed {
			tb.Logf("ok   %s (%s, %d scans)", r.Name, r.Elapsed, r.Scans)
			continue
		}
		tb.Errorf("FAIL %s\n%s", r.Name, indent(FormatFailure(r), "     "))
	}
}

// RunDir discovers and runs every `*_test.yaml` under fsys.
func RunDir(fsys fs.FS, opts runtime.Options) ([]Result, error) {
	paths, err := DiscoverSuites(fsys)
	if err != nil {
		return nil, err
	}
	var out []Result
	for _, p := range paths {
		suite, err := LoadSuite(fsys, p)
		if err != nil {
			return nil, err
		}
		rs, err := RunSuite(suite, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, rs...)
	}
	return out, nil
}

// RunSuite runs one loaded suite.
func RunSuite(s *Suite, opts runtime.Options) ([]Result, error) {
	out := make([]Result, 0, len(s.Tests))
	for _, t := range s.Tests {
		r, err := runTest(s, t, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// testRun is one test's world: its own runtime, driver, and clock.
type testRun struct {
	rt     *runtime.Runtime
	sch    *Scheduler
	drv    *nio.Memory
	libs   []string
	tol    float64
	inputs map[string]bool // tags the driver produces — `given` routes there
	known  map[string]bool
	types  map[string]string // tag name → declared UDT, for field-level `given`
	meta   map[string]runtime.TagMeta
	preds  map[string]*predicate
}

func runTest(s *Suite, t *Test, opts runtime.Options) (Result, error) {
	res := Result{Suite: s.Path, Name: t.Name, Line: t.Line}

	// A stub driver, always — whatever the manifest configures. Tests never
	// open a socket, so a project bound to real hardware is fully testable
	// on a laptop with nothing on the network.
	o := opts
	drv := nio.NewMemory()
	o.Driver = drv

	rt, sch, err := NewRuntime(o)
	if err != nil {
		return res, fmt.Errorf("%s: compile: %w", s.Path, err)
	}
	r := &testRun{
		rt: rt, sch: sch, drv: drv, libs: o.Libraries,
		tol:    firstNonZero(deref(t.Tolerance), s.Tolerance),
		inputs: inputSet(o),
		known:  knownTags(o),
		types:  declaredTypes(o),
		// From the runtime, not the Options: role-declared tags carry their
		// unit and description in TagDef, and only New() merges those into
		// the flat meta map a trace reads.
		meta:  rt.Meta(),
		preds: map[string]*predicate{},
	}

	suspend := t.Suspend
	if suspend == nil {
		suspend = s.Suspend
	}
	if err := sch.Suspend(suspend...); err != nil {
		return res, fmt.Errorf("%s: test %q: %w", s.Path, t.Name, err)
	}

	if err := r.apply(t.Given); err != nil {
		return res, fmt.Errorf("%s:%d: test %q: %w", s.Path, t.Line, t.Name, err)
	}

	steps, err := t.steps()
	if err != nil {
		return res, err
	}
	for i, st := range steps {
		fail, err := r.runStep(st)
		if err != nil {
			return res, fmt.Errorf("%s:%d: test %q: %w", s.Path, st.Line, t.Name, err)
		}
		if fail != nil {
			fail.Step = i + 1
			fail.Line = st.Line
			fail.AtMs = float64(fail.At) / float64(time.Millisecond)
			res.Failure = fail
			res.Elapsed, res.Scans = sch.Elapsed(), sch.ScanCount(runtime.MainTaskName)
			return res, nil
		}
	}

	// The assertion nobody has to write: a scan that faulted fails the test
	// that ran it.
	if n, last := sch.LogicErrors(); n > 0 {
		res.Failure = &Failure{
			At:     sch.Elapsed(),
			AtMs:   float64(sch.Elapsed()) / float64(time.Millisecond),
			Reason: fmt.Sprintf("%d logic error(s) during the run", n),
			Detail: last,
		}
		res.Elapsed, res.Scans = sch.Elapsed(), sch.ScanCount(runtime.MainTaskName)
		return res, nil
	}

	res.Passed = true
	res.Elapsed, res.Scans = sch.Elapsed(), sch.ScanCount(runtime.MainTaskName)
	return res, nil
}

// runStep applies the step's writes, spends its virtual time, and checks
// what must hold. A nil Failure means it passed.
func (r *testRun) runStep(st *Step) (*Failure, error) {
	if err := r.apply(st.Given); err != nil {
		return nil, err
	}

	tr := r.newTracer(st)
	var alwaysFail *Failure
	var alwaysErr error

	r.sch.OnTick = func() {
		tr.sample(r.sch.Elapsed())
		if st.Always == nil || alwaysFail != nil || alwaysErr != nil {
			return
		}
		ok, detail, err := r.check(st.Always)
		if err != nil {
			alwaysErr = err
			return
		}
		if !ok {
			alwaysFail = &Failure{
				At:     r.sch.Elapsed(),
				Reason: "invariant broke",
				Detail: detail,
			}
		}
	}
	defer func() { r.sch.OnTick = nil }()

	tr.sample(r.sch.Elapsed())

	switch {
	case st.Until != nil:
		var perr error
		ok, _ := r.sch.AdvanceUntil(st.Until.get(), st.Hold.get(), func() bool {
			if perr != nil {
				return false
			}
			held, _, err := r.check(st.Expect)
			if err != nil {
				perr = err
			}
			return held
		})
		if perr != nil {
			return nil, perr
		}
		if alwaysErr != nil {
			return nil, alwaysErr
		}
		if alwaysFail != nil {
			alwaysFail.Trace = tr.result()
			return alwaysFail, nil
		}
		if !ok {
			_, detail, err := r.check(st.Expect)
			if err != nil {
				return nil, err
			}
			reason := fmt.Sprintf("never held within %s", st.Until.get())
			if st.Hold.get() > 0 {
				reason = fmt.Sprintf("never held for %s within %s", st.Hold.get(), st.Until.get())
			}
			return &Failure{At: r.sch.Elapsed(), Reason: reason, Detail: detail, Trace: tr.result()}, nil
		}
	case st.Advance != nil:
		r.sch.Advance(st.Advance.get())
	case st.Scans != nil:
		if err := r.sch.Scans(*st.Scans); err != nil {
			return nil, err
		}
	}

	if alwaysErr != nil {
		return nil, alwaysErr
	}
	if alwaysFail != nil {
		alwaysFail.Trace = tr.result()
		return alwaysFail, nil
	}
	if st.Until == nil && st.Expect != nil {
		ok, detail, err := r.check(st.Expect)
		if err != nil {
			return nil, err
		}
		if !ok {
			return &Failure{
				At: r.sch.Elapsed(), Reason: "expectation failed",
				Detail: detail, Trace: tr.result(),
			}, nil
		}
	}
	return nil, nil
}

// check evaluates every term; the first that fails renders the detail.
func (r *testRun) check(e *Expect) (bool, string, error) {
	if e == nil {
		return true, "", nil
	}
	for _, term := range e.Terms {
		if term.Expr != "" {
			p, err := r.predicate(term.Expr)
			if err != nil {
				return false, "", err
			}
			ok, err := p.eval(r.rt)
			if err != nil {
				return false, "", err
			}
			if !ok {
				return false, fmt.Sprintf("%s   is false", term.Expr), nil
			}
			continue
		}
		got, err := r.value(term.Tag)
		if err != nil {
			return false, "", err
		}
		ok, want := term.Matcher.match(got, r.tol)
		if !ok {
			return false, fmt.Sprintf("%s = %s, want %s", term.Tag, show(got), want), nil
		}
	}
	return true, "", nil
}

// predicate compiles an ST expression once and reuses it — an `until` loop
// evaluates it on every tick.
func (r *testRun) predicate(expr string) (*predicate, error) {
	if p, ok := r.preds[expr]; ok {
		return p, nil
	}
	p, err := compilePredicate(r.rt, r.libs, expr)
	if err != nil {
		return nil, err
	}
	r.preds[expr] = p
	return p, nil
}

// value resolves a name to a stored value. A dotted name is ambiguous once
// tags can be UDTs — `P101.Speed` is a field, `main.err` is a task local —
// so the order is fixed and tag-first:
//
//  1. an exact tag name (a tag literally called "A.B" wins; unambiguous)
//  2. <tag>.<field>… when the head names a tag holding a struct
//  3. <task>.<local> when the head names a task
//  4. otherwise an error listing BOTH namespaces, since the writer cannot
//     tell from the name alone which one they meant to reach
//
// Tags come first because they are the manifest's namespace and the thing a
// test is usually about; a task local is the more specialized reach.
func (r *testRun) value(name string) (ir.Value, error) {
	if v, err := r.rt.Tags().ReadGlobal(name); err == nil {
		return v, nil
	}
	head, rest, dotted := strings.Cut(name, ".")
	if !dotted {
		return ir.Value{}, fmt.Errorf("no tag %q in this project", name)
	}
	if root, err := r.rt.Tags().ReadGlobal(head); err == nil {
		// The walk itself is Tags.ReadPath now — the same promotion the
		// design brief asks for, so a test, the alarm engine and the API
		// resolve a member path identically. ReadPath only needs true/false
		// (an HTTP handler has no prose to show a caller), but a test
		// author staring at a typo needs to know WHICH segment failed and
		// what the struct actually has — fieldMissErr redoes that one walk
		// purely to build the message, only on the (rare, already-failed)
		// error path.
		if v, ok := r.rt.Tags().ReadPath(name); ok {
			return irValue(v)
		}
		return ir.Value{}, fieldMissErr(root, head, rest)
	}
	if prog := r.rt.TaskProgram(head); prog != nil {
		v, ok := prog.Locals()[rest]
		if !ok {
			return ir.Value{}, fmt.Errorf("task %s has no local %q", head, rest)
		}
		return irValue(v)
	}
	return ir.Value{}, fmt.Errorf("no tag or task %q in this project (tags: %s; tasks: %s)",
		head, strings.Join(sortedKeys(anyMap(r.known)), ", "), strings.Join(r.rt.TaskNames(), ", "))
}

// fieldMissErr reconstructs the reason a dotted path did not resolve via
// Tags.ReadPath, for a test-writer-friendly message: which segment failed,
// and what the struct at that point actually has. path may be several
// segments deep ("Motor.Drive.Speed"); each step reports what it could not
// find and what was available, because a wrong field name in a test is
// otherwise indistinguishable from a wrong value.
func fieldMissErr(v ir.Value, sofar, path string) error {
	for path != "" {
		var field string
		field, path, _ = strings.Cut(path, ".")
		if v.Kind != ir.TypeStruct || v.Struct == nil {
			return fmt.Errorf("%s is not a struct, so it has no field %q", sofar, field)
		}
		i, ok := v.Struct.FieldIndex[field]
		if !ok || i >= len(v.Fld) {
			return fmt.Errorf("%s has no field %q (%s has: %s)",
				sofar, field, v.Struct.Name, strings.Join(fieldNames(v.Struct), ", "))
		}
		v, sofar = v.Fld[i], sofar+"."+field
	}
	// Unreachable in practice: ReadPath already walked this exact path and
	// found nothing wrong, so this branch has nothing to report.
	return fmt.Errorf("%s: could not resolve", sofar)
}

func fieldNames(sd *ir.StructDef) []string {
	out := make([]string, 0, len(sd.Fields))
	for _, f := range sd.Fields {
		out = append(out, f.Name)
	}
	return out
}

func anyMap(m map[string]bool) map[string]any {
	out := make(map[string]any, len(m))
	for k := range m {
		out[k] = nil
	}
	return out
}

// apply writes the step's given values, routing each by the role the
// manifest already declared: a driver-fed input goes to the input image,
// where the next scan picks it up exactly as the field would; everything
// else goes straight to the tag store.
//
// Dotted field writes are gathered per root tag rather than applied one at a
// time: see applyFields for why a single map's fields must compose onto one
// base value instead of each re-reading the store independently.
func (r *testRun) apply(given map[string]any) error {
	fieldEdits := map[string]map[string]any{} // root tag → path → raw
	for _, name := range sortedKeys(given) {
		if !r.known[name] {
			// A dotted name may address one field of a UDT tag. Resolution
			// order matches `expect` (value()): whole tag first, then field.
			if head, path, dotted := strings.Cut(name, "."); dotted && r.known[head] {
				if fieldEdits[head] == nil {
					fieldEdits[head] = map[string]any{}
				}
				fieldEdits[head][path] = given[name]
				continue
			}
			return fmt.Errorf("given: no tag %q in this project", name)
		}
		v := r.coerce(name, given[name])
		if r.inputs[name] {
			if err := r.drv.WriteOutputs(nio.Values{name: v}); err != nil {
				return err
			}
			continue
		}
		r.rt.Tags().Set(name, v)
	}
	// Every root tag's field edits are applied — and written — as one unit,
	// after the whole-tag writes above. Map iteration order is undefined, so
	// the root tags are visited in sorted order for a deterministic result
	// when a test (wrongly, but harmlessly now) gives both a whole tag and
	// one of its fields in the same map.
	tags := make([]string, 0, len(fieldEdits))
	for tag := range fieldEdits {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		if err := r.applyFields(tag, fieldEdits[tag]); err != nil {
			return fmt.Errorf("given: %w", err)
		}
	}
	return nil
}

// applyFields sets every field a single `given:` map addressed on one struct
// tag: read the current value ONCE, replace every field on that one copy,
// write the whole tag back ONCE — the tag store and the driver image both
// hold whole tags, so a partial write is not possible.
//
// Applying edits one at a time here used to be a trap: each call re-read
// the tag store as its base, but an INPUT tag's `given` writes land in the
// driver's image, not the tag store — nothing promotes it there before the
// next scan. So `{X.HH: true, X.H: true}` in one map raced: both reads saw
// the same stale (usually zero-of-type) base, and the driver's last write
// replaced the whole tag, silently discarding every edit but one. Gathering
// every field for a tag before reading the base — and writing only once —
// makes them compose regardless of routing or map iteration order.
//
// The base value's other wrinkle: a typed INPUT is deliberately unseeded by
// the runtime, so on the first `given` there is nothing to modify. The
// harness materialises zero-of-type for that case and production does not:
// a test that never delivers a value fails its own `expect`, so the
// loud-fault argument that keeps inputs unseeded does not apply here. It
// happens lazily, only for a tag a test actually addresses by field, so no
// test that does not use this feature changes behaviour.
func (r *testRun) applyFields(tag string, edits map[string]any) error {
	base, err := r.rt.Tags().ReadGlobal(tag)
	if err != nil {
		typeName, typed := r.types[tag]
		if !typed {
			return fmt.Errorf("%s has no value yet and no declared type, so its "+
				"field cannot be set — give the whole tag instead", tag)
		}
		t, ok := r.rt.Types()[typeName]
		if !ok {
			return fmt.Errorf("%s is declared as %s, which this project does not declare", tag, typeName)
		}
		base = ir.Zero(t)
	}
	paths := make([]string, 0, len(edits))
	for path := range edits {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		// ir.SetField is the shared dotted-write primitive: the same
		// read-modify-write the HTTP API's member writes use (Tags.SetPath),
		// so a `given:` path and an operator's POST resolve, coerce and fail
		// identically.
		updated, err := ir.SetField(base, strings.Split(path, "."), edits[path], tag)
		if err != nil {
			return err
		}
		base = updated
	}
	if r.inputs[tag] {
		return r.drv.WriteOutputs(nio.Values{tag: base})
	}
	r.rt.Tags().Set(tag, base)
	return nil
}

// declaredTypes maps tag name → declared UDT name, for the tags that have one.
func declaredTypes(o runtime.Options) map[string]string {
	m := map[string]string{}
	for _, d := range o.Tags {
		if d.Type != "" {
			m[d.Name] = d.Type
		}
	}
	return m
}

// coerce keeps a write in the tag's own type: YAML has one number kind, the
// tag store distinguishes DINT from REAL, and silently retyping a tag
// mid-test would be a nasty way to lose an afternoon.
func (r *testRun) coerce(name string, v any) any {
	cur, err := r.rt.Tags().ReadGlobal(name)
	if err == nil {
		switch cur.Kind {
		case ir.TypeReal:
			if f, ok := toFloat(v); ok {
				return f
			}
		case ir.TypeInt, ir.TypeTime:
			if f, ok := toFloat(v); ok {
				return int64(f)
			}
		}
	}
	if f, ok := v.(int); ok {
		return float64(f)
	}
	return v
}

// ─── failure traces ────────────────────────────────────────────────────

const traceSamples = 8 // columns in a printed trace

type tracer struct {
	r     *testRun
	names []string
	at    []time.Duration
	vals  [][]ir.Value
	cap   int
}

// newTracer picks the tags a failure would need to explain itself: every
// tag the step asserts on, plus any tag named inside an ST expression.
func (r *testRun) newTracer(st *Step) *tracer {
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		if n == "" || seen[n] || !r.known[n] {
			return
		}
		seen[n] = true
		names = append(names, n)
	}
	for _, e := range []*Expect{st.Expect, st.Always} {
		if e == nil {
			continue
		}
		for _, term := range e.Terms {
			add(term.Tag)
			for _, id := range identifiers(term.Expr) {
				add(id)
			}
		}
	}
	return &tracer{r: r, names: names, cap: 4096}
}

func (t *tracer) sample(at time.Duration) {
	if len(t.names) == 0 || len(t.at) >= t.cap {
		return
	}
	row := make([]ir.Value, len(t.names))
	for i, n := range t.names {
		v, err := t.r.value(n)
		if err == nil {
			row[i] = v
		}
	}
	t.at = append(t.at, at)
	t.vals = append(t.vals, row)
}

// result downsamples to a handful of evenly spaced columns — enough to see
// the shape of the trajectory, few enough to read in a CI log.
func (t *tracer) result() []TraceTag {
	if len(t.at) == 0 {
		return nil
	}
	idx := pickSamples(len(t.at), traceSamples)
	out := make([]TraceTag, len(t.names))
	for i, n := range t.names {
		tt := TraceTag{Name: n}
		if m, ok := t.r.meta[n]; ok {
			tt.Unit, tt.Desc = m.Unit, m.Desc
		}
		for _, j := range idx {
			tt.At = append(tt.At, float64(t.at[j])/float64(time.Millisecond))
			tt.Values = append(tt.Values, show(t.vals[j][i]))
		}
		out[i] = tt
	}
	return out
}

func pickSamples(n, want int) []int {
	if n <= want {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	out := make([]int, want)
	for i := range want {
		out[i] = i * (n - 1) / (want - 1)
	}
	return out
}

var identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

func identifiers(expr string) []string {
	if expr == "" {
		return nil
	}
	return identRe.FindAllString(expr, -1)
}

// ─── small helpers ─────────────────────────────────────────────────────

func inputSet(o runtime.Options) map[string]bool {
	m := map[string]bool{}
	for _, n := range o.Inputs {
		m[n] = true
	}
	for _, d := range o.Tags {
		if d.Role == runtime.RoleInput {
			m[d.Name] = true
		}
	}
	return m
}

func knownTags(o runtime.Options) map[string]bool {
	m := map[string]bool{}
	for _, n := range o.Inputs {
		m[n] = true
	}
	for _, n := range o.Outputs {
		m[n] = true
	}
	for n := range o.Seed {
		m[n] = true
	}
	for _, d := range o.Tags {
		m[d.Name] = true
	}
	return m
}

func irValue(v any) (ir.Value, error) {
	switch x := v.(type) {
	case bool:
		return ir.BoolVal(x), nil
	case float64:
		return ir.RealVal(x), nil
	case int:
		return ir.IntVal(int64(x)), nil
	case int64:
		return ir.IntVal(x), nil
	case string:
		return ir.StringVal(x), nil
	case ir.Value:
		return x, nil
	}
	return ir.Value{}, fmt.Errorf("cannot compare a %T", v)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func firstNonZero(vals ...float64) float64 {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

func indent(s, pad string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}
