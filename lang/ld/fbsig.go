package ld

import (
	"regexp"
	"strings"

	"github.com/joyautomation/nautilus/lang/fbcatalog"
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

// resolver answers "which pins does a rung's power use on this block type?"
// It knows the standard blocks outright and the user blocks the shared
// catalog scan (lang/fbcatalog) found in this file and its libraries.
type resolver struct{ scope *fbcatalog.Scope }

var (
	fbStartRe = regexp.MustCompile(`(?i)^\s*FUNCTION_BLOCK\s+([A-Za-z_][A-Za-z0-9_]*)`)
	fbEndRe   = regexp.MustCompile(`(?i)^\s*END_FUNCTION_BLOCK\s*$`)
)

// newResolver scans src plus any library sources for user FB signatures.
// Later sources win, so the file being compiled shadows a library — the
// same precedence the ST compiler's in-file FB table has.
func newResolver(src string, libs []string) *resolver {
	return &resolver{scope: fbcatalog.NewScope(src, libs)}
}

// lookup finds a user FB signature by type name (case-insensitively).
func (r *resolver) lookup(typ string) (fbcatalog.Sig, bool) {
	if r == nil {
		return fbcatalog.Sig{}, false
	}
	return r.scope.Lookup(typ)
}

// scanFBSigs reads every FUNCTION_BLOCK's pins out of ST or LD source
// (fbcatalog.ScanSigs: textual, comment-aware, runs before any parse).
func scanFBSigs(src string) []fbcatalog.Sig { return fbcatalog.ScanSigs(src) }

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
	case "CTUD":
		return "CU", "QU", true
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
	for _, p := range sig.Inputs {
		if strings.EqualFold(p.Name, "EN") && isBool(p.Type) {
			in = p.Name
			break
		}
	}
	if in == "" {
		for _, p := range sig.Inputs {
			if isBool(p.Type) && !bindsPin(args, p.Name) {
				in = p.Name
				break
			}
		}
	}
	for _, p := range sig.Outputs {
		if strings.EqualFold(p.Name, "ENO") && isBool(p.Type) {
			out = p.Name
			break
		}
	}
	if out == "" {
		for _, p := range sig.Outputs {
			if isBool(p.Type) {
				out = p.Name
				break
			}
		}
	}
	return in, out
}

func isBool(typ string) bool { return strings.EqualFold(strings.TrimSpace(typ), "BOOL") }
