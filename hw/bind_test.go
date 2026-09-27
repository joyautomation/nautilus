package hw

import (
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

func field(name string, k ir.TypeKind) Field { return Field{Name: name, Kind: k} }

func TestBindingApply(t *testing.T) {
	now := time.Unix(1000, 0)
	cases := []struct {
		name string
		b    Binding
		f    Field
		raw  Raw
		want ir.Value
	}{
		{"int passthrough", Binding{}, field("Index", ir.TypeInt), RawIntVal(7), ir.IntVal(7)},
		{"uint to real", Binding{}, field("SpeedMbps", ir.TypeReal), RawUintVal(1000), ir.RealVal(1000)},
		{"string", Binding{}, field("Name", ir.TypeString), RawStringVal("Gi0/1"), ir.StringVal("Gi0/1")},
		{"int to string", Binding{}, field("Serial", ir.TypeString), RawIntVal(42), ir.StringVal("42")},
		{"eq int", Binding{Eq: 1}, field("OperUp", ir.TypeBool), RawIntVal(1), ir.BoolVal(true)},
		{"eq miss", Binding{Eq: 1}, field("OperUp", ir.TypeBool), RawIntVal(2), ir.BoolVal(false)},
		{"eq string", Binding{Eq: "On"}, field("PowerOn", ir.TypeBool), RawStringVal("On"), ir.BoolVal(true)},
		{"map enum", Binding{Map: map[string]any{"OK": 0, "Warning": 1, "Critical": 2}}, field("Health", ir.TypeInt), RawStringVal("Critical"), ir.IntVal(2)},
		{"map to bool", Binding{Map: map[string]any{"1": true, "2": false}}, field("AdminUp", ir.TypeBool), RawIntVal(2), ir.BoolVal(false)},
		{"scale", Binding{Scale: 0.01}, field("UptimeS", ir.TypeInt), RawUintVal(123456), ir.IntVal(1235)},
		{"scale real", Binding{Scale: 0.1, Offset: -1}, field("Value", ir.TypeReal), RawIntVal(255), ir.RealVal(24.5)},
		{"string number to real", Binding{}, field("Value", ir.TypeReal), RawStringVal("36.5"), ir.RealVal(36.5)},
		{"bool raw to real", Binding{}, field("Pct", ir.TypeReal), RawBoolVal(true), ir.RealVal(1)},
		{"const", Binding{Const: 3}, field("Index", ir.TypeInt), RawStringVal("ignored"), ir.IntVal(3)},
		{"float to int rounds", Binding{}, field("Health", ir.TypeInt), RawFloatVal(1.6), ir.IntVal(2)},
	}
	for _, c := range cases {
		if err := c.b.Validate(c.f); err != nil {
			t.Fatalf("%s: validate: %v", c.name, err)
		}
		got, ok, err := c.b.Apply(c.f, c.raw, nil, now)
		if err != nil || !ok {
			t.Fatalf("%s: %v, ok=%v", c.name, err, ok)
		}
		if got.Kind != c.want.Kind || got.I != c.want.I || got.F != c.want.F || got.B != c.want.B || got.S != c.want.S {
			t.Errorf("%s = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestBindingRate(t *testing.T) {
	b := Binding{Rate: true, Width: 64, Scale: 8}
	f := field("InBps", ir.TypeReal)
	if err := b.Validate(f); err != nil {
		t.Fatal(err)
	}
	var c Counter
	t0 := time.Unix(1000, 0)
	if _, ok, err := b.Apply(f, RawUintVal(1000), &c, t0); ok || err != nil {
		t.Fatalf("first sample: ok=%v err=%v", ok, err)
	}
	v, ok, err := b.Apply(f, RawUintVal(2000), &c, t0.Add(10*time.Second))
	if err != nil || !ok || v.F != 800 {
		t.Fatalf("rate = %+v ok=%v err=%v; want 800 bit/s", v, ok, err)
	}
	// A string counter off a text exposition format is fine too.
	c.Reset()
	b32 := Binding{Rate: true, Width: 32}
	b32.Apply(f, RawStringVal("4294967000"), &c, t0)
	v, ok, _ = b32.Apply(f, RawStringVal("200"), &c, t0.Add(time.Second))
	if !ok || v.F != 496 {
		t.Fatalf("32-bit wrap from strings: %+v %v", v, ok)
	}
	if _, _, err := b.Apply(f, RawIntVal(-1), &c, t0); err == nil {
		t.Fatal("negative counter must error")
	}
	if _, _, err := b.Apply(f, RawUintVal(1), nil, t0); err == nil {
		t.Fatal("rate with no counter must error")
	}
}

func TestBindingErrors(t *testing.T) {
	now := time.Unix(1000, 0)
	// Map miss is an error the protocol reports as Bad, not a silent zero.
	b := Binding{Map: map[string]any{"1": true}}
	if _, _, err := b.Apply(field("X", ir.TypeBool), RawIntVal(9), nil, now); err == nil || !strings.Contains(err.Error(), "not in map") {
		t.Errorf("map miss: %v", err)
	}
	// String where a number is wanted.
	if _, _, err := (Binding{}).Apply(field("X", ir.TypeReal), RawStringVal("n/a"), nil, now); err == nil {
		t.Error("non-numeric string into REAL must error")
	}
	// Validation refuses shapes that could never work.
	bad := []struct {
		b Binding
		f Field
	}{
		{Binding{Eq: 1}, field("X", ir.TypeReal)},
		{Binding{Eq: 1, Map: map[string]any{"1": true}}, field("X", ir.TypeBool)},
		{Binding{Rate: true}, field("X", ir.TypeString)},
		{Binding{Width: 16}, field("X", ir.TypeReal)},
		{Binding{Scale: 2}, field("X", ir.TypeBool)},
		{Binding{Const: "seven"}, field("X", ir.TypeInt)},
		{Binding{Map: map[string]any{"a": "x"}}, field("X", ir.TypeInt)},
		{Binding{Derived: "A &&"}, field("X", ir.TypeBool)},
		{Binding{Derived: "A", Scale: 2}, field("X", ir.TypeReal)},
		{Binding{Const: 1, Rate: true}, field("X", ir.TypeReal)},
	}
	for i, c := range bad {
		if err := c.b.Validate(c.f); err == nil {
			t.Errorf("case %d validated; want an error", i)
		}
	}
	if !(Binding{Const: 1}).IsStatic() || !(Binding{Derived: "A"}).IsStatic() || (Binding{}).IsStatic() {
		t.Error("IsStatic")
	}
}
