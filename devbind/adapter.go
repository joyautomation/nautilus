package devbind

import (
	"sort"

	nio "github.com/joyautomation/nautilus/io"
)

// structs is the input side every device adapter shares: a driver
// delivers each device's points under internal member names
// ("CONV.StatusWord") and the adapter hands the runtime one struct per
// device instead.
type structs struct {
	asm    []*assembler
	member map[string]int // internal member name -> index into asm
	inputs []string
}

// newStructs adapts a driver's inputs (names) for devs.
func newStructs(devs []*device, names []string) structs {
	x := structs{member: map[string]int{}}
	for _, dev := range devs {
		a := newAssembler(dev)
		for _, m := range a.members {
			x.member[m] = len(x.asm)
		}
		x.asm = append(x.asm, a)
	}
	seen := map[string]bool{}
	for _, n := range names {
		if i, ok := x.member[n]; ok {
			n = x.asm[i].dev
		}
		if !seen[n] {
			seen[n] = true
			x.inputs = append(x.inputs, n)
		}
	}
	return x
}

// newAssembly is a fresh copy of the input side, sharing no state with it.
func (x *structs) newAssembly() func(nio.Values) {
	asm := make([]*assembler, len(x.asm))
	for i, a := range x.asm {
		asm[i] = &assembler{dev: a.dev, def: a.def, members: a.members, derived: a.derived, got: make([]bool, len(a.members))}
	}
	return func(vals nio.Values) {
		for _, a := range asm {
			a.take(vals)
		}
	}
}

// clear takes the device structs out of the runtime's map: a driver keeps
// dst to exactly its own keys, and takes a slower path when it finds others.
func (x *structs) clear(dst nio.Values) {
	for _, a := range x.asm {
		delete(dst, a.dev)
	}
}

func (x *structs) assemble(vals nio.Values) {
	for _, a := range x.asm {
		a.take(vals)
	}
}

// inputType is a device's struct type.
func (x *structs) inputType(name string) (string, bool) {
	for _, a := range x.asm {
		if a.dev == name {
			return a.def.Name, true
		}
	}
	return "", false
}

func (x *structs) quality(in map[string]nio.Quality) map[string]nio.Quality {
	return rollup(x.asm, x.member, in)
}

// connections names the device tags in place of their members.
func (x *structs) connections(cs []nio.Connection) []nio.Connection {
	for i := range cs {
		seen := map[string]bool{}
		var tags []string
		for _, t := range cs[i].Inputs {
			if j, ok := x.member[t]; ok {
				t = x.asm[j].dev
			}
			if !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
		sort.Strings(tags)
		cs[i].Inputs = tags
	}
	return cs
}
