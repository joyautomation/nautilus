package writer

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ld"
)

// The runtime's own functions, as Logix spells them. FIRST_SCAN() is a
// core nautilus function (TRUE for the whole first scan of its program
// after a start or download, not after an online edit); on a Logix
// controller the same fact is the status flag S:FS.

// firstScanFlag is the Logix status bit FIRST_SCAN() reads.
const firstScanFlag = "S:FS"

// isFirstScan reports whether a function contact or call is FIRST_SCAN()
// with no arguments.
func isFirstScan(fn, args string) bool {
	return strings.EqualFold(fn, "FIRST_SCAN") && strings.TrimSpace(args) == ""
}

// LOCAL_TIME is the calendar now, as GSV(WallClockTime,,LocalDateTime,…)
// writes it: seven DINTs, year to microsecond. An instance is a DINT[7]
// tag, a call fills it, and a member reads its element. The import of a
// GSV is a call followed by one assignment that copies the seven into the
// GSV's destination array, and that folds back into the one GSV.

const localTimeType = "LOCAL_TIME"

// localTimeIndex is each output's element in the LocalDateTime array.
// MILLISECOND has none: element 6 is microseconds.
var localTimeIndex = map[string]int{"YEAR": 0, "MONTH": 1, "DAY": 2, "HOUR": 3, "MINUTE": 4, "SECOND": 5}

func gsvLocalTime(dest string) string {
	return "GSV(WallClockTime,,LocalDateTime," + dest + ")"
}

// isLocalTime reports whether a declared type is LOCAL_TIME.
func isLocalTime(typ string) bool {
	return strings.EqualFold(strings.TrimSpace(typ), localTimeType)
}

// localTimeCopy is the assignment the importer writes after a call: the
// seven outputs into dest[k..k+6], in GSV order. It returns dest[k] when
// text is exactly that copy for inst.
func localTimeCopy(inst, text string) (string, bool) {
	stmts := strings.Split(strings.TrimSuffix(strings.TrimSpace(text), ";"), ";")
	if len(stmts) != 7 {
		return "", false
	}
	var arr string
	var base int
	members := []string{"YEAR", "MONTH", "DAY", "HOUR", "MINUTE", "SECOND"}
	for i, s := range stmts {
		lhs, rhs, ok := strings.Cut(s, ":=")
		if !ok {
			return "", false
		}
		lhs, rhs = strings.Join(strings.Fields(lhs), ""), strings.Join(strings.Fields(rhs), "")
		m := localTimeElemRe.FindStringSubmatch(lhs)
		if m == nil {
			return "", false
		}
		k, _ := strconv.Atoi(m[2])
		if i == 0 {
			arr, base = m[1], k
		} else if !strings.EqualFold(m[1], arr) || k != base+i {
			return "", false
		}
		want := inst + "." + "MILLISECOND*1000"
		if i < 6 {
			want = inst + "." + members[i]
		}
		if !strings.EqualFold(rhs, want) {
			return "", false
		}
	}
	return arr + "[" + strconv.Itoa(base) + "]", true
}

var localTimeElemRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\[(\d+)\]$`)

// localTimeMember rewrites inst.MEMBER to the instance tag's element and
// marks the tag used. In ladder MILLISECOND is refused (the element holds
// microseconds, and an operand there cannot be an expression); in ST it is
// the expression.
func (lw *lowered) localTimeMember(inst, member string, line int, rung string) (string, bool) {
	lw.clockUsed[strings.ToLower(inst)] = true
	m := strings.ToUpper(member)
	if k, ok := localTimeIndex[m]; ok {
		return inst + "[" + strconv.Itoa(k) + "]", true
	}
	if m == "MILLISECOND" && lw.st {
		return "(" + inst + "[6] / 1000)", true
	}
	if m == "MILLISECOND" {
		lw.diag(ruleMember, line, rung, "%s.MILLISECOND: Logix's LocalDateTime keeps microseconds in %s[6]; read MILLISECOND in an ST routine, or divide %s[6] by 1000 in a { := } expression", inst, inst, inst)
		return "", false
	}
	lw.diag(ruleMember, line, rung, "%s.%s: LOCAL_TIME's outputs are YEAR, MONTH, DAY, HOUR, MINUTE, SECOND and MILLISECOND", inst, member)
	return "", false
}

// dropUnusedClocks removes the tag of a LOCAL_TIME instance whose every
// call folded into a GSV into another array and that nothing else reads.
func (lw *lowered) dropUnusedClocks() {
	keep := func(tags []tagDef) []tagDef {
		out := tags[:0]
		for _, t := range tags {
			if k := strings.ToLower(t.Name); lw.clocks[k] && !lw.clockUsed[k] {
				continue
			}
			out = append(out, t)
		}
		return out
	}
	lw.ctrlTags, lw.progTags = keep(lw.ctrlTags), keep(lw.progTags)
}

// planClocks decides which LOCAL_TIME instances fold: one whose every
// call is followed by the import's copy, and that nothing else names. An
// instance read anywhere else keeps its own DINT[7], which its calls
// fill, because a fold into another array would leave that tag unread
// and unwritten.
func (lw *lowered) planClocks(rungs []ld.Rung) {
	lw.clockFold = foldableClocks(rungs, lw.clocks)
}

// foldableClocks is planClocks over the declared clocks (lower-cased
// names), shared with the round-trip comparator.
func foldableClocks(rungs []ld.Rung, clocks map[string]bool) map[string]bool {
	fold := map[string]bool{}
	other := map[string]bool{}
	var walk func(elems []ld.Element)
	walk = func(elems []ld.Element) {
		for i := 0; i < len(elems); i++ {
			e := elems[i]
			if e.Kind == "fb" && isLocalTime(e.Type) {
				k := strings.ToLower(e.Inst)
				if i+1 < len(elems) && elems[i+1].Kind == "assign" {
					if _, ok := localTimeCopy(e.Inst, elems[i+1].Text); ok {
						if _, seen := fold[k]; !seen {
							fold[k] = true
						}
						i++
						continue
					}
				}
				other[k] = true
				continue
			}
			for _, s := range []string{e.Ref, e.Args, e.Text} {
				for name := range clocks {
					if mentions(s, name) {
						other[name] = true
					}
				}
			}
			for _, leg := range e.Legs {
				walk(leg)
			}
		}
	}
	for _, r := range rungs {
		if r.POU != "" {
			continue
		}
		walk(r.Elements)
		for _, c := range r.Coils {
			for name := range clocks {
				if mentions(c.Ref, name) {
					other[name] = true
				}
			}
		}
	}
	for k := range other {
		delete(fold, k)
	}
	return fold
}

// mentions reports whether text names the identifier name (any case).
func mentions(text, name string) bool {
	if text == "" {
		return false
	}
	return regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9_])`).MatchString(text)
}

// localTimeMoves copies an instance's seven elements to dest[k..k+6]:
// the import's copy when the instance cannot fold.
func localTimeMoves(inst, dest string) []string {
	m := localTimeElemRe.FindStringSubmatch(dest)
	k, _ := strconv.Atoi(m[2])
	out := make([]string, 7)
	for i := range out {
		out[i] = fmt.Sprintf("MOVE(%s[%d],%s[%d])", inst, i, m[1], k+i)
	}
	return out
}
