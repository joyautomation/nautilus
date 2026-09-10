package sparkplug

import (
	"fmt"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// Sparkplug → ir.Value: the decode-side twin of MetricFromValue (payload.go).
// The edge node publishes tags outward; a host application consumes metrics
// inward, and needs exactly the inverse mapping:
//
//	Boolean                        → ir.TypeBool
//	Int8..Int64, UInt8..UInt64,
//	DateTime                       → ir.TypeInt   (nautilus's IR collapses all
//	                                 integer widths to int64; a UInt64 above
//	                                 2^63 wraps — the edge side has the same
//	                                 limitation)
//	Float, Double                  → ir.TypeReal
//	String, Text, UUID             → ir.TypeString
//	Template                       → ir.TypeStruct, members matched by name
//	                                 against the target StructDef
//
// Arrays, DataSet, Bytes, File and PropertySet are not represented in the IR
// and are rejected.

// ValueFromMetric converts one decoded metric into an ir.Value shaped by t.
//
// t may be nil for scalars, in which case the metric's Sparkplug datatype
// alone picks the ir kind. When t is non-nil the natural value is coerced to
// t's kind (ir.CoerceValue), so a DateTime bound to a TIME tag lands as
// ir.TypeTime.
//
// For a Template metric t must be a struct type (t.Kind == ir.TypeStruct with
// a non-nil Struct): members are matched by name against StructDef.FieldIndex,
// members absent from the template are left at the zero value of their field
// type, and members the definition does not know are ignored. Callers that
// need partial-template merge semantics ("keep the previous value for absent
// members") or want to see the unknown members should use ValueFromMetricInto.
func ValueFromMetric(m Metric, t *ir.Type) (ir.Value, error) {
	if t != nil && t.Kind == ir.TypeStruct && t.Struct == nil {
		return ir.Value{}, fmt.Errorf("sparkplug: metric %q: struct type with no definition", m.Name)
	}
	v, _, err := ValueFromMetricInto(m, ir.Zero(t))
	return v, err
}

// ValueFromMetricInto merges one decoded metric into a previous value and
// returns the result, the names of template members that had no matching
// field (dotted paths for nested templates, in template order), and an error.
//
// prev carries the target shape: its Kind selects the coercion target for
// scalars, and for a Template its Struct is the definition members are matched
// against. Fields the template does not mention keep their value from prev —
// partial template updates are legal in Sparkplug, so a DDATA carrying one
// member must not zero the rest. prev is never mutated; struct field slices
// are copied on write.
//
// A null metric (IsNull) is "no value" and leaves prev unchanged.
//
// Unknown members are reported, not fatal: the caller decides the policy
// (log once and continue, count, or fail in strict mode).
func ValueFromMetricInto(m Metric, prev ir.Value) (ir.Value, []string, error) {
	if m.IsNull {
		return prev, nil, nil
	}
	if m.Datatype == spb.DataType_Template {
		return structFromTemplate(m, prev)
	}
	v, err := scalarFromMetric(m)
	if err != nil {
		return prev, nil, err
	}
	if prev.Kind != ir.TypeVoid {
		v = ir.CoerceValue(v, &ir.Type{Kind: prev.Kind})
	}
	return v, nil, nil
}

// scalarFromMetric maps a non-template metric to its natural ir.Value.
func scalarFromMetric(m Metric) (ir.Value, error) {
	switch m.Datatype {
	case spb.DataType_Boolean:
		b, ok := m.Value.(bool)
		if !ok {
			return ir.Value{}, fmt.Errorf("sparkplug: metric %q: %w", m.Name, typeErr("Boolean", m.Value))
		}
		return ir.BoolVal(b), nil
	case spb.DataType_Int8, spb.DataType_Int16, spb.DataType_Int32, spb.DataType_Int64,
		spb.DataType_UInt8, spb.DataType_UInt16, spb.DataType_UInt32, spb.DataType_UInt64,
		spb.DataType_DateTime:
		n, ok := toInt64(m.Value)
		if !ok {
			return ir.Value{}, fmt.Errorf("sparkplug: metric %q: %w", m.Name, typeErr("integer", m.Value))
		}
		return ir.IntVal(n), nil
	case spb.DataType_Float, spb.DataType_Double:
		f, ok := toFloat64(m.Value)
		if !ok {
			return ir.Value{}, fmt.Errorf("sparkplug: metric %q: %w", m.Name, typeErr("Double", m.Value))
		}
		return ir.RealVal(f), nil
	case spb.DataType_String, spb.DataType_Text, spb.DataType_UUID:
		s, ok := m.Value.(string)
		if !ok {
			return ir.Value{}, fmt.Errorf("sparkplug: metric %q: %w", m.Name, typeErr("String", m.Value))
		}
		return ir.StringVal(s), nil
	}
	return ir.Value{}, fmt.Errorf("sparkplug: metric %q has unrepresentable datatype %v", m.Name, m.Datatype)
}

// structFromTemplate merges a Template metric into prev, which must be a
// struct value carrying the StructDef to match member names against.
func structFromTemplate(m Metric, prev ir.Value) (ir.Value, []string, error) {
	tmpl, ok := m.Value.(*Template)
	if !ok || tmpl == nil {
		return prev, nil, fmt.Errorf("sparkplug: metric %q: %w", m.Name, typeErr("*Template", m.Value))
	}
	if prev.Kind != ir.TypeStruct || prev.Struct == nil {
		return prev, nil, fmt.Errorf("sparkplug: metric %q is a Template but the target is not a struct type", m.Name)
	}
	sd := prev.Struct
	out := ir.Value{Kind: ir.TypeStruct, Struct: sd, Fld: make([]ir.Value, len(sd.Fields))}
	copy(out.Fld, prev.Fld)
	// A prev built by hand may be short; pad with zeros so every field exists.
	for i := len(prev.Fld); i < len(sd.Fields); i++ {
		out.Fld[i] = ir.Zero(sd.Fields[i].Type)
	}

	var unknown []string
	for _, mm := range tmpl.Metrics {
		i, ok := sd.FieldIndex[mm.Name]
		if !ok || i >= len(out.Fld) {
			unknown = append(unknown, mm.Name)
			continue
		}
		fv, sub, err := ValueFromMetricInto(mm, out.Fld[i])
		if err != nil {
			return prev, unknown, fmt.Errorf("sparkplug: template %q member %q: %w", m.Name, mm.Name, err)
		}
		for _, u := range sub {
			unknown = append(unknown, mm.Name+"."+u)
		}
		out.Fld[i] = fv
	}
	return out, unknown, nil
}

// ── Template definitions → ir.StructDef ──────────────────────────────────

// StructDefsFromTemplates builds an ir.StructDef for every Template
// *definition* metric in defs — the IsDefinition templates an NBIRTH carries
// so a host can interpret the instance metrics that reference them (the
// inverse of templateDefs in templates.go).
//
// Metrics that are not definition templates are skipped, so a whole NBIRTH
// metric slice can be handed in. The definition's metric Name is the type
// name. A member whose datatype is Template is resolved through its
// TemplateRef; a member carrying an inline IsDefinition template instead is
// admitted under the synthetic name "<parent>.<member>". Recursion is
// memoized and cycle-detecting.
func StructDefsFromTemplates(defs []Metric) (map[string]*ir.StructDef, error) {
	index := make(map[string]*Template, len(defs))
	var order []string
	for _, m := range defs {
		t, ok := m.Value.(*Template)
		if !ok || t == nil || !t.IsDefinition || m.Name == "" {
			continue
		}
		if _, dup := index[m.Name]; !dup {
			order = append(order, m.Name)
		}
		index[m.Name] = t
	}

	out := make(map[string]*ir.StructDef, len(order))
	var build func(name string, t *Template, stack []string) (*ir.StructDef, error)

	memberType := func(parent string, mm Metric, stack []string) (*ir.Type, error) {
		switch mm.Datatype {
		case spb.DataType_Boolean:
			return ir.BoolT, nil
		case spb.DataType_Int8, spb.DataType_Int16, spb.DataType_Int32, spb.DataType_Int64,
			spb.DataType_UInt8, spb.DataType_UInt16, spb.DataType_UInt32, spb.DataType_UInt64,
			spb.DataType_DateTime:
			return ir.IntT, nil
		case spb.DataType_Float, spb.DataType_Double:
			return ir.RealT, nil
		case spb.DataType_String, spb.DataType_Text, spb.DataType_UUID:
			return ir.StringT, nil
		case spb.DataType_Template:
			nested, _ := mm.Value.(*Template)
			switch {
			case nested != nil && nested.TemplateRef != "":
				sub, ok := index[nested.TemplateRef]
				if !ok {
					return nil, fmt.Errorf("sparkplug: template %q member %q references unknown template %q",
						parent, mm.Name, nested.TemplateRef)
				}
				sd, err := build(nested.TemplateRef, sub, stack)
				if err != nil {
					return nil, err
				}
				return &ir.Type{Kind: ir.TypeStruct, Struct: sd}, nil
			case nested != nil && nested.IsDefinition:
				name := parent + "." + mm.Name
				sd, err := build(name, nested, stack)
				if err != nil {
					return nil, err
				}
				return &ir.Type{Kind: ir.TypeStruct, Struct: sd}, nil
			}
			return nil, fmt.Errorf("sparkplug: template %q member %q is a Template with no templateRef", parent, mm.Name)
		}
		return nil, fmt.Errorf("sparkplug: template %q member %q has unrepresentable datatype %v",
			parent, mm.Name, mm.Datatype)
	}

	build = func(name string, t *Template, stack []string) (*ir.StructDef, error) {
		if sd, ok := out[name]; ok {
			return sd, nil
		}
		for _, s := range stack {
			if s == name {
				return nil, fmt.Errorf("sparkplug: template cycle through %q", name)
			}
		}
		sd := &ir.StructDef{Name: name, FieldIndex: make(map[string]int, len(t.Metrics))}
		inner := append(stack, name)
		for _, mm := range t.Metrics {
			if mm.Name == "" {
				return nil, fmt.Errorf("sparkplug: template %q has an unnamed member", name)
			}
			if _, dup := sd.FieldIndex[mm.Name]; dup {
				return nil, fmt.Errorf("sparkplug: template %q has duplicate member %q", name, mm.Name)
			}
			ft, err := memberType(name, mm, inner)
			if err != nil {
				return nil, err
			}
			sd.FieldIndex[mm.Name] = len(sd.Fields)
			sd.Fields = append(sd.Fields, ir.StructField{Name: mm.Name, Type: ft})
		}
		out[name] = sd
		return sd, nil
	}

	for _, name := range order {
		if _, err := build(name, index[name], nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}
