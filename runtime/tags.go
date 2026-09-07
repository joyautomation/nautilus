// Package runtime is the heart of nautilus: a fixed-interval scan loop that
// hosts an IEC 61131-3 program on a virtual machine, binding field I/O and
// operator values through a shared tag store — the same model a real PLC
// uses. Pure stdlib. Bring your own I/O driver, redundancy, and HMI.
package runtime

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Tags is the ir.Host for the program's virtual machine: every VAR_EXTERNAL
// reference resolves here by name. The runtime writes field inputs before
// each scan and reads outputs after; operator commands write between scans.
// Safe for concurrent use.
//
// # Write generations
//
// Every stored value carries the store GENERATION at which it was last
// CHANGED — a single counter, bumped once per value-changing write, never
// per read. Two facts follow, and everything downstream is built on them:
//
//   - A write that stores a value EQUAL to the one already there is a
//     no-op: the counter does not move and the tag keeps the value (and the
//     backing arrays) it already had. The tag store holds values, not
//     deliveries, so re-delivering the same reading is not an event.
//   - Therefore "this tag's generation is the one I saw last time" means
//     "this tag has not changed since", with no comparison of the values
//     themselves — including no deep-compare of a 46-member UDT.
//
// That turns the three per-scan/per-tick sweeps that used to walk every tag
// and every member into pointer-cheap integer comparisons:
//
//	Scan()      skips WriteOutputs entirely when no output tag moved
//	Sparkplug   skips RBE's struct deep-compare for a metric that did not move
//	observers   can take a snapshot only when Generation() advanced
//
// Generations are per-STORE, not per-tag ordinals: they are comparable for
// equality (did this tag change?) and for ordering (which write came
// first?), but they are not a change count. Nothing may assume gen+1 is the
// next write to the same tag.
//
// The equality that suppresses a write is deliberately conservative — see
// sameValue: two struct values are equal only when they share the same
// StructDef, and a function-block instance is never equal to anything,
// because an FB is identity (its retained frame), not a value.
type Tags struct {
	mu   sync.RWMutex
	vals map[string]*tagVal
	// gen is the store's write generation: bumped once per write that
	// actually changes a value, and stamped into that tag's tagVal.
	gen uint64
	// keyGen changes whenever the SET of tag names changes (a tag is
	// created). names caches the sorted key set; namesGen records the
	// keyGen it was built from, so the sort happens once per shape change
	// rather than once per publish tick.
	keyGen   uint64
	names    []string
	namesGen uint64
	// outGen is gen as of the last change to a tag flagged as a driver
	// OUTPUT, and outNames is the flag's registry (see markOutputs). The
	// flag rides in the tagVal the write already loaded, so "did any output
	// move?" is one field read per write instead of a sweep of thousands of
	// names per scan.
	outGen   uint64
	outNames map[string]struct{}
	// clock backs NowMs. Nil = wall clock; a Runtime built with
	// Options.Clock sets it once at construction, before any scan runs.
	clock Clock
}

// tagVal is one stored tag: its value and the store generation at which
// that value was last changed. Held BY POINTER in the map: a tagVal is over
// 100 bytes, and a store with thousands of tags spent more of every scan
// copying entries in and out of the map than doing anything with them. The
// pointed-to value is mutated only under t.mu, and every reader copies what
// it needs out before releasing the lock.
type tagVal struct {
	v   ir.Value
	gen uint64
	out bool // this tag is bound to a driver output; see Tags.outGen
}

// Sample is a tag's value together with the store generation it was written
// at — what SnapshotInto hands a consumer that wants to skip unchanged tags
// without comparing their values. See Tags' "Write generations".
type Sample struct {
	Value ir.Value
	Gen   uint64
}

func NewTags() *Tags { return &Tags{vals: make(map[string]*tagVal)} }

func (t *Tags) ReadGlobal(name string) (ir.Value, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	v, ok := t.vals[name]
	if !ok {
		return ir.Value{}, &UndefinedTagError{name}
	}
	return v.v, nil
}

func (t *Tags) WriteGlobal(name string, v ir.Value) error {
	t.mu.Lock()
	t.writeLocked(name, v)
	t.mu.Unlock()
	return nil
}

// writeLocked is the single point every store write funnels through: it
// suppresses a write that would not change the value, and otherwise stamps
// the new value with a fresh generation. Reports whether the store changed.
// Caller holds t.mu.
func (t *Tags) writeLocked(name string, v ir.Value) bool {
	if cur, ok := t.vals[name]; ok {
		if sameValue(&cur.v, &v) {
			return false
		}
		t.gen++
		cur.v, cur.gen = v, t.gen
		if cur.out {
			t.outGen = t.gen
		}
		return true
	}
	t.keyGen++
	_, out := t.outNames[name] // a tag registered before it existed
	t.gen++
	if out {
		t.outGen = t.gen
	}
	t.vals[name] = &tagVal{v: v, gen: t.gen, out: out}
	return true
}

// markOutputs registers the tags the runtime hands to the driver, so a write
// to one of them can stamp outGen without anyone sweeping the output list.
// Names that do not exist yet are remembered and flagged when created.
func (t *Tags) markOutputs(names []string) {
	if len(names) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.outNames == nil {
		t.outNames = make(map[string]struct{}, len(names))
	}
	for _, n := range names {
		t.outNames[n] = struct{}{}
		if tv, ok := t.vals[n]; ok && !tv.out {
			tv.out = true
			// Whatever it holds now is a value the driver has not been told
			// about, so the first scan after registration must send it.
			t.outGen = t.gen
		}
	}
}

// outputGeneration returns the store generation as of the last change to any
// registered output tag — the whole "does the driver already hold this?"
// question, in one integer read.
func (t *Tags) outputGeneration() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.outGen
}

// Generation returns the store's current write generation — a counter that
// advances on every write that changes a value and on no other event. A
// consumer that recorded it earlier knows, by comparing, whether ANY tag has
// changed since, which is enough to skip a whole snapshot. See Tags' "Write
// generations" for the guarantees.
func (t *Tags) Generation() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.gen
}

// NameGeneration returns a counter that changes whenever the SET of tag
// names changes — i.e. when a tag is created. It does NOT move when a tag's
// value changes.
//
// A consumer that caches anything derived from the tag set — a sorted name
// list, a per-tag rule table, metric-to-device buckets — rebuilds that cache
// exactly when this number moves, and never once per tick "just in case".
func (t *Tags) NameGeneration() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.keyGen
}

// TagGeneration returns the generation at which one tag last changed, and
// whether the tag exists. Equal generations mean the tag holds the very same
// value — no comparison of the values needed.
func (t *Tags) TagGeneration(name string) (uint64, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if v, ok := t.vals[name]; ok {
		return v.gen, true
	}
	return 0, false
}

// setMany applies one driver delivery under a SINGLE lock: for each name in
// order, the value the driver delivered, coerced exactly as setAny does.
// Names absent from in are left alone. Returns the store generation after
// the batch.
//
// The batching is the point: a 560-input controller used to take and release
// the store's write lock 560 times per scan, and the writes that changed
// nothing (most of them, most scans) still paid a map assignment of a
// 100-byte value. Now it is one lock, and an unchanged input costs one map
// lookup and a comparison.
func (t *Tags) setMany(names []string, in map[string]any) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, name := range names {
		v, ok := in[name]
		if !ok {
			continue
		}
		if iv, isIR := irValue(v); isIR {
			t.writeLocked(name, iv)
		}
	}
	return t.gen
}

// readMany fills dst with the named tags' driver-facing values under a
// SINGLE lock — compound values (UDTs, arrays) as ir.Value so typed drivers
// keep field names and integer widths, scalars as plain Go values. Names
// that do not exist are skipped, and dst is cleared of anything stale.
func (t *Tags) readMany(names []string, dst map[string]any) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, name := range names {
		tv, ok := t.vals[name]
		if !ok {
			delete(dst, name)
			continue
		}
		if tv.v.Kind == ir.TypeStruct || tv.v.Kind == ir.TypeArray {
			dst[name] = tv.v
		} else {
			dst[name] = plain(tv.v)
		}
	}
}

// irValue coerces one driver/config value to the ir.Value the store holds,
// with the same kind mapping setAny applies. ok is false for a Go type the
// store has no representation for, which is dropped rather than stored.
func irValue(v any) (ir.Value, bool) {
	switch x := v.(type) {
	case bool:
		return ir.BoolVal(x), true
	case float64:
		return ir.RealVal(x), true
	case int:
		return ir.RealVal(float64(x)), true
	case int64:
		return ir.IntVal(x), true
	case string:
		return ir.StringVal(x), true
	case ir.Value:
		return x, true
	}
	return ir.Value{}, false
}

// sameValue reports whether storing b over a would be a no-op — the
// comparison that decides whether the write generation advances.
//
// Conservative by construction: anything it cannot compare cheaply and
// exactly it calls DIFFERENT, so the only error it can make is to report a
// change that did not happen (a redundant publish, a redundant driver
// write), never to hide one. Two struct values must share the same
// StructDef pointer — equal field values under a different definition are a
// different tag shape, and keeping the old definition would silently rename
// members. A function-block instance is never equal to anything: an FB is
// identity, not a value.
func sameValue(a, b *ir.Value) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case ir.TypeBool:
		return a.B == b.B
	case ir.TypeReal:
		return a.F == b.F
	case ir.TypeInt, ir.TypeTime:
		return a.I == b.I
	case ir.TypeString:
		return a.S == b.S
	case ir.TypeArray:
		if len(a.Arr) != len(b.Arr) {
			return false
		}
		if sameBacking(a.Arr, b.Arr) {
			return true
		}
		for i := range a.Arr {
			if !sameValue(&a.Arr[i], &b.Arr[i]) {
				return false
			}
		}
		return true
	case ir.TypeStruct:
		if a.Struct != b.Struct || len(a.Fld) != len(b.Fld) {
			return false
		}
		if sameBacking(a.Fld, b.Fld) {
			return true
		}
		for i := range a.Fld {
			if !sameValue(&a.Fld[i], &b.Fld[i]) {
				return false
			}
		}
		return true
	}
	return false // TypeFB and anything unmodelled: identity, never equal
}

// sameBacking reports whether two same-length slices are the same storage,
// which makes them element-for-element equal without looking at a single
// element. A driver that holds its last delivery and re-hands it (the common
// shape: cache the decoded UDT, deliver until the field changes it) turns a
// 46-member walk into one pointer comparison; a driver that decodes a fresh
// value every poll simply falls through to the walk.
func sameBacking(a, b []ir.Value) bool {
	return len(a) > 0 && &a[0] == &b[0]
}

// NowMs is the millisecond base every IEC timer counts from (ir.Host). It
// follows the runtime's Clock so TON/TOF/TP elapse in virtual time under
// test; production leaves the clock nil and reads the wall.
func (t *Tags) NowMs() int64 {
	if t.clock != nil {
		return t.clock.Now().UnixMilli()
	}
	return time.Now().UnixMilli()
}

// UndefinedTagError is returned when a program reads a tag that was never set.
type UndefinedTagError struct{ Name string }

func (e *UndefinedTagError) Error() string { return "undefined tag " + e.Name }

// Typed accessors (zero value if absent).

func (t *Tags) Real(name string) float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if v, ok := t.vals[name]; ok {
		return v.v.F
	}
	return 0
}

func (t *Tags) Bool(name string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if v, ok := t.vals[name]; ok {
		return v.v.B
	}
	return false
}

func (t *Tags) SetReal(name string, v float64) { _ = t.WriteGlobal(name, ir.RealVal(v)) }
func (t *Tags) SetBool(name string, v bool)    { _ = t.WriteGlobal(name, ir.BoolVal(v)) }

// Set writes a Go value to a tag, choosing the tag kind. Scalars accept
// bool/float64/int/int64/string; drivers with typed values (integer widths,
// UDT structs, arrays) pass an ir.Value directly and it is stored as-is.
//
// A name containing a "." is a MEMBER address, never a new tag: it is
// resolved through SetPath, and a path that resolves to nothing is dropped
// rather than stored. That is the whole point — a flat assignment of
// "P101.Speed" used to CREATE a top-level tag by that literal name, which no
// program reads and which a Sparkplug edge would then publish as a bogus
// metric. Callers that need to know WHY a write went nowhere (the HTTP API)
// call SetPath and report its error; the runtime's own paths — seeding,
// the per-scan driver copy, a retained restore — go through setAny, which
// creates tags by their configured name and never guesses at members.
func (t *Tags) Set(name string, v any) {
	if _, isMap := v.(map[string]any); isMap || strings.Contains(name, ".") {
		_ = t.SetPath(name, v)
		return
	}
	t.setAny(name, v)
}

// setAny is Set without the member-path guard: the flat, tag-creating store
// write the runtime uses for values that come from configuration (seeds,
// declared inputs, retained state) rather than from an operator.
func (t *Tags) setAny(name string, v any) {
	if iv, ok := irValue(v); ok {
		_ = t.WriteGlobal(name, iv)
	}
}

// SetPath writes an operator value to a tag, or to ONE MEMBER of a struct
// tag, addressed by a dotted path — the write-path counterpart of the dotted
// paths a test's `expect:` and the HMI's tag bindings already use:
//
//	SetPath("TempSP", 65.0)                                    // whole tag
//	SetPath("P101.Drive.Speed", 60.0)                          // one member
//	SetPath("P101", map[string]any{"Cmd": true, "Speed": 60})  // partial merge
//
// The whole struct is read, modified and written back under one lock, so a
// member write is atomic against the scan and against another writer: the
// store holds whole tags, and a partial write is not a thing it can do.
//
// A map merges: members it names are set, every other member keeps its
// CURRENT value (unlike `init:` seeding, which zero-fills what it omits —
// see ir.SetField). Leaves are coerced to the member's own type and never
// retype it. Errors name the tag and the member path:
//
//	undefined tag WEL15_SUP_015
//	tag WEL15_SUP_015: unknown member STRAT (did you mean START?)
//	tag WEL15_SUP_015.START: want BOOL, got a number
//	tag TempSP is a REAL, not a struct — it has no member Hi
//
// No path ever creates a tag: an unknown root is an error, which is what
// keeps a typo out of the tag store (and off the Sparkplug wire).
func (t *Tags) SetPath(path string, v any) error {
	if path == "" {
		return fmt.Errorf("no tag name")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// A tag whose own NAME contains a dot wins over member resolution.
	// Nothing in nautilus creates one today, but a driver is free to name a
	// tag after the field symbol it was read from, and such a tag must stay
	// writable as a whole.
	if cur, ok := t.vals[path]; ok {
		return t.setFieldLocked(path, cur.v, nil, v)
	}
	root, rest, dotted := strings.Cut(path, ".")
	if !dotted {
		return &UndefinedTagError{root}
	}
	cur, ok := t.vals[root]
	if !ok {
		return &UndefinedTagError{root}
	}
	return t.setFieldLocked(root, cur.v, strings.Split(rest, "."), v)
}

// setFieldLocked applies one member write to a tag already read under the
// lock, and stores the result. Caller holds t.mu.
func (t *Tags) setFieldLocked(root string, cur ir.Value, path []string, v any) error {
	nv, err := ir.SetField(cur, path, v, "tag "+root)
	if err != nil {
		return err
	}
	t.writeLocked(root, nv)
	return nil
}

// ReadPath resolves a tag or a member path — "Tag" or "Tag.Member.Sub" — the
// read counterpart of SetPath's address space, and the walk the acceptance
// harness's `expect:`/`given:` and ST expressions already do to reach a
// struct field like RTU9_WEL15_FIT_001.HH. As SetPath does, a tag whose own
// NAME contains a dot wins over member resolution; failing that, the first
// segment names the tag and every remaining segment steps into one struct
// field.
//
// A leaf value comes back as a plain Go scalar — bool, int64, float64, or
// string, the same collapse All() applies to a whole tag — so a caller
// compares against a literal without importing ir. TIME collapses to its
// int64 milliseconds like INT does; nothing here needs to tell them apart
// (see All's `plain`). A struct, array, or FB sub-tree comes back as
// ir.Value, since there is no lossless plain form for one and a caller that
// wants a single field deeper just extends the path.
//
// ok is false for an unknown tag, an unknown member at any step, or a step
// into a non-struct — never an error, because this is meant to be tried
// speculatively (is "X.Y" a tag or a field?) far more often than SetPath's
// descriptive errors are wanted. Goroutine-safe: one RLock for the whole
// walk, the same guarantee ReadGlobal gives a single tag.
func (t *Tags) ReadPath(path string) (any, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if v, ok := t.vals[path]; ok {
		return plainLeaf(v.v), true
	}
	root, rest, dotted := strings.Cut(path, ".")
	if !dotted {
		return nil, false
	}
	tv, ok := t.vals[root]
	if !ok {
		return nil, false
	}
	v := tv.v
	for rest != "" {
		var field string
		field, rest, _ = strings.Cut(rest, ".")
		if v.Kind != ir.TypeStruct || v.Struct == nil {
			return nil, false
		}
		i, ok := v.Struct.FieldIndex[field]
		if !ok || i >= len(v.Fld) {
			return nil, false
		}
		v = v.Fld[i]
	}
	return plainLeaf(v), true
}

// plainLeaf collapses a scalar ir.Value to its plain Go form; a composite
// value (struct/array/FB) comes back as-is, since ReadPath's contract only
// promises a plain form for leaves.
func plainLeaf(v ir.Value) any {
	switch v.Kind {
	case ir.TypeBool:
		return v.B
	case ir.TypeReal:
		return v.F
	case ir.TypeInt, ir.TypeTime:
		return v.I
	case ir.TypeString:
		return v.S
	default:
		return v
	}
}

// Snapshot returns a typed copy of every tag as ir.Value — for consumers
// that need the kind (e.g. the Sparkplug node's faithful datatype mapping),
// where All()'s plain-JSON collapse would lose int-vs-real.
//
// A consumer that runs on a tick — a publish interval, an SSE frame — should
// prefer SnapshotInto, which reuses its map and carries each tag's write
// generation so unchanged tags need no comparison at all.
func (t *Tags) Snapshot() map[string]ir.Value {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]ir.Value, len(t.vals))
	for k, v := range t.vals {
		out[k] = v.v
	}
	return out
}

// SnapshotInto refills dst with every tag as a Sample — value plus write
// generation — and returns it (allocating when dst is nil). Reusing one map
// across ticks is the point: a 550-tag store is ~140 kB of map per
// Snapshot(), and a publish loop at 10 Hz was handing the GC 1.4 MB/s to
// look at values that mostly had not moved.
//
// The generations are what make the reuse worth having: a consumer keeps the
// Gen it last acted on per tag and skips everything whose Gen still matches,
// with no value comparison — see Tags' "Write generations".
//
// dst is left holding exactly the current tag set: names that disappeared
// are removed.
func (t *Tags) SnapshotInto(dst map[string]Sample) map[string]Sample {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if dst == nil {
		dst = make(map[string]Sample, len(t.vals))
	}
	for k, v := range t.vals {
		dst[k] = Sample{Value: v.v, Gen: v.gen}
	}
	// vals ⊆ dst now, so equal lengths already prove the key sets match;
	// only a REMOVED tag can make them differ, and that is the rare path.
	if len(dst) != len(t.vals) {
		for k := range dst {
			if _, ok := t.vals[k]; !ok {
				delete(dst, k)
			}
		}
	}
	return dst
}

// Change is one tag that moved: its name, its new value, and the store
// generation it moved at. What ChangedSince appends.
type Change struct {
	Name  string
	Value ir.Value
	Gen   uint64
}

// ChangedSince appends to dst every tag whose value changed AFTER gen, and
// returns dst along with the store's current generation — which the caller
// passes back next time to get the next batch. Pass 0 to get every tag.
//
// This is the whole delta mechanism, and it is only possible because
// generations are per-STORE and monotonic (see Tags' "Write generations"):
// "changed since the client last heard from us" is one integer comparison
// per tag, with no per-tag bookkeeping on the consumer's side and no
// comparison of the values themselves. An SSE client's entire delta state is
// therefore ONE uint64, not a 10,000-entry map of what it was last sent.
//
// The sweep is O(tags) but touches nothing but two integers per tag, and it
// appends only what moved — which for a plant at steady state is a handful
// of names out of ten thousand. Passing dst[:0] back each tick reuses the
// caller's buffer and allocates nothing.
//
// Deletions are NOT reported: a tag that vanished has no generation to
// compare. NameGeneration moves when the tag SET changes, so a consumer
// that cares takes the (rare) full snapshot then — see the server's
// resync-on-shape-change rule.
func (t *Tags) ChangedSince(gen uint64, dst []Change) ([]Change, uint64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.gen == gen {
		return dst, t.gen // nothing moved; the common tick
	}
	for k, v := range t.vals {
		if v.gen > gen {
			dst = append(dst, Change{Name: k, Value: v.v, Gen: v.gen})
		}
	}
	return dst, t.gen
}

// Plain renders one tag value in the plain-JSON form All() and the HTTP
// frame use — scalars collapsed to Go primitives, a struct or FB to a map
// of its members, an array to a slice. Exported so a consumer that renders
// tags INCREMENTALLY (the server's delta frames re-render only what moved)
// gets byte-identical output to the whole-store rendering, rather than a
// second, subtly different copy of these rules.
func Plain(v ir.Value) any { return plain(v) }

// AppendNames appends every tag name, in sorted order, to dst and returns
// it. The sort is cached and redone only when a tag is CREATED, so a caller
// that needs deterministic order every tick (Sparkplug's metric order) pays
// a slice copy rather than a 550-element string sort ten times a second.
//
// The returned names are appended to the caller's own slice, so the cache
// can never be mutated from outside; passing dst[:0] back each time reuses
// the caller's buffer and allocates nothing.
func (t *Tags) AppendNames(dst []string) []string {
	t.mu.Lock() // cache fill mutates; the sort is rare, the lock is cheap
	defer t.mu.Unlock()
	if t.names == nil || t.namesGen != t.keyGen {
		t.names = make([]string, 0, len(t.vals))
		for k := range t.vals {
			t.names = append(t.names, k)
		}
		sort.Strings(t.names)
		t.namesGen = t.keyGen
	}
	return append(dst, t.names...)
}

// All returns a plain-JSON snapshot of every tag — for an HMI's live watch.
func (t *Tags) All() map[string]any {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]any, len(t.vals))
	for k, v := range t.vals {
		out[k] = plain(v.v)
	}
	return out
}

func plain(v ir.Value) any {
	switch v.Kind {
	case ir.TypeBool:
		return v.B
	case ir.TypeReal:
		return v.F
	case ir.TypeInt, ir.TypeTime:
		return v.I
	case ir.TypeString:
		return v.S
	case ir.TypeArray:
		out := make([]any, len(v.Arr))
		for i, e := range v.Arr {
			out[i] = plain(e)
		}
		return out
	case ir.TypeStruct:
		out := make(map[string]any, len(v.Fld))
		for i, f := range v.Fld {
			name := ""
			if v.Struct != nil && i < len(v.Struct.Fields) {
				name = v.Struct.Fields[i].Name
			}
			if name == "" {
				name = "_" + strconv.Itoa(i)
			}
			out[name] = plain(f)
		}
		return out
	case ir.TypeFB:
		// A function-block instance renders like a struct of its pins, so a
		// watch (editor inline values, HMI) can show t1.Q and t1.ET live.
		// Internals with the built-ins' underscore prefix stay hidden.
		if v.FB == nil || v.FB.Def == nil {
			return nil
		}
		slots := v.FB.Def.AllSlots()
		out := make(map[string]any, len(slots))
		for i, s := range slots {
			if i >= len(v.FB.Slots) || strings.HasPrefix(s.Name, "_") {
				continue
			}
			out[s.Name] = plain(v.FB.Slots[i])
		}
		return out
	default:
		return nil
	}
}
