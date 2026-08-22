// Package fbd compiles IEC 61131-3 Function Block Diagram source (.fbd) to the
// shared nautilus IR. FBD is a graphical language; nautilus's git-diffable form
// is a concise expression netlist — named blocks whose inputs are variables,
// constants, or other blocks' outputs — inside an otherwise-ST POU:
//
//	PROGRAM MotorControl
//	VAR_EXTERNAL Start : BOOL; Stop : BOOL; Run : BOOL; Started : BOOL; END_VAR
//	FBD
//	  seal  = OR(Start, Run)          // a wire "seal" driven by an OR block
//	  latch = AND(seal, NOT Stop)     // NOT is an inline pin negation
//	  Run  := latch                   // coil: drive a variable from a wire
//	  t1 : TON(IN := Run, PT := T#5S) // a standard function-block instance
//	  Started := t1.Q                 // read an FB output pin
//	END_FBD
//	END_PROGRAM
//
// Compilation transpiles the netlist to equivalent ST and reuses the ST
// front-end (st.Parse + st.Lower), so FBD inherits the whole type system, the
// standard function/FB library, and diagnostics. Wires are pure combinational
// blocks: they are inlined at each use (fan-out just duplicates a pure
// expression) and must be acyclic. Variables carry state, so a variable fed
// back into an earlier block is a seal-in latch, exactly as a PLC evaluates it.
package fbd

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// Compile parses .fbd source and lowers it to an ir.Program, reusing the ST
// front-end. userFBs/userFuncs registries are forwarded to st.Lower.
func Compile(src string, userFBs ...map[string]*ir.FBDef) (*ir.Program, error) {
	stSrc, err := Transpile(src)
	if err != nil {
		return nil, err
	}
	prog, err := st.Parse(stSrc)
	if err != nil {
		return nil, err
	}
	return st.Lower(prog, userFBs...)
}

// Transpile converts .fbd source to equivalent ST source. Exported so the LSP
// and diagram tooling can obtain the ST form (and thus diagnostics) without
// re-implementing the netlist semantics.
func Transpile(src string) (string, error) {
	stSrc, _, err := TranspileWithLines(src)
	return stSrc, err
}

// TranspileWithLines is Transpile plus a line map: lineMap[i] is the 1-based
// line in the .fbd source that 1-based line i+1 of the ST output came from.
// Header and footer lines map verbatim; each generated statement maps to the
// netlist statement it was emitted for, so diagnostics against the ST form
// can be projected back onto the .fbd file exactly.
//
// A file may hold SEVERAL netlist blocks — one per POU. That is what lets a
// FUNCTION_BLOCK carry an FBD (or, through the LD → FBD hop, a ladder) body
// alongside the file's PROGRAM: each FBD … END_FBD becomes the statements of
// the POU it sits in, and everything between blocks (the next POU's header,
// its VAR sections, END_FUNCTION_BLOCK) passes through as ST verbatim.
func TranspileWithLines(src string) (string, []int, error) {
	blocks, err := splitBlocks(src)
	if err != nil {
		return "", nil, err
	}
	lines := strings.Split(src, "\n")

	var b strings.Builder
	var lineMap []int
	writeLine := func(s string, orig int) {
		b.WriteString(s)
		b.WriteByte('\n')
		lineMap = append(lineMap, orig)
	}

	prev := 0 // 0-based index of the first not-yet-written line
	for _, blk := range blocks {
		for i := prev; i < blk.start; i++ {
			writeLine(lines[i], i+1)
		}
		body := strings.Join(lines[blk.start+1:blk.end], "\n")
		net, err := parseNetlist(body, blk.start+1)
		if err != nil {
			return "", nil, err
		}
		stmts, stmtLines, fbDecls, err := net.transpile()
		if err != nil {
			return "", nil, err
		}
		// An instance the POU's own header already declares keeps that
		// declaration — the netlist's `inst : TYPE(...)` is then a CALL on
		// a declared instance, not a second declaration of it. (Without
		// this, writing both — which reads naturally, and is how a block's
		// retained timers are usually spelled out — is a duplicate slot.)
		declared := declaredNames(strings.Join(lines[prev:blk.start], "\n"))
		var fresh []fbDecl
		for _, d := range fbDecls {
			if !declared[strings.ToLower(d.name)] {
				fresh = append(fresh, d)
			}
		}
		if len(fresh) > 0 {
			writeLine("VAR", fresh[0].line)
			for _, d := range fresh {
				writeLine(fmt.Sprintf("  %s : %s;", d.name, d.typ), d.line)
			}
			writeLine("END_VAR", fresh[len(fresh)-1].line)
		}
		for i, s := range stmts {
			writeLine(s, stmtLines[i])
		}
		prev = blk.end + 1 // drops END_FBD; the POU's own END_ keyword follows
	}
	// The tail is the source's remainder verbatim; written raw (no trailing
	// newline added) to keep the output byte-identical with what tests and
	// hashes have seen.
	tail := strings.Join(lines[prev:], "\n")
	b.WriteString(tail)
	for i := prev; i < len(lines); i++ {
		lineMap = append(lineMap, i+1)
	}
	if prev >= len(lines) {
		// Nothing after the last END_FBD: the empty tail still splits to
		// one (empty) line, so the map keeps one entry per output line.
		lineMap = append(lineMap, len(lines))
	}
	return b.String(), lineMap, nil
}

// block is one FBD … END_FBD netlist body, by 0-based line index.
type block struct{ start, end int }

// headerDeclRe matches a `name :` declaration head inside a VAR section.
var headerDeclRe = regexp.MustCompile(`(?im)(?:^|[;,])\s*([A-Za-z_][A-Za-z0-9_]*)\s*:[^=]`)

// headerSectionRe spans one VAR* … END_VAR section, however it is laid out.
var headerSectionRe = regexp.MustCompile(`(?is)\bVAR(?:_[A-Z]+)?\b(.*?)\bEND_VAR\b`)

// declaredNames lists the variable names a POU's header declares, lowered
// for case-insensitive comparison (IEC identifiers are case-insensitive).
func declaredNames(header string) map[string]bool {
	out := map[string]bool{}
	for _, sec := range headerSectionRe.FindAllStringSubmatch(header, -1) {
		for _, m := range headerDeclRe.FindAllStringSubmatch("\n"+sec[1], -1) {
			out[strings.ToLower(m[1])] = true
		}
	}
	return out
}

// splitBlocks finds every FBD … END_FBD pair in source order and rejects the
// malformed shapes (an unclosed block, an END_FBD with no opener).
func splitBlocks(src string) ([]block, error) {
	lines := strings.Split(src, "\n")
	var out []block
	start := -1
	for i, l := range lines {
		switch strings.ToUpper(strings.TrimSpace(l)) {
		case "FBD":
			if start != -1 {
				return nil, fmt.Errorf("fbd: line %d: FBD inside an FBD block — the previous one has no END_FBD", i+1)
			}
			start = i
		case "END_FBD":
			if start == -1 {
				return nil, fmt.Errorf("fbd: line %d: END_FBD with no FBD", i+1)
			}
			out = append(out, block{start: start, end: i})
			start = -1
		}
	}
	if start != -1 {
		return nil, fmt.Errorf("fbd: line %d: FBD block has no END_FBD", start+1)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("fbd: source must contain an FBD ... END_FBD body")
	}
	return out, nil
}

// splitFBD separates the source into the ST header (up to the FBD keyword),
// the netlist body (between FBD and END_FBD), and the footer (END_FBD onward,
// with END_FBD replaced by nothing so END_PROGRAM remains). The header/footer
// are valid ST verbatim. bodyLine is the number of file lines preceding the
// body (the offset that maps body-relative parse positions back to the file).
func splitFBD(src string) (header, body, footer string, bodyLine int, err error) {
	lines := strings.Split(src, "\n")
	start, end := -1, -1
	for i, l := range lines {
		switch strings.ToUpper(strings.TrimSpace(l)) {
		case "FBD":
			if start == -1 {
				start = i
			}
		case "END_FBD":
			end = i
		}
	}
	if start == -1 || end == -1 || end < start {
		return "", "", "", 0, fmt.Errorf("fbd: source must contain an FBD ... END_FBD body")
	}
	header = strings.Join(lines[:start], "\n")
	body = strings.Join(lines[start+1:end], "\n")
	footer = strings.Join(lines[end+1:], "\n") // drops END_FBD; keeps END_PROGRAM
	return header, body, footer, start + 1, nil
}
