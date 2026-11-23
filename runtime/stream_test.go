package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/retain"
)

// runWith runs rt until the test ends.
func runWith(t *testing.T, rt *Runtime) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (f *fakeStore) snapshot() (retain.State, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.saves
}

// A standby carries an image's retain section into its own store only
// when the image is after the position the store records (#710 S1b): an
// older image, or one of an older leadership, never takes the file back;
// the same image again changes nothing. Once it leads it carries none, and
// its own saves record the term it leads in.
func TestCarryRetainedTakesOnlyNewerImages(t *testing.T) {
	store := &fakeStore{state: retain.State{Tags: map[string]any{"SP": 1.0}, Stream: &retain.StreamPos{Term: 5, Seq: 3}}}
	coord := &keepTerm{}
	rt := newRetained(t, store, coord)
	rt.SetTakeoverFlush(func() error { return nil })
	runWith(t, rt)
	ctx := context.Background()
	img := []byte(`{"tags":{"SP":9}}`)

	if _, err := rt.CarryRetained(ctx, img, 5, 2); !errors.Is(err, ErrStaleImage) {
		t.Fatalf("an older image of the same term: %v, want ErrStaleImage", err)
	}
	if _, err := rt.CarryRetained(ctx, img, 4, 9); !errors.Is(err, ErrStaleImage) {
		t.Fatalf("an image of an older term: %v, want ErrStaleImage", err)
	}
	if _, err := rt.CarryRetained(ctx, img, 5, 3); err != nil {
		t.Fatalf("the image the store stands at: %v, want nil", err)
	}
	if st, saves := store.snapshot(); saves != 0 || st.Tags["SP"] != 1.0 {
		t.Fatalf("after refused and repeated images: SP %v, %d saves; want 1 and none", st.Tags["SP"], saves)
	}
	if _, err := rt.CarryRetained(ctx, img, 5, 4); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.snapshot(); st.Tags["SP"] != 9.0 || *st.Stream != (retain.StreamPos{Term: 5, Seq: 4}) {
		t.Fatalf("after a newer image: SP %v at %+v, want 9 at (5, 4)", st.Tags["SP"], st.Stream)
	}
	if _, err := rt.CarryRetained(ctx, []byte(`{"tags":{"SP":10}}`), 6, 1); err != nil {
		t.Fatalf("the first image of a newer term (its seq restarts): %v", err)
	}

	coord.term.Store(7)
	coord.lead.Store(true)
	waitFor(t, "the takeover", func() bool { term, lead := rt.LeadingTerm(); return lead && term == 7 })
	if got := rt.Tags().Real("SP"); got != 10 {
		t.Fatalf("SP after the takeover = %v, want the carried 10", got)
	}
	if _, err := rt.CarryRetained(ctx, []byte(`{"tags":{"SP":11}}`), 8, 1); !errors.Is(err, ErrLeadingNoCarry) {
		t.Fatalf("a carry while leading: %v, want ErrLeadingNoCarry", err)
	}
	if err := rt.SaveRetained(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.snapshot(); st.Stream == nil || *st.Stream != (retain.StreamPos{Term: 7}) {
		t.Fatalf("the leader's save records %+v, want term 7", st.Stream)
	}
}

// A takeover flushes the newest image into the retain store before it
// reads the store (#710 S1b): the flush runs off the scan, no logic runs
// meanwhile, and the takeover then loads what the flush wrote.
func TestTakeoverFlushesTheNewestImageBeforeItLoads(t *testing.T) {
	store := &fakeStore{state: retain.State{Tags: map[string]any{"SP": 1.0}}}
	coord := &keepTerm{}
	rt := newRetained(t, store, coord)
	// A pair's live I/O configuration reads the store at the edge and
	// keeps that read for the takeover's own load: the flush must come
	// before it (review F1).
	rt.SetIO(&fakeKeeper{current: true})
	release := make(chan struct{})
	var flushes atomic.Int32
	rt.SetTakeoverFlush(func() error {
		flushes.Add(1)
		<-release
		_, err := rt.CarryRetained(context.Background(), []byte(`{"tags":{"SP":42}}`), 3, 7)
		return err
	})
	runWith(t, rt)
	coord.term.Store(3)
	coord.lead.Store(true)
	time.Sleep(100 * time.Millisecond) // two scan periods and more
	if n := scanned(rt); n != 0 {
		t.Fatalf("%d scans ran logic while the flush was under way, want none", n)
	}
	close(release)
	waitFor(t, "the takeover", func() bool { _, lead := rt.LeadingTerm(); return lead })
	if got := rt.Tags().Real("SP"); got != 42 {
		t.Fatalf("SP after the takeover = %v, want the flushed image's 42", got)
	}
	if n := flushes.Load(); n != 1 {
		t.Fatalf("the flush ran %d times for one takeover, want once", n)
	}
}

// A frame copy the stream asked for before a takeover is of the old
// frames: the takeover's reset drops it, so the new leadership's stream
// never sends it under its own term (#710 S1b).
func TestATakeoverDropsAFrameCopyMadeBeforeIt(t *testing.T) {
	rt := newRetained(t, &fakeStore{}, nil)
	rt.RequestImage()
	rt.Scan()
	if !rt.prog.imageReady() {
		t.Fatal("no copy after a requested scan: the case proves nothing")
	}
	rt.prog.ResetFrame() // what takeover does to every program
	var img Image
	if rt.TakeImage(&img) {
		t.Fatal("a copy made before the takeover's reset was taken after it")
	}
}

// A takeover's flush that does not end holds the takeover no longer than
// takeoverFlushWait: the new leadership runs on what the store holds,
// with a warning, rather than not at all (#710 S1b).
func TestTakeoverFlushIsBounded(t *testing.T) {
	saved := takeoverFlushWait
	takeoverFlushWait = 50 * time.Millisecond
	t.Cleanup(func() { takeoverFlushWait = saved })
	store := &fakeStore{state: retain.State{Tags: map[string]any{"SP": 1.0}}}
	coord := &keepTerm{}
	rt := newRetained(t, store, coord)
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	rt.SetTakeoverFlush(func() error { <-stuck; return nil })
	runWith(t, rt)
	coord.term.Store(3)
	coord.lead.Store(true)
	waitFor(t, "the takeover past a flush that never ends", func() bool { _, lead := rt.LeadingTerm(); return lead })
}

// A flush belongs to its term: a pending takeover whose term moves
// before it is taken (lost and won again while it waited) flushes again,
// since the other controller may have led in between (review F6).
func TestATakeoverFlushesAgainForANewTerm(t *testing.T) {
	store := &fakeStore{state: retain.State{IO: keptIO()}}
	coord := &keepTerm{}
	rt := newRetained(t, store, coord)
	k := &fakeKeeper{release: make(chan struct{})}
	rt.SetIO(k) // its restore holds the edge after the flush
	var flushes atomic.Int32
	rt.SetTakeoverFlush(func() error { flushes.Add(1); return nil })
	runWith(t, rt)
	coord.term.Store(3)
	coord.lead.Store(true)
	waitFor(t, "the first flush", func() bool { return flushes.Load() == 1 })
	coord.term.Store(4)
	waitFor(t, "a flush for the new term", func() bool { return flushes.Load() == 2 })
	close(k.release)
	waitFor(t, "the takeover", func() bool { term, lead := rt.LeadingTerm(); return lead && term == 4 })
}

// A takeover whose retain store cannot be read says so
// (RetainLoadFailed), so a pair's leader streams nothing over its
// standby's good file; a value that merely no longer fits does not
// (review F5).
func TestRetainLoadFailedOnlyForAnUnreadableStore(t *testing.T) {
	for _, c := range []struct {
		name  string
		store *fakeStore
		want  bool
	}{
		{"unreadable", &fakeStore{fail: true}, true},
		{"a value that no longer fits", &fakeStore{state: retain.State{Tags: map[string]any{"SP": "not a number"}, Programs: map[string]string{"gone": "x"}}}, false},
		{"good", &fakeStore{state: retain.State{Tags: map[string]any{"SP": 2.0}}}, false},
	} {
		coord := &keepTerm{}
		rt := newRetained(t, c.store, coord)
		runWith(t, rt)
		coord.term.Store(3)
		coord.lead.Store(true)
		waitFor(t, "the takeover", func() bool { _, lead := rt.LeadingTerm(); return lead })
		if got := rt.RetainLoadFailed(); got != c.want {
			t.Errorf("%s: RetainLoadFailed = %v, want %v", c.name, got, c.want)
		}
	}
}
