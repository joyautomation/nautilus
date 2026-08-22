// Package ld compiles IEC 61131-3 Ladder Diagram programs written in
// nautilus's textual rung format. IEC defines LD only graphically; this is
// the git-friendly text form — the same posture as the FBD netlist, with
// standard semantics and no vendor dialect.
//
//	PROGRAM Main
//	VAR_EXTERNAL ... END_VAR
//	LD
//	  RUNG seal (* motor seal-in *)
//	    [ Start | Run ] /Stop ( Run )
//
//	  RUNG alarm (* 10 s on-delay *)
//	    Cold t1:TON(PT := T#10S) ( Alm )
//	END_LD
//	END_PROGRAM
//
// Rung grammar, left to right along the power rail:
//
//	Name          NO contact — passes power when Name is TRUE
//	/Name         NC contact — passes power when Name is FALSE
//	/FN(args)     negated function contact — passes power when the call
//	              yields FALSE (negation applies to any BOOL-yielding
//	              contact term: a plain ref, an accessor chain (/t1.Q),
//	              or a function call)
//	+Name         rising-edge contact — one scan TRUE on Name's 0→1
//	              transition (an implicit R_TRIG instance)
//	-Name         falling-edge contact — one scan TRUE on Name's 1→0
//	              transition (an implicit F_TRIG instance)
//	[ a | b ]     parallel branch (legs are series; branches nest)
//	FN(args)      a function used as a contact: GT(TempC, 90.0)
//	inst:TYPE(…)  a function block in the rung: power drives its boolean
//	              input (IN for timers, CU/CD for counters, CLK for edges),
//	              power continues from its boolean output (Q). A rung
//	              needs at least one coil or function block — the block's
//	              own output (inst.Q, inst.ET, …) is a legal sole output.
//	( Name )      output coil: Name := rung condition
//	( S Name )    set coil:    Name := OR(Name, condition)  — latches
//	( R Name )    reset coil:  Name := AND(Name, NOT condition)
//	( P Name )    rising-edge coil:  Name := TRUE for one scan when the
//	              rung condition rises (an implicit R_TRIG instance)
//	( N Name )    falling-edge coil: Name := TRUE for one scan when the
//	              rung condition falls (an implicit F_TRIG instance)
//
// Contacts and coils accept the same accessor references as FBD
// (Levels[2], M.Cmd). Series composes as AND, branches as OR.
//
// Edge instances (+Name, -Name, and the P/N coils) are unnamed in the
// text — the compiler derives a stable instance name from the rung name,
// the reference, and the occurrence's position among repeats of the same
// (rung, ref, edge kind) within that rung, so the name — and the R_TRIG's
// retained _prev state — survives an unrelated edit elsewhere in the
// program (a warm swap keys retained FB state by instance name).
//
// A rung's trailing output zone is coils only: a function block that is
// itself the rung's last element (no trailing coil) is fine — that's how
// a rung whose sole output is a TON/CTU reads its own .Q/.DN elsewhere —
// but a coil ahead of a later function block in the same rung is not
// supported (`coils must sit at the rung's right end`): unlike a
// graphical diagram's 2-D wiring, this text format has no way to tell
// "coil taps the rail, power continues" apart from "coil ends the rung,
// what follows is a new parallel output leg" from left-to-right text
// alone. Split such a rung into one nautilus rung per output leg instead
// (each keeping the source rung's identity, e.g. a letter suffix).
//
// Compilation is a single hop: LD emits an FBD netlist, so the whole FBD
// toolchain — the function vocabulary, user FUNCTION_BLOCKs, arrays, the
// ST lowering, and exact diagnostics — is inherited rather than rebuilt.
package ld

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	ldStartRe = regexp.MustCompile(`(?i)^\s*LD\s*$`)
	ldEndRe   = regexp.MustCompile(`(?i)^\s*END_LD\s*$`)
	rungRe    = regexp.MustCompile(`(?i)^\s*RUNG(?:\s+([A-Za-z_][A-Za-z0-9_]*))?\s*(?:\(\*\s*(.*?)\s*\*\)\s*)?$`)
)

// HasBlock reports whether source contains an LD … END_LD body.
func HasBlock(src string) bool {
	for _, line := range strings.Split(src, "\n") {
		if ldStartRe.MatchString(line) {
			return true
		}
	}
	return false
}

// Transpile rewrites an LD program as an FBD-netlist program.
func Transpile(src string) (string, error) {
	out, _, err := TranspileWithLines(src)
	return out, err
}

// TranspileWithLines also returns, for each 1-based output line, the
// 1-based source line it came from — diagnostics map back through it.
func TranspileWithLines(src string) (string, []int, error) {
	lines := strings.Split(src, "\n")
	var out []string
	var lineOf []int
	emit := func(text string, srcLine int) {
		out = append(out, text)
		lineOf = append(lineOf, srcLine)
	}

	inLD := false
	var rung *rungParse
	flushRung := func() error {
		if rung == nil {
			return nil
		}
		stmts, err := rung.compile()
		if err != nil {
			return err
		}
		for _, s := range stmts {
			emit("  "+s, rung.line)
		}
		rung = nil
		return nil
	}

	for i, raw := range lines {
		n := i + 1
		switch {
		case !inLD && ldStartRe.MatchString(raw):
			inLD = true
			emit("FBD", n)
		case inLD && ldEndRe.MatchString(raw):
			if err := flushRung(); err != nil {
				return "", nil, err
			}
			inLD = false
			emit("END_FBD", n)
		case inLD:
			trimmed := strings.TrimSpace(raw)
			if m := rungRe.FindStringSubmatch(raw); m != nil {
				if err := flushRung(); err != nil {
					return "", nil, err
				}
				name := m[1]
				if name == "" {
					name = fmt.Sprintf("rung%d", n)
				}
				rung = &rungParse{name: name, line: n}
				// A named rung reads as a comment above its network.
				emit("  // RUNG "+name, n)
			} else if trimmed == "" || strings.HasPrefix(trimmed, "//") {
				// Blank lines and comments pass through inside the block.
				emit(raw, n)
			} else {
				if rung == nil {
					return "", nil, fmt.Errorf("ld: line %d: rung elements before any RUNG", n)
				}
				rung.text += " " + trimmed
			}
		default:
			emit(raw, n)
		}
	}
	if rung != nil {
		if err := flushRung(); err != nil {
			return "", nil, err
		}
	}
	if inLD {
		return "", nil, fmt.Errorf("ld: missing END_LD")
	}
	return strings.Join(out, "\n"), lineOf, nil
}

// ── rung parsing ────────────────────────────────────────────────────────────

type rungParse struct {
	name    string
	comment string // the header's (* … *) text, if any
	line    int
	text    string // the rung's element text, newlines collapsed
}

// element kinds
type contact struct {
	ref string
	neg bool
}
type branch struct{ legs [][]any }
type fnEl struct {
	fn   string
	args string
	neg  bool
}
type fbEl struct {
	inst, typ, args string
}
type coilEl struct {
	ref  string
	mode string // "", "S", "R", "P", "N"
}

// edgeEl is an edge contact (+Name / -Name): an implicit R_TRIG/F_TRIG
// instance whose Q joins the rung's condition like any other contact.
type edgeEl struct {
	ref  string
	rise bool // true: rising (R_TRIG); false: falling (F_TRIG)
}

// edgeCtx names the implicit edge-trigger instances a rung introduces —
// stable across recompiles of unchanged text, so retained state survives
// a warm swap (see the package doc).
type edgeCtx struct {
	rung string
	seen map[string]int
}

func (ec *edgeCtx) name(kind, ref string) string {
	base := kind + "_" + sanitizeIdent(ec.rung) + "_" + sanitizeIdent(ref)
	n := ec.seen[base]
	ec.seen[base] = n + 1
	if n == 0 {
		return base
	}
	return fmt.Sprintf("%s_%d", base, n+1)
}

var identSanitizeRe = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// sanitizeIdent turns a reference (which may carry an accessor chain,
// e.g. Levels[2] or M.Cmd) into a plain identifier fragment for a
// synthesized instance name.
func sanitizeIdent(s string) string {
	s = identSanitizeRe.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return "x"
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "_" + s
	}
	return s
}

// compile parses the rung text and emits FBD statements.
func (r *rungParse) compile() ([]string, error) {
	p := &rungTok{src: r.text, line: r.line}
	elems, err := p.series(false)
	if err != nil {
		return nil, err
	}
	if p.peek() != "" {
		return nil, p.errf("unexpected %q", p.peek())
	}

	// Split trailing coils from the condition elements.
	var coils []coilEl
	condEnd := len(elems)
	for condEnd > 0 {
		if c, ok := elems[condEnd-1].(coilEl); ok {
			coils = append([]coilEl{c}, coils...)
			condEnd--
			continue
		}
		break
	}
	for _, e := range elems[:condEnd] {
		if _, ok := e.(coilEl); ok {
			return nil, fmt.Errorf("ld: rung %s (line %d): coils must sit at the rung's right end", r.name, r.line)
		}
	}
	// A rung's only output may be a function block instance — its own
	// output (inst.Q, inst.DN, …) is read elsewhere, the same way a coil
	// would be. Only a bare rung (no coil AND no FB anywhere) is illegal.
	if len(coils) == 0 && !hasFB(elems) {
		return nil, fmt.Errorf("ld: rung %s (line %d): a rung needs at least one coil or function block", r.name, r.line)
	}

	ec := &edgeCtx{rung: r.name, seen: map[string]int{}}
	var stmts []string
	cond, err := seriesCond(elems[:condEnd], ec, &stmts)
	if err != nil {
		return nil, fmt.Errorf("ld: rung %s (line %d): %w", r.name, r.line, err)
	}

	// Several coils share the condition through a named wire, so the
	// diagram shows one rail fanning out — and the logic evaluates once.
	if len(coils) > 1 && cond != "TRUE" {
		wire := "w_" + r.name
		stmts = append(stmts, fmt.Sprintf("%s = %s", wire, cond))
		cond = wire
	}
	for _, c := range coils {
		switch c.mode {
		case "S":
			stmts = append(stmts, fmt.Sprintf("%s := OR(%s, %s)", c.ref, c.ref, cond))
		case "R":
			stmts = append(stmts, fmt.Sprintf("%s := AND(%s, NOT %s)", c.ref, c.ref, cond))
		case "P":
			inst := ec.name("rt", c.ref)
			stmts = append(stmts, fmt.Sprintf("%s : R_TRIG(CLK := %s)", inst, cond))
			stmts = append(stmts, fmt.Sprintf("%s := %s.Q", c.ref, inst))
		case "N":
			inst := ec.name("ft", c.ref)
			stmts = append(stmts, fmt.Sprintf("%s : F_TRIG(CLK := %s)", inst, cond))
			stmts = append(stmts, fmt.Sprintf("%s := %s.Q", c.ref, inst))
		default:
			stmts = append(stmts, fmt.Sprintf("%s := %s", c.ref, cond))
		}
	}
	return stmts, nil
}

// hasFB reports whether elems contains a function block instance,
// anywhere — top level or nested in a branch leg.
func hasFB(elems []any) bool {
	for _, e := range elems {
		switch x := e.(type) {
		case fbEl:
			return true
		case branch:
			for _, leg := range x.legs {
				if hasFB(leg) {
					return true
				}
			}
		}
	}
	return false
}

// seriesCond folds condition elements into one FBD expression, emitting FB
// declarations along the way (power flows through their boolean pins).
// ec names any implicit edge-trigger instances the elements introduce.
func seriesCond(elems []any, ec *edgeCtx, stmts *[]string) (string, error) {
	parts := []string{}
	flush := func() string {
		switch len(parts) {
		case 0:
			return "TRUE"
		case 1:
			return parts[0]
		default:
			return "AND(" + strings.Join(parts, ", ") + ")"
		}
	}
	for _, e := range elems {
		switch x := e.(type) {
		case contact:
			if x.neg {
				parts = append(parts, "NOT "+x.ref)
			} else {
				parts = append(parts, x.ref)
			}
		case fnEl:
			call := x.fn + "(" + x.args + ")"
			if x.neg {
				call = "NOT " + call
			}
			parts = append(parts, call)
		case edgeEl:
			typ, kind := "R_TRIG", "rt"
			if !x.rise {
				typ, kind = "F_TRIG", "ft"
			}
			inst := ec.name(kind, x.ref)
			*stmts = append(*stmts, fmt.Sprintf("%s : %s(CLK := %s)", inst, typ, x.ref))
			parts = append(parts, inst+".Q")
		case branch:
			var legs []string
			for _, leg := range x.legs {
				lc, err := seriesCond(leg, ec, stmts)
				if err != nil {
					return "", err
				}
				legs = append(legs, lc)
			}
			parts = append(parts, "OR("+strings.Join(legs, ", ")+")")
		case fbEl:
			in, out := powerPins(x.typ)
			if containsPin(x.args, in) {
				return "", fmt.Errorf("%s: the rung's power drives %s — don't pass it as an argument", x.inst, in)
			}
			args := in + " := " + flush()
			if strings.TrimSpace(x.args) != "" {
				args += ", " + x.args
			}
			*stmts = append(*stmts, fmt.Sprintf("%s : %s(%s)", x.inst, x.typ, args))
			parts = []string{x.inst + "." + out}
		default:
			return "", fmt.Errorf("unexpected element %T", e)
		}
	}
	return flush(), nil
}

// powerPins is the boolean in/out pin a rung's power rail connects on each
// standard block; unknown (user) blocks default to IN/Q.
func powerPins(typ string) (in, out string) {
	switch strings.ToUpper(typ) {
	case "TON", "TOF", "TP":
		return "IN", "Q"
	case "CTU":
		return "CU", "Q"
	case "CTD":
		return "CD", "Q"
	case "R_TRIG", "F_TRIG":
		return "CLK", "Q"
	case "SR":
		return "S1", "Q1"
	case "RS":
		return "S", "Q1"
	}
	return "IN", "Q"
}

func containsPin(args, pin string) bool {
	re := regexp.MustCompile(`(?i)(^|[,(\s])` + pin + `\s*:=`)
	return re.MatchString(args)
}

// ── rung tokenizer ──────────────────────────────────────────────────────────

type rungTok struct {
	src  string
	pos  int
	line int
}

func (t *rungTok) errf(format string, a ...any) error {
	return fmt.Errorf("ld: line %d: %s", t.line, fmt.Sprintf(format, a...))
}

func (t *rungTok) ws() {
	for t.pos < len(t.src) && (t.src[t.pos] == ' ' || t.src[t.pos] == '\t') {
		t.pos++
	}
}

// peek returns the next meaningful character as a string ("" at end).
func (t *rungTok) peek() string {
	t.ws()
	if t.pos >= len(t.src) {
		return ""
	}
	return string(t.src[t.pos])
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}
func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// ident consumes a reference: an identifier with optional accessor chain
// (Levels[2], M.Cmd) — the same references FBD accepts.
func (t *rungTok) ident() (string, error) {
	t.ws()
	start := t.pos
	if t.pos >= len(t.src) || !isIdentStart(t.src[t.pos]) {
		return "", t.errf("expected a name, got %q", t.peek())
	}
	for t.pos < len(t.src) && isIdentPart(t.src[t.pos]) {
		t.pos++
	}
	// accessor chain, verbatim
	for t.pos < len(t.src) {
		switch t.src[t.pos] {
		case '[':
			depth := 0
			for t.pos < len(t.src) {
				if t.src[t.pos] == '[' {
					depth++
				}
				if t.src[t.pos] == ']' {
					depth--
				}
				t.pos++
				if depth == 0 {
					break
				}
			}
			if depth != 0 {
				return "", t.errf("unclosed '[' in reference")
			}
			continue
		case '.':
			if t.pos+1 < len(t.src) && isIdentStart(t.src[t.pos+1]) {
				t.pos++
				for t.pos < len(t.src) && isIdentPart(t.src[t.pos]) {
					t.pos++
				}
				continue
			}
		}
		break
	}
	return t.src[start:t.pos], nil
}

// rawArgs consumes a balanced "(...)" and returns its inside verbatim.
func (t *rungTok) rawArgs() (string, error) {
	t.ws()
	if t.peek() != "(" {
		return "", t.errf("expected '('")
	}
	depth := 0
	start := t.pos + 1
	for t.pos < len(t.src) {
		switch t.src[t.pos] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				inside := t.src[start:t.pos]
				t.pos++
				return strings.TrimSpace(inside), nil
			}
		}
		t.pos++
	}
	return "", t.errf("unclosed '('")
}

// series parses elements until end of input, a ']' or '|' (inside a branch).
func (t *rungTok) series(inBranch bool) ([]any, error) {
	var out []any
	for {
		switch t.peek() {
		case "", "]", "|":
			if !inBranch && t.peek() != "" {
				return nil, t.errf("unexpected %q outside a branch", t.peek())
			}
			return out, nil
		case "/":
			t.pos++
			ref, err := t.ident()
			if err != nil {
				return nil, err
			}
			if t.pos < len(t.src) && t.src[t.pos] == '(' {
				// Negated function contact: /GT(a, b). Negation applies
				// to any BOOL-yielding contact term, not just plain refs
				// — /t1.Q already worked via the ident() accessor chain.
				args, err := t.rawArgs()
				if err != nil {
					return nil, err
				}
				out = append(out, fnEl{fn: strings.ToUpper(ref), args: args, neg: true})
			} else {
				out = append(out, contact{ref: ref, neg: true})
			}
		case "+":
			t.pos++
			ref, err := t.ident()
			if err != nil {
				return nil, err
			}
			out = append(out, edgeEl{ref: ref, rise: true})
		case "-":
			t.pos++
			ref, err := t.ident()
			if err != nil {
				return nil, err
			}
			out = append(out, edgeEl{ref: ref, rise: false})
		case "[":
			t.pos++
			var legs [][]any
			for {
				leg, err := t.series(true)
				if err != nil {
					return nil, err
				}
				legs = append(legs, leg)
				if t.peek() == "|" {
					t.pos++
					continue
				}
				break
			}
			if t.peek() != "]" {
				return nil, t.errf("expected ']' to close the branch")
			}
			t.pos++
			if len(legs) < 2 {
				return nil, t.errf("a branch needs at least two legs ([ a | b ])")
			}
			out = append(out, branch{legs: legs})
		case "(":
			// coil: ( Name ), ( S Name ), ( R Name )
			t.pos++
			t.ws()
			mode := ""
			ref, err := t.ident()
			if err != nil {
				return nil, err
			}
			t.ws()
			if t.peek() != ")" && (strings.EqualFold(ref, "S") || strings.EqualFold(ref, "R") || strings.EqualFold(ref, "P") || strings.EqualFold(ref, "N")) {
				mode = strings.ToUpper(ref)
				ref, err = t.ident()
				if err != nil {
					return nil, err
				}
				t.ws()
			}
			if t.peek() != ")" {
				return nil, t.errf("expected ')' to close the coil")
			}
			t.pos++
			out = append(out, coilEl{ref: ref, mode: mode})
		default:
			name, err := t.ident()
			if err != nil {
				return nil, err
			}
			// A function contact's paren is ATTACHED — `GT(TempC, 90.0)`.
			// A detached '(' starts a coil, so `Stop ( Run )` reads as a
			// contact then a coil, never a call.
			attachedParen := t.pos < len(t.src) && t.src[t.pos] == '('
			t.ws()
			if t.pos < len(t.src) && t.src[t.pos] == ':' && (t.pos+1 >= len(t.src) || t.src[t.pos+1] != '=') {
				// inst:TYPE(args) — a function block in the rung
				t.pos++
				typ, err := t.ident()
				if err != nil {
					return nil, err
				}
				args := ""
				if t.peek() == "(" {
					if args, err = t.rawArgs(); err != nil {
						return nil, err
					}
				}
				out = append(out, fbEl{inst: name, typ: typ, args: args})
			} else if attachedParen {
				// FN(args) — a function used as a contact
				args, err := t.rawArgs()
				if err != nil {
					return nil, err
				}
				out = append(out, fnEl{fn: strings.ToUpper(name), args: args})
			} else {
				out = append(out, contact{ref: name})
			}
		}
	}
}
