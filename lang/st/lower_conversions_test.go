package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// #244: the conversion matrix through the compiler — typed X_TO_Y, the
// overloaded TO_<type> resolved from the argument's declared type, the
// typed truncations, and the compile errors for what the standard does not
// define.
func TestConversionMatrixThroughST(t *testing.T) {
	src := `
PROGRAM P
VAR_EXTERNAL
    d : DINT; s : SINT; u : UINT; w : WORD; r : REAL; lr : LREAL; tm : TIME; txt : STRING; b : BOOL;
    oInt : INT; oUsint : USINT; oReal : REAL; oLreal : LREAL; oDword : DWORD; oStr : STRING;
    oToUint : UINT; oToReal : REAL; oToStr : STRING; oTrunc : DINT; oTimeInt : INT; oBool : BOOL;
    oFromTxt : WORD; oToTime : TIME;
END_VAR
oInt := DINT_TO_INT(d);
oUsint := SINT_TO_USINT(s);
oReal := DINT_TO_REAL(d);
oLreal := LINT_TO_LREAL(TO_LINT(d) * 1000);
oDword := REAL_TO_DWORD(r);
oStr := UINT_TO_STRING(u);
oToUint := TO_UINT(s);
oToReal := TO_REAL(u);
oToStr := TO_STRING(lr);
oTrunc := LREAL_TRUNC_DINT(lr);
oTimeInt := TIME_TO_INT(tm);
oBool := WORD_TO_BOOL(w);
oFromTxt := STRING_TO_WORD(txt);
oToTime := TO_TIME('T#1m30s');
END_PROGRAM`
	seed := map[string]ir.Value{
		"d": ir.IntVal(70000), "s": ir.IntVal(-1), "u": ir.IntVal(65535), "w": ir.IntVal(0x1234),
		"r": ir.RealVal(1.0), "lr": ir.RealVal(-2.75), "tm": ir.TimeVal(70000), "txt": ir.StringVal("16#00FF"),
		"b": ir.BoolVal(false),
	}
	for k, v := range map[string]ir.Value{"oInt": ir.IntVal(0), "oUsint": ir.IntVal(0), "oReal": ir.RealVal(0),
		"oLreal": ir.RealVal(0), "oDword": ir.IntVal(0), "oStr": ir.StringVal(""), "oToUint": ir.IntVal(0),
		"oToReal": ir.RealVal(0), "oToStr": ir.StringVal(""), "oTrunc": ir.IntVal(0), "oTimeInt": ir.IntVal(0),
		"oBool": ir.BoolVal(false), "oFromTxt": ir.IntVal(0), "oToTime": ir.TimeVal(0)} {
		seed[k] = v
	}
	h, _, _ := scanN(t, src, 1, seed)
	want := map[string]ir.Value{
		"oInt": ir.IntVal(4464), "oUsint": ir.IntVal(255), "oReal": ir.RealVal(70000), "oLreal": ir.RealVal(7e7),
		"oDword": ir.IntVal(0x3F800000), "oStr": ir.StringVal("65535"), "oToUint": ir.IntVal(65535),
		"oToReal": ir.RealVal(65535), "oToStr": ir.StringVal("-2.75"), "oTrunc": ir.IntVal(-2),
		"oTimeInt": ir.IntVal(4464), "oBool": ir.BoolVal(true), "oFromTxt": ir.IntVal(255), "oToTime": ir.TimeVal(90000),
	}
	for k, w := range want {
		got := h.globals[k]
		if got.I != w.I || got.F != w.F || got.S != w.S || got.B != w.B {
			t.Errorf("%s = %+v, want %+v", k, got, w)
		}
	}
}

func TestConversionCompileErrors(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"w := REAL_TO_WORD(r);", "IEC 61131-3 converts REAL only to DWORD, as its bit pattern (REAL_TO_DWORD); for the number, rounded, use REAL_TO_UINT"},
		{"r := WORD_TO_REAL(w);", "use UINT_TO_REAL"},
		{"w := TO_WORD(r);", "there is no REAL_TO_WORD"},
		{"w := INT_TO_WORD(r);", "cannot pass REAL as INT"},
		{"w := TO_WORD(m);", ""}, // an enumeration converts as its integer
		{"r := INT_TO_REAL(m);", "cannot pass Mode as INT"},
	} {
		src := `TYPE Mode : (Idle, Run); END_TYPE
PROGRAM P
VAR_EXTERNAL r : REAL; w : WORD; m : Mode; END_VAR
` + c.body + `
END_PROGRAM`
		prog, err := Parse(src)
		if err != nil {
			t.Fatalf("%s: parse: %v", c.body, err)
		}
		_, err = Lower(prog)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v", c.body, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.body, err, c.want)
		}
	}
}
