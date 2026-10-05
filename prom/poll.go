// poll.go is the Poll function hw.Base calls for one (source, class): fetch
// the body, parse it, and resolve every member due in that class against
// the parsed samples. See driver.go for New/the transport. Rates use
// hw.Counter's float form: a Prometheus counter is an already-float64
// accumulator that never wraps, where any decrease means the exporter
// restarted.
package prom

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/lang/ir"
)

// errRateWarm marks "this rate-bound selector matched a series, but there
// is no previous sample yet (first poll, or a reset)" — not an error at
// all, the documented "rate members read 0.0 for exactly one interval"
// (docs/design/it-drivers.md §5): the member is simply left as it was for
// this one poll, exactly like an absent metric.
var errRateWarm = errors.New("prom: rate: no previous sample yet")

// outcome is what resolving one member produced.
type outcome int

const (
	outcomeOK   outcome = iota // deliver the value
	outcomeSkip                // leave the member as it was; not an error, not Bad
	outcomeBad                 // the whole TAG goes in Result.Bad
)

// poll is the hw.PollFunc: fetch one source's `/metrics` once, and resolve
// every member this (source, class) call is responsible for. A non-nil
// error here is a TRANSPORT failure (Base's contract) — the GET failing or
// the body failing to parse at all; a single member or selector going
// missing inside an otherwise-good scrape is Result.Bad on its tag, not a
// transport error, so one dead sensor never takes its neighbours offline.
func (d *Driver) poll(ctx context.Context, sourceID, class string) (hw.Result, error) {
	sr := d.sources[sourceID]
	timeout := sr.cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	body, err := d.fetch(ctx, sr.cfg.URL, sr.headers, sr.cfg.InsecureSkipVerify, timeout)
	if err != nil {
		sr.mu.Lock()
		sr.wasDown[class] = true
		sr.mu.Unlock()
		return hw.Result{}, fmt.Errorf("prom: scrape %s: %w", sourceID, err)
	}
	scrape, err := ParseText(body)
	if err != nil {
		sr.mu.Lock()
		sr.wasDown[class] = true
		sr.mu.Unlock()
		return hw.Result{}, fmt.Errorf("prom: scrape %s: %w", sourceID, err)
	}
	idx := buildIndex(scrape.Samples)
	now := time.Now()

	sr.mu.Lock()
	reconnected := sr.wasDown[class]
	sr.wasDown[class] = false
	sr.mu.Unlock()

	var res hw.Result
	badTags := map[string]bool{}
	for tagName, tr := range d.tags {
		if tr.src != sr {
			continue
		}
		for member, cb := range tr.members {
			if cb.b.Binding.Const != nil || cb.b.Binding.Derived != "" {
				continue // Base applies/derives these; Poll never sees them
			}
			if d.classOf[tagName+"."+member] != class {
				continue
			}
			v, oc := d.resolveMember(tr, member, cb, idx, reconnected, now)
			switch oc {
			case outcomeOK:
				res.Updates = append(res.Updates, hw.Update{Tag: tagName, Member: member, Value: v})
			case outcomeBad:
				badTags[tagName] = true
			}
			// outcomeSkip: leave the member as it was, exactly like a
			// device that never mentions this row.
		}
	}
	for name := range badTags {
		res.Bad = append(res.Bad, name)
	}
	return res, nil
}

// resolveMember resolves one member's binding against one parsed scrape.
func (d *Driver) resolveMember(tr *tagRuntime, member string, cb compiledBinding, idx sampleIndex, reconnected bool, now time.Time) (ir.Value, outcome) {
	if cb.expr != nil {
		return d.resolveExprMember(tr, member, cb, idx, reconnected, now)
	}

	matches := idx.match(cb.b.Metric, cb.b.Labels)
	if len(matches) == 0 {
		tr.src.warnOnce(tr.decl.Name, member, "absent", fmt.Sprintf("metric %s%v never appeared in the scrape", cb.b.Metric, cb.b.Labels))
		return ir.Value{}, outcomeSkip
	}
	if cb.b.Label != "" {
		lv, ok := matches[0].Labels[cb.b.Label]
		if !ok {
			tr.src.warnOnce(tr.decl.Name, member, "label-absent", fmt.Sprintf("metric %s has no label %q", cb.b.Metric, cb.b.Label))
			return ir.Value{}, outcomeSkip
		}
		v, ok, err := cb.b.Binding.Apply(cb.field, hw.RawStringVal(lv), nil, now)
		if err != nil || !ok {
			return ir.Value{}, outcomeBad
		}
		return v, outcomeOK
	}

	val, err := aggregate(cb.b.Agg, matches)
	if err != nil {
		tr.src.warnOnce(tr.decl.Name, member, "ambiguous", err.Error())
		return ir.Value{}, outcomeBad
	}

	applied := cb.b.Binding
	raw := hw.RawFloatVal(val)
	if applied.Rate {
		c := tr.src.counterFor(tr.decl.Name + "." + member)
		if reconnected {
			c.Reset()
		}
		rate, ok := c.ObserveFloat(val, now)
		if !ok {
			return ir.Value{}, outcomeSkip // "reads 0.0 for exactly one interval" (§5)
		}
		applied.Rate = false // the counter already produced the rate; Apply only scales/maps it now
		raw = hw.RawFloatVal(rate)
	}
	v, ok, err := applied.Apply(cb.field, raw, nil, now)
	if err != nil {
		return ir.Value{}, outcomeBad
	}
	if !ok {
		return ir.Value{}, outcomeSkip
	}
	return v, outcomeOK
}

// resolveExprMember evaluates an expr binding's arithmetic over its named
// selectors (hw.Expr, hw.ParseExpr — the same tiny calculator a `derived:`
// binding uses over sibling members, here over independently-fetched
// series). A selector that matches nothing is the ONE case that promotes to
// Result.Bad rather than a quiet skip — unlike a plain metric: binding's
// absence, an expr's selectors are core, always-present series a healthy
// node_exporter carries (idle cpu-seconds, memory totals), so their absence
// means something is actually wrong (a renamed metric, brief §3.3's
// foreign-test case), not "this hardware has no fan 4".
func (d *Driver) resolveExprMember(tr *tagRuntime, member string, cb compiledBinding, idx sampleIndex, reconnected bool, now time.Time) (ir.Value, outcome) {
	warming := false
	bad := false
	var badReason error
	lookup := func(name string) (ir.Value, bool) {
		if name == builtinNow {
			return ir.RealVal(float64(now.Unix())), true
		}
		sel, ok := cb.b.From[name]
		if !ok {
			return ir.Value{}, false // Validate should have caught this; defensive
		}
		v, err := d.resolveSelector(tr, member, name, sel, idx, reconnected, now)
		switch {
		case err == nil:
			return v, true
		case errors.Is(err, errRateWarm):
			warming = true
			return ir.Value{}, false
		default:
			bad = true
			badReason = err
			return ir.Value{}, false
		}
	}
	v, err := cb.expr.Eval(lookup)
	if err != nil {
		if bad {
			tr.src.warnOnce(tr.decl.Name, member, "expr-selector", badReason.Error())
			return ir.Value{}, outcomeBad
		}
		if warming {
			return ir.Value{}, outcomeSkip
		}
		// Any other eval error (a type clash the manifest's own Validate
		// should already forbid) is still this tag's problem, not a
		// transport failure.
		tr.src.warnOnce(tr.decl.Name, member, "expr-eval", err.Error())
		return ir.Value{}, outcomeBad
	}
	cv, err := hw.Coerce(v, cb.field)
	if err != nil {
		tr.src.warnOnce(tr.decl.Name, member, "expr-coerce", err.Error())
		return ir.Value{}, outcomeBad
	}
	return cv, outcomeOK
}

// resolveSelector resolves one named input to an expr — the same
// metric+labels+agg locator as a plain Binding, with its own independent
// Rate.
func (d *Driver) resolveSelector(tr *tagRuntime, member, name string, sel Selector, idx sampleIndex, reconnected bool, now time.Time) (ir.Value, error) {
	matches := idx.match(sel.Metric, sel.Labels)
	if len(matches) == 0 {
		return ir.Value{}, fmt.Errorf("selector %s: metric %s%v matched nothing", name, sel.Metric, sel.Labels)
	}
	if sel.Label != "" {
		lv, ok := matches[0].Labels[sel.Label]
		if !ok {
			return ir.Value{}, fmt.Errorf("selector %s: metric %s has no label %q", name, sel.Metric, sel.Label)
		}
		return ir.StringVal(lv), nil
	}
	val, err := aggregate(sel.Agg, matches)
	if err != nil {
		return ir.Value{}, fmt.Errorf("selector %s: %w", name, err)
	}
	if sel.Rate {
		c := tr.src.counterFor(tr.decl.Name + "." + member + "." + name)
		if reconnected {
			c.Reset()
		}
		rate, ok := c.ObserveFloat(val, now)
		if !ok {
			return ir.Value{}, errRateWarm
		}
		val = rate
	}
	return ir.RealVal(val), nil
}

// ── sample index & aggregation ──────────────────────────────────────────

// sampleIndex groups one scrape's samples by metric name for O(1) lookup;
// matching on labels within a name is a linear scan, which is fine at
// node_exporter's per-metric cardinality (tens of hwmon rows, at most a few
// hundred cpu/mode combinations).
type sampleIndex map[string][]Sample

func buildIndex(samples []Sample) sampleIndex {
	idx := make(sampleIndex, len(samples)/2+1)
	for _, s := range samples {
		idx[s.Name] = append(idx[s.Name], s)
	}
	return idx
}

// match returns every series of metric whose labels are a SUPERSET of want
// — an exact match on the given labels, any other label on the series free
// (docs/design/it-drivers.md §3.3).
func (idx sampleIndex) match(metric string, want map[string]string) []Sample {
	var out []Sample
	for _, s := range idx[metric] {
		ok := true
		for k, v := range want {
			if s.Labels[k] != v {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, s)
		}
	}
	return out
}

// aggregate combines several matching series into one number. With no agg
// and exactly one match, that match's value; with no agg and more than one,
// an error (Validate should have caught a manifest that can produce this at
// runtime, but a scrape can drift under a manifest that used to be exact).
func aggregate(agg string, matches []Sample) (float64, error) {
	if agg == "" {
		if len(matches) != 1 {
			return 0, fmt.Errorf("%d series matched with no agg: to combine them", len(matches))
		}
		return matches[0].Value, nil
	}
	switch agg {
	case "sum":
		var t float64
		for _, m := range matches {
			t += m.Value
		}
		return t, nil
	case "count":
		return float64(len(matches)), nil
	case "max":
		v := matches[0].Value
		for _, m := range matches[1:] {
			if m.Value > v {
				v = m.Value
			}
		}
		return v, nil
	case "min":
		v := matches[0].Value
		for _, m := range matches[1:] {
			if m.Value < v {
				v = m.Value
			}
		}
		return v, nil
	case "avg":
		var t float64
		for _, m := range matches {
			t += m.Value
		}
		return t / float64(len(matches)), nil
	}
	return 0, fmt.Errorf("unknown agg %q", agg)
}

// ── per-source runtime state ────────────────────────────────────────────

// counterFor returns key's counter, creating it on first use.
func (sr *sourceRuntime) counterFor(key string) *hw.Counter {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	c, ok := sr.counters[key]
	if !ok {
		c = &hw.Counter{}
		sr.counters[key] = c
	}
	return c
}

// warnOnce logs one condition exactly once per source — "a member absent
// from the wire is logged once per source" (docs/design/it-drivers.md §4),
// not once per poll for as long as the sensor stays missing.
func (sr *sourceRuntime) warnOnce(tag, member, reason, detail string) {
	key := tag + "." + member + ":" + reason
	sr.mu.Lock()
	already := sr.warned[key]
	sr.warned[key] = true
	sr.mu.Unlock()
	if !already {
		sr.logger.Warn("prom: member absent", "source", sr.cfg.ID, "tag", tag, "member", member, "reason", reason, "detail", detail)
	}
}
