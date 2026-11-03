package server

import (
	"math"
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// A TIME tag or member takes its IEC literal over HTTP, the form a
// manifest's init: and a test's given: already use, as well as a number of
// milliseconds: POST /api/tags {"value":"T#1m"} and POST /api/forces
// {"value":"T#45s"} both land. Before, the first was a 422 ("value must be
// a number or boolean") and the second a 400 ("want TIME (milliseconds),
// got a string"). A malformed literal is the wrong kind of value (422 on a
// whole tag, 400 on a member, as text to a REAL is); a literal finer than a
// millisecond or past int64 is out of range (400), as the same number is,
// never rounded or wrapped. A STRING tag still takes any string as text.
func newTimeRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program:   "PROGRAM Test\nVAR_EXTERNAL\n  MinRun : TIME;\n  Delay : TIME;\n  Recipe : STRING;\n  Step : Step;\nEND_VAR\nEND_PROGRAM\n",
		Libraries: []string{"TYPE Step : STRUCT\n  Hold : TIME;\n  Speed : REAL;\nEND_STRUCT;\nEND_TYPE\n"},
		Driver:    nio.NewMemory(),
		Tags: []runtime.TagDef{
			runtime.Typed("MinRun", runtime.RoleSetpoint, "TIME", runtime.Init("T#30s")),
			runtime.Typed("Delay", runtime.RoleSetpoint, "TIME"),
			runtime.Typed("Recipe", runtime.RoleSetpoint, "STRING", runtime.Init("storm")),
			runtime.Typed("Step", runtime.RoleSetpoint, "Step"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func timeMs(t *testing.T, rt *runtime.Runtime, path string) int64 {
	t.Helper()
	root, sub := ir.SplitAddress(path)
	v, err := rt.Tags().ReadGlobal(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	if len(sub) > 0 {
		if v, _, err = ir.FieldAt(v, rt.Tags().TypeOf(root), sub, root); err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
	}
	if v.Kind != ir.TypeTime {
		t.Fatalf("%s is a %s, want TIME", path, v.Kind)
	}
	return v.I
}

func TestWriteTimeTakesIECLiteral(t *testing.T) {
	rt := newTimeRuntime(t)
	srv := New(rt)
	for _, tc := range []struct {
		body string
		path string
		ms   int64
	}{
		{`{"name":"MinRun","value":"T#1m"}`, "MinRun", 60000},
		{`{"name":"MinRun","value":"TIME#1m30s"}`, "MinRun", 90000},
		{`{"name":"MinRun","value":"t#2s_500ms"}`, "MinRun", 2500},
		{`{"name":"MinRun","value":45000}`, "MinRun", 45000}, // milliseconds still work
		{`{"name":"Delay","value":"T#250ms"}`, "Delay", 250},
		{`{"name":"Step.Hold","value":"T#1h"}`, "Step.Hold", 3600000},
		{`{"name":"Step","value":{"Hold":"T#5s"}}`, "Step.Hold", 5000},
		{`{"name":"Delay","value":"T#1.5s"}`, "Delay", 1500},
		{`{"name":"Delay","value":"T#2000us"}`, "Delay", 2},
		// The extremes land exactly: no float rounding on the way.
		{`{"name":"Delay","value":"T#9223372036854775807ms"}`, "Delay", math.MaxInt64},
		{`{"name":"Delay","value":"T#-106751991167d7h12m55s808ms"}`, "Delay", math.MinInt64},
	} {
		if rec := postTag(t, srv, tc.body); rec.Code != 204 {
			t.Errorf("%s: %d %q, want 204", tc.body, rec.Code, rec.Body)
			continue
		}
		if got := timeMs(t, rt, tc.path); got != tc.ms {
			t.Errorf("%s: %s = %d ms, want %d", tc.body, tc.path, got, tc.ms)
		}
	}

	// A refused value names the problem and lands nothing.
	for _, tc := range []struct {
		body string
		code int
		msg  string
	}{
		{`{"name":"MinRun","value":"T#1x"}`, 422, `"T#1x" is not a TIME literal`},
		{`{"name":"MinRun","value":"soon"}`, 422, `"soon" is not a TIME literal`},
		{`{"name":"Step.Hold","value":"T#"}`, 400, `"T#" is not a TIME literal`},
		// Past int64 milliseconds: refused, not wrapped to a negative.
		{`{"name":"MinRun","value":"T#106751991168d"}`, 400, `"T#106751991168d" is longer than a TIME holds`},
		{`{"name":"MinRun","value":"T#9223372036854775808ms"}`, 400, `longer than a TIME holds`},
		{`{"name":"Step.Hold","value":"T#-106751991168d"}`, 400, `longer than a TIME holds`},
		// Finer than a millisecond: refused, not rounded, as 2.5 is.
		{`{"name":"MinRun","value":"T#1.5ms"}`, 400, `"T#1.5ms" is finer than the millisecond a TIME holds`},
		{`{"name":"MinRun","value":"T#0.4ms"}`, 400, `finer than the millisecond`},
		{`{"name":"MinRun","value":"T#1us"}`, 400, `finer than the millisecond`},
		// A fraction of a millisecond as a number: out of range, a 400,
		// unlike a fraction to an INT (422).
		{`{"name":"MinRun","value":2.5}`, 400, `2.5 is not a whole number of milliseconds`},
		{`{"name":"MinRun","value":1e300}`, 400, `1e+300 milliseconds is out of range for a TIME`},
	} {
		rec := postTag(t, srv, tc.body)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.msg) {
			t.Errorf("%s: %d %q, want %d containing %q", tc.body, rec.Code, rec.Body, tc.code, tc.msg)
		}
	}
	if got := timeMs(t, rt, "MinRun"); got != 45000 {
		t.Errorf("MinRun = %d after refused writes, want 45000", got)
	}

	// A STRING tag is untouched: a T# literal is text to it.
	if rec := postTag(t, srv, `{"name":"Recipe","value":"T#1m"}`); rec.Code != 204 {
		t.Fatalf("Recipe: %d %q", rec.Code, rec.Body)
	}
	if v, _ := rt.Tags().ReadGlobal("Recipe"); v.Kind != ir.TypeString || v.S != "T#1m" {
		t.Errorf("Recipe = %#v, want the STRING \"T#1m\"", v)
	}
}

func TestForceTimeTakesIECLiteral(t *testing.T) {
	rt := newTimeRuntime(t)
	srv := New(rt)
	if rec := call(t, srv, "POST", "/api/forces", `{"name":"MinRun","value":"T#45s"}`); rec.Code != 200 {
		t.Fatalf("force T#45s: %d %s", rec.Code, rec.Body)
	}
	if got := timeMs(t, rt, "MinRun"); got != 45000 {
		t.Errorf("MinRun = %d under a force of T#45s, want 45000", got)
	}
	if rec := call(t, srv, "POST", "/api/forces", `{"name":"Step.Hold","value":"T#1m30s"}`); rec.Code != 200 {
		t.Fatalf("force Step.Hold: %d %s", rec.Code, rec.Body)
	}
	if got := timeMs(t, rt, "Step.Hold"); got != 90000 {
		t.Errorf("Step.Hold = %d under a force of T#1m30s, want 90000", got)
	}
	for body, msg := range map[string]string{
		`{"name":"MinRun","value":"T#forever"}`:       `"T#forever" is not a TIME literal`,
		`{"name":"MinRun","value":"T#106751991168d"}`: `longer than a TIME holds`,
		`{"name":"Step.Hold","value":"T#1.5ms"}`:      `finer than the millisecond`,
	} {
		rec := call(t, srv, "POST", "/api/forces", body)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), msg) {
			t.Errorf("force %s: %d %q, want 400 containing %q", body, rec.Code, rec.Body, msg)
		}
	}
	if got := timeMs(t, rt, "MinRun"); got != 45000 {
		t.Errorf("MinRun = %d under the force after refused forces, want 45000", got)
	}
}
