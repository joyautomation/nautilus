package sparkplug

import (
	"reflect"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// motorDef is the StructDef the round-trip tests bind Template values to.
func motorDef() *ir.StructDef {
	return &ir.StructDef{
		Name: "Motor",
		Fields: []ir.StructField{
			{Name: "Run", Type: ir.BoolT},
			{Name: "Speed", Type: ir.RealT},
			{Name: "Label", Type: ir.StringT},
		},
		FieldIndex: map[string]int{"Run": 0, "Speed": 1, "Label": 2},
	}
}

// pumpDef nests a Motor inside another struct.
func pumpDef(motor *ir.StructDef) *ir.StructDef {
	return &ir.StructDef{
		Name: "Pump",
		Fields: []ir.StructField{
			{Name: "Hours", Type: ir.IntT},
			{Name: "Drive", Type: &ir.Type{Kind: ir.TypeStruct, Struct: motor}},
		},
		FieldIndex: map[string]int{"Hours": 0, "Drive": 1},
	}
}

// TestValueFromMetricScalarRoundTrip walks every scalar kind out through
// MetricFromValue and back in through ValueFromMetric.
func TestValueFromMetricScalarRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		typ  *ir.Type
		val  ir.Value
	}{
		{"bool true", ir.BoolT, ir.BoolVal(true)},
		{"bool false", ir.BoolT, ir.BoolVal(false)},
		{"int", ir.IntT, ir.IntVal(-42)},
		{"int big", ir.IntT, ir.IntVal(1 << 40)},
		{"real", ir.RealT, ir.RealVal(3.5)},
		{"time", ir.TimeT, ir.TimeVal(1500)},
		{"string", ir.StringT, ir.StringVal("hello")},
		{"string empty", ir.StringT, ir.StringVal("")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := MetricFromValue("tag", c.val, "")
			if err != nil {
				t.Fatalf("MetricFromValue: %v", err)
			}
			got, err := ValueFromMetric(m, c.typ)
			if err != nil {
				t.Fatalf("ValueFromMetric: %v", err)
			}
			if !reflect.DeepEqual(got, c.val) {
				t.Fatalf("round trip: got %+v want %+v", got, c.val)
			}
		})
	}
}

// TestValueFromMetricScalarWireRoundTrip goes through the protobuf encoder as
// well, so the datatype packing (Int32 vs Int64 oneofs) is exercised.
func TestValueFromMetricScalarWireRoundTrip(t *testing.T) {
	cases := []struct {
		dt   spb.DataType
		in   any
		typ  *ir.Type
		want ir.Value
	}{
		{spb.DataType_Boolean, true, ir.BoolT, ir.BoolVal(true)},
		{spb.DataType_Int8, int64(-8), nil, ir.IntVal(-8)},
		{spb.DataType_Int16, int64(-300), nil, ir.IntVal(-300)},
		{spb.DataType_Int32, int64(-70000), nil, ir.IntVal(-70000)},
		{spb.DataType_Int64, int64(-5e12), nil, ir.IntVal(-5e12)},
		{spb.DataType_UInt8, int64(200), nil, ir.IntVal(200)},
		{spb.DataType_UInt16, int64(60000), nil, ir.IntVal(60000)},
		{spb.DataType_UInt32, int64(2000000000), nil, ir.IntVal(2000000000)},
		{spb.DataType_UInt64, int64(1 << 40), nil, ir.IntVal(1 << 40)},
		{spb.DataType_DateTime, int64(1712345678901), ir.TimeT, ir.TimeVal(1712345678901)},
		{spb.DataType_Float, 1.5, ir.RealT, ir.RealVal(1.5)},
		{spb.DataType_Double, -2.25, ir.RealT, ir.RealVal(-2.25)},
		{spb.DataType_String, "s", ir.StringT, ir.StringVal("s")},
		{spb.DataType_Text, "t", ir.StringT, ir.StringVal("t")},
		{spb.DataType_UUID, "u", ir.StringT, ir.StringVal("u")},
	}
	for _, c := range cases {
		t.Run(c.dt.String(), func(t *testing.T) {
			b, err := Payload{Metrics: []Metric{{Name: "m", Datatype: c.dt, Value: c.in}}}.Encode()
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			p, err := DecodePayload(b)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			got, err := ValueFromMetric(p.Metrics[0], c.typ)
			if err != nil {
				t.Fatalf("ValueFromMetric: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
		})
	}
}

// TestValueFromMetricUInt32SignExtends pins the known lossy leg: Int8..UInt32
// all ride the 32-bit IntValue oneof and decodeMetric sign-extends from 32
// bits, so an unsigned metric above 2^31 arrives negative. Pre-existing
// parent-package behaviour, pinned here so a future fix is a deliberate change.
func TestValueFromMetricUInt32SignExtends(t *testing.T) {
	b, err := Payload{Metrics: []Metric{{Name: "m", Datatype: spb.DataType_UInt32, Value: int64(4000000000)}}}.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	p, err := DecodePayload(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, err := ValueFromMetric(p.Metrics[0], ir.IntT)
	if err != nil {
		t.Fatalf("ValueFromMetric: %v", err)
	}
	if got.I != -294967296 {
		t.Fatalf("got %d, want the sign-extended -294967296", got.I)
	}
}

func TestValueFromMetricUnrepresentableDatatype(t *testing.T) {
	if _, err := ValueFromMetric(Metric{Name: "arr", Datatype: spb.DataType_DoubleArray}, nil); err == nil {
		t.Fatal("expected an error for an array datatype")
	}
	if _, err := ValueFromMetric(Metric{Name: "b", Datatype: spb.DataType_Boolean, Value: "no"}, nil); err == nil {
		t.Fatal("expected a type error")
	}
}

// TestValueFromMetricNullKeepsPrevious — a null metric carries no value.
func TestValueFromMetricNullKeepsPrevious(t *testing.T) {
	prev := ir.RealVal(7)
	got, unknown, err := ValueFromMetricInto(Metric{Name: "m", Datatype: spb.DataType_Double, IsNull: true}, prev)
	if err != nil || len(unknown) != 0 {
		t.Fatalf("err=%v unknown=%v", err, unknown)
	}
	if !reflect.DeepEqual(got, prev) {
		t.Fatalf("got %+v want %+v", got, prev)
	}
}

// TestValueFromMetricTemplateRoundTrip round-trips a nested struct out through
// MetricFromValue and back.
func TestValueFromMetricTemplateRoundTrip(t *testing.T) {
	motor := motorDef()
	pump := pumpDef(motor)
	pumpT := &ir.Type{Kind: ir.TypeStruct, Struct: pump}

	want := ir.Zero(pumpT)
	want.Fld[0] = ir.IntVal(120)
	want.Fld[1].Fld[0] = ir.BoolVal(true)
	want.Fld[1].Fld[1] = ir.RealVal(58.25)
	want.Fld[1].Fld[2] = ir.StringVal("M1")

	m, err := MetricFromValue("W6_Pump1", want, "Pump")
	if err != nil {
		t.Fatalf("MetricFromValue: %v", err)
	}
	if m.Datatype != spb.DataType_Template {
		t.Fatalf("datatype = %v", m.Datatype)
	}
	b, err := Payload{Metrics: []Metric{m}}.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	p, err := DecodePayload(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, err := ValueFromMetric(p.Metrics[0], pumpT)
	if err != nil {
		t.Fatalf("ValueFromMetric: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}
}

// TestValueFromMetricTemplateAbsentMembersAreZero — with no previous value,
// members the template omits sit at their field type's zero.
func TestValueFromMetricTemplateAbsentMembersAreZero(t *testing.T) {
	motor := motorDef()
	mt := &ir.Type{Kind: ir.TypeStruct, Struct: motor}
	m := Metric{Name: "M", Datatype: spb.DataType_Template, Value: &Template{
		TemplateRef: "Motor",
		Metrics:     []Metric{{Name: "Speed", Datatype: spb.DataType_Double, Value: 12.5}},
	}}
	got, err := ValueFromMetric(m, mt)
	if err != nil {
		t.Fatalf("ValueFromMetric: %v", err)
	}
	want := ir.Zero(mt)
	want.Fld[1] = ir.RealVal(12.5)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

// TestValueFromMetricIntoPartialUpdate — a partial template update keeps the
// members it does not mention and never mutates the previous value.
func TestValueFromMetricIntoPartialUpdate(t *testing.T) {
	motor := motorDef()
	pump := pumpDef(motor)
	pumpT := &ir.Type{Kind: ir.TypeStruct, Struct: pump}

	prev := ir.Zero(pumpT)
	prev.Fld[0] = ir.IntVal(120)
	prev.Fld[1].Fld[0] = ir.BoolVal(true)
	prev.Fld[1].Fld[1] = ir.RealVal(58.25)
	prev.Fld[1].Fld[2] = ir.StringVal("M1")

	upd := Metric{Name: "W6_Pump1", Datatype: spb.DataType_Template, Value: &Template{
		TemplateRef: "Pump",
		Metrics: []Metric{{Name: "Drive", Datatype: spb.DataType_Template, Value: &Template{
			TemplateRef: "Motor",
			Metrics:     []Metric{{Name: "Speed", Datatype: spb.DataType_Double, Value: 60.0}},
		}}},
	}}

	got, unknown, err := ValueFromMetricInto(upd, prev)
	if err != nil {
		t.Fatalf("ValueFromMetricInto: %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown = %v, want none", unknown)
	}
	if got.Fld[0].I != 120 {
		t.Errorf("Hours = %d, want 120 (untouched)", got.Fld[0].I)
	}
	if !got.Fld[1].Fld[0].B {
		t.Error("Drive.Run = false, want true (untouched)")
	}
	if got.Fld[1].Fld[1].F != 60 {
		t.Errorf("Drive.Speed = %v, want 60", got.Fld[1].Fld[1].F)
	}
	if got.Fld[1].Fld[2].S != "M1" {
		t.Errorf("Drive.Label = %q, want M1 (untouched)", got.Fld[1].Fld[2].S)
	}
	// prev must be untouched.
	if prev.Fld[1].Fld[1].F != 58.25 {
		t.Errorf("prev mutated: Drive.Speed = %v", prev.Fld[1].Fld[1].F)
	}
}

// TestValueFromMetricIntoUnknownMembers — unknown members are reported with
// dotted paths, not fatal, and the known members still apply.
func TestValueFromMetricIntoUnknownMembers(t *testing.T) {
	motor := motorDef()
	pump := pumpDef(motor)
	pumpT := &ir.Type{Kind: ir.TypeStruct, Struct: pump}

	upd := Metric{Name: "W6_Pump1", Datatype: spb.DataType_Template, Value: &Template{
		TemplateRef: "Pump",
		Metrics: []Metric{
			{Name: "Hours", Datatype: spb.DataType_Int64, Value: int64(9)},
			{Name: "Bogus", Datatype: spb.DataType_Double, Value: 1.0},
			{Name: "Drive", Datatype: spb.DataType_Template, Value: &Template{
				TemplateRef: "Motor",
				Metrics: []Metric{
					{Name: "Run", Datatype: spb.DataType_Boolean, Value: true},
					{Name: "Torque", Datatype: spb.DataType_Double, Value: 2.0},
				},
			}},
		},
	}}

	got, unknown, err := ValueFromMetricInto(upd, ir.Zero(pumpT))
	if err != nil {
		t.Fatalf("ValueFromMetricInto: %v", err)
	}
	want := []string{"Bogus", "Drive.Torque"}
	if !reflect.DeepEqual(unknown, want) {
		t.Fatalf("unknown = %v, want %v", unknown, want)
	}
	if got.Fld[0].I != 9 || !got.Fld[1].Fld[0].B {
		t.Fatalf("known members did not apply: %+v", got)
	}
}

// TestValueFromMetricTemplateNeedsStructTarget — a Template with no struct
// type to bind to is an error, not a silent drop.
func TestValueFromMetricTemplateNeedsStructTarget(t *testing.T) {
	m := Metric{Name: "M", Datatype: spb.DataType_Template, Value: &Template{}}
	if _, err := ValueFromMetric(m, ir.RealT); err == nil {
		t.Fatal("expected an error binding a Template to REAL")
	}
	if _, err := ValueFromMetric(m, &ir.Type{Kind: ir.TypeStruct}); err == nil {
		t.Fatal("expected an error for a struct type with no definition")
	}
	bad := Metric{Name: "M", Datatype: spb.DataType_Template, Value: "not a template"}
	if _, err := ValueFromMetric(bad, &ir.Type{Kind: ir.TypeStruct, Struct: motorDef()}); err == nil {
		t.Fatal("expected an error for a non-Template value")
	}
}

// ── StructDefsFromTemplates ──────────────────────────────────────────────

func defMetric(name string, members ...Metric) Metric {
	return Metric{Name: name, Datatype: spb.DataType_Template,
		Value: &Template{IsDefinition: true, Metrics: members}}
}

func member(name string, dt spb.DataType) Metric {
	return Metric{Name: name, Datatype: dt, IsNull: true}
}

func refMember(name, ref string) Metric {
	return Metric{Name: name, Datatype: spb.DataType_Template, Value: &Template{TemplateRef: ref}}
}

func TestStructDefsFromTemplates(t *testing.T) {
	defs, err := StructDefsFromTemplates([]Metric{
		// A plain data metric in the same NBIRTH is ignored.
		{Name: "Well/Level", Datatype: spb.DataType_Double, Value: 1.0},
		// So is an instance metric (IsDefinition false).
		{Name: "Pump1", Datatype: spb.DataType_Template, Value: &Template{TemplateRef: "Motor"}},
		defMetric("Motor",
			member("Run", spb.DataType_Boolean),
			member("Speed", spb.DataType_Double),
			member("Hours", spb.DataType_Int32),
			member("Label", spb.DataType_String),
			member("Born", spb.DataType_DateTime),
		),
		defMetric("Skid",
			member("Tag", spb.DataType_Text),
			refMember("Drive", "Motor"),
		),
	})
	if err != nil {
		t.Fatalf("StructDefsFromTemplates: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("defs = %v, want Motor + Skid", keys(defs))
	}
	motor := defs["Motor"]
	if motor == nil {
		t.Fatal("no Motor def")
	}
	wantKinds := []ir.TypeKind{ir.TypeBool, ir.TypeReal, ir.TypeInt, ir.TypeString, ir.TypeInt}
	if len(motor.Fields) != len(wantKinds) {
		t.Fatalf("Motor has %d fields, want %d", len(motor.Fields), len(wantKinds))
	}
	for i, k := range wantKinds {
		if motor.Fields[i].Type.Kind != k {
			t.Errorf("Motor.%s kind = %v, want %v", motor.Fields[i].Name, motor.Fields[i].Type.Kind, k)
		}
		if motor.FieldIndex[motor.Fields[i].Name] != i {
			t.Errorf("FieldIndex[%s] = %d, want %d", motor.Fields[i].Name, motor.FieldIndex[motor.Fields[i].Name], i)
		}
	}
	skid := defs["Skid"]
	if skid == nil || len(skid.Fields) != 2 {
		t.Fatalf("Skid = %+v", skid)
	}
	if skid.Fields[1].Type.Kind != ir.TypeStruct || skid.Fields[1].Type.Struct != motor {
		t.Fatalf("Skid.Drive did not resolve to the shared Motor def: %+v", skid.Fields[1].Type)
	}

	// The defs are usable as decode targets.
	v, unknown, err := ValueFromMetricInto(Metric{Name: "S", Datatype: spb.DataType_Template, Value: &Template{
		TemplateRef: "Skid",
		Metrics: []Metric{refInstance("Drive", "Motor",
			Metric{Name: "Speed", Datatype: spb.DataType_Double, Value: 30.0})},
	}}, ir.Zero(&ir.Type{Kind: ir.TypeStruct, Struct: skid}))
	if err != nil || len(unknown) != 0 {
		t.Fatalf("err=%v unknown=%v", err, unknown)
	}
	if v.Fld[1].Fld[1].F != 30 {
		t.Fatalf("Skid.Drive.Speed = %v, want 30", v.Fld[1].Fld[1].F)
	}
}

func refInstance(name, ref string, members ...Metric) Metric {
	return Metric{Name: name, Datatype: spb.DataType_Template,
		Value: &Template{TemplateRef: ref, Metrics: members}}
}

func TestStructDefsFromTemplatesInlineNested(t *testing.T) {
	defs, err := StructDefsFromTemplates([]Metric{
		defMetric("Skid",
			Metric{Name: "Drive", Datatype: spb.DataType_Template, Value: &Template{
				IsDefinition: true,
				Metrics:      []Metric{member("Run", spb.DataType_Boolean)},
			}},
		),
	})
	if err != nil {
		t.Fatalf("StructDefsFromTemplates: %v", err)
	}
	sd := defs["Skid.Drive"]
	if sd == nil || len(sd.Fields) != 1 || sd.Fields[0].Name != "Run" {
		t.Fatalf("inline nested def = %+v (all: %v)", sd, keys(defs))
	}
	if defs["Skid"].Fields[0].Type.Struct != sd {
		t.Fatal("Skid.Drive does not point at the inline def")
	}
}

func TestStructDefsFromTemplatesErrors(t *testing.T) {
	cases := []struct {
		name string
		in   []Metric
	}{
		{"cycle", []Metric{
			defMetric("A", refMember("b", "B")),
			defMetric("B", refMember("a", "A")),
		}},
		{"self cycle", []Metric{defMetric("A", refMember("a", "A"))}},
		{"unknown ref", []Metric{defMetric("A", refMember("b", "B"))}},
		{"template member with no ref", []Metric{
			defMetric("A", member("b", spb.DataType_Template)),
		}},
		{"unrepresentable member", []Metric{
			defMetric("A", member("b", spb.DataType_DoubleArray)),
		}},
		{"unnamed member", []Metric{defMetric("A", member("", spb.DataType_Boolean))}},
		{"duplicate member", []Metric{
			defMetric("A", member("x", spb.DataType_Boolean), member("x", spb.DataType_Double)),
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if defs, err := StructDefsFromTemplates(c.in); err == nil {
				t.Fatalf("expected an error, got %v", keys(defs))
			}
		})
	}
}

func keys(m map[string]*ir.StructDef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
