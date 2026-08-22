// mapping.go is the Sparkplug-name → nautilus-tag-name mapping (B1) and the
// manifest-derived indexes the driver reads on every message.
//
// The sanitizer mirrors eip/codegen's tagIdent, so both importers produce the
// same kind of identifier from a foreign name. Its one reserved output is the
// double underscore: because runs of "_" collapse, no real metric name can
// produce "__", which is what makes "<site>__Online" a safe marker for the
// driver-synthesized companion tags (docs/design/sparkplug-host.md §2, §3).

package host

import (
	"fmt"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Roles a generated tag can take. They are the tag file's own spelling, so a
// TagSpec drops straight into internal/tagfile.Tag.
const (
	RoleInput  = "input"
	RoleOutput = "output"
)

// Companion tag suffixes. The double underscore is reserved — Sanitize
// collapses "_" runs, so no metric name can produce one.
const (
	suffixOnline    = "__Online"
	suffixLastBirth = "__LastBirthMs"
	suffixRebirth   = "__Rebirth"
)

// ── The sanitizer ────────────────────────────────────────────────────────

// Sanitize converts a Sparkplug name into an ST/Go-safe identifier: every rune
// outside [A-Za-z0-9_] becomes "_", leading and trailing "_" are trimmed, runs
// of "_" collapse to one, and a name that is empty or starts with a digit is
// prefixed "M_".
//
//	"Well/Level"  → "Well_Level"
//	"1Pump"       → "M_1Pump"
//	"Motor °C"    → "Motor_C"
//	""            → "M_"
//
// Collisions are possible by construction ("A/B" and "A.B" agree) and are
// resolved by uniqueNames at generation time; composeTags rejects duplicates
// outright regardless.
func Sanitize(name string) string { return identGuard(sanitizeRunes(name)) }

// sanitizeRunes is Sanitize without the leading-digit guard — the form used
// for the segments of a composite tag name, where only the *first* segment
// can put a digit at the front.
func sanitizeRunes(name string) string {
	var sb strings.Builder
	sb.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	id := strings.Trim(sb.String(), "_")
	for strings.Contains(id, "__") {
		id = strings.ReplaceAll(id, "__", "_")
	}
	return id
}

// identGuard prefixes "M_" when an identifier is empty or digit-initial.
func identGuard(id string) string {
	if id == "" || (id[0] >= '0' && id[0] <= '9') {
		return "M_" + id
	}
	return id
}

// TagName composes the nautilus tag name for one metric:
// "<prefix>_<sanitize(device)>_<sanitize(metric)>", with the device segment
// omitted for a node-level metric.
//
//	TagName("W6", "", "Well/Level")     → "W6_Well_Level"
//	TagName("W6", "PLC1", "Pump/Run")   → "W6_PLC1_Pump_Run"
func TagName(prefix, device, metric string) string {
	parts := make([]string, 0, 3)
	for _, seg := range [...]string{prefix, device, metric} {
		if seg == "" {
			continue
		}
		if s := sanitizeRunes(seg); s != "" {
			parts = append(parts, s)
		}
	}
	return identGuard(strings.Join(parts, "_"))
}

// uniqueName returns base, or base_2, base_3, ... if base is taken. It marks
// the result used. Ported from eip/codegen's uniqueName so both importers
// resolve collisions identically.
func uniqueName(base string, used map[string]bool) string {
	name := base
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	used[name] = true
	return name
}

// uniqueNames resolves a whole slice of candidate names in order, so the
// result is positionally aligned with the input and deterministic: the first
// occurrence keeps the plain name, later ones get "_2", "_3", ...
func uniqueNames(names []string) []string {
	used := make(map[string]bool, len(names))
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = uniqueName(n, used)
	}
	return out
}

// ── Synthesized companion tags ───────────────────────────────────────────
//
// Every node gets an online flag, a last-birth timestamp and a rebirth
// trigger; every device gets an online flag. They are ordinary tags — ST
// interlocks on them and the HMI binds them — and the manifest may name them
// explicitly (the generator always does). An empty name in the manifest means
// "use the default", so a hand-written manifest gets them for free.

// TagPrefix is the tag-name prefix for this node's metrics: the manifest's
// prefix when set, else sanitize(edge_node_id).
func (n Node) TagPrefix() string {
	if n.Prefix != "" {
		return Sanitize(n.Prefix)
	}
	return Sanitize(n.EdgeNode)
}

// OnlineTagName is the node's BOOL online companion tag, "<prefix>__Online"
// unless the manifest names it.
func (n Node) OnlineTagName() string {
	if n.OnlineTag != "" {
		return n.OnlineTag
	}
	return n.TagPrefix() + suffixOnline
}

// BirthTagName is the node's INT last-NBIRTH-epoch-ms companion tag,
// "<prefix>__LastBirthMs" unless the manifest names it.
func (n Node) BirthTagName() string {
	if n.BirthTag != "" {
		return n.BirthTag
	}
	return n.TagPrefix() + suffixLastBirth
}

// RebirthTagName is the node's BOOL *output* companion tag: a rising edge
// sends NCMD Node Control/Rebirth. "<prefix>__Rebirth" unless the manifest
// names it.
func (n Node) RebirthTagName() string {
	if n.RebirthTag != "" {
		return n.RebirthTag
	}
	return n.TagPrefix() + suffixRebirth
}

// DeviceOnlineTagName is a device's BOOL online companion tag,
// "<prefix>_<device>__Online" unless the manifest names it.
func (n Node) DeviceOnlineTagName(d Device) string {
	if d.OnlineTag != "" {
		return d.OnlineTag
	}
	return n.TagPrefix() + "_" + sanitizeRunes(d.Device) + suffixOnline
}

// ── Generated tag specs ──────────────────────────────────────────────────

// TagSpec is one line of the generated tag file: what the manifest implies a
// nautilus tag must be, independent of how it is rendered. The codegen tier
// turns these into internal/tagfile.Tag values.
type TagSpec struct {
	// Name is the nautilus tag name.
	Name string
	// Role is RoleInput or RoleOutput. Writable bindings and the rebirth
	// companion are outputs; everything else is an input.
	Role string
	// Type names a types: entry for a Template-shaped tag, and is empty for a
	// scalar — whose type comes from the value the driver delivers.
	Type string
	// Init is an output's initial value, so the tag exists from scan one:
	// the binding's init when it has one, else the type zero. Nil for inputs
	// and for struct-shaped outputs, which have no scalar literal.
	Init any
}

// TagSpecs returns every tag this manifest implies — the synthesized
// companions and the bindings — sorted by name, so a regenerated tag file
// diffs cleanly against the last one.
func (m Manifest) TagSpecs() []TagSpec {
	types := m.typeIndex()
	out := make([]TagSpec, 0, len(m.Tags)+3*len(m.Nodes))

	for _, n := range m.Nodes {
		for _, name := range [...]string{n.OnlineTagName(), n.BirthTagName()} {
			if name != "" {
				out = append(out, TagSpec{Name: name, Role: RoleInput})
			}
		}
		if name := n.RebirthTagName(); name != "" {
			out = append(out, TagSpec{Name: name, Role: RoleOutput, Init: false})
		}
		for _, dv := range n.Devices {
			if name := n.DeviceOnlineTagName(dv); name != "" {
				out = append(out, TagSpec{Name: name, Role: RoleInput})
			}
		}
	}

	for _, b := range m.Bindings() {
		spec := TagSpec{Name: b.Name, Role: RoleInput}
		if _, isStruct := types[b.Type]; isStruct {
			spec.Type = b.Type
		}
		if b.Writable {
			spec.Role = RoleOutput
			spec.Init = b.Init
			if spec.Init == nil {
				spec.Init = zeroInit(b.Type)
			}
		}
		out = append(out, spec)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// zeroInit is the tag-file literal for a Sparkplug datatype's zero value.
// Struct-shaped types have none (nil), so a writable Template binding
// generates without an init.
func zeroInit(typeName string) any {
	t, ok := scalarType(typeName)
	if !ok {
		return nil
	}
	switch t.Kind {
	case ir.TypeBool:
		return false
	case ir.TypeInt:
		return int64(0)
	case ir.TypeReal:
		return 0.0
	case ir.TypeString:
		return ""
	}
	return nil
}

// ── Driver indexes ───────────────────────────────────────────────────────

// buildIndexes validates the manifest and fills the driver's read-only,
// manifest-derived indexes. New calls it once; everything it writes is read
// without a lock afterwards.
func (d *Driver) buildIndexes() error {
	m := d.manifest
	if err := m.Validate(); err != nil {
		return err
	}
	defs, err := m.structDefs()
	if err != nil {
		return err
	}
	d.defs = defs

	d.nodeCfg = make(map[string]Node, len(m.Nodes))
	for _, n := range m.Nodes {
		d.nodeCfg[n.EdgeNode] = n
	}

	d.inputs = nil
	d.byName = make(map[string]Binding)
	d.byMetric = make(map[metricKey]Binding, len(m.Tags))
	for _, b := range m.Bindings() {
		// Validate has already rejected an unresolvable type; this catches a
		// Driver built from a Manifest literal that skipped the loader.
		if _, err := bindingType(b, d.defs); err != nil {
			return err
		}
		d.byMetric[metricKey{EdgeNode: b.Node, Device: b.Device, Metric: b.Metric}] = b
		if b.Writable {
			d.byName[b.Name] = b
		} else {
			d.inputs = append(d.inputs, b)
		}
	}

	d.synthInputs = nil
	d.synthOutputs = nil
	d.rebirthTags = make(map[string]string, len(m.Nodes))
	for _, n := range m.Nodes {
		for _, name := range [...]string{n.OnlineTagName(), n.BirthTagName()} {
			if name != "" {
				d.synthInputs = append(d.synthInputs, name)
			}
		}
		for _, dv := range n.Devices {
			if name := n.DeviceOnlineTagName(dv); name != "" {
				d.synthInputs = append(d.synthInputs, name)
			}
		}
		if name := n.RebirthTagName(); name != "" {
			d.synthOutputs = append(d.synthOutputs, name)
			d.rebirthTags[name] = n.EdgeNode
		}
	}
	sort.Strings(d.synthInputs)
	sort.Strings(d.synthOutputs)
	return nil
}

// InputNames returns every tag the driver delivers values for: the read-only
// bindings plus the synthesized companions (__Online, __LastBirthMs) — for
// runtime.Options.Inputs.
//
// The companions are present from t=0 even though a data metric stays absent
// from the snapshot until it is first seen, so alarm logic can interlock on
// __Online before the first birth (docs/design/sparkplug-host.md §2).
func (d *Driver) InputNames() []string {
	out := make([]string, 0, len(d.inputs)+len(d.synthInputs))
	for _, b := range d.inputs {
		out = append(out, b.Name)
	}
	out = append(out, d.synthInputs...)
	sort.Strings(out)
	return out
}

// OutputNames returns the tags the driver accepts writes for: the writable
// bindings (NCMD/DCMD) plus the synthesized __Rebirth triggers — for
// runtime.Options.Outputs.
func (d *Driver) OutputNames() []string {
	out := make([]string, 0, len(d.byName)+len(d.synthOutputs))
	for n := range d.byName {
		out = append(out, n)
	}
	out = append(out, d.synthOutputs...)
	sort.Strings(out)
	return out
}

// StructDefs returns the ir.StructDefs the manifest's types: block lowers to,
// keyed by type name — the shapes a Template-typed tag's value carries.
func (d *Driver) StructDefs() map[string]*ir.StructDef {
	out := make(map[string]*ir.StructDef, len(d.defs))
	for k, v := range d.defs {
		out[k] = v
	}
	return out
}
