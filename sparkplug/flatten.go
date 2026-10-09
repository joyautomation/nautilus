package sparkplug

import (
	"reflect"
	"strings"

	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// Flattened UDTs. By default a struct tag publishes as one Sparkplug Template
// metric, with its type's definition in NBIRTH. Templates are standard
// Sparkplug B, but not every host reads them: one that skips Template metrics
// loses every UDT tag, and those tend to be the tags that matter most (a
// motor's Running, Fault, Speed). WithFlattenUDTs publishes each UDT member as
// a plain metric of its own instead, named by its path with "/" between the
// levels — the folder convention Sparkplug hosts already display as a tree:
//
//	P101            Template (Motor)       →  P101/Running   Boolean
//	                                          P101/Speed     Double
//	                                          P101/Valve/Pos Double  (nested UDT)
//
// No Template definitions are published. Report by exception still decides per tag, so a
// class or deadband assigned to P101 governs all of its members; what changes
// is the message: a data message carries only the members that moved since
// the last publish (every member when the heartbeat forces one).
//
// Commands address members the same way: an NCMD for "P101/Speed" writes
// P101.Speed.

// WithFlattenUDTs publishes UDT (struct) tags as one metric per member rather
// than as Sparkplug Templates. See flatten.go.
func WithFlattenUDTs() Option {
	return func(n *Node) { n.flatten = true }
}

// flattenMetric expands a Template instance metric into one metric per leaf
// member, at any depth, named parent/member. Each leaf keeps its own datatype,
// value and properties (birth documentation attached by attachMeta) and takes
// the parent's timestamp and historical flag. Any other metric is returned as
// the only element.
func flattenMetric(m Metric) []Metric {
	t, ok := m.Value.(*Template)
	if !ok || t == nil || m.Datatype != spb.DataType_Template {
		return []Metric{m}
	}
	var out []Metric
	for _, member := range t.Metrics {
		leaf := member
		leaf.Name = m.Name + "/" + member.Name
		leaf.Timestamp = m.Timestamp
		leaf.IsHistorical = m.IsHistorical
		out = append(out, flattenMetric(leaf)...)
	}
	return out
}

// changedLeaves picks the leaves of cur to put in a data message: those whose
// value differs from the same leaf in prev, the last published value. With no
// prev (the first publish since a birth) or no difference at all (the
// heartbeat forced this publish), every leaf goes.
func changedLeaves(cur, prev []Metric) []Metric {
	if prev == nil {
		return cur
	}
	before := make(map[string]Metric, len(prev))
	for _, p := range prev {
		before[p.Name] = p
	}
	var out []Metric
	for _, c := range cur {
		p, ok := before[c.Name]
		if !ok || p.IsNull != c.IsNull || p.Datatype != c.Datatype || !reflect.DeepEqual(p.Value, c.Value) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return cur
	}
	return out
}

// memberPath turns a flattened command metric name back into the dotted path
// the tag store uses: "P101/Speed" → "P101.Speed". Tag and member names are
// identifiers, so a "/" can only be a level separator.
func memberPath(name string) string {
	return strings.ReplaceAll(name, "/", ".")
}

// expand is a birth metric as it goes on the wire: itself, or its flattened
// members when the node flattens UDTs.
func (n *Node) expand(m Metric) []Metric {
	if !n.flatten {
		return []Metric{m}
	}
	return flattenMetric(m)
}
