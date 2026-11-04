package runtime

import (
	"fmt"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// #578: at the end of a scan the runtime used to commit the whole struct
// tag, including members the program had only copied in at the start, so
// an external write to one member that landed mid-scan was overwritten by
// the stale copy. A commit now lands only what the program assigned (see
// membermerge.go). These tests park a scan in the middle of its logic
// (gateClock), write from outside, and let it commit.

const mergeLib = `
TYPE
  Inner : STRUCT
    X : DINT;
    Y : DINT;
  END_STRUCT;
  Rec : STRUCT
    A     : DINT;
    B     : DINT;
    Word  : DINT;
    In    : Inner;
    Items : ARRAY[1..3] OF Inner;
    Flags : ARRAY[0..199] OF BOOL;
  END_STRUCT;
END_TYPE
`

// newMergeRuntime runs body as the main program after a LOCAL_TIME read
// (the gate), with a Rec state tag S, a second Rec tag T, and an array tag
// Tbl in scope.
func newMergeRuntime(t *testing.T, clk Clock, body string, tasks ...Task) *Runtime {
	t.Helper()
	r, err := New(Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL S : Rec; T : Rec; Tbl : ARRAY[1..4] OF DINT; END_VAR\n" +
			"VAR wall : LOCAL_TIME; END_VAR\nwall();\n" + body + "\nEND_PROGRAM",
		Libraries: []string{mergeLib},
		Driver:    nio.NewMemory(),
		Tags: []TagDef{
			Typed("S", RoleState, "Rec"),
			Typed("T", RoleState, "Rec"),
			Typed("Tbl", RoleState, "ARRAY[1..4] OF DINT"),
		},
		Tasks: tasks,
		Clock: clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// parkedScan runs one main scan, calls during while it is parked mid-logic
// (after the snapshot, before the commit), then lets it finish.
func parkedScan(t *testing.T, r *Runtime, clk *gateClock, during func()) {
	t.Helper()
	clk.arm()
	done := make(chan struct{})
	go func() { r.Scan(); close(done) }()
	select {
	case <-clk.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("setup: the main program never read the clock")
	}
	during()
	close(clk.release)
	<-done
}

func write(t *testing.T, r *Runtime, path string, v any) {
	t.Helper()
	if errs := r.Tags().Write([]WriteOp{{Path: path, Value: v}}); errs[0] != nil {
		t.Fatalf("write %s: %v", path, errs[0])
	}
}

func readPath(t *testing.T, r *Runtime, path string) any {
	t.Helper()
	v, ok := r.Tags().ReadPath(path)
	if !ok {
		t.Fatalf("no %s", path)
	}
	return v
}

func wantPath(t *testing.T, r *Runtime, path string, want any) {
	t.Helper()
	if got := readPath(t, r, path); got != want {
		t.Errorf("%s = %v (%T), want %v", path, got, got, want)
	}
}

// The issue's case: the program writes one member, an external writer a
// different member of the same tag mid-scan. Both land.
func TestExternalMemberWriteSurvivesProgramMemberWrite(t *testing.T) {
	clk := newGateClock()
	r := newMergeRuntime(t, clk, "S.A := S.A + 1;")
	parkedScan(t, r, clk, func() { write(t, r, "S.B", 7) })
	wantPath(t, r, "S.A", int64(1))
	wantPath(t, r, "S.B", int64(7))
}

// The same member from both sides in one scan: the program's assignment
// is the last write and wins, as it does for a scalar tag — even when it
// assigns the value the member already had. A merge decides by what the
// program ASSIGNED, not by what changed.
func TestProgramAssignmentWinsTheSameMember(t *testing.T) {
	clk := newGateClock()
	r2, err := New(Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL S : Rec; Plain : DINT; END_VAR\n" +
			"VAR wall : LOCAL_TIME; END_VAR\nwall();\nS.A := 5;\nPlain := 5;\nEND_PROGRAM",
		Libraries: []string{mergeLib},
		Driver:    nio.NewMemory(),
		Tags: []TagDef{
			Typed("S", RoleState, "Rec"),
			Typed("Plain", RoleState, "DINT"),
		},
		Clock: clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	r2.Scan() // S.A and Plain are 5 already
	parkedScan(t, r2, clk, func() {
		write(t, r2, "S.A", 100)
		write(t, r2, "Plain", 100)
		write(t, r2, "S.B", 9)
	})
	wantPath(t, r2, "Plain", int64(5)) // the scalar rule, unchanged
	wantPath(t, r2, "S.A", int64(5))   // the same rule for a member
	wantPath(t, r2, "S.B", int64(9))   // a member the program left alone
}

// Nested members, array elements and array-of-struct members merge at the
// path the program assigned; a sibling at any depth survives.
func TestMemberMergeNestedAndArrays(t *testing.T) {
	clk := newGateClock()
	r := newMergeRuntime(t, clk, "S.In.X := 1;\nS.Items[2].X := 2;\nTbl[1] := 3;\nS.Flags[5] := TRUE;")
	parkedScan(t, r, clk, func() {
		write(t, r, "S.In.Y", 11)
		write(t, r, "S.Items[2].Y", 12)
		write(t, r, "S.Items[3].X", 13)
		write(t, r, "Tbl[2]", 14)
		write(t, r, "S.Flags[6]", true)
		write(t, r, "S.A", 15)
	})
	for path, want := range map[string]any{
		"S.In.X": int64(1), "S.Items[2].X": int64(2), "Tbl[1]": int64(3), "S.Flags[5]": true,
		"S.In.Y": int64(11), "S.Items[2].Y": int64(12), "S.Items[3].X": int64(13),
		"Tbl[2]": int64(14), "S.Flags[6]": true, "S.A": int64(15),
	} {
		wantPath(t, r, path, want)
	}
}

// Assigning a whole member assigns everything beneath it, and assigning
// the whole tag assigns every member: an external write under it is the
// program's to overwrite, in either order with a member write.
func TestWholeAssignmentOwnsEverythingBeneath(t *testing.T) {
	for _, body := range []string{
		"S := T;",
		"S.A := 1;\nS := T;",
		"S := T;\nS.A := 1;",
	} {
		t.Run(body, func(t *testing.T) {
			clk := newGateClock()
			r := newMergeRuntime(t, clk, body)
			write(t, r, "T.B", 2)
			write(t, r, "T.In.Y", 3)
			parkedScan(t, r, clk, func() {
				write(t, r, "S.B", 50)
				write(t, r, "S.In.Y", 51)
			})
			wantPath(t, r, "S.B", int64(2))
			wantPath(t, r, "S.In.Y", int64(3))
		})
	}
	t.Run("member struct", func(t *testing.T) {
		clk := newGateClock()
		r := newMergeRuntime(t, clk, "S.In := T.In;")
		write(t, r, "T.In.Y", 3)
		parkedScan(t, r, clk, func() {
			write(t, r, "S.In.Y", 51) // under the assigned member: the program's
			write(t, r, "S.B", 52)    // beside it: survives
		})
		wantPath(t, r, "S.In.Y", int64(3))
		wantPath(t, r, "S.B", int64(52))
	})
}

// A bit assignment is an assignment of its word: the granularity is the
// member, so a sibling member survives and the word is the program's.
func TestBitAssignmentIsItsWord(t *testing.T) {
	clk := newGateClock()
	r := newMergeRuntime(t, clk, "S.Word.3 := TRUE;")
	parkedScan(t, r, clk, func() {
		write(t, r, "S.B", 4)
	})
	wantPath(t, r, "S.Word", int64(8))
	wantPath(t, r, "S.B", int64(4))
}

// Two tasks writing different members of one tag both land, whichever
// commits first. Before #578 the slow task's commit put back the member the
// fast task had written while it ran.
func TestTwoTasksWritingDifferentMembersBothLand(t *testing.T) {
	clk := newGateClock()
	r := newMergeRuntime(t, clk, "S.A := S.A + 1;", Task{Name: "fast", Scan: time.Millisecond, Libraries: []string{mergeLib},
		Program: "PROGRAM Fast\nVAR_EXTERNAL S : Rec; END_VAR\nS.B := S.B + 10;\nEND_PROGRAM"})
	parkedScan(t, r, clk, func() {
		if err := r.ScanTask("fast"); err != nil {
			t.Fatal(err)
		}
	})
	wantPath(t, r, "S.A", int64(1))
	wantPath(t, r, "S.B", int64(10))
}

// A forced tag: the program's member write lands in the tag's ACTUAL value
// without copying the forced member into it, so removing the force returns
// the member to what it was, not to the forced value.
func TestMemberCommitKeepsForcedMembersOutOfActual(t *testing.T) {
	r := newMergeRuntime(t, nil, "S.A := S.A + 1;")
	write(t, r, "S.B", 3)
	if err := r.Tags().Force("S.B", 99); err != nil {
		t.Fatal(err)
	}
	r.Scan()
	wantPath(t, r, "S.B", int64(99))
	wantPath(t, r, "S.A", int64(1))
	r.Tags().Unforce("S.B")
	wantPath(t, r, "S.B", int64(3))
	wantPath(t, r, "S.A", int64(1))
}

// The issue's evidence, as a race: a program writes one member of a struct
// every scan while writers set 200 other members concurrently through the
// front door. Every one of them must be there at the end (the throwaway
// test that found #578 lost 8 of 200). The program spins between its read
// and its write to hold the window open. Run with -race.
func TestConcurrentMemberWritesAreNeverLost(t *testing.T) {
	r, err := New(Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL S : Rec; END_VAR\nVAR i : DINT; n : DINT; END_VAR\n" +
			"n := S.A;\nFOR i := 1 TO 500 DO n := n + 1; END_FOR;\nS.A := n;\nEND_PROGRAM",
		Libraries: []string{mergeLib},
		Driver:    nio.NewMemory(),
		Tags:      []TagDef{Typed("S", RoleState, "Rec")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 5; round++ {
		stop := make(chan struct{})
		scanned := make(chan struct{})
		go func() {
			defer close(scanned)
			for {
				select {
				case <-stop:
					return
				default:
					r.Scan()
				}
			}
		}()
		var wg sync.WaitGroup
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := w; i < 200; i += 4 {
					if errs := r.Tags().Write([]WriteOp{{Path: fmt.Sprintf("S.Flags[%d]", i), Value: round%2 == 0}}); errs[0] != nil {
						t.Error(errs[0])
						return
					}
				}
			}(w)
		}
		wg.Wait()
		close(stop)
		<-scanned
		r.Scan()
		lost := 0
		for i := 0; i < 200; i++ {
			if v, _ := r.Tags().ReadPath(fmt.Sprintf("S.Flags[%d]", i)); v != (round%2 == 0) {
				lost++
			}
		}
		if lost > 0 {
			t.Fatalf("round %d: %d of 200 member writes lost to the program's commit", round, lost)
		}
	}
}

// pathSpy is a view that keeps the last member path the VM handed it.
type pathSpy struct {
	*scanView
	path ir.MemberPath
}

func (s *pathSpy) WriteGlobalMember(name string, val ir.Value, path ir.MemberPath) error {
	s.path = path
	return s.scanView.WriteGlobalMember(name, val, path)
}

// The member bookkeeping is allocation-free once a tag's mask exists:
// marking, a commit that needs no merge, and the reset.
// (The VM's own copy of the aggregate it assigns into predates #578 and is
// outside this test.)
func TestMemberCommitAllocatesNothing(t *testing.T) {
	r := newMergeRuntime(t, nil, "S.Items[2].X := 1;")
	r.Scan()
	p := r.Program()
	spy := &pathSpy{scanView: newScanView(r.Tags())}
	spy.snapshot(p.exts)
	if err := ir.Run(p.prog, p.frame, spy); err != nil {
		t.Fatal(err)
	}
	spy.commit()
	if spy.path.Len() != 3 {
		t.Fatalf("captured path has %d steps, want 3 (Items, [2], X)", spy.path.Len())
	}
	s, _ := r.Tags().ReadGlobal("S")
	run := func() {
		spy.snapshot(p.exts)
		_ = spy.scanView.WriteGlobalMember("S", s, spy.path)
		spy.commit()
	}
	if n := testing.AllocsPerRun(100, run); n != 0 {
		t.Errorf("member write + commit = %v allocs per scan, want 0", n)
	}
}
