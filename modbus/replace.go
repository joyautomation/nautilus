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
// BREAK BEFORE MAKE. The source set is swapped under the source lock, then
// the old loop is cancelled and waited for (its request in flight done or
// timed out), and only then does the new source's loop start and the
// driver ask for the outputs (OutputsWanted): the two never talk to a
// device at once, a single-connection device sees the old connection close
// before the new one dials, and the old loop, winding down with its own
// bindings, never writes the new addresses. Nothing waits while holding
// the source lock. The new source's tags read NotConnected until its first
// poll; the other sources' tags are untouched; the new connection starts
// from the current commands. A parked source stays parked, and __Outages
// carries over (it counts since the driver started; a replace is not an
// outage).
//
// The type each tag delivers and accepts (DINT, REAL, BOOL; see InputType)
// cannot change either: the runtime fixed it at start.
//
// A new address that does not answer is not refused here: the source is
// replaced and reads NotConnected. Probing it first, and putting the old
// configuration back when the new one does not come up, is the staged
// apply's job (#706 PR 3/4).
//
// After a replace on a started driver, the source's first share of an
// output snapshot is sent (the default) or, with opts.HoldOutputs, only
// recorded as its baseline, as the runtime's Options.HoldOutputsOnSwap
// does for a whole driver: pass that one switch here.
//
// On a stopped driver the replacement is recorded and its loop starts with
// the next Start; replacing it again before then is fine.
func (d *Driver) ReplaceSource(src Source, tags []TagBinding, opts ReplaceOptions) error {
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
	if err := sameTypes(oldTags, eff); err != nil {
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
	ns.baseline = opts.HoldOutputs
	ns.observe = opts.Observe
	if opts.Held {
		ns.held, ns.state = true, "held"
	}
	old.mu.Lock()
	ns.enabled = old.enabled
	ns.outages = old.outages // since the driver started; a replace is not one
	ns.dropped = old.dropped // the same
	old.mu.Unlock()

	d.srcMu.Lock()
	started := d.started.Load()
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
	// Copies: the manifest's slices may still be the caller's (New).
	srcs := slices.Clone(d.manifest.Sources)
	for i, s := range srcs {
		if s.ID == src.ID {
			srcs[i] = src
		}
	}
	d.manifest.Sources = srcs
	keep := make([]TagBinding, 0, len(d.manifest.Tags))
	for _, t := range d.manifest.Tags {
		if t.Source != src.ID {
			keep = append(keep, t)
		}
	}
	d.manifest.Tags = append(keep, eff...)
	d.srcMu.Unlock()

	if started {
		// Only a loop that ran is cancelled and waited for: a source
		// recorded after Stop (startSource did nothing) has no cancel and
		// no done, and waiting on its nil done would block forever.
		d.lifeMu.Lock()
		cancel, done := old.cancel, old.done
		d.lifeMu.Unlock()
		if done != nil {
			cancel()
			<-done // its request in flight is done or timed out
		}
		d.startSource(ns)
	}
	d.wanted.Store(true) // the new connection starts from the current commands
	d.log.Info("modbus: source replaced", "source", src.ID, "addr", src.Addr(), "unit", src.UnitID, "bindings", len(eff), "held", opts.Held)
	return nil
}

// Held reports whether the source id is held (ReplaceOptions.Held).
func (d *Driver) Held(id string) bool {
	d.srcMu.RLock()
	defer d.srcMu.RUnlock()
	s := d.bySource[id]
	return s != nil && s.held
}

// ReplaceOptions are ReplaceSource's choices.
type ReplaceOptions struct {
	// HoldOutputs records the source's first share of an output snapshot
	// after the replace as its baseline instead of sending it: the new
	// connection writes once a command changes. False (the default) sends
	// it, so the device starts from the current commands. It is the
	// runtime's Options.HoldOutputsOnSwap (#706, decision 1).
	HoldOutputs bool

	// Observe, when set, is called once with the source's first share of
	// an output snapshot after the replace: what that share queued to send,
	// or why it sent nothing. It is how a staged apply reports the commands
	// the new connection was actually handed (#706), from the rule the
	// driver itself follows, not a copy of it. It runs on the scan's
	// goroutine at the end of WriteOutputs, under the driver's source lock
	// (srcMu, read) but no source's: keep it short, and never call back
	// into the driver from it.
	Observe func(FirstShare)

	// Held puts the source in with no connection: it is not polled and
	// sends nothing, and every tag it binds reads Bad, until a replace
	// without Held. A controller holds a device this way when the
	// configuration it should run there could not be put in (#706): what
	// it would read from the address it has is not to be trusted.
	Held bool
}

// FirstShare is a replaced source's first share of an output snapshot.
type FirstShare struct {
	// Queued are the commands the share queued for the device, by tag.
	Queued map[string]any
	// Undecided are outputs the share left to decide: each is compared
	// with what its device holds at its first live read, and commanded then
	// only if it differs (the start-up baseline, #1123). They are not in
	// Queued, and may never be commanded.
	Undecided []string
	// GateClosed: the write gate was closed, so nothing was queued.
	GateClosed bool
	// Parked: the source's enable tag is false; the commands wait in the
	// queue until it is enabled.
	Parked bool
}

// ObserveFirstShares has every source report its next share of an output
// snapshot, as ReplaceOptions.Observe does for one replaced source: for a
// driver swapped in whole (#706), arm it before the swap, and f hears what
// each source's first share queued (its start-up baseline, the write gate
// and a parked source all applied). Same rules for f as Observe's. It
// returns how many sources will report.
func (d *Driver) ObserveFirstShares(f func(id string, s FirstShare)) int {
	d.srcMu.RLock()
	defer d.srcMu.RUnlock()
	for _, s := range d.sources {
		id := s.cfg.ID
		s.mu.Lock()
		s.observe = func(fs FirstShare) { f(id, fs) }
		s.mu.Unlock()
	}
	return len(d.sources)
}

// HeldAtBaseline reports whether b's command, when it lands in a start-up
// baseline or a held replace's first share, is only recorded and not sent.
// A rewrite: keep-alive is always sent: the device depends on it.
func HeldAtBaseline(b TagBinding) bool { return b.Rewrite <= 0 }

// Sending reports whether a command for source id goes out now: the write
// gate is open and the source is not parked by its enable tag. why says
// which stops it. It looks without acting on what it finds (gateOpen acts
// on a gate transition; the next write does that).
func (d *Driver) Sending(id string) (ok bool, why string) {
	d.gateMu.Lock()
	gate := d.writeGate
	d.gateMu.Unlock()
	if gate != nil && !gate() {
		return false, "the write gate is closed (this replica is not the leader, or its scans are not live)"
	}
	d.srcMu.RLock()
	s := d.bySource[id]
	d.srcMu.RUnlock()
	if s == nil {
		return false, "no source " + id
	}
	s.mu.Lock()
	on := s.enabled
	s.mu.Unlock()
	if !on {
		return false, "parked: its enable tag " + s.cfg.Enable + " is false"
	}
	return true, ""
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

// sameTypes refuses a replacement that changes the IEC type a tag
// delivers or accepts (DINT, REAL, BOOL): the runtime fixed each tag's
// type at start.
func sameTypes(oldTags, newTags []TagBinding) error {
	was := map[string]string{}
	for _, t := range oldTags {
		was[t.Name] = bindingType(t)
	}
	for _, t := range newTags {
		if w, ok := was[t.Name]; ok && w != bindingType(t) {
			return fmt.Errorf("binding %q would deliver %s instead of %s: a tag's type cannot change live (restart instead)", t.Name, bindingType(t), w)
		}
	}
	return nil
}
