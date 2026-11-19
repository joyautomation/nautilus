package devbind

import (
	"os"
	"strconv"
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

// The type holds the selected points only (a member nobody polls would read
// zero under Good quality forever), compiles, and the tags are the struct
// plus a command per selected writable point.
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
	if _, err := st.Parse(src + "\nPROGRAM P\nVAR d : ABB_ACS355_FMBT21; END_VAR\nd.ControlWord := 1;\nEND_PROGRAM\n"); err != nil {
		t.Fatalf("types do not parse: %v\n%s", err, src)
	}
	for _, m := range []string{"ControlWord : WORD", "StatusWord : WORD"} {
		if !strings.Contains(src, m) {
			t.Errorf("type lacks %q:\n%s", m, src)
		}
	}
	for _, m := range []string{"Reference1", "Current"} {
		if strings.Contains(src, m) {
			t.Errorf("type has %s, which the row does not select:\n%s", m, src)
		}
	}
	var tags []string
	for _, tg := range s.Tags() {
		tags = append(tags, tg.Name+":"+tg.Role+":"+tg.Type)
	}
	if want := "CONV:input:ABB_ACS355_FMBT21 CONV__Online:input:BOOL CONV__Outages:input:DINT CONV_ControlWord:output:WORD"; strings.Join(tags, " ") != want {
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
	if _, err := Resolve(fsys, rows); err == nil || !strings.Contains(err.Error(), "both of type ABB_CLASSIC, member 6 differs") {
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
		{Device{Name: "C", Profile: "pepperl-fuchs-ice3", Endpoint: "h", UnitID: intp(1)}, "unit-id is a Modbus device's"},
		{Device{Name: "C", Profile: "pepperl-fuchs-ice3", Endpoint: "http://h"}, "want host[:port] or opc.tcp://host[:port]"},
		{Device{Name: "C", Profile: "pepperl-fuchs-ice3", Endpoint: "opc.tcp://h:0"}, "bad port"},
		{Device{Name: "C", Profile: "pepperl-fuchs-ice3", Endpoint: "opc.tcp://:4840"}, "want host[:port]"},
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

// Two devices of one type select the same points; a udt: may not hide a
// standard name or share a device's; ids and simulator sources are names.
func TestResolveTypeRules(t *testing.T) {
	fsys := vendored(t, "abb-acs355-fmbt21")
	rows := []Device{
		{Name: "A", Profile: "abb-acs355-fmbt21", Endpoint: "h", Points: []string{"StatusWord"}},
		{Name: "B", Profile: "abb-acs355-fmbt21", Endpoint: "h", Points: []string{"StatusWord", "Current"}},
	}
	if _, err := Resolve(fsys, rows); err == nil || !strings.Contains(err.Error(), "select the same points") {
		t.Errorf("one type, two selections: %v", err)
	}
	rows[1].Points = []string{"Status*"}
	if _, err := Resolve(fsys, rows); err != nil {
		t.Errorf("one type, the same selection by another glob: %v", err)
	}
	withUDT := func(udt string) fstest.MapFS {
		raw := strings.Replace(string(builtin(t, "abb-acs355-fmbt21")), "version:", "udt: "+udt+"\nversion:", 1)
		return fstest.MapFS{"profiles/abb-acs355-fmbt21.yaml": {Data: []byte(raw)}}
	}
	for udt, want := range map[string]string{"TON": "standard function block", "WORD": "elementary type", "conv": "has the name of a device type"} {
		_, err := Resolve(withUDT(udt), []Device{{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: "h"}})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("udt: %s: %v, want %q", udt, err, want)
		}
	}
	if _, err := Resolve(fsys, []Device{{Name: "C", Profile: "../profiles/abb-acs355-fmbt21", Endpoint: "h"}}); err == nil || !strings.Contains(err.Error(), "not a profile id") {
		t.Errorf("a path for a profile id: %v", err)
	}
}

// The device roll-up: a substituted member makes the device Substituted
// (below Bad and Stale), and a quality this build does not know counts as
// Bad, never as Good (#707).
func TestRollupSubstitutedAndUnknown(t *testing.T) {
	asm := []*assembler{{dev: "P1", members: []string{"a", "b"}}}
	member := map[string]int{"a": 0, "b": 1}
	// Every quality, on one member beside a Good one: the device is Good
	// only when the member is, and a known quality never becomes Good.
	for _, q := range nio.AllQualities() {
		got, ok := rollup(asm, member, map[string]nio.Quality{"a": q, "b": nio.Good})["P1"]
		if !ok {
			got = nio.Good
		}
		if (got == nio.Good) != (q == nio.Good) {
			t.Errorf("a member %s beside a good one: device %s", q, got)
		}
	}
	for _, c := range []struct {
		in   map[string]nio.Quality
		want nio.Quality
		good bool
	}{
		{map[string]nio.Quality{"a": nio.Substituted}, nio.Substituted, false},
		{map[string]nio.Quality{"a": nio.Substituted, "b": nio.Stale}, nio.Stale, false},
		{map[string]nio.Quality{"a": nio.Substituted, "b": nio.Bad}, nio.Bad, false},
		{map[string]nio.Quality{"a": nio.Quality(len(nio.AllQualities()))}, nio.Bad, false},
		{map[string]nio.Quality{"a": nio.Good}, nio.Good, true},
	} {
		got, ok := rollup(asm, member, c.in)["P1"]
		if c.good {
			if ok && got != nio.Good {
				t.Errorf("%v: device %v, want Good", c.in, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%v: device %v, want %v", c.in, got, c.want)
		}
	}
}

// limited is the ACS355 profile documenting n Modbus connections (0: not
// documented), vendored.
func limited(t *testing.T, n int) fstest.MapFS {
	t.Helper()
	raw := string(builtin(t, "abb-acs355-fmbt21"))
	if n > 0 {
		edited := strings.Replace(raw, "modbus:\n  port: 502\n", "modbus:\n  port: 502\n  max-connections: "+strconv.Itoa(n)+"\n  max-connections-doc: \"fena §7.1\"\n", 1)
		if edited == raw {
			t.Fatal("the fixture edit did not apply")
		}
		raw = edited
	}
	return fstest.MapFS{"profiles/abb-acs355-fmbt21.yaml": {Data: []byte(raw)}}
}

// validate: is live or static; live on a device documenting one
// connection is refused (the check's connection would be the
// controller's), and allowed at two.
func TestResolveValidate(t *testing.T) {
	row := func(v string) []Device {
		return []Device{{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: "h", Validate: v}}
	}
	for _, tc := range []struct {
		limit int
		v     string
		want  string
	}{
		{0, "live", ""}, {0, "static", ""}, {0, "", ""}, {2, "live", ""}, {1, "static", ""}, {1, "", ""},
		{1, "live", "validate: live, but profile abb-acs355-fmbt21 documents max-connections 1 (fena §7.1)"},
		{0, "Live", `validate: "Live" is not live or static`},
	} {
		_, err := Resolve(limited(t, tc.limit), row(tc.v))
		if tc.want == "" && err != nil {
			t.Errorf("limit %d, validate %q: %v", tc.limit, tc.v, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("limit %d, validate %q: %v, want %q", tc.limit, tc.v, err, tc.want)
		}
	}
}

// The facts a live configuration check composes (#706): the row's own
// validate:, and the default with its reason, static when the profile
// documents two connections or fewer.
func TestSetValidate(t *testing.T) {
	for _, tc := range []struct {
		limit         int
		v             string
		row, def, why string
	}{
		{0, "", "", "live", "profile abb-acs355-fmbt21 does not document a connection limit"},
		{1, "", "", "static", "profile abb-acs355-fmbt21 documents max-connections 1 (fena §7.1)"},
		{2, "live", "live", "static", "profile abb-acs355-fmbt21 documents max-connections 2 (fena §7.1)"},
		{3, "static", "static", "live", "profile abb-acs355-fmbt21 documents max-connections 3, room for a check's own"},
	} {
		s, err := Resolve(limited(t, tc.limit), []Device{{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: "h", Validate: tc.v}})
		if err != nil {
			t.Fatal(err)
		}
		row, def, why, ok := s.Validate("conv")
		if !ok || row != tc.row || def != tc.def || why != tc.why {
			t.Errorf("limit %d, validate %q: (%q, %q, %q, %v), want (%q, %q, %q)", tc.limit, tc.v, row, def, why, ok, tc.row, tc.def, tc.why)
		}
	}
	s, _ := Resolve(limited(t, 0), []Device{{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: "h"}})
	if _, _, _, ok := s.Validate("OTHER"); ok {
		t.Error("a device the set does not have")
	}
}
