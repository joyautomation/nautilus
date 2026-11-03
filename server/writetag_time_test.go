package server

import (
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
// got a string"). A malformed literal is a 400 that says what is wrong, and
// a STRING tag still takes any string as text.
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
	} {
		if rec := postTag(t, srv, tc.body); rec.Code != 204 {
			t.Errorf("%s: %d %q, want 204", tc.body, rec.Code, rec.Body)
			continue
		}
		if got := timeMs(t, rt, tc.path); got != tc.ms {
			t.Errorf("%s: %s = %d ms, want %d", tc.body, tc.path, got, tc.ms)
		}
	}

	// A malformed literal is a 400 naming the problem, and lands nothing.
	for _, tc := range []struct{ body, msg string }{
		{`{"name":"MinRun","value":"T#1x"}`, `"T#1x" is not a TIME literal`},
		{`{"name":"MinRun","value":"soon"}`, `"soon" is not a TIME literal`},
		{`{"name":"Step.Hold","value":"T#"}`, `"T#" is not a TIME literal`},
		// A fraction of a millisecond is finer than a TIME holds: a 400
		// (out of range) on a whole tag, unlike a fraction to an INT (422).
		{`{"name":"MinRun","value":2.5}`, `2.5 is not a whole number of milliseconds`},
	} {
		rec := postTag(t, srv, tc.body)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), tc.msg) {
			t.Errorf("%s: %d %q, want 400 containing %q", tc.body, rec.Code, rec.Body, tc.msg)
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
	rec := call(t, srv, "POST", "/api/forces", `{"name":"MinRun","value":"T#forever"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"T#forever" is not a TIME literal`) {
		t.Errorf("force T#forever: %d %q, want 400 naming the literal", rec.Code, rec.Body)
	}
}
