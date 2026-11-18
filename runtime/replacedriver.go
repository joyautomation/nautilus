package runtime

import (
	"fmt"
	"slices"

	nio "github.com/joyautomation/nautilus/io"
)

// replacer is a composite driver one of whose children can be swapped
// (io.Multi): the runtime's seam for an I/O configuration applied live
// (#706).
type replacer interface {
	Child(name string) (nio.Driver, bool)
	Replace(name string, d nio.Driver) (nio.Driver, error)
}

// ReplaceDriver swaps the child driver named name for d between two scans,
// without stopping the scan loop, and returns the driver it replaced (#706).
//
// The runtime's tags are fixed at New, so d must deliver and accept exactly
// the tags the old driver did: a binding or address may change, the tag set
// may not. The swap holds mainMu, so no scan sees half of it; the scan after
// it pushes every output once, so the new driver starts from the current
// commands rather than waiting for them to change.
//
// The caller starts d before (a Modbus or OPC UA driver reads as not
// connected until it is, and an unstarted one fails the whole read) and
// stops the returned driver after, neither under a lock a scan holds. Write
// gates already installed on the composite follow d by themselves.
func (r *Runtime) ReplaceDriver(name string, d nio.Driver) (nio.Driver, error) {
	rp, ok := r.driver.(replacer)
	if !ok {
		return nil, fmt.Errorf("replace driver %q: the runtime's driver (%T) is not a composite; a live swap needs the drivers composed (io.Multi)", name, r.driver)
	}
	old, ok := rp.Child(name)
	if !ok {
		return nil, fmt.Errorf("replace driver %q: no such driver", name)
	}
	if err := sameTags(old, d); err != nil {
		return nil, fmt.Errorf("replace driver %q: %w", name, err)
	}
	r.mainMu.Lock()
	defer r.mainMu.Unlock()
	prev, err := rp.Replace(name, d)
	if err != nil {
		return nil, err
	}
	r.outSent.Store(false) // the next scan hands the new driver every output
	return prev, nil
}

// tagNames is what a driver declares it owns (io.Multi requires it).
type tagNames interface {
	InputNames() []string
	OutputNames() []string
}

// sameTags refuses a replacement whose input or output tags differ from the
// driver it replaces: the runtime's input and output sets were fixed at New.
func sameTags(old, d nio.Driver) error {
	o, ok1 := old.(tagNames)
	n, ok2 := d.(tagNames)
	if !ok1 || !ok2 {
		return fmt.Errorf("both drivers must declare their tags (InputNames/OutputNames)")
	}
	if a, b := sorted(o.InputNames()), sorted(n.InputNames()); !slices.Equal(a, b) {
		return fmt.Errorf("the replacement delivers inputs %v, not %v: the tag set cannot change live (restart, or activate a program)", b, a)
	}
	if a, b := sorted(o.OutputNames()), sorted(n.OutputNames()); !slices.Equal(a, b) {
		return fmt.Errorf("the replacement accepts outputs %v, not %v: the tag set cannot change live (restart, or activate a program)", b, a)
	}
	return nil
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	slices.Sort(out)
	return out
}
