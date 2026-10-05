// rebirth_test.go pins the rebirth ordering invariant (see Node.pubMu and
// requestRebirth): once a "Node Control/Rebirth" command has been accepted,
// no DATA — and no device DBIRTH/DDEATH — is handed to the MQTT client until
// the NBIRTH answering it has been, and nothing from the new session goes out
// ahead of that NBIRTH. The Sparkplug TCK scores this as
// tck-id-operational-behavior-data-commands-rebirth-action-1; these tests
// check it at the client seam, where the order is exact, instead of through
// broker timestamps.

package sparkplug

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

func rebirthCommand() Payload {
	return Payload{Metrics: []Metric{{
		Name: "Node Control/Rebirth", Datatype: spb.DataType_Boolean, Value: true,
	}}}
}

// waitPublished waits until the client has been handed at least want
// publishes of msgType.
func waitPublished(t *testing.T, fc *fakeClient, msgType string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(fc.published(msgType)) < want {
		if time.Now().After(deadline) {
			t.Fatalf("%d %s published, want %d", len(fc.published(msgType)), msgType, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// pubsSince returns every publish handed to the client from index i on.
func (c *fakeClient) pubsSince(i int) []fakePub {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]fakePub(nil), c.pubs[i:]...)
}

func (c *fakeClient) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pubs)
}

// checkNoDataBeforeBirth fails if anything but the NBIRTH (and the DBIRTHs
// after it) leads the publishes since a rebirth command was accepted, or if
// a DATA message after it does not continue the NBIRTH's sequence.
func checkNoDataBeforeBirth(t *testing.T, pubs []fakePub) {
	t.Helper()
	born := false
	var last uint64
	for _, p := range pubs {
		seq, _ := decodeSeq(t, p)
		switch {
		case strings.Contains(p.topic, "/NBIRTH/"):
			born, last = true, seq
			if seq != 0 {
				t.Fatalf("NBIRTH seq = %d, want 0", seq)
			}
		case !born:
			t.Fatalf("%s (seq %d) handed to the client after the rebirth command and before the NBIRTH answering it", p.topic, seq)
		default:
			if seq != last+1 {
				t.Fatalf("%s seq = %d after seq %d, want %d", p.topic, seq, last, last+1)
			}
			last = seq
		}
	}
	if !born {
		t.Fatal("no NBIRTH answered the rebirth command")
	}
}

// A tick that runs right after the command is accepted — before or after
// the rebirth goroutine gets the wire, whichever the scheduler picks — must
// not put DATA ahead of the NBIRTH. Before the gate closed synchronously,
// `go n.Rebirth()` left the node born until that goroutine ran, and a tick
// in that gap sent old-session NDATA after the command.
func TestRebirthCommandStopsDataUntilTheNBIRTH(t *testing.T) {
	n, fc, rt := newSilentLinkNode(t)
	for i := 0; i < 50; i++ {
		rt.Tags().Set("LevelSP", ir.RealVal(float64(100+i)))
		births := len(fc.published("NBIRTH"))
		mark := fc.count()
		n.applyCommand(rebirthCommand())
		n.scanAndPublish()
		waitPublished(t, fc, "NBIRTH", births+1)
		// One more tick so a sample the gate held back goes out after the
		// birth, and its seq is checked against the NBIRTH's.
		rt.Tags().Set("LevelSP", ir.RealVal(float64(1000+i)))
		n.scanAndPublish()
		checkNoDataBeforeBirth(t, fc.pubsSince(mark))
	}
}

// The birth holds the wire from the moment it restarts seq and marks the
// node born until its NBIRTH is handed to paho. Before, a tick in that gap
// saw born=true and sent NDATA seq=1 ahead of NBIRTH seq=0.
func TestTickCannotOvertakeTheNBIRTH(t *testing.T) {
	n, fc, rt := newSilentLinkNode(t)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	fc.mu.Lock()
	fc.onPublish = func(topic string) {
		if strings.Contains(topic, "/NBIRTH/") {
			once.Do(func() { close(entered); <-release })
		}
	}
	fc.mu.Unlock()

	mark := fc.count()
	go n.Rebirth()
	<-entered // the rebirth is inside its NBIRTH publish
	rt.Tags().Set("LevelSP", ir.RealVal(77))
	tick := make(chan struct{})
	go func() { n.scanAndPublish(); close(tick) }()
	select {
	case <-tick:
		// The tick did not wait for the birth. Whatever it sent is out of
		// order; checkNoDataBeforeBirth below names it.
	case <-time.After(100 * time.Millisecond):
	}
	if got := fc.published("NDATA"); len(got) != 0 {
		close(release)
		t.Fatalf("a tick handed NDATA (seq %d) to the client while the NBIRTH was still being published", func() uint64 { s, _ := decodeSeq(t, got[0]); return s }())
	}
	close(release)
	<-tick
	pubs := fc.pubsSince(mark)
	checkNoDataBeforeBirth(t, pubs)
	if got := fc.published("NDATA"); len(got) != 1 {
		t.Fatalf("%d NDATA after the birth, want 1", len(got))
	}
}

// A rebirth command accepted part-way through a tick stops the tick's very
// next record: here the NCMD lands while the tick's NDATA is on the wire, so
// its DDATA must not follow. With store-and-forward on, the held-back record
// is buffered and replays as historical after the birth.
func TestRebirthCommandMidTickStopsTheNextRecord(t *testing.T) {
	n, fc, rt := newSilentLinkNode(t,
		WithDevice(Device{ID: "Dev", Tags: []string{"LevelFt"}}),
		WithStoreForward(10))
	births := len(fc.published("NBIRTH"))

	var once sync.Once
	fc.mu.Lock()
	fc.onPublish = func(topic string) {
		if strings.Contains(topic, "/NDATA/") {
			once.Do(func() { n.applyCommand(rebirthCommand()) })
		}
	}
	fc.mu.Unlock()

	rt.Tags().Set("LevelSP", ir.RealVal(41))
	rt.Tags().Set("LevelFt", ir.RealVal(42))
	mark := fc.count()
	tickWithin(t, n, 2*time.Second)
	waitPublished(t, fc, "NBIRTH", births+1)

	pubs := fc.pubsSince(mark)
	if len(pubs) == 0 || !strings.Contains(pubs[0].topic, "/NDATA/") {
		t.Fatalf("first publish of the tick = %v, want the NDATA the command interrupted", pubs)
	}
	checkNoDataBeforeBirth(t, pubs[1:])
	if got := n.sf.len(); got != 1 {
		t.Fatalf("store-and-forward holds %d records, want the 1 DDATA the command held back", got)
	}
	tickWithin(t, n, 2*time.Second)
	ddata := fc.published("DDATA")
	if len(ddata) != 1 {
		t.Fatalf("%d DDATA after the birth, want the 1 replayed", len(ddata))
	}
	if _, hist := decodeSeq(t, ddata[0]); !hist {
		t.Fatal("the held-back DDATA replayed live, want historical")
	}
	checkNoDataBeforeBirth(t, fc.pubsSince(mark+1))
}
