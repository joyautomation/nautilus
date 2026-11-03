package sparkplug

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/internal/writable"
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
// below it. Without it every write is refused.
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
// p itself, or any dotted ancestor of it, matches a writable pattern (the
// rule internal/writable shares with opcua.writable).
func (n *Node) writeAllowed(p string) bool { return writable.Patterns(n.writable).Allows(p) }

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
// first member separator ("Motor1.START" → "Motor1").
func rootTag(name string) string {
	if i := strings.IndexByte(name, '.'); i >= 0 {
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
