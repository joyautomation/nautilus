package writer

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/st"
)

// Copies: Logix cannot assign a whole array or structure (MOV takes one
// value; ST rejects `B := A` on either), so a nautilus assignment of one
// is a COP, its length the destination's element count (1 for a
// structure). A copy the L5X import spelled element by element (a slice,
// one element) carries `(* @logix COP *)` on its last assignment, and
// `(* @logix CPS *)` marks a CPS; the writer folds such a run back into
// the one instruction — logix/importer/copy.go is the other half.

// wholeCopy reports whether `dst := src` copies a whole array or structure
// of one type, and its COP length. bad names a copy Logix cannot make (a
// multi-dimensional array).
func (w *stWriter) wholeCopy(dst, src st.Expression) (n int, ok bool, bad string) {
	if dst == nil || src == nil || !isRefExpr(src) {
		return 0, false, ""
	}
	dt, sti := strings.TrimSpace(w.rawType(dst)), strings.TrimSpace(w.rawType(src))
	if dt == "" || sti == "" || w.elementary(dt) != "" {
		return 0, false, ""
	}
	if !sameType(dt, sti) {
		return 0, false, ""
	}
	if m := arrayRe.FindStringSubmatch(dt); m != nil {
		if m[3] != "" {
			return 0, false, "a whole multi-dimensional array"
		}
		lo, _ := strconv.Atoi(m[1])
		hi, _ := strconv.Atoi(m[2])
		return hi - lo + 1, true, ""
	}
	if strings.HasPrefix(strings.ToUpper(dt), "ARRAY") {
		return 0, false, "an array whose bounds the writer cannot read"
	}
	return 1, true, ""
}

func isRefExpr(e st.Expression) bool {
	switch e.(type) {
	case *st.IdentExpr, *st.MemberExpr, *st.IndexExpr:
		return true
	}
	return false
}

// sameType compares two declared types as written, ignoring case and
// spacing (`ARRAY [0..3] OF DINT` is `ARRAY[0..3] OF DINT`).
func sameType(a, b string) bool {
	norm := func(s string) string { return strings.ToUpper(strings.Join(strings.Fields(s), "")) }
	return norm(a) == norm(b)
}

// copyMnemonic is the instruction a copy's pragma names, "" for none.
func copyMnemonic(a ld.Assignment) string {
	switch {
	case a.Pragma("logix CPS"):
		return "CPS"
	case a.Pragma("logix COP"):
		return "COP"
	}
	return ""
}

// elemRe splits an element reference: `Path[3]` → Path, 3.
var elemRe = regexp.MustCompile(`^(.+)\[\s*(-?\d+)\s*\]$`)

// copyRuns finds, in one assignment box, the element-by-element runs the
// import wrote for a COP/CPS: each ends on an assignment carrying the
// pragma and reaches back over the contiguous element copies before it
// (same arrays, indexes stepping by one, no pragma of their own). The map
// is keyed by the run's first assignment.
type copyRun struct {
	end   int
	instr string
}

func copyRuns(as []ld.Assignment) map[int]copyRun {
	runs := map[int]copyRun{}
	taken := make([]bool, len(as))
	for i, a := range as {
		mn := copyMnemonic(a)
		if mn == "" {
			continue
		}
		start := i
		if dp, di, ok := elemOf(a.Target); ok {
			if sp, si, ok := elemOf(refText(a.Value)); ok {
				for start > 0 && !taken[start-1] && copyMnemonic(as[start-1]) == "" {
					p := as[start-1]
					step := i - start + 1
					pdp, pdi, ok1 := elemOf(p.Target)
					psp, psi, ok2 := elemOf(refText(p.Value))
					if !ok1 || !ok2 || !strings.EqualFold(pdp, dp) || !strings.EqualFold(psp, sp) || pdi != di-step || psi != si-step {
						break
					}
					start--
				}
			}
		}
		for k := start; k <= i; k++ {
			taken[k] = true
		}
		runs[start] = copyRun{end: i, instr: mn}
	}
	return runs
}

func elemOf(ref string) (string, int, bool) {
	m := elemRe.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, false
	}
	return m[1], n, true
}

func refText(e ld.Expr) string {
	if r, ok := e.(*ld.Ref); ok {
		return r.Text
	}
	return ""
}

// copyText lowers a whole copy, or a pragma'd run starting at as[i], to
// its COP/CPS. ok is false when the assignment is not a copy (an ordinary
// MOVE or CPT); done says the copy failed with a diagnostic already given.
func (c *rungCtx) copyText(as []ld.Assignment, i int, run *copyRun) (text string, next int, ok bool) {
	a := as[i]
	w := &stWriter{lw: c.lw}
	if run != nil {
		first, last := as[i], as[run.end]
		src, ok1 := c.ref(refText(first.Value))
		dst, ok2 := c.ref(first.Target)
		if refText(first.Value) == "" || !ok1 || !ok2 {
			c.lw.diag(ruleDataOp, c.r.Line, c.r.Name, "{ %s }: the @logix %s pragma marks a copy, but %s := %s copies no reference", ld.PrintAssignments(as[i:run.end+1]), run.instr, first.Target, first.Value.IEC())
			return "", run.end + 1, false
		}
		n := run.end - i + 1
		if run.end == i {
			// One assignment: an element, a scalar or a whole value.
			if wn, whole, _ := w.wholeCopy(parseSTExpr(last.Target), parseSTExpr(refText(last.Value))); whole {
				n = wn
			}
		}
		return run.instr + "(" + src + "," + dst + "," + strconv.Itoa(n) + ")", run.end + 1, true
	}
	ref := refText(a.Value)
	if ref == "" {
		return "", i, false
	}
	n, whole, bad := w.wholeCopy(parseSTExpr(a.Target), parseSTExpr(ref))
	if bad != "" {
		c.lw.diag(ruleDataOp, c.r.Line, c.r.Name, "{ %s := %s }: %s has no Logix copy", a.Target, ref, bad)
		return "", i + 1, false
	}
	if !whole {
		return "", i, false
	}
	src, ok1 := c.ref(ref)
	dst, ok2 := c.ref(a.Target)
	if !ok1 || !ok2 {
		return "", i + 1, false
	}
	return "COP(" + src + "," + dst + "," + strconv.Itoa(n) + ")", i + 1, true
}
