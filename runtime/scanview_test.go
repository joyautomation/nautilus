package runtime

import (
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// A view sees the store as of its snapshot, lands its changes as one
// unit, and leaves unchanged tags' generations alone.
func TestScanViewSnapshotAndCommit(t *testing.T) {
	store := NewTags()
	store.SetReal("A", 1)
	store.SetReal("B", 2)
	store.SetReal("C", 3)
	gen0 := store.Generation()
	v := newScanView(store)
	v.snapshot([]string{"A", "B", "Missing"})

	store.SetReal("A", 100) // a write landing after the snapshot
	if a, _ := v.ReadGlobal("A"); a.F != 1 {
		t.Errorf("view A = %v, want the snapshot's 1", a.F)
	}
	if _, err := v.ReadGlobal("Missing"); err == nil {
		t.Error("missing tag read through the view did not error")
	}
	if c, err := v.ReadGlobal("C"); err != nil || c.F != 3 {
		t.Errorf("a name outside the snapshot falls through to the store: %v %v", c, err)
	}
	_ = v.WriteGlobal("B", ir.RealVal(2)) // same value: must not bump B's generation
	_ = v.WriteGlobal("A", ir.RealVal(5))
	if store.Real("A") != 100 {
		t.Error("view write reached the store before commit")
	}
	v.commit()
	if store.Real("A") != 5 {
		t.Errorf("A after commit = %v, want 5 (last commit wins)", store.Real("A"))
	}
	if bg, _ := store.TagGeneration("B"); bg > gen0 {
		t.Error("committing an equal value moved B's generation")
	}
	if len(v.dirty) != 0 {
		t.Error("dirty set not cleared by commit")
	}
}

// Readers never see a scan half-done: a program that writes X then Y in
// one scan lands them together, under a concurrent reader hammering the
// store.
func TestScanCommitsAreAtomic(t *testing.T) {
	r, err := New(Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL X : INT; Y : INT; END_VAR\nX := X + 1;\nY := Y + 1;\nEND_PROGRAM",
		Seed:    map[string]any{"X": int64(0), "Y": int64(0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var torn int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			snap := r.Tags().Snapshot()
			if snap["X"].I != snap["Y"].I {
				torn++
			}
		}
	}()
	for i := 0; i < 2000; i++ {
		r.Scan()
	}
	close(stop)
	wg.Wait()
	if torn > 0 {
		t.Errorf("%d snapshots saw X and Y from different scans", torn)
	}
}

// gateClock is a Clock that, once armed, parks the first call made from
// inside the MAIN task's program (here, a LOCAL_TIME read) until the test
// releases it: a main scan stopped partway through its logic for as long as
// the test likes, on any machine, where a slow loop would finish too fast
// on a quick runner to show anything. Every other call (the scan's own dt
// reading, any task's) reads the wall clock and returns.
type gateClock struct {
	mu      sync.Mutex
	armed   bool
	entered chan struct{} // closed when the main program is parked
	release chan struct{} // closed by the test to let it go on
}

func newGateClock() *gateClock {
	return &gateClock{entered: make(chan struct{}), release: make(chan struct{})}
}

func (c *gateClock) arm() {
	c.mu.Lock()
	c.armed = true
	c.mu.Unlock()
}

func (c *gateClock) Now() time.Time {
	c.mu.Lock()
	park := c.armed && inMainProgram()
	if park {
		c.armed = false
	}
	c.mu.Unlock()
	if park {
		close(c.entered)
		<-c.release
	}
	return time.Now()
}

// inMainProgram reports whether the caller is running the main task's
// program: (*Program).Run beneath (*Runtime).scanAt on this stack.
func inMainProgram() bool {
	pcs := make([]uintptr, 64)
	frames := goruntime.CallersFrames(pcs[:goruntime.Callers(2, pcs)])
	inProgram, inMain := false, false
	for {
		f, more := frames.Next()
		switch {
		case strings.HasSuffix(f.Function, ".(*Program).Run"):
			inProgram = true
		case strings.HasSuffix(f.Function, ".(*Runtime).scanAt"):
			inMain = true
		}
		if !more {
			return inProgram && inMain
		}
	}
}

// The point of the change: a fast task beside a slow one does not wait.
// The main scan is parked in the middle of its program (see gateClock), and
// the fast task must scan to completion, and commit, while it is.
func TestFastTaskDoesNotWaitForSlowTask(t *testing.T) {
	clk := newGateClock()
	r, err := New(Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL Acc : REAL; END_VAR\nVAR wall : LOCAL_TIME; END_VAR\nwall();\nAcc := Acc + 1.0;\nEND_PROGRAM",
		Tasks: []Task{{Name: "fast", Scan: time.Millisecond,
			Program: "PROGRAM Fast\nVAR_EXTERNAL N : INT; END_VAR\nN := N + 1;\nEND_PROGRAM"}},
		Seed:  map[string]any{"Acc": 0.0, "N": int64(0)},
		Clock: clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Scan() // one ordinary main scan: Acc = 1
	clk.arm()
	mainDone := make(chan struct{})
	go func() { r.Scan(); close(mainDone) }()
	select {
	case <-clk.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("setup: the main program never read the clock")
	}
	// Main is now mid-program, and stays there until released. The
	// timeout is only how long a broken scheduler is given to show itself.
	fastDone := make(chan error, 1)
	go func() { fastDone <- r.ScanTask("fast") }()
	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		close(clk.release)
		<-mainDone
		t.Fatal("the fast task waited behind a main scan parked mid-program")
	}
	// The fast scan committed while main was still running: N moved, and
	// main's half-done scan has not landed.
	if n := r.Tags().All()["N"]; n != int64(1) {
		t.Errorf("N = %v with main parked; want the fast task's 1", n)
	}
	if acc := r.Tags().Real("Acc"); acc != 1 {
		t.Errorf("Acc = %v with main parked; want 1 (its second scan has not committed)", acc)
	}
	close(clk.release)
	<-mainDone
	if acc := r.Tags().Real("Acc"); acc != 2 {
		t.Errorf("Acc = %v after main finished; want 2", acc)
	}
}

// Every optional ir.Host extension the VM asks for must be forwarded by
// the view, or it silently disappears behind it.
func TestScanViewForwardsHostExtensions(t *testing.T) {
	var _ ir.DivZeroCounter = (*scanView)(nil)
	var _ ir.MemberWriter = (*scanView)(nil)
}
