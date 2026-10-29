package plcnext

// channel.go is what the manifest's module list says about a binding: the
// module whose bytes it lands in, which channel of that module a digital
// binding is, and whether a ch:N runs off the end of its module.

import "fmt"

// moduleAt finds the module whose span of the image holds the binding's
// offset, walking the modules in bus order and summing their byte counts
// for the binding's direction; start is the module's first byte.
func (m Manifest) moduleAt(b Binding) (Module, int, bool) {
	off := 0
	for _, mod := range m.Modules {
		n := mod.bytes(b.Dir)
		if b.Offset >= off && b.Offset < off+n {
			return mod, off, true
		}
		off += n
	}
	return Module{}, 0, false
}

func (mod Module) bytes(dir string) int {
	if dir == DirOut {
		return mod.Out
	}
	return mod.In
}

// channelInModule refuses a ch:N that reaches past the module its offset
// is in — ch:17 on a DI16 lands in the next module's byte. Only when the
// manifest lists its modules; bit:N is left as it always was.
func (m Manifest) channelInModule(b Binding, f Format) error {
	if f.Kind != "ch" {
		return nil
	}
	mod, start, ok := m.moduleAt(b)
	if !ok {
		return nil
	}
	n := mod.bytes(b.Dir)
	if b.Offset+f.Bytes() <= start+n {
		return nil
	}
	return fmt.Errorf("tag %s: %s at offset %d reaches byte %d, past %s (slot %d, %s bytes %d..%d, channels 1..%d from offset %d)",
		b.Name, f, b.Offset, b.Offset+f.Bytes()-1, mod.Part, mod.Slot, b.Dir, start, start+n-1, (start+n-b.Offset)*8, b.Offset)
}

// describe is a tag's description when the manifest gives none: the
// module and, for a digital binding, the module's own channel number
// (whichever spelling bound it), else the byte and format.
func (m Manifest) describe(b Binding) string {
	mod, start, ok := m.moduleAt(b)
	if !ok {
		return ""
	}
	f := b.format()
	if f.Digital() {
		i, bit := f.bitAt(m.ByteOrder)
		if abs := b.Offset + i; abs < start+mod.bytes(b.Dir) {
			what := "input"
			if b.Dir == DirOut {
				what = "output"
			}
			return fmt.Sprintf("%s (slot %d) %s channel %02d", mod.Part, mod.Slot, what, (abs-start)*8+bit+1)
		}
	}
	return fmt.Sprintf("%s (slot %d) %s byte %d %s", mod.Part, mod.Slot, b.Dir, b.Offset, f)
}
