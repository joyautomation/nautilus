package runtime

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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
    Ws    : ARRAY[0..199] OF DWORD;
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

// The plain integer tags the bit tests run against: every width and
// family the language allows bit access on.
var bitTagTypes = []struct{ name, typ string }{
	{"B8", "BYTE"}, {"W16", "WORD"}, {"D32", "DWORD"}, {"L64", "LWORD"},
	{"SI8", "SINT"}, {"I16", "INT"}, {"DI32", "DINT"}, {"LI64", "LINT"},
	{"US8", "USINT"}, {"UI16", "UINT"}, {"UD32", "UDINT"}, {"UL64", "ULINT"},
}

// newBitRuntime is newMergeRuntime with the plain integer tags of
// bitTagTypes in scope too. src is the main program's body, or, when it
// starts with PROGRAM, the whole program.
func newBitRuntime(t *testing.T, clk Clock, src string) *Runtime {
	t.Helper()
	ext := "VAR_EXTERNAL S : Rec; T : Rec; Tbl : ARRAY[1..4] OF DINT;"
	tags := []TagDef{
		Typed("S", RoleState, "Rec"),
		Typed("T", RoleState, "Rec"),
		Typed("Tbl", RoleState, "ARRAY[1..4] OF DINT"),
	}
	for _, b := range bitTagTypes {
		ext += " " + b.name + " : " + b.typ + ";"
		tags = append(tags, Typed(b.name, RoleState, b.typ))
	}
	if !strings.HasPrefix(src, "PROGRAM") {
		src = "PROGRAM Main\n" + ext + " END_VAR\nVAR wall : LOCAL_TIME; END_VAR\nwall();\n" + src + "\nEND_PROGRAM"
	} else {
		src = strings.Replace(src, "VAR_EXTERNAL", ext, 1)
	}
	r, err := New(Options{
		Program:   src,
		Libraries: []string{mergeLib},
		Driver:    nio.NewMemory(),
		Tags:      tags,
		Clock:     clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A bit assignment assigns that bit only. An external write that sets a
// DIFFERENT bit of the same word mid-scan survives — every front door
// writes the whole word, so it arrives as the word with bit 5 set — on a
// plain tag of every integer width and family, on a member, a nested
// member, an array element and a member of an array-of-structs element.
// A sibling member beside the word survives as before.
func TestExternalBitWriteSurvivesProgramBitWrite(t *testing.T) {
	targets := []string{"S.Word", "S.In.X", "Tbl[2]", "S.Items[2].Y"}
	for _, b := range bitTagTypes {
		targets = append(targets, b.name)
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			clk := newGateClock()
			r := newBitRuntime(t, clk, target+".3 := TRUE;")
			parkedScan(t, r, clk, func() {
				write(t, r, target, 32) // bit 5, from outside
				write(t, r, "S.B", 4)
			})
			wantPath(t, r, target, int64(40))
			wantPath(t, r, "S.B", int64(4))
		})
	}
	// The top bit of each bit string, and a partial access (the second
	// byte), against an external write of bit 0.
	for _, tc := range []struct {
		target, body string
		want         int64
	}{
		{"B8", "B8.7 := TRUE;", 0x81},
		{"W16", "W16.15 := TRUE;", 0x8001},
		{"D32", "D32.31 := TRUE;", 0x80000001},
		{"L64", "L64.63 := TRUE;", int64(-1<<63 | 1)},
		{"W16", "W16.%B1 := 16#AB;", 0xAB01},
		{"S.Word", "S.Word.%B1 := 16#AB;", 0xAB01},
	} {
		t.Run(tc.body, func(t *testing.T) {
			clk := newGateClock()
			r := newBitRuntime(t, clk, tc.body)
			parkedScan(t, r, clk, func() { write(t, r, tc.target, 1) })
			wantPath(t, r, tc.target, tc.want)
		})
	}
}

// The same bit from both sides in one scan: the program's assignment wins,
// as it does for a member and a scalar tag — even when it assigns the value
// the bit already had. Bits it did not assign are the writer's.
func TestProgramAssignmentWinsTheSameBit(t *testing.T) {
	for _, target := range []string{"D32", "S.Word", "Tbl[1]"} {
		t.Run(target, func(t *testing.T) {
			clk := newGateClock()
			r := newBitRuntime(t, clk, target+".3 := TRUE;\n"+target+".4 := FALSE;")
			r.Scan() // bit 3 is set, bit 4 clear already
			wantPath(t, r, target, int64(8))
			parkedScan(t, r, clk, func() {
				// Clears bit 3 and sets bit 4 (both the program's), and sets
				// bit 5 (not the program's).
				write(t, r, target, 16|32)
			})
			wantPath(t, r, target, int64(8|32))
		})
	}
}

// Assigning the word itself assigns every bit: an external write of
// another bit mid-scan is the program's to overwrite, in either order with
// a bit write, for a plain tag and a member.
func TestWholeWordAssignmentOwnsEveryBit(t *testing.T) {
	for _, tc := range []struct {
		target, body string
		want         int64
	}{
		{"D32", "D32 := 1;", 1},
		{"D32", "D32.3 := TRUE;\nD32 := 1;", 1},
		{"D32", "D32 := 1;\nD32.3 := TRUE;", 9},
		{"S.Word", "S.Word := 1;", 1},
		{"S.Word", "S.Word.3 := TRUE;\nS.Word := 1;", 1},
		{"S.Word", "S.Word := 1;\nS.Word.3 := TRUE;", 9},
		{"S.Word", "S := T;\nS.Word.3 := TRUE;", 8},
	} {
		t.Run(tc.body, func(t *testing.T) {
			clk := newGateClock()
			r := newBitRuntime(t, clk, tc.body)
			parkedScan(t, r, clk, func() { write(t, r, tc.target, 32) })
			wantPath(t, r, tc.target, tc.want)
		})
	}
}

// Ladder coils on bits go through the same path: an output coil, a set
// coil and a reset coil each own their bit only, on a plain tag and on a
// member, and the writer's bit beside them survives.
func TestLadderBitCoilsMergeByBit(t *testing.T) {
	clk := newGateClock()
	r := newBitRuntime(t, clk, `PROGRAM Main
VAR_EXTERNAL END_VAR
LD
  RUNG gate
    wall:LOCAL_TIME()
  RUNG coil
    S.Flags[0] ( D32.3 )
  RUNG set
    S.Flags[0] ( S D32.4 )
  RUNG reset
    S.Flags[0] ( R D32.6 )
  RUNG member
    S.Flags[0] ( S.Word.3 )
END_LD
END_PROGRAM`)
	write(t, r, "D32", 64) // bit 6 set, for the reset coil to clear
	write(t, r, "S.Flags[0]", true)
	parkedScan(t, r, clk, func() {
		write(t, r, "D32", 64|32) // bit 5 from outside
		write(t, r, "S.Word", 32)
	})
	wantPath(t, r, "D32", int64(8|16|32))
	wantPath(t, r, "S.Word", int64(8|32))
	if n := r.Stats().LogicErrors; n != 0 {
		t.Fatalf("%d logic errors", n)
	}
}

// The sign bit of a signed word against an outside write to another bit of
// it in the same scan (#611): the result is in the type's range and has
// both bits. The program's sign carries the bits above it (the VM holds a
// word sign-extended), so they are the program's too: without that, the
// writer's -1 with the program's I16.15 := FALSE committed -32769, and the
// writer's 1 with SI8.7 := TRUE committed 129. A plain word and a member
// word (S.Word is a DINT), set and cleared, and in ladder.
func TestSignBitMergesWithAnOutsideBit(t *testing.T) {
	for _, tc := range []struct {
		target, body string
		outside      int64 // written mid-scan; the word starts at 0
		want         int64
	}{
		{"SI8", "SI8.7 := TRUE;", 1, -127},
		{"SI8", "SI8.7 := FALSE;", -1, 127},
		{"I16", "I16.15 := TRUE;", 1, -32767},
		{"I16", "I16.15 := FALSE;", -1, 32767},
		{"I16", "I16.%B1 := 16#80;", 1, -32767},
		{"I16", "I16.%B1.7 := FALSE;", -1, 32767},
		{"DI32", "DI32.31 := TRUE;", 1, -2147483647},
		{"DI32", "DI32.31 := FALSE;", -1, 2147483647},
		{"LI64", "LI64.63 := TRUE;", 1, -1<<63 | 1},
		{"LI64", "LI64.63 := FALSE;", -1, 1<<63 - 1},
		{"S.Word", "S.Word.31 := TRUE;", 1, -2147483647},
		{"S.Word", "S.Word.31 := FALSE;", -1, 2147483647},
		{"Tbl[2]", "Tbl[2].31 := TRUE;", 1, -2147483647},
		{"S.Items[2].Y", "S.Items[2].Y.31 := FALSE;", -1, 2147483647},
		// A bit below the sign already merged on a negative word.
		{"I16", "I16.3 := FALSE;", -1, -9},
	} {
		t.Run(tc.body, func(t *testing.T) {
			clk := newGateClock()
			r := newBitRuntime(t, clk, tc.body)
			parkedScan(t, r, clk, func() { write(t, r, tc.target, tc.outside) })
			wantPath(t, r, tc.target, tc.want)
		})
	}
	t.Run("ladder", func(t *testing.T) {
		clk := newGateClock()
		r := newBitRuntime(t, clk, `PROGRAM Main
VAR_EXTERNAL END_VAR
LD
  RUNG gate
    wall:LOCAL_TIME()
  RUNG coil
    S.Flags[0] ( I16.15 )
  RUNG set
    S.Flags[0] ( S SI8.7 )
  RUNG reset
    S.Flags[0] ( R DI32.31 )
  RUNG member
    S.Flags[0] ( S.Word.%X31 )
END_LD
END_PROGRAM`)
		write(t, r, "S.Flags[0]", true)
		write(t, r, "DI32", -1) // the reset coil clears its sign
		parkedScan(t, r, clk, func() {
			write(t, r, "I16", 1)
			write(t, r, "SI8", 2)
			write(t, r, "DI32", -2) // bit 0 cleared from outside
			write(t, r, "S.Word", 4)
		})
		wantPath(t, r, "I16", int64(-32767))
		wantPath(t, r, "SI8", int64(-126))
		wantPath(t, r, "DI32", int64(2147483646))
		wantPath(t, r, "S.Word", int64(-2147483644))
		if n := r.Stats().LogicErrors; n != 0 {
			t.Fatalf("%d logic errors", n)
		}
	})
}

// Ladder coils on the sign bit of a signed word, with nothing else writing:
// an output coil, a set coil and a reset coil give in-range values.
func TestLadderCoilOnSignBit(t *testing.T) {
	r := newBitRuntime(t, nil, `PROGRAM Main
VAR_EXTERNAL END_VAR
LD
  RUNG coil
    S.Flags[0] ( I16.15 )
  RUNG set
    S.Flags[0] ( S SI8.7 )
  RUNG reset
    S.Flags[0] ( R DI32.31 )
  RUNG off
    /S.Flags[0] ( S.Word.31 )
END_LD
END_PROGRAM`)
	write(t, r, "S.Flags[0]", true)
	write(t, r, "DI32", -1)
	write(t, r, "S.Word", -1)
	r.Scan()
	wantPath(t, r, "I16", int64(-32768))
	wantPath(t, r, "SI8", int64(-128))
	wantPath(t, r, "DI32", int64(2147483647))
	wantPath(t, r, "S.Word", int64(2147483647))
	write(t, r, "S.Flags[0]", false)
	r.Scan()
	wantPath(t, r, "I16", int64(0))
	wantPath(t, r, "SI8", int64(-128)) // latched
	wantPath(t, r, "S.Word", int64(-1))
}

// A forced word: the program's bit lands in the tag's ACTUAL value without
// the forced value's other bits, so removing the force returns the word to
// its real value with only the program's bit added. The same for a forced
// member word.
func TestBitCommitKeepsForcedBitsOutOfActual(t *testing.T) {
	r := newBitRuntime(t, nil, "D32.3 := TRUE;\nS.Word.3 := TRUE;")
	write(t, r, "D32", 32)
	write(t, r, "S.Word", 32)
	for _, path := range []string{"D32", "S.Word"} {
		if err := r.Tags().Force(path, 0x41); err != nil {
			t.Fatal(err)
		}
	}
	r.Scan()
	wantPath(t, r, "D32", int64(0x41))
	wantPath(t, r, "S.Word", int64(0x41))
	r.Tags().Unforce("D32")
	r.Tags().Unforce("S.Word")
	wantPath(t, r, "D32", int64(32|8))
	wantPath(t, r, "S.Word", int64(32|8))
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
// every scan, and bit 0 of 200 words, while writers set 200 other members,
// and another bit of each of those 200 words, concurrently through the
// front door. Every one of them must be there at the end (the throwaway
// test that found #578 lost 8 of 200). The program spins between its read
// and its write to hold the window open. Then a plain word tag the program
// sets bit 0 of every scan: each external write of another bit is checked
// once two further scans have committed. Run with -race.
func TestConcurrentMemberWritesAreNeverLost(t *testing.T) {
	r, err := New(Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL S : Rec; W : DWORD; END_VAR\nVAR i : DINT; n : DINT; END_VAR\n" +
			"n := S.A;\nFOR i := 1 TO 500 DO n := n + 1; END_FOR;\nS.A := n;\n" +
			"FOR i := 0 TO 199 DO S.Ws[i].0 := TRUE; END_FOR;\nW.0 := TRUE;\nEND_PROGRAM",
		Libraries: []string{mergeLib},
		Driver:    nio.NewMemory(),
		Tags:      []TagDef{Typed("S", RoleState, "Rec"), Typed("W", RoleState, "DWORD")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 5; round++ {
		var scans atomic.Uint64
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
					scans.Add(1)
				}
			}
		}()
		bit := int64(1) << (5 + round%2) // a different bit each round
		var wg sync.WaitGroup
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := w; i < 200; i += 4 {
					if errs := r.Tags().Write([]WriteOp{
						{Path: fmt.Sprintf("S.Flags[%d]", i), Value: round%2 == 0},
						{Path: fmt.Sprintf("S.Ws[%d]", i), Value: bit},
					}); errs[0] != nil || errs[1] != nil {
						t.Error(errs)
						return
					}
				}
			}(w)
		}
		wg.Wait()
		lostW := 0
		for k := 1; k <= 20; k++ {
			write(t, r, "W", int64(1)<<k)
			after := scans.Load() + 2
			deadline := time.Now().Add(10 * time.Second)
			for scans.Load() < after {
				if time.Now().After(deadline) {
					t.Fatal("the scan loop stopped")
				}
				time.Sleep(10 * time.Microsecond)
			}
			if v, _ := r.Tags().ReadPath("W"); v != int64(1)<<k|1 {
				lostW++
			}
		}
		close(stop)
		<-scanned
		r.Scan()
		lost, lostBits := 0, 0
		for i := 0; i < 200; i++ {
			if v, _ := r.Tags().ReadPath(fmt.Sprintf("S.Flags[%d]", i)); v != (round%2 == 0) {
				lost++
			}
			if v, _ := r.Tags().ReadPath(fmt.Sprintf("S.Ws[%d]", i)); v != bit|1 {
				lostBits++
			}
		}
		if lost > 0 || lostBits > 0 || lostW > 0 {
			t.Fatalf("round %d: lost to the program's commit: %d of 200 member writes, %d of 200 bit writes into member words, %d of 20 bit writes into a plain word",
				round, lost, lostBits, lostW)
		}
	}
}

// pathSpy is a view that keeps every member path the VM handed it.
type pathSpy struct {
	*scanView
	names []string
	paths []ir.MemberPath
}

func (s *pathSpy) WriteGlobalMember(name string, val ir.Value, path ir.MemberPath) error {
	s.names, s.paths = append(s.names, name), append(s.paths, path)
	return s.scanView.WriteGlobalMember(name, val, path)
}

// The member bookkeeping is allocation-free once a tag's mask exists:
// marking a member, a bit of a member word and a bit of a plain word tag, a
// commit that needs no merge, and the reset.
// (The VM's own copy of the aggregate it assigns into predates #578 and is
// outside this test.)
func TestMemberCommitAllocatesNothing(t *testing.T) {
	r := newBitRuntime(t, nil, "S.Items[2].X := 1;\nS.Word.3 := TRUE;\nD32.3 := TRUE;")
	r.Scan()
	p := r.Program()
	spy := &pathSpy{scanView: newScanView(r.Tags())}
	spy.snapshot(p.exts)
	if err := ir.Run(p.prog, p.frame, spy); err != nil {
		t.Fatal(err)
	}
	spy.commit()
	if len(spy.paths) != 3 {
		t.Fatalf("captured %d member writes, want 3", len(spy.paths))
	}
	for i, want := range []struct {
		steps int
		bits  uint64
	}{{3, 0}, {1, 1 << 3}, {0, 1 << 3}} {
		if got := spy.paths[i]; got.Len() != want.steps || got.Bits() != want.bits {
			t.Fatalf("write %d (%s): %d steps, bits %#x; want %d steps, bits %#x",
				i, spy.names[i], got.Len(), got.Bits(), want.steps, want.bits)
		}
	}
	vals := make([]ir.Value, len(spy.names))
	for i, name := range spy.names {
		vals[i], _ = r.Tags().ReadGlobal(name)
	}
	run := func() {
		spy.snapshot(p.exts)
		for i, name := range spy.names {
			_ = spy.scanView.WriteGlobalMember(name, vals[i], spy.paths[i])
		}
		spy.commit()
	}
	if n := testing.AllocsPerRun(100, run); n != 0 {
		t.Errorf("member and bit writes + commit = %v allocs per scan, want 0", n)
	}
	// A bit merge onto a word written mid-scan allocates nothing either.
	var m writeMask
	path := spy.paths[2]
	if n := testing.AllocsPerRun(100, func() {
		m.mark(ir.IntVal(8), path)
		_ = m.merge(ir.IntVal(32), ir.IntVal(8))
		m.reset()
	}); n != 0 {
		t.Errorf("bit mark + merge = %v allocs, want 0", n)
	}
}
