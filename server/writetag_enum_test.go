package server

import (
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// #247: POST /api/tags by member path into an enumerated member — what the
// extension's Set Value and an HMI faceplate send — stores the named value,
// exactly as a whole-tag write of an enumeration does; a name that is no
// member is a 400 naming the members.
const enumRecipeLib = `
TYPE
  Mode : (Idle, Run := 10, Fault) := Idle;
  Step : STRUCT
    Mode : Mode;
  END_STRUCT;
  Recipe : STRUCT
    Mode  : Mode;
    Steps : ARRAY[1..2] OF Step;
  END_STRUCT;
END_TYPE
`

func newEnumRecipeServer(t *testing.T) (*runtime.Runtime, *Server) {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: `PROGRAM Test
VAR_EXTERNAL R : Recipe; Cmd : Mode; END_VAR
END_PROGRAM
`,
		Libraries: []string{enumRecipeLib},
		Driver:    nio.NewMemory(),
		Tags: []runtime.TagDef{
			runtime.Typed("R", runtime.RoleState, "Recipe"),
			runtime.Typed("Cmd", runtime.RoleState, "Mode"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt, New(rt)
}

func TestWriteEnumMemberByPath(t *testing.T) {
	rt, srv := newEnumRecipeServer(t)
	for _, c := range []struct{ body, path, want string }{
		{`{"name":"R.Mode","value":"Run"}`, "R.Mode", "Run"},
		{`{"name":"R.Mode","value":11}`, "R.Mode", "Fault"},
		{`{"name":"R.Steps[2].Mode","value":"Mode#Run"}`, "R.Steps[2].Mode", "Run"},
		{`{"name":"R","value":{"Steps":[{"Mode":"Fault"}]}}`, "R.Steps[1].Mode", "Fault"},
		{`{"name":"Cmd","value":"Run"}`, "Cmd", "Run"}, // whole tag: a name, not refused as text
	} {
		if rec := postTag(t, srv, c.body); rec.Code != 204 {
			t.Errorf("%s → %d %s", c.body, rec.Code, rec.Body.String())
			continue
		}
		if got, _ := rt.Tags().ReadPath(c.path); got != c.want {
			t.Errorf("%s → %s = %v, want %s", c.body, c.path, got, c.want)
		}
	}
	if all := rt.Tags().All(); all["R"].(map[string]any)["Mode"] != "Fault" {
		t.Errorf("/api/state shape: R.Mode = %v", all["R"].(map[string]any)["Mode"])
	}
}

func TestWriteEnumNonMemberIs400ListingMembers(t *testing.T) {
	_, srv := newEnumRecipeServer(t)
	for _, body := range []string{
		`{"name":"R.Mode","value":"Stop"}`,
		`{"name":"R.Steps[1].Mode","value":"Stop"}`,
		`{"name":"Cmd","value":"Stop"}`,
	} {
		rec := postTag(t, srv, body)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"Stop" is not a member of Mode (Idle, Run, Fault)`) {
			t.Errorf("%s → %d %q", body, rec.Code, rec.Body.String())
		}
	}
	if rec := postTag(t, srv, `{"name":"R.Steps[5].Mode","value":"Run"}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "index out of bounds 1..2") {
		t.Errorf("out-of-bounds index → %d %q", rec.Code, rec.Body.String())
	}
}

// A force by member path into an enumeration takes the member name too,
// shows it in the force table, and refuses a non-member with the list.
func TestForceEnumMemberByPath(t *testing.T) {
	rt, srv := newEnumRecipeServer(t)
	rec := call(t, srv, "POST", "/api/forces", `{"name":"R.Steps[2].Mode","value":"Fault"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"R.Steps[2].Mode"`) || !strings.Contains(rec.Body.String(), `"value":"Fault"`) {
		t.Fatalf("force: %d %s", rec.Code, rec.Body)
	}
	if got, _ := rt.Tags().ReadPath("R.Steps[2].Mode"); got != "Fault" {
		t.Errorf("forced member = %v", got)
	}
	if rec := postTag(t, srv, `{"name":"R.Steps[2].Mode","value":"Run"}`); rec.Code != 409 {
		t.Errorf("write to the forced member = %d, want 409", rec.Code)
	}
	rec = call(t, srv, "POST", "/api/forces", `{"name":"R.Mode","value":"Halt"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "(Idle, Run, Fault)") {
		t.Errorf("non-member force: %d %s", rec.Code, rec.Body)
	}
}
