package runtime_test

import (
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// The jitter harness's slow task: a plain FOR loop of REAL arithmetic and
// a builtin call. In Phase 1 it provoked thousands of GCs a minute — the
// per-iteration allocations in the VM's call path are what Phase 2 item 3
// drives to zero.
const benchLoop = `PROGRAM Loop
VAR_EXTERNAL
    Iters : INT;
    Acc   : REAL;
END_VAR
VAR
    i   : INT;
    acc : REAL;
END_VAR
acc := 0.0;
FOR i := 1 TO Iters DO
    acc := acc + SQRT(INT_TO_REAL(i)) * 1.0001;
END_FOR;
Acc := acc;
END_PROGRAM`

// The harness's allocation task: string building, which must allocate
// (strings are values) but should allocate exactly the strings.
const benchStrings = `PROGRAM Strs
VAR_EXTERNAL
    Iters : INT;
    Len   : INT;
END_VAR
VAR
    i : INT;
    s : STRING;
END_VAR
s := '';
FOR i := 1 TO Iters DO
    s := CONCAT(RIGHT(s, 48), INT_TO_STRING(i));
END_FOR;
Len := LEN(s);
END_PROGRAM`

func benchProgram(b *testing.B, src string, iters int64) {
	b.Helper()
	r, err := runtime.New(runtime.Options{Program: src, Seed: nio.Values{"Iters": iters, "Acc": 0.0, "Len": int64(0)}})
	if err != nil {
		b.Fatal(err)
	}
	r.Scan()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Scan()
	}
}

// 1000 iterations per scan: allocs/op ÷ 1000 is the per-iteration cost.
func BenchmarkLoopScan(b *testing.B)    { benchProgram(b, benchLoop, 1000) }
func BenchmarkStringsScan(b *testing.B) { benchProgram(b, benchStrings, 200) }

// A user FUNCTION_BLOCK whose body calls builtins, stepped every scan: the
// path that used to build a frame per step.
const benchUserFB = `FUNCTION_BLOCK Filter
VAR_INPUT
    In : REAL;
    K  : REAL;
END_VAR
VAR_OUTPUT
    Out : REAL;
END_VAR
Out := LIMIT(0.0, Out + K * (In - Out), 1000.0);
END_FUNCTION_BLOCK

PROGRAM Main
VAR_EXTERNAL
    Raw : REAL;
    Flt : REAL;
END_VAR
VAR
    f : Filter;
END_VAR
f(In := Raw, K := 0.1);
Flt := f.Out;
END_PROGRAM`

func BenchmarkUserFBScan(b *testing.B) {
	r, err := runtime.New(runtime.Options{Program: benchUserFB, Seed: nio.Values{"Raw": 42.0, "Flt": 0.0}})
	if err != nil {
		b.Fatal(err)
	}
	r.Scan()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Scan()
	}
}
