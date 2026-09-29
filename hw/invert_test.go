package hw

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// The manifest read backwards must read forwards to the same value: for
// every binding shape the importers generate, Apply(Invert(v)) == v.
func TestInvertRoundTrips(t *testing.T) {
	boolF := Field{Name: "OperUp", Kind: ir.TypeBool}
	intF := Field{Name: "Health", Kind: ir.TypeInt}
	realF := Field{Name: "SpeedMbps", Kind: ir.TypeReal}
	strF := Field{Name: "Name", Kind: ir.TypeString}
	health := map[string]any{"OK": 0, "Warning": 1, "Critical": 2}

	cases := []struct {
		name string
		b    Binding
		f    Field
		cur  Raw
		v    ir.Value
		want Raw // the wire value served, when it matters
	}{
		{"eq true", Binding{Eq: 1}, boolF, RawIntVal(2), ir.BoolVal(true), RawIntVal(1)},
		{"eq false from up is down(2)", Binding{Eq: 1}, boolF, RawIntVal(1), ir.BoolVal(false), RawIntVal(2)},
		{"eq false keeps a recorded lowerLayerDown(7)", Binding{Eq: 1}, boolF, RawIntVal(7), ir.BoolVal(false), RawIntVal(7)},
		{"eq on a string", Binding{Eq: "On"}, boolF, RawStringVal("Off"), ir.BoolVal(true), RawStringVal("On")},
		{"eq bool false", Binding{Eq: true}, boolF, RawBoolVal(true), ir.BoolVal(false), RawBoolVal(false)},
		{"map", Binding{Map: health}, intF, RawStringVal("OK"), ir.IntVal(2), RawStringVal("Critical")},
		{"map keeps the recorded key", Binding{Map: map[string]any{"1": true, "3": true, "2": false}}, boolF, RawIntVal(3), ir.BoolVal(true), RawIntVal(3)},
		{"map lowest key wins a tie", Binding{Map: map[string]any{"10": true, "3": true, "2": false}}, boolF, RawIntVal(2), ir.BoolVal(true), RawIntVal(3)},
		{"plain gauge", Binding{}, realF, RawUintVal(10000), ir.RealVal(1000), RawUintVal(1000)},
		{"scaled timeticks", Binding{Scale: 0.01}, Field{Name: "UptimeS", Kind: ir.TypeInt}, RawUintVal(5), ir.IntVal(3600), RawUintVal(360000)},
		{"scale and offset", Binding{Scale: 0.1, Offset: -40}, realF, RawIntVal(0), ir.RealVal(25), RawIntVal(650)},
		{"string", Binding{}, strF, RawStringVal("old"), ir.StringVal("Te1/0/25"), RawStringVal("Te1/0/25")},
		{"float wire", Binding{}, realF, RawFloatVal(0), ir.RealVal(41.5), RawFloatVal(41.5)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := c.b.Invert(c.f, c.v, c.cur)
			if err != nil {
				t.Fatal(err)
			}
			if raw != c.want {
				t.Fatalf("served %+v, want %+v", raw, c.want)
			}
			got, ok, err := c.b.Apply(c.f, raw, nil, time.Now())
			if err != nil || !ok {
				t.Fatalf("Apply(%+v): ok=%v err=%v", raw, ok, err)
			}
			if !sameValue(got, c.v) {
				t.Fatalf("round trip: %s → %+v → %s", valueString(c.v), raw, valueString(got))
			}
		})
	}
}

// A rate member inverts to its counter's per-second rate: integrate that
// and the driver's Counter reads the member back.
func TestInvertRate(t *testing.T) {
	b := Binding{Rate: true, Width: 64, Scale: 8} // ifHCInOctets → InBps
	f := Field{Name: "InBps", Kind: ir.TypeReal}
	raw, err := b.Invert(f, ir.RealVal(8e6), RawUintVal(123))
	if err != nil {
		t.Fatal(err)
	}
	if raw.Kind != RawFloat || raw.F != 1e6 {
		t.Fatalf("want 1e6 octets/s, got %+v", raw)
	}
	var c Counter
	t0 := time.Unix(1000, 0)
	_, _, _ = b.Apply(f, RawUintVal(123), &c, t0)
	got, ok, err := b.Apply(f, RawUintVal(123+uint64(raw.F*5)), &c, t0.Add(5*time.Second))
	if err != nil || !ok || math.Abs(got.F-8e6) > 1e-6 {
		t.Fatalf("read back %v ok=%v err=%v", got.F, ok, err)
	}
}

func TestInvertRefuses(t *testing.T) {
	boolF := Field{Name: "Down", Kind: ir.TypeBool}
	if _, err := (Binding{Derived: "AdminUp && !OperUp"}).Invert(boolF, ir.BoolVal(true), Raw{}); !errors.Is(err, ErrStatic) {
		t.Fatalf("derived: %v", err)
	}
	if _, err := (Binding{Const: 3}).Invert(Field{Name: "Index", Kind: ir.TypeInt}, ir.IntVal(3), Raw{}); !errors.Is(err, ErrStatic) {
		t.Fatalf("const: %v", err)
	}
	if _, err := (Binding{Map: map[string]any{"1": 0}}).Invert(Field{Name: "Health", Kind: ir.TypeInt}, ir.IntVal(2), RawIntVal(1)); err == nil {
		t.Fatal("a value no map key reads must be an error")
	}
	if _, err := (Binding{Eq: "On"}).Invert(boolF, ir.BoolVal(false), RawStringVal("On")); err == nil {
		t.Fatal("eq on a string cannot invent the false value")
	}
}
