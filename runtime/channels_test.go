package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// fakeChannel is a ChannelIO whose requests wait until the test answers
// them, so a test controls when a READ or WRITE completes.
type fakeChannel struct {
	mu      sync.Mutex
	reqs    []fakeReq
	started chan fakeReq
	answer  chan fakeAnswer
	closed  bool
}

type fakeReq struct {
	write    bool
	variable string
	val      ir.Value
	elem     string
}

type fakeAnswer struct {
	val ir.Value
	err error
}

type statusErr int

func (s statusErr) Error() string   { return "peer status" }
func (s statusErr) CommStatus() int { return int(s) }

func newFakeChannel() *fakeChannel {
	return &fakeChannel{started: make(chan fakeReq, 16), answer: make(chan fakeAnswer)}
}

func (f *fakeChannel) do(ctx context.Context, r fakeReq) (ir.Value, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	f.mu.Unlock()
	f.started <- r
	select {
	case a := <-f.answer:
		return a.val, a.err
	case <-ctx.Done():
		return ir.Value{}, ctx.Err()
	}
}

func (f *fakeChannel) Read(ctx context.Context, variable string, shape ir.Value, elem string) (ir.Value, error) {
	return f.do(ctx, fakeReq{variable: variable, val: shape, elem: elem})
}

func (f *fakeChannel) Write(ctx context.Context, variable string, data ir.Value, elem string) error {
	_, err := f.do(ctx, fakeReq{write: true, variable: variable, val: data, elem: elem})
	return err
}

func (f *fakeChannel) Close() error { f.mu.Lock(); f.closed = true; f.mu.Unlock(); return nil }

func (f *fakeChannel) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.reqs) }

const commSrc = `TYPE Pt : STRUCT A : INT; B : REAL; END_STRUCT; END_TYPE
PROGRAM Main
VAR_EXTERNAL
  Go : BOOL; Push : BOOL; Chan : STRING;
  Ndr : BOOL; Err : BOOL; Busy : BOOL; St : INT; LA : INT; LB : REAL;
  Done : BOOL; WErr : BOOL; WSt : INT; Src : INT;
END_VAR
VAR rd : READ; wr : WRITE; Local : Pt; Out : Pt; END_VAR
rd(REQ := Go, ID := Chan, VAR_1 := 'Remote', RD_1 := Local);
Ndr := rd.NDR; Err := rd.ERROR; Busy := rd.BUSY; St := rd.STATUS;
LA := Local.A; LB := Local.B;
Out.A := Src;
wr(REQ := Push, ID := 'peer', VAR_1 := 'Remote', SD_1 := Out);
Done := wr.DONE; WErr := wr.ERROR; WSt := wr.STATUS;
END_PROGRAM`

type commRig struct {
	t    *testing.T
	p    *Program
	tags *Tags
	ch   *fakeChannel
}

func newCommRig(t *testing.T, chs ...Channel) *commRig {
	t.Helper()
	p, err := Compile(commSrc)
	if err != nil {
		t.Fatal(err)
	}
	r := &commRig{t: t, p: p, tags: NewTags(), ch: newFakeChannel()}
	if chs == nil {
		chs = []Channel{{Name: "peer", Channel: r.ch}}
	}
	r.tags.comm = newCommHub(chs)
	t.Cleanup(r.tags.comm.close)
	for _, b := range []string{"Go", "Push", "Ndr", "Err", "Busy", "Done", "WErr"} {
		r.tags.SetBool(b, false)
	}
	for _, n := range []string{"St", "LA", "WSt", "Src"} {
		setInt(r.tags, n, 0)
	}
	r.tags.SetReal("LB", 0)
	setStr(r.tags, "Chan", "peer")
	return r
}

func (r *commRig) scan() {
	r.t.Helper()
	if err := r.p.Run(r.tags); err != nil {
		r.t.Fatal(err)
	}
}

// scanUntil scans until cond holds, failing after a second.
func (r *commRig) scanUntil(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			r.t.Fatalf("never saw %s", what)
		}
		r.scan()
		time.Sleep(time.Millisecond)
	}
}

func (r *commRig) started() fakeReq {
	r.t.Helper()
	select {
	case q := <-r.ch.started:
		return q
	case <-time.After(time.Second):
		r.t.Fatal("the channel never saw the request")
	}
	return fakeReq{}
}

func ptValue(r *commRig, a int64, b float64) ir.Value {
	shape := r.ch.reqs[0].val
	v := ir.CopyValue(shape)
	v.Fld[0], v.Fld[1] = ir.IntVal(a), ir.RealVal(b)
	return v
}

// A READ posts on the REQ edge, stays BUSY without blocking the scan, lands
// the reply whole on the call that sees it, and holds NDR until the next
// REQ edge (James's call, 2026-11-16: Logix DN behaviour).
func TestReadEdgeHoldAndLanding(t *testing.T) {
	r := newCommRig(t)
	r.tags.SetBool("Go", true)
	r.scan()
	q := r.started()
	if q.write || q.variable != "Remote" || q.val.Kind != ir.TypeStruct {
		t.Fatalf("request = %+v, want a read of Remote with the Pt shape", q)
	}
	if !r.tags.Bool("Busy") || r.tags.Bool("Ndr") {
		t.Fatal("want BUSY and no NDR while the request is in flight")
	}
	// The peer has not answered: scans run on, nothing lands, and a held
	// REQ (no new edge) posts nothing more.
	start := time.Now()
	for i := 0; i < 200; i++ {
		r.scan()
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("200 scans with a request in flight took %v: the scan waited on the channel", el)
	}
	if getInt(r.tags, "LA") != 0 || r.ch.count() != 1 {
		t.Fatalf("LA=%d posts=%d, want nothing landed and one post", getInt(r.tags, "LA"), r.ch.count())
	}
	r.ch.answer <- fakeAnswer{val: ptValue(r, 7, 2.5)}
	r.scanUntil("NDR", func() bool { return r.tags.Bool("Ndr") })
	if getInt(r.tags, "LA") != 7 || r.tags.Real("LB") != 2.5 || r.tags.Bool("Busy") || r.tags.Bool("Err") {
		t.Fatalf("LA=%d LB=%v Busy=%v Err=%v after the reply", getInt(r.tags, "LA"), r.tags.Real("LB"), r.tags.Bool("Busy"), r.tags.Bool("Err"))
	}
	// Held: REQ high, then REQ low, NDR stays.
	r.scan()
	r.tags.SetBool("Go", false)
	r.scan()
	if !r.tags.Bool("Ndr") {
		t.Fatal("NDR dropped before the next REQ edge")
	}
	// The next edge clears it and starts again.
	r.tags.SetBool("Go", true)
	r.scan()
	r.started()
	if r.tags.Bool("Ndr") || !r.tags.Bool("Busy") {
		t.Fatal("a new REQ edge should clear NDR and set BUSY")
	}
	if getInt(r.tags, "LA") != 7 {
		t.Fatal("a new request must not disturb RD_1 until it completes")
	}
}

// A REQ edge while BUSY is ignored, and REQ falling does not cancel.
func TestReadEdgeWhileBusyIgnored(t *testing.T) {
	r := newCommRig(t)
	r.tags.SetBool("Go", true)
	r.scan()
	r.started()
	for i := 0; i < 3; i++ {
		r.tags.SetBool("Go", false)
		r.scan()
		r.tags.SetBool("Go", true)
		r.scan()
	}
	if n := r.ch.count(); n != 1 {
		t.Fatalf("%d posts, want 1", n)
	}
	r.ch.answer <- fakeAnswer{val: ptValue(r, 1, 0)}
	r.scanUntil("NDR", func() bool { return r.tags.Bool("Ndr") })
}

// A failed read sets ERROR and the peer's STATUS, holds them, and leaves
// RD_1 untouched.
func TestReadErrorLeavesTarget(t *testing.T) {
	r := newCommRig(t)
	r.tags.SetBool("Go", true)
	r.scan()
	r.started()
	r.ch.answer <- fakeAnswer{err: statusErr(5)}
	r.scanUntil("ERROR", func() bool { return r.tags.Bool("Err") })
	if getInt(r.tags, "St") != 5 || r.tags.Bool("Ndr") || r.tags.Bool("Busy") || getInt(r.tags, "LA") != 0 {
		t.Fatalf("St=%d Ndr=%v Busy=%v LA=%d", getInt(r.tags, "St"), r.tags.Bool("Ndr"), r.tags.Bool("Busy"), getInt(r.tags, "LA"))
	}
	r.tags.SetBool("Go", false)
	r.scan()
	if !r.tags.Bool("Err") || getInt(r.tags, "St") != 5 {
		t.Fatal("ERROR/STATUS must hold until the next REQ edge")
	}
}

// A reply that does not fit the variable is a type clash, not a landing.
func TestReadShapeClash(t *testing.T) {
	r := newCommRig(t)
	r.tags.SetBool("Go", true)
	r.scan()
	r.started()
	r.ch.answer <- fakeAnswer{val: ir.RealVal(1)}
	r.scanUntil("ERROR", func() bool { return r.tags.Bool("Err") })
	if getInt(r.tags, "St") != ir.CommTypeClash {
		t.Fatalf("St=%d, want %d", getInt(r.tags, "St"), ir.CommTypeClash)
	}
}

// An ID naming no channel fails on the same call with CommNoChannel, and so
// does every request on a runtime with no channels at all.
func TestReadUnknownChannel(t *testing.T) {
	r := newCommRig(t)
	setStr(r.tags, "Chan", "nope")
	r.tags.SetBool("Go", true)
	r.scan()
	if !r.tags.Bool("Err") || getInt(r.tags, "St") != ir.CommNoChannel || r.tags.Bool("Busy") {
		t.Fatalf("Err=%v St=%d Busy=%v", r.tags.Bool("Err"), getInt(r.tags, "St"), r.tags.Bool("Busy"))
	}

	r.tags.comm = nil
	r.tags.SetBool("Go", false)
	r.scan()
	setStr(r.tags, "Chan", "peer")
	r.tags.SetBool("Go", true)
	r.scan()
	if getInt(r.tags, "St") != ir.CommNoChannel {
		t.Fatalf("St=%d without channels, want %d", getInt(r.tags, "St"), ir.CommNoChannel)
	}
}

// A request the peer never answers times out with CommTimeout.
func TestReadTimeout(t *testing.T) {
	ch := newFakeChannel()
	r := newCommRig(t, Channel{Name: "peer", Channel: ch, Timeout: 20 * time.Millisecond})
	r.ch = ch
	r.tags.SetBool("Go", true)
	r.scan()
	r.started()
	r.scanUntil("ERROR", func() bool { return r.tags.Bool("Err") })
	if getInt(r.tags, "St") != ir.CommTimeout {
		t.Fatalf("St=%d, want %d", getInt(r.tags, "St"), ir.CommTimeout)
	}
}

// A WRITE sends SD_1 as it was on the REQ edge, and holds DONE.
func TestWriteSnapshotAndDone(t *testing.T) {
	r := newCommRig(t)
	setInt(r.tags, "Src", 42)
	r.tags.SetBool("Push", true)
	r.scan()
	q := r.started()
	setInt(r.tags, "Src", 99) // changes after the edge do not reach the peer
	r.scan()
	if !q.write || q.val.Kind != ir.TypeStruct || q.val.Fld[0].I != 42 {
		t.Fatalf("request = %+v, want a write of A=42", q)
	}
	r.ch.answer <- fakeAnswer{}
	r.scanUntil("DONE", func() bool { return r.tags.Bool("Done") })
	r.tags.SetBool("Push", false)
	r.scan()
	if !r.tags.Bool("Done") || r.tags.Bool("WErr") {
		t.Fatal("DONE must hold until the next REQ edge")
	}
}

// A channel already holding its limit refuses the next request at once.
func TestQueueFull(t *testing.T) {
	ch := newFakeChannel()
	r := newCommRig(t, Channel{Name: "peer", Channel: ch, Queue: 1})
	r.ch = ch
	r.tags.SetBool("Go", true)
	r.scan()
	r.started() // the worker holds the read; the queue is empty again
	r.tags.SetBool("Push", true)
	r.scan() // the write waits in the queue
	if r.tags.Bool("WErr") {
		t.Fatal("the first queued request should fit")
	}
	h := r.tags.comm
	if tk, st := h.PostRead("peer", "X", ir.IntVal(0), ""); tk != 0 || st != ir.CommQueueFull {
		t.Fatalf("third post = %d, %d; want refused with %d", tk, st, ir.CommQueueFull)
	}
}

// A ticket the hub never issued (a block restored from an earlier process)
// polls as lost, and the block reports it.
func TestPollUnknownTicket(t *testing.T) {
	h := newCommHub([]Channel{{Name: "peer", Channel: newFakeChannel()}})
	defer h.close()
	res, done := h.Poll(12345)
	if !done || res.Status != ir.CommLost {
		t.Fatalf("Poll = %+v %v, want lost", res, done)
	}
}

func TestCommStatus(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{nil, ir.CommOK},
		{statusErr(4), 4},
		{context.DeadlineExceeded, ir.CommTimeout},
		{errors.New("connection refused"), ir.CommUnreachable},
	} {
		if got := commStatus(c.err); got != c.want {
			t.Errorf("commStatus(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

// Run closes the channels when it returns.
func TestRunClosesChannels(t *testing.T) {
	ch := newFakeChannel()
	rt, err := New(Options{Program: "PROGRAM Main\nEND_PROGRAM", Scan: time.Millisecond, Channels: []Channel{{Name: "peer", Channel: ch}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	rt.Run(ctx)
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if !ch.closed {
		t.Fatal("Run returned without closing its channels")
	}
	if _, err := New(Options{Program: "PROGRAM Main\nEND_PROGRAM", Channels: []Channel{{Name: "a", Channel: ch}, {Name: "a", Channel: ch}}}); err == nil {
		t.Fatal("two channels with one name should be refused")
	}
}

func setStr(t *Tags, name, v string)       { _ = t.WriteGlobal(name, ir.StringVal(v)) }
func setInt(t *Tags, name string, v int64) { _ = t.WriteGlobal(name, ir.IntVal(v)) }
func getInt(t *Tags, name string) int64 {
	v, _ := t.ReadGlobal(name)
	return v.I
}

func TestUnknownChannels(t *testing.T) {
	src := `PROGRAM Main
VAR_EXTERNAL Go : BOOL; X : REAL; END_VAR
VAR a : READ; b : WRITE; c : READ; END_VAR
IF Go THEN
  a(REQ := Go, ID := 'peer', VAR_1 := 'R', RD_1 := X);
ELSE
  b(REQ := Go, ID := 'other', VAR_1 := 'R', SD_1 := X);
END_IF;
c(REQ := Go, ID := 'other', VAR_1 := 'R', RD_1 := X);
END_PROGRAM`
	rt, err := New(Options{Program: src, Channels: []Channel{{Name: "peer", Channel: newFakeChannel()}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.UnknownChannels(); len(got) != 1 || got[0] != "other" {
		t.Fatalf("UnknownChannels = %v, want [other]", got)
	}
}

// Channel names match case-insensitively, as identifiers do, in the hub and
// in UnknownChannels alike.
func TestChannelNamesIgnoreCase(t *testing.T) {
	r := newCommRig(t)
	setStr(r.tags, "Chan", "PEER")
	r.tags.SetBool("Go", true)
	r.scan()
	r.started()
	if r.tags.Bool("Err") {
		t.Fatalf("ID 'PEER' for channel peer: STATUS %d", getInt(r.tags, "St"))
	}
}

// After close, a queued request finishes as lost and a new post is
// refused, so no block stays BUSY on a channel that is gone.
func TestCloseFinishesQueuedAndRefusesPosts(t *testing.T) {
	ch := newFakeChannel()
	h := newCommHub([]Channel{{Name: "peer", Channel: ch, Queue: 4}})
	first, _ := h.PostRead("peer", "A", ir.IntVal(0), "")
	<-ch.started // the worker holds the first
	queued, _ := h.PostRead("peer", "B", ir.IntVal(0), "")
	h.close()
	// Lost when close drains it; cancelled (timeout) when the worker took
	// it as close began. Either way it is finished, never BUSY for good.
	if res, done := h.Poll(queued); !done || (res.Status != ir.CommLost && res.Status != ir.CommTimeout) {
		t.Fatalf("queued request after close = %+v %v, want finished", res, done)
	}
	if res, done := h.Poll(first); !done || res.Status != ir.CommTimeout {
		t.Fatalf("in-flight request after close = %+v %v, want cancelled", res, done)
	}
	if tk, st := h.PostRead("peer", "C", ir.IntVal(0), ""); tk != 0 || st != ir.CommUnreachable {
		t.Fatalf("post after close = %d, %d", tk, st)
	}
}

// The declared elementary type of the bound variable reaches the channel,
// so it can tell an INT from a DINT.
func TestElemTypeReachesChannel(t *testing.T) {
	src := `PROGRAM Main
VAR_EXTERNAL Go : BOOL; END_VAR
VAR rd : READ; wr : WRITE; n : INT; arr : ARRAY[1..4] OF DINT; END_VAR
rd(REQ := Go, ID := 'peer', VAR_1 := 'R', RD_1 := n);
wr(REQ := Go, ID := 'peer', VAR_1 := 'W', SD_1 := arr);
END_PROGRAM`
	p, err := Compile(src)
	if err != nil {
		t.Fatal(err)
	}
	ch := newFakeChannel()
	tags := NewTags()
	tags.comm = newCommHub([]Channel{{Name: "peer", Channel: ch}})
	t.Cleanup(tags.comm.close)
	tags.SetBool("Go", true)
	if err := p.Run(tags); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for i := 0; i < 2; i++ {
		q := <-ch.started
		got[q.variable] = q.elem
		ch.answer <- fakeAnswer{val: q.val}
	}
	if got["R"] != "INT" || got["W"] != "DINT" {
		t.Fatalf("elem types = %v, want R: INT, W: DINT", got)
	}
}

// naut check sees a READ inside a user FUNCTION_BLOCK too.
func TestUnknownChannelsInUserFB(t *testing.T) {
	src := `FUNCTION_BLOCK Poller
VAR_INPUT Go : BOOL; END_VAR
VAR rd : READ; X : REAL; END_VAR
rd(REQ := Go, ID := 'remote2', VAR_1 := 'R', RD_1 := X);
END_FUNCTION_BLOCK
PROGRAM Main
VAR p : Poller; END_VAR
p(Go := TRUE);
END_PROGRAM`
	rt, err := New(Options{Program: src})
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.UnknownChannels(); len(got) != 1 || got[0] != "remote2" {
		t.Fatalf("UnknownChannels = %v, want [remote2]", got)
	}
}

// A write a leader queued must not leave once its gate has closed: a
// standby (or the next leader) may be writing the same peer by then.
func TestQueuedWriteDroppedWhenGateCloses(t *testing.T) {
	f := newFakeChannel()
	h := newCommHub([]Channel{{Name: "peer", Channel: f}})
	var leading atomicBool
	leading.set(true)
	h.setGate(leading.get)
	defer h.close()

	t1, st := h.PostWrite("peer", "A", ir.IntVal(1), "DINT")
	if st != ir.CommOK {
		t.Fatalf("post 1: %d", st)
	}
	t2, st := h.PostWrite("peer", "B", ir.IntVal(2), "DINT")
	if st != ir.CommOK {
		t.Fatalf("post 2: %d", st)
	}
	<-f.started // write 1 is in flight, write 2 is queued
	leading.set(false)
	f.answer <- fakeAnswer{}

	waitPoll(t, h, t1)
	res := waitPoll(t, h, t2)
	if res.Status != ir.CommLost {
		t.Fatalf("queued write after the gate closed: status %d, want CommLost", res.Status)
	}
	if n := f.count(); n != 1 {
		t.Fatalf("peer saw %d writes, want 1 (the one in flight before the gate closed)", n)
	}
}

// A write queued under one leadership must not leave under the next, even
// with the gate open again: it was computed by a leadership that is over.
func TestQueuedWriteDroppedAcrossTakeover(t *testing.T) {
	f := newFakeChannel()
	h := newCommHub([]Channel{{Name: "peer", Channel: f}})
	h.setGate(func() bool { return true })
	defer h.close()

	t1, _ := h.PostWrite("peer", "A", ir.IntVal(1), "DINT")
	t2, _ := h.PostWrite("peer", "B", ir.IntVal(2), "DINT")
	<-f.started
	h.newLeadership()
	f.answer <- fakeAnswer{}
	waitPoll(t, h, t1)
	if res := waitPoll(t, h, t2); res.Status != ir.CommLost {
		t.Fatalf("write from the last leadership: status %d, want CommLost", res.Status)
	}
	if n := f.count(); n != 1 {
		t.Fatalf("peer saw %d writes, want 1", n)
	}
	// Reads are not gated: they change nothing on the peer.
	t3, _ := h.PostRead("peer", "C", ir.IntVal(0), "DINT")
	<-f.started
	f.answer <- fakeAnswer{val: ir.IntVal(7)}
	if res := waitPoll(t, h, t3); res.Status != ir.CommOK {
		t.Fatalf("read: status %d", res.Status)
	}
}

type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (b *atomicBool) set(v bool) { b.mu.Lock(); b.v = v; b.mu.Unlock() }
func (b *atomicBool) get() bool  { b.mu.Lock(); defer b.mu.Unlock(); return b.v }

func waitPoll(t *testing.T, h *commHub, ticket int64) ir.CommResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if res, done := h.Poll(ticket); done {
			return res
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("ticket %d never finished", ticket)
	return ir.CommResult{}
}
