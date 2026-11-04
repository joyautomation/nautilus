package ld

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/lint"
)

// Lint reports the ladder structure warnings (lang/lint, nautilus#561) for
// one .ld source: LD001 to LD006, at cfg's thresholds, less what cfg
// disables and what an `(* @allow … *)` pragma switches off. A source
// that does not parse has no findings: its parse error is the diagnostic.
func Lint(src string, cfg lint.Config) []lint.Finding {
	m, err := Graph(src)
	if err != nil {
		return nil
	}
	var fs []lint.Finding
	lines := strings.Split(src, "\n")
	add := func(rule string, line int, format string, args ...any) {
		fs = append(fs, lint.Finding{Rule: rule, Line: line, Col: nameCol(lines, line), Message: fmt.Sprintf(format, args...)})
	}

	// rung index → a // note sits on the line above its header (blank
	// lines between are fine). A note further up, or inside the previous
	// rung's body, is that body's note, not this rung's comment.
	commented := map[int]bool{}
	for _, c := range m.Comments {
		for i, r := range m.Rungs {
			if r.Line <= c.EndLine {
				continue
			}
			above := true
			for l := c.EndLine + 1; l < r.Line && l <= len(lines); l++ {
				if strings.TrimSpace(lines[l-1]) != "" {
					above = false
					break
				}
			}
			if above {
				commented[i] = true
			}
			break
		}
	}

	type writer struct {
		rung *Rung
		mode string
	}
	writers := map[string][]writer{} // POU + coil target → its coils, in source order
	var order []string

	for i := range m.Rungs {
		r := &m.Rungs[i]
		for _, c := range r.Coils {
			if c.Kind != "coil" {
				continue
			}
			k := r.POU + "\x00" + ir.NameKey(c.Ref)
			if writers[k] == nil {
				order = append(order, k)
			}
			writers[k] = append(writers[k], writer{r, c.Mode})
		}

		if d := branchDepth(r.Elements); d > cfg.MaxBranchDepth {
			add("LD002", r.Line, "rung %s nests branches %d deep (the limit is %d): compute the inner "+
				"branch into a named BOOL on a rung of its own (PumpPermissive, PumpAutoCall) and use "+
				"that BOOL here as one contact", r.Name, d, cfg.MaxBranchDepth)
		}
		if n := seriesLen(r.Elements); n > cfg.MaxSeries {
			add("LD003", r.Line, "rung %s has %d elements in series (the limit is %d): compute part of "+
				"the condition into a named BOOL on a rung of its own and use that BOOL here as one "+
				"contact", r.Name, n, cfg.MaxSeries)
		}
		eachBranch(r.Elements, func(b Element) {
			for _, c := range commonToLegs(b.Legs) {
				add("LD004", r.Line, "%s is on every leg of a branch in rung %s: put it once, in "+
					"series, %s the branch, and take it out of the legs", elementText(c.el), r.Name, c.where)
			}
		})
		if c := strings.TrimSpace(r.Comment); (c == "" || lint.IsPragma(c)) && !commented[i] {
			add("LD005", r.Line, "rung %s has no comment: write why the rung exists after its name, "+
				"RUNG %s (* … *), or in a // note on the line above — the reason, not a list of "+
				"the contacts", r.Name, r.Name)
		}
		eachElement(r.Elements, func(el Element) {
			if el.Kind != "fn" || !lint.IsThresholdCompare(el.Fn) {
				return
			}
			for _, a := range splitArgs(el.Args) {
				if lint.MagicNumber(a) {
					add("LD006", r.Line, "%s", lint.ThresholdMessage(elementText(el), strings.TrimSpace(a)))
					return
				}
			}
		})
	}

	// LD001: one coil per BOOL. Plain, P and N coils each write the tag
	// every scan, so a second one overwrites the first; a set/reset pair
	// is the latch idiom and is fine on its own.
	for _, k := range order {
		ws := writers[k]
		for i := 1; i < len(ws); i++ {
			cur := ws[i]
			for _, prev := range ws[:i] {
				curPlain, prevPlain := plainCoil(cur.mode), plainCoil(prev.mode)
				if !curPlain && !prevPlain {
					continue
				}
				ref := strings.SplitN(k, "\x00", 2)[1]
				name := coilRef(cur.rung, ref)
				switch {
				case curPlain && prevPlain:
					add("LD001", cur.rung.Line, "%s already has a coil, %s on rung %s (line %d): the later "+
						"rung overwrites it every scan. Drive %s from one rung (OR the conditions there, or "+
						"compute each into its own BOOL and OR those), or latch it with a ( S %s ) / "+
						"( R %s ) pair", name, coilText(prev.mode, name), prev.rung.Name, prev.rung.Line,
						name, name, name)
				case curPlain:
					// The plain coil runs after the latch: it writes the tag
					// every scan, so the latch never holds.
					add("LD001", cur.rung.Line, "%s is both latched (%s, rung %s) and driven by %s (rung %s): "+
						"rung %s comes later and writes %s every scan, so it overwrites the latch and the "+
						"latch never holds. Use one %s rung, or a ( S %s ) / ( R %s ) pair, not both",
						name, coilText(prev.mode, name), prev.rung.Name, coilText(cur.mode, name), cur.rung.Name,
						cur.rung.Name, name, coilText(cur.mode, name), name, name)
				default:
					// The latch runs after the plain coil: it overrides the
					// coil's result for the scans its rung is true, and the
					// coil writes the tag again on the next scan.
					effect := fmt.Sprintf("forces %s off while it is true, so the coil on rung %s only "+
						"holds while rung %s is false", name, prev.rung.Name, cur.rung.Name)
					if cur.mode == "S" {
						effect = fmt.Sprintf("forces %s on while it is true, so %s is on while either rung is",
							name, name)
					}
					add("LD001", cur.rung.Line, "%s is both latched (%s, rung %s) and driven by %s (rung %s): "+
						"rung %s comes later and %s; it never latches, because rung %s writes %s again "+
						"every scan. Put rung %s's condition into rung %s (one %s rung), or use a ( S %s ) / "+
						"( R %s ) pair, not both",
						name, coilText(cur.mode, name), cur.rung.Name, coilText(prev.mode, name), prev.rung.Name,
						cur.rung.Name, effect, prev.rung.Name, name, cur.rung.Name, prev.rung.Name,
						coilText(prev.mode, name), name, name)
				}
				break
			}
		}
	}
	return lint.Filter(src, cfg, fs)
}

// plainCoil: a coil that writes its tag every scan (( X ), ( P X ), ( N X )).
func plainCoil(mode string) bool { return mode != "S" && mode != "R" }

func coilText(mode, ref string) string {
	if mode == "" {
		return "( " + ref + " )"
	}
	return "( " + mode + " " + ref + " )"
}

// coilRef is the coil's target as the rung spells it.
func coilRef(r *Rung, key string) string {
	for _, c := range r.Coils {
		if ir.NameKey(c.Ref) == key {
			return c.Ref
		}
	}
	return key
}

// branchDepth is how deep branches nest: 0 for a plain series, 1 for
// `[ a | b ]`, 2 for a branch inside a leg of one.
func branchDepth(els []Element) int {
	d := 0
	for _, e := range els {
		if e.Kind != "branch" {
			continue
		}
		for _, leg := range e.Legs {
			if n := 1 + branchDepth(leg); n > d {
				d = n
			}
		}
	}
	return d
}

// seriesLen counts the rung's longest path of condition elements: a
// branch counts as its longest leg. Coils do not count.
func seriesLen(els []Element) int {
	n := 0
	for _, e := range els {
		if e.Kind != "branch" {
			n++
			continue
		}
		longest := 0
		for _, leg := range e.Legs {
			if l := seriesLen(leg); l > longest {
				longest = l
			}
		}
		n += longest
	}
	return n
}

func eachBranch(els []Element, fn func(Element)) {
	for _, e := range els {
		if e.Kind == "branch" {
			fn(e)
			for _, leg := range e.Legs {
				eachBranch(leg, fn)
			}
		}
	}
}

func eachElement(els []Element, fn func(Element)) {
	for _, e := range els {
		fn(e)
		for _, leg := range e.Legs {
			eachElement(leg, fn)
		}
	}
}

// common is one contact LD004 reports, and where it can go: "before" or
// "after" the branch.
type common struct {
	el    Element
	where string
}

// commonToLegs is every contact (plain or comparison) that appears in
// every leg of a branch and can be factored out without changing the
// program, in the first leg's order. Edge contacts and blocks are left
// out: each occurrence is its own instance with its own state, so
// factoring one is not the same program. And a contact moves only past
// other plain or comparison contacts: in `[ t1:TON(…) A | B A ]`, A
// after the timer is not A before it (the timer's IN would change), so
// that branch is not reported.
func commonToLegs(legs [][]Element) []common {
	if len(legs) < 2 {
		return nil
	}
	var out []common
	seen := map[string]bool{}
	for _, el := range legs[0] {
		k := contactKey(el)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		before, after := true, true
		for _, leg := range legs {
			at := -1
			for j, o := range leg {
				if contactKey(o) == k {
					at = j
					break
				}
			}
			if at < 0 {
				before, after = false, false
				break
			}
			before = before && pureSeries(leg[:at])
			after = after && pureSeries(leg[at+1:])
		}
		switch {
		case before:
			out = append(out, common{el, "before"})
		case after:
			out = append(out, common{el, "after"})
		}
	}
	return out
}

// pureSeries reports whether els are only plain and comparison contacts
// (and branches of them): elements a contact can move past, because none
// keeps state or acts on the power that reaches it.
func pureSeries(els []Element) bool {
	for _, e := range els {
		switch e.Kind {
		case "contact", "fn":
		case "branch":
			for _, leg := range e.Legs {
				if !pureSeries(leg) {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

// nameCol is the column of the rung's name on its header line (that of
// the RUNG keyword for an unnamed rung), so the editor marks the name.
func nameCol(lines []string, line int) int {
	if line < 1 || line > len(lines) {
		return 1
	}
	loc := rungNameRe.FindStringSubmatchIndex(lines[line-1])
	switch {
	case loc == nil:
		return 1
	case loc[2] >= 0:
		return loc[2] + 1
	}
	return len(lines[line-1]) - len(strings.TrimLeft(lines[line-1], " \t")) + 1
}

func contactKey(el Element) string {
	neg := ""
	if el.Neg {
		neg = "/"
	}
	switch el.Kind {
	case "contact":
		return "c" + neg + ir.NameKey(el.Ref)
	case "fn":
		return "f" + neg + strings.ToUpper(el.Fn) + "(" + strings.ToUpper(strings.Join(strings.Fields(el.Args), "")) + ")"
	}
	return ""
}

func elementText(el Element) string {
	neg := ""
	if el.Neg {
		neg = "/"
	}
	if el.Kind == "fn" {
		return neg + el.Fn + "(" + el.Args + ")"
	}
	return neg + el.Ref
}

// splitArgs splits a call's argument text at its top-level commas.
func splitArgs(s string) []string {
	var out []string
	depth, start := 0, 0
	inStr := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			inStr = !inStr
		case inStr:
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
