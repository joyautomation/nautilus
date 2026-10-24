package server

import (
	"encoding/json"
	"math"
	"net/http/httptest"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// A REAL tag that is not a number — a groov EPIC reports an unwired ICTD
// channel as NaN — must not blank the whole state: JSON has no NaN, so it
// renders as null and every other tag still arrives. Before this, the
// encoder's error was dropped and /api/state answered 200 with no body.
func TestStateRendersNaNAsNull(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: testProgram,
		Driver:  nio.NewMemory(),
		Inputs:  []string{"Level"},
		Outputs: []string{"Out"},
		Seed:    nio.Values{"Level": math.NaN(), "SP": math.Inf(1), "Out": 0.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	srv := New(rt)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		t.Fatal("empty body: the NaN tag blanked the frame")
	}
	var f Frame
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Level", "SP"} {
		v, ok := f.Tags[name]
		if !ok {
			t.Errorf("%s missing from frame", name)
		} else if v != nil {
			t.Errorf("%s = %v, want null", name, v)
		}
	}
	if _, ok := f.Tags["Out"]; !ok {
		t.Error("Out missing: the rest of the frame must survive one NaN")
	}
}
