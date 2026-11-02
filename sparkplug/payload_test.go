package sparkplug

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

func TestPayloadRoundTrip(t *testing.T) {
	p := Payload{Timestamp: 1000, Seq: 5, Metrics: []Metric{
		{Name: "Enable", Datatype: spb.DataType_Boolean, Value: true},
		{Name: "Count", Datatype: spb.DataType_Int64, Value: int64(-7)},
		{Name: "Speed", Datatype: spb.DataType_Double, Value: 42.5},
		{Name: "Label", Datatype: spb.DataType_String, Value: "run"},
	}}
	b, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePayload(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Seq != 5 || len(got.Metrics) != 4 {
		t.Fatalf("decoded %+v", got)
	}
	if got.Metrics[0].Value != true || got.Metrics[1].Value != int64(-7) ||
		got.Metrics[2].Value != 42.5 || got.Metrics[3].Value != "run" {
		t.Errorf("values wrong: %+v", got.Metrics)
	}
}

func TestOmitSeqForDeath(t *testing.T) {
	// NDEATH: only bdSeq, no seq field on the wire.
	b, _ := Payload{OmitSeq: true, Metrics: []Metric{
		{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: int64(3)},
	}}.Encode()
	var raw spb.Payload
	if err := unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Seq != nil {
		t.Errorf("NDEATH must omit seq, got %d", raw.GetSeq())
	}
}

func TestFaithfulTypingFromIR(t *testing.T) {
	cases := []struct {
		v    ir.Value
		want spb.DataType
	}{
		{ir.BoolVal(true), spb.DataType_Boolean},
		{ir.IntVal(3), spb.DataType_Int64},
		{ir.RealVal(1.5), spb.DataType_Double},
		{ir.StringVal("x"), spb.DataType_String},
	}
	for _, c := range cases {
		m, err := MetricFromValue("t", c.v, "")
		if err != nil || m.Datatype != c.want {
			t.Errorf("%v -> %v (want %v), err %v", c.v.Kind, m.Datatype, c.want, err)
		}
	}
}

func TestStructToTemplate(t *testing.T) {
	sd := &ir.StructDef{Name: "Header", Fields: []ir.StructField{
		{Name: "Displacement", Type: ir.RealT}, {Name: "Valid", Type: ir.BoolT},
	}}
	v := ir.Value{Kind: ir.TypeStruct, Struct: sd, Fld: []ir.Value{ir.RealVal(3.5), ir.BoolVal(true)}}
	m, err := MetricFromValue("TRS", v, "Header")
	if err != nil {
		t.Fatal(err)
	}
	if m.Datatype != spb.DataType_Template {
		t.Fatalf("want Template, got %v", m.Datatype)
	}
	tmpl := m.Value.(*Template)
	if tmpl.TemplateRef != "Header" || len(tmpl.Metrics) != 2 ||
		tmpl.Metrics[0].Name != "Displacement" || tmpl.Metrics[0].Value != 3.5 ||
		tmpl.Metrics[1].Name != "Valid" || tmpl.Metrics[1].Value != true {
		t.Errorf("template wrong: %+v", tmpl)
	}
	// Round-trips through the wire.
	b, err := Payload{Metrics: []Metric{m}}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePayload(b); err != nil {
		t.Fatal(err)
	}
}

// The 32-bit int_value field means what its datatype says: a UInt32 at or
// above 2^31 decodes as the unsigned value, an Int32 as a negative one, in
// a metric and in a property alike (#501 review found the UInt32 one).
func TestIntValueDecodesByDatatype(t *testing.T) {
	p := Payload{Timestamp: 1, Metrics: []Metric{
		{Name: "U32", Datatype: spb.DataType_UInt32, Value: int64(3000000000)},
		{Name: "U16", Datatype: spb.DataType_UInt16, Value: int64(65535)},
		{Name: "I32", Datatype: spb.DataType_Int32, Value: int64(-5)},
		{Name: "I8", Datatype: spb.DataType_Int8, Value: int64(-128)},
	}}
	b, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePayload(b)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int64{3000000000, 65535, -5, -128} {
		if got.Metrics[i].Value != want {
			t.Errorf("%s = %v, want %d", got.Metrics[i].Name, got.Metrics[i].Value, want)
		}
	}
	u32, i32 := uint32(spb.DataType_UInt32), uint32(spb.DataType_Int32)
	props := decodeProperties(&spb.Payload_PropertySet{
		Keys: []string{"hi", "lo"},
		Values: []*spb.Payload_PropertyValue{
			{Type: &u32, Value: &spb.Payload_PropertyValue_IntValue{IntValue: 4000000000}},
			{Type: &i32, Value: &spb.Payload_PropertyValue_IntValue{IntValue: uint32(0xFFFFFFFB)}},
		},
	})
	if len(props) != 2 || props[0].Value != int64(4000000000) || props[1].Value != int64(-5) {
		t.Errorf("properties = %+v, want hi 4000000000 (UInt32), lo -5 (Int32)", props)
	}
}
