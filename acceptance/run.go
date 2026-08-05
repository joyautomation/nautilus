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
//	    proj, _ := project.Load(os.DirFS("."))
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

// value resolves a name to a stored value: a tag, or `task.local` for a
// program's retained internal state.
func (r *testRun) value(name string) (ir.Value, error) {
	if task, local, dotted := strings.Cut(name, "."); dotted {
		prog := r.rt.TaskProgram(task)
		if prog == nil {
			return ir.Value{}, fmt.Errorf("no task %q in this project (tasks: %s)", task, strings.Join(r.rt.TaskNames(), ", "))
		}
		v, ok := prog.Locals()[local]
		if !ok {
			return ir.Value{}, fmt.Errorf("task %s has no local %q", task, local)
		}
		return irValue(v)
	}
	v, err := r.rt.Tags().ReadGlobal(name)
	if err != nil {
		return ir.Value{}, fmt.Errorf("no tag %q in this project", name)
	}
	return v, nil
}

// apply writes the step's given values, routing each by the role the
// manifest already declared: a driver-fed input goes to the input image,
// where the next scan picks it up exactly as the field would; everything
// else goes straight to the tag store.
func (r *testRun) apply(given map[string]any) error {
	for _, name := range sortedKeys(given) {
		if !r.known[name] {
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
	return nil
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
