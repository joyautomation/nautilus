//go:build !js

package runtime

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"
	"unsafe"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// IntentView against the runtime itself (#1455 PR 2b): the keep decision
// replays the view, never the runtime, so the view must hold what the
// runtime holds after the same committed entries.

// viewOp is one committed substitute entry, applied to both.
type viewOp struct {
	kind string // start, lapsed, unsub, suback, lapse
	e    ReplicatedSubstitute
	ids  []uint64
}

func randomViewOps(r *rand.Rand, clk *subClock, n int, addrs []string) []viewOp {
	var ops []viewOp
	var ids []uint64
	reqs := []string{"", "", "q1", "q2"}
	pick := func() uint64 {
		if len(ids) == 0 {
			return 1
		}
		return ids[r.Intn(len(ids))]
	}
	for i := 1; i <= n; i++ {
		id := uint64(i)
		switch r.Intn(7) {
		case 0, 1, 2:
			e := replicated(clk, id, addrs[r.Intn(len(addrs))], float64(r.Intn(3)), time.Duration(1+r.Intn(48))*time.Hour)
			e.AckOnStart, e.RequestID = r.Intn(2) == 0, reqs[r.Intn(len(reqs))]
			e.UntilMs -= r.Int63n(10) * (24 * time.Hour).Milliseconds() // some started days ago
			ops = append(ops, viewOp{kind: "start", e: e})
			ids = append(ids, id)
		case 3:
			e := replicated(clk, id, addrs[r.Intn(len(addrs))], 1, time.Hour)
			e.RequestID = reqs[r.Intn(len(reqs))]
			ops = append(ops, viewOp{kind: "lapsed", e: e})
			ids = append(ids, id)
		case 4:
			ops = append(ops, viewOp{kind: "unsub", ids: []uint64{pick()}})
		case 5:
			ops = append(ops, viewOp{kind: "suback", ids: []uint64{pick()}})
		case 6:
			ops = append(ops, viewOp{kind: "lapse", ids: []uint64{pick()}})
		}
	}
	return ops
}

func (op viewOp) apply(rt *Runtime, v *IntentView) {
	switch op.kind {
	case "start":
		_ = rt.ApplySubstitute(op.e)
		v.Start(op.e)
	case "lapsed":
		rt.ApplyLapsedSubstitute(op.e)
		v.LapsedStart(op.e)
	case "unsub":
		rt.ApplyUnsubstitute(op.ids[0], "op", "")
		v.Unsubstitute(op.ids[0])
	case "suback":
		rt.ApplySuback(op.ids, "op")
		v.Suback(op.ids)
	case "lapse":
		rt.ApplyLapse(op.ids, "system", false)
		v.Lapse(op.ids)
	}
}

// runtimeState is what the runtime holds of the log's substitutes, as the
// view holds it.
func runtimeState(rt *Runtime) (subs []string, reqs map[string]reqSeen, lapses []SubstituteLapse, applied uint64) {
	for _, s := range rt.Substitutes() {
		subs = append(subs, fmt.Sprintf("%s id %d value %v by %s until %d granted %d ackOnStart %t acked %t req %q",
			ir.NameKey(s.Name), s.ID, s.Value, s.By, s.UntilMs, s.GrantedMs, s.AckOnStart, s.Acked, s.RequestID))
	}
	sort.Strings(subs)
	rt.subMu.Lock()
	defer rt.subMu.Unlock()
	reqs = map[string]reqSeen{}
	for k, v := range rt.intent.reqs {
		reqs[k] = v
	}
	for _, l := range rt.intent.lapses {
		l.AtMs, l.journalled, l.Name = 0, false, ir.NameKey(l.Name)
		lapses = append(lapses, l)
	}
	return subs, reqs, lapses, rt.intent.applied
}

func viewState(v *IntentView) (subs []string, reqs map[string]reqSeen, lapses []SubstituteLapse, applied uint64) {
	for k, s := range v.active {
		subs = append(subs, fmt.Sprintf("%s id %d value %v by %s until %d granted %d ackOnStart %t acked %t req %q",
			k, s.ID, s.Value, s.By, s.UntilMs, s.GrantedMs, s.AckOnStart, s.acked, s.RequestID))
	}
	sort.Strings(subs)
	reqs = map[string]reqSeen{}
	for k, s := range v.reqs {
		reqs[k] = s
	}
	for _, l := range v.lapses {
		l.Name = ir.NameKey(l.Name) // the runtime lists the program's spelling, the view the entry's
		lapses = append(lapses, l)
	}
	return subs, reqs, lapses, v.applied
}

// TestIntentViewMatchesRuntime: after every committed entry of random
// logs (starts, retries with request IDs, replacements, stops, acks,
// lapsed starts and lapses), on addresses the program can substitute, the
// view holds the runtime's substitutes, request IDs, lapse listing and
// high-water mark.
func TestIntentViewMatchesRuntime(t *testing.T) {
	for seed := int64(0); seed < 300; seed++ {
		clk := noon()
		rt, _ := newRetainSubRig(t, lead(false), nil, clk)
		rt.Scan()
		rt.tags.setAny("Level", ir.RealVal(10))
		rt.tags.setAny("Flow", ir.RealVal(3))
		v := NewIntentView()
		r := rand.New(rand.NewSource(seed))
		for i, op := range randomViewOps(r, clk, 80, []string{"Level", "Flow", "level"}) {
			op.apply(rt, v)
			rs, rr, rl, ra := runtimeState(rt)
			vs, vr, vl, va := viewState(v)
			if !reflect.DeepEqual(rs, vs) || !reflect.DeepEqual(rr, vr) || !reflect.DeepEqual(rl, vl) || ra != va {
				t.Fatalf("seed %d, after op %d (%+v):\nruntime subs %v\nview    subs %v\nruntime reqs %+v\nview    reqs %+v\nruntime lapses %+v\nview    lapses %+v\napplied %d / %d",
					seed, i, op, rs, vs, rr, vr, rl, vl, ra, va)
			}
		}
	}
}

// TestIntentViewKeepsARefusedStart: a start the runtime refuses (an
// address the program does not have) is held by the view as though it
// ran, and a retry with its request ID, which the runtime then runs (it
// never recorded the refused start's ID), is kept while its time runs,
// with the stop that ends it: the decision keeps more, never less.
func TestIntentViewKeepsARefusedStart(t *testing.T) {
	clk := noon()
	rt, _ := newRetainSubRig(t, lead(false), nil, clk)
	rt.Scan()
	rt.tags.setAny("Level", ir.RealVal(10))
	v := NewIntentView()
	e := replicated(clk, 1, "NotInTheProgram", 1, time.Hour)
	e.RequestID = "q"
	_ = rt.ApplySubstitute(e)
	v.Start(e)
	if len(rt.Substitutes()) != 0 {
		t.Fatal("the runtime took a start on an address it does not have")
	}
	if live := v.Live(clk.Now()); len(live) != 2 {
		t.Fatalf("the view's live items %v, want the entry and its request ID", live)
	}
	retry := replicated(clk, 2, "Level", 1, time.Hour)
	retry.RequestID = "q"
	_ = rt.ApplySubstitute(retry)
	f := v.Start(retry)
	if l := rt.Substitutes(); len(l) != 1 || l[0].ID != 2 {
		t.Fatalf("the runtime's substitutes %+v, want the retry running", l)
	}
	if !slices.Contains(f.Writes, maybeItem(2)) || !slices.Contains(v.Live(clk.Now()), maybeItem(2)) {
		t.Fatalf("the retry the runtime runs is not kept: writes %v, live %v", f.Writes, v.Live(clk.Now()))
	}
	if f := v.Unsubstitute(2); !slices.Contains(f.Writes, maybeItem(2)) || slices.Contains(v.Live(clk.Now()), maybeItem(2)) {
		t.Fatalf("its stop does not end it: writes %v, live %v", f.Writes, v.Live(clk.Now()))
	}
}

// TestACompactedReplayGatesTheRetainStore: once the log's base hook says
// everything up to Through was delivered (NoteIntentBase), a failed
// catch-up's retain fallback never re-adds a retained substitute whose ID
// is at or below it, though the compacted replay never applied it (its
// entry was dropped as ended).
func TestACompactedReplayGatesTheRetainStore(t *testing.T) {
	for _, base := range []bool{false, true} {
		clk := noon()
		rt, _ := newRetainSubRig(t, lead(false), nil, clk)
		rt.Scan()
		rt.tags.setAny("Level", ir.RealVal(10))
		if base {
			rt.NoteIntentBase(10)
		}
		stale := retain.SubstituteRetain{ID: 5, Name: "Level", Value: 1.0, By: "op", StartMs: clk.Now().UnixMilli(),
			UntilMs: clk.Now().Add(time.Hour).UnixMilli(), GrantedMs: time.Hour.Milliseconds()}
		rt.subMu.Lock()
		rt.tags.mu.Lock()
		rt.loadIntentFallback([]retain.SubstituteRetain{stale}, rt.mono(), clk.Now().UnixMilli())
		held := rt.tags.hasIDLocked(5)
		rt.tags.mu.Unlock()
		rt.subMu.Unlock()
		if held == base {
			t.Fatalf("base hook %t: the stale retained substitute held %t", base, held)
		}
	}
}

// TestIntentViewDigestCoversEveryField: every field of the view's state
// changes its digest, but for those named as left out (the compaction's
// check compares digests: a field left out is state lost unseen).
func TestIntentViewDigestCoversEveryField(t *testing.T) {
	at := noon().Now()
	ms := at.UnixMilli()
	mk := func(f func(v *IntentView)) string {
		v := NewIntentView()
		v.active["LEVEL"] = viewSub{ReplicatedSubstitute: ReplicatedSubstitute{ID: 3, Name: "Level", Value: 1.0, By: "op", UntilMs: ms + 3600_000,
			GrantedMs: 3600_000, AckOnStart: true, RequestID: "q"}, acked: true}
		v.byID[3] = "LEVEL"
		v.reqs["q"] = reqSeen{id: 3, fp: "LEVEL|1|3600000", startMs: ms}
		v.lapses = []SubstituteLapse{{ID: 2, Name: "Flow", Value: 2.0, By: "op", RequestID: "p", ProposedMs: ms, Ended: true}}
		v.applied, v.subSeq = 3, 3
		if f != nil {
			f(v)
		}
		d, _ := v.Digest(at, 0)
		return d
	}
	base := mk(nil)
	left := map[string]bool{"byID": true, "maybe": true} // an index of active; what the runtime may hold, kept by Live, never the view's state
	ty := reflect.TypeOf(IntentView{})
	for i := range ty.NumField() {
		if n := ty.Field(i).Name; !left[n] && !map[string]bool{"active": true, "reqs": true, "lapses": true, "applied": true, "subSeq": true}[n] {
			t.Errorf("IntentView.%s is neither digested nor left out", n)
		}
	}
	change := func(name string, f reflect.Value) {
		f = reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
		switch f.Kind() {
		case reflect.String:
			f.SetString(f.String() + "x")
		case reflect.Bool:
			f.SetBool(!f.Bool())
		case reflect.Int64:
			f.SetInt(f.Int() + 7)
		case reflect.Uint64:
			f.SetUint(f.Uint() + 7)
		case reflect.Interface:
			f.Set(reflect.ValueOf(9.0))
		default:
			t.Fatalf("%s: no way to change a %s", name, f.Type())
		}
	}
	for _, name := range []string{"ID", "Name", "Value", "By", "UntilMs", "GrantedMs", "AckOnStart", "RequestID", "acked"} {
		got := mk(func(v *IntentView) {
			s := v.active["LEVEL"]
			change(name, reflect.ValueOf(&s).Elem().FieldByName(name))
			v.active["LEVEL"] = s
		})
		if got == base {
			t.Errorf("viewSub.%s does not change the digest", name)
		}
	}
	if n := reflect.TypeOf(ReplicatedSubstitute{}).NumField(); n != 8 {
		t.Errorf("ReplicatedSubstitute has %d fields, the list above 8: name each new one", n)
	}
	for _, name := range []string{"id", "fp", "startMs"} {
		got := mk(func(v *IntentView) {
			s := v.reqs["q"]
			change(name, reflect.ValueOf(&s).Elem().FieldByName(name))
			v.reqs["q"] = s
		})
		if got == base {
			t.Errorf("reqSeen.%s does not change the digest", name)
		}
	}
	leftLapse := map[string]bool{"AtMs": true, "journalled": true} // this replica's apply time; its journal
	lt := reflect.TypeOf(SubstituteLapse{})
	for i := range lt.NumField() {
		name := lt.Field(i).Name
		if leftLapse[name] {
			continue
		}
		got := mk(func(v *IntentView) { change(name, reflect.ValueOf(&v.lapses[0]).Elem().FieldByName(name)) })
		if got == base {
			t.Errorf("SubstituteLapse.%s does not change the digest", name)
		}
	}
	for _, f := range []func(v *IntentView){func(v *IntentView) { v.applied++ }, func(v *IntentView) { v.subSeq++ }} {
		if mk(f) == base {
			t.Error("a high-water mark does not change the digest")
		}
	}
}
