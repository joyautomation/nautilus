package runtime

import (
	"context"
	"fmt"
	"slices"
	"time"

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
// may not. The swap holds mainMu, so no scan sees half of it. The scan after
// it pushes every output once; unless Options.HoldOutputsOnSwap, d's
// start-up baseline is disarmed first (io.BaselineDisarmer), so that push
// is sent: the device starts from the current commands. Held, the push is
// d's baseline and it writes once an output changes.
//
// The caller gates d with the composite's gates (io.Multi.Gate) and starts
// it before (an unstarted driver is refused), and stops the returned driver
// after, outside any lock a scan holds; the returned driver is already
// gated closed. ctx bounds the wait for the scan in progress: a hung scan
// does not hang the apply.
func (r *Runtime) ReplaceDriver(ctx context.Context, name string, d nio.Driver) (nio.Driver, error) {
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
	if err := r.lockMain(ctx); err != nil {
		return nil, fmt.Errorf("replace driver %q: waiting for the scan in progress: %w", name, err)
	}
	defer r.mainMu.Unlock()
	if bd, ok := d.(nio.BaselineDisarmer); ok && !r.holdOnSwap {
		bd.DisarmBaseline()
	}
	prev, err := rp.Replace(name, d)
	if err != nil {
		return nil, err
	}
	r.outSent.Store(false) // the next scan hands the new driver every output
	return prev, nil
}

// lockMain takes mainMu, or gives up when ctx ends.
func (r *Runtime) lockMain(ctx context.Context) error {
	for !r.mainMu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	return nil
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
