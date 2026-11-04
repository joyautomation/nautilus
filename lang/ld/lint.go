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
	add := func(rule string, line int, format string, args ...any) {
		fs = append(fs, lint.Finding{Rule: rule, Line: line, Col: 1, Message: fmt.Sprintf(format, args...)})
	}

	commented := map[int]bool{} // rung index → a // note sits right above it
	for _, c := range m.Comments {
		for i, r := range m.Rungs {
			if r.Line > c.EndLine {
				commented[i] = true
				break
			}
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
			for _, el := range commonToLegs(b.Legs) {
				add("LD004", r.Line, "%s is on every leg of a branch in rung %s: put it once, in "+
					"series, before the branch, and take it out of the legs", elementText(el), r.Name)
			}
		})
		if strings.TrimSpace(r.Comment) == "" && !commented[i] {
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
				default:
					latch, plain := prev, cur
					if prevPlain {
						latch, plain = cur, prev
					}
					add("LD001", cur.rung.Line, "%s is both latched (%s, rung %s) and driven by %s (rung %s): "+
						"the plain coil overwrites the latch every scan. Use one %s rung, or a ( S %s ) / "+
						"( R %s ) pair, not both", name, coilText(latch.mode, name), latch.rung.Name,
						coilText(plain.mode, name), plain.rung.Name, coilText(plain.mode, name), name, name)
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

// commonToLegs is every contact (plain or comparison) that appears in
// every leg of a branch, in the first leg's order. Edge contacts and
// blocks are left out: each occurrence is its own instance with its own
// state, so factoring one is not the same program.
func commonToLegs(legs [][]Element) []Element {
	if len(legs) < 2 {
		return nil
	}
	var out []Element
	seen := map[string]bool{}
	for _, el := range legs[0] {
		k := contactKey(el)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		all := true
		for _, leg := range legs[1:] {
			found := false
			for _, o := range leg {
				if contactKey(o) == k {
					found = true
					break
				}
			}
			if !found {
				all = false
				break
			}
		}
		if all {
			out = append(out, el)
		}
	}
	return out
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
