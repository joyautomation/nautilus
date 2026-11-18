package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

type reading struct {
	Value  float64            `json:"value"`
	Limits [2]float32         `json:"limits"`
	Scale  *float64           `json:"scale,omitempty"`
	Hidden float64            `json:"-"`
	Raw    map[string]float64 `json:"raw"`
	At     time.Time          `json:"at"`
	inner  float64
}

type wrapped struct {
	reading
	Name string `json:"name"`
}

// NaN and ±Inf inside typed containers — a []float64, a map of floats, a
// struct — are journalled as strings too, in the ring and in the file,
// not refused: a caller may hand over a value it read as is (#873 review).
func TestNonFiniteInTypedContainers(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	at := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"[]float64", []float64{1, nan, -inf}, `[1,"NaN","-Inf"]`},
		{"[2]float32", [2]float32{float32(inf), 2}, `["+Inf",2]`},
		{"map[string]float64", map[string]float64{"a": nan}, `{"a":"NaN"}`},
		{"map[int]float64", map[int]float64{3: inf}, `{"3":"+Inf"}`},
		{"struct", reading{Value: nan, Hidden: nan, Raw: map[string]float64{"x": 1}, At: at, inner: nan},
			`{"at":"2026-11-18T00:00:00Z","limits":[0,0],"raw":{"x":1},"value":"NaN"}`},
		{"*struct, embedded", &wrapped{reading: reading{Scale: &inf, At: at}, Name: "FT1"},
			`{"at":"2026-11-18T00:00:00Z","limits":[0,0],"name":"FT1","raw":null,"scale":"+Inf","value":0}`},
		{"[]any of []float64", []any{[]float64{nan}}, `[["NaN"]]`},
		{"named float", celsius(nan), `"NaN"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := NewRing(4)
			e, err := r.Record(Entry{Action: "tag.write", Before: c.v, After: c.v, Detail: map[string]any{"v": c.v}})
			if err != nil {
				t.Fatal(err)
			}
			for _, got := range []any{e.Before, r.Recent()[0].After, e.Detail["v"]} {
				b, err := json.Marshal(got)
				if err != nil {
					t.Fatalf("the ring's entry does not encode: %v", err)
				}
				if string(b) != c.want {
					t.Errorf("got %s, want %s", b, c.want)
				}
			}
			dir := t.TempDir()
			j := open(t, dir, &clock{t0()}, FileOptions{})
			if _, err := j.Record(Entry{Action: "tag.write", Before: c.v, Result: ResultOK}); err != nil {
				t.Fatalf("file Record: %v", err)
			}
			if l := lines(t, dir)[0]; !strings.Contains(l, `"before":`+c.want) {
				t.Errorf("line lacks before %s:\n%s", c.want, l)
			}
			mustVerify(t, dir)
		})
	}
}

type celsius float64

// A container with only finite values is left as is, so its JSON is
// exactly encoding/json's.
func TestFiniteTypedContainersUnchanged(t *testing.T) {
	v := reading{Value: 1.5, Raw: map[string]float64{"x": 2}}
	if _, ok := jsonSafe(v).(reading); !ok {
		t.Errorf("a finite struct was rebuilt: %T", jsonSafe(v))
	}
	if _, ok := jsonSafe([]float64{1, 2}).([]float64); !ok {
		t.Error("a finite []float64 was rebuilt")
	}
}

// Cap keeps a value whose JSON fits and replaces one that does not with a
// marker carrying the full JSON's size and SHA-256; NaN does not stop it.
func TestCap(t *testing.T) {
	if got := Cap([]float64{1, 2}, 64); !reflect.DeepEqual(got, []float64{1, 2}) {
		t.Errorf("a small value: %v", got)
	}
	if Cap(nil, 1) != nil {
		t.Error("nil is not nil")
	}
	v := map[string]any{"a": strings.Repeat("x", 100), "b": math.NaN()}
	full, _ := json.Marshal(jsonSafe(v))
	m, ok := Cap(v, 64).(map[string]any)
	sum := sha256.Sum256(full)
	if !ok || m["truncated"] != true || m["bytes"] != len(full) || m["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("Cap = %v", m)
	}
	if h, _ := m["head"].(string); len(h) > 16 || !strings.HasPrefix(string(full), h) {
		t.Errorf("head %q", h)
	}
	if b, err := json.Marshal(m); err != nil || len(b) > 256 {
		t.Errorf("the marker is %d bytes (%v)", len(b), err)
	}
}
