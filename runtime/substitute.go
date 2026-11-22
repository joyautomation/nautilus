package runtime

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Substitute values (#707): while a sensor is bad, or plausibly wrong, the
// program runs on a value someone chose, flagged as such everywhere, for a
// bounded time.
//
// Like a force (force.go), a substitute lives in the tag store, on the one
// write path every writer funnels through (writeLocked), so nothing gets
// around it. The layers, bottom up:
//
//   - LIVE: what the driver last delivered (or anyone else last wrote).
//   - the SUBSTITUTE, laid over the live value: a whole tag, or one member
//     of a struct tag ("IOL_P3.Level").
//   - a FORCE, laid over that. The force's "actual" is the substituted view,
//     so a force wins (it is the engineer's debugging tool), and removing it
//     returns the tag to the substitute, not to the bad live reading.
//
// A member write (writeBase) edits the live value and the retain store
// saves the live value (readActual): neither may bake the substitute into
// what the tag returns to.
//
// Only a driver-bound INPUT can be substituted, and the input set cannot
// change while the runtime runs (ReplaceDriver refuses), so a substitute
// is never left on a tag that stopped being an input.
//
// An entry is named by its ID, not its tag: Restore(id) ends exactly that
// entry, so a stale stop (a late request, a replicated entry from before
// a newer start) never ends a newer substitute of the same address.
// Substituting an address that already has one is a NEW entry (a new ID,
// the old one ended): a substitute is never silently extended.
//
// Unlike forces, substitutes survive a takeover: they are operator intent
// meant to run the plant, not a debugging aid, and #707 replicates them.

// Substitution is one active substitute, as the API reports it.
type Substitution struct {
	// ID names this entry. Restore and acknowledgement take it.
	ID uint64 `json:"id"`
	// Name is the substituted address: a tag ("TT_101") or a member path
	// ("IOL_P3.Level").
	Name string `json:"name"`
	// Value is the substitute, in the plain JSON form All() uses.
	Value any `json:"value"`
	// Live is what the driver last delivered at this address: the value
	// the tag held when the substitute began, until the next delivery
	// (a sensor that stays quiet is still known by its last reading).
	Live any `json:"live,omitempty"`
	// LiveQuality is the address's quality without the substitute, as
	// TagQuality would report it. Filled in by Runtime.Substitutes.
	LiveQuality string `json:"liveQuality,omitempty"`
	// By is who started it.
	By string `json:"by"`
	// StartMs and UntilMs are the wall-clock start and end, epoch ms.
	// GrantedMs is the duration granted at the start.
	StartMs   int64 `json:"startMs"`
	UntilMs   int64 `json:"untilMs"`
	GrantedMs int64 `json:"grantedMs"`
	// AckOnStart: the program's start interlock waits for an
	// acknowledgement of this substitute. Acked says whether it has had one.
	AckOnStart bool `json:"ackOnStart"`
	Acked      bool `json:"acked"`
	// Fault, when set, says what about this entry needs a person while it
	// runs on (SubstituteReasonClockCapped); __Substitute_Fault is true
	// while any entry has one. It clears when the entry ends.
	Fault string `json:"fault,omitempty"`
	// RequestID is the client's ID for the request that started it, if it
	// gave one: a start retried with the same ID is answered with this
	// entry instead of starting another.
	RequestID string `json:"requestId,omitempty"`
	// RemainingMs is the time left, on the clock the runtime times it on
	// (not the wall clock: see substitute_expiry.go). Filled in by
	// Runtime.Substitutes.
	RemainingMs int64 `json:"remainingMs"`
}

// SubstituteRequest is what starting a substitute takes besides the address.
type SubstituteRequest struct {
	// Value is the substitute, checked and converted exactly as a write
	// to the address would be.
	Value any
	// For is the granted duration. It must be positive.
	For time.Duration
	// By is who asked: a user, or "system".
	By string
	// AckOnStart: see Substitution.AckOnStart.
	AckOnStart bool
	// RequestID: see Substitution.RequestID.
	RequestID string
	// ID names the entry. Zero lets the runtime assign the next one (and
	// only a leader may start one); a replicated entry brings its own
	// (#707's oplog seq) and is accepted on a standby too.
	ID uint64
}

// subSet is every substitute on ONE root tag.
type subSet struct {
	// entries in application order: a whole-tag substitute first, then
	// member substitutes, so a member refines a whole one.
	entries []subEntry
	// live is the value the writers last asked to store, under the
	// substitutes: what the tag returns to when the last one ends.
	live ir.Value
	// wrote is whether anything has been written since the first entry
	// began (the reported Live is only meaningful once it has).
	wrote bool
	typ   *ir.Type
}

type subEntry struct {
	Substitution
	path []string // member path below the root; nil = the whole tag
	val  ir.Value // the substitute, coerced to the member's type
	// deadline is when it expires on the runtime's monotonic clock
	// (Runtime.mono), once timed is set: this process has timed it since
	// it applied it. See substitute_expiry.go.
	deadline time.Duration
	timed    bool
}

// apply lays every substitute of the set over v. Entries were checked and
// coerced when they began, so an error here means the tag changed shape
// under them: that entry is skipped rather than the write lost.
func (ss *subSet) apply(v ir.Value) ir.Value {
	for i := range ss.entries {
		e := &ss.entries[i]
		if e.path == nil {
			// The whole tag, coerced to its type at the start: laid
			// directly, without boxing it for SetFieldTyped (an allocation
			// on every delivery).
			v = ir.CopyValue(e.val)
			continue
		}
		if nv, err := ir.SetFieldTyped(v, ss.typ, e.path, e.val, ""); err == nil {
			v = nv
		}
	}
	return v
}

// whole reports whether one of the set's entries covers the whole tag.
func (ss *subSet) whole() bool {
	return len(ss.entries) > 0 && ss.entries[0].path == nil
}

// substituteLocked starts a substitute on addr (a tag or a member path)
// with the entry e (its ID, timing and who are filled in by the caller),
// and returns any entry it replaced on the same address. Caller holds t.mu.
func (t *Tags) substituteLocked(addr string, v any, e subEntry) (Substitution, *subEntry, error) {
	root, path, tv, err := t.resolveForceAddr(addr)
	if err != nil {
		return Substitution{}, nil, err
	}
	if tv.v.Kind == ir.TypeFB {
		return Substitution{}, nil, fmt.Errorf("tag %s is a function-block instance: it has no value to substitute", root)
	}
	ss := t.subs[root]
	base := t.liveLocked(root, tv)
	rootT := t.types[ir.NameKey(root)]
	// Checked and converted by the very routine a strict write uses: a
	// value the type cannot hold is refused with the same error.
	nv, err := ir.SetFieldTypedStrict(base, rootT, path, v, "tag "+root)
	if err != nil {
		return Substitution{}, nil, err
	}
	sv, _, err := ir.FieldAt(nv, rootT, path, root)
	if err != nil {
		return Substitution{}, nil, err
	}
	if sv.Kind == ir.TypeFB {
		return Substitution{}, nil, fmt.Errorf("%s is a function-block instance: it has no value to substitute", addr)
	}
	if ss == nil {
		ss = &subSet{live: base, typ: rootT}
		if t.subs == nil {
			t.subs = map[string]*subSet{}
		}
		t.subs[root] = ss
		tv.substituted = true
	}
	e.Name, e.path, e.val = ir.JoinPath(root, path), path, ir.CopyValue(sv)
	e.Value = plain(e.val)
	var replaced *subEntry
	for i := range ss.entries {
		if ss.entries[i].Name == e.Name {
			old := ss.entries[i]
			old.Substitution = t.reportLocked(ss, &old)
			replaced = &old
			ss.entries = append(ss.entries[:i], ss.entries[i+1:]...)
			break
		}
	}
	if path == nil {
		ss.entries = append([]subEntry{e}, ss.entries...)
	} else {
		ss.entries = append(ss.entries, e)
	}
	t.restackLocked(root, tv, ss)
	t.subRev.Add(1)
	t.syncSubLocked()
	return e.Substitution, replaced, nil
}

// unsubstituteLocked ends the entry with this ID, and reports it. The tag
// goes back at once to what lies under it: its other substitutes over the
// live value, or the live value itself, with any force laid back on top.
// Caller holds t.mu.
func (t *Tags) unsubstituteLocked(id uint64) (Substitution, bool) {
	for root, ss := range t.subs {
		for i := range ss.entries {
			if ss.entries[i].ID != id {
				continue
			}
			e := ss.entries[i]
			ss.entries = append(ss.entries[:i], ss.entries[i+1:]...)
			tv := t.vals[root]
			if len(ss.entries) == 0 {
				delete(t.subs, root)
				if tv != nil {
					tv.substituted = false
				}
			}
			if tv != nil {
				t.restackLocked(root, tv, ss)
			}
			t.subRev.Add(1)
			t.syncSubLocked()
			return t.reportLocked(ss, &e), true
		}
	}
	return Substitution{}, false
}

// restackLocked stores a root's layers again after its substitutes changed:
// the substitutes over the live value, and the forces over that, the
// substituted view becoming the forces' actual. Caller holds t.mu.
func (t *Tags) restackLocked(root string, tv *tagVal, ss *subSet) {
	view := ss.apply(ss.live)
	if tv.forced {
		fs := t.forces[root]
		fs.actual, fs.wrote = view, true
		view = fs.apply(view)
	}
	t.storeLocked(root, tv, view)
}

// liveLocked is the root's live value: under its substitutes and forces.
// Caller holds t.mu.
func (t *Tags) liveLocked(root string, tv *tagVal) ir.Value {
	if tv.substituted {
		return t.subs[root].live
	}
	if tv.forced {
		return t.forces[root].actual
	}
	return tv.v
}

// reportLocked is an entry as the API reports it, with its live value.
// Caller holds t.mu.
func (t *Tags) reportLocked(ss *subSet, e *subEntry) Substitution {
	s := e.Substitution
	if a, ok := leafAt(ss.live, e.path); ok {
		s.Live = plain(a)
	}
	return s
}

// substitutions lists the active substitutes, sorted by address, with
// their time left at now on the runtime's monotonic clock.
func (t *Tags) substitutions(now time.Duration) []Substitution {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []Substitution
	for _, ss := range t.subs {
		for i := range ss.entries {
			s := t.reportLocked(ss, &ss.entries[i])
			s.RemainingMs = max(0, (ss.entries[i].deadline - now).Milliseconds())
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SubstituteCount is the number of active substitutes.
func (t *Tags) SubstituteCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n := 0
	for _, ss := range t.subs {
		n += len(ss.entries)
	}
	return n
}

// SubstituteRevision changes whenever the substitute table does, and at
// no other time.
func (t *Tags) SubstituteRevision() uint64 {
	return t.subRev.Load()
}

// subState is how a root tag is substituted, for its quality.
type subState uint8

const (
	subNone   subState = iota
	subMember          // one or more members, not the whole tag
	subWhole           // the whole tag
)

// substitutedRoot reports how the root tag name is substituted.
func (t *Tags) substitutedRoot(name string) subState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.subs) == 0 {
		return subNone
	}
	tv, key, ok := t.lookupLocked(name)
	if !ok || !tv.substituted {
		return subNone
	}
	if t.subs[key].whole() {
		return subWhole
	}
	return subMember
}

// eachSubstitutedRoot calls fn with every substituted root's stored name
// and how it is substituted. fn runs under the store's read lock and must
// not call back into the store.
func (t *Tags) eachSubstitutedRoot(fn func(root string, s subState)) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for root, ss := range t.subs {
		s := subMember
		if ss.whole() {
			s = subWhole
		}
		fn(root, s)
	}
}

// Substitute starts a substitute on addr, a substitutable input
// (Options.Substitutable) or a member of one, for req.For. The value is
// checked as a write to addr would be. Substituting an address that
// already has one ends that entry (a stop, reason "replaced") and starts
// a new one (never an extension). The returned entry carries its ID.
func (r *Runtime) Substitute(addr string, req SubstituteRequest) (Substitution, error) {
	s, _, err := r.substitute(addr, req)
	return s, err
}

// substitute is Substitute, and also returns the entry it replaced (with
// its timing), for undoStart.
func (r *Runtime) substitute(addr string, req SubstituteRequest) (Substitution, *subEntry, error) {
	if req.For <= 0 {
		return Substitution{}, nil, fmt.Errorf("substitute %s: a duration is required", addr)
	}
	// Only the leader starts one: a start a standby took would be hidden
	// from the leader's list and stop, yet survive a takeover and go live
	// later. A replicated entry brings its own ID (PR 3's oplog) and is
	// every replica's to apply.
	if req.ID == 0 && r.standby() {
		return Substitution{}, nil, fmt.Errorf("substitute %s: this replica is not leading: start it on the leader", addr)
	}
	root, _ := ir.SplitAddress(addr)
	if _, _, whole := r.tags.lookupName(addr); whole {
		root = addr
	}
	if !r.isInput(root) {
		return Substitution{}, nil, fmt.Errorf("substitute %s: only a driver-bound input can be substituted", addr)
	}
	if !r.substitutable(root) {
		return Substitution{}, nil, fmt.Errorf("substitute %s: %s is not substitutable (no substitutes: rule names it), so it has no %s flag for the logic to interlock on", addr, root, SubstitutedSuffix)
	}
	r.subMu.Lock()
	defer r.subMu.Unlock()
	id := req.ID
	if id == 0 {
		id = r.subSeq + 1
	}
	if r.tags.hasSubstituteID(id) {
		return Substitution{}, nil, fmt.Errorf("substitute %s: entry %d is already active", addr, id)
	}
	// An ID is not issued twice, across restarts too (the high-water
	// mark is retained): an entry at or below it that is not active has
	// ended, and a stale stop naming it must not end a new one.
	if id <= r.subSeq {
		return Substitution{}, nil, fmt.Errorf("substitute %s: entry %d has ended, and an entry's ID is never reused", addr, id)
	}
	wall := r.wallNow()
	e := subEntry{
		Substitution: Substitution{
			ID: id, By: req.By, AckOnStart: req.AckOnStart, RequestID: req.RequestID,
			StartMs: wall.UnixMilli(), UntilMs: wall.Add(req.For).UnixMilli(),
			GrantedMs: req.For.Milliseconds(),
		},
		deadline: r.mono() + req.For, timed: true,
	}
	r.tags.mu.Lock()
	s, replaced, err := r.tags.substituteLocked(addr, req.Value, e)
	r.tags.mu.Unlock()
	if err != nil {
		return Substitution{}, nil, fmt.Errorf("substitute %s: %w", addr, err)
	}
	if id > r.subSeq {
		r.subSeq = id
	}
	evs := make([]SubstituteEvent, 0, 2)
	if replaced != nil {
		evs = append(evs, SubstituteEvent{Kind: SubstituteEventStop, Reason: SubstituteReasonReplaced, Entry: replaced.Substitution, By: req.By, AtMs: s.StartMs})
		evs = append(evs, faultCleared(replaced.Substitution, s.StartMs)...)
	}
	r.subEvents.push(append(evs, SubstituteEvent{Kind: SubstituteEventStart, Entry: s, By: req.By, AtMs: s.StartMs}))
	return s, replaced, nil
}

// Restore ends the substitute entry with this ID on behalf of by, and
// reports it; false when no such entry is active (it ended already,
// expired, or a newer entry has replaced it), or when this replica is not
// leading: a stop a standby took would be undone at its next takeover,
// when the store's (or the log's) set replaces its table. The address
// goes back to its live value at once.
func (r *Runtime) Restore(id uint64, by string) (Substitution, bool) {
	if r.standby() {
		return Substitution{}, false
	}
	r.subMu.Lock()
	defer r.subMu.Unlock()
	r.tags.mu.Lock()
	s, ok := r.tags.unsubstituteLocked(id)
	r.tags.mu.Unlock()
	if !ok {
		s, ok = r.pendingRestore(id) // retained, still waiting for its input
	}
	if ok {
		at := r.wallNow().UnixMilli()
		r.subEvents.push(append([]SubstituteEvent{{Kind: SubstituteEventStop, Entry: s, By: by, AtMs: at}}, faultCleared(s, at)...))
	}
	return s, ok
}

// Substitutes lists the active substitutes, sorted by address, each with
// the quality its address would have without it.
func (r *Runtime) Substitutes() []Substitution {
	out := r.tags.substitutions(r.mono())
	standby := r.standby()
	for i := range out {
		root, _ := ir.SplitAddress(out[i].Name)
		out[i].LiveQuality = r.liveQuality(root, standby).String()
	}
	return out
}

// Leading reports whether this replica takes operator intent on
// substitutes: a start, stop or acknowledgement (always, without a
// coordinator). A server answers 503 otherwise, rather than change a table
// the next takeover would replace.
func (r *Runtime) Leading() bool { return !r.standby() }

// hasSubstituteID reports whether an entry with this ID is active.
func (t *Tags) hasSubstituteID(id uint64) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.hasIDLocked(id)
}

// lookupName reports a tag's stored name, and whether name itself is a
// tag (a tag whose own name contains a dot wins over a member path, as in
// SetPath).
func (t *Tags) lookupName(name string) (string, *tagVal, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	tv, key, ok := t.lookupLocked(name)
	return key, tv, ok
}

// substitutable reports whether the root name is in Options.Substitutable.
func (r *Runtime) substitutable(name string) bool {
	_, ok := r.subRoots[ir.NameKey(name)]
	return ok
}

// isInput reports whether name is one of the driver-bound inputs.
func (r *Runtime) isInput(name string) bool {
	for _, in := range r.inputs {
		if ir.SameName(in, name) {
			return true
		}
	}
	return false
}

// WallNow is the wall clock substitutes are stamped with (the injected
// clock under naut test, else the real one): a server proposing a start
// to the operator-intent log stamps its until with it.
func (r *Runtime) WallNow() time.Time { return r.wallNow() }

// wallNow is the wall clock substitutes are stamped with: the injected
// clock under test, else the real one.
func (r *Runtime) wallNow() time.Time {
	if r.clock != nil {
		return r.clock.Now()
	}
	return time.Now()
}

// CheckSubstituteValue reports whether v is a value addr (a tag, or a
// member path of one) could be substituted with, checked exactly as a
// write to it would be, without starting anything. It is what a
// manifest's `substitutes:` rule is held to by `naut check`. An address
// whose type the runtime does not know (an undeclared tag) is not
// checked.
func (r *Runtime) CheckSubstituteValue(addr string, v any) error {
	root, path := ir.SplitAddress(addr)
	if _, _, whole := r.tags.lookupName(addr); whole {
		root, path = addr, nil
	}
	rt := r.tags.TypeOf(root)
	if rt == nil {
		if len(path) > 0 {
			return fmt.Errorf("%s has no declared type, so it has no member %q to substitute", root, strings.Join(path, "."))
		}
		return nil
	}
	nv, err := ir.SetFieldTypedStrict(ir.Zero(rt), rt, path, v, "tag "+root)
	if err != nil {
		return err
	}
	sv, _, err := ir.FieldAt(nv, rt, path, root)
	if err != nil {
		return err
	}
	if sv.Kind == ir.TypeFB {
		return fmt.Errorf("%s is a function-block instance: it has no value to substitute", addr)
	}
	return nil
}

// SubstituteCovering reports whether path (a tag, or a member path into
// one) reads a substituted signal: a substitute on the tag itself, or on
// a member the path is in (#715: an alarm on it is suppressed). addr is
// the covering substitute's address. Segments compare as names do
// (ir.NameKey), never as a string prefix: TT_10 does not cover TT_101. A
// substitute on another member of the same tag does not cover path.
func (t *Tags) SubstituteCovering(path string) (addr string, ok bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.subs) == 0 {
		return "", false
	}
	root, segs := ir.SplitAddress(path)
	tv, key, found := t.lookupLocked(root)
	if !found || !tv.substituted {
		return "", false
	}
	ss := t.subs[key]
	if ss == nil {
		return "", false
	}
	for i := range ss.entries {
		if pathCovers(ss.entries[i].path, segs) {
			return ss.entries[i].Name, true
		}
	}
	return "", false
}

// pathCovers reports whether the member path sub (nil: the whole tag) is
// segs or one of its ancestors.
func pathCovers(sub, segs []string) bool {
	if len(sub) > len(segs) {
		return false
	}
	for i := range sub {
		if ir.NameKey(sub[i]) != ir.NameKey(segs[i]) {
			return false
		}
	}
	return true
}
