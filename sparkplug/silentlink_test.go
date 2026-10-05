// silentlink_test.go covers the edge node's behaviour when the broker stops
// answering without closing the socket — a hung controller, a dead radio
// link. paho then hands back publish tokens that never complete, and the
// invariant under test is: one publish that never completes must not stop
// the next tick. See docs/handover/2026-09-19-sparkplug-edge-findings.md.
//
// Stopping an in-process broker closes sockets and does not reproduce this,
// so the tests run the node over a fake mqtt.Client whose tokens complete —
// or never do — under test control.

package sparkplug

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

const silentLinkProgramST = `
PROGRAM Device
VAR_EXTERNAL
	LevelSP : REAL;
	LevelFt : REAL;
END_VAR
LevelFt := LevelSP;
END_PROGRAM
`

// fakeClient is the slice of mqtt.Client the edge node touches. Everything
// else panics through the nil embedded interface, which is the point: a test
// that reaches it is exercising something this fake does not model.
type fakeClient struct {
	mqtt.Client
	mu   sync.Mutex
	open bool      // what IsConnectionOpen / IsConnected report
	hang bool      // Publish returns tokens that never complete
	pubs []fakePub // every publish handed to the client, completed or not
	// onPublish, when set, runs at the start of every Publish, before it is
	// recorded and outside c.mu — a test's hook to hold a publish or to
	// inject an event at an exact point on the wire.
	onPublish func(topic string)
}

type fakePub struct {
	topic   string
	payload []byte
}

func (c *fakeClient) IsConnected() bool      { return c.IsConnectionOpen() }
func (c *fakeClient) IsConnectionOpen() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.open }

func (c *fakeClient) set(open, hang bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.open, c.hang = open, hang
}

func (c *fakeClient) Publish(topic string, _ byte, _ bool, payload interface{}) mqtt.Token {
	c.mu.Lock()
	hook := c.onPublish
	c.mu.Unlock()
	if hook != nil {
		hook(topic)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pubs = append(c.pubs, fakePub{topic, payload.([]byte)})
	if c.hang {
		return neverToken{}
	}
	return doneToken{}
}

// published returns the publishes whose topic contains msgType, in order.
func (c *fakeClient) published(msgType string) []fakePub {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []fakePub
	for _, p := range c.pubs {
		if strings.Contains(p.topic, msgType) {
			out = append(out, p)
		}
	}
	return out
}

// neverToken is a paho token for a publish handed to a connection that is
// being torn down: paho 1.5.1 completes a QoS 0 token only on a successful
// socket write, so this one never completes.
type neverToken struct{}

func (neverToken) Wait() bool                       { select {} }
func (neverToken) WaitTimeout(d time.Duration) bool { time.Sleep(d); return false }
func (neverToken) Done() <-chan struct{}            { return nil }
func (neverToken) Error() error                     { return nil }

type doneToken struct{}

func (doneToken) Wait() bool                     { return true }
func (doneToken) WaitTimeout(time.Duration) bool { return true }
func (doneToken) Done() <-chan struct{}          { ch := make(chan struct{}); close(ch); return ch }
func (doneToken) Error() error                   { return nil }

// newSilentLinkNode builds a born node over the two-tag project the repro
// script uses, publishing through a fake client that is connected and
// completing tokens. Extra options (store-and-forward) pass through to New.
func newSilentLinkNode(t *testing.T, opts ...Option) (*Node, *fakeClient, *runtime.Runtime) {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: silentLinkProgramST,
		Driver:  nio.NewMemory(),
		Scan:    50 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.Setpoint("LevelSP", 10.0),
			runtime.State("LevelFt", 10.0),
		},
	})
	if err != nil {
		t.Fatalf("build runtime: %v", err)
	}
	n, err := New(rt, Config{GroupID: "Repro", EdgeNode: "silent-link"}, opts...)
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	fc := &fakeClient{open: true}
	n.cli = fc
	if err := n.birth(); err != nil {
		t.Fatalf("birth: %v", err)
	}
	if got := fc.published("NBIRTH"); len(got) != 1 {
		t.Fatalf("birth published %d NBIRTH, want 1", len(got))
	}
	return n, fc, rt
}

// tickWithin runs one publish tick and fails the test if it has not returned
// by d — the wedge this file exists for.
func tickWithin(t *testing.T, n *Node, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() { n.scanAndPublish(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("scanAndPublish did not return within %v: a publish token that never completes wedged the tick", d)
	}
}

func decodeSeq(t *testing.T, p fakePub) (seq uint64, historical bool) {
	t.Helper()
	pl, err := DecodePayload(p.payload)
	if err != nil {
		t.Fatalf("decode %s: %v", p.topic, err)
	}
	return pl.Seq, len(pl.Metrics) > 0 && pl.Metrics[0].IsHistorical
}

func TestPublishThatNeverCompletesDoesNotStopTheNextTick(t *testing.T) {
	n, fc, rt := newSilentLinkNode(t)
	n.tokenTimeout = 50 * time.Millisecond

	// The link dies silently: paho still says the connection is open, and
	// the token it hands back for the next publish never completes.
	fc.set(true, true)
	rt.Tags().Set("LevelSP", ir.RealVal(55))
	tickWithin(t, n, 2*time.Second)
	if got := fc.published("NDATA"); len(got) != 1 {
		t.Fatalf("the silent tick handed %d NDATA to the client, want 1", len(got))
	}
	n.mu.Lock()
	seq := n.seq
	n.mu.Unlock()
	if seq != 0 {
		t.Fatalf("seq = %d after a publish that never went out, want 0 (a message that was not sent must not consume a sequence number)", seq)
	}

	// The link is back. The next tick publishes, and its seq follows the
	// birth's, not the unsent message's.
	fc.set(true, false)
	rt.Tags().Set("LevelSP", ir.RealVal(56))
	tickWithin(t, n, 2*time.Second)
	got := fc.published("NDATA")
	if len(got) != 2 {
		t.Fatalf("after the link recovered %d NDATA were published, want 2", len(got))
	}
	if seq, _ := decodeSeq(t, got[1]); seq != 1 {
		t.Fatalf("first NDATA after recovery carries seq %d, want 1", seq)
	}
}

func TestNoPublishWhileTheConnectionIsNotOpen(t *testing.T) {
	n, fc, rt := newSilentLinkNode(t, WithStoreForward(10))

	// paho has noticed the loss and is reconnecting; born still lags it (the
	// connection-lost handler has not run yet). Nothing must be handed to
	// the client, and with store-and-forward on the sample is buffered.
	fc.set(false, true)
	rt.Tags().Set("LevelSP", ir.RealVal(55))
	tickWithin(t, n, 2*time.Second)
	if got := fc.published("NDATA"); len(got) != 0 {
		t.Fatalf("%d NDATA handed to a client that is not connected, want 0", len(got))
	}
	if n.sf.len() != 1 {
		t.Fatalf("store-and-forward holds %d records, want 1", n.sf.len())
	}

	// Back up: the buffered sample replays as historical with the next seq.
	fc.set(true, false)
	tickWithin(t, n, 2*time.Second)
	got := fc.published("NDATA")
	if len(got) != 1 {
		t.Fatalf("after reconnect %d NDATA were published, want 1 (the replay)", len(got))
	}
	if seq, hist := decodeSeq(t, got[0]); seq != 1 || !hist {
		t.Fatalf("replayed NDATA: seq=%d historical=%v, want seq 1, historical", seq, hist)
	}
	if n.sf.len() != 0 {
		t.Fatalf("store-and-forward holds %d records after the drain, want 0", n.sf.len())
	}
}

func TestTimedOutTickIsBufferedWhenStoreForwardIsOn(t *testing.T) {
	n, fc, rt := newSilentLinkNode(t, WithStoreForward(10))
	n.tokenTimeout = 50 * time.Millisecond

	fc.set(true, true)
	rt.Tags().Set("LevelSP", ir.RealVal(55))
	tickWithin(t, n, 2*time.Second)
	if n.sf.len() != 1 {
		t.Fatalf("store-and-forward holds %d records after a timed-out publish, want 1", n.sf.len())
	}

	// Recovery: the timed-out sample replays as historical at seq 1 (its
	// original seq was given back), then the live change follows at seq 2.
	fc.set(true, false)
	rt.Tags().Set("LevelSP", ir.RealVal(56))
	tickWithin(t, n, 2*time.Second)
	got := fc.published("NDATA")
	if len(got) != 3 { // the hung one, the replay, the live one
		t.Fatalf("%d NDATA handed to the client in total, want 3", len(got))
	}
	if seq, hist := decodeSeq(t, got[1]); seq != 1 || !hist {
		t.Fatalf("replay: seq=%d historical=%v, want seq 1, historical", seq, hist)
	}
	if seq, hist := decodeSeq(t, got[2]); seq != 2 || hist {
		t.Fatalf("live: seq=%d historical=%v, want seq 2, live", seq, hist)
	}
}

func TestWaitGivesUpEarlyOnceTheConnectionIsGone(t *testing.T) {
	n, fc, rt := newSilentLinkNode(t)
	n.tokenTimeout = 10 * time.Second // the production value; must not be what bounds this

	fc.set(true, true)
	rt.Tags().Set("LevelSP", ir.RealVal(55))
	done := make(chan struct{})
	go func() { n.scanAndPublish(); close(done) }()
	time.Sleep(100 * time.Millisecond)
	// paho notices and reconnects — fast enough that by the time anyone
	// looks, the connection is open again; only the lost signal tells the
	// orphaned token's wait that its connection is gone.
	n.connectionLost(fc, errors.New("EOF"))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scanAndPublish kept waiting on a token after paho reported the connection gone")
	}
}

func TestBirthWhosePublishNeverCompletesIsNotBorn(t *testing.T) {
	n, fc, _ := newSilentLinkNode(t)
	n.tokenTimeout = 50 * time.Millisecond

	fc.set(true, true)
	done := make(chan error, 1)
	go func() { done <- n.birth() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("birth reported success for an NBIRTH that never went out")
		}
		if !errors.Is(err, errPublishTimeout) {
			t.Fatalf("birth error = %v, want a publish timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("birth did not return: the NBIRTH token wedged it")
	}
	if n.Status().Born {
		t.Fatal("node reports born after its NBIRTH never went out")
	}
}

// With store-and-forward on, a broker outage — paho has reported the loss,
// born is false — does not stop sampling: every change buffers and replays
// as historical, in order, after the reconnect's birth. Before this the tick
// returned at !born and an outage was a hole in the historian, whatever the
// guide promised.
func TestStoreForwardBuffersAcrossABrokerOutage(t *testing.T) {
	n, fc, rt := newSilentLinkNode(t, WithStoreForward(10))

	n.connectionLost(fc, errors.New("EOF"))
	fc.set(false, false)
	for i, v := range []float64{55, 56, 57} {
		rt.Tags().Set("LevelSP", ir.RealVal(v))
		tickWithin(t, n, 2*time.Second)
		if got := n.sf.len(); got != i+1 {
			t.Fatalf("after change %d the buffer holds %d records, want %d", i+1, got, i+1)
		}
	}
	if got := fc.published("NDATA"); len(got) != 0 {
		t.Fatalf("%d NDATA handed to a client that is not connected, want 0", len(got))
	}

	// Reconnect: onConnect births (seq 0), and the next tick replays the
	// three as historical at seq 1..3, then keeps going live.
	fc.set(true, false)
	if err := n.birth(); err != nil {
		t.Fatal(err)
	}
	tickWithin(t, n, 2*time.Second)
	got := fc.published("NDATA")
	if len(got) != 3 {
		t.Fatalf("after reconnect %d NDATA were published, want the 3 buffered", len(got))
	}
	for i, p := range got {
		if seq, hist := decodeSeq(t, p); seq != uint64(i+1) || !hist {
			t.Fatalf("replay %d: seq=%d historical=%v, want seq %d, historical", i, seq, hist, i+1)
		}
	}
	rt.Tags().Set("LevelSP", ir.RealVal(58))
	tickWithin(t, n, 2*time.Second)
	got = fc.published("NDATA")
	if len(got) != 4 {
		t.Fatalf("%d NDATA after a live change, want 4", len(got))
	}
	if seq, hist := decodeSeq(t, got[3]); seq != 4 || hist {
		t.Fatalf("live: seq=%d historical=%v, want seq 4, live", seq, hist)
	}
}

// Without store-and-forward an unborn node samples nothing, as before.
func TestUnbornNodeWithoutStoreForwardStaysQuiet(t *testing.T) {
	n, fc, rt := newSilentLinkNode(t)
	n.connectionLost(fc, errors.New("EOF"))
	fc.set(false, false)
	rt.Tags().Set("LevelSP", ir.RealVal(55))
	tickWithin(t, n, 2*time.Second)
	if got := fc.published("NDATA"); len(got) != 0 {
		t.Fatalf("%d NDATA from an unborn node, want 0", len(got))
	}
}
