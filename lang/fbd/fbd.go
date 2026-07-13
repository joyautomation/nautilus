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
func TranspileWithLines(src string) (string, []int, error) {
	header, body, footer, bodyLine, err := splitFBD(src)
	if err != nil {
		return "", nil, err
	}
	net, err := parseNetlist(body, bodyLine)
	if err != nil {
		return "", nil, err
	}
	stmts, stmtLines, fbDecls, err := net.transpile()
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	var lineMap []int
	writeLine := func(s string, orig int) {
		b.WriteString(s)
		b.WriteByte('\n')
		lineMap = append(lineMap, orig)
	}
	for i, l := range strings.Split(header, "\n") {
		writeLine(l, i+1)
	}
	if len(fbDecls) > 0 {
		writeLine("VAR", fbDecls[0].line)
		for _, d := range fbDecls {
			writeLine(fmt.Sprintf("  %s : %s;", d.name, d.typ), d.line)
		}
		writeLine("END_VAR", fbDecls[len(fbDecls)-1].line)
	}
	for i, s := range stmts {
		writeLine(s, stmtLines[i])
	}
	// The footer is the source tail verbatim; written raw (no trailing
	// newline added) to keep the output byte-identical with what tests and
	// hashes have seen.
	footerLines := strings.Split(footer, "\n")
	footerStart := len(strings.Split(src, "\n")) - len(footerLines) + 1
	b.WriteString(footer)
	for i := range footerLines {
		lineMap = append(lineMap, footerStart+i)
	}
	return b.String(), lineMap, nil
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
