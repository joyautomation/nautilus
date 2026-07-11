// Package fbd compiles IEC 61131-3 Function Block Diagram source (.fbd) to the
// shared nautilus IR. FBD is a graphical language; nautilus's git-diffable form
// is a concise expression netlist — named blocks whose inputs are variables,
// constants, or other blocks' outputs — inside an otherwise-ST POU:
//
//	PROGRAM MotorControl
//	VAR_EXTERNAL Start : BOOL; Stop : BOOL; Run : BOOL; Started : BOOL; END_VAR
//	FBD
//	  seal  = OR(Start, Run)          -- a wire "seal" driven by an OR block
//	  latch = AND(seal, NOT Stop)     -- NOT is an inline pin negation
//	  Run  := latch                   -- coil: drive a variable from a wire
//	  t1 : TON(IN := Run, PT := T#5S) -- a standard function-block instance
//	  Started := t1.Q                 -- read an FB output pin
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
	header, body, footer, err := splitFBD(src)
	if err != nil {
		return "", err
	}
	net, err := parseNetlist(body)
	if err != nil {
		return "", err
	}
	stmts, fbDecls, err := net.transpile()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(header)
	if !strings.HasSuffix(header, "\n") {
		b.WriteByte('\n')
	}
	if len(fbDecls) > 0 {
		b.WriteString("VAR\n")
		for _, d := range fbDecls {
			fmt.Fprintf(&b, "  %s : %s;\n", d.name, d.typ)
		}
		b.WriteString("END_VAR\n")
	}
	for _, s := range stmts {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	b.WriteString(footer)
	return b.String(), nil
}

// splitFBD separates the source into the ST header (up to the FBD keyword),
// the netlist body (between FBD and END_FBD), and the footer (END_FBD onward,
// with END_FBD replaced by nothing so END_PROGRAM remains). The header/footer
// are valid ST verbatim.
func splitFBD(src string) (header, body, footer string, err error) {
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
		return "", "", "", fmt.Errorf("fbd: source must contain an FBD ... END_FBD body")
	}
	header = strings.Join(lines[:start], "\n")
	body = strings.Join(lines[start+1:end], "\n")
	footer = strings.Join(lines[end+1:], "\n") // drops END_FBD; keeps END_PROGRAM
	return header, body, footer, nil
}
