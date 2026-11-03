package server

import (
	"fmt"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/internal/writable"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// WritableWarnings is `naut check`'s look at opcua.writable against a
// compiled runtime: a pattern that matches no tag or member (a typo, or a
// tag since renamed), one that matches only inputs (never writable: the
// driver replaces an input before every scan), and an output it opens that
// a program writes (a client's value lasts until the program's next
// commit). Malformed patterns are Load's error, not a warning here.
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

// list names up to three addresses.
func list(addrs []string) string {
	if len(addrs) > 3 {
		return strings.Join(addrs[:3], ", ") + fmt.Sprintf(" and %d more", len(addrs)-3)
	}
	return strings.Join(addrs, ", ")
}
