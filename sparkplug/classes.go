package sparkplug

import "path"

// Publish classes are the Sparkplug analogue of the EtherNet/IP driver's scan
// classes: named groups of metrics, each with its own report-by-exception
// rule. A metric lands in the default class unless a WithMetricClass glob
// assigns it elsewhere; the reserved NoPublish class keeps a tag out of the
// birth and data entirely.

// DefaultClass is the class of every metric not assigned another. Its RBE is
// WithDefaultRBE (publish-on-change if unset).
const DefaultClass = "default"

// NoPublish excludes matching tags from Sparkplug entirely — not in the
// birth, never published. For internal/scratch tags you don't want on the bus.
const NoPublish = "none"

// classAssignment maps glob patterns to a publish class, applied in option
// order (last match wins).
type classAssignment struct {
	class    string
	patterns []string
}

// WithDefaultRBE sets the report-by-exception rule for the default class.
func WithDefaultRBE(r RBE) Option {
	return func(n *Node) { n.classRBE[DefaultClass] = r }
}

// WithPublishClass defines (or redefines) a publish class and its RBE rule.
func WithPublishClass(name string, r RBE) Option {
	return func(n *Node) { n.classRBE[name] = r }
}

// WithMetricClass assigns metrics to a publish class by glob patterns matched
// against the tag name ("Motor*", "*_Alarm", "Line1/*"). Later assignments
// override earlier ones. Use the NoPublish class to exclude tags.
func WithMetricClass(class string, patterns ...string) Option {
	return func(n *Node) {
		n.assignments = append(n.assignments, classAssignment{class: class, patterns: patterns})
	}
}

// classOf resolves a tag's publish class from the assignments (last match
// wins), defaulting to DefaultClass.
func (n *Node) classOf(tag string) string {
	class := DefaultClass
	for _, a := range n.assignments {
		for _, p := range a.patterns {
			if ok, _ := path.Match(p, tag); ok {
				class = a.class
				break
			}
		}
	}
	return class
}

// rbeFor returns the RBE rule for a tag and whether it is published at all.
func (n *Node) rbeFor(tag string) (RBE, bool) {
	class := n.classOf(tag)
	if class == NoPublish {
		return RBE{}, false
	}
	r, ok := n.classRBE[class]
	if !ok {
		// A metric names an undefined class — treat as default rather than
		// dropping it silently; validated at New so this is belt-and-suspenders.
		r = n.classRBE[DefaultClass]
	}
	return r, true
}
