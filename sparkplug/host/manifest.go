// manifest.go is the manifest's own behaviour (B1): loading it off disk,
// validating it as a whole, and lowering its types: block to the ir.StructDefs
// the runtime binds VAR_EXTERNAL declarations against.
//
// The shapes themselves live in types.go, shared with the state machine
// (state.go) and the transport (mqtt.go). They carry NO yaml struct tags, so
// the on-disk keys are the lowercased Go field names ("edgenode", "onlinetag",
// "arraylen") — eip.Manifest's convention exactly, decoded with
// KnownFields(true) so a typo is an error rather than a silently dropped
// binding.

package host

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug/spb"
	"gopkg.in/yaml.v3"
)

// ── Loading ──────────────────────────────────────────────────────────────

// LoadManifest decodes a sparkplug_manifest.yaml. It does not validate — call
// Validate (New does, through buildIndexes) for the cross-references, since a
// generator may want to load, edit and re-render a manifest that is not yet
// consistent.
func LoadManifest(r io.Reader) (Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true) // a typo is an error, not a silently dropped binding
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return Manifest{}, fmt.Errorf("host: manifest is empty")
		}
		return Manifest{}, fmt.Errorf("host: manifest: %w", err)
	}
	return m, nil
}

// ParseManifest decodes a manifest from bytes — LoadManifest for callers that
// already hold the file (the project loader reads it through an fs.FS).
func ParseManifest(data []byte) (Manifest, error) {
	return LoadManifest(bytes.NewReader(data))
}

// ── Datatypes ────────────────────────────────────────────────────────────

// datatypeOf resolves a Sparkplug datatype *name* ("Boolean", "Int32",
// "Double", ...) to its spb.DataType. The name table is the generated
// protobuf enum's own, so the manifest cannot drift from the wire format.
// "Unknown" is not a usable name and reports false, which is what makes
// datatypeOf a clean "is this a datatype or a types: reference?" test.
func datatypeOf(name string) (spb.DataType, bool) {
	v, ok := spb.DataType_value[name]
	if !ok || spb.DataType(v) == spb.DataType_Unknown {
		return spb.DataType_Unknown, false
	}
	return spb.DataType(v), true
}

// scalarType maps a Sparkplug datatype name to its ir type, per
// docs/design/sparkplug-host.md §5 — the same mapping sparkplug.ValueFromMetric
// applies to a metric on the wire, so a manifest type and the value that
// arrives for it agree by construction.
//
// nautilus's IR collapses every integer width to int64, so a UInt64 above 2^63
// wraps; the edge side has the same limitation.
func scalarType(name string) (*ir.Type, bool) {
	switch name {
	case "Boolean":
		return ir.BoolT, true
	case "Int8", "Int16", "Int32", "Int64",
		"UInt8", "UInt16", "UInt32", "UInt64", "DateTime":
		return ir.IntT, true
	case "Float", "Double":
		return ir.RealT, true
	case "String", "Text", "UUID":
		return ir.StringT, true
	}
	return nil, false
}

// ── Indexes ──────────────────────────────────────────────────────────────

// typeIndex resolves manifest types by name.
func (m Manifest) typeIndex() map[string]TypeDef {
	idx := make(map[string]TypeDef, len(m.Types))
	for _, t := range m.Types {
		idx[t.Name] = t
	}
	return idx
}

// nodeIndex resolves manifest nodes by edge_node_id.
func (m Manifest) nodeIndex() map[string]Node {
	idx := make(map[string]Node, len(m.Nodes))
	for _, n := range m.Nodes {
		idx[n.EdgeNode] = n
	}
	return idx
}

// Bindings returns the metric bindings in a deterministic order — by nautilus
// tag name, then by their Sparkplug address. Generators and the driver's own
// indexes both walk this, so a regenerated tag file diffs against the last one
// instead of reshuffling.
func (m Manifest) Bindings() []Binding {
	out := append([]Binding(nil), m.Tags...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		if a.Device != b.Device {
			return a.Device < b.Device
		}
		return a.Metric < b.Metric
	})
	return out
}

// ── Validation ───────────────────────────────────────────────────────────

// Validate checks the manifest as a whole: no duplicate tag names, every
// binding's node and device declared, every binding's type either a Sparkplug
// datatype name or a types: entry, the reserved rebirth metric refused, and no
// collision between a binding and a synthesized companion tag.
//
// It is deliberately exhaustive and fail-loud: the manifest is the only path
// to a tag (the runtime's tag set is fixed at compose time), so a mistake here
// is a tag that silently never updates.
func (m Manifest) Validate() error {
	types, err := m.validateTypes()
	if err != nil {
		return err
	}
	nodes, devices, err := m.validateNodes()
	if err != nil {
		return err
	}
	return m.validateBindings(types, nodes, devices)
}

// validateTypes checks the types: block and returns the set of declared type
// names. Field *types* are resolved by structDefs, which reports unknown
// references and cycles with the path that reached them.
func (m Manifest) validateTypes() (map[string]bool, error) {
	types := make(map[string]bool, len(m.Types))
	for i, t := range m.Types {
		if t.Name == "" {
			return nil, fmt.Errorf("host: manifest types[%d]: name is required", i)
		}
		if types[t.Name] {
			return nil, fmt.Errorf("host: manifest declares type %q twice", t.Name)
		}
		if _, ok := datatypeOf(t.Name); ok {
			return nil, fmt.Errorf("host: type %q shadows the Sparkplug datatype of the same name", t.Name)
		}
		types[t.Name] = true

		fields := make(map[string]bool, len(t.Fields))
		for j, f := range t.Fields {
			if f.Name == "" {
				return nil, fmt.Errorf("host: type %q field[%d]: name is required", t.Name, j)
			}
			if fields[f.Name] {
				return nil, fmt.Errorf("host: type %q declares field %q twice", t.Name, f.Name)
			}
			fields[f.Name] = true
			if f.Type == "" {
				return nil, fmt.Errorf("host: type %q field %q: type is required", t.Name, f.Name)
			}
			if f.ArrayLen > 0 {
				return nil, fmt.Errorf("host: type %q field %q: arrays unsupported", t.Name, f.Name)
			}
		}
	}
	return types, nil
}

// validateNodes checks the nodes: block and returns the node index plus the
// set of declared "<edge>/<device>" pairs.
func (m Manifest) validateNodes() (map[string]Node, map[string]bool, error) {
	nodes := make(map[string]Node, len(m.Nodes))
	devices := map[string]bool{}
	for i, n := range m.Nodes {
		if n.EdgeNode == "" {
			return nil, nil, fmt.Errorf("host: manifest nodes[%d]: edgenode is required", i)
		}
		if _, dup := nodes[n.EdgeNode]; dup {
			return nil, nil, fmt.Errorf("host: manifest declares node %q twice", n.EdgeNode)
		}
		nodes[n.EdgeNode] = n
		for j, dv := range n.Devices {
			if dv.Device == "" {
				return nil, nil, fmt.Errorf("host: node %q devices[%d]: device is required", n.EdgeNode, j)
			}
			key := n.EdgeNode + "/" + dv.Device
			if devices[key] {
				return nil, nil, fmt.Errorf("host: node %q declares device %q twice", n.EdgeNode, dv.Device)
			}
			devices[key] = true
		}
	}
	return nodes, devices, nil
}

// validateBindings checks the tags: block against the types and nodes, then
// the synthesized companion tags against the bindings.
func (m Manifest) validateBindings(types map[string]bool, nodes map[string]Node, devices map[string]bool) error {
	byName := map[string]bool{}
	byMetric := map[metricKey]string{}

	for i, b := range m.Tags {
		switch {
		case b.Name == "":
			return fmt.Errorf("host: manifest tags[%d]: name is required", i)
		case b.Node == "":
			return fmt.Errorf("host: binding %q: node is required", b.Name)
		case b.Metric == "":
			return fmt.Errorf("host: binding %q: metric is required", b.Name)
		case b.Type == "":
			return fmt.Errorf("host: binding %q: type is required", b.Name)
		}
		if byName[b.Name] {
			return fmt.Errorf("host: duplicate binding name %q", b.Name)
		}
		byName[b.Name] = true

		if b.ArrayLen > 0 {
			return fmt.Errorf("host: binding %q: arrays unsupported", b.Name)
		}

		n, ok := nodes[b.Node]
		if !ok {
			return fmt.Errorf("host: binding %q references unknown node %q", b.Name, b.Node)
		}
		if b.Device != "" && !devices[b.Node+"/"+b.Device] {
			return fmt.Errorf("host: binding %q references unknown device %q on node %q",
				b.Name, b.Device, b.Node)
		}

		// "Node Control/Rebirth" is the driver's own channel: <site>__Rebirth
		// is the sanctioned operator path, so a binding may not race it.
		if b.Metric == RebirthMetric {
			return fmt.Errorf("host: binding %q targets the reserved metric %q — "+
				"use the node's rebirthtag (%s) instead",
				b.Name, RebirthMetric, n.RebirthTagName())
		}

		if _, ok := scalarType(b.Type); !ok && !types[b.Type] {
			if dt, isDT := datatypeOf(b.Type); isDT {
				return fmt.Errorf("host: binding %q: Sparkplug datatype %v is not representable as a nautilus value", b.Name, dt)
			}
			return fmt.Errorf("host: binding %q references unknown type %q", b.Name, b.Type)
		}

		// One metric feeding two tags cannot be routed: the inbound index is
		// keyed by (node, device, metric).
		k := metricKey{EdgeNode: b.Node, Device: b.Device, Metric: b.Metric}
		if prev, dup := byMetric[k]; dup {
			return fmt.Errorf("host: bindings %q and %q both bind metric %s",
				prev, b.Name, metricPath(k))
		}
		byMetric[k] = b.Name
	}

	// The synthesized companion tags are real tags in the same namespace.
	owner := map[string]string{}
	for _, n := range m.Nodes {
		for _, t := range []struct{ name, what string }{
			{n.OnlineTagName(), "onlinetag"},
			{n.BirthTagName(), "birthtag"},
			{n.RebirthTagName(), "rebirthtag"},
		} {
			if err := claimCompanion(owner, byName, t.name, fmt.Sprintf("node %q %s", n.EdgeNode, t.what)); err != nil {
				return err
			}
		}
		for _, dv := range n.Devices {
			what := fmt.Sprintf("node %q device %q onlinetag", n.EdgeNode, dv.Device)
			if err := claimCompanion(owner, byName, n.DeviceOnlineTagName(dv), what); err != nil {
				return err
			}
		}
	}
	return nil
}

// claimCompanion records one synthesized tag name, rejecting a collision with
// a binding or with another node's companion.
func claimCompanion(owner map[string]string, bindings map[string]bool, name, what string) error {
	if name == "" {
		return nil
	}
	if bindings[name] {
		return fmt.Errorf("host: %s is %q, which a binding already declares", what, name)
	}
	if prev, dup := owner[name]; dup {
		return fmt.Errorf("host: %s is %q, which %s already declares", what, name, prev)
	}
	owner[name] = what
	return nil
}

// metricPath renders a metric's Sparkplug address for error messages.
func metricPath(k metricKey) string {
	if k.Device == "" {
		return k.EdgeNode + "/" + k.Metric
	}
	return k.EdgeNode + "/" + k.Device + "/" + k.Metric
}

// ── types: → ir.StructDef ────────────────────────────────────────────────

// structDefs builds the ir.StructDef for every manifest type, resolving nested
// references. The defs mirror what the generated sparkplug_types.st TYPE block
// lowers to: same field names, same order — so the values the driver builds
// from a Template metric bind cleanly to VAR_EXTERNAL declarations.
//
// Adapted from eip's structDefs (eip/manifest.go): the same memoized,
// cycle-detecting build with Sparkplug datatype names instead of Logix ones.
func (m Manifest) structDefs() (map[string]*ir.StructDef, error) {
	idx := m.typeIndex()
	defs := make(map[string]*ir.StructDef, len(m.Types))
	var build func(name string, stack []string) (*ir.StructDef, error)

	irType := func(owner string, f FieldDef, stack []string) (*ir.Type, error) {
		if f.ArrayLen > 0 {
			return nil, fmt.Errorf("host: type %q field %q: arrays unsupported", owner, f.Name)
		}
		if elem, ok := scalarType(f.Type); ok {
			return elem, nil
		}
		// Not a representable scalar: either another types: entry (recurse as
		// a struct) or a datatype the IR has no shape for.
		if _, ok := idx[f.Type]; !ok {
			if dt, isDT := datatypeOf(f.Type); isDT {
				return nil, fmt.Errorf("host: type %q field %q: Sparkplug datatype %v is not representable as a nautilus value",
					owner, f.Name, dt)
			}
		}
		sd, err := build(f.Type, stack)
		if err != nil {
			return nil, err
		}
		return &ir.Type{Kind: ir.TypeStruct, Struct: sd}, nil
	}

	build = func(name string, stack []string) (*ir.StructDef, error) {
		if d, ok := defs[name]; ok {
			return d, nil
		}
		for _, s := range stack {
			if s == name {
				return nil, fmt.Errorf("host: type cycle through %q", name)
			}
		}
		td, ok := idx[name]
		if !ok {
			return nil, fmt.Errorf("host: manifest references unknown type %q", name)
		}
		sd := &ir.StructDef{Name: td.Name, FieldIndex: map[string]int{}}
		inner := append(stack, name)
		for i, f := range td.Fields {
			ft, err := irType(name, f, inner)
			if err != nil {
				return nil, err
			}
			sd.Fields = append(sd.Fields, ir.StructField{Name: f.Name, Type: ft})
			sd.FieldIndex[f.Name] = i
		}
		defs[name] = sd
		return sd, nil
	}

	for _, t := range m.Types {
		if _, err := build(t.Name, nil); err != nil {
			return nil, err
		}
	}
	return defs, nil
}

// bindingType is the ir.Type one binding delivers: a scalar for a datatype
// name, a struct for a types: reference. defs is the map structDefs built.
func bindingType(b Binding, defs map[string]*ir.StructDef) (*ir.Type, error) {
	if t, ok := scalarType(b.Type); ok {
		return t, nil
	}
	sd, ok := defs[b.Type]
	if !ok {
		return nil, fmt.Errorf("host: binding %q references unknown type %q", b.Name, b.Type)
	}
	return &ir.Type{Kind: ir.TypeStruct, Struct: sd}, nil
}
