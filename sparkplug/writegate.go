package sparkplug

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/internal/writable"
	"github.com/joyautomation/nautilus/lang/ir"
)

// The write gate (docs/design/edge-collector.md §6). An edge node is
// read-only by default: NCMD/DCMD writes are refused unless the tag they
// name is opted in with WithWritable, and Node Control/Rebirth is the one
// command always honoured. Anyone who can publish to the broker can send a
// command, so a node that wrote whatever it was sent would let any host on
// the broker write through to a controller.
//
// A command must also reach the tag where the node publishes it: a DCMD
// writes only the tags its device births, an NCMD only the node-level ones.
// A host addresses a metric by the topic it was born on, so a command that
// names a tag on another device is a host bug or a probe, not a write.

// WithWritable opens command writes for the tags matching any of patterns —
// globs (path.Match) over the tag name, or over a UDT member's dotted path
// ("Motor1.START", "Pump*.SpeedSP"). A pattern that matches a tag opens all
// of its members; one that matches a member opens that member and anything
// below it. Patterns match the declared spelling of the tag and member,
// whatever case a command names them in. Without it every write is refused.
func WithWritable(patterns ...string) Option {
	return func(n *Node) { n.writable = append(n.writable, patterns...) }
}

// validateWritable rejects a malformed pattern at New, rather than letting
// it silently match nothing.
func validateWritable(patterns []string) error {
	if err := writable.Patterns(patterns).Validate(); err != nil {
		return fmt.Errorf("sparkplug: %w", err)
	}
	return nil
}

// writeAllowed reports whether a command may write the tag or member at p:
// its declared spelling (canonicalPath), or any dotted ancestor of that,
// matches a writable pattern (the rule internal/writable shares with
// opcua.writable). The patterns are matched against the declared name, not
// the host's spelling: tag and member names are case-insensitive, so a
// host's "speedSP" writes SpeedSP, and matching what it sent let a
// respelling open a tag the patterns keep closed, or close one they open
// (#619). OPC UA matches its canonical address the same way.
func (n *Node) writeAllowed(p string) bool {
	return writable.Patterns(n.writable).Allows(n.canonicalPath(p))
}

// canonicalPath is the declared spelling of the tag or member a command
// names, in any case: the tag as the store holds it (or a declared output
// no scan has written yet, under the name writeCommand creates it with),
// and each member as its struct type declares it ("motor1.start" →
// "Motor1.START"). An index is kept as sent. A part that resolves to
// nothing is kept as sent, so an unknown name is gated, and refused,
// exactly as before. That fallback is safe only because the write resolves
// names the same way and refuses what this does not resolve: tags and
// declared outputs are both folded with ir.NameKey, and writeCommand
// creates no tag but a declared output, under its declared name.
func (n *Node) canonicalPath(p string) string {
	tags := n.rt.Tags()
	// A tag whose own name contains a dot wins over member resolution, as
	// it does in the store's write (Tags.SetPath).
	if c, ok := tags.Canonical(p); ok {
		return c
	}
	root, segs := ir.SplitAddress(p)
	val, typ := ir.Value{}, tags.TypeOf(root)
	if c, ok := tags.Canonical(root); ok {
		root = c
		if v, err := tags.ReadGlobal(c); err == nil {
			val = v
		}
	} else if d, ok := tags.DeclaredOutput(root); ok {
		root = d
	}
	for i, seg := range segs {
		if strings.HasPrefix(seg, "[") {
			// An element: what is below it is the element type's. Index
			// writes are refused before the gate today; resolving past one
			// keeps a member below it in its declared case all the same.
			// The index itself is kept as sent, though PathSegments and the
			// store read "[01]" and "[+1]" as "[1]": if element writes are
			// ever enabled, canonicalise the index here too (to the
			// element's decimal index), or a pattern naming `Pumps\[1\]`
			// could be dodged with "Pumps[01]".
			if val.Kind == ir.TypeArray && len(val.Arr) > 0 {
				val = val.Arr[0]
			} else {
				val = ir.Value{}
			}
			if typ != nil && typ.Kind == ir.TypeArray {
				typ = typ.Elem
			} else {
				typ = nil
			}
			continue
		}
		def := structOf(val, typ)
		fi, ok := def.FieldOf(seg)
		if !ok || fi >= len(def.Fields) {
			break // an unknown member: it and what is below it kept as sent
		}
		segs[i] = def.Fields[fi].Name
		if val.Kind == ir.TypeStruct && fi < len(val.Fld) {
			val = val.Fld[fi]
		} else {
			val = ir.Value{}
		}
		typ = def.Fields[fi].Type
	}
	return ir.JoinPath(root, segs)
}

// structOf is the struct definition a member name resolves against: the
// stored value's, else the declared type's; nil when neither is a struct.
func structOf(v ir.Value, t *ir.Type) *ir.StructDef {
	if v.Kind == ir.TypeStruct && v.Struct != nil {
		return v.Struct
	}
	if t != nil && t.Kind == ir.TypeStruct {
		return t.Struct
	}
	return nil
}

// writableTemplate prunes a Template command metric to the members a write
// may reach, at any depth, and returns the dotted paths it dropped. The
// result has no members when nothing in it was writable. The input is not
// modified.
func (n *Node) writableTemplate(p string, m Metric) (Metric, []string) {
	if n.writeAllowed(p) {
		return m, nil
	}
	t, ok := m.Value.(*Template)
	if !ok || t == nil {
		return Metric{}, []string{p}
	}
	var kept []Metric
	var refused []string
	for _, member := range t.Metrics {
		mp := p + "." + member.Name
		if _, nested := member.Value.(*Template); nested {
			pruned, r := n.writableTemplate(mp, member)
			refused = append(refused, r...)
			if pt, _ := pruned.Value.(*Template); pt != nil && len(pt.Metrics) > 0 {
				kept = append(kept, pruned)
			}
			continue
		}
		if n.writeAllowed(mp) {
			kept = append(kept, member)
		} else {
			refused = append(refused, mp)
		}
	}
	out := *t
	out.Metrics = kept
	m.Value = &out
	return m, refused
}

// rootTag is the tag a command metric name addresses: the name up to its
// first member or element separator ("Motor1.START" → "Motor1",
// "Levels[1]" → "Levels"). Cutting only at "." let an element's name miss
// the device-ownership table, so a node-level command could write a
// device's tag (#506 review).
func rootTag(name string) string {
	if i := strings.IndexAny(name, ".["); i >= 0 {
		return name[:i]
	}
	return name
}

// commandDevice is the device a command topic addresses: "" for an NCMD
// (spBv1.0/<group>/NCMD/<node>), the device id for a DCMD
// (spBv1.0/<group>/DCMD/<node>/<device>).
func commandDevice(topic string) string {
	parts := strings.Split(topic, "/")
	if len(parts) == 5 {
		return parts[4]
	}
	return ""
}

// refuseCommand counts a refused write and logs it once per tag and reason,
// so a host retrying every scan does not flood the log.
func (n *Node) refuseCommand(reason, tag string, args ...any) {
	n.mu.Lock()
	n.cmdRefused++
	n.mu.Unlock()
	n.warnCommandOnce("refused:"+reason+":"+tag,
		"sparkplug: command write refused — "+reason, append([]any{"tag", tag}, args...)...)
}
