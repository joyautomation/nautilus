package server

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// #578 end to end through POST /api/tags: an operator's write to one member
// of a struct tag survives a program that writes another member of the same
// tag every scan. Before the fix the scan's commit put back the member as
// the program had copied it at scan start, and a write that landed mid-scan
// was lost (8 of 200 in the issue's throwaway test). The same for a bit:
// the program sets bit 0 of 200 member words and of a plain word tag every
// scan while operators write another bit of the same words, and those
// writes survive too.
const mergeLib = `
TYPE
  Panel : STRUCT
    Heartbeat : DINT;
    Lamps     : ARRAY[0..199] OF BOOL;
    Sel       : DINT;
    Words     : ARRAY[0..199] OF DWORD;
  END_STRUCT;
END_TYPE
`

const mergeProgram = `PROGRAM Main
VAR_EXTERNAL P : Panel; W : DWORD; END_VAR
VAR i : DINT; n : DINT; END_VAR
n := P.Heartbeat;
FOR i := 1 TO 500 DO n := n + 1; END_FOR;
P.Heartbeat := n;
P.Sel := 1;
FOR i := 0 TO 199 DO P.Words[i].0 := TRUE; END_FOR;
W.0 := TRUE;
END_PROGRAM
`

func TestHTTPMemberWritesSurviveProgramMemberWrites(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:   mergeProgram,
		Libraries: []string{mergeLib},
		Driver:    nio.NewMemory(),
		Tags:      []runtime.TagDef{runtime.Typed("P", runtime.RoleState, "Panel"), runtime.Typed("W", runtime.RoleState, "DWORD")},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(rt)
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
				rt.Scan()
				scans.Add(1)
			}
		}
	}()
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < 200; i += 4 {
				for _, body := range []string{
					fmt.Sprintf(`{"name": "P.Lamps[%d]", "value": true}`, i),
					fmt.Sprintf(`{"name": "P.Words[%d]", "value": 32}`, i), // bit 5
				} {
					if rec := postTag(t, srv, body); rec.Code != 204 {
						t.Errorf("%s = %d, body %s", body, rec.Code, rec.Body)
						return
					}
				}
			}
		}(w)
	}
	wg.Wait()
	// A plain word tag: each write of another bit is checked once two
	// further scans have committed.
	lostW := 0
	for k := 1; k <= 20; k++ {
		if rec := postTag(t, srv, fmt.Sprintf(`{"name": "W", "value": %d}`, 1<<k)); rec.Code != 204 {
			t.Fatalf("W write = %d, body %s", rec.Code, rec.Body)
		}
		after := scans.Load() + 2
		deadline := time.Now().Add(10 * time.Second)
		for scans.Load() < after {
			if time.Now().After(deadline) {
				t.Fatal("the scan loop stopped")
			}
			time.Sleep(10 * time.Microsecond)
		}
		if v, _ := rt.Tags().ReadPath("W"); v != int64(1)<<k|1 {
			lostW++
		}
	}
	// The same member from both sides: the program assigns P.Sel := 1
	// every scan, so its assignment is the last write — the rule a scalar
	// tag has always had.
	if rec := postTag(t, srv, `{"name": "P.Sel", "value": 7}`); rec.Code != 204 {
		t.Fatalf("P.Sel write = %d, body %s", rec.Code, rec.Body)
	}
	close(stop)
	<-scanned
	rt.Scan()
	lost, lostBits := 0, 0
	for i := 0; i < 200; i++ {
		if v, _ := rt.Tags().ReadPath(fmt.Sprintf("P.Lamps[%d]", i)); v != true {
			lost++
		}
		if v, _ := rt.Tags().ReadPath(fmt.Sprintf("P.Words[%d]", i)); v != int64(32|1) {
			lostBits++
		}
	}
	if lost > 0 {
		t.Errorf("%d of 200 HTTP member writes lost to the program's commit", lost)
	}
	if lostBits > 0 || lostW > 0 {
		t.Errorf("HTTP bit writes lost to the program's bit writes: %d of 200 into member words, %d of 20 into a plain word", lostBits, lostW)
	}
	if v, _ := rt.Tags().ReadPath("P.Sel"); v != int64(1) {
		t.Errorf("P.Sel = %v, want the program's 1", v)
	}
	if v, _ := rt.Tags().ReadPath("P.Heartbeat"); v == int64(0) {
		t.Error("the program never committed its own member")
	}
}
