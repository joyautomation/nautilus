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

// AnonymousWritesWarning is `naut check`'s look at opcua.anonymous-writes,
// given opcua.writable and opcua.endpoint ("" is DefaultEndpoint). Off, it
// is "". On with no writable list, it opens nothing, since the switch says
// who may write, not what: the server runs read-only, which is safe, but
// the author meant something else. On with a list, it is in effect, and
// the warning says so: informational on loopback, where only clients on
// this machine reach the endpoint, and stronger beyond it, where anyone
// who can reach the port may write (the start-up ERROR's case).
func AnonymousWritesWarning(on bool, patterns []string, endpoint string) string {
	if !on {
		return ""
	}
	if len(patterns) == 0 {
		return "opcua.anonymous-writes is on but opcua.writable lists nothing, so nothing is writable — anonymous-writes says who may write; list the tags under opcua.writable"
	}
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	tags := strings.Join(patterns, ", ")
	if anonymousLoopback(endpoint) {
		return fmt.Sprintf("opcua.anonymous-writes is on: any client that can reach %s (this machine only: it listens on loopback) can write the tags opcua.writable opens (%s) without a certificate — expected in a lab, never on a plant network", endpoint, tags)
	}
	return fmt.Sprintf("opcua.anonymous-writes is on and %s listens beyond loopback: ANY client that can reach it over the network can write the tags opcua.writable opens (%s) without a certificate, and only its address is logged — use it on a lab network only, never on a plant network; when the client runs on this machine, listen on 127.0.0.1 instead", endpoint, tags)
}

// anonymousLoopback reports whether endpoint listens on loopback alone,
// decided as Start decides it for its start-up line. An endpoint that does
// not parse is not known to be loopback.
func anonymousLoopback(endpoint string) bool {
	host, _, err := ParseEndpoint(endpoint)
	if err != nil {
		return false
	}
	listen, _ := endpointHosts(host)
	return loopbackHost(listen)
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
// The addresses are the ones a client browses (every tag, and every
// member of a struct or composite array below it) and the elements of
// every array of scalars, which a client reaches by node id though Browse
// does not list them: "Counts[2]" or "Counts[*]" names elements, and is
// no typo.
func WritableWarnings(rt *runtime.Runtime, patterns []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	ns := &namespace{rt: rt}
	var addrs, elems []member
	var collect func(m member)
	collect = func(m member) {
		addrs = append(addrs, m)
		if m.val.Kind == ir.TypeArray && !isObject(m) {
			elems = append(elems, elements(m)...)
		}
		for _, c := range children(m) {
			collect(c)
		}
	}
	for _, m := range ns.roots() {
		collect(m)
	}
	all := append(append([]member(nil), addrs...), elems...)
	var out []string
	for _, pat := range patterns {
		var matched, inputs []string
		elem := false // matches an element of an array of scalars outside the inputs
		for _, m := range all {
			if !writable.Matches(pat, m.addr) {
				continue
			}
			matched = append(matched, m.addr)
			root, _ := ir.SplitAddress(m.addr)
			if ns.isInput(root) {
				inputs = append(inputs, m.addr)
			} else if strings.HasSuffix(m.addr, "]") && m.val.Kind != ir.TypeArray && !isObject(m) {
				elem = true
			}
		}
		switch {
		case elem:
			// It names elements: scalars a client writes one at a time.
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
		for _, m := range all {
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

// elements are the members of an array of scalars, one per element, with
// the addresses a client writes them by: m.addr[lo] … m.addr[hi]. Browse
// lists none of them (children returns nil for such an array).
func elements(m member) []member {
	lo := 0
	var et *ir.Type
	if m.typ != nil && m.typ.Kind == ir.TypeArray {
		lo, et = m.typ.ArrLoBound, m.typ.Elem
	}
	out := make([]member, len(m.val.Arr))
	for i, e := range m.val.Arr {
		out[i] = member{addr: m.addr + "[" + strconv.Itoa(lo+i) + "]", val: e, typ: et}
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
