package runtime

import (
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

// The point of the change: a fast task beside a slow one does not wait.
func TestFastTaskDoesNotWaitForSlowTask(t *testing.T) {
	r, err := New(Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL Acc : REAL; END_VAR\nVAR i : INT; END_VAR\nFOR i := 1 TO 400000 DO Acc := Acc + 1.0; END_FOR;\nEND_PROGRAM",
		Tasks: []Task{{Name: "fast", Scan: time.Millisecond,
			Program: "PROGRAM Fast\nVAR_EXTERNAL N : INT; END_VAR\nN := N + 1;\nEND_PROGRAM"}},
		Seed: map[string]any{"Acc": 0.0, "N": int64(0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Time one main scan alone so the test adapts to the machine.
	t0 := time.Now()
	r.Scan()
	slow := time.Since(t0)
	if slow < 5*time.Millisecond {
		t.Skipf("main scan took only %v; too fast to show waiting", slow)
	}
	done := make(chan struct{})
	go func() { r.Scan(); close(done) }()
	time.Sleep(slow / 4) // main is now mid-scan
	t1 := time.Now()
	if err := r.ScanTask("fast"); err != nil {
		t.Fatal(err)
	}
	fast := time.Since(t1)
	<-done
	if fast > slow/2 {
		t.Errorf("fast task took %v beside a %v main scan: it waited", fast, slow)
	}
	if r.Tags().Real("Acc") != 800000 || r.Tags().All()["N"] != int64(1) {
		t.Errorf("results: Acc=%v N=%v", r.Tags().Real("Acc"), r.Tags().All()["N"])
	}
}

// Every optional ir.Host extension the VM asks for must be forwarded by
// the view, or it silently disappears behind it.
func TestScanViewForwardsHostExtensions(t *testing.T) {
	var _ ir.DivZeroCounter = (*scanView)(nil)
}
