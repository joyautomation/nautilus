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
