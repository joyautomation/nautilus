// sites.go is the OFFLINE half of the importer: generation from a committed
// site list instead of from a live broker.
//
// `nautilus sparkplug import --broker ...` needs every site to be online,
// birthing, and reachable from wherever the import runs. That is fine for one
// site and wrong for sixty: the central project has to be buildable in CI,
// reviewable before the field work happens, and regenerable when a site is
// down. So the same generator accepts a `--sites` file — a plain YAML list of
// nodes, devices, metrics and template shapes — and produces byte-identical
// output to what the broker path would have produced from the corresponding
// births.
//
// That is also the seam a project generator writes to: the Pomona edge
// projects already know their tag lists, so the central manifest can be
// generated from the same source of truth rather than sampled off a wire.
//
// The file's convention matches the manifest's: NO yaml struct tags, so keys
// are the lowercased Go field names, decoded with KnownFields(true) — a typo
// is an error, not a silently dropped site.
//
//	# sites.yaml
//	group: PomonaWRD
//	types:
//	  - name: Motor
//	    fields:
//	      - {name: Speed, type: Double}
//	      - {name: Run,   type: Boolean}
//	sites:
//	  - node: W6                       # the Sparkplug edge_node_id
//	    prefix: W6                     # optional; default sanitize(node)
//	    metrics:                       # node-level metrics (NDATA)
//	      - {name: Well/Level, type: Double}
//	      - {name: Pump1,      type: Motor}          # a template instance
//	    devices:
//	      - device: PLC1
//	        metrics:
//	          - {name: Pump/Run,     type: Boolean}
//	          - {name: Pump/SpeedSP, type: Double, writable: true, init: 0.0}
//
// `type:` is a Sparkplug datatype name (Boolean, Int8..UInt64, DateTime,
// Float, Double, String, Text, UUID) or one of the `types:` entries.

package codegen

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/joyautomation/nautilus/sparkplug/host"
	"gopkg.in/yaml.v3"
)

// SitesFile is the on-disk shape of a --sites file.
type SitesFile struct {
	// Group is the Sparkplug group_id every site lives under. A site may
	// override it, but the generated manifest still holds exactly one group.
	Group string
	// Types are the Template (UDT) shapes the metrics reference, shared by
	// every site in the file.
	Types []host.TypeDef
	// Sites are the edge nodes.
	Sites []SiteSpec
}

// SiteSpec is one edge node: what a `--sites` file lists, and what
// FromSites consumes. It is deliberately the shape a project generator can
// emit without knowing anything about Sparkplug wire formats.
type SiteSpec struct {
	// Group is this site's group_id; empty means the file's group.
	Group string
	// Node is the Sparkplug edge_node_id, verbatim.
	Node string
	// Prefix overrides the tag-name prefix for this site's metrics; empty
	// means the Options prefix policy (default: sanitize(Node)).
	Prefix string
	// Types are template shapes this site contributes. ParseSites copies the
	// file-level types into every site, so FromSites needs nothing else.
	Types []host.TypeDef
	// Metrics are the node-level metrics (the ones an NBIRTH/NDATA carries).
	Metrics []MetricSpec
	// Devices are the Sparkplug devices under this node.
	Devices []DeviceSpec
}

// DeviceSpec is one device and its metrics.
type DeviceSpec struct {
	Device  string
	Metrics []MetricSpec
}

// MetricSpec is one metric: its verbatim Sparkplug name and the type it
// carries.
type MetricSpec struct {
	// Name is the Sparkplug metric name, verbatim ("Well/Level").
	Name string
	// Type is a Sparkplug datatype name or a types: entry.
	Type string
	// Writable marks the metric an output: writes go back as NCMD/DCMD.
	//
	// `true` writes the metric AS A WHOLE, which only a scalar metric can be
	// — a Template metric written whole would clobber every member the edge
	// is driving. A Template's controls are named individually instead, as a
	// list of dotted member paths:
	//
	//	- {name: Pump/SpeedSP, type: Double, writable: true}
	//	- {name: Motor1,       type: Motor,  writable: [START, LVL.CTL1HSP]}
	//
	// Each member becomes its own scalar output tag, and a write publishes a
	// partial template carrying only that member.
	Writable any
	// Init is a whole-metric writable's initial value; nil means the type
	// zero. Member bindings take the member's own zero (they are many, and
	// one init: could not say which).
	Init any
}

// writableSpec normalises a MetricSpec's writable: value into "the whole
// metric" or "these member paths".
func writableSpec(node, name string, v any) (bool, []string, error) {
	bad := func(hint string) error {
		return fmt.Errorf("codegen: site %q metric %q: writable: %s — want `true` for a scalar "+
			"metric, or a list of member paths for a Template (writable: [START, LVL.HSP])", node, name, hint)
	}
	switch x := v.(type) {
	case nil:
		return false, nil, nil
	case bool:
		return x, nil, nil
	case []any:
		members := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok || s == "" {
				return false, nil, bad(fmt.Sprintf("member %v is not a path", e))
			}
			members = append(members, s)
		}
		if len(members) == 0 {
			return false, nil, bad("the member list is empty")
		}
		return false, members, nil
	}
	return false, nil, bad(fmt.Sprintf("%v", v))
}

// ParseSites decodes a --sites file and folds its file-level group and types
// into every site, so FromSites needs only the returned slice.
func ParseSites(data []byte) ([]SiteSpec, error) {
	var f SitesFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // a typo is an error, not a silently dropped site
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("codegen: sites file is empty")
		}
		return nil, fmt.Errorf("codegen: sites file: %w", err)
	}
	if len(f.Sites) == 0 {
		return nil, fmt.Errorf("codegen: sites file declares no sites:")
	}
	out := make([]SiteSpec, 0, len(f.Sites))
	for i, s := range f.Sites {
		if s.Node == "" {
			return nil, fmt.Errorf("codegen: sites[%d]: node is required", i)
		}
		if s.Group == "" {
			s.Group = f.Group
		}
		s.Types = append(append([]host.TypeDef(nil), f.Types...), s.Types...)
		out = append(out, s)
	}
	return out, nil
}

// FromSites builds a manifest from a site list — the offline twin of
// FromBirths, taking the same Options (filters, layout, prefix policy) so the
// two paths cannot diverge in how they name tags.
func FromSites(sites []SiteSpec, opts Options) (host.Manifest, error) {
	if _, err := opts.layout(); err != nil {
		return host.Manifest{}, err
	}

	group := ""
	types := map[string]host.TypeDef{}
	var typeOrder []string
	sitesByNode := map[string]*site{}
	var order []string

	for i, s := range sites {
		if s.Node == "" {
			return host.Manifest{}, fmt.Errorf("codegen: sites[%d]: node is required", i)
		}
		if !selects(s.Node, opts.Nodes) {
			continue
		}
		switch {
		case group == "":
			group = s.Group
		case s.Group != "" && s.Group != group:
			return host.Manifest{}, fmt.Errorf(
				"codegen: sites span groups %q and %q — a manifest describes one group", group, s.Group)
		}
		for _, td := range s.Types {
			if td.Name == "" {
				return host.Manifest{}, fmt.Errorf("codegen: site %q declares a type with no name", s.Node)
			}
			prev, seen := types[td.Name]
			if !seen {
				types[td.Name] = td
				typeOrder = append(typeOrder, td.Name)
				continue
			}
			if !sameFields(prev, td) {
				return host.Manifest{}, fmt.Errorf(
					"codegen: type %q is declared with different fields by more than one site (at %q)",
					td.Name, s.Node)
			}
		}

		st, ok := sitesByNode[s.Node]
		if !ok {
			st = &site{edge: s.Node, devices: map[string][]metric{}, prefix: s.Prefix}
			sitesByNode[s.Node] = st
			order = append(order, s.Node)
		}
		ms, err := siteMetrics(s.Node, "", s.Metrics, opts)
		if err != nil {
			return host.Manifest{}, err
		}
		st.metrics = append(st.metrics, ms...)
		for _, dv := range s.Devices {
			if dv.Device == "" {
				return host.Manifest{}, fmt.Errorf("codegen: site %q declares a device with no id", s.Node)
			}
			if _, seen := st.devices[dv.Device]; !seen {
				st.deviceIDs = append(st.deviceIDs, dv.Device)
			}
			dms, err := siteMetrics(s.Node, dv.Device, dv.Metrics, opts)
			if err != nil {
				return host.Manifest{}, err
			}
			st.devices[dv.Device] = append(st.devices[dv.Device], dms...)
		}
	}
	if len(order) == 0 {
		return host.Manifest{}, fmt.Errorf("codegen: no site matched --nodes %s", patternsText(opts.Nodes))
	}
	sort.Strings(order)

	m := host.Manifest{Group: group}
	for _, name := range typeOrder {
		m.Types = append(m.Types, types[name])
	}
	if err := buildNodes(&m, sitesByNode, order, opts); err != nil {
		return host.Manifest{}, err
	}
	if err := m.Validate(); err != nil {
		return host.Manifest{}, fmt.Errorf("codegen: generated manifest is invalid: %w", err)
	}
	return m, nil
}

// siteMetrics filters and normalises one site's metric list.
func siteMetrics(node, device string, ms []MetricSpec, opts Options) ([]metric, error) {
	var out []metric
	for i, mm := range ms {
		switch {
		case mm.Name == "":
			return nil, fmt.Errorf("codegen: site %q metrics[%d]: name is required", node, i)
		case mm.Type == "":
			return nil, fmt.Errorf("codegen: site %q metric %q: type is required", node, mm.Name)
		}
		if isProtocolMetric(mm.Name) {
			opts.skip(fmt.Sprintf("%s: %q is Sparkplug plumbing, not process data",
				qualify(device, mm.Name), mm.Name))
			continue
		}
		if !selects(qualify(device, mm.Name), opts.Metrics) && !selects(mm.Name, opts.Metrics) {
			continue
		}
		whole, members, err := writableSpec(node, mm.Name, mm.Writable)
		if err != nil {
			return nil, err
		}
		if len(members) > 0 && mm.Init != nil {
			return nil, fmt.Errorf("codegen: site %q metric %q: init: applies to a whole-metric "+
				"writable, not to a member list — each member tag takes its member's zero", node, mm.Name)
		}
		out = append(out, metric{
			name:     mm.Name,
			typ:      mm.Type,
			writable: whole,
			members:  members,
			init:     mm.Init,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// sameFields compares two type declarations of the same name.
func sameFields(a, b host.TypeDef) bool {
	if len(a.Fields) != len(b.Fields) {
		return false
	}
	for i := range a.Fields {
		if a.Fields[i] != b.Fields[i] {
			return false
		}
	}
	return true
}
