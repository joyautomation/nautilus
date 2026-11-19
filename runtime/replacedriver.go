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
	prev, _, err := r.replaceDriver(ctx, name, d, false)
	return prev, err
}

// ReplaceDriverObserved is ReplaceDriver, and reports on the returned
// channel (one value, buffered) the outputs the first push after the swap
// hands d: its own output tags, as handed. The watch is armed under the
// same lock as the swap, so no earlier push answers it. It is how a staged
// apply reports what it actually commanded (#706); what d then sends of it
// is d's rule (a held baseline, its write gate).
func (r *Runtime) ReplaceDriverObserved(ctx context.Context, name string, d nio.Driver) (nio.Driver, <-chan nio.Values, error) {
	return r.replaceDriver(ctx, name, d, true)
}

// pushWatch asks for the outputs the next successful push hands over.
type pushWatch struct {
	names []string
	ch    chan nio.Values
}

// deliverPush answers every pushWatch with the outputs just handed over.
// It runs in the scan, under mainMu, as the swaps that arm them do.
func (r *Runtime) deliverPush(out nio.Values) {
	if len(r.pushWatches) == 0 {
		return
	}
	for _, w := range r.pushWatches {
		got := make(nio.Values, len(w.names))
		for _, n := range w.names {
			if v, ok := out[n]; ok {
				got[n] = v
			}
		}
		w.ch <- got // buffered for one, and each watch is answered once
	}
	r.pushWatches = r.pushWatches[:0]
}

func (r *Runtime) replaceDriver(ctx context.Context, name string, d nio.Driver, observe bool) (nio.Driver, <-chan nio.Values, error) {
	rp, ok := r.driver.(replacer)
	if !ok {
		return nil, nil, fmt.Errorf("replace driver %q: the runtime's driver (%T) is not a composite; a live swap needs the drivers composed (io.Multi)", name, r.driver)
	}
	old, ok := rp.Child(name)
	if !ok {
		return nil, nil, fmt.Errorf("replace driver %q: no such driver", name)
	}
	if err := sameTags(old, d); err != nil {
		return nil, nil, fmt.Errorf("replace driver %q: %w", name, err)
	}
	if err := r.lockMain(ctx); err != nil {
		return nil, nil, fmt.Errorf("replace driver %q: waiting for the scan in progress: %w", name, err)
	}
	defer r.mainMu.Unlock()
	if bd, ok := d.(nio.BaselineDisarmer); ok && !r.holdOnSwap {
		bd.DisarmBaseline()
	}
	prev, err := rp.Replace(name, d)
	if err != nil {
		return nil, nil, err
	}
	r.bindStatsReaders(d)  // a status driver swapped in reads this runtime too (#850)
	r.outSent.Store(false) // the next scan hands the new driver every output
	var pushed chan nio.Values
	if observe {
		pushed = make(chan nio.Values, 1)
		var names []string
		if o, ok := d.(tagNames); ok {
			names = o.OutputNames()
		}
		r.pushWatches = append(r.pushWatches, &pushWatch{names: names, ch: pushed})
	}
	return prev, pushed, nil
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
