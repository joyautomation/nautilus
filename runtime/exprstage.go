package runtime

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// The expression stage (struct wiring, #1458 slice 8b; one-struct-model §6):
// every instance's expr: members, evaluated once per scan of the main task,
// after the inputs are delivered and the substitutes expire, before its
// program runs. A standby does not scan, so it does not evaluate; the other
// tasks read the values the last main scan committed.
//
//   - One unit per member, each its own ir.Program over one shared frame, so
//     a fault in one (an index out of range, a division by zero) stops only
//     that one: it keeps its last value, its quality is Bad, the others and
//     the programs run on. A fault is logged when it starts and when it
//     clears, never once per scan.
//   - The units run in dependency order, and each result goes into the
//     stage's view, so an expression reading another reads this scan's
//     value. A forced member is not evaluated: it and whatever reads it see
//     the force.
//   - Until everything it reads has arrived (each delivered member it reads
//     delivered once, or covered by a force or a substitute; each tag it
//     reads in the store; each expression it reads computed), a unit does
//     not run: it stays at its zero (FALSE), and its quality is
//     NotConnected.
//   - One commit lands only the expression leaves that changed, onto the
//     tag's live value under the store lock, so a memory or command member
//     an operator or another task wrote meanwhile is kept. It is no write by
//     a program: nothing is marked written for the push rules and the output
//     generation does not move, so a changing expression sends nothing.
//   - Quality is computed when asked (exprQuality), never in the scan.
//
// The units compile from the main program's own source (its library
// prelude: the FUNCTIONs and TYPEs), so every install of the main program
// (Swap, SwapWarm, Check, Rollback, the hot-takeover precheck) compiles the
// stage too, and an install that breaks an expression is refused whole,
// naming the member.

// exprSet is the stage's description, fixed at New, and the stage compiled
// for the main program's current source.
type exprSet struct {
	units  []exprUnit
	order  []int
	byInst map[*instInfo][]int

	// Under the main Program's mu.
	stage, prev *exprStage

	// mu guards each unit's fault record, which Stats reads.
	mu sync.Mutex
}

// exprUnit is one instance's expr: member.
type exprUnit struct {
	in    *instInfo
	node  *knode
	idx   []int  // the member's positions below the root
	full  string // "P101.Healthy"
	text  string // rewritten onto the instance ("P101.Running AND NOT P101.Fault")
	reads []exprRead
	deps  []int // the units it reads

	// The scan's state, under the main Program's mu.
	forced, gateOK, ready, ran bool
	val                        ir.Value

	// faulted and seen (it has had a value: computed once, or forced) are
	// read by quality from other goroutines; the record under exprSet.mu
	// by Stats.
	faulted atomic.Bool
	seen    atomic.Bool
	fault   exprFaultRec
}

// exprRead is one thing a unit reads: a member path of an instance (keys:
// its delivered keys at or under or over the path; units: its expressions
// there), or a plain tag.
type exprRead struct {
	in    *instInfo
	keys  []int
	units []int
	tag   string // the store's spelling; "" for an instance read
}

type exprFaultRec struct {
	msg   string
	since time.Time
	scans uint64
}

// exprStage is the stage compiled for one source.
type exprStage struct {
	units []*ir.Program // one per unit: the shared program with one statement
	slots []int         // each unit's result slot
	frame *ir.Frame
	host  *exprHost
	// roots are the host read slots of the instances the stage writes, and
	// rootUnits their units.
	roots     []int
	rootUnits [][]int
	unitRoot  []int // each unit's root read slot
	tagReads  [][]int
}

// exprHost is what the units' VM reads: the scan's snapshot of every tag the
// expressions read, by read slot.
type exprHost struct {
	store   *Tags
	idx     map[string]int // as the program spells a global → read slot
	keys    []string       // the store's spelling, per read slot
	vals    []ir.Value
	have    []bool
	own     []bool     // vals[i] is bufs[i] this scan
	bufs    []ir.Value // a private copy of a root the stage writes
	divZero bool
}

func (h *exprHost) ReadGlobal(name string) (ir.Value, error) {
	if i, ok := h.idx[name]; ok && h.have[i] {
		return h.vals[i], nil
	}
	return h.store.ReadGlobal(name)
}

func (h *exprHost) WriteGlobal(name string, _ ir.Value) error {
	return fmt.Errorf("an expression writes %s: it may only compute", name)
}

func (h *exprHost) NowMs() int64 { return h.store.NowMs() }

// DivZero is the VM's ir.DivZeroCounter: counted as any task's is, and a
// fault of the unit (§6: a division by zero leaves it at its last value).
func (h *exprHost) DivZero() {
	h.divZero = true
	h.store.DivZero()
}

var errExprDivZero = errors.New("division by zero")

// exprBeforeCommit, set only by a test, runs between the units and the
// commit: what another writer does while the stage computes.
var exprBeforeCommit func()

// newExprSet describes the expr: members of every instance, in dependency
// order. A cycle is refused, naming its members; nil when there are none.
func newExprSet(t *Tags, defs []TagDef) (*exprSet, error) {
	ex := &exprSet{byInst: map[*instInfo][]int{}}
	byName := map[string]TagDef{}
	for _, d := range defs {
		if d.Instance != nil {
			byName[ir.NameKey(d.Name)] = d
		}
	}
	var targets []string
	var reads [][]string
	for _, in := range t.instList {
		d := byName[ir.NameKey(in.name)]
		siblings := map[string]bool{}
		for _, n := range in.root.names {
			siblings[ir.NameKey(n)] = true
		}
		paths := sortedMemberPaths(d.Instance)
		for _, p := range paths {
			m := d.Instance.Members[p]
			if m.Kind != MemberExpr {
				continue
			}
			segs := ir.PathSegments(p)
			node, _ := in.nodeAt(segs)
			text, refs, err := st.RewriteExpr(in.name, func(n string) bool { return siblings[ir.NameKey(n)] }, m.Expr)
			if err != nil {
				return nil, fmt.Errorf("instance %s: member %s: expr %q is not one expression: %w", in.name, p, m.Expr, err)
			}
			idx := make([]int, len(segs))
			n := &in.root
			for i, sg := range segs {
				idx[i], _ = n.child(sg)
				n = &n.kids[idx[i]]
			}
			var rs []string
			for _, r := range refs {
				rs = append(rs, r.Full(in.name))
			}
			ex.units = append(ex.units, exprUnit{in: in, node: node, idx: idx, full: in.name + addrSuffix(node.path), text: text})
			ex.byInst[in] = append(ex.byInst[in], len(ex.units)-1)
			targets = append(targets, in.name+addrSuffix(node.path))
			reads = append(reads, rs)
		}
	}
	if len(ex.units) == 0 {
		return nil, nil
	}
	edges := st.ExprEdges(targets, reads)
	order, cycle := st.ExprOrder(edges)
	if cycle != nil {
		names := make([]string, len(cycle))
		for i, c := range cycle {
			names[i] = ex.units[c].full
		}
		return nil, fmt.Errorf("the expr: members %s read each other in a cycle: an expression cannot read itself, directly or through another expr:; break the cycle with a memory: member a program sets", strings.Join(names, ", "))
	}
	ex.order = order
	for i := range ex.units {
		u := &ex.units[i]
		u.deps = edges[i]
		for _, full := range reads[i] {
			u.reads = append(u.reads, ex.readOf(t, full))
		}
	}
	return ex, nil
}

// readOf resolves one read path to what quality and the gate look at.
func (ex *exprSet) readOf(t *Tags, full string) exprRead {
	root, segs := ir.SplitAddress(full)
	in := t.instanceOf(root)
	if in == nil {
		key, ok := t.Canonical(root)
		if !ok {
			key = root
		}
		return exprRead{tag: key}
	}
	n, _ := in.nodeAt(segs)
	whole := n == &in.root // the instance read whole (passed to a FUNCTION)
	r := exprRead{in: in}
	for i, k := range in.keys {
		if whole || pathUnder(k.node.canon, n.canon) || pathUnder(n.canon, k.node.canon) {
			r.keys = append(r.keys, i)
		}
	}
	for _, j := range ex.byInst[in] {
		c := ex.units[j].node.canon
		if whole || pathUnder(c, n.canon) || pathUnder(n.canon, c) {
			r.units = append(r.units, j)
		}
	}
	return r
}

// nodeAt resolves segs in the tree with no allocation; ok is false for a
// path the type does not have (n is then the deepest node reached).
func (in *instInfo) nodeAt(segs []string) (n *knode, ok bool) {
	n = &in.root
	for _, s := range segs {
		i, found := n.child(s)
		if !found {
			return n, false
		}
		n = &n.kids[i]
	}
	return n, true
}

func sortedMemberPaths(d *InstanceDef) []string {
	paths := make([]string, 0, len(d.Members))
	for p := range d.Members {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// compile builds the stage for program source src (the main program's,
// library prelude included) in the program's compile context: the units
// lower as one program after the source's FUNCTIONs and TYPEs, then each
// statement becomes its own unit writing a slot of its own. The purity rules
// are applied again, since an install may change a FUNCTION an expression
// calls. An error names the member, what is wrong, and the fix.
func (ex *exprSet) compile(src string, opts st.LowerOpts, t *Tags) (*exprStage, error) {
	ast, err := parseSource(src)
	if err != nil {
		return nil, err // the program's own error
	}
	us := make([]st.ExprUnit, len(ex.units))
	for i := range ex.units {
		u := &ex.units[i]
		us[i] = st.ExprUnit{Text: u.text, Target: u.full}
		e, err := st.ParseExpr(u.text)
		if err != nil {
			return nil, ex.unitErr(i, err.Error())
		}
		if ps := st.ExprPurity(ast, e); len(ps) > 0 {
			p := ps[0]
			what := p.Rule
			if p.Func != "" {
				what = "calls " + strings.Join(p.Via, " → ") + ", and " + p.Func + " " + p.Rule
			}
			return nil, ex.unitErr(i, what+"; an expression only computes from what it reads, so a function it calls must too: make it a program or a block instead")
		}
	}
	eprog, err := st.Parse(st.ExprProgram(us))
	if err != nil {
		return nil, fmt.Errorf("the expr: members could not be compiled together: %w", err)
	}
	combined := &st.Program{
		Name: eprog.Name, TopKeyword: eprog.TopKeyword, Statements: eprog.Statements,
		TypeDecls: ast.TypeDecls, FBDecls: ast.FBDecls, FuncDecls: ast.FuncDecls, GlobalConsts: ast.GlobalConsts,
	}
	low, err := st.LowerWithOpts(combined, opts)
	if err != nil {
		if le, ok := st.AsLowerError(err); ok {
			if i := st.ExprUnitOfLine(us, le.Pos.Line); i >= 0 {
				return nil, ex.unitErr(i, "does not compile: "+le.Err.Error())
			}
		}
		return nil, fmt.Errorf("the expr: members do not compile with this program: %w", err)
	}
	if len(low.Body) != len(ex.units) {
		return nil, fmt.Errorf("the expr: members compiled to %d statements for %d members", len(low.Body), len(ex.units))
	}
	shared := *low
	shared.Slots = append([]ir.VarSlot(nil), low.Slots...)
	s := &exprStage{units: make([]*ir.Program, len(ex.units)), slots: make([]int, len(ex.units)), unitRoot: make([]int, len(ex.units)), tagReads: make([][]int, len(ex.units))}
	assigns := make([]*ir.Assign, len(ex.units))
	for i, stmt := range low.Body {
		a, ok := stmt.(*ir.Assign)
		if !ok {
			return nil, ex.unitErr(i, "uses EN/ENO execution control, which an expression does not take: call the function without EN")
		}
		assigns[i] = a
		s.slots[i] = len(shared.Slots)
		shared.Slots = append(shared.Slots, ir.VarSlot{Name: fmt.Sprintf("__expr%d", i), Type: a.Target.ExprType(), Kind: ir.VarLocal})
	}
	for i, a := range assigns {
		one := shared
		one.Body = []ir.Stmt{&ir.Assign{Target: &ir.SlotRef{Slot: s.slots[i], T: a.Target.ExprType()}, Value: a.Value, Wrap: a.Wrap}}
		s.units[i] = &one
	}
	s.frame = ir.NewFrame(&shared)

	// The read slots: every global the units read, by the store's spelling
	// (two spellings of one tag share a slot).
	h := &exprHost{store: t, idx: map[string]int{}}
	slotOf := map[string]int{}
	for name := range shared.GlobalsDeep() {
		key, ok := t.Canonical(name)
		if !ok {
			key = name
		}
		i, seen := slotOf[key]
		if !seen {
			i = len(h.keys)
			slotOf[key] = i
			h.keys = append(h.keys, key)
		}
		h.idx[name] = i
	}
	for i := range ex.units {
		u := &ex.units[i]
		ri, ok := slotOf[u.in.name]
		if !ok {
			ri = len(h.keys)
			slotOf[u.in.name] = ri
			h.keys = append(h.keys, u.in.name)
			h.idx[u.in.name] = ri
		}
		s.unitRoot[i] = ri
		for _, r := range u.reads {
			if r.tag != "" {
				if j, ok := slotOf[r.tag]; ok {
					s.tagReads[i] = append(s.tagReads[i], j)
				}
			}
		}
	}
	n := len(h.keys)
	h.vals, h.have, h.own, h.bufs = make([]ir.Value, n), make([]bool, n), make([]bool, n), make([]ir.Value, n)
	at := map[int]int{}
	for i, ri := range s.unitRoot {
		k, ok := at[ri]
		if !ok {
			k = len(s.roots)
			at[ri] = k
			s.roots = append(s.roots, ri)
			s.rootUnits = append(s.rootUnits, nil)
		}
		s.rootUnits[k] = append(s.rootUnits[k], i)
	}
	s.host = h
	return s, nil
}

func (ex *exprSet) unitErr(i int, what string) error {
	return fmt.Errorf("expr: member %s (%s): %s", ex.units[i].full, ex.units[i].text, what)
}

// run is one scan of the stage. Caller holds the main Program's mu; the
// faults that started or cleared are returned for the caller to log.
func (ex *exprSet) run(t *Tags, now time.Time, events []exprEvent) []exprEvent {
	s := ex.stage
	if s == nil {
		return events
	}
	h := s.host

	// 1. The snapshot: every tag read, as served (a force shows), and which
	// units are forced or have what their delivered reads need.
	t.mu.RLock()
	for i, k := range h.keys {
		tv, ok := t.vals[k]
		h.have[i], h.own[i] = ok, false
		if ok {
			h.vals[i] = tv.v
		} else {
			h.vals[i] = ir.Value{}
		}
	}
	for i := range ex.units {
		u := &ex.units[i]
		u.forced = t.exprForcedLocked(u)
		u.gateOK = true
		for _, r := range u.reads {
			for _, k := range r.keys {
				if !r.in.got[k].Load() && !t.keyCoveredLocked(r.in, k) {
					u.gateOK = false
				}
			}
		}
	}
	t.mu.RUnlock()

	// 2. The units, in order; each result into the view.
	changed := false
	for _, i := range ex.order {
		u := &ex.units[i]
		u.ran = false
		if u.forced {
			u.ready = true
			if !u.seen.Load() {
				u.seen.Store(true)
			}
			continue
		}
		ready := u.gateOK
		for _, j := range s.tagReads[i] {
			ready = ready && h.have[j]
		}
		for _, d := range u.deps {
			ready = ready && ex.units[d].ready
		}
		u.ready = ready
		if !ready {
			continue
		}
		h.divZero = false
		err := ir.Run(s.units[i], s.frame, h)
		if err == nil && h.divZero {
			err = errExprDivZero
		}
		if err != nil {
			if !u.faulted.Load() {
				events = ex.faultStart(i, err.Error(), now, events)
			} else {
				ex.mu.Lock()
				u.fault.scans++
				ex.mu.Unlock()
			}
			continue
		}
		if u.faulted.Load() {
			events = ex.faultClear(i, now, events)
		}
		if !u.seen.Load() {
			u.seen.Store(true)
		}
		v := s.frame.Slots[s.slots[i]]
		ri := s.unitRoot[i]
		if cur, ok := valueAt(h.vals[ri], u.idx); ok && sameValue(&cur, &v) {
			continue
		}
		if !h.own[ri] {
			h.bufs[ri] = copyInto(h.bufs[ri], h.vals[ri])
			h.vals[ri], h.own[ri] = h.bufs[ri], true
		}
		placeAt(&h.vals[ri], u.idx, v)
		u.val, u.ran, changed = v, true, true
	}
	if !changed {
		return events
	}
	if exprBeforeCommit != nil {
		exprBeforeCommit()
	}

	// 3. The commit: the expression leaves that changed, onto each root's
	// live value as the store holds it now.
	t.mu.Lock()
	for k, ri := range s.roots {
		if !h.own[ri] {
			continue
		}
		cur, key, ok := t.lookupLocked(h.keys[ri])
		if !ok {
			continue
		}
		nv, moved := t.writeBase(key, cur), false
		for _, i := range s.rootUnits[k] {
			u := &ex.units[i]
			if !u.ran {
				continue
			}
			if leaf, ok := valueAt(nv, u.idx); ok && sameValue(&leaf, &u.val) {
				continue
			}
			if m, ok := setAt(nv, u.idx, ir.CopyValue(u.val)); ok {
				nv, moved = m, true
			}
		}
		if moved {
			t.writeLocked(key, nv)
		}
	}
	t.mu.Unlock()
	return events
}

// copyInto makes dst a copy of src that shares no aggregate with it,
// reusing dst's storage when it has src's shape (no allocation then).
func copyInto(dst, src ir.Value) ir.Value {
	switch src.Kind {
	case ir.TypeStruct:
		if dst.Kind != ir.TypeStruct || len(dst.Fld) != len(src.Fld) || dst.Fld == nil {
			return ir.CopyValue(src)
		}
		fld := dst.Fld
		out := src
		out.Fld = fld
		for i := range src.Fld {
			fld[i] = copyInto(fld[i], src.Fld[i])
		}
		return out
	case ir.TypeArray:
		if dst.Kind != ir.TypeArray || len(dst.Arr) != len(src.Arr) || dst.Arr == nil {
			return ir.CopyValue(src)
		}
		arr := dst.Arr
		out := src
		out.Arr = arr
		for i := range src.Arr {
			arr[i] = copyInto(arr[i], src.Arr[i])
		}
		return out
	}
	return src
}

// placeAt sets the leaf at idx inside v, which the stage owns, in place.
func placeAt(v *ir.Value, idx []int, leaf ir.Value) {
	cur := v
	for _, i := range idx {
		switch cur.Kind {
		case ir.TypeStruct:
			if i >= len(cur.Fld) {
				return
			}
			cur = &cur.Fld[i]
		case ir.TypeArray:
			if i >= len(cur.Arr) {
				return
			}
			cur = &cur.Arr[i]
		default:
			return
		}
	}
	*cur = copyInto(*cur, leaf)
}

// exprForcedLocked: a force covers the unit's member (the whole tag, the
// member, or something it lies in). Caller holds t.mu.
func (t *Tags) exprForcedLocked(u *exprUnit) bool {
	if len(t.forces) == 0 {
		return false
	}
	fs := t.forces[u.in.name]
	if fs == nil {
		return false
	}
	for _, e := range fs.entries {
		if len(e.path) == 0 {
			return true
		}
		if n, ok := u.in.nodeAt(e.path); ok && pathUnder(u.node.canon, n.canon) {
			return true
		}
	}
	return false
}

// keyCoveredLocked: a force or a substitute covers delivered key k, so what
// an expression reads there has a value though the device never sent one.
// Caller holds t.mu.
func (t *Tags) keyCoveredLocked(in *instInfo, k int) bool {
	c := in.keys[k].node.canon
	covers := func(path []string) bool {
		if len(path) == 0 {
			return true
		}
		n, ok := in.nodeAt(path)
		return ok && pathUnder(c, n.canon)
	}
	if fs := t.forces[in.name]; fs != nil {
		for _, e := range fs.entries {
			if covers(e.path) {
				return true
			}
		}
	}
	if ss := t.subs[in.name]; ss != nil {
		for _, e := range ss.entries {
			if covers(e.path) {
				return true
			}
		}
	}
	return false
}

// exprEvent is a fault that started or cleared, for the log.
type exprEvent struct {
	member, msg string
	cleared     bool
	scans       uint64
	dur         time.Duration
}

func (ex *exprSet) faultStart(i int, msg string, now time.Time, events []exprEvent) []exprEvent {
	u := &ex.units[i]
	ex.mu.Lock()
	u.fault = exprFaultRec{msg: msg, since: now, scans: 1}
	ex.mu.Unlock()
	u.faulted.Store(true)
	return append(events, exprEvent{member: u.full, msg: msg})
}

func (ex *exprSet) faultClear(i int, now time.Time, events []exprEvent) []exprEvent {
	u := &ex.units[i]
	ex.mu.Lock()
	rec := u.fault
	u.fault = exprFaultRec{}
	ex.mu.Unlock()
	u.faulted.Store(false)
	return append(events, exprEvent{member: u.full, msg: rec.msg, cleared: true, scans: rec.scans, dur: now.Sub(rec.since)})
}

// runExprs runs the stage for one main scan and logs its fault transitions.
func (r *Runtime) runExprs(now time.Time) {
	p := r.prog
	ex := p.ex
	if ex == nil {
		return
	}
	p.mu.Lock()
	r.exprEvents = ex.run(r.tags, now, r.exprEvents[:0])
	p.mu.Unlock()
	if len(r.exprEvents) == 0 {
		return
	}
	log := r.log
	if log == nil {
		log = slog.Default()
	}
	for _, e := range r.exprEvents {
		if e.cleared {
			log.Info("runtime: expression fault cleared", "member", e.member, "error", e.msg, "scans", e.scans, "for", e.dur.Round(time.Millisecond))
		} else {
			log.Warn("runtime: expression fault: the member keeps its last value, quality bad", "member", e.member, "error", e.msg)
		}
	}
}

// ExprStats is the expression stage in ScanStats: how many expr: members
// the main task evaluates, and the ones faulting now.
type ExprStats struct {
	Members  int         `json:"members"`
	Faulting []ExprFault `json:"faulting,omitempty"`
}

// ExprFault is one expr: member faulting now: it keeps its last value, with
// quality bad, until a scan evaluates it cleanly.
type ExprFault struct {
	Member string    `json:"member"`
	Error  string    `json:"error"`
	Since  time.Time `json:"since"`
	Scans  uint64    `json:"scans"`
}

func (ex *exprSet) stats() *ExprStats {
	out := &ExprStats{Members: len(ex.units)}
	ex.mu.Lock()
	for i := range ex.units {
		u := &ex.units[i]
		if u.fault.msg != "" {
			out.Faulting = append(out.Faulting, ExprFault{Member: u.full, Error: u.fault.msg, Since: u.fault.since, Scans: u.fault.scans})
		}
	}
	ex.mu.Unlock()
	return out
}

// Quality.

// qualRank orders qualities worst last: an expression is as good as the
// worst of what it read.
func qualRank(q nio.Quality) int {
	switch q {
	case nio.Good:
		return 0
	case nio.Substituted:
		return 1
	case nio.Stale:
		return 2
	case nio.NotConnected:
		return 3
	}
	return 4
}

func worstQ(a, b nio.Quality) nio.Quality {
	if qualRank(b) > qualRank(a) {
		return b
	}
	return a
}

// exprQualities computes the quality of the units asked for and of every
// unit they read, memoised in qs (nio.Quality(255) = not yet). roll gives
// an instance's delivered qualities, tag a plain tag's.
type exprQual struct {
	ex      *exprSet
	qs      []nio.Quality
	done    []bool
	roll    func(*instInfo) instRoll
	tag     func(string) nio.Quality
	standby bool
}

func (ex *exprSet) newQual(roll func(*instInfo) instRoll, tag func(string) nio.Quality, standby bool) *exprQual {
	return &exprQual{ex: ex, qs: make([]nio.Quality, len(ex.units)), done: make([]bool, len(ex.units)), roll: roll, tag: tag, standby: standby}
}

func (x *exprQual) of(i int) nio.Quality {
	if x.done[i] {
		return x.qs[i]
	}
	x.done[i] = true // no cycles: New refused them
	u := &x.ex.units[i]
	q := nio.Good
	for _, r := range u.reads {
		if r.in == nil {
			q = worstQ(q, x.tag(r.tag))
			continue
		}
		if len(r.keys) > 0 {
			roll := x.roll(r.in)
			ks := make([]nio.Quality, len(r.keys))
			for j, k := range r.keys {
				ks[j] = roll.members[k]
			}
			kq, _ := instanceQuality(ks)
			q = worstQ(q, kq)
		}
		for _, j := range r.units {
			q = worstQ(q, x.of(j))
		}
	}
	switch {
	case x.standby:
		// A standby does not scan: what it holds is as of its last scan as
		// leader, or its seed, and Stale as every scan-fed value is there.
		q = worstQ(q, nio.Stale)
	case u.faulted.Load():
		q = worstQ(q, nio.Bad)
	case !u.seen.Load():
		q = nio.NotConnected // not computed yet: its zero is no value, whatever its inputs say
	}
	x.qs[i] = q
	return q
}

// under appends to qs the quality of each of instance in's expression
// members at, under or over the path canon ("" for the whole instance).
func (x *exprQual) under(in *instInfo, canon string, qs []nio.Quality) []nio.Quality {
	for _, i := range x.ex.byInst[in] {
		c := x.ex.units[i].node.canon
		if canon == "" || pathUnder(c, canon) || pathUnder(canon, c) {
			qs = append(qs, x.of(i))
		}
	}
	return qs
}

// rootWithExprs is an instance root's quality with its expression members
// rolled in: the instance rule over its delivered roll-up and them, so an
// expression not computed yet beside delivered members is Bad, as a member
// not delivered yet is.
func rootWithExprs(root nio.Quality, in *instInfo, eq []nio.Quality) nio.Quality {
	if len(eq) == 0 {
		return root
	}
	qs := eq
	if in.root.hasDel || root != nio.Good {
		qs = append([]nio.Quality{root}, eq...)
	}
	q, _ := instanceQuality(qs)
	return q
}
