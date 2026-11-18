package modbus

import (
	"fmt"
	"path"
	"slices"
	"sort"
)

// ReplaceSource puts one device's new configuration in place of the running
// one while every other source keeps polling (#706): a new host, port,
// unit id, byte order, timeouts, or register addresses, tables, formats,
// scaling and scan classes for the same tags.
//
// The replacement keeps the source's ID, its Enable tag, and exactly the
// same polled and writable tag names: the driver's input and output names,
// and the runtime's tags behind them, are fixed. tags are the source's
// bindings (each names src.ID). They are validated and planned on their
// own before anything changes; a refusal leaves the running source as it
// was.
//
// On a started driver the new source's loop is started, the source set is
// swapped under the source lock, and only then is the old loop cancelled and
// waited for: its own bindings stay with it, so it never writes the new
// addresses to the old device. The new source's tags read NotConnected
// until its first poll, the other sources' tags are untouched, and the
// driver asks the runtime for the outputs (OutputsWanted), so the new
// connection starts from the current commands. A parked source stays
// parked.
func (d *Driver) ReplaceSource(src Source, tags []TagBinding) error {
	d.replMu.Lock()
	defer d.replMu.Unlock()
	d.srcMu.RLock()
	old := d.bySource[src.ID]
	var oldTags []TagBinding
	for _, t := range d.manifest.Tags {
		if t.Source == src.ID {
			oldTags = append(oldTags, t)
		}
	}
	d.srcMu.RUnlock()
	if old == nil {
		return fmt.Errorf("modbus: replace source %q: no such source", src.ID)
	}
	if src.Enable != old.cfg.Enable {
		return fmt.Errorf("modbus: replace source %q: its enable tag cannot change live (%q → %q)", src.ID, old.cfg.Enable, src.Enable)
	}

	// The tag-class assignments rewrite scan classes as New does.
	eff := make([]TagBinding, len(tags))
	for i, t := range tags {
		if t.Source != src.ID {
			return fmt.Errorf("modbus: replace source %q: binding %q names source %q", src.ID, t.Name, t.Source)
		}
		for _, a := range d.assignments {
			for _, p := range a.patterns {
				if ok, _ := path.Match(p, t.Name); ok {
					t.ScanClass = a.class
					break
				}
			}
		}
		eff[i] = t
	}
	plan, err := BuildPlan(Manifest{Sources: []Source{src}, Tags: eff}, d.gap)
	if err != nil {
		return fmt.Errorf("modbus: replace source %q: %w", src.ID, err)
	}
	for _, b := range plan.Blocks {
		if rate, ok := d.classRates[b.Class]; !ok || rate <= 0 {
			return fmt.Errorf("modbus: replace source %q: binding %q names undefined scan class %q", src.ID, b.Bindings[0].Name, b.Class)
		}
	}
	oldPlan := d.Plan()
	if err := sameSourceTags(src.ID, oldPlan, oldTags, plan, eff); err != nil {
		return fmt.Errorf("modbus: replace source %q: %w", src.ID, err)
	}

	ns := newSource(src)
	for _, b := range plan.Blocks {
		ns.addBlock(b, d.classRates[b.Class])
	}
	for _, t := range eff {
		if t.Writable {
			ns.addOutput(t)
		}
	}
	old.mu.Lock()
	ns.enabled = old.enabled
	old.mu.Unlock()

	d.srcMu.Lock()
	started := d.started.Load()
	if started {
		d.startSource(ns)
	}
	for i, s := range d.sources {
		if s == old {
			d.sources[i] = ns
		}
	}
	d.bySource[src.ID] = ns
	if src.Enable != "" {
		for i, s := range d.enables[src.Enable] {
			if s == old {
				d.enables[src.Enable][i] = ns
			}
		}
	}
	blocks := make([]Block, 0, len(d.plan.Blocks))
	for _, b := range d.plan.Blocks {
		if b.Source != src.ID {
			blocks = append(blocks, b)
		}
	}
	d.plan.Blocks = append(blocks, plan.Blocks...)
	for i, s := range d.manifest.Sources {
		if s.ID == src.ID {
			d.manifest.Sources[i] = src
		}
	}
	keep := make([]TagBinding, 0, len(d.manifest.Tags))
	for _, t := range d.manifest.Tags {
		if t.Source != src.ID {
			keep = append(keep, t)
		}
	}
	d.manifest.Tags = append(keep, eff...)
	d.srcMu.Unlock()

	d.wanted.Store(true) // the new connection starts from the current commands
	if started {
		old.cancel()
		<-old.done
	}
	d.log.Info("modbus: source replaced", "source", src.ID, "addr", src.Addr(), "unit", src.UnitID, "bindings", len(eff))
	return nil
}

// sameSourceTags refuses a replacement that polls or accepts other tag names
// than the source does now: the driver's InputNames and OutputNames, and
// the runtime's tags behind them, cannot change while it runs.
func sameSourceTags(id string, oldPlan Plan, oldTags []TagBinding, newPlan Plan, newTags []TagBinding) error {
	polled := func(p Plan) []string {
		var out []string
		for _, b := range p.Blocks {
			if b.Source != id {
				continue
			}
			for _, t := range b.Bindings {
				out = append(out, t.Name)
			}
		}
		sort.Strings(out)
		return out
	}
	writable := func(ts []TagBinding) []string {
		var out []string
		for _, t := range ts {
			if t.Writable {
				out = append(out, t.Name)
			}
		}
		sort.Strings(out)
		return out
	}
	if a, b := polled(oldPlan), polled(newPlan); !slices.Equal(a, b) {
		return fmt.Errorf("it would poll %v instead of %v: the tags a device delivers cannot change live (restart instead)", b, a)
	}
	if a, b := writable(oldTags), writable(newTags); !slices.Equal(a, b) {
		return fmt.Errorf("it would accept writes for %v instead of %v: the tags a device accepts cannot change live (restart instead)", b, a)
	}
	return nil
}
