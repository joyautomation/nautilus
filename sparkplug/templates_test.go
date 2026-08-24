package sparkplug

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

func TestTemplateDefsNestedAndOrdered(t *testing.T) {
	header := &ir.StructDef{Name: "Header_Type", Fields: []ir.StructField{
		{Name: "Displacement", Type: ir.RealT}, {Name: "Valid", Type: ir.BoolT},
	}}
	plt := &ir.StructDef{Name: "Plt_Type", Fields: []ir.StructField{
		{Name: "Header", Type: &ir.Type{Kind: ir.TypeStruct, Struct: header}},
		{Name: "Count", Type: ir.IntT},
	}}
	pltVal := ir.Value{Kind: ir.TypeStruct, Struct: plt, Fld: []ir.Value{
		{Kind: ir.TypeStruct, Struct: header, Fld: []ir.Value{ir.RealVal(1), ir.BoolVal(true)}},
		ir.IntVal(3),
	}}

	n := &Node{classRBE: map[string]RBE{DefaultClass: {}}, tagOwner: map[string]string{}}
	snap := map[string]ir.Value{"TRS": pltVal, "Speed": ir.RealVal(1)}
	defs := n.templateDefs(snap, 100)

	// Nested type defined before its user, one metric each, definitions only.
	if len(defs) != 2 || defs[0].Name != "Header_Type" || defs[1].Name != "Plt_Type" {
		t.Fatalf("defs order = %v", names(defs))
	}
	for _, d := range defs {
		tmpl := d.Value.(*Template)
		if !tmpl.IsDefinition {
			t.Errorf("%s not a definition", d.Name)
		}
	}
	// Plt_Type members: Header (Template) + Count (Int64).
	pltDef := defs[1].Value.(*Template)
	if len(pltDef.Metrics) != 2 || pltDef.Metrics[0].Datatype != spb.DataType_Template ||
		pltDef.Metrics[1].Datatype != spb.DataType_Int64 {
		t.Errorf("Plt_Type members wrong: %+v", pltDef.Metrics)
	}
	// The struct-typed member (Header) is not IsNull: it carries a Template
	// value whose TemplateRef names the nested definition (Header_Type),
	// emitted above it in defs — the reference a host resolves against.
	if pltDef.Metrics[0].IsNull {
		t.Error("struct-typed definition member must not be IsNull")
	}
	headerRef, ok := pltDef.Metrics[0].Value.(*Template)
	if !ok || headerRef.TemplateRef != "Header_Type" || headerRef.IsDefinition {
		t.Errorf("Plt_Type.Header value = %+v, want TemplateRef=Header_Type, IsDefinition=false", pltDef.Metrics[0].Value)
	}
	// The scalar member (Count) is still IsNull with no value.
	if !pltDef.Metrics[1].IsNull {
		t.Error("scalar definition member must have no value (IsNull)")
	}
}

// TestDefinitionTemplateTwoLevelNestedTemplateRef round-trips a definition
// with a two-level nested struct through protobuf encode/decode and confirms
// TemplateRef survives the wire for both nesting levels.
func TestDefinitionTemplateTwoLevelNestedTemplateRef(t *testing.T) {
	inner := &ir.StructDef{Name: "Header_Type", Fields: []ir.StructField{
		{Name: "Valid", Type: ir.BoolT},
	}}
	mid := &ir.StructDef{Name: "Plt_Type", Fields: []ir.StructField{
		{Name: "Header", Type: &ir.Type{Kind: ir.TypeStruct, Struct: inner}},
	}}
	outer := &ir.StructDef{Name: "Well_Type", Fields: []ir.StructField{
		{Name: "Plt", Type: &ir.Type{Kind: ir.TypeStruct, Struct: mid}},
	}}

	def := definitionTemplate(outer)
	b, err := Payload{Metrics: []Metric{{Name: "Well_Type", Datatype: spb.DataType_Template, Value: def}}}.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	p, err := DecodePayload(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	got := p.Metrics[0].Value.(*Template)
	pltMember := got.Metrics[0]
	if pltMember.Name != "Plt" || pltMember.Datatype != spb.DataType_Template {
		t.Fatalf("Well_Type.Plt = %+v", pltMember)
	}
	pltRef, ok := pltMember.Value.(*Template)
	if !ok || pltRef.TemplateRef != "Plt_Type" {
		t.Fatalf("Well_Type.Plt value = %+v, want TemplateRef=Plt_Type", pltMember.Value)
	}

	// And the definition for Plt_Type itself (built separately, as templateDefs
	// would emit it) carries a TemplateRef to Header_Type on its own member.
	pltDef := definitionTemplate(mid)
	if len(pltDef.Metrics) != 1 {
		t.Fatalf("Plt_Type def = %+v", pltDef.Metrics)
	}
	headerRef, ok := pltDef.Metrics[0].Value.(*Template)
	if !ok || headerRef.TemplateRef != "Header_Type" {
		t.Fatalf("Plt_Type.Header value = %+v, want TemplateRef=Header_Type", pltDef.Metrics[0].Value)
	}
}

func names(ms []Metric) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Name
	}
	return out
}
