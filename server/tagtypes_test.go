package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/runtime"
)

// #246: /api/meta carries each tag's declared type, and an enumeration's
// members, so a client can render an enumerated value (which streams as
// its member's name) unquoted instead of as a STRING — for a tag a program
// binds, a manifest tag typed only by its type:, a STRING tag, a struct
// member, and a program local. The stream itself stays type-free.
func TestMetaTagTypes(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: `PROGRAM P
VAR_EXTERNAL Cmd : Mode; Label : STRING; Speed : REAL; P101 : Pump; END_VAR
VAR last : Mode; n : INT; END_VAR
last := Cmd;
IF Cmd = Run THEN Speed := 1.0; END_IF;
END_PROGRAM`,
		Libraries: []string{`
TYPE Mode : (Idle, Run := 10, Fault) := Idle; END_TYPE
TYPE Pump : STRUCT State : Mode; Hz : REAL; END_STRUCT; END_TYPE
`},
		Tags: []runtime.TagDef{
			runtime.Typed("Cmd", runtime.RoleSetpoint, "Mode", runtime.Desc("Operating mode")),
			runtime.Typed("Spare", runtime.RoleSetpoint, "Mode"), // no program binds it
			runtime.Typed("P101", runtime.RoleState, "Pump"),
		},
		Seed: map[string]any{"Label": "hello", "Speed": 0.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(rt)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/meta", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var m metaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	wantEnum := []enumMember{{"Idle", 0}, {"Run", 10}, {"Fault", 11}}
	sameEnum := func(what string, got []enumMember) {
		t.Helper()
		if len(got) != len(wantEnum) {
			t.Fatalf("%s enum = %v, want %v", what, got, wantEnum)
		}
		for i := range got {
			if got[i] != wantEnum[i] {
				t.Errorf("%s enum[%d] = %v, want %v", what, i, got[i], wantEnum[i])
			}
		}
	}
	cmd := m.Tags["Cmd"]
	if cmd.Type != "Mode" || cmd.Desc != "Operating mode" {
		t.Errorf("Cmd = %+v; want type Mode with its desc", cmd)
	}
	sameEnum("Cmd", cmd.Enum)
	if sp := m.Tags["Spare"]; sp.Type != "Mode" {
		t.Errorf("Spare (manifest-typed, unbound) = %+v; want type Mode", sp)
	} else {
		sameEnum("Spare", sp.Enum)
	}
	if l := m.Tags["Label"]; l.Type != "STRING" || l.Enum != nil {
		t.Errorf("Label = %+v; want a STRING with no members", l)
	}
	if s := m.Tags["Speed"]; s.Type != "REAL" {
		t.Errorf("Speed = %+v; want REAL", s)
	}
	p := m.Tags["P101"]
	if p.Type != "Pump" || p.Members["State"] == nil || p.Members["State"].Type != "Mode" {
		t.Fatalf("P101 = %+v; want Pump with its enumerated State member", p)
	}
	sameEnum("P101.State", p.Members["State"].Enum)
	if _, ok := p.Members["Hz"]; ok {
		t.Errorf("P101.Hz is listed; members are only where an enumeration sits beneath")
	}
	if l := m.Locals["last"]; l.Type != "Mode" {
		t.Errorf("local last = %+v; want Mode", l)
	} else {
		sameEnum("last", l.Enum)
	}
	if n := m.Locals["n"]; n.Type != "INT" {
		t.Errorf("local n = %+v; want INT", n)
	}
	// The stream stays lean: a frame carries the member's name, no type.
	rt.Scan()
	if got := rt.Tags().All()["Cmd"]; got != "Idle" {
		t.Errorf("Cmd streams as %v, want Idle", got)
	}
}

// #246: POST /api/tags takes an enumerated tag's member by name (or
// Type#-qualified), as it takes text for a STRING — and refuses a name that
// is no member, listing them, rather than answering 204 for nothing.
func TestWriteEnumTagByName(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: `PROGRAM P
VAR_EXTERNAL Cmd : Mode; END_VAR
END_PROGRAM`,
		Libraries: []string{`TYPE Mode : (Idle, Run := 10, Fault) := Idle; END_TYPE`},
		Tags:      []runtime.TagDef{runtime.Typed("Cmd", runtime.RoleSetpoint, "Mode")},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(rt)
	post := func(body string) int {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/tags", strings.NewReader(body)))
		return rec.Code
	}
	for _, c := range []struct {
		body string
		code int
		want any
	}{
		{`{"name":"Cmd","value":"Run"}`, 204, "Run"},
		{`{"name":"cmd","value":"fault"}`, 204, "Fault"},
		{`{"name":"Cmd","value":"Mode#Idle"}`, 204, "Idle"},
		{`{"name":"Cmd","value":10}`, 204, "Run"},
		{`{"name":"Cmd","value":"Running"}`, 422, "Run"},
		{`{"name":"Cmd","value":"Other#Fault"}`, 422, "Run"},
	} {
		if got := post(c.body); got != c.code {
			t.Errorf("%s: status %d, want %d", c.body, got, c.code)
		}
		if got := rt.Tags().All()["Cmd"]; got != c.want {
			t.Errorf("%s: Cmd = %v, want %v", c.body, got, c.want)
		}
	}
}
