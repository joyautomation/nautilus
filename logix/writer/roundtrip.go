package writer

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/lang/l5x"
	"github.com/joyautomation/nautilus/lang/ld"
)

// The structural round trip (logix-authoring.md §5.2): source → writer →
// L5X → lang/l5x reader → ladder graph, compared with the source graph
// modulo the rewrites the package doc lists. The reader is corpus-tested
// against real exports, so a writer that produces text the reader cannot
// turn back into the source's shape has produced text Logix would not
// have — and this finds it with no Rockwell software in the loop.
//
// The comparator is deliberately a second implementation of the mapping:
// it walks the GRAPH and asks "is this what the rewrite says should be
// here?", while the writer builds TEXT. A spacing, paren or ordering bug
// in the writer shows up as a mismatch here; a shared wrong decision
// about Logix semantics does not, which is what the behavioural harness
// (§5.5) is for.

// RoundTrip writes src, reads the L5X back and compares. It returns the
// L5X, every mismatch found, and an error for a source that does not
// write at all (a rule fired, or it does not parse).
func RoundTrip(src string, opts Options) (doc []byte, problems []string, err error) {
	m, err := ld.Graph(src, opts.Libs...)
	if err != nil {
		return nil, nil, err
	}
	doc, diags, err := Write(src, opts)
	if err != nil {
		return nil, nil, err
	}
	if len(diags) > 0 {
		return nil, nil, fmt.Errorf("source does not write: %s", diags[0])
	}
	opts = opts.withDefaults(m.Name)
	f, err := l5x.Parse(doc)
	if err != nil {
		return doc, nil, fmt.Errorf("emitted L5X does not parse: %w", err)
	}
	got, err := l5x.Ladder(f, l5x.LadderOptions{Routine: opts.Program + "/" + opts.Routine})
	if err != nil {
		return doc, nil, fmt.Errorf("emitted L5X renders no ladder: %w", err)
	}
	c := &cmp{src: m, got: got.Rungs, types: map[string]string{}, opts: opts, file: f}
	for _, v := range m.Vars {
		c.types[strings.ToLower(v.Name)] = strings.ToUpper(strings.TrimSpace(v.Type))
	}
	notes := map[int]string{}
	for _, cm := range m.Comments {
		notes[cm.EndLine+1] = cm.Text
	}
	for _, r := range m.Rungs {
		c.rung(r, notes[r.Line])
	}
	if c.pos < len(c.got) {
		c.problemf("", "%d unexpected rung(s) after the last source rung; first: %+v", len(c.got)-c.pos, c.got[c.pos].Elements)
	}
	c.tags()
	return doc, c.problems, nil
}

type cmp struct {
	src      *ld.Model
	got      []ld.Rung
	pos      int // next got rung to consume
	types    map[string]string
	opts     Options
	file     *l5x.File
	problems []string

	// per source rung
	cur     []ld.Element // the got rung's condition elements being matched
	idx     int
	outs    []string // helper edge outputs, in element order
	resets  []string // RES rungs expected after, as "ref|inst"
	seen    map[string]int
	genTags map[string]bool
	// legHead mirrors the writer's: set while inside the legs of a branch
	// that takes power from the rail.
	legHead bool
}

func (c *cmp) problemf(rung, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if rung != "" {
		msg = "rung " + rung + ": " + msg
	}
	c.problems = append(c.problems, msg)
}

func (c *cmp) take(rung string) (ld.Rung, bool) {
	if c.pos >= len(c.got) {
		c.problemf(rung, "L5X ran out of rungs")
		return ld.Rung{}, false
	}
	r := c.got[c.pos]
	c.pos++
	return r, true
}

// logixRef is the operand spelling the rewrites call for.
func (c *cmp) logixRef(ref string) string {
	base, rest := splitRef(ref)
	if rest == "" || rest[0] == '[' {
		return ref
	}
	st := blockTypes[c.types[strings.ToLower(base)]]
	if st == "" {
		return ref
	}
	if to, ok := memberRewrite[st][strings.ToUpper(rest[1:])]; ok {
		return base + "." + to
	}
	return ref
}

func (c *cmp) rung(r ld.Rung, notes string) {
	c.outs, c.resets, c.seen = nil, nil, map[string]int{}
	c.helpers(r, r.Elements, true)
	gr, ok := c.take(r.Name)
	if !ok {
		return
	}
	if want := rungComment(notes, r.Comment); gr.Comment != want {
		c.problemf(r.Name, "comment %q, want %q", gr.Comment, want)
	}
	c.cur, c.idx = gr.Elements, 0
	c.series(r, r.Elements, true, len(r.Coils) == 0)
	if c.idx < len(c.cur) {
		c.problemf(r.Name, "%d unexpected element(s) at the end of the rung: %+v", len(c.cur)-c.idx, c.cur[c.idx:])
	}
	last := c.got[c.pos-1]
	if len(last.Coils) != len(r.Coils) {
		c.problemf(r.Name, "%d coil(s), want %d", len(last.Coils), len(r.Coils))
	} else {
		for i, sc := range r.Coils {
			gc := last.Coils[i]
			if gc.Ref != c.logixRef(sc.Ref) || gc.Mode != sc.Mode {
				c.problemf(r.Name, "coil %d is %s%q, want %s%q", i, gc.Mode, gc.Ref, sc.Mode, c.logixRef(sc.Ref))
			}
		}
	}
	for _, rs := range c.resets {
		ref, inst, _ := strings.Cut(rs, "|")
		hr, ok := c.take(r.Name)
		if !ok {
			return
		}
		if len(hr.Elements) != 2 || len(hr.Coils) != 0 ||
			hr.Elements[0].Kind != "contact" || hr.Elements[0].Ref != ref || hr.Elements[0].Neg ||
			hr.Elements[1].Kind != "fn" || hr.Elements[1].Fn != "RES" || hr.Elements[1].Args != inst {
			c.problemf(r.Name, "expected the reset rung XIC(%s)RES(%s), got %+v / %+v", ref, inst, hr.Elements, hr.Coils)
		}
	}
}

// helpers consumes the helper rungs a source rung puts before itself, in
// element order: an OSR/OSF rung per edge away from the head, a MOVE rung
// per variable preset. It also notes the RES rungs expected after.
func (c *cmp) helpers(r ld.Rung, elems []ld.Element, top bool) {
	for i, e := range elems {
		switch e.Kind {
		case "branch":
			inherited := c.legHead
			c.legHead = top && i == 0 || !top && inherited && i == 0
			for _, leg := range e.Legs {
				c.helpers(r, leg, false)
			}
			c.legHead = inherited
		case "edge":
			if e.Mode == "P" && (top && i == 0 || !top && c.legHead && i == 0) {
				c.seen["rt_"+sanitizeIdent(r.Name)+"_"+sanitizeIdent(e.Ref)]++
				continue
			}
			kind, instr := "rt", "OSR"
			if e.Mode == "N" {
				kind, instr = "ft", "OSF"
			}
			base := kind + "_" + sanitizeIdent(r.Name) + "_" + sanitizeIdent(e.Ref)
			n := c.seen[base]
			c.seen[base]++
			st := base
			if n > 0 {
				st = fmt.Sprintf("%s_%d", base, n+1)
			}
			out := st + "_Q"
			c.outs = append(c.outs, out)
			hr, ok := c.take(r.Name)
			if !ok {
				return
			}
			want := fmt.Sprintf("%s, %s", st, out)
			if len(hr.Elements) != 2 || len(hr.Coils) != 0 ||
				hr.Elements[0].Kind != "contact" || hr.Elements[0].Ref != c.logixRef(e.Ref) || hr.Elements[0].Neg ||
				hr.Elements[1].Kind != "fn" || hr.Elements[1].Fn != instr || hr.Elements[1].Args != want {
				c.problemf(r.Name, "expected the edge helper XIC(%s)%s(%s), got %+v / %+v", e.Ref, instr, want, hr.Elements, hr.Coils)
			}
		case "fb":
			for _, a := range splitArgs(e.Args) {
				pin, val, isOut, ok := splitBinding(a)
				if !ok || isOut {
					continue
				}
				switch strings.ToUpper(pin) {
				case "PT", "PV":
					if isRef(val) {
						hr, ok := c.take(r.Name)
						if !ok {
							return
						}
						want := val + ", " + e.Inst + ".PRE"
						if len(hr.Elements) != 1 || len(hr.Coils) != 0 || hr.Elements[0].Kind != "fn" || hr.Elements[0].Fn != "MOVE" || hr.Elements[0].Args != want {
							c.problemf(r.Name, "expected the preset helper MOVE(%s), got %+v / %+v", want, hr.Elements, hr.Coils)
						}
					}
				case "R":
					c.resets = append(c.resets, val+"|"+e.Inst)
				}
			}
		}
	}
}

func (c *cmp) next(r ld.Rung) (ld.Element, bool) {
	if c.idx >= len(c.cur) {
		c.problemf(r.Name, "L5X rung ends early; source still has elements")
		return ld.Element{}, false
	}
	e := c.cur[c.idx]
	c.idx++
	return e, true
}

// series matches one source series against the got elements under the
// cursor. noCoils tells whether the rung has coils (a block that is the
// very last thing on a coil-less rung carries no DN contact).
func (c *cmp) series(r ld.Rung, elems []ld.Element, top, noCoils bool) {
	for i, e := range elems {
		last := i == len(elems)-1
		switch e.Kind {
		case "contact":
			g, ok := c.next(r)
			if !ok {
				return
			}
			if g.Kind != "contact" || g.Ref != c.logixRef(e.Ref) || g.Neg != e.Neg {
				c.problemf(r.Name, "element %+v, want contact %q neg=%v", g, c.logixRef(e.Ref), e.Neg)
			}
		case "fn":
			g, ok := c.next(r)
			if !ok {
				return
			}
			fn := strings.ToUpper(e.Fn)
			if e.Neg {
				fn = compareComplement[fn]
			}
			expr := false
			var args []string
			for _, a := range splitArgs(e.Args) {
				a = strings.TrimSpace(a)
				if !numRe.MatchString(a) && !isRef(a) {
					expr = true
				}
				args = append(args, c.logixRef(a))
			}
			if expr {
				if g.Kind != "fn" || g.Fn != "CMP" || !strings.Contains(g.Args, cmpOp[fn]) {
					c.problemf(r.Name, "element %+v, want CMP(… %s …)", g, cmpOp[fn])
				}
				continue
			}
			want := strings.Join(args, ", ")
			if g.Kind != "fn" || g.Fn != fn || g.Args != want {
				c.problemf(r.Name, "element %+v, want %s(%s)", g, fn, want)
			}
		case "edge":
			if e.Mode == "P" && (top && i == 0 && c.idx == 0 && c.pos > 0 || !top && c.legHead && i == 0) {
				g1, ok := c.next(r)
				if !ok {
					return
				}
				g2, ok := c.next(r)
				if !ok {
					return
				}
				st := "rt_" + sanitizeIdent(r.Name) + "_" + sanitizeIdent(e.Ref)
				if g1.Kind != "contact" || g1.Ref != c.logixRef(e.Ref) || g1.Neg || g2.Kind != "fn" || g2.Fn != "ONS" || g2.Args != st {
					c.problemf(r.Name, "elements %+v %+v, want XIC(%s)ONS(%s)", g1, g2, e.Ref, st)
				}
				continue
			}
			if len(c.outs) == 0 {
				c.problemf(r.Name, "edge %s%s has no helper output to read", e.Mode, e.Ref)
				return
			}
			out := c.outs[0]
			c.outs = c.outs[1:]
			g, ok := c.next(r)
			if !ok {
				return
			}
			if g.Kind != "contact" || g.Ref != out || g.Neg {
				c.problemf(r.Name, "element %+v, want the edge output XIC(%s)", g, out)
			}
		case "assign":
			as, err := ld.ParseAssignments(e.Text)
			if err != nil {
				c.problemf(r.Name, "assignment %q: %v", e.Text, err)
				continue
			}
			for _, a := range as {
				g, ok := c.next(r)
				if !ok {
					return
				}
				target := c.logixRef(a.Target)
				if g.Kind != "fn" || !dataInstr[g.Fn] || !containsOperand(g.Args, target) {
					c.problemf(r.Name, "element %+v, want a data instruction writing %s", g, target)
				}
			}
		case "branch":
			g, ok := c.next(r)
			if !ok {
				return
			}
			if g.Kind != "branch" || len(g.Legs) != len(e.Legs) {
				c.problemf(r.Name, "element %+v, want a branch of %d legs", g, len(e.Legs))
				continue
			}
			saved, savedIdx := c.cur, c.idx
			inherited := c.legHead
			c.legHead = top && i == 0 && savedIdx == 1 && c.pos > 0 || !top && inherited && i == 0
			for li, leg := range e.Legs {
				c.cur, c.idx = g.Legs[li], 0
				c.series(r, leg, false, noCoils)
				if c.idx < len(c.cur) {
					c.problemf(r.Name, "branch leg %d has %d extra element(s)", li, len(c.cur)-c.idx)
				}
			}
			c.legHead = inherited
			c.cur, c.idx = saved, savedIdx
		case "fb":
			g, ok := c.next(r)
			if !ok {
				return
			}
			typ := strings.ToUpper(e.Type)
			if g.Kind != "fb" || g.Inst != e.Inst || g.Type != typ || g.Args != "?, ?" {
				c.problemf(r.Name, "element %+v, want %s(%s,?,?)", g, typ, e.Inst)
			}
			if last && noCoils {
				continue
			}
			if splitsRung(typ) {
				// The rung must end here and the next one open with DN.
				if c.idx < len(c.cur) {
					c.problemf(r.Name, "a %s should end its rung; %d element(s) follow", typ, len(c.cur)-c.idx)
				}
				if len(c.got[c.pos-1].Coils) != 0 {
					c.problemf(r.Name, "a %s's rung should carry no coils", typ)
				}
				nr, ok := c.take(r.Name)
				if !ok {
					return
				}
				if nr.Comment != "" {
					c.problemf(r.Name, "continuation rung carries a comment %q", nr.Comment)
				}
				c.cur, c.idx = nr.Elements, 0
			}
			d, ok := c.next(r)
			if !ok {
				return
			}
			if d.Kind != "contact" || d.Ref != e.Inst+".DN" || d.Neg {
				c.problemf(r.Name, "element %+v after %s, want XIC(%s.DN)", d, e.Inst, e.Inst)
			}
		}
	}
}

// tags checks that every declared variable landed as a tag of the right
// type, scope and preset, and that nothing else did except the generated
// edge tags the rungs reference.
func (c *cmp) tags() {
	byScope := map[string]map[string]*l5x.Tag{}
	for _, t := range c.file.Controller.Tags {
		add(byScope, "", t)
	}
	for _, p := range c.file.Controller.Programs {
		for _, t := range p.Tags {
			add(byScope, p.Name, t)
		}
	}
	presets := c.presets()
	expected := map[string]bool{}
	for _, v := range c.src.Vars {
		scope := ""
		if strings.EqualFold(v.Section, "VAR") {
			scope = c.opts.Program
		}
		expected[scope+"/"+v.Name] = true
		t := byScope[scope][v.Name]
		if t == nil {
			c.problemf("", "variable %s (%s) has no %s tag", v.Name, v.Section, scopeName(scope))
			continue
		}
		typ := strings.ToUpper(strings.TrimSpace(v.Type))
		want := scalarTypes[typ]
		if want == "" {
			want = blockTypes[typ]
		}
		if want == "" && t.DataType != "" && strings.EqualFold(t.DataType, strings.TrimSpace(v.Type)) {
			want = t.DataType // a user-defined type, by its declared name
		}
		if typ == "TIME" {
			want = "DINT"
		}
		if m := arrayRe.FindStringSubmatch(strings.TrimSpace(v.Type)); m != nil {
			want = scalarTypes[strings.ToUpper(m[4])]
			if t.Dimensions != fmt.Sprint(atoi(m[2])+1) {
				c.problemf("", "tag %s has Dimensions=%q, want %d", v.Name, t.Dimensions, atoi(m[2])+1)
			}
		}
		if t.DataType != want {
			c.problemf("", "tag %s is %s, want %s", v.Name, t.DataType, want)
		}
		if pre, ok := presets[strings.ToLower(v.Name)]; ok {
			s, _ := t.Value.(map[string]any)
			got, _ := s["PRE"].(int64)
			if got != pre {
				c.problemf("", "tag %s PRE=%d, want %d", v.Name, got, pre)
			}
		}
	}
	for scope, tags := range byScope {
		for name := range tags {
			if expected[scope+"/"+name] {
				continue
			}
			if scope == c.opts.Program && (strings.HasPrefix(name, "rt_") || strings.HasPrefix(name, "ft_")) && tags[name].DataType == "BOOL" {
				continue
			}
			if scope == "" && name == c.opts.Side.Heartbeat {
				continue
			}
			c.problemf("", "unexpected %s tag %s", scopeName(scope), name)
		}
	}
}

// presets reads the literal presets the source binds, per instance.
func (c *cmp) presets() map[string]int64 {
	out := map[string]int64{}
	var walk func([]ld.Element)
	walk = func(elems []ld.Element) {
		for _, e := range elems {
			switch e.Kind {
			case "branch":
				for _, leg := range e.Legs {
					walk(leg)
				}
			case "fb":
				for _, a := range splitArgs(e.Args) {
					pin, val, isOut, ok := splitBinding(a)
					if !ok || isOut {
						continue
					}
					switch strings.ToUpper(pin) {
					case "PT":
						if ms, ok := parseTime(val); ok {
							out[strings.ToLower(e.Inst)] = ms
						}
					case "PV":
						if n, ok := parseInt(val); ok {
							out[strings.ToLower(e.Inst)] = n
						}
					}
				}
			}
		}
	}
	for _, r := range c.src.Rungs {
		walk(r.Elements)
	}
	return out
}

func add(m map[string]map[string]*l5x.Tag, scope string, t *l5x.Tag) {
	if m[scope] == nil {
		m[scope] = map[string]*l5x.Tag{}
	}
	m[scope][t.Name] = t
}

func scopeName(scope string) string {
	if scope == "" {
		return "controller"
	}
	return "program"
}

func atoi(s string) int {
	n := 0
	fmt.Sscanf(s, "%d", &n)
	return n
}

// roundTripAgainst runs the comparator over a document supplied by the
// caller instead of one freshly written — the test that proves the
// comparator can fail.
func roundTripAgainst(src string, opts Options, doc []byte) ([]byte, []string, error) {
	m, err := ld.Graph(src, opts.Libs...)
	if err != nil {
		return nil, nil, err
	}
	opts = opts.withDefaults(m.Name)
	f, err := l5x.Parse(doc)
	if err != nil {
		return doc, nil, err
	}
	got, err := l5x.Ladder(f, l5x.LadderOptions{Routine: opts.Program + "/" + opts.Routine})
	if err != nil {
		return doc, nil, err
	}
	c := &cmp{src: m, got: got.Rungs, types: map[string]string{}, opts: opts, file: f}
	for _, v := range m.Vars {
		c.types[strings.ToLower(v.Name)] = strings.ToUpper(strings.TrimSpace(v.Type))
	}
	notes := map[int]string{}
	for _, cm := range m.Comments {
		notes[cm.EndLine+1] = cm.Text
	}
	for _, r := range m.Rungs {
		c.rung(r, notes[r.Line])
	}
	c.tags()
	return doc, c.problems, nil
}

var dataInstr = map[string]bool{"MOVE": true, "ADD": true, "SUB": true, "MUL": true, "DIV": true, "ABS": true, "CPT": true}

// containsOperand reports whether a comma-separated operand list names
// target as one operand.
func containsOperand(args, target string) bool {
	for _, a := range strings.Split(args, ",") {
		if strings.TrimSpace(a) == target {
			return true
		}
	}
	return false
}
