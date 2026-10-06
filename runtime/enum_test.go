package runtime

import "testing"

const enumLib = `
TYPE Mode : (Idle, Run := 10, Fault) := Idle; END_TYPE
`

// #238: an enumeration's value shows by member NAME in the plain JSON the
// API, the stream and the editors read; retain persists the integer; a tag
// typed with the enumeration seeds named, by name or by integer.
func TestEnumTagsRenderByName(t *testing.T) {
	rt, err := New(Options{
		Program: `PROGRAM P
VAR_EXTERNAL Cmd : Mode; Echo : Mode; END_VAR
VAR last : Mode; END_VAR
last := Cmd;
IF Cmd = Idle THEN Echo := Run; ELSE Echo := Fault; END_IF;
END_PROGRAM`,
		Libraries: []string{enumLib},
		Tags: []TagDef{
			Typed("Cmd", RoleState, "Mode"),
			Typed("Echo", RoleSetpoint, "Mode", Init("Fault")),
		},
		Retain: &fakeStore{},
	})
	if err != nil {
		t.Fatal(err)
	}
	all := rt.Tags().All()
	if all["Cmd"] != "Idle" || all["Echo"] != "Fault" {
		t.Fatalf("seeded: Cmd=%v Echo=%v; want Idle, Fault", all["Cmd"], all["Echo"])
	}
	rt.Scan()
	all = rt.Tags().All()
	if all["Echo"] != "Run" {
		t.Errorf("after a scan Echo = %v, want Run", all["Echo"])
	}
	if got := rt.prog.Locals()["last"]; got != "Idle" {
		t.Errorf("local last = %v, want Idle", got)
	}
	if v := rt.retainState().Tags["Echo"]; v != int64(10) {
		t.Errorf("retained Echo = %#v, want int64 10", v)
	}
	if v, _ := rt.Tags().ReadGlobal("Echo"); v.I != 10 || v.S != "Run" {
		t.Errorf("typed value = %+v", v)
	}
}
