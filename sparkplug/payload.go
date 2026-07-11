// Package sparkplug is an MQTT Sparkplug B edge node for nautilus: it
// publishes the runtime's tag store to a Sparkplug host (Ignition, a broker
// with a SCADA consumer) and accepts commands back. The runtime is the edge
// node; each io.Driver is a Sparkplug device whose birth/death tracks its
// connection health. MQTT and protobuf live only in this package — the
// runtime core stays stdlib-only.
package sparkplug

import (
	"fmt"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug/spb"
	"google.golang.org/protobuf/proto"
)

// Metric is a friendly Sparkplug metric: a name (birth) or alias (data), a
// datatype, and a Go value. It wraps the generated spb types so the node code
// never touches protobuf oneofs directly.
type Metric struct {
	Name      string
	Alias     uint64
	Timestamp uint64
	Datatype  spb.DataType
	IsNull    bool
	Value     any // bool | int64 | float64 | string | *Template
}

// Template is a Sparkplug template value — a UDT definition (IsDefinition) or
// an instance (TemplateRef set to the definition name).
type Template struct {
	IsDefinition bool
	TemplateRef  string
	Version      string
	Metrics      []Metric
}

// Payload is a friendly NBIRTH/NDATA/etc. payload. OmitSeq drops the seq field
// entirely — required for NDEATH, which must carry only bdSeq and no seq.
type Payload struct {
	Timestamp uint64
	Seq       uint64
	OmitSeq   bool
	Metrics   []Metric
}

// Encode marshals a Payload to Sparkplug B protobuf wire bytes.
func (p Payload) Encode() ([]byte, error) {
	pb := &spb.Payload{Timestamp: proto.Uint64(p.Timestamp)}
	if !p.OmitSeq {
		pb.Seq = proto.Uint64(p.Seq)
	}
	for _, m := range p.Metrics {
		em, err := encodeMetric(m)
		if err != nil {
			return nil, err
		}
		pb.Metrics = append(pb.Metrics, em)
	}
	return proto.Marshal(pb)
}

// DecodePayload parses Sparkplug B wire bytes into a friendly Payload.
func DecodePayload(b []byte) (Payload, error) {
	var pb spb.Payload
	if err := proto.Unmarshal(b, &pb); err != nil {
		return Payload{}, err
	}
	out := Payload{Timestamp: pb.GetTimestamp(), Seq: pb.GetSeq()}
	for _, m := range pb.GetMetrics() {
		out.Metrics = append(out.Metrics, decodeMetric(m))
	}
	return out, nil
}

func encodeMetric(m Metric) (*spb.Payload_Metric, error) {
	em := &spb.Payload_Metric{Datatype: proto.Uint32(uint32(m.Datatype))}
	if m.Name != "" {
		em.Name = proto.String(m.Name)
	}
	if m.Alias != 0 {
		em.Alias = proto.Uint64(m.Alias)
	}
	if m.Timestamp != 0 {
		em.Timestamp = proto.Uint64(m.Timestamp)
	}
	if m.IsNull {
		em.IsNull = proto.Bool(true)
		return em, nil
	}
	if err := setMetricValue(em, m.Datatype, m.Value); err != nil {
		return nil, fmt.Errorf("metric %q: %w", m.Name, err)
	}
	return em, nil
}

// setMetricValue writes the value into the correct protobuf oneof for the
// datatype. Integer widths all ride IntValue/LongValue per the Sparkplug
// spec's field packing.
func setMetricValue(em *spb.Payload_Metric, dt spb.DataType, v any) error {
	switch dt {
	case spb.DataType_Boolean:
		b, ok := v.(bool)
		if !ok {
			return typeErr("Boolean", v)
		}
		em.Value = &spb.Payload_Metric_BooleanValue{BooleanValue: b}
	case spb.DataType_Int8, spb.DataType_Int16, spb.DataType_Int32,
		spb.DataType_UInt8, spb.DataType_UInt16, spb.DataType_UInt32:
		n, ok := toInt64(v)
		if !ok {
			return typeErr("integer", v)
		}
		em.Value = &spb.Payload_Metric_IntValue{IntValue: uint32(n)}
	case spb.DataType_Int64, spb.DataType_UInt64, spb.DataType_DateTime:
		n, ok := toInt64(v)
		if !ok {
			return typeErr("long", v)
		}
		em.Value = &spb.Payload_Metric_LongValue{LongValue: uint64(n)}
	case spb.DataType_Float:
		f, ok := toFloat64(v)
		if !ok {
			return typeErr("Float", v)
		}
		em.Value = &spb.Payload_Metric_FloatValue{FloatValue: float32(f)}
	case spb.DataType_Double:
		f, ok := toFloat64(v)
		if !ok {
			return typeErr("Double", v)
		}
		em.Value = &spb.Payload_Metric_DoubleValue{DoubleValue: f}
	case spb.DataType_String, spb.DataType_Text, spb.DataType_UUID:
		s, ok := v.(string)
		if !ok {
			return typeErr("String", v)
		}
		em.Value = &spb.Payload_Metric_StringValue{StringValue: s}
	case spb.DataType_Template:
		t, ok := v.(*Template)
		if !ok {
			return typeErr("Template", v)
		}
		et, err := encodeTemplate(t)
		if err != nil {
			return err
		}
		em.Value = &spb.Payload_Metric_TemplateValue{TemplateValue: et}
	default:
		return fmt.Errorf("unsupported datatype %v", dt)
	}
	return nil
}

func encodeTemplate(t *Template) (*spb.Payload_Template, error) {
	et := &spb.Payload_Template{IsDefinition: proto.Bool(t.IsDefinition)}
	if t.TemplateRef != "" {
		et.TemplateRef = proto.String(t.TemplateRef)
	}
	if t.Version != "" {
		et.Version = proto.String(t.Version)
	}
	for _, m := range t.Metrics {
		em, err := encodeMetric(m)
		if err != nil {
			return nil, err
		}
		et.Metrics = append(et.Metrics, em)
	}
	return et, nil
}

func decodeMetric(m *spb.Payload_Metric) Metric {
	out := Metric{
		Name:      m.GetName(),
		Alias:     m.GetAlias(),
		Timestamp: m.GetTimestamp(),
		Datatype:  spb.DataType(m.GetDatatype()),
		IsNull:    m.GetIsNull(),
	}
	switch v := m.GetValue().(type) {
	case *spb.Payload_Metric_BooleanValue:
		out.Value = v.BooleanValue
	case *spb.Payload_Metric_IntValue:
		out.Value = int64(int32(v.IntValue))
	case *spb.Payload_Metric_LongValue:
		out.Value = int64(v.LongValue)
	case *spb.Payload_Metric_FloatValue:
		out.Value = float64(v.FloatValue)
	case *spb.Payload_Metric_DoubleValue:
		out.Value = v.DoubleValue
	case *spb.Payload_Metric_StringValue:
		out.Value = v.StringValue
	case *spb.Payload_Metric_TemplateValue:
		out.Value = decodeTemplate(v.TemplateValue)
	}
	return out
}

func decodeTemplate(t *spb.Payload_Template) *Template {
	out := &Template{
		IsDefinition: t.GetIsDefinition(),
		TemplateRef:  t.GetTemplateRef(),
		Version:      t.GetVersion(),
	}
	for _, m := range t.GetMetrics() {
		out.Metrics = append(out.Metrics, decodeMetric(m))
	}
	return out
}

// ── ir.Value → Sparkplug ─────────────────────────────────────────────────

// MetricFromValue builds a metric for a tag from its ir.Value, mapping types
// faithfully: BOOL→Boolean, integer→Int64, REAL→Double, STRING→String,
// STRUCT→Template. Nautilus's IR collapses integer widths to int64 and reals
// to float64, so Int64/Double are the honest targets. name and timestamp are
// applied by the caller as appropriate for birth vs data.
func MetricFromValue(name string, v ir.Value, tmplRef string) (Metric, error) {
	switch v.Kind {
	case ir.TypeBool:
		return Metric{Name: name, Datatype: spb.DataType_Boolean, Value: v.B}, nil
	case ir.TypeInt, ir.TypeTime:
		return Metric{Name: name, Datatype: spb.DataType_Int64, Value: v.I}, nil
	case ir.TypeReal:
		return Metric{Name: name, Datatype: spb.DataType_Double, Value: v.F}, nil
	case ir.TypeString:
		return Metric{Name: name, Datatype: spb.DataType_String, Value: v.S}, nil
	case ir.TypeStruct:
		tmpl, err := templateInstance(v, tmplRef)
		if err != nil {
			return Metric{}, err
		}
		return Metric{Name: name, Datatype: spb.DataType_Template, Value: tmpl}, nil
	default:
		return Metric{}, fmt.Errorf("sparkplug: tag %q has unpublishable kind %v", name, v.Kind)
	}
}

// templateInstance builds a Template instance metric tree from a struct value,
// recursing into nested structs. Field names come from the value's StructDef.
func templateInstance(v ir.Value, ref string) (*Template, error) {
	t := &Template{TemplateRef: ref}
	for i, fv := range v.Fld {
		name := fmt.Sprintf("m%d", i)
		if v.Struct != nil && i < len(v.Struct.Fields) {
			name = v.Struct.Fields[i].Name
		}
		nestedRef := ""
		if fv.Kind == ir.TypeStruct && fv.Struct != nil {
			nestedRef = fv.Struct.Name
		}
		m, err := MetricFromValue(name, fv, nestedRef)
		if err != nil {
			return nil, err
		}
		t.Metrics = append(t.Metrics, m)
	}
	return t, nil
}

func typeErr(want string, v any) error { return fmt.Errorf("expected %s, got %T", want, v) }

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

// unmarshal is a test seam for inspecting raw wire payloads.
func unmarshal(b []byte, p *spb.Payload) error { return proto.Unmarshal(b, p) }
