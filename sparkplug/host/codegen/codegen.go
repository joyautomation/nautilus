// Package codegen turns Sparkplug birth certificates — or an offline site
// list — into the three committed files a nautilus host-application project
// needs:
//
//	sparkplug_types.st        IEC TYPE block, one per Sparkplug Template (UDT)
//	sparkplug_manifest.yaml   the driver's binding contract
//	tags/sparkplug.yaml       the tag file, composed through tag-files:
//
// It is the engine behind `nautilus sparkplug import | tags`, and it is the
// eip/codegen of the Sparkplug world: the same split into a discovery step
// that needs a broker (host.Discover, or --sites for none) and PURE renderers
// that need only a committed manifest, so `nautilus sparkplug tags` can
// regenerate the tag file in CI, during review, or after the manifest is
// hand-edited — with no broker in sight.
//
// Every renderer is deterministic. A regenerated file must diff against the
// last one, showing what changed at the sites, rather than reshuffling and
// hiding it: types come out dependency-ordered, bindings and tags sorted by
// name, and nothing in a generated header echoes a command line (the broker
// lives in nautilus.yaml, not in the manifest, so a header that named it
// would churn every time someone re-ran `nautilus sparkplug tags`).
package codegen

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/stgen"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/host"
	"github.com/joyautomation/nautilus/sparkplug/spb"
	"gopkg.in/yaml.v3"
)

// Layouts. Only LayoutFlat is implemented: one nautilus tag per metric, so a
// partial NDATA update is a single-tag write and RBE globs, the historian and
// the HMI all work per tag. LayoutStruct ("W6.Well.Level", one TYPE per site)
// is parsed and rejected — the flag exists so a project that wants it later
// does not have to change its scripts, and so the rejection is explicit
// rather than a silently different layout.
const (
	LayoutFlat   = "flat"
	LayoutStruct = "struct"
)

// Prefix modes for PrefixOf.
const (
	// PrefixNode derives the per-node tag prefix from the edge_node_id
	// (the default): W6 + Well/Level → W6_Well_Level.
	PrefixNode = "node"
	// PrefixNone drops the prefix from *metric* tag names — for a
	// single-site project where the node id adds nothing. The synthesized
	// companion tags keep it: __Online has to be unique per node.
	PrefixNone = "none"
)

// Options select and shape the generation.
type Options struct {
	// Nodes are path.Match globs against edge_node_id. Empty selects every
	// node heard from.
	Nodes []string
	// Metrics are path.Match globs against a metric's name ("Well/Level") or
	// its device-qualified path ("PLC1/Pump/Run"). Empty selects everything.
	//
	// path.Match's "*" does not cross "/", which is a feature here: Sparkplug
	// metric names are folder-shaped, so "Pump/*" selects one folder and
	// "*" selects only top-level metrics.
	Metrics []string
	// Writable are the same shape of globs, marking matching bindings
	// writable — the tags whose values go back out as NCMD/DCMD.
	//
	// A pattern containing "." is a MEMBER pattern: it is matched against
	// "<metric>.<member.path>" (and its device-qualified form) and generates
	// one scalar output tag per matching member of a Template metric —
	// "Motor1.START", "*.HSP", "*.LVL.CTL*SP". A pattern with no "." marks
	// the whole metric writable, which only a scalar metric can be: matching
	// a Template as a whole is an ERROR, because writing the struct back
	// would clobber the members the edge is driving.
	Writable []string
	// Layout is LayoutFlat (default) or LayoutStruct (rejected — see above).
	Layout string
	// Prefix is PrefixNode (default), PrefixNone, or a literal prefix, which
	// is only legal for a single-node import (two nodes sharing one literal
	// prefix would collide on every companion tag).
	Prefix string
	// OnSkip, if set, is called once per metric or template member the
	// generator could not bind, with a human-readable reason. `nautilus
	// sparkplug import` prints them to stderr, the way eip import prints
	// Output.Skipped: nothing is dropped silently.
	OnSkip func(string)
}

func (o Options) skip(msg string) {
	if o.OnSkip != nil {
		o.OnSkip(msg)
	}
}

// layout validates the layout flag.
func (o Options) layout() (string, error) {
	switch o.Layout {
	case "", LayoutFlat:
		return LayoutFlat, nil
	case LayoutStruct:
		return "", fmt.Errorf("codegen: --layout struct is not yet supported: " +
			"every partial metric update would become a read-modify-write of the whole site " +
			"(docs/design/sparkplug-host.md §3). Use --layout flat")
	}
	return "", fmt.Errorf("codegen: unknown layout %q (want %q)", o.Layout, LayoutFlat)
}

// prefixFor is the tag-name prefix for one edge node.
func (o Options) prefixFor(edge string) string {
	switch o.Prefix {
	case "", PrefixNode:
		return host.Sanitize(edge)
	case PrefixNone:
		return ""
	}
	return host.Sanitize(o.Prefix)
}

// ── discovery → manifest ─────────────────────────────────────────────────

// FromBirths builds a manifest from birth certificates — what
// `nautilus sparkplug import` does with what host.Discover heard on the
// broker.
//
// Node-level metrics come from each NBIRTH, device metrics from each DBIRTH,
// and the types: block from the NBIRTH Template *definitions* (IsDefinition),
// which is how a Sparkplug host learns a UDT's shape. Sparkplug plumbing
// (bdSeq, Node Control/*, Device Control/*) is skipped — and NOTHING else is:
// the driver's own isProtocolMetric skips exactly this set, so a metric the
// import leaves out is one the runtime will report as unmanifested forever.
//
// The manifest holds one group, so births from two groups are an error: run
// one import per group and compose the projects, rather than silently
// dropping half the sites.
func FromBirths(births []host.Birth, opts Options) (host.Manifest, error) {
	if _, err := opts.layout(); err != nil {
		return host.Manifest{}, err
	}
	sorted := append([]host.Birth(nil), births...)
	sort.SliceStable(sorted, func(i, j int) bool { return lessBirth(sorted[i], sorted[j]) })

	group := ""
	for _, b := range sorted {
		if !selects(b.EdgeNode, opts.Nodes) {
			continue
		}
		if group == "" {
			group = b.Group
			continue
		}
		if b.Group != group {
			return host.Manifest{}, fmt.Errorf(
				"codegen: births span groups %q and %q — a manifest describes one group; "+
					"run one import per group (--group)", group, b.Group)
		}
	}

	tb := newTypeBuilder(opts)
	for _, b := range sorted {
		if !selects(b.EdgeNode, opts.Nodes) {
			continue
		}
		if err := tb.harvest(b); err != nil {
			return host.Manifest{}, err
		}
	}
	types, err := tb.typeDefs()
	if err != nil {
		return host.Manifest{}, err
	}

	// Nodes and their metrics, in discovery order (which is sorted).
	sites := map[string]*site{}
	var order []string
	for _, b := range sorted {
		if !selects(b.EdgeNode, opts.Nodes) {
			continue
		}
		s, ok := sites[b.EdgeNode]
		if !ok {
			s = &site{edge: b.EdgeNode, devices: map[string][]metric{}}
			sites[b.EdgeNode] = s
			order = append(order, b.EdgeNode)
		}
		ms := collectMetrics(b, tb, opts)
		if b.IsNode() {
			s.metrics = append(s.metrics, ms...)
			continue
		}
		if _, seen := s.devices[b.Device]; !seen {
			s.deviceIDs = append(s.deviceIDs, b.Device)
		}
		s.devices[b.Device] = append(s.devices[b.Device], ms...)
	}
	sort.Strings(order)

	m := host.Manifest{Group: group, Types: types}
	if err := buildNodes(&m, sites, order, opts); err != nil {
		return host.Manifest{}, err
	}
	if len(m.Nodes) == 0 {
		return host.Manifest{}, fmt.Errorf(
			"codegen: no births matched (%d heard; --nodes %s)",
			len(births), patternsText(opts.Nodes))
	}
	if err := m.Validate(); err != nil {
		return host.Manifest{}, fmt.Errorf("codegen: generated manifest is invalid: %w", err)
	}
	return m, nil
}

// site is one edge node's harvested metrics.
type site struct {
	edge string
	// prefix is a per-site tag-prefix override (--sites files may carry one);
	// empty means the Options prefix policy decides.
	prefix    string
	metrics   []metric
	deviceIDs []string
	devices   map[string][]metric
}

// metric is one bindable metric: its Sparkplug name and the manifest type
// name it takes (a datatype name, or a types: entry for a Template).
type metric struct {
	name string
	typ  string
	// writable/members/init come from a --sites file, which can state them; a
	// birth cannot, so the broker path leaves them to the --writable globs.
	writable bool
	// members are dotted member paths inside a Template metric, each of which
	// becomes its own scalar output binding.
	members []string
	init    any
}

// buildNodes turns the harvested sites into manifest nodes, devices and
// bindings, resolving tag-name collisions with uniqueName's _2/_3 suffixes.
//
// Companion tag names are claimed FIRST, before any binding name: a metric
// literally called "Online" must yield to <site>__Online, not shadow it —
// Manifest.Validate rejects the collision either way, and this way the import
// resolves it instead of failing.
func buildNodes(m *host.Manifest, sites map[string]*site, order []string, opts Options) error {
	// Metric tag prefix per node ("" under --prefix none), and the prefix the
	// COMPANION tags take, which is never empty: __Online has to be unique
	// per node even when the metric names drop the prefix.
	prefixes := make(map[string]string, len(order))
	companions := make(map[string]string, len(order))
	owner := map[string]string{}
	for _, edge := range order {
		p := opts.prefixFor(edge)
		if s := sites[edge]; s.prefix != "" {
			p = host.Sanitize(s.prefix)
		}
		prefixes[edge] = p
		c := p
		if c == "" {
			c = host.Sanitize(edge)
		}
		companions[edge] = c
		if prev, dup := owner[c]; dup {
			return fmt.Errorf("codegen: nodes %q and %q both resolve to the tag prefix %q — "+
				"their __Online companions would collide; use --prefix node or give one a prefix",
				prev, edge, c)
		}
		owner[c] = edge
	}

	used := map[string]bool{}
	nodes := make([]host.Node, 0, len(order))
	for _, edge := range order {
		s := sites[edge]
		n := host.Node{EdgeNode: edge, Prefix: prefixes[edge]}
		companion := host.Node{EdgeNode: edge, Prefix: companions[edge]}
		n.OnlineTag = claim(companion.OnlineTagName(), used)
		n.BirthTag = claim(companion.BirthTagName(), used)
		n.RebirthTag = claim(companion.RebirthTagName(), used)
		sort.Strings(s.deviceIDs)
		for _, id := range s.deviceIDs {
			dv := host.Device{Device: id}
			dv.OnlineTag = claim(companion.DeviceOnlineTagName(dv), used)
			n.Devices = append(n.Devices, dv)
		}
		nodes = append(nodes, n)
	}
	m.Nodes = nodes

	types := make(map[string]host.TypeDef, len(m.Types))
	for _, td := range m.Types {
		types[td.Name] = td
	}
	for _, edge := range order {
		s := sites[edge]
		for _, mt := range s.metrics {
			bs, err := bindings(prefixes[edge], edge, "", mt, used, opts, types)
			if err != nil {
				return err
			}
			m.Tags = append(m.Tags, bs...)
		}
		for _, id := range s.deviceIDs {
			for _, mt := range s.devices[id] {
				bs, err := bindings(prefixes[edge], edge, id, mt, used, opts, types)
				if err != nil {
					return err
				}
				m.Tags = append(m.Tags, bs...)
			}
		}
	}
	sort.SliceStable(m.Tags, func(i, j int) bool { return m.Tags[i].Name < m.Tags[j].Name })
	return nil
}

// bindings composes the tag bindings one metric implies, claiming their
// names: the metric's own binding, plus one scalar output binding per
// writable MEMBER of a Template metric.
func bindings(prefix, edge, device string, mt metric, used map[string]bool,
	opts Options, types map[string]host.TypeDef) ([]host.Binding, error) {

	_, isTemplate := types[mt.typ]
	path := qualify(device, mt.name)

	// Same matching rule as --metrics: a glob may name the metric alone
	// ("Pump/*") or its device path ("PLC1/Pump/*"). Only the patterns
	// WITHOUT a "." speak about the metric as a whole.
	whole := mt.writable
	for _, p := range opts.Writable {
		if strings.Contains(p, host.MemberSep) {
			continue
		}
		if selects(path, []string{p}) || selects(mt.name, []string{p}) {
			whole = true
			break
		}
	}
	if whole && isTemplate {
		return nil, fmt.Errorf("codegen: %s is a Template (%s) — templates are written per member: "+
			"use metric.MEMBER (e.g. --writable %q) rather than the whole metric, "+
			"which would clobber every member the edge is driving",
			path, mt.typ, mt.name+"."+firstLeaf(types, mt.typ))
	}

	b := host.Binding{
		Name:   claim(host.TagName(prefix, device, mt.name), used),
		Node:   edge,
		Device: device,
		Metric: mt.name,
		Type:   mt.typ,
	}
	b.Writable = whole
	if whole {
		b.Init = mt.init
	}
	out := []host.Binding{b}

	// Member bindings. Candidates are the type's scalar leaves, walked in
	// declaration order so the generated names are deterministic; a leaf is
	// taken when the --sites file names it or a dotted --writable glob
	// matches "<metric>.<leaf>" (or its device-qualified form).
	leaves := leafPaths(types, mt.typ)
	named := make(map[string]bool, len(mt.members))
	for _, s := range mt.members {
		named[s] = true
	}
	if !isTemplate && len(mt.members) > 0 {
		return nil, fmt.Errorf("codegen: %s is a %s, not a Template — writable: names members (%s) "+
			"but a scalar metric has none; use writable: true",
			path, mt.typ, strings.Join(mt.members, ", "))
	}
	seen := map[string]bool{}
	for _, leaf := range leaves {
		wanted := named[leaf]
		if !wanted {
			for _, p := range opts.Writable {
				if !strings.Contains(p, host.MemberSep) {
					continue
				}
				cand := mt.name + host.MemberSep + leaf
				if selects(cand, []string{p}) || selects(qualify(device, cand), []string{p}) {
					wanted = true
					break
				}
			}
		}
		if !wanted {
			continue
		}
		seen[leaf] = true
		out = append(out, host.Binding{
			Name:     claim(host.MemberTagName(prefix, device, mt.name, leaf), used),
			Node:     edge,
			Device:   device,
			Metric:   mt.name,
			Member:   leaf,
			Type:     mt.typ,
			Writable: true,
		})
	}
	// A named member that is not a scalar leaf of the type is a typo or a
	// stale path: fail loud rather than generate nothing for it.
	for _, s := range mt.members {
		if !seen[s] {
			return nil, fmt.Errorf("codegen: %s: type %s has no scalar member %q (members: %s)",
				path, mt.typ, s, strings.Join(leaves, ", "))
		}
	}
	return out, nil
}

// leafPaths lists the dotted paths to every SCALAR leaf of a manifest type,
// depth-first in declaration order — the candidate set a dotted --writable
// glob matches against, and the set a --sites writable: list must name from.
// A nested template contributes its own leaves under its member name; a
// cyclic types: block (which Manifest.Validate rejects) terminates here too.
func leafPaths(types map[string]host.TypeDef, typ string) []string {
	var out []string
	var walk func(t, prefix string, stack []string)
	walk = func(t, prefix string, stack []string) {
		td, ok := types[t]
		if !ok {
			return
		}
		for _, s := range stack {
			if s == t {
				return
			}
		}
		inner := append(stack, t)
		for _, f := range td.Fields {
			if f.ArrayLen > 0 {
				continue
			}
			p := f.Name
			if prefix != "" {
				p = prefix + host.MemberSep + f.Name
			}
			if _, nested := types[f.Type]; nested {
				walk(f.Type, p, inner)
				continue
			}
			if representable(f.Type) {
				out = append(out, p)
			}
		}
	}
	walk(typ, "", nil)
	return out
}

// firstLeaf names one member of a type, for the "use metric.MEMBER" hint.
func firstLeaf(types map[string]host.TypeDef, typ string) string {
	if l := leafPaths(types, typ); len(l) > 0 {
		return l[0]
	}
	return "MEMBER"
}

// claim resolves one generated name against the names already taken,
// appending _2, _3, ... — host.Sanitize can map two distinct Sparkplug names
// onto one identifier ("A/B" and "A.B" agree), and a tag may be declared once.
func claim(name string, used map[string]bool) string {
	if name == "" {
		return ""
	}
	out := name
	for i := 2; used[out]; i++ {
		out = fmt.Sprintf("%s_%d", name, i)
	}
	used[out] = true
	return out
}

// collectMetrics pulls the bindable metrics out of one birth: skipping
// Sparkplug plumbing, template definitions, metrics the filters exclude, and
// datatypes the IR cannot represent.
func collectMetrics(b host.Birth, tb *typeBuilder, opts Options) []metric {
	var out []metric
	for _, mm := range b.Payload.Metrics {
		if mm.Name == "" || isProtocolMetric(mm.Name) || isDefinition(mm) {
			continue
		}
		if !selects(qualify(b.Device, mm.Name), opts.Metrics) && !selects(mm.Name, opts.Metrics) {
			continue
		}
		typ, ok := tb.bindingType(mm)
		if !ok {
			opts.skip(fmt.Sprintf("%s: datatype %v is not representable as a nautilus value",
				where(b, mm.Name), mm.Datatype))
			continue
		}
		out = append(out, metric{name: mm.Name, typ: typ})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// where renders a metric's Sparkplug address for a skip message.
func where(b host.Birth, name string) string {
	if b.Device == "" {
		return b.EdgeNode + "/" + name
	}
	return b.EdgeNode + "/" + b.Device + "/" + name
}

// qualify is a metric's device-scoped path, which the --metrics/--writable
// globs match alongside the bare name.
func qualify(device, name string) string {
	if device == "" {
		return name
	}
	return device + "/" + name
}

// patternsText renders a glob list for an error message.
func patternsText(pats []string) string {
	if len(pats) == 0 {
		return "(any)"
	}
	return strings.Join(pats, ",")
}

// selects reports whether name matches any pattern. No patterns = everything.
func selects(name string, pats []string) bool {
	if len(pats) == 0 {
		return true
	}
	for _, p := range pats {
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

// isProtocolMetric is the driver's own rule (sparkplug/host/state.go), kept
// byte-identical on purpose: a metric the import skips but the driver would
// bind becomes a permanent "metric not in the manifest" log line.
func isProtocolMetric(name string) bool {
	return name == "bdSeq" || name == host.RebirthMetric ||
		strings.HasPrefix(name, "Node Control/") ||
		strings.HasPrefix(name, "Device Control/")
}

func isDefinition(m sparkplug.Metric) bool {
	t, ok := m.Value.(*sparkplug.Template)
	return ok && t != nil && t.IsDefinition
}

func lessBirth(a, b host.Birth) bool {
	if a.Group != b.Group {
		return a.Group < b.Group
	}
	if a.EdgeNode != b.EdgeNode {
		return a.EdgeNode < b.EdgeNode
	}
	return a.Device < b.Device
}

// ── Template definitions → types: ────────────────────────────────────────

// typeBuilder harvests Template definitions across every birth and renders
// them as manifest TypeDefs.
//
// The definitions are read DIRECTLY, rather than through
// sparkplug.StructDefsFromTemplates: that helper lowers straight to the IR,
// which collapses every integer width to int64 and both floats to float64,
// and the manifest's job is to record the Sparkplug contract (Int32, Float)
// rather than what nautilus happens to store it in. The shapes are still
// checked end to end — Manifest.Validate resolves every reference, and
// TypesST compiles the generated ST through stgen.
type typeBuilder struct {
	opts Options
	// defs are the raw definitions by Sparkplug template name, first one wins.
	defs map[string]*sparkplug.Template
	// order is definition-first-seen order, for a stable type name pass.
	order []string
	// names maps a Sparkplug template name to its manifest/ST type name.
	names map[string]string
	// used reserves type names: every Sparkplug datatype name is taken (a
	// manifest type may not shadow one — Manifest.Validate rejects it).
	used map[string]bool
	// built memoizes the rendered TypeDefs by manifest type name.
	built map[string]host.TypeDef
}

func newTypeBuilder(opts Options) *typeBuilder {
	used := map[string]bool{}
	for _, n := range spb.DataType_value {
		used[spb.DataType(n).String()] = true
	}
	return &typeBuilder{
		opts:  opts,
		defs:  map[string]*sparkplug.Template{},
		names: map[string]string{},
		used:  used,
		built: map[string]host.TypeDef{},
	}
}

// harvest records one birth's Template definitions, rejecting two nodes that
// disagree about the same type name — the manifest holds one shape per name,
// and quietly keeping the first would bind half the fleet to the wrong shape.
func (tb *typeBuilder) harvest(b host.Birth) error {
	for _, mm := range b.Payload.Metrics {
		t, ok := mm.Value.(*sparkplug.Template)
		if !ok || t == nil || !t.IsDefinition || mm.Name == "" {
			continue
		}
		prev, seen := tb.defs[mm.Name]
		if !seen {
			tb.defs[mm.Name] = t
			tb.order = append(tb.order, mm.Name)
			continue
		}
		if !sameShape(prev, t) {
			return fmt.Errorf("codegen: template %q is defined with different members by "+
				"more than one node (at %s) — the manifest holds one shape per type name",
				mm.Name, b.EdgeNode)
		}
	}
	return nil
}

// sameShape compares two definitions by member name and datatype, which is
// all a definition carries.
func sameShape(a, b *sparkplug.Template) bool {
	if len(a.Metrics) != len(b.Metrics) {
		return false
	}
	for i := range a.Metrics {
		if a.Metrics[i].Name != b.Metrics[i].Name || a.Metrics[i].Datatype != b.Metrics[i].Datatype {
			return false
		}
		at, aok := a.Metrics[i].Value.(*sparkplug.Template)
		bt, bok := b.Metrics[i].Value.(*sparkplug.Template)
		if aok != bok {
			return false
		}
		if aok && at != nil && bt != nil && at.TemplateRef != bt.TemplateRef {
			return false
		}
	}
	return true
}

// typeName is the manifest/ST identifier for one Sparkplug template name,
// sanitized, deduplicated, and never shadowing a Sparkplug datatype name.
func (tb *typeBuilder) typeName(tmplName string) string {
	if n, ok := tb.names[tmplName]; ok {
		return n
	}
	n := claim(host.Sanitize(tmplName), tb.used)
	tb.names[tmplName] = n
	return n
}

// bindingType is the manifest type for one instance metric: a datatype name
// for a scalar, the referenced type's name for a Template.
func (tb *typeBuilder) bindingType(m sparkplug.Metric) (string, bool) {
	if m.Datatype == spb.DataType_Template {
		t, ok := m.Value.(*sparkplug.Template)
		if !ok || t == nil || t.TemplateRef == "" {
			return "", false
		}
		if _, known := tb.defs[t.TemplateRef]; !known {
			return "", false
		}
		return tb.typeName(t.TemplateRef), true
	}
	name := m.Datatype.String()
	if !representable(name) {
		return "", false
	}
	return name, true
}

// representable mirrors host's scalarType: the Sparkplug datatypes that have
// an ir.Type. Arrays, DataSet, Bytes, File and PropertySet do not.
func representable(name string) bool {
	switch name {
	case "Boolean",
		"Int8", "Int16", "Int32", "Int64",
		"UInt8", "UInt16", "UInt32", "UInt64", "DateTime",
		"Float", "Double",
		"String", "Text", "UUID":
		return true
	}
	return false
}

// typeDefs renders every harvested definition, dependency-ordered so a nested
// type is declared before the type that uses it — the order the ST TYPE block
// and Manifest.structDefs both want.
func (tb *typeBuilder) typeDefs() ([]host.TypeDef, error) {
	var out []host.TypeDef
	emitted := map[string]bool{}
	var emit func(tmplName string, stack []string) error
	emit = func(tmplName string, stack []string) error {
		name := tb.typeName(tmplName)
		if emitted[name] {
			return nil
		}
		for _, s := range stack {
			if s == tmplName {
				return fmt.Errorf("codegen: template cycle through %q", tmplName)
			}
		}
		t, ok := tb.defs[tmplName]
		if !ok {
			return fmt.Errorf("codegen: template %q references undefined template", tmplName)
		}
		td := host.TypeDef{Name: name}
		inner := append(stack, tmplName)
		for _, mm := range t.Metrics {
			if mm.Name == "" {
				tb.opts.skip(fmt.Sprintf("template %s: unnamed member skipped", name))
				continue
			}
			ft, err := tb.memberType(name, mm, inner, emit)
			if err != nil {
				return err
			}
			if ft == "" {
				continue // skipped, already reported
			}
			td.Fields = append(td.Fields, host.FieldDef{Name: mm.Name, Type: ft})
		}
		emitted[name] = true
		out = append(out, td)
		return nil
	}
	for _, tmplName := range tb.order {
		if err := emit(tmplName, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// memberType resolves one definition member to a manifest type name, emitting
// the nested type first when the member is itself a template. "" means the
// member was skipped (and reported).
func (tb *typeBuilder) memberType(owner string, mm sparkplug.Metric, stack []string,
	emit func(string, []string) error) (string, error) {

	if mm.Datatype != spb.DataType_Template {
		name := mm.Datatype.String()
		if !representable(name) {
			tb.opts.skip(fmt.Sprintf("template %s member %s: datatype %v is not representable "+
				"as a nautilus value", owner, mm.Name, mm.Datatype))
			return "", nil
		}
		return name, nil
	}
	nested, _ := mm.Value.(*sparkplug.Template)
	switch {
	case nested != nil && nested.TemplateRef != "":
		// The Ignition/Tahu convention nautilus's own edge follows: the
		// member is a REFERENCE to another definition metric.
		if _, known := tb.defs[nested.TemplateRef]; !known {
			tb.opts.skip(fmt.Sprintf("template %s member %s: references unknown template %q",
				owner, mm.Name, nested.TemplateRef))
			return "", nil
		}
		if err := emit(nested.TemplateRef, stack); err != nil {
			return "", err
		}
		return tb.typeName(nested.TemplateRef), nil
	case nested != nil && nested.IsDefinition:
		// An inline nested definition: admitted under a synthetic name, the
		// same way sparkplug.StructDefsFromTemplates admits "<parent>.<member>".
		synthetic := owner + "/" + mm.Name
		if _, dup := tb.defs[synthetic]; !dup {
			tb.defs[synthetic] = nested
		}
		if err := emit(synthetic, stack); err != nil {
			return "", err
		}
		return tb.typeName(synthetic), nil
	}
	tb.opts.skip(fmt.Sprintf("template %s member %s: Template member with no templateRef",
		owner, mm.Name))
	return "", nil
}

// ── renderers ────────────────────────────────────────────────────────────

// ManifestYAML renders sparkplug_manifest.yaml: the manifest as data, in
// yaml.v3 block style, with no struct tags — so the on-disk keys are the
// lowercased Go field names ("edgenode", "onlinetag", "arraylen"), decoded
// with KnownFields(true). eip_manifest.yaml's convention exactly.
func ManifestYAML(m host.Manifest) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	body, err := yaml.Marshal(withFloatInits(m))
	if err != nil {
		return nil, fmt.Errorf("codegen: render manifest: %w", err)
	}
	header := "# Generated by `nautilus sparkplug import` — the group's template shapes,\n" +
		"# edge nodes and metric bindings. Re-run the import to refresh; births are\n" +
		"# validated against it at runtime, and unmanifested metrics are reported\n" +
		"# through on-unknown: (log by default) rather than bound silently.\n" +
		"#\n" +
		"# Wire it up in nautilus.yaml:\n" +
		"#   driver: {type: sparkplug-host, broker: ..., host-id: ..., manifest: sparkplug_manifest.yaml}\n"
	return append([]byte(header), body...), nil
}

// withFloatInits copies the manifest with every float init: wrapped so it
// keeps its decimal point on the way out.
//
// yaml.v3 renders float64(0) as "0", which decodes back as an *int* — and a
// REAL init that silently becomes a DINT init retypes the tag it seeds. It is
// the same trap internal/tagfile.scalar guards in the tag file, guarded here
// in the manifest that feeds it.
func withFloatInits(m host.Manifest) host.Manifest {
	out := m
	out.Tags = append([]host.Binding(nil), m.Tags...)
	for i, b := range out.Tags {
		if f, ok := b.Init.(float64); ok {
			out.Tags[i].Init = floatLiteral(f)
		}
	}
	return out
}

// floatLiteral is a float64 that always renders with a decimal point.
type floatLiteral float64

func (f floatLiteral) MarshalYAML() (any, error) {
	s := strconv.FormatFloat(float64(f), 'g', -1, 64)
	if !strings.ContainsAny(s, ".eEnN") {
		s += ".0"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: s}, nil
}

// TypesST renders sparkplug_types.st: one IEC TYPE per manifest types: entry,
// dependency-ordered, plus a ready-to-paste VAR_EXTERNAL suggestion covering
// every tag the manifest implies (bindings and synthesized companions).
//
// The TYPE block is built and COMPILED by stgen, so a broken import fails
// here, in the generator, rather than on disk at `nautilus check` time.
func TypesST(m host.Manifest) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("(*\n")
	sb.WriteString("  Generated by `nautilus sparkplug import` from the group's NBIRTH template\n")
	sb.WriteString("  definitions. Do not edit — re-run the import when a site's UDTs change.\n")
	if m.Group != "" {
		fmt.Fprintf(&sb, "  Group: %s\n", m.Group)
	}
	sb.WriteString("*)\n\n")

	structs := make([]*stgen.StructDef, 0, len(m.Types))
	for _, t := range m.Types {
		s := stgen.Struct(t.Name)
		for _, f := range t.Fields {
			s.AddField(stgen.Field(f.Name, stType(f.Type, f.ArrayLen)))
		}
		structs = append(structs, s)
	}
	block, err := stgen.Render(structs...)
	if err != nil {
		return nil, err
	}
	if block != "" {
		sb.WriteString(block)
		sb.WriteString("\n")
	}

	sb.WriteString("(* Add the tags your program uses to its VAR_EXTERNAL block. The __Online,\n")
	sb.WriteString("   __LastBirthMs and __Rebirth tags are synthesized by the driver: interlock\n")
	sb.WriteString("   on __Online before trusting a site's values, and pulse __Rebirth to force\n")
	sb.WriteString("   a resync.\n\n")
	sb.WriteString(stgen.VarBlock("VAR_EXTERNAL", varFields(m)...))
	sb.WriteString("*)\n")
	return []byte(sb.String()), nil
}

// varFields is the VAR_EXTERNAL suggestion: every tag the manifest implies,
// sorted by name, with the ST type it lands as.
func varFields(m host.Manifest) []stgen.FieldDef {
	type entry struct {
		name string
		typ  stgen.Type
	}
	var all []entry
	for _, n := range m.Nodes {
		if t := n.OnlineTagName(); t != "" {
			all = append(all, entry{t, stgen.BOOL})
		}
		if t := n.BirthTagName(); t != "" {
			all = append(all, entry{t, stgen.LINT})
		}
		if t := n.RebirthTagName(); t != "" {
			all = append(all, entry{t, stgen.BOOL})
		}
		for _, dv := range n.Devices {
			if t := n.DeviceOnlineTagName(dv); t != "" {
				all = append(all, entry{t, stgen.BOOL})
			}
		}
	}
	for _, b := range m.Tags {
		// A member binding is a SCALAR tag carrying the LEAF member's type,
		// not the enclosing template's — declaring it as the struct would not
		// compile against the value the driver writes.
		typ := b.Type
		if b.Member != "" {
			if f, _, err := m.ResolveMember(b.Type, b.Member); err == nil {
				typ = f.Type
			}
		}
		all = append(all, entry{b.Name, stType(typ, b.ArrayLen)})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].name < all[j].name })
	out := make([]stgen.FieldDef, 0, len(all))
	for _, e := range all {
		out = append(out, stgen.Field(e.name, e.typ))
	}
	return out
}

// stType maps a manifest type name to an ST type: a Sparkplug datatype name
// to the IEC type of the same WIDTH (every integer width still lands in the
// IR's int64 and both floats in its float64 — the width is documentation, and
// what an operator reading the file expects to see), anything else to a
// reference to a generated TYPE.
//
// DateTime is LINT: Sparkplug timestamps are epoch MILLISECONDS, which
// overflow a DINT in 1970 + 25 days. TIME would be wrong — that is a
// duration, and these are absolute.
func stType(name string, arrayLen int) stgen.Type {
	var elem stgen.Type
	switch name {
	case "Boolean":
		elem = stgen.BOOL
	case "Int8":
		elem = stgen.SINT
	case "Int16":
		elem = stgen.INT
	case "Int32":
		elem = stgen.DINT
	case "Int64", "DateTime":
		elem = stgen.LINT
	case "UInt8":
		elem = stgen.USINT
	case "UInt16":
		elem = stgen.UINT
	case "UInt32":
		elem = stgen.UDINT
	case "UInt64":
		elem = stgen.ULINT
	case "Float":
		elem = stgen.REAL
	case "Double":
		elem = stgen.LREAL
	case "String", "Text", "UUID":
		elem = stgen.STRING
	default:
		elem = stgen.Ref(name)
	}
	if arrayLen > 0 {
		return stgen.ArrayOf(elem, 0, arrayLen-1)
	}
	return elem
}
