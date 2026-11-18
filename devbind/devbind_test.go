package devbind

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

func builtin(t *testing.T, id string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../devprofile/profiles/" + id + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func vendored(t *testing.T, ids ...string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for _, id := range ids {
		fsys["profiles/"+id+".yaml"] = &fstest.MapFile{Data: builtin(t, id)}
	}
	return fsys
}

func intp(n int) *int { return &n }

func TestModbusManifest(t *testing.T) {
	s, err := Resolve(vendored(t, "abb-acs355-fmbt21", "asi-275m"), []Device{
		{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: "10.0.0.9", UnitID: intp(3), Points: []string{"ControlWord", "StatusWord"}},
		{Name: "RB1", Profile: "asi-275m", Endpoint: "10.0.0.10:1502"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := s.ModbusManifest()
	if len(m.Sources) != 2 {
		t.Fatalf("sources %+v", m.Sources)
	}
	conv, rb := m.Sources[0], m.Sources[1]
	if conv.ID != "CONV" || conv.Host != "10.0.0.9" || conv.Port != 502 || conv.UnitID != 3 {
		t.Errorf("CONV source %+v: want 10.0.0.9:502 unit 3 (the row's unit id over the profile's)", conv)
	}
	if rb.Port != 1502 {
		t.Errorf("RB1 port %d, want the endpoint's 1502", rb.Port)
	}
	got := map[string]string{}
	for _, b := range m.Tags {
		if b.Source != "CONV" {
			continue
		}
		kind := "read"
		if b.WriteOnly && b.Writable {
			kind = "write"
		} else if b.Writable || b.WriteOnly {
			kind = "BAD"
		}
		got[b.Name] = kind
	}
	want := map[string]string{"CONV.ControlWord": "read", "CONV_ControlWord": "write", "CONV.StatusWord": "read"}
	if len(got) != len(want) {
		t.Errorf("CONV bindings %v, want %v (points: selects; unselected points are not polled)", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("binding %s: %q, want %q", k, got[k], v)
		}
	}
}

// The type keeps every point whatever a row selects, compiles, and the
// tags are the struct plus a command per selected writable point.
func TestTypesAndTags(t *testing.T) {
	s, err := Resolve(vendored(t, "abb-acs355-fmbt21"), []Device{
		{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: "h", Points: []string{"ControlWord", "Status*"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	src, err := s.TypesST()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Parse(src + "\nPROGRAM P\nVAR d : ABB_ACS355_FMBT21; END_VAR\nd.Current := 1.5;\nEND_PROGRAM\n"); err != nil {
		t.Fatalf("types do not parse: %v\n%s", err, src)
	}
	for _, m := range []string{"ControlWord : WORD", "Reference1 : INT", "Current : REAL"} {
		if !strings.Contains(src, m) {
			t.Errorf("type lacks %q:\n%s", m, src)
		}
	}
	var tags []string
	for _, tg := range s.Tags() {
		tags = append(tags, tg.Name+":"+tg.Role+":"+tg.Type)
	}
	if want := "CONV:input:ABB_ACS355_FMBT21 CONV_ControlWord:output:WORD"; strings.Join(tags, " ") != want {
		t.Errorf("tags %v, want %s", tags, want)
	}
}

func TestResolveSharedType(t *testing.T) {
	a := builtin(t, "abb-acs355-fmbt21")
	same := strings.Replace(string(a), "profile: abb-acs355-fmbt21", "profile: other-drive", 1)
	shared := func(raw string) string { return strings.Replace(raw, "version:", "udt: ABB_CLASSIC\nversion:", 1) }
	fsys := fstest.MapFS{
		"profiles/abb-acs355-fmbt21.yaml": {Data: []byte(shared(string(a)))},
		"profiles/other-drive.yaml":       {Data: []byte(shared(same))},
	}
	rows := []Device{{Name: "A", Profile: "abb-acs355-fmbt21", Endpoint: "h"}, {Name: "B", Profile: "other-drive", Endpoint: "h"}}
	s, err := Resolve(fsys, rows)
	if err != nil {
		t.Fatalf("two profiles with the same points sharing a udt: %v", err)
	}
	if src, _ := s.TypesST(); strings.Count(src, "ABB_CLASSIC :") != 1 {
		t.Errorf("the shared type is declared %d times:\n%s", strings.Count(src, "ABB_CLASSIC :"), src)
	}
	// The same name over different points is a mistake.
	fsys["profiles/other-drive.yaml"] = &fstest.MapFile{Data: []byte(shared(strings.Replace(same, "name: Actual2,", "name: Actual9,", 1)))}
	if _, err := Resolve(fsys, rows); err == nil || !strings.Contains(err.Error(), "both declare type ABB_CLASSIC") {
		t.Errorf("different members under one udt: %v", err)
	}
}

func TestResolveErrors(t *testing.T) {
	fsys := vendored(t, "abb-acs355-fmbt21", "pepperl-fuchs-ice3")
	for _, tc := range []struct {
		row  Device
		want string
	}{
		{Device{Name: "C", Profile: "abb-acs580-fmbt21", Endpoint: "h"}, "naut devices add abb-acs580-fmbt21"},
		{Device{Name: "C", Profile: "abb-acs355-fmbt21"}, "endpoint is required"},
		{Device{Name: "C", Profile: "abb-acs355-fmbt21", Endpoint: "h:99999"}, "bad port"},
		{Device{Name: "C", Profile: "abb-acs355-fmbt21", Endpoint: "h", UnitID: intp(300)}, "not 0-255"},
		{Device{Name: "C", Profile: "abb-acs355-fmbt21", Endpoint: "h", Params: map[string]string{"x": "1"}}, `no param "x"`},
		{Device{Name: "C", Profile: "abb-acs355-fmbt21", Endpoint: "h", Points: []string{"["}}, "points"},
		{Device{Name: "C", Profile: "pepperl-fuchs-ice3", Endpoint: "h"}, "only Modbus devices"},
	} {
		if _, err := Resolve(fsys, []Device{tc.row}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v, want %q", tc.row, err, tc.want)
		}
	}
	if _, err := Resolve(fsys, []Device{{Name: "C", Profile: "abb-acs355-fmbt21", Endpoint: "h"}, {Name: "c", Profile: "abb-acs355-fmbt21", Endpoint: "h"}}); err == nil {
		t.Error("two devices whose names differ only in case: no error")
	}
}

func testAssembler(t *testing.T) *assembler {
	s, err := Resolve(vendored(t, "abb-acs355-fmbt21"), []Device{{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: "h", Points: []string{"StatusWord", "Current"}}})
	if err != nil {
		t.Fatal(err)
	}
	return newAssembler(s.devices[0])
}

// The struct is absent until a member arrives; a member missing from a
// delivery keeps its value; a delivered struct is never changed in place
// by a later delivery (the runtime holds it); members leave the map.
func TestAssembler(t *testing.T) {
	a := testAssembler(t)
	vals := map[string]any{"CONV__Online": false}
	a.take(vals)
	if _, ok := vals["CONV"]; ok {
		t.Fatal("a struct before any member arrived")
	}
	vals = map[string]any{"CONV.StatusWord": int64(0x237), "CONV.Current": 12.5}
	a.take(vals)
	first, ok := vals["CONV"].(ir.Value)
	if !ok || len(vals) != 1 {
		t.Fatalf("delivery %v: want only the struct", vals)
	}
	sw, cur := first.Struct.FieldIndex["StatusWord"], first.Struct.FieldIndex["Current"]
	if first.Fld[sw].I != 0x237 || first.Fld[cur].F != 12.5 {
		t.Fatalf("struct %+v", first.Fld)
	}
	vals = map[string]any{"CONV.Current": 3.0}
	a.take(vals)
	second := vals["CONV"].(ir.Value)
	if second.Fld[sw].I != 0x237 || second.Fld[cur].F != 3 {
		t.Errorf("a member missing from a delivery lost its value: %+v", second.Fld)
	}
	if first.Fld[cur].F != 12.5 {
		t.Error("the earlier delivered struct was changed in place")
	}
}

func TestRollup(t *testing.T) {
	a := testAssembler(t) // StatusWord and Current polled
	member := map[string]int{}
	for _, m := range a.members {
		member[m] = 0
	}
	for _, tc := range []struct {
		in   map[string]nio.Quality
		want nio.Quality
		set  bool
	}{
		{map[string]nio.Quality{}, nio.Good, false},
		{map[string]nio.Quality{"CONV.StatusWord": nio.NotConnected, "CONV.Current": nio.NotConnected}, nio.NotConnected, true},
		{map[string]nio.Quality{"CONV.Current": nio.NotConnected}, nio.Bad, true},
		{map[string]nio.Quality{"CONV.Current": nio.Bad}, nio.Bad, true},
		{map[string]nio.Quality{"CONV.Current": nio.Stale, "CONV.StatusWord": nio.Stale}, nio.Stale, true},
		// a point the row did not select is not polled and says nothing
		{map[string]nio.Quality{"CONV.Actual1": nio.NotConnected}, nio.Good, false},
	} {
		in := map[string]nio.Quality{"OTHER": nio.Stale}
		for k, v := range tc.in {
			in[k] = v
		}
		out := rollup([]*assembler{a}, member, in)
		q, set := out["CONV"]
		if set != tc.set || q != tc.want {
			t.Errorf("%v: CONV %v (set %v), want %v (set %v)", tc.in, q, set, tc.want, tc.set)
		}
		if out["OTHER"] != nio.Stale {
			t.Errorf("%v: a tag that is not a member did not pass through", tc.in)
		}
		for k := range out {
			if strings.HasPrefix(k, "CONV.") {
				t.Errorf("%v: member %s leaked", tc.in, k)
			}
		}
	}
}
