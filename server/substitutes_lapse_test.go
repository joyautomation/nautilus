package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/runtime"
)

// Lapsed starts (#1030 review) as a replica's runtime and API see them.
// The oplog decides which entries lapse (oplog's lapse tests); these apply
// what it delivers through SubstituteLog.Apply, as the log would.

func startEntry(seq uint64, tag, req string, lapsed bool, now time.Time) oplog.Entry {
	return oplog.Entry{Seq: seq, Term: 1, Kind: oplog.KindSubstitute, Tag: tag, Value: json.RawMessage(`7`),
		GrantedMs: time.Hour.Milliseconds(), Until: now.Add(time.Hour).UnixMilli(), By: "op", Req: req, Lapsed: lapsed}
}

// A start applied as lapsed never starts: it is listed as lapsed (not
// started), journalled once by the leader that named it, raises nothing,
// and leaves its request ID free, so a retry with it starts. A stop, an
// acknowledgement or a lapse naming it later ends nothing.
func TestLapsedStartNeverStarts(t *testing.T) {
	p := newPair(t)
	firstScan(t, p.rts[0])
	now := p.rts[0].WallNow()
	sl := p.srvs[0].subLog
	if _, err := sl.Apply(startEntry(41, "Flow", "r-1", true, now)); err != nil {
		t.Fatal(err)
	}
	if l := p.rts[0].Substitutes(); len(l) != 0 {
		t.Fatalf("a lapsed start is active: %+v", l)
	}
	if p.rts[0].Tags().Bool(runtime.SubstituteFaultTag) {
		t.Error("a lapsed start raised __Substitute_Fault")
	}
	resp := decodeSubs(t, call(t, p.srvs[0], "GET", "/api/substitutes", "").Body.Bytes())
	if len(resp.Lapsed) != 1 || resp.Lapsed[0].ID != 41 || resp.Lapsed[0].Ended || resp.Lapsed[0].RequestID != "r-1" ||
		resp.Lapsed[0].Note != "lapsed (outcome was unknown; not started)" {
		t.Fatalf("GET /api/substitutes lapsed = %+v", resp.Lapsed)
	}
	// The lapse itself, from this replica's own leadership: journalled once.
	if _, err := sl.Apply(oplog.Entry{Seq: 43, Term: 2, Kind: oplog.KindLapse, Refs: []uint64{41}, By: "system", Origin: p.urls[0]}); err != nil {
		t.Fatal(err)
	}
	lapses := func() int {
		n := 0
		for _, e := range p.srvs[0].audit.ring.Recent() {
			if e.Action == "substitute.lapse" && e.Target == "Flow" && e.Result == audit.ResultOK {
				n++
			}
		}
		return n
	}
	eventually(t, "the lapse journalled", func() bool { return lapses() == 1 }) // events are delivered asynchronously
	// A lapse that missed its majority is followed by another naming the
	// same start (#1032 review): journalled once.
	if _, err := sl.Apply(oplog.Entry{Seq: 45, Term: 3, Kind: oplog.KindLapse, Refs: []uint64{41}, By: "system", Origin: p.urls[0]}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := lapses(); n != 1 {
		t.Fatalf("a second lapse naming the same start journalled it again: %d entries", n)
	}
	// Late stops, acknowledgements and expiries name an entry that is not
	// active: nothing happens.
	p.rts[0].ApplyUnsubstitute(41, "op", "")
	p.rts[0].ApplyUnsubstitute(41, "system", runtime.SubstituteReasonExpired)
	p.rts[0].ApplySuback([]uint64{41}, "op")
	if l := p.rts[0].Substitutes(); len(l) != 0 {
		t.Fatalf("a late stop or ack revived the lapsed entry: %+v", l)
	}
	// A retry with the same request ID starts.
	if _, err := sl.Apply(startEntry(44, "Flow", "r-1", false, now)); err != nil {
		t.Fatal(err)
	}
	if l := p.rts[0].Substitutes(); len(l) != 1 || l[0].ID != 44 {
		t.Fatalf("a retry with the lapsed start's request ID: %+v; want entry 44 running", l)
	}
}

// A start that had started here (its commit was known only outside the
// majority the new leader heard) and that a lapse then names is ended,
// loudly: a stop with reason lapsed, journalled as "substitute ended:
// lapsed at takeover", and listed as ended. A replica that is not the
// lapse's author ends it too, without journalling it twice.
func TestLapseEndsAStartedSubstituteLoudly(t *testing.T) {
	p := newPair(t)
	firstScan(t, p.rts[0])
	p.rts[1].Scan()
	now := p.rts[0].WallNow()
	for i := range 2 {
		if _, err := p.srvs[i].subLog.Apply(startEntry(41, "Flow", "", false, now)); err != nil {
			t.Fatal(err)
		}
		if l := p.rts[i].Substitutes(); len(l) != 1 {
			t.Fatalf("instance %d: the start did not start: %+v", i, l)
		}
	}
	lapse := oplog.Entry{Seq: 43, Term: 2, Kind: oplog.KindLapse, Refs: []uint64{41}, By: "system", Origin: p.urls[0]}
	for i := range 2 {
		if _, err := p.srvs[i].subLog.Apply(lapse); err != nil {
			t.Fatal(err)
		}
		if l := p.rts[i].Substitutes(); len(l) != 0 {
			t.Fatalf("instance %d: the lapse left it running: %+v", i, l)
		}
		resp := decodeSubs(t, call(t, p.srvs[i], "GET", "/api/substitutes", "").Body.Bytes())
		if len(resp.Lapsed) != 1 || !resp.Lapsed[0].Ended || !strings.Contains(resp.Lapsed[0].Note, "lapsed at takeover") {
			t.Fatalf("instance %d: lapsed = %+v", i, resp.Lapsed)
		}
	}
	journal := func(i int) int {
		n := 0
		for _, e := range p.srvs[i].audit.ring.Recent() {
			if e.Action == "substitute.stop" && e.Detail["reason"] == runtime.SubstituteReasonLapsed &&
				e.Detail["note"] == "substitute ended: lapsed at takeover (its commit could not be confirmed)" {
				n++
			}
		}
		return n
	}
	eventually(t, "the lapse's author to journal the end", func() bool { return journal(0) == 1 })
	if _, err := p.srvs[0].subLog.Apply(oplog.Entry{Seq: 45, Term: 3, Kind: oplog.KindLapse, Refs: []uint64{41}, By: "system", Origin: p.urls[0]}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // the other's events, had it any, are delivered by now too
	if a, b := journal(0), journal(1); a != 1 || b != 0 {
		t.Fatalf("journalled %d times on the lapse's author and %d on the other; want 1 and 0", a, b)
	}
}

// The bound: a start the new leader commits is lapsed only when it was
// proposed more than SubstituteStartCommitBound before the leader's now.
func TestSubstituteLapseBound(t *testing.T) {
	var sl SubstituteLog
	now := time.UnixMilli(time.Now().UnixMilli()) // whole ms, as an entry's TS
	e := oplog.Entry{Kind: oplog.KindSubstitute}
	for _, c := range []struct {
		age  time.Duration
		want bool
	}{{time.Second, false}, {runtime.SubstituteStartCommitBound, false}, {runtime.SubstituteStartCommitBound + time.Second, true}, {2 * time.Hour, true}} {
		e.TS = now.Add(-c.age).UnixMilli()
		if got := sl.Lapse(e, now); got != c.want {
			t.Errorf("a start %v old: lapse %v, want %v", c.age, got, c.want)
		}
	}
	for _, k := range []string{oplog.KindUnsubstitute, oplog.KindSuback, oplog.KindAck} {
		if sl.Lapse(oplog.Entry{Kind: k, TS: now.Add(-time.Hour).UnixMilli()}, now) {
			t.Errorf("a %s lapsed: only starts lapse; a late stop is safe", k)
		}
	}
}
