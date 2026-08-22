package ld

import (
	"regexp"
	"strings"
)

// User FUNCTION_BLOCKs in ladder.
//
// A `.ld` file may DEFINE function blocks as well as (or instead of) a
// PROGRAM — each one an ordinary IEC POU whose body happens to be rungs:
//
//	FUNCTION_BLOCK PumpSeq
//	VAR_INPUT  Start : BOOL; Level : REAL; END_VAR
//	VAR_OUTPUT Run : BOOL; END_VAR
//	VAR        t1 : TON; END_VAR
//	LD
//	  RUNG seal [ Start | Run ] /Stop ( Run )
//	END_LD
//	END_FUNCTION_BLOCK
//
// The transpile is the same single hop the rest of LD takes: the block's
// LD body becomes an FBD netlist, the file passes through lang/fbd, and
// the ST compiler sees an ordinary `FUNCTION_BLOCK … END_FUNCTION_BLOCK`.
// So a ladder-written block is callable from ST, FBD, SFC and ladder with
// no special case anywhere downstream — a ladder SUBROUTINE, which is the
// thing IEC gives you instead of a JSR.
//
// Placing a rung's power on a user block needs the block's SIGNATURE, and
// a signature may live in another file (a project library). This file is
// the text-level scan that finds them: it reads FUNCTION_BLOCK pin
// declarations out of ST **or** LD source, so the same scan serves the
// file being compiled and the library prelude joined ahead of it.

// pin is one declared FUNCTION_BLOCK pin, in declaration order.
type pin struct{ name, typ string }

// fbSig is a user FUNCTION_BLOCK's ladder-relevant signature.
type fbSig struct {
	name    string
	inputs  []pin // VAR_INPUT, declaration order
	outputs []pin // VAR_OUTPUT, declaration order
}

// resolver answers "which pins does a rung's power use on this block type?"
// It knows the standard blocks outright and the user blocks it scanned.
type resolver struct{ sigs map[string]fbSig }

var (
	fbStartRe = regexp.MustCompile(`(?i)^\s*FUNCTION_BLOCK\s+([A-Za-z_][A-Za-z0-9_]*)`)
	fbEndRe   = regexp.MustCompile(`(?i)^\s*END_FUNCTION_BLOCK\s*$`)
	// A VAR_INPUT / VAR_OUTPUT section anywhere in a block's header text,
	// declarations and END_VAR possibly sharing its line.
	varSectionRe  = regexp.MustCompile(`(?is)\b(VAR_INPUT|VAR_OUTPUT)\b(.*?)\bEND_VAR\b`)
	commentRe     = regexp.MustCompile(`(?s)\(\*.*?\*\)`)
	lineCommentRe = regexp.MustCompile(`(?m)//.*$`)
)

// newResolver scans src plus any library sources for user FB signatures.
// Later sources win, so the file being compiled shadows a library — the
// same precedence the ST compiler's in-file FB table has.
func newResolver(src string, libs []string) *resolver {
	r := &resolver{sigs: map[string]fbSig{}}
	for _, l := range libs {
		for _, s := range scanFBSigs(l) {
			r.sigs[s.name] = s
		}
	}
	for _, s := range scanFBSigs(src) {
		r.sigs[s.name] = s
	}
	return r
}

// lookup finds a user FB signature by type name (case-insensitively, the
// way IEC treats identifiers).
func (r *resolver) lookup(typ string) (fbSig, bool) {
	if r == nil {
		return fbSig{}, false
	}
	if s, ok := r.sigs[typ]; ok {
		return s, true
	}
	for name, s := range r.sigs {
		if strings.EqualFold(name, typ) {
			return s, true
		}
	}
	return fbSig{}, false
}

// scanFBSigs reads every `FUNCTION_BLOCK … END_FUNCTION_BLOCK` in source and
// returns its VAR_INPUT / VAR_OUTPUT pins. It is deliberately textual: it
// runs before any parse, over ST and LD alike, and a shape it cannot read
// simply yields no signature (the caller falls back to the IN/Q default and
// the real compiler reports whatever is wrong).
func scanFBSigs(src string) []fbSig {
	var out []fbSig
	lines := strings.Split(src, "\n")
	name, start := "", -1
	for i, l := range lines {
		if start == -1 {
			if m := fbStartRe.FindStringSubmatch(l); m != nil {
				name, start = m[1], i+1
			}
			continue
		}
		if fbEndRe.MatchString(l) {
			out = append(out, scanFBBody(name, strings.Join(lines[start:i], "\n")))
			name, start = "", -1
		}
	}
	if start != -1 {
		out = append(out, scanFBBody(name, strings.Join(lines[start:], "\n")))
	}
	return out
}

func scanFBBody(name, body string) fbSig {
	sig := fbSig{name: name}
	// A block's body may itself be ladder; the pin sections precede it, and
	// stripping comments first keeps a commented-out END_VAR out of the way.
	body = commentRe.ReplaceAllString(body, " ")
	body = lineCommentRe.ReplaceAllString(body, "")
	for _, m := range varSectionRe.FindAllStringSubmatch(body, -1) {
		pins := parseDecls(m[2])
		if strings.EqualFold(m[1], "VAR_INPUT") {
			sig.inputs = append(sig.inputs, pins...)
		} else {
			sig.outputs = append(sig.outputs, pins...)
		}
	}
	return sig
}

var declNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseDecls reads `a, b : TYPE := init;` declarations out of one VAR
// section's text — several to a line, or several lines to a declaration.
func parseDecls(text string) []pin {
	var out []pin
	for _, d := range strings.Split(text, ";") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		names, typ, ok := strings.Cut(d, ":")
		if !ok {
			continue
		}
		if i := strings.Index(typ, ":="); i >= 0 {
			typ = typ[:i]
		}
		typ = strings.TrimSpace(typ)
		if typ == "" {
			continue
		}
		for _, n := range strings.Split(names, ",") {
			n = strings.TrimSpace(n)
			if declNameRe.MatchString(n) {
				out = append(out, pin{name: n, typ: typ})
			}
		}
	}
	return out
}

// bindsPin reports whether an argument list already binds pin, with either
// `:=` (an input) or `=>` (an output capture).
func bindsPin(args, pin string) bool {
	re := regexp.MustCompile(`(?i)(^|[,(\s])` + regexp.QuoteMeta(pin) + `\s*(:=|=>)`)
	return re.MatchString(args)
}

// builtinPowerPins is the boolean in/out pin a rung's power rail connects on
// each STANDARD block.
func builtinPowerPins(typ string) (in, out string, ok bool) {
	switch strings.ToUpper(typ) {
	case "TON", "TOF", "TP":
		return "IN", "Q", true
	case "CTU":
		return "CU", "Q", true
	case "CTD":
		return "CD", "Q", true
	case "R_TRIG", "F_TRIG":
		return "CLK", "Q", true
	case "SR":
		return "S1", "Q1", true
	case "RS":
		return "S", "Q1", true
	}
	return "", "", false
}

// powerPins reports the pins a rung's power enters and leaves a block on.
//
// Standard blocks use the table above. A USER block resolved from this file
// or a project library uses `EN`/`ENO` when it declares them, else its first
// BOOL VAR_INPUT the call does not already bind and its first BOOL
// VAR_OUTPUT. Either may come back empty, and both are meaningful:
//
//   - no power-in — every BOOL input is bound by name, or the block has
//     none: the block takes no power, so it may only sit on a rung whose
//     condition is the rail itself. seriesCond enforces that.
//   - no power-out — the block has no BOOL output: power PASSES THROUGH
//     unchanged, so whatever conditioned the block still conditions the
//     coils to its right.
//
// A type nothing declares falls back to IN/Q — the pre-existing default,
// which keeps a block whose library the tooling could not see compiling to
// the same ST it always did.
func (r *resolver) powerPins(typ, args string) (in, out string) {
	if in, out, ok := builtinPowerPins(typ); ok {
		return in, out
	}
	sig, ok := r.lookup(typ)
	if !ok {
		return "IN", "Q"
	}
	for _, p := range sig.inputs {
		if strings.EqualFold(p.name, "EN") && isBool(p.typ) {
			in = p.name
			break
		}
	}
	if in == "" {
		for _, p := range sig.inputs {
			if isBool(p.typ) && !bindsPin(args, p.name) {
				in = p.name
				break
			}
		}
	}
	for _, p := range sig.outputs {
		if strings.EqualFold(p.name, "ENO") && isBool(p.typ) {
			out = p.name
			break
		}
	}
	if out == "" {
		for _, p := range sig.outputs {
			if isBool(p.typ) {
				out = p.name
				break
			}
		}
	}
	return in, out
}

func isBool(typ string) bool { return strings.EqualFold(strings.TrimSpace(typ), "BOOL") }
