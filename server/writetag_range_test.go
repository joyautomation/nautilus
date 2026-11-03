package server

import (
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// #421 part 1 (black-box F-06): POST /api/tags goes through the runtime's
// write front door, so an integer outside its declared width is a 400 that
// names the range, a whole number keeps the tag's kind, a fraction for an
// integer or a boolean for a REAL is a 422, and no refused write retypes
// the tag. Before, Int1 = 70000 was a 204 and the INT held a REAL for good.
func TestWriteOutOfRangeIs400AndNeverRetypes(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:   "PROGRAM Test\nVAR_EXTERNAL\n  Int1 : INT;\n  Byte1 : BYTE;\n  UInt1 : UINT;\n  SP : REAL;\n  Mode : Mode;\nEND_VAR\nEND_PROGRAM\n",
		Libraries: []string{"TYPE Mode : (Idle, Run := 10) := Idle; END_TYPE\n"},
		Driver:    nio.NewMemory(),
		Tags: []runtime.TagDef{
			runtime.Typed("Int1", runtime.RoleSetpoint, "INT"),
			runtime.Typed("Byte1", runtime.RoleSetpoint, "BYTE"),
			runtime.Typed("UInt1", runtime.RoleSetpoint, "UINT"),
			runtime.Typed("SP", runtime.RoleSetpoint, "REAL"),
			runtime.Typed("Mode", runtime.RoleState, "Mode"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(rt)
	for _, tc := range []struct {
		body string
		code int
		msg  string
	}{
		{`{"name":"Int1","value":70000}`, 400, "INT (-32768..32767)"},
		{`{"name":"Int1","value":-32769}`, 400, "INT (-32768..32767)"},
		{`{"name":"Byte1","value":300}`, 400, "BYTE (0..255)"},
		{`{"name":"UInt1","value":-1}`, 400, "UINT (0..65535)"},
		{`{"name":"Int1","value":2.5}`, 422, "want INT"},
		{`{"name":"SP","value":true}`, 422, "want REAL"},
		{`{"name":"Mode","value":3}`, 400, "not the value of any member of Mode"},
		{`{"name":"Int1","value":-32768}`, 204, ""},
		{`{"name":"Byte1","value":255}`, 204, ""},
		{`{"name":"Mode","value":10}`, 204, ""},
	} {
		rec := postTag(t, srv, tc.body)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.msg) {
			t.Errorf("%s: %d %q, want %d containing %q", tc.body, rec.Code, rec.Body, tc.code, tc.msg)
		}
	}
	all := rt.Tags().All()
	if all["Int1"] != int64(-32768) || all["Byte1"] != int64(255) || all["UInt1"] != int64(0) || all["SP"] != 0.0 {
		t.Errorf("store after the writes: Int1=%#v Byte1=%#v UInt1=%#v SP=%#v", all["Int1"], all["Byte1"], all["UInt1"], all["SP"])
	}
	if postTag(t, srv, `{"name":"Int1","value":5}`).Code != 204 || rt.Tags().All()["Int1"] != int64(5) {
		t.Errorf("Int1 = 5 after the refusals: %#v, want int64 5", rt.Tags().All()["Int1"])
	}
}

// An output no scan has written yet is not in the store, but it has a
// declared type, and a write to it is held to that type like any other: it
// cannot be born a REAL or a STRING (#501 review; #421 on the output a
// Sparkplug host commands through).
func TestWriteUndeliveredTypedOutputIsChecked(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM Test\nEND_PROGRAM\n",
		Driver:  nio.NewMemory(),
		Tags: []runtime.TagDef{
			runtime.Typed("Cmd", runtime.RoleOutput, "INT"),
			runtime.Typed("Flag", runtime.RoleOutput, "BOOL"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Tags().ReadGlobal("Cmd"); err == nil {
		t.Skip("Cmd is seeded before any scan; the fallback is not reached")
	}
	srv := New(rt)
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"name":"Cmd","value":70000}`, 400},
		{`{"name":"Cmd","value":2.5}`, 422},
		{`{"name":"Flag","value":"hello"}`, 422},
		{`{"name":"Cmd","value":7}`, 204},
	} {
		if rec := postTag(t, srv, tc.body); rec.Code != tc.code {
			t.Errorf("%s: %d %q, want %d", tc.body, rec.Code, rec.Body, tc.code)
		}
	}
	if v, err := rt.Tags().ReadGlobal("Cmd"); err != nil || v.Kind != ir.TypeInt || v.I != 7 {
		t.Errorf("Cmd = %v (%v), want INT 7", v, err)
	}
	if _, err := rt.Tags().ReadGlobal("Flag"); err == nil {
		t.Errorf("Flag was created by a refused write")
	}
}
