package server

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/internal/writable"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// AnonymousWritesWarning is `naut check`'s look at opcua.anonymous-writes:
// on with no opcua.writable, it opens nothing, since the switch says who
// may write, not what. A warning, not an error: the server runs read-only
// as written, which is safe, but the author meant something else.
func AnonymousWritesWarning(on bool, patterns []string) string {
	if !on || len(patterns) > 0 {
		return ""
	}
	return "opcua.anonymous-writes is on but opcua.writable lists nothing, so nothing is writable — anonymous-writes says who may write; list the tags under opcua.writable"
}

// WritableWarnings is `naut check`'s look at opcua.writable against a
// compiled runtime: a pattern that matches no tag or member (a typo, or a
// tag since renamed), one that matches only inputs (never writable: the
// driver replaces an input before every scan), one that opens no scalar
// but arrays of scalars (their elements are writable one at a time by node
// id, ns=1;s=Trend[0]; a whole-array or IndexRange write is not, until
// array writes land), one that opens only Objects with nothing writable
// below them, and an output it opens that a program writes (a client's
// value lasts until the program's next commit). Malformed patterns are Load's error, not a warning here.
//
// The addresses are the ones a client browses: every tag, and every
// member of a struct or composite array below it.
func WritableWarnings(rt *runtime.Runtime, patterns []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	ns := &namespace{rt: rt}
	var addrs []member
	var collect func(m member)
	collect = func(m member) {
		addrs = append(addrs, m)
		for _, c := range children(m) {
			collect(c)
		}
	}
	for _, m := range ns.roots() {
		collect(m)
	}
	var out []string
	for _, pat := range patterns {
		var matched, inputs []string
		for _, m := range addrs {
			if !writable.Matches(pat, m.addr) {
				continue
			}
			matched = append(matched, m.addr)
			if root, _ := ir.SplitAddress(m.addr); ns.isInput(root) {
				inputs = append(inputs, m.addr)
			}
		}
		switch {
		case len(matched) == 0:
			out = append(out, fmt.Sprintf("opcua.writable: %q matches no tag or member — a typo, or a tag that no longer exists", pat))
		case len(inputs) == len(matched):
			out = append(out, fmt.Sprintf("opcua.writable: %q matches only inputs (%s), which are never writable — the driver replaces an input before every scan; open the setpoint the logic reads instead", pat, list(inputs)))
		case !opensScalar(ns, writable.Patterns{pat}, addrs):
			if arrs := scalarArrays(ns, writable.Patterns{pat}, addrs); len(arrs) > 0 {
				out = append(out, fmt.Sprintf("opcua.writable: %q opens arrays (%s): a client writes their elements one at a time, by node id (ns=1;s=%s); a whole-array or IndexRange write is refused until array writes land", pat, list(addrsOf(arrs)), firstElement(arrs[0])))
				break
			}
			out = append(out, fmt.Sprintf("opcua.writable: %q opens only Objects with no scalar member (%s), so nothing a client can write", pat, list(matched)))
		}
	}
	// Outputs a program writes: open to a client, overwritten by the logic.
	written := map[string]bool{}
	for name := range rt.GlobalUses().Written {
		written[ir.NameKey(name)] = true
	}
	ps := writable.Patterns(patterns)
	var outs []string
	for _, name := range rt.Outputs() {
		if !written[ir.NameKey(name)] {
			continue
		}
		for _, m := range addrs {
			if root, _ := ir.SplitAddress(m.addr); ir.NameKey(root) == ir.NameKey(name) && ps.Allows(m.addr) {
				outs = append(outs, name)
				break
			}
		}
	}
	sort.Strings(outs)
	for _, name := range outs {
		out = append(out, fmt.Sprintf("opcua.writable opens output %q, which a program writes — a client's value lasts only until the program's next commit", name))
	}
	return out
}

// opensScalar reports whether ps opens any scalar Variable outside the
// inputs: something a client can write whole. A pattern on a UDT opens its
// scalar members; one on an array of scalars opens its elements, which are
// not browsed (scalarArrays).
func opensScalar(ns *namespace, ps writable.Patterns, addrs []member) bool {
	for _, m := range addrs {
		if isObject(m) || m.val.Kind == ir.TypeArray || !ps.Allows(m.addr) {
			continue
		}
		if root, _ := ir.SplitAddress(m.addr); !ns.isInput(root) {
			return true
		}
	}
	return false
}

// scalarArrays lists the arrays of scalars ps opens outside the inputs. An
// array of scalars is one Variable, refused whole, but each element is a
// Variable of its own by node id (Inputs[17]), writable one at a time.
func scalarArrays(ns *namespace, ps writable.Patterns, addrs []member) []member {
	var out []member
	for _, m := range addrs {
		if isObject(m) || m.val.Kind != ir.TypeArray || !ps.Allows(m.addr) {
			continue
		}
		if root, _ := ir.SplitAddress(m.addr); !ns.isInput(root) {
			out = append(out, m)
		}
	}
	return out
}

// firstElement is the address of an array's first element, counted from
// its declared lower bound.
func firstElement(m member) string {
	lo := 0
	if m.typ != nil && m.typ.Kind == ir.TypeArray {
		lo = m.typ.ArrLoBound
	}
	return m.addr + "[" + strconv.Itoa(lo) + "]"
}

func addrsOf(ms []member) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.addr
	}
	return out
}

// list names up to three addresses.
func list(addrs []string) string {
	if len(addrs) > 3 {
		return strings.Join(addrs[:3], ", ") + fmt.Sprintf(" and %d more", len(addrs)-3)
	}
	return strings.Join(addrs, ", ")
}
