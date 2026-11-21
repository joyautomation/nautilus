package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/runtime"
)

func fptr(f float64) *float64 { return &f }

// GET /api/parameters lists the scalar setpoints with their range and drift
// state, and POST /api/tags refuses an out-of-range external write with a
// 400 that names the range, leaving the value (#718).
func TestParametersRouteAndRangeRefusal(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM P\nVAR_EXTERNAL SP : REAL; END_VAR\nEND_PROGRAM",
		Tags:    []runtime.TagDef{runtime.Setpoint("SP", 65.0, runtime.Unit("%"), runtime.Range(fptr(10), fptr(90)))},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	srv := New(rt)

	rec := call(t, srv, "GET", "/api/parameters", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got struct {
		Parameters []runtime.Parameter `json:"parameters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Parameters) != 1 || got.Parameters[0].Name != "SP" || got.Parameters[0].State != runtime.ParamInStep ||
		got.Parameters[0].Min == nil || *got.Parameters[0].Max != 90 || got.Parameters[0].Unit != "%" {
		t.Fatalf("listing = %s", rec.Body)
	}

	rec = call(t, srv, "POST", "/api/tags", `{"name":"SP","value":95}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "[10, 90]") {
		t.Fatalf("out of range: %d %s, want 400 naming the range", rec.Code, rec.Body)
	}
	if rec = call(t, srv, "POST", "/api/tags", `{"name":"SP","value":80}`); rec.Code != http.StatusNoContent {
		t.Fatalf("in range: %d %s", rec.Code, rec.Body)
	}
	rec = call(t, srv, "GET", "/api/parameters", "")
	if !strings.Contains(rec.Body.String(), `"value":80`) || !strings.Contains(rec.Body.String(), `"state":"plant-ahead"`) {
		t.Fatalf("after the edit: %s", rec.Body)
	}
	if !strings.Contains(call(t, srv, "GET", "/api/meta", "").Body.String(), `"max":90`) {
		t.Error("/api/meta does not carry the range")
	}
}
