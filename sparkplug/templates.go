package sparkplug

import (
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// UDT struct tags publish as Sparkplug Template instances (TemplateRef → type
// name). A host like Ignition needs the matching Template *definition* to
// interpret them, so NBIRTH carries one definition metric per distinct struct
// type — name = type name, value = a Template with IsDefinition=true and
// member name+datatype (no values), nested types included.

// templateDefs returns definition metrics for every distinct struct type
// reachable from the published tags in snap, deduped by type name and ordered
// dependencies-first so a nested type is defined before the type that uses it.
// Caller holds n.mu (reads rbeFor).
func (n *Node) templateDefs(snap map[string]ir.Value, ts uint64) []Metric {
	seen := map[string]bool{}
	var order []*ir.StructDef
	var walk func(sd *ir.StructDef)
	walk = func(sd *ir.StructDef) {
		if sd == nil || seen[sd.Name] {
			return
		}
		seen[sd.Name] = true
		for _, f := range sd.Fields {
			if s := structOf(f.Type); s != nil {
				walk(s) // define nested types first
			}
		}
		order = append(order, sd)
	}
	for _, name := range sortedNames(snap) {
		if _, ok := n.rbeFor(name); !ok {
			continue
		}
		if v := snap[name]; v.Kind == ir.TypeStruct {
			walk(v.Struct)
		}
	}

	defs := make([]Metric, 0, len(order))
	for _, sd := range order {
		defs = append(defs, Metric{
			Name:      sd.Name,
			Datatype:  spb.DataType_Template,
			Timestamp: ts,
			Value:     definitionTemplate(sd),
		})
	}
	return defs
}

// definitionTemplate builds a Template definition (members carry name +
// datatype, no value) for a struct type.
func definitionTemplate(sd *ir.StructDef) *Template {
	t := &Template{IsDefinition: true}
	for _, f := range sd.Fields {
		t.Metrics = append(t.Metrics, Metric{
			Name:     f.Name,
			Datatype: definitionDatatype(f.Type),
			IsNull:   true, // definition members have no value
		})
	}
	return t
}

// definitionDatatype maps a field's ir.Type to its Sparkplug datatype for a
// definition member. Arrays use their element datatype (v1 approximation);
// nested structs are Template.
func definitionDatatype(t *ir.Type) spb.DataType {
	if t == nil {
		return spb.DataType_String
	}
	switch t.Kind {
	case ir.TypeBool:
		return spb.DataType_Boolean
	case ir.TypeInt, ir.TypeTime:
		return spb.DataType_Int64
	case ir.TypeReal:
		return spb.DataType_Double
	case ir.TypeString:
		return spb.DataType_String
	case ir.TypeStruct:
		return spb.DataType_Template
	case ir.TypeArray:
		return definitionDatatype(t.Elem)
	}
	return spb.DataType_String
}

// structOf returns the StructDef a type resolves to (through an array element),
// or nil if it isn't a struct.
func structOf(t *ir.Type) *ir.StructDef {
	if t == nil {
		return nil
	}
	switch t.Kind {
	case ir.TypeStruct:
		return t.Struct
	case ir.TypeArray:
		return structOf(t.Elem)
	}
	return nil
}
