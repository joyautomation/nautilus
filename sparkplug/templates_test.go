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
	// Plt_Type members: Header (Template) + Count (Int64), no values.
	pltDef := defs[1].Value.(*Template)
	if len(pltDef.Metrics) != 2 || pltDef.Metrics[0].Datatype != spb.DataType_Template ||
		pltDef.Metrics[1].Datatype != spb.DataType_Int64 {
		t.Errorf("Plt_Type members wrong: %+v", pltDef.Metrics)
	}
	if !pltDef.Metrics[0].IsNull {
		t.Error("definition members must have no value (IsNull)")
	}
}

func names(ms []Metric) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Name
	}
	return out
}
