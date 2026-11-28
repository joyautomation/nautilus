package wiring

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Decode reads one wiring file strictly: a key this format does not know is
// an error with the file and line, never a silently dropped binding. name is
// the file's name for messages and for each declaration's Pos.
func Decode(name string, raw []byte) (*File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	f.SetPositions(name, raw)
	return &f, nil
}

// SetPositions gives each wiring and instance the file and line it began on,
// read from raw, the YAML f came from. A manifest that holds wirings inline
// calls it with the manifest's own text.
func (f *File) SetPositions(name string, raw []byte) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return
	}
	doc := &root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		seq := doc.Content[i+1]
		if seq.Kind != yaml.SequenceNode {
			continue
		}
		switch doc.Content[i].Value {
		case "wirings":
			for j := range f.Wirings {
				if j < len(seq.Content) {
					f.Wirings[j].Pos = Pos{name, seq.Content[j].Line}
				}
			}
		case "instances":
			for j := range f.Instances {
				if j < len(seq.Content) {
					f.Instances[j].Pos = Pos{name, seq.Content[j].Line}
				}
			}
		}
	}
}

// mapping returns the node's key/value pairs, or an error naming what was
// there instead.
func mapping(n *yaml.Node, what string) ([]*yaml.Node, error) {
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: %s must be a mapping", n.Line, what)
	}
	return n.Content, nil
}

// scalarAny decodes a YAML value into a Go value.
func scalarAny(n *yaml.Node) (any, error) {
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// UnmarshalYAML reads one member: its kind key, then the keys this format
// names (readback, event, unit, desc), then the driver's own, kept in Addr.
func (m *Member) UnmarshalYAML(n *yaml.Node) error {
	kv, err := mapping(n, "a member")
	if err != nil {
		return err
	}
	m.Line = n.Line
	seen := map[string]bool{}
	var kinds []string
	for i := 0; i+1 < len(kv); i += 2 {
		k, v := kv[i].Value, kv[i+1]
		if seen[k] {
			return fmt.Errorf("line %d: %q is given twice", kv[i].Line, k)
		}
		seen[k] = true
		switch k {
		case "from", "to", "both":
			m.Kind, m.Source = Kind(k), v.Value
			kinds = append(kinds, k)
		case "memory":
			m.Kind, m.MemoryMode = Memory, v.Value
			kinds = append(kinds, k)
		case "expr":
			m.Kind, m.Expr = Expr, v.Value
			kinds = append(kinds, k)
		case "readback":
			m.Readback = v.Value
		case "event":
			if err := v.Decode(&m.Event); err != nil {
				return fmt.Errorf("line %d: event: must be true or false", v.Line)
			}
		case "unit":
			m.Unit = v.Value
		case "desc":
			m.Desc = v.Value
		default:
			val, err := scalarAny(v)
			if err != nil {
				return fmt.Errorf("line %d: %s: %w", v.Line, k, err)
			}
			if m.Addr == nil {
				m.Addr = map[string]any{}
			}
			m.Addr[k] = val
		}
	}
	if len(kinds) > 1 {
		sort.Strings(kinds)
		return fmt.Errorf("line %d: a member has one kind, and this one gives %s", n.Line, strings.Join(kinds, " and "))
	}
	return nil
}

// UnmarshalYAML reads `{driver: modbus, endpoint: "{endpoint}", ...}`.
func (s *Source) UnmarshalYAML(n *yaml.Node) error {
	kv, err := mapping(n, "a source")
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(kv); i += 2 {
		k, v := kv[i].Value, kv[i+1]
		if k == "driver" {
			s.Driver = v.Value
			continue
		}
		val, err := scalarAny(v)
		if err != nil {
			return fmt.Errorf("line %d: %s: %w", v.Line, k, err)
		}
		if s.Keys == nil {
			s.Keys = map[string]any{}
		}
		if _, dup := s.Keys[k]; dup {
			return fmt.Errorf("line %d: %q is given twice", kv[i].Line, k)
		}
		s.Keys[k] = val
	}
	return nil
}

// UnmarshalYAML reads `{from: plc, member: Plc, tag: "{tag}"}`.
func (f *Fill) UnmarshalYAML(n *yaml.Node) error {
	kv, err := mapping(n, "fill")
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(kv); i += 2 {
		k, v := kv[i].Value, kv[i+1]
		switch k {
		case "from":
			f.From = v.Value
		case "member":
			f.Member = v.Value
		default:
			val, err := scalarAny(v)
			if err != nil {
				return fmt.Errorf("line %d: %s: %w", v.Line, k, err)
			}
			if f.Keys == nil {
				f.Keys = map[string]any{}
			}
			f.Keys[k] = val
		}
	}
	return nil
}

// UnmarshalYAML reads a param: a scalar default, null for "required", or a
// mapping with default and publish.
func (p *Param) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		kv := n.Content
		for i := 0; i+1 < len(kv); i += 2 {
			k, v := kv[i].Value, kv[i+1]
			switch k {
			case "default":
				val, err := scalarAny(v)
				if err != nil {
					return fmt.Errorf("line %d: default: %w", v.Line, err)
				}
				p.Default = val
			case "publish":
				var b bool
				if err := v.Decode(&b); err != nil {
					return fmt.Errorf("line %d: publish: must be true or false", v.Line)
				}
				p.Publish = &b
			default:
				return fmt.Errorf("line %d: a param takes default and publish, not %q", kv[i].Line, k)
			}
		}
		return nil
	}
	val, err := scalarAny(n)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	if _, ok := val.(map[string]any); ok {
		return fmt.Errorf("line %d: a param is a scalar, or a mapping with default and publish", n.Line)
	}
	if _, ok := val.([]any); ok {
		return fmt.Errorf("line %d: a param is a scalar, or a mapping with default and publish", n.Line)
	}
	p.Default = val
	return nil
}

// The keys this format names itself, in the custom decoders below. The
// extension's schema must offer exactly these (internal/project's
// TestSchemaWiringKeys), and TestKeyListsMatchTheDecoders holds each list to
// its decoder, so neither can drift from the other.
var (
	MemberKeys = []string{"from", "to", "both", "memory", "expr", "readback", "event", "unit", "desc"}
	SourceKeys = []string{"driver"}
	FillKeys   = []string{"from", "member"}
	ParamKeys  = []string{"default", "publish"}
	// TuningKeys are an alarm tuning's fields.
	TuningKeys = []string{"name", "priority", "on-delay", "class", "notes"}
)

var tuningKeys = TuningKeys

// UnmarshalYAML reads `false` or a mapping of alarm fields.
func (t *Tuning) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var b bool
		if err := n.Decode(&b); err != nil || b {
			return fmt.Errorf("line %d: an alarm tuning is false (turn it off) or a mapping of %s", n.Line, strings.Join(tuningKeys, ", "))
		}
		t.Off = true
		return nil
	}
	return t.fields(n, tuningKeys)
}

func (t *Tuning) fields(n *yaml.Node, allowed []string) error {
	kv, err := mapping(n, "an alarm tuning")
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(kv); i += 2 {
		k, v := kv[i].Value, kv[i+1].Value
		ok := false
		for _, a := range allowed {
			ok = ok || a == k
		}
		if !ok {
			return fmt.Errorf("line %d: an alarm tuning takes %s, not %q", kv[i].Line, strings.Join(allowed, ", "), k)
		}
		switch k {
		case "name":
			t.Name = v
		case "priority":
			t.Priority = v
		case "on-delay":
			t.OnDelay = v
		case "class":
			t.Class = v
		case "notes":
			t.Notes = v
		}
	}
	return nil
}

// UnmarshalYAML reads `offline-alarm:`: false, a mapping of priority and
// on-delay for every connection the instance owns, or a mapping of source
// name to such a tuning.
func (o *Offline) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var t Tuning
		if err := t.UnmarshalYAML(n); err != nil {
			return err
		}
		o.Off = true
		return nil
	}
	kv, err := mapping(n, "offline-alarm")
	if err != nil {
		return err
	}
	direct := true
	for i := 0; i+1 < len(kv); i += 2 {
		if k := kv[i].Value; k != "priority" && k != "on-delay" {
			direct = false
		}
	}
	if direct {
		var t Tuning
		if err := t.fields(n, []string{"priority", "on-delay"}); err != nil {
			return err
		}
		o.All = &t
		return nil
	}
	o.BySource = map[string]Tuning{}
	for i := 0; i+1 < len(kv); i += 2 {
		var t Tuning
		v := kv[i+1]
		if v.Kind == yaml.MappingNode {
			if err := t.fields(v, []string{"priority", "on-delay"}); err != nil {
				return err
			}
		} else if err := t.UnmarshalYAML(v); err != nil {
			return err
		}
		o.BySource[kv[i].Value] = t
	}
	return nil
}
