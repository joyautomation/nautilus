package runtime

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/sfc"
)

// Online SFC commands — Codesys's "set step" and "force transition", applied
// to a running chart between two scans.
//
// A chart's state is its step-activity slots (`_S_<Step>_X`, retained VAR
// slots in the program's frame — see lang/sfc/transpile.go), so both
// commands are edits of those slots made under the program's lock, i.e.
// between one scan and the next, and both take effect as if the change had
// happened at the end of the previous scan:
//
//   - SetStep jumps the chart: every step is deactivated and the named step
//     activated, ONCE. The chart then evolves normally from there. The old
//     steps see a falling edge (their P0 and final-scan actions run), the
//     new step a rising one (its P/P1 actions run), and its Step.T starts
//     from zero — the same scan the transpiler's own evolution would give a
//     step that had just been entered.
//   - FireTransition takes one transition ONCE, whatever its condition says:
//     its source steps are deactivated and its targets activated. It refuses
//     when the transition is not enabled by the chart — a source step not
//     active — because firing it then would invent tokens the chart never
//     had (a jump does that, explicitly, with SetStep).
//
// Neither is a force: nothing is held, and the next scan's transitions run
// against the new activity. A step entered this way whose outgoing condition
// is already TRUE is left on that very next scan, as it would be if the
// chart had arrived there on its own.

// SFCInfo describes one running chart for a client offering the commands.
type SFCInfo struct {
	Task        string           `json:"task"`
	POU         string           `json:"pou"`
	Steps       []SFCStepInfo    `json:"steps"`
	Transitions []SFCTransitInfo `json:"transitions"`
}

// SFCStepInfo is one step and whether it is active right now.
type SFCStepInfo struct {
	Name    string `json:"name"`
	Initial bool   `json:"initial,omitempty"`
	Active  bool   `json:"active"`
}

// SFCTransitInfo is one transition. ID is how the commands address it: the
// declared name, or "t<line>" for an unnamed one — the transpiler's own
// identifier, which the editor derives the same way.
type SFCTransitInfo struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Line    int      `json:"line"`
	From    []string `json:"from"`
	To      []string `json:"to"`
	Enabled bool     `json:"enabled"` // every source step active
}

// sfcChart parses the program's running source as a chart, or reports that
// it is not one. Caller holds p.mu.
func (p *Program) sfcChartLocked() (*sfc.Program, error) {
	if Language(p.source) != "sfc" {
		return nil, fmt.Errorf("program %s is not an SFC chart", POUOf(p.source))
	}
	return sfc.Parse(p.source)
}

// transitionIDs mirrors the transpiler's ID assignment (declared name, else
// t<line>, de-duplicated with _2, _3…), so an ID the editor computed from the
// same source names the same transition here.
func transitionIDs(chart *sfc.Program) []string {
	used := map[string]bool{}
	ids := make([]string, len(chart.Transitions))
	for i, t := range chart.Transitions {
		id := t.Name
		if id == "" {
			id = fmt.Sprintf("t%d", t.Pos.Line)
		}
		base := id
		for n := 2; used[strings.ToUpper(id)]; n++ {
			id = fmt.Sprintf("%s_%d", base, n)
		}
		used[strings.ToUpper(id)] = true
		ids[i] = id
	}
	return ids
}

// stepSlot finds the frame slot of a step's activity flag. Caller holds p.mu.
func (p *Program) stepSlotLocked(step string) (int, bool) {
	want := "_S_" + step + "_X"
	for i, s := range p.prog.Slots {
		if s.Kind == ir.VarLocal && strings.EqualFold(s.Name, want) && i < len(p.frame.Slots) {
			return i, true
		}
	}
	return 0, false
}

// canonStep resolves a step name case-insensitively to its declared spelling.
func canonStep(chart *sfc.Program, name string) (*sfc.Step, bool) {
	for _, s := range chart.Steps {
		if strings.EqualFold(s.Name, name) {
			return s, true
		}
	}
	return nil, false
}

// SFC reports the program's chart with live activity, or an error when the
// program is not SFC.
func (p *Program) SFC() (SFCInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	chart, err := p.sfcChartLocked()
	if err != nil {
		return SFCInfo{}, err
	}
	info := SFCInfo{POU: chart.Name}
	active := map[string]bool{}
	for _, s := range chart.Steps {
		a := false
		if i, ok := p.stepSlotLocked(s.Name); ok {
			a = p.frame.Slots[i].B
		}
		active[strings.ToUpper(s.Name)] = a
		info.Steps = append(info.Steps, SFCStepInfo{Name: s.Name, Initial: s.Initial, Active: a})
	}
	for i, id := range transitionIDs(chart) {
		t := chart.Transitions[i]
		en := len(t.From) > 0
		for _, f := range t.From {
			en = en && active[strings.ToUpper(f)]
		}
		info.Transitions = append(info.Transitions, SFCTransitInfo{
			ID: id, Name: t.Name, Line: t.Pos.Line, From: t.From, To: t.To, Enabled: en,
		})
	}
	return info, nil
}

// SetStep jumps the chart to one step (see the comment at the top of this
// file). Returns the step's declared spelling.
func (p *Program) SetStep(step string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	chart, err := p.sfcChartLocked()
	if err != nil {
		return "", err
	}
	target, ok := canonStep(chart, step)
	if !ok {
		return "", fmt.Errorf("chart %s has no step %s", chart.Name, step)
	}
	ti, ok := p.stepSlotLocked(target.Name)
	if !ok {
		return "", fmt.Errorf("step %s has no activity slot in the running program", target.Name)
	}
	for _, s := range chart.Steps {
		if i, ok := p.stepSlotLocked(s.Name); ok {
			p.frame.Slots[i] = ir.BoolVal(false)
		}
	}
	p.frame.Slots[ti] = ir.BoolVal(true)
	return target.Name, nil
}

// FireTransition takes one transition once (see the comment at the top of
// this file). id is the transition's name or "t<line>". Returns the ID.
func (p *Program) FireTransition(id string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	chart, err := p.sfcChartLocked()
	if err != nil {
		return "", err
	}
	ids := transitionIDs(chart)
	var t *sfc.Transition
	for i, tid := range ids {
		if strings.EqualFold(tid, id) {
			t, id = chart.Transitions[i], tid
			break
		}
	}
	if t == nil {
		return "", fmt.Errorf("chart %s has no transition %s", chart.Name, id)
	}
	from := make([]int, 0, len(t.From))
	for _, s := range t.From {
		i, ok := p.stepSlotLocked(s)
		if !ok {
			return "", fmt.Errorf("step %s has no activity slot in the running program", s)
		}
		if !p.frame.Slots[i].B {
			return "", fmt.Errorf("transition %s is not enabled: its source step %s is not active (use Set Active Step to jump the chart)", id, s)
		}
		from = append(from, i)
	}
	to := make([]int, 0, len(t.To))
	for _, s := range t.To {
		i, ok := p.stepSlotLocked(s)
		if !ok {
			return "", fmt.Errorf("step %s has no activity slot in the running program", s)
		}
		to = append(to, i)
	}
	// Clears first, then sets — the transpiler's set-dominates-clear order,
	// so a step that is both source and target stays active.
	for _, i := range from {
		p.frame.Slots[i] = ir.BoolVal(false)
	}
	for _, i := range to {
		p.frame.Slots[i] = ir.BoolVal(true)
	}
	return id, nil
}

// SFCCharts lists every SFC program in the resource (main first).
func (r *Runtime) SFCCharts() []SFCInfo {
	var out []SFCInfo
	add := func(task string, p *Program) {
		if info, err := p.SFC(); err == nil {
			info.Task = task
			out = append(out, info)
		}
	}
	add(MainTaskName, r.prog)
	for _, tr := range r.tasks {
		add(tr.name, tr.prog)
	}
	return out
}

// SFCProgram resolves the chart an SFC command addresses: by POU name when
// given, else the resource's only SFC program — or the only one with a step
// (or transition) of that name, so a single-chart controller needs no POU.
func (r *Runtime) SFCProgram(pou, step, transition string) (*Program, error) {
	if pou != "" {
		p, _ := r.ProgramByPOU(pou)
		if p == nil {
			return nil, fmt.Errorf("no program %s", pou)
		}
		return p, nil
	}
	var matches []*Program
	var charts int
	consider := func(p *Program) {
		info, err := p.SFC()
		if err != nil {
			return
		}
		charts++
		for _, s := range info.Steps {
			if step != "" && strings.EqualFold(s.Name, step) {
				matches = append(matches, p)
				return
			}
		}
		for _, t := range info.Transitions {
			if transition != "" && strings.EqualFold(t.ID, transition) {
				matches = append(matches, p)
				return
			}
		}
	}
	consider(r.prog)
	for _, tr := range r.tasks {
		consider(tr.prog)
	}
	switch {
	case charts == 0:
		return nil, fmt.Errorf("no SFC program is running")
	case len(matches) == 1:
		return matches[0], nil
	case len(matches) > 1:
		return nil, fmt.Errorf("more than one chart has %s%s — name the program (pou)", step, transition)
	}
	return nil, fmt.Errorf("no running chart has a step or transition %s%s", step, transition)
}
