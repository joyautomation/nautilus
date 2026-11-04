package server

import (
	"fmt"
	"sync"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// #578 end to end through POST /api/tags: an operator's write to one member
// of a struct tag survives a program that writes another member of the same
// tag every scan. Before the fix the scan's commit put back the member as
// the program had copied it at scan start, and a write that landed mid-scan
// was lost (8 of 200 in the issue's throwaway test).
const mergeLib = `
TYPE
  Panel : STRUCT
    Heartbeat : DINT;
    Lamps     : ARRAY[0..199] OF BOOL;
    Sel       : DINT;
  END_STRUCT;
END_TYPE
`

const mergeProgram = `PROGRAM Main
VAR_EXTERNAL P : Panel; END_VAR
VAR i : DINT; n : DINT; END_VAR
n := P.Heartbeat;
FOR i := 1 TO 500 DO n := n + 1; END_FOR;
P.Heartbeat := n;
P.Sel := 1;
END_PROGRAM
`

func TestHTTPMemberWritesSurviveProgramMemberWrites(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:   mergeProgram,
		Libraries: []string{mergeLib},
		Driver:    nio.NewMemory(),
		Tags:      []runtime.TagDef{runtime.Typed("P", runtime.RoleState, "Panel")},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(rt)
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
			}
		}
	}()
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < 200; i += 4 {
				body := fmt.Sprintf(`{"name": "P.Lamps[%d]", "value": true}`, i)
				if rec := postTag(t, srv, body); rec.Code != 204 {
					t.Errorf("%s = %d, body %s", body, rec.Code, rec.Body)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	// The same member from both sides: the program assigns P.Sel := 1
	// every scan, so its assignment is the last write — the rule a scalar
	// tag has always had.
	if rec := postTag(t, srv, `{"name": "P.Sel", "value": 7}`); rec.Code != 204 {
		t.Fatalf("P.Sel write = %d, body %s", rec.Code, rec.Body)
	}
	close(stop)
	<-scanned
	rt.Scan()
	lost := 0
	for i := 0; i < 200; i++ {
		if v, _ := rt.Tags().ReadPath(fmt.Sprintf("P.Lamps[%d]", i)); v != true {
			lost++
		}
	}
	if lost > 0 {
		t.Errorf("%d of 200 HTTP member writes lost to the program's commit", lost)
	}
	if v, _ := rt.Tags().ReadPath("P.Sel"); v != int64(1) {
		t.Errorf("P.Sel = %v, want the program's 1", v)
	}
	if v, _ := rt.Tags().ReadPath("P.Heartbeat"); v == int64(0) {
		t.Error("the program never committed its own member")
	}
}
