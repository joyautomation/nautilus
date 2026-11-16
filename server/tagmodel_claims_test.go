package server

import (
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// Tag-model: quality travels in every stream frame — a non-good entry is
// named, an absent name means good — read off the /api/stream SSE wire
// rather than /api/state.
func TestStreamFrameCarriesQuality(t *testing.T) {
	_, rt, drv := newQualityServer(t)
	f := openStream(t, rt, "")
	first := f.next()
	if len(first.Quality) != 0 {
		t.Fatalf("a healthy first frame carries quality: %v", first.Quality)
	}

	drv.SetQuality("Level", nio.Stale)
	drv.SetQuality("Ghost", nio.NotConnected)
	rt.Scan()
	f.tick()
	fr := f.next()
	if fr.Quality["Level"] != "stale" {
		t.Errorf("stream quality = %v, want Level:stale", fr.Quality)
	}
	if fr.Quality["Ghost"] != "notConnected" {
		t.Errorf("stream quality = %v, want Ghost:notConnected", fr.Quality)
	}
	if _, said := fr.Quality["SP"]; said {
		t.Errorf("a good tag is named in the stream's quality map: %v", fr.Quality)
	}
}

// Tag-model: the stream carries an enumerated value as its member's name.
func TestStreamCarriesEnumeratedValueByName(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: `PROGRAM P
VAR_EXTERNAL Cmd : Mode; END_VAR
END_PROGRAM`,
		Libraries: []string{`TYPE Mode : (Idle, Run := 10, Fault) := Idle; END_TYPE`},
		Tags: []runtime.TagDef{
			runtime.Typed("Cmd", runtime.RoleSetpoint, "Mode", runtime.Init("Run")),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := openStream(t, rt, "")
	first := f.next()
	if got := first.Tags["Cmd"]; got != "Run" {
		t.Errorf("stream carried Cmd = %#v, want the member name \"Run\" (not 10)", got)
	}
	if err := rt.Tags().SetPath("Cmd", "Fault"); err != nil {
		t.Fatal(err)
	}
	f.tick()
	if got := f.next().Tags["Cmd"]; got != "Fault" {
		t.Errorf("after a write the stream carried Cmd = %#v, want \"Fault\"", got)
	}
}

// Tag-model: setpoint roots are writable by member path, and a struct-typed
// output with no init: starts at the zero of its type (#696), so a member
// write lands on it before anything has written the whole value.
func TestMemberWritesToSetpointRootAndInitlessStructOutput(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:   udtProgram,
		Libraries: []string{udtLib},
		Driver:    nio.NewMemory(),
		Tags: []runtime.TagDef{
			runtime.Typed("Feed", runtime.RoleInput, "Motor"),
			runtime.Typed("P101", runtime.RoleState, "Motor"),
			runtime.Typed("Recipe", runtime.RoleSetpoint, "Motor",
				runtime.Init(map[string]any{"Speed": 10.0})),
			runtime.Typed("Cmd", runtime.RoleOutput, "Motor"), // no init:
			runtime.Typed("CmdSeeded", runtime.RoleOutput, "Motor", runtime.Init(map[string]any{})),
			runtime.Setpoint("SP", 65.0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(rt)

	if rec := postTag(t, srv, `{"name": "Recipe.Speed", "value": 33.5}`); rec.Code != 204 {
		t.Fatalf("setpoint member write = %d, body %s", rec.Code, rec.Body)
	}
	if got := tagMember(t, rt, "Recipe", "Speed"); got != 33.5 {
		t.Errorf("Recipe.Speed = %v, want 33.5", got)
	}

	if rec := postTag(t, srv, `{"name": "CmdSeeded.START", "value": true}`); rec.Code != 204 {
		t.Fatalf("seeded struct output member write = %d, body %s", rec.Code, rec.Body)
	}

	if rec := postTag(t, srv, `{"name": "Cmd.START", "value": true}`); rec.Code != 204 {
		t.Fatalf("member write to a struct output with no init: = %d, want 204 (body %s)", rec.Code, rec.Body)
	}
	if got := tagMember(t, rt, "Cmd", "START"); got != true {
		t.Errorf("Cmd.START = %v, want true", got)
	}
	if _, made := rt.Tags().All()["Cmd.START"]; made {
		t.Error("the member write created a top-level tag named Cmd.START")
	}
}
