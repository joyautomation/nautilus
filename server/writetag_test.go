package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// POST /api/tags addressing one MEMBER of a UDT tag — the write a faceplate
// Start button or an alarm setpoint inside an AnalogInput makes. The role
// rule is applied to the ROOT tag, because the store holds whole tags.

const udtLib = `
TYPE
  Limits : STRUCT
    HSP : REAL;
    LSP : REAL;
  END_STRUCT;
  Motor : STRUCT
    START : BOOL;
    Speed : REAL;
    LVL   : Limits;
  END_STRUCT;
END_TYPE
`

const udtProgram = `PROGRAM Test
VAR_EXTERNAL
  Feed : Motor;
  P101 : Motor;
  Cmd  : Motor;
  SP   : REAL;
END_VAR
Cmd.Speed := P101.Speed;
END_PROGRAM
`

// newUDTRuntime has one struct tag per role that matters to a write: a
// driver-owned input (Feed), operator/logic-owned state (P101), and an
// output (Cmd) — which a Sparkplug host driver publishes as a command, so
// it must stay writable.
func newUDTRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program:   udtProgram,
		Libraries: []string{udtLib},
		Driver:    nio.NewMemory(),
		Tags: []runtime.TagDef{
			runtime.Typed("Feed", runtime.RoleInput, "Motor"),
			runtime.Typed("P101", runtime.RoleState, "Motor",
				runtime.Init(map[string]any{"Speed": 42.0, "LVL": map[string]any{"HSP": 80.0}})),
			runtime.Typed("Cmd", runtime.RoleOutput, "Motor", runtime.Init(map[string]any{})),
			runtime.Setpoint("SP", 65.0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func postTag(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/tags", bytes.NewBufferString(body)))
	return rec
}

func tagMember(t *testing.T, rt *runtime.Runtime, tag string, path ...string) any {
	t.Helper()
	var cur any = rt.Tags().All()[tag]
	for _, seg := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%s is not a struct: %v", tag, cur)
		}
		cur = m[seg]
	}
	return cur
}

func TestWriteMemberOfStateAndOutputRoots(t *testing.T) {
	rt := newUDTRuntime(t)
	srv := New(rt)

	if rec := postTag(t, srv, `{"name": "P101.START", "value": true}`); rec.Code != 204 {
		t.Fatalf("state member write = %d, body %s", rec.Code, rec.Body)
	}
	if got := tagMember(t, rt, "P101", "START"); got != true {
		t.Errorf("P101.START = %v, want true", got)
	}
	// A nested member, two segments deep.
	if rec := postTag(t, srv, `{"name": "P101.LVL.HSP", "value": 95.5}`); rec.Code != 204 {
		t.Fatalf("nested member write = %d, body %s", rec.Code, rec.Body)
	}
	if got := tagMember(t, rt, "P101", "LVL", "HSP"); got != 95.5 {
		t.Errorf("P101.LVL.HSP = %v, want 95.5", got)
	}
	// An OUTPUT root is writable: that is how a Sparkplug host commands an
	// edge node — the operator writes the output tag, the driver publishes.
	if rec := postTag(t, srv, `{"name": "Cmd.START", "value": true}`); rec.Code != 204 {
		t.Fatalf("output member write = %d, body %s", rec.Code, rec.Body)
	}
	if got := tagMember(t, rt, "Cmd", "START"); got != true {
		t.Errorf("Cmd.START = %v, want true", got)
	}
}

// An object payload merges into a struct tag: what it names is set, what it
// omits keeps its current value.
func TestWriteStructMapMerges(t *testing.T) {
	rt := newUDTRuntime(t)
	srv := New(rt)

	rec := postTag(t, srv, `{"name": "P101", "value": {"START": true, "LVL": {"LSP": 20.0}}}`)
	if rec.Code != 204 {
		t.Fatalf("map write = %d, body %s", rec.Code, rec.Body)
	}
	if got := tagMember(t, rt, "P101", "START"); got != true {
		t.Errorf("START = %v", got)
	}
	if got := tagMember(t, rt, "P101", "LVL", "LSP"); got != 20.0 {
		t.Errorf("LVL.LSP = %v", got)
	}
	if got := tagMember(t, rt, "P101", "Speed"); got != 42.0 {
		t.Errorf("Speed = %v, want 42 — a merge must not zero what it omits", got)
	}
	if got := tagMember(t, rt, "P101", "LVL", "HSP"); got != 80.0 {
		t.Errorf("LVL.HSP = %v, want 80 — a merge must not zero what it omits", got)
	}
}

// A driver-owned input's members are refused: the driver replaces the whole
// tag before the next scan, so the read-modify-write could not survive even
// one cycle.
func TestWriteMemberOfInputRootRefused(t *testing.T) {
	rt := newUDTRuntime(t)
	srv := New(rt)

	rec := postTag(t, srv, `{"name": "Feed.START", "value": true}`)
	if rec.Code != 400 {
		t.Fatalf("input member write = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "driver-owned input") {
		t.Errorf("body = %q", rec.Body.String())
	}
	// Same for a whole-struct merge into an input.
	rec = postTag(t, srv, `{"name": "Feed", "value": {"START": true}}`)
	if rec.Code != 400 {
		t.Errorf("input map write = %d, want 400", rec.Code)
	}
}

// Every rejection carries its reason in the body, and none of them may
// leave a tag behind.
func TestWriteMemberErrorsAre400WithReason(t *testing.T) {
	rt := newUDTRuntime(t)
	srv := New(rt)

	cases := []struct {
		name, body, want string
	}{
		{"unknown member", `{"name": "P101.STRAT", "value": true}`,
			"tag P101: unknown member STRAT (did you mean START?)"},
		{"nested unknown member", `{"name": "P101.LVL.HSPX", "value": 1.0}`,
			"tag P101.LVL: unknown member HSPX (did you mean HSP?)"},
		{"type mismatch", `{"name": "P101.START", "value": 1.0}`,
			"tag P101.START: want BOOL, got a number"},
		{"non-struct root", `{"name": "SP.Hi", "value": 1.0}`,
			"tag SP is a REAL, not a struct — it has no member Hi"},
		{"unknown root", `{"name": "Nope.START", "value": true}`,
			"undefined tag Nope"},
		{"scalar into a struct", `{"name": "P101", "value": {"LVL": 1.0}}`,
			"tag P101.LVL: Limits is a struct — a write must be a mapping of member: value, not a number"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := postTag(t, srv, c.body)
			if rec.Code != 400 {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != c.want {
				t.Errorf("body = %q, want %q", got, c.want)
			}
		})
	}
	// The store is exactly as it was: no dotted tag, nothing retyped.
	for name := range rt.Tags().All() {
		if strings.Contains(name, ".") {
			t.Errorf("a refused write created the tag %q", name)
		}
	}
	if got := tagMember(t, rt, "P101", "START"); got != false {
		t.Errorf("P101.START = %v, want false", got)
	}
}

// A member write still answers to the same auth as any other write.
func TestWriteMemberHonorsAuth(t *testing.T) {
	rt := newUDTRuntime(t)
	srv := New(rt, Options{AuthToken: "s3cret"})
	rec := postTag(t, srv, `{"name": "P101.START", "value": true}`)
	if rec.Code != 401 {
		t.Fatalf("unauthenticated member write = %d, want 401", rec.Code)
	}
	if got := tagMember(t, rt, "P101", "START"); got != false {
		t.Error("unauthenticated member write landed anyway")
	}
}

func TestMetaAdvertisesMemberWrites(t *testing.T) {
	srv := New(newUDTRuntime(t))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/meta", nil))
	var meta struct {
		MemberWrites bool     `json:"memberWrites"`
		Inputs       []string `json:"inputs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.MemberWrites {
		t.Error("meta.memberWrites = false — an HMI cannot tell that members are writable")
	}
	if len(meta.Inputs) != 1 || meta.Inputs[0] != "Feed" {
		t.Errorf("meta.inputs = %v — the root-role rule needs them", meta.Inputs)
	}
}
