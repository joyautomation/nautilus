package importer

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/joyautomation/nautilus/lang/l5x"
)

// parsed is one Logix rung with its term tree.
type parsed struct {
	rg    l5x.Rung
	terms []l5x.Term
	drop  bool // folded into another rung, or NOP
	// cont is the rung this one continues: the writer splits a rung at a
	// TOF or CTU and heads the rest with XIC(inst.DN); nautilus continues
	// from the block's Q in the same rung.
	cont *parsed
}

// routine is the per-routine lowering state: the folds found in the
// pre-pass and the output.
type routine struct {
	im    *importer
	sc    *scope
	p     *pou
	rungs []*parsed
	// edges maps a one-shot's output tag to the edge contact it folds to
	// (+Name / -Name); the helper rung itself is dropped.
	edges map[string]string
	// presetVar maps a timer/counter instance to the variable a MOVE
	// fed its preset from; the MOVE rung is dropped.
	presetVar map[string]string
	// resetCond maps a counter instance to the condition of the RES
	// rung that followed its count; the RES rung is dropped.
	resetCond map[string]string
	// resetRung is the index of the folded RES rung per counter.
	resetRung map[string]int
	// enable maps a rung index to the condition the writer put on an
	// en_ helper rung ahead of an Add-On Instruction call.
	enable map[int][]string
	// refs counts operand bases, for the preset fold.
	refs map[string]int
	// pulses counts the one-shot tags made for compound ONS; curName is
	// the rung being lowered, for their names.
	pulses  int
	curName string
	// synth are the tags this import made (one-shots), resolvable by name.
	synth map[string]bool
}

type refusal struct {
	reason, text string
}

func (r *refusal) Error() string { return r.reason + ": " + r.text }

func refuse(reason, format string, a ...any) *refusal {
	return &refusal{reason: reason, text: fmt.Sprintf(format, a...)}
}

var (
	numLit = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?([eE][+-]?\d+)?|\d+#[0-9A-Fa-f_]+)$`)
	genEn  = regexp.MustCompile(`^en_[A-Za-z0-9_]+$`)
)

// lowerRoutine imports one ladder routine into p.
func (im *importer) lowerRoutine(sc *scope, r *l5x.Routine, p *pou) {
	rt := &routine{im: im, sc: sc, p: p, edges: map[string]string{}, presetVar: map[string]string{}, resetCond: map[string]string{}, resetRung: map[string]int{}, synth: map[string]bool{}, enable: map[int][]string{}, refs: map[string]int{}}
	for _, rg := range r.Rungs {
		pr := &parsed{rg: rg}
		terms, err := l5x.ParseRung(rg.Text)
		if err != nil {
			rt.skip(pr, "parse", rg.Text)
			pr.drop = true
		} else {
			pr.terms = terms
			walkTerms(terms, func(in *l5x.Instr) {
				for _, a := range in.Args {
					rt.refs[strings.ToLower(baseOf(a))]++
				}
			})
		}
		rt.rungs = append(rt.rungs, pr)
	}
	p.total = len(r.Rungs)
	rt.prepass()
	for i, pr := range rt.rungs {
		if pr.drop {
			continue
		}
		rt.lower(i, pr)
	}
	p.complete = p.imported == p.total
	if !p.complete {
		p.header = append(p.header,
			fmt.Sprintf("Imported from %s by `naut logix import`: %d of %d rungs carried.", sc.owner, p.imported, p.total),
			"The rest are kept below as comments; this program is NOT deployable",
			"until they are rewritten in nautilus.")
	} else {
		p.header = append(p.header, fmt.Sprintf("Imported from %s by `naut logix import`.", sc.owner))
	}
	im.proj.Routines++
	if p.complete {
		im.proj.Complete++
	}
	im.proj.Rungs += p.total
	im.proj.Imported += p.imported
}

func baseOf(ref string) string {
	if i := strings.IndexAny(ref, ".["); i >= 0 {
		return ref[:i]
	}
	return ref
}

func (rt *routine) skip(pr *parsed, reason, text string) {
	rt.skipWhy(pr, reason, "", text)
}

func (rt *routine) skipWhy(pr *parsed, reason, why, text string) {
	header, lines := rungComment(pr.rg.Comment)
	if header != "" {
		lines = append(lines, header)
	}
	label := reason
	if why != "" {
		label = reason + ": " + why
	}
	rt.p.rungs = append(rt.p.rungs, outRung{Lines: lines, Skipped: label, Body: strings.TrimSpace(text)})
	rt.im.proj.Notes = append(rt.im.proj.Notes, Note{Owner: rt.sc.owner, Rung: pr.rg.Number, Reason: reason, Why: why, Text: strings.TrimSpace(text)})
}

// ── the pre-pass: folding the writer's idioms back ──────────────────────────

func instrIs(t l5x.Term, mnemonics ...string) *l5x.Instr {
	if t.Instr == nil {
		return nil
	}
	for _, m := range mnemonics {
		if strings.EqualFold(t.Instr.Mnemonic, m) {
			return t.Instr
		}
	}
	return nil
}

func (rt *routine) prepass() {
	for i, pr := range rt.rungs {
		if pr.drop {
			continue
		}
		terms := pr.terms
		if len(terms) == 0 {
			// An empty rung: nothing to carry but its comment.
			if h, lines := rungComment(pr.rg.Comment); h != "" || len(lines) > 0 {
				if h != "" {
					lines = append(lines, h)
				}
				rt.p.rungs = append(rt.p.rungs, outRung{Lines: lines, Skipped: "empty", Body: ";"})
			}
			pr.drop = true
			rt.p.imported++
			continue
		}
		// XIC(x)OSR(st,q) / OSF: the edge helper. q reads as +x / -x.
		if len(terms) == 2 {
			if x := instrIs(terms[0], "XIC"); x != nil && len(x.Args) == 1 {
				if os := instrIs(terms[1], "OSR", "OSF"); os != nil && len(os.Args) == 2 {
					sign := "+"
					if strings.EqualFold(os.Mnemonic, "OSF") {
						sign = "-"
					}
					rt.edges[strings.ToLower(os.Args[1])] = sign + x.Args[0]
					rt.edges["st:"+strings.ToLower(os.Args[0])] = ""
					pr.drop = true
					rt.p.imported++
					continue
				}
			}
		}
		// MOVE(v,inst.PRE) ahead of the block's rung: a variable preset.
		if len(terms) == 1 {
			if mv := instrIs(terms[0], "MOVE", "MOV"); mv != nil && len(mv.Args) == 2 && strings.HasSuffix(strings.ToUpper(mv.Args[1]), ".PRE") && isPlainRef(mv.Args[0]) {
				inst := mv.Args[1][:len(mv.Args[1])-4]
				if blk := rt.blockRungAfter(i, inst); blk != "" {
					v := rt.sc.lookup(mv.Args[0])
					f := rt.sc.lookup(inst)
					if v != nil && f != nil && v.tag != nil && v.dims == "" {
						vt := strings.ToUpper(v.dtype)
						ok := false
						switch strings.ToUpper(f.dtype) {
						case "TIMER":
							// The variable becomes a TIME: only when the
							// preset is all it feeds.
							ok = vt == "DINT" && rt.refs[strings.ToLower(mv.Args[0])] == rt.presetMoves(mv.Args[0])
						case "COUNTER":
							ok = vt == "DINT" || vt == "INT" || vt == "SINT"
						}
						if ok {
							rt.presetVar[strings.ToLower(inst)] = mv.Args[0]
							pr.drop = true
							rt.p.imported++
							continue
						}
					}
				}
			}
		}
		// <cond>RES(c) after the rung that counts c: the CTU's R pin.
		if n := len(terms); n >= 2 {
			if res := instrIs(terms[n-1], "RES"); res != nil && len(res.Args) == 1 {
				c := strings.ToLower(res.Args[0])
				if f := rt.sc.lookup(res.Args[0]); f != nil && strings.EqualFold(f.dtype, "COUNTER") && rt.countRungBefore(i, c) {
					if cond, ok := rt.boolCond(terms[:n-1]); ok {
						rt.resetCond[c] = cond
						rt.resetRung[c] = i
						pr.drop = true
						rt.p.imported++
						continue
					}
				}
			}
		}
		// <cond>OTE(en_x) ahead of a call that takes en_x: the writer's
		// enable helper for an Add-On Instruction with a rung condition.
		if n := len(terms); n >= 2 && i+1 < len(rt.rungs) {
			if ote := instrIs(terms[n-1], "OTE"); ote != nil && len(ote.Args) == 1 && genEn.MatchString(ote.Args[0]) {
				next := rt.rungs[i+1]
				if len(next.terms) > 0 && next.terms[0].Instr != nil && rt.im.aois[strings.ToLower(next.terms[0].Instr.Mnemonic)] != nil {
					uses := false
					for _, a := range next.terms[0].Instr.Args {
						if strings.EqualFold(a, ote.Args[0]) {
							uses = true
						}
					}
					if uses {
						if cond, err := rt.series([][]l5x.Term{terms[:n-1]}, false, true); err == nil {
							rt.enable[i+1] = cond
							rt.edges["st:"+strings.ToLower(ote.Args[0])] = ""
							pr.drop = true
							rt.p.imported++
							continue
						}
					}
				}
			}
		}
		// XIC(inst.DN)… right after the rung a TOF or CTU ends: the
		// writer's continuation.
		if x := instrIs(terms[0], "XIC"); x != nil && len(x.Args) == 1 && strings.HasSuffix(strings.ToUpper(x.Args[0]), ".DN") && len(terms) > 1 {
			inst := x.Args[0][:len(x.Args[0])-3]
			for j := i - 1; j >= 0 && j >= i-2; j-- {
				prev := rt.rungs[j]
				if prev.drop {
					continue
				}
				if n := len(prev.terms); n > 0 {
					if blk := instrIs(prev.terms[n-1], "TOF", "CTU"); blk != nil && len(blk.Args) > 0 && strings.EqualFold(blk.Args[0], inst) {
						pr.cont = prev
					}
				}
				break
			}
		}
		// NOP(): nothing to carry but the comment.
		if len(terms) == 1 && instrIs(terms[0], "NOP") != nil {
			_, lines := rungComment(pr.rg.Comment)
			if h, _ := rungComment(pr.rg.Comment); h != "" {
				lines = []string{h}
			}
			if len(lines) > 0 {
				rt.p.rungs = append(rt.p.rungs, outRung{Lines: lines, Skipped: "NOP", Body: "NOP();"})
			}
			pr.drop = true
			rt.p.imported++
		}
	}
}

func isPlainRef(s string) bool {
	return s != "" && !strings.ContainsAny(s, ".[:?") && !numLit.MatchString(s)
}

func (rt *routine) presetMoves(v string) int {
	n := 0
	for _, pr := range rt.rungs {
		if len(pr.terms) == 1 {
			if mv := instrIs(pr.terms[0], "MOVE", "MOV"); mv != nil && len(mv.Args) == 2 && strings.EqualFold(mv.Args[0], v) && strings.HasSuffix(strings.ToUpper(mv.Args[1]), ".PRE") {
				n++
			}
		}
	}
	return n
}

// blockRungAfter finds the block instruction that owns inst in the next
// few rungs (the writer puts the MOVE directly ahead; a person may put a
// comment rung between).
func (rt *routine) blockRungAfter(i int, inst string) string {
	for j := i + 1; j < len(rt.rungs) && j <= i+3; j++ {
		found := ""
		walkTerms(rt.rungs[j].terms, func(in *l5x.Instr) {
			if blockInstr[strings.ToUpper(in.Mnemonic)] && len(in.Args) > 0 && strings.EqualFold(in.Args[0], inst) {
				found = strings.ToUpper(in.Mnemonic)
			}
		})
		if found != "" {
			return found
		}
	}
	return ""
}

// countRungBefore reports whether the CTU that counts c sits within the
// three rungs before i, with every rung between touching c (the writer's
// continuation and captures).
func (rt *routine) countRungBefore(i int, c string) bool {
	for j := i - 1; j >= 0 && j >= i-3; j-- {
		counts, touches := false, false
		walkTerms(rt.rungs[j].terms, func(in *l5x.Instr) {
			if strings.EqualFold(in.Mnemonic, "CTU") && len(in.Args) > 0 && strings.EqualFold(in.Args[0], c) {
				counts = true
			}
			for _, a := range in.Args {
				if strings.EqualFold(baseOf(a), c) {
					touches = true
				}
			}
		})
		if counts {
			return true
		}
		if !touches {
			return false
		}
	}
	return false
}

// boolCond renders a condition series as the pin expression a nautilus
// block takes: a tag, NOT(tag), AND(...) / OR(...) of those.
func (rt *routine) boolCond(terms []l5x.Term) (string, bool) {
	var parts []string
	for _, t := range terms {
		if t.Instr == nil {
			var legs []string
			for _, leg := range t.Legs {
				s, ok := rt.boolCond(leg)
				if !ok {
					return "", false
				}
				legs = append(legs, s)
			}
			if len(legs) == 1 {
				parts = append(parts, legs[0])
			} else {
				parts = append(parts, "OR("+strings.Join(legs, ", ")+")")
			}
			continue
		}
		in := t.Instr
		if len(in.Args) != 1 {
			return "", false
		}
		ref, err := rt.ref(in.Args[0], "")
		if err != nil {
			return "", false
		}
		switch strings.ToUpper(in.Mnemonic) {
		case "XIC":
			parts = append(parts, ref)
		case "XIO":
			parts = append(parts, "NOT("+ref+")")
		default:
			return "", false
		}
	}
	switch len(parts) {
	case 0:
		return "", false
	case 1:
		return parts[0], true
	}
	return "AND(" + strings.Join(parts, ", ") + ")", true
}

// ── lowering one rung ───────────────────────────────────────────────────────

// lower emits the nautilus rung(s) for pr, or skips it with a reason.
func (rt *routine) lower(i int, pr *parsed) {
	rt.curName = fmt.Sprintf("r%d", pr.rg.Number)
	bodies, err := rt.split(pr.terms, rt.enable[i])
	if err != nil {
		rt.skipWhy(pr, err.reason, err.text, pr.rg.Text)
		return
	}
	if len(bodies) == 0 {
		rt.skip(pr, "empty", pr.rg.Text)
		return
	}
	header, lines := rungComment(pr.rg.Comment)
	// A continuation rung (XIC(inst.DN) after the block's rung) folds in:
	// its rest, on the block's rung, with power from Q.
	for j, next := range rt.rungs {
		if next.cont != pr || next.drop {
			continue
		}
		if len(bodies) != 1 {
			break
		}
		// XIC(c.DN)OTE(x) with the counter's RES between: the writer's
		// capture of `Q => x`, which it orders after the reset. Bind it
		// so the trip back lands in the same order.
		if len(next.terms) == 2 {
			if ote := instrIs(next.terms[1], "OTE"); ote != nil && len(ote.Args) == 1 {
				inst := strings.ToLower(next.terms[0].Instr.Args[0])
				inst = inst[:len(inst)-3]
				if k, folded := rt.resetRung[inst]; folded && k < j && k > i {
					if target, err := rt.ref(ote.Args[0], ""); err == nil {
						blk := bodies[0][len(bodies[0])-1]
						bodies[0][len(bodies[0])-1] = strings.TrimSuffix(blk, ")") + ", Q => " + target + ")"
						next.drop = true
						rt.p.imported++
						break
					}
				}
			}
		}
		more, err := rt.split(next.terms[1:], bodies[0])
		if err != nil {
			rt.skipWhy(next, err.reason, err.text, next.rg.Text)
			next.drop = true
			break
		}
		bodies = more
		if h, l := rungComment(next.rg.Comment); h != "" || len(l) > 0 {
			lines = append(lines, l...)
			if h != "" {
				lines = append(lines, h)
			}
		}
		next.drop = true
		rt.p.imported++
	}
	for k, body := range bodies {
		name := fmt.Sprintf("r%d", pr.rg.Number)
		if k > 0 {
			name += string(rune('a' + k))
		}
		out := outRung{Name: name, Body: strings.Join(body, " ")}
		if k == 0 {
			out.Comment, out.Lines = header, lines
		}
		rt.p.rungs = append(rt.p.rungs, out)
	}
	rt.p.imported++
}

var coilMode = map[string]string{"OTE": "", "OTL": "S", "OTU": "R"}

// split walks the rung's top-level series, emitting a nautilus rung per
// output leg: a coil mid-rung, a block whose rung-in passes through, a
// branch of output legs. The condition so far is the prefix every leg
// shares — which is what Logix's 2-D wiring means and the text form
// cannot say in one rung.
func (rt *routine) split(terms []l5x.Term, prefix []string) ([][]string, *refusal) {
	var out [][]string
	parts := append([]string(nil), prefix...)
	// dirty: conditions added since the last output leg was emitted. A
	// rung that ends dirty has conditions driving nothing.
	dirty := false
	emit := func(elems []string) {
		out = append(out, append([]string(nil), elems...))
		dirty = false
	}
	add := func(elems ...string) {
		parts = append(parts, elems...)
		dirty = true
	}
	for i := 0; i < len(terms); i++ {
		t := terms[i]
		last := i == len(terms)-1
		if t.Instr == nil {
			kind := rt.branchKind(t)
			switch kind {
			case "coils":
				var coils []string
				for _, leg := range t.Legs {
					c, err := rt.coil(leg[0].Instr)
					if err != nil {
						return nil, err
					}
					coils = append(coils, c)
				}
				emit(append(append([]string(nil), parts...), coils...))
				if last {
					return out, nil
				}
				continue
			case "outputs", "mixed":
				// A leg that ends in coils is a rung of its own: prefix +
				// the leg. Its coils pass power on in Logix, so the branch
				// still carries the leg's conditions for what follows.
				var condLegs [][]l5x.Term
				for _, leg := range t.Legs {
					if k := firstCoil(leg); k >= 0 {
						legOut, err := rt.split(leg, parts)
						if err != nil {
							return nil, err
						}
						out = append(out, legOut...)
						condLegs = append(condLegs, leg[:k])
					} else {
						condLegs = append(condLegs, leg)
					}
				}
				if last {
					return out, nil
				}
				legs, hoisted, err := rt.hoistBlocks(condLegs, parts)
				if err != nil {
					return nil, err
				}
				out = append(out, hoisted...)
				if allEmpty(legs) {
					continue
				}
				s, err := rt.series(legs, true, len(parts) == 0)
				if err != nil {
					return nil, err
				}
				add(s...)
				continue
			}
			// A timer or counter inside a leg passes its rung-in on in
			// Logix, so it is its own output leg: prefix + the leg's
			// conditions before it + the block, on a rung of its own;
			// the leg keeps its conditions and loses the block.
			legs, hoisted, err := rt.hoistBlocks(t.Legs, parts)
			if err != nil {
				return nil, err
			}
			out = append(out, hoisted...)
			if allEmpty(legs) {
				continue // the branch was blocks alone; they are rungs now
			}
			s, err := rt.series(legs, true, len(parts) == 0)
			if err != nil {
				return nil, err
			}
			add(s...)
			continue
		}
		in := t.Instr
		m := strings.ToUpper(in.Mnemonic)
		switch {
		case m == "XIC" || m == "XIO":
			if len(in.Args) != 1 {
				return nil, refuse(m, "%d operands", len(in.Args))
			}
			if !last && len(parts) == 0 {
				// A compound condition's one-shot is the ONS case below.
				if ons := instrIs(terms[i+1], "ONS"); ons != nil {
					ref, err := rt.ref(in.Args[0], "")
					if err != nil {
						return nil, err
					}
					sign := "+"
					if m == "XIO" {
						sign = "-"
					}
					add(sign + ref)
					if len(ons.Args) == 1 {
						rt.edges["st:"+strings.ToLower(ons.Args[0])] = ""
					}
					i++
					continue
				}
			}
			c, err := rt.contact(in)
			if err != nil {
				return nil, err
			}
			add(c...)
		case m == "ONS":
			// A one-shot of the condition so far. Driving one OTE, it is
			// the rising-edge coil ( P x ). Driving anything else, the
			// pulse gets a tag of its own: <cond> ( P os_rN ), then
			// os_rN <rest> — exactly what the ONS did, in two rungs.
			if len(parts) == 0 {
				return nil, refuse("ONS", "a one-shot with nothing ahead of it")
			}
			if len(in.Args) == 1 {
				rt.edges["st:"+strings.ToLower(in.Args[0])] = ""
			}
			if i+2 == len(terms) {
				if ote := instrIs(terms[i+1], "OTE"); ote != nil && len(ote.Args) == 1 {
					ref, err := rt.ref(ote.Args[0], "")
					if err != nil {
						return nil, err
					}
					rt.markWritten(ote.Args[0])
					emit(append(parts, "( P "+ref+" )"))
					return out, nil
				}
			}
			if i+1 == len(terms) {
				return nil, refuse("ONS", "a one-shot at the rung's end drives nothing")
			}
			pulse := rt.pulseTag()
			emit(append(append([]string(nil), parts...), "( P "+pulse+" )"))
			rest, err := rt.split(terms[i+1:], []string{pulse})
			if err != nil {
				return nil, err
			}
			return append(out, rest...), nil
		case m == "OSR" || m == "OSF":
			// OSR(st,q): q is TRUE one scan when the rung-in rises; the
			// rung-in passes on. That is the ( P q ) coil as an output
			// leg; OSF is ( N q ).
			if len(in.Args) != 2 {
				return nil, refuse(m, "%d operands", len(in.Args))
			}
			if len(parts) == 0 {
				return nil, refuse(m, "a one-shot with nothing ahead of it")
			}
			q, err := rt.ref(in.Args[1], "")
			if err != nil {
				return nil, err
			}
			rt.markWritten(in.Args[1])
			rt.edges["st:"+strings.ToLower(in.Args[0])] = ""
			mode := "P"
			if m == "OSF" {
				mode = "N"
			}
			emit(append(append([]string(nil), parts...), "( "+mode+" "+q+" )"))
			if last {
				return out, nil
			}
		case compareFn[m] != "":
			s, err := rt.compare(in)
			if err != nil {
				return nil, err
			}
			add(s)
		case blockInstr[m]:
			text, err := rt.block(in)
			if err != nil {
				return nil, err
			}
			if last {
				add(text)
				emit(parts)
				return out, nil
			}
			// A TON's done bit is the rung-in delayed: XIC(t.DN) right
			// after it is the writer's inline continuation, and power
			// flows on from Q. Anything else after a block gets the
			// rung-in: the block ends an output leg.
			if m == "TON" {
				if nx := instrIs(terms[i+1], "XIC"); nx != nil && len(nx.Args) == 1 && strings.EqualFold(nx.Args[0], in.Args[0]+".DN") {
					add(text)
					i++
					continue
				}
			}
			emit(append(append([]string(nil), parts...), text))
		case coilMode[m] != "" || m == "OTE":
			c, err := rt.coil(in)
			if err != nil {
				return nil, err
			}
			if last {
				emit(append(parts, c))
				return out, nil
			}
			// Coils in series at the tail: all of them are this rung's.
			j := i + 1
			coils := []string{c}
			for ; j < len(terms); j++ {
				nx := terms[j].Instr
				if nx == nil || (coilMode[strings.ToUpper(nx.Mnemonic)] == "" && strings.ToUpper(nx.Mnemonic) != "OTE") {
					break
				}
				cc, err := rt.coil(nx)
				if err != nil {
					return nil, err
				}
				coils = append(coils, cc)
			}
			if j == len(terms) {
				emit(append(parts, coils...))
				return out, nil
			}
			// Mid-rung: an output leg of its own; power continues.
			emit(append(append([]string(nil), parts...), coils...))
			i = j - 1
		case rt.im.aois[strings.ToLower(in.Mnemonic)] != nil:
			text, powerOut, err := rt.aoiCall(in)
			if err != nil {
				return nil, err
			}
			add(text)
			if last {
				emit(parts)
				return out, nil
			}
			if !last {
				// The writer's continuation: XIC(inst.<out>) or XIC(en_x).
				if nx := instrIs(terms[i+1], "XIC"); nx != nil && len(nx.Args) == 1 {
					a := nx.Args[0]
					if (powerOut != "" && strings.EqualFold(a, in.Args[0]+"."+powerOut)) || genEn.MatchString(a) {
						i++
					}
				}
			}
		case m == "NOP":
			// A NOP among other instructions carries nothing.
		case m == "RES":
			return nil, refuse("RES", "a reset away from its counter's rung (nautilus binds R := on the CTU)")
		case m == "AFI":
			return nil, refuse("AFI", "always-false instruction")
		case assignInstr[m]:
			text, err := rt.dataInstr(in)
			if err != nil {
				return nil, err
			}
			// A box passes its rung-in on; alone at the rung's end it is
			// the rung's output.
			add(text)
			if last {
				emit(parts)
				return out, nil
			}
		case m == "LIMIT":
			texts, err := rt.limitInstr(in)
			if err != nil {
				return nil, err
			}
			add(texts...)
		case dataOp[m]:
			return nil, refuse(m, "a data operation with no assignment form")
		default:
			return nil, refuse(m, "no nautilus form")
		}
	}
	if dirty {
		// Data boxes are outputs: a rung (or a branch) of MOVEs alone is
		// complete as it stands.
		if containsBox(parts) {
			emit(parts)
		} else if len(out) == 0 {
			// Conditions with no output: Logix allows it; nautilus does not.
			return nil, refuse("no-output", "a rung with conditions and no coil or block")
		}
	}
	// Conditions left after the last output leg (a timer hoisted out of a
	// branch, a coil mid-rung with a trailing contact) drive nothing in
	// Logix either; they are dropped.
	return out, nil
}

// assignInstr are the data instructions with an assignment form.
var assignInstr = map[string]bool{"MOVE": true, "MOV": true, "ADD": true, "SUB": true, "MUL": true, "DIV": true, "MOD": true, "NEG": true, "ABS": true, "SQR": true, "XPY": true, "CPT": true, "CLR": true}

var dataOp = map[string]bool{"COP": true, "CPS": true, "FLL": true, "BTD": true, "LIMIT": true, "AND": true, "OR": true, "XOR": true, "NOT": true, "TRN": true, "SWPB": true, "DTOS": true, "STOD": true, "RTOS": true, "STOR": true, "CONCAT": true, "SIZE": true, "FAL": true, "FSC": true, "AVE": true, "SRT": true, "STD": true}

// branchKind classifies a branch: "coils" (every leg one coil), "outputs"
// (every leg ends in coils), "mixed", or "" for a condition branch.
func (rt *routine) branchKind(t l5x.Term) string {
	coils, outputs, conds := 0, 0, 0
	for _, leg := range t.Legs {
		switch {
		case len(leg) == 1 && leg[0].Instr != nil && isCoil(leg[0].Instr):
			coils++
		case len(leg) > 0 && leg[len(leg)-1].Instr != nil && isCoil(leg[len(leg)-1].Instr):
			outputs++
		default:
			conds++
		}
	}
	switch {
	case coils == len(t.Legs):
		return "coils"
	case coils+outputs == len(t.Legs):
		return "outputs"
	case coils+outputs > 0:
		return "mixed"
	}
	return ""
}

func isCoil(in *l5x.Instr) bool {
	_, ok := coilMode[strings.ToUpper(in.Mnemonic)]
	return ok
}

// series lowers branch legs into one `[ a | b ]` element (with nested
// branches inside legs), or a leg's own series when branch is false.
// atHead says the branch takes power from the rail, so a one-shot at a
// leg's head sees that leg's first contact alone and is +Name.
func (rt *routine) series(legs [][]l5x.Term, branch, atHead bool) ([]string, *refusal) {
	var legTexts []string
	for _, leg := range legs {
		var parts []string
		for i := 0; i < len(leg); i++ {
			t := leg[i]
			if t.Instr == nil {
				if k := rt.branchKind(t); k != "" {
					return nil, refuse("branch", "coils inside a branch leg")
				}
				s, err := rt.series(t.Legs, true, atHead && len(parts) == 0)
				if err != nil {
					return nil, err
				}
				parts = append(parts, s...)
				continue
			}
			in := t.Instr
			m := strings.ToUpper(in.Mnemonic)
			switch {
			case m == "XIC" || m == "XIO":
				if i+1 < len(leg) && instrIs(leg[i+1], "ONS") != nil {
					if !atHead || len(parts) != 0 || len(in.Args) != 1 {
						return nil, refuse("ONS", "a one-shot of a compound condition inside a branch leg")
					}
					ref, err := rt.ref(in.Args[0], "")
					if err != nil {
						return nil, err
					}
					sign := "+"
					if m == "XIO" {
						sign = "-"
					}
					parts = append(parts, sign+ref)
					i++
					continue
				}
				c, err := rt.contact(in)
				if err != nil {
					return nil, err
				}
				parts = append(parts, c...)
			case compareFn[m] != "":
				s, err := rt.compare(in)
				if err != nil {
					return nil, err
				}
				parts = append(parts, s)
			case m == "TON":
				text, err := rt.block(in)
				if err != nil {
					return nil, err
				}
				if nx := i + 1; nx < len(leg) {
					if x := instrIs(leg[nx], "XIC"); x != nil && len(x.Args) == 1 && strings.EqualFold(x.Args[0], in.Args[0]+".DN") {
						parts = append(parts, text)
						i++
						continue
					}
				}
				return nil, refuse("TON", "a timer inside a branch leg passes its rung-in through in Logix; nautilus continues from Q")
			case blockInstr[m]:
				return nil, refuse(m, "a %s inside a branch leg", m)
			case isCoil(in):
				return nil, refuse("branch", "a coil inside a branch leg with logic after it")
			case m == "ONS":
				return nil, refuse("ONS", "a one-shot inside a branch leg")
			case assignInstr[m]:
				text, err := rt.dataInstr(in)
				if err != nil {
					return nil, err
				}
				parts = append(parts, text)
			case m == "LIMIT":
				texts, err := rt.limitInstr(in)
				if err != nil {
					return nil, err
				}
				parts = append(parts, texts...)
			case dataOp[m]:
				return nil, refuse(m, "a data operation with no assignment form")
			default:
				return nil, refuse(m, "no nautilus form")
			}
		}
		legTexts = append(legTexts, strings.Join(parts, " "))
	}
	if !branch {
		return legTexts, nil
	}
	return []string{"[ " + strings.Join(legTexts, " | ") + " ]"}, nil
}

// contact lowers XIC/XIO. It returns elements, plural, because a timer's
// TT bit has no IEC member: TT is EN AND NOT DN, so XIC(t.TT) is the two
// contacts t.IN /t.Q and XIO(t.TT) the branch [ /t.IN | t.Q ].
func (rt *routine) contact(in *l5x.Instr) ([]string, *refusal) {
	if len(in.Args) != 1 {
		return nil, refuse(strings.ToUpper(in.Mnemonic), "%d operands", len(in.Args))
	}
	a := in.Args[0]
	neg := strings.EqualFold(in.Mnemonic, "XIO")
	if e, ok := rt.edges[strings.ToLower(a)]; ok && e != "" {
		if neg {
			return nil, refuse("edge", "XIO of a one-shot output")
		}
		ref, err := rt.ref(e[1:], "")
		if err != nil {
			return nil, err
		}
		return []string{e[:1] + ref}, nil
	}
	if strings.HasSuffix(strings.ToUpper(a), ".TT") {
		base := a[:len(a)-3]
		if f := rt.sc.lookup(baseOf(base)); f != nil && strings.EqualFold(f.dtype, "TIMER") {
			ref, err := rt.ref(base, "")
			if err != nil {
				return nil, err
			}
			if neg {
				return []string{"[ /" + ref + ".IN | " + ref + ".Q ]"}, nil
			}
			return []string{ref + ".IN", "/" + ref + ".Q"}, nil
		}
	}
	ref, err := rt.ref(a, "")
	if err != nil {
		return nil, err
	}
	if neg {
		return []string{"/" + ref}, nil
	}
	return []string{ref}, nil
}

func (rt *routine) coil(in *l5x.Instr) (string, *refusal) {
	if len(in.Args) != 1 {
		return "", refuse(strings.ToUpper(in.Mnemonic), "%d operands", len(in.Args))
	}
	ref, err := rt.ref(in.Args[0], "")
	if err != nil {
		return "", err
	}
	rt.markWritten(in.Args[0])
	mode := coilMode[strings.ToUpper(in.Mnemonic)]
	if mode == "" {
		return "( " + ref + " )", nil
	}
	return "( " + mode + " " + ref + " )", nil
}

func (rt *routine) markWritten(operand string) {
	if f := rt.sc.lookup(baseOf(operand)); f != nil && f.ext && rt.sc.ctrl != nil && f.tag != nil {
		if rt.im.written == nil {
			rt.im.written = map[string]bool{}
		}
		rt.im.written[strings.ToLower(f.name)] = true
	}
}

var compareFn = map[string]string{"EQ": "EQ", "EQU": "EQ", "NE": "NE", "NEQ": "NE", "GT": "GT", "GRT": "GT", "GE": "GE", "GEQ": "GE", "LT": "LT", "LES": "LT", "LE": "LE", "LEQ": "LE", "CMP": "CMP"}

var cmpOp = map[string]string{"=": "EQ", "<>": "NE", "<": "LT", "<=": "LE", ">": "GT", ">=": "GE"}

func (rt *routine) compare(in *l5x.Instr) (string, *refusal) {
	fn := compareFn[strings.ToUpper(in.Mnemonic)]
	if fn == "CMP" {
		return rt.cmpInstr(in)
	}
	if len(in.Args) != 2 {
		return "", refuse(strings.ToUpper(in.Mnemonic), "%d operands", len(in.Args))
	}
	var ops []string
	for _, a := range in.Args {
		if numLit.MatchString(a) {
			ops = append(ops, a)
			continue
		}
		ref, err := rt.ref(a, "")
		if err != nil {
			return "", err
		}
		ops = append(ops, ref)
	}
	return fn + "(" + strings.Join(ops, ", ") + ")", nil
}

var timerMember = map[string]string{"DN": "Q", "ACC": "ET", "PRE": "PT", "EN": "IN"}
var counterMember = map[string]string{"DN": "Q", "ACC": "CV", "PRE": "PV", "CU": "CU"}

// ref rewrites one operand reference: the base through the identifier
// mapping and the scope, timer and counter members to their IEC names,
// struct members and indexes verbatim. blockType is the instruction
// driving a TIMER/COUNTER base, or "" for a read.
func (rt *routine) ref(a string, blockType string) (string, *refusal) {
	if a == "?" || a == "" {
		return "", refuse("operand", "an unset operand")
	}
	if strings.Contains(a, ":") {
		return "", refuse("io", "module I/O operand %s; alias it to a tag", a)
	}
	base := baseOf(a)
	rest := a[len(base):]
	if rt.synth[strings.ToLower(base)] && rest == "" {
		return base, nil // a one-shot tag this import made
	}
	f := rt.sc.lookup(base)
	if f == nil {
		return "", refuse("undefined", "tag %s is not in scope", base)
	}
	up := strings.ToUpper(f.dtype)
	if up == "TIMER" || up == "COUNTER" {
		if f.dims != "" {
			return "", refuse("array", "an array of %s", up)
		}
		if owner, ok := rt.sc.blockOwner[strings.ToLower(f.name)]; ok && blockType == "" && rt.sc.ctrl != nil {
			if !strings.EqualFold(rt.sc.owner, rt.ownerProgram()+"/"+owner) {
				return "", refuse("shared-block", "%s is run by routine %s; a block instance belongs to one nautilus program", f.name, owner)
			}
		}
		if blockType != "" && rest != "" {
			return "", refuse("operand", "%s: a block instance with a member", a)
		}
		if !rt.im.declareFound(rt.sc, rt.p, f, blockType) {
			return "", refuse("block-type", "%s is driven by two different instructions", f.name)
		}
		if rest == "" {
			return f.name, nil
		}
		if !strings.HasPrefix(rest, ".") {
			return "", refuse("member", "%s on a %s", rest, up)
		}
		table := timerMember
		if up == "COUNTER" {
			table = counterMember
		}
		m, ok := table[strings.ToUpper(rest[1:])]
		if !ok {
			return "", refuse("member", "%s.%s has no IEC member", f.name, rest[1:])
		}
		return f.name + "." + m, nil
	}
	if blockType != "" {
		return "", refuse("block-type", "%s is a %s, not a timer or counter", f.name, f.dtype)
	}
	if !rt.im.declareFound(rt.sc, rt.p, f, "") {
		return "", refuse("type", "%s: type %s has no nautilus declaration", f.name, f.dtype)
	}
	out, err := rt.accessors(rest)
	if err != nil {
		return "", err
	}
	return f.name + out, nil
}

func (rt *routine) ownerProgram() string {
	if i := strings.Index(rt.sc.owner, "/"); i >= 0 {
		return rt.sc.owner[:i]
	}
	return rt.sc.owner
}

// accessors rewrites a member/index chain: .Name through the identifier
// mapping, [i] verbatim with an identifier index mapped, .3 refused.
func (rt *routine) accessors(rest string) (string, *refusal) {
	var b strings.Builder
	for rest != "" {
		switch rest[0] {
		case '.':
			j := 1
			for j < len(rest) && rest[j] != '.' && rest[j] != '[' {
				j++
			}
			name := rest[1:j]
			if name == "" || (name[0] >= '0' && name[0] <= '9') {
				return "", refuse("bit", "bit-level access .%s has no nautilus form", name)
			}
			b.WriteString("." + l5x.Ident(name))
			rest = rest[j:]
		case '[':
			j := strings.IndexByte(rest, ']')
			if j < 0 {
				return "", refuse("operand", "unclosed index")
			}
			idx := strings.TrimSpace(rest[1:j])
			if strings.Contains(idx, ",") {
				return "", refuse("array", "a multi-dimensional index")
			}
			if !numLit.MatchString(idx) {
				if !isPlainRef(idx) {
					return "", refuse("operand", "an index expression %s", idx)
				}
				f := rt.sc.lookup(idx)
				if f == nil {
					return "", refuse("undefined", "index tag %s is not in scope", idx)
				}
				if !rt.im.declareFound(rt.sc, rt.p, f, "") {
					return "", refuse("type", "%s: type %s has no nautilus declaration", f.name, f.dtype)
				}
				idx = f.name
			}
			b.WriteString("[" + idx + "]")
			rest = rest[j+1:]
		default:
			return "", refuse("operand", "unreadable reference tail %q", rest)
		}
	}
	return b.String(), nil
}

// block lowers TON/TOF/CTU(inst,?,?) to the nautilus call with its preset
// (and a folded reset) bound.
func (rt *routine) block(in *l5x.Instr) (string, *refusal) {
	m := strings.ToUpper(in.Mnemonic)
	if m == "RTO" || m == "CTD" {
		return "", refuse(m, "no IEC block with these semantics in the subset")
	}
	if len(in.Args) < 1 {
		return "", refuse(m, "no instance operand")
	}
	inst := in.Args[0]
	name, err := rt.ref(inst, m)
	if err != nil {
		return "", err
	}
	f := rt.sc.lookup(inst)
	var binds []string
	pv, hasVar := rt.presetVar[strings.ToLower(inst)]
	switch m {
	case "TON", "TOF":
		if !strings.EqualFold(f.dtype, "TIMER") {
			return "", refuse("block-type", "%s is a %s, not a TIMER", inst, f.dtype)
		}
		if hasVar {
			v := rt.sc.lookup(pv)
			rt.p.declare(decl{Name: v.name, Type: "TIME", Section: rt.sectionOf(v), Comment: "DINT milliseconds in the export; feeds a timer preset"})
			binds = append(binds, "PT := "+v.name)
		} else {
			binds = append(binds, "PT := "+timeLit(presetOf(f)))
		}
	case "CTU":
		if !strings.EqualFold(f.dtype, "COUNTER") {
			return "", refuse("block-type", "%s is a %s, not a COUNTER", inst, f.dtype)
		}
		if hasVar {
			v := rt.sc.lookup(pv)
			if !rt.im.declareFound(rt.sc, rt.p, v, "") {
				return "", refuse("type", "%s has no nautilus declaration", pv)
			}
			binds = append(binds, "PV := "+v.name)
		} else {
			binds = append(binds, fmt.Sprintf("PV := %d", presetOf(f)))
		}
		if cond, ok := rt.resetCond[strings.ToLower(inst)]; ok {
			binds = append(binds, "R := "+cond)
		}
	}
	return name + ":" + m + "(" + strings.Join(binds, ", ") + ")", nil
}

func (rt *routine) sectionOf(f *found) string {
	if f.ext {
		return "VAR_EXTERNAL"
	}
	return "VAR"
}

// presetOf reads PRE from a TIMER/COUNTER tag's decorated value.
func presetOf(f *found) int64 {
	if f == nil || f.tag == nil {
		return 0
	}
	if m, ok := f.tag.Value.(map[string]any); ok {
		switch v := m["PRE"].(type) {
		case int64:
			return v
		case float64:
			return int64(v)
		}
	}
	return 0
}

func timeLit(ms int64) string {
	switch {
	case ms == 0:
		return "T#0S"
	case ms%60000 == 0:
		return fmt.Sprintf("T#%dM", ms/60000)
	case ms%1000 == 0:
		return fmt.Sprintf("T#%dS", ms/1000)
	}
	return fmt.Sprintf("T#%dMS", ms)
}

// aoiCall lowers Block(inst,in1,…) to inst:Block(P := in1, …). The
// operand order is the definition's: Inputs then InOuts, Required or
// not. Returns the name of the output the writer continues power from.
func (rt *routine) aoiCall(in *l5x.Instr) (text, powerOut string, err *refusal) {
	a := rt.im.aois[strings.ToLower(in.Mnemonic)]
	if len(in.Args) < 1 {
		return "", "", refuse("AOI", "%s: no instance operand", a.Name)
	}
	inst := in.Args[0]
	f := rt.sc.lookup(inst)
	if f == nil {
		return "", "", refuse("undefined", "tag %s is not in scope", inst)
	}
	if !strings.EqualFold(f.dtype, a.Name) {
		return "", "", refuse("AOI", "%s is a %s, not a %s", inst, f.dtype, a.Name)
	}
	rt.p.declare(decl{Name: f.name, Type: l5x.Ident(a.Name), Section: "VAR", Comment: firstLine(descOf(f))})
	var params []*l5x.Parameter
	for i := range a.Parameters {
		p := &a.Parameters[i]
		if strings.EqualFold(p.Name, "EnableIn") || strings.EqualFold(p.Name, "EnableOut") {
			continue
		}
		if p.Usage == "Input" || p.Usage == "InOut" {
			params = append(params, p)
		}
	}
	if len(in.Args)-1 != len(params) {
		return "", "", refuse("AOI", "%s(%s): %d operands for %d parameters", a.Name, strings.Join(in.Args, ","), len(in.Args)-1, len(params))
	}
	var binds []string
	powerBound := false
	for i, p := range params {
		v := in.Args[i+1]
		switch {
		case v == "1" && p.Usage == "Input" && strings.EqualFold(p.DataType, "BOOL") && !powerBound:
			// The writer's power pin: unbound, the rail drives it.
			powerBound = true
			continue
		case genEn.MatchString(v) && p.Usage == "Input" && strings.EqualFold(p.DataType, "BOOL") && !powerBound:
			powerBound = true
			continue
		case v == "0" && p.Usage == "Input" && strings.EqualFold(p.DataType, "BOOL"):
			binds = append(binds, l5x.Ident(p.Name)+" := FALSE")
			continue
		case numLit.MatchString(v):
			binds = append(binds, l5x.Ident(p.Name)+" := "+v)
			continue
		}
		ref, err := rt.ref(v, "")
		if err != nil {
			return "", "", err
		}
		binds = append(binds, l5x.Ident(p.Name)+" := "+ref)
	}
	for _, p := range a.Parameters {
		if p.Usage == "Output" && strings.EqualFold(p.DataType, "BOOL") && !strings.EqualFold(p.Name, "EnableOut") {
			powerOut = p.Name
			break
		}
	}
	return f.name + ":" + l5x.Ident(a.Name) + "(" + strings.Join(binds, ", ") + ")", powerOut, nil
}

// pulseTag names the one-shot tag a compound ONS gets, declared as a
// program-local BOOL.
func (rt *routine) pulseTag() string {
	rt.pulses++
	name := fmt.Sprintf("os_%s_%d", rt.curName, rt.pulses)
	rt.p.declare(decl{Name: name, Type: "BOOL", Section: "VAR", Comment: "one-shot of a rung condition (Logix ONS)"})
	return name
}

// hoistBlocks moves TON/TOF/CTU instructions out of branch legs onto rungs
// of their own: prefix + the leg's conditions ahead of the block + the
// block. A following XIC(inst.DN) in the leg stays, as the contact it is.
// Nested branches are handled the same way, their prefix being the outer
// leg's conditions so far.
func (rt *routine) hoistBlocks(legs [][]l5x.Term, prefix []string) ([][]l5x.Term, [][]string, *refusal) {
	var hoisted [][]string
	outLegs := make([][]l5x.Term, 0, len(legs))
	for _, leg := range legs {
		var kept []l5x.Term
		var condSoFar []string
		for _, t := range leg {
			if t.Instr == nil {
				inner, h, err := rt.hoistBlocks(t.Legs, append(append([]string(nil), prefix...), condSoFar...))
				if err != nil {
					return nil, nil, err
				}
				hoisted = append(hoisted, h...)
				kept = append(kept, l5x.Term{Legs: inner})
				// the branch's own condition text joins condSoFar
				s, err := rt.series(inner, true, false)
				if err != nil {
					return nil, nil, err
				}
				condSoFar = append(condSoFar, s...)
				continue
			}
			m := strings.ToUpper(t.Instr.Mnemonic)
			if m == "TON" || m == "TOF" || m == "CTU" {
				text, err := rt.block(t.Instr)
				if err != nil {
					return nil, nil, err
				}
				body := append(append(append([]string(nil), prefix...), condSoFar...), text)
				hoisted = append(hoisted, body)
				continue
			}
			if m == "ONS" {
				// A one-shot of the leg's condition so far (with the
				// prefix it inherits). At the head of a rail-fed leg
				// after one contact it stays for series() as +x; else the
				// pulse gets a tag: prefix cond ( P os ), and the leg
				// reads os from there.
				if len(prefix) == 0 && len(kept) == 1 && kept[0].Instr != nil && instrIs(kept[0], "XIC", "XIO") != nil {
					kept = append(kept, t)
					continue
				}
				if len(t.Instr.Args) == 1 {
					rt.edges["st:"+strings.ToLower(t.Instr.Args[0])] = ""
				}
				pulse := rt.pulseTag()
				body := append(append(append([]string(nil), prefix...), condSoFar...), "( P "+pulse+" )")
				hoisted = append(hoisted, body)
				rt.synth[strings.ToLower(pulse)] = true
				kept = []l5x.Term{{Instr: &l5x.Instr{Mnemonic: "XIC", Args: []string{pulse}}}}
				condSoFar = []string{pulse}
				continue
			}
			kept = append(kept, t)
			switch {
			case m == "XIC" || m == "XIO":
				c, err := rt.contact(t.Instr)
				if err != nil {
					return nil, nil, err
				}
				condSoFar = append(condSoFar, c...)
			case compareFn[m] != "":
				c, err := rt.compare(t.Instr)
				if err != nil {
					return nil, nil, err
				}
				condSoFar = append(condSoFar, c)
			default:
				// a data box or anything else passes power; it does not
				// change the condition
			}
		}
		outLegs = append(outLegs, kept)
	}
	return outLegs, hoisted, nil
}

// firstCoil is the index of the first coil in a leg, or -1.
func firstCoil(leg []l5x.Term) int {
	for i, t := range leg {
		if t.Instr != nil && isCoil(t.Instr) {
			return i
		}
	}
	return -1
}

func allEmpty(legs [][]l5x.Term) bool {
	for _, leg := range legs {
		if len(leg) > 0 {
			return false
		}
	}
	return true
}

func containsBox(parts []string) bool {
	for _, p := range parts {
		if strings.Contains(p, "{ ") {
			return true
		}
	}
	return false
}
