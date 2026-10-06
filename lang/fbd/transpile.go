package fbd

import (
	"fmt"
	"strings"
)

// transpile turns the netlist into ordered ST statement strings plus the FB
// instance declarations to inject as a VAR block. lines[i] is the 1-based
// .fbd source line stmts[i] came from, so diagnostics can be mapped back.
func (nl *netlist) transpile() (stmts []string, lines []int, fbDecls []fbDecl, err error) {
	if err := nl.prepareExec(); err != nil {
		return nil, nil, nil, err
	}
	// Order nodes (FB calls + coils) network by network; within a network
	// an FB call precedes any node reading its outputs; ties keep source
	// order. Cycles fall back to source order.
	order, err := nl.order()
	if err != nil {
		return nil, nil, nil, err
	}
	enoDone := map[string]bool{} // exec wires whose ENO is already written
	for _, i := range order {
		n := nl.nodes[i]
		var s string
		if n.isCall {
			s, err = nl.emitCall(n)
		} else {
			s, err = nl.emitCoil(n, enoDone)
		}
		if err != nil {
			return nil, nil, nil, err
		}
		stmts = append(stmts, s)
		lines = append(lines, n.line)
	}
	decls := append([]fbDecl(nil), nl.fbDecls...)
	for _, name := range nl.hiddenOrder {
		decls = append(decls, fbDecl{name: name + "__ENO", typ: "BOOL", line: nl.hiddenLine[name]})
	}
	return stmts, lines, decls, nil
}

// ── EN/ENO (execution control) ──────────────────────────────────────────────
//
// An FB call passes EN := … and ENO => … straight through to ST, whose
// lowering gates the call (lang/st lower_eneno.go — and leaves them alone on
// a block that declares its own EN/ENO pins). A FUNCTION block with EN/ENO
// is gated here: its result is written only while EN is TRUE, so the block
// must drive a variable — a coil, directly or through its named wire. A
// diagram that reads ENO as a wire (`t1.ENO`, `w.ENO`) reads a hidden BOOL
// `<name>__ENO` the call writes (a double underscore: never a legal IEC
// identifier of the user's own).

// prepareExec indexes the netlist's execution control: which instances and
// wires have their ENO read as a wire, and the first coil each EN/ENO wire
// drives (where its ENO is written).
func (nl *netlist) prepareExec() error {
	nl.hiddenENO = map[string]bool{}
	nl.hiddenLine = map[string]int{}
	nl.hiddenOrder = nil
	nl.execFirst = map[string]int{}
	callOf := map[string]int{}
	for i, n := range nl.nodes {
		if n.isCall {
			callOf[n.inst] = i
		}
	}
	var visit func(e expr)
	visit = func(e expr) {
		switch x := e.(type) {
		case pinExpr:
			if !strings.EqualFold(x.pin, "ENO") || nl.hiddenENO[x.inst] {
				return
			}
			if j, ok := callOf[x.inst]; ok {
				nl.addHidden(x.inst, nl.nodes[j].line)
			} else if w, ok := nl.wires[x.inst]; ok {
				if _, isCall := w.(callExpr); isCall {
					nl.addHidden(x.inst, x.line)
				}
			}
		case notExpr:
			visit(x.inner)
		case callExpr:
			for _, a := range x.reads() {
				visit(a)
			}
		}
	}
	for _, w := range nl.wireSrc {
		visit(nl.wires[w])
	}
	for _, n := range nl.nodes {
		if n.isCall {
			for _, a := range n.args {
				if !a.out {
					visit(a.val)
				}
			}
		} else {
			visit(n.source)
		}
	}
	for i, n := range nl.nodes {
		if n.isCall {
			continue
		}
		if _, wire, ok := nl.execTop(n.source); ok && wire != "" {
			if _, seen := nl.execFirst[wire]; !seen {
				nl.execFirst[wire] = i
			}
		}
	}
	for _, name := range nl.hiddenOrder {
		if _, isWire := nl.wires[name]; !isWire {
			continue
		}
		if _, ok := nl.execFirst[name]; !ok {
			fn := ""
			if c, ok := nl.wires[name].(callExpr); ok {
				fn = c.fn + " "
			}
			return &ParseError{Line: nl.hiddenLine[name], Col: 1, Msg: fmt.Sprintf(
				"%s%s.ENO is read, but %s drives no variable — a block's ENO is written where its result is (wire %s to a coil)",
				fn, name, name, name)}
		}
	}
	return nil
}

func (nl *netlist) addHidden(name string, line int) {
	nl.hiddenENO[name] = true
	nl.hiddenLine[name] = line
	nl.hiddenOrder = append(nl.hiddenOrder, name)
}

// execTop finds the block an expression's value comes from when that block
// has execution control: the expression itself, or the named wire(s) it
// reads straight through. wire is the wire's name ("" for an inline call).
func (nl *netlist) execTop(e expr) (callExpr, string, bool) {
	wire := ""
	for range 64 { // wire chains are short; a cycle is reported elsewhere
		switch x := e.(type) {
		case callExpr:
			// A wire whose ENO the diagram reads is gated too (EN unbound:
			// it always runs, but its ENO has to be written somewhere).
			return x, wire, x.execControl() || wire != "" && nl.hiddenENO[wire]
		case refExpr:
			w, ok := nl.wires[x.name]
			if !ok {
				return callExpr{}, "", false
			}
			e, wire = w, x.name
			continue
		}
		return callExpr{}, "", false
	}
	return callExpr{}, "", false
}

// emitCoil renders a coil: `target := expr;`, or — when its source is a
// block with EN/ENO — the gated form, ENO written by the first coil the
// block drives:
//
//	IF en THEN [eno := TRUE;] target := F(...); ELSE [eno := FALSE;] END_IF;
func (nl *netlist) emitCoil(n node, enoDone map[string]bool) (string, error) {
	c, wire, gated := nl.execTop(n.source)
	if !gated {
		e, err := nl.emit(n.source, nil)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s := %s;", n.target, e), nil
	}
	plain := c
	plain.en, plain.eno = nil, nil
	val, err := nl.emitBlock(plain, nil)
	if err != nil {
		return "", err
	}
	// ENO targets: the explicit `ENO => x` binding, and the hidden wire
	// read — each written once, at the first coil the block drives.
	var enoTargets []string
	if wire == "" || !enoDone[wire] {
		if c.eno != nil {
			enoTargets = append(enoTargets, exprText(c.eno))
		}
		if wire != "" && nl.hiddenENO[wire] {
			enoTargets = append(enoTargets, wire+"__ENO")
		}
		if wire != "" {
			enoDone[wire] = true
		}
	}
	okVal := "TRUE"
	if (c.fn == "DIV" || c.fn == "MOD") && len(c.args) == 2 {
		// ENO = EN AND no error: a zero divisor is the error (the result
		// follows the documented ÷0 rule either way).
		args, err := nl.positionalArgs(c)
		if err != nil {
			return "", err
		}
		d, err := nl.emit(args[1], nil)
		if err != nil {
			return "", err
		}
		okVal = "(" + d + " <> 0)"
	}
	var then, els []string
	for _, t := range enoTargets {
		then = append(then, t+" := "+okVal+";")
		els = append(els, t+" := FALSE;")
	}
	then = append(then, fmt.Sprintf("%s := %s;", n.target, val))
	if c.en == nil {
		return strings.Join(then, " "), nil
	}
	en, err := nl.emit(c.en, nil)
	if err != nil {
		return "", err
	}
	out := "IF " + en + " THEN " + strings.Join(then, " ")
	if len(els) > 0 {
		out += " ELSE " + strings.Join(els, " ")
	}
	return out + " END_IF;", nil
}

// order returns node indices network by network; within a network in
// dependency order (FB call before readers of its pins), stable by source
// order, tolerant of cycles. A read of a LATER network's FB output is no
// dependency: networks run in order, so it sees last scan's value.
func (nl *netlist) order() ([]int, error) {
	callOf := map[string]int{} // inst -> node index of its call
	for i, n := range nl.nodes {
		if n.isCall {
			callOf[n.inst] = i
		}
	}
	// deps[i] = set of node indices node i must follow.
	deps := make([]map[int]bool, len(nl.nodes))
	for i, n := range nl.nodes {
		deps[i] = map[int]bool{}
		insts := map[string]bool{}
		if n.isCall {
			for _, a := range n.args {
				if a.out {
					continue // a binding target is written, not read
				}
				if err := nl.readsPins(a.val, insts, nil); err != nil {
					return nil, err
				}
			}
		} else {
			if err := nl.readsPins(n.source, insts, nil); err != nil {
				return nil, err
			}
		}
		for inst := range insts {
			j, ok := callOf[inst]
			if !ok {
				// w.ENO: written by the first coil wire w drives.
				j, ok = nl.execFirst[inst]
			}
			if ok && j != i && nl.nodes[j].net == n.net {
				deps[i][j] = true
			}
		}
	}
	var out []int
	for net := range max(len(nl.networks), 1) {
		var members []int
		for i, n := range nl.nodes {
			if n.net == net {
				members = append(members, i)
			}
		}
		emitted := map[int]bool{}
		for range members {
			progress := false
			for _, i := range members {
				if emitted[i] {
					continue
				}
				ready := true
				for d := range deps[i] {
					if !emitted[d] {
						ready = false
						break
					}
				}
				if ready {
					emitted[i] = true
					out = append(out, i)
					progress = true
					break // restart to preserve source-order stability
				}
			}
			if !progress {
				// Cycle among FB calls — emit the earliest remaining in source
				// order (scan semantics: the reader gets last-scan values).
				for _, i := range members {
					if !emitted[i] {
						emitted[i] = true
						out = append(out, i)
						break
					}
				}
			}
		}
	}
	return out, nil
}

// readsPins collects the FB-instance names whose output pins an expression
// reads, following inlined wires. visited guards wire cycles.
func (nl *netlist) readsPins(e expr, into map[string]bool, visited []string) error {
	switch x := e.(type) {
	case pinExpr:
		into[x.inst] = true
	case notExpr:
		return nl.readsPins(x.inner, into, visited)
	case callExpr:
		for _, a := range x.reads() {
			if err := nl.readsPins(a, into, visited); err != nil {
				return err
			}
		}
	case refExpr:
		if w, ok := nl.wires[x.name]; ok {
			for _, v := range visited {
				if v == x.name {
					return fmt.Errorf("fbd: combinational loop through wire %q", x.name)
				}
			}
			return nl.readsPins(w, into, append(visited, x.name))
		}
	}
	return nil
}

// emitCall renders an FB invocation as an ST call statement. EN := … and
// ENO => … pass through (the ST lowering owns their semantics); a diagram
// that reads inst.ENO as a wire gets it bound to the hidden inst__ENO,
// and an explicit `ENO => x` then copies from there.
func (nl *netlist) emitCall(n node) (string, error) {
	var args []string
	var after []string
	hidden := nl.hiddenENO[n.inst]
	for _, a := range n.args {
		if a.out {
			if hidden && strings.EqualFold(a.pin, "ENO") {
				after = append(after, fmt.Sprintf(" %s := %s__ENO;", exprText(a.val), n.inst))
				continue
			}
			// Output bindings pass through VERBATIM: the target is a write
			// destination, never inlined as a wire read.
			args = append(args, fmt.Sprintf("%s => %s", a.pin, exprText(a.val)))
			continue
		}
		e, err := nl.emit(a.val, nil)
		if err != nil {
			return "", err
		}
		args = append(args, fmt.Sprintf("%s := %s", a.pin, e))
	}
	if hidden {
		args = append(args, "ENO => "+n.inst+"__ENO")
	}
	return fmt.Sprintf("%s(%s);", n.inst, strings.Join(args, ", ")) + strings.Join(after, ""), nil
}

// exprText renders a binding target (a ref, an accessor chain, or a
// single `.member` struct field) as-written.
func exprText(e expr) string {
	switch x := e.(type) {
	case refExpr:
		return x.name
	case accExpr:
		return x.text
	case pinExpr:
		return x.inst + "." + x.pin
	}
	return "_"
}

// emit renders an FBD expression as ST source, inlining wire references.
func (nl *netlist) emit(e expr, visited []string) (string, error) {
	switch x := e.(type) {
	case litExpr:
		return x.text, nil
	case accExpr:
		return x.text, nil // Levels[2], tbl[i].val — ST accepts it verbatim
	case pinExpr:
		if strings.EqualFold(x.pin, "ENO") && nl.hiddenENO[x.inst] {
			return x.inst + "__ENO", nil
		}
		return x.inst + "." + x.pin, nil
	case notExpr:
		s, err := nl.emit(x.inner, visited)
		if err != nil {
			return "", err
		}
		return "NOT (" + s + ")", nil
	case refExpr:
		if w, ok := nl.wires[x.name]; ok {
			for _, v := range visited {
				if v == x.name {
					return "", fmt.Errorf("fbd: combinational loop through wire %q", x.name)
				}
			}
			return nl.emit(w, append(visited, x.name))
		}
		return x.name, nil // a variable
	case callExpr:
		return nl.emitBlock(x, visited)
	}
	return "", fmt.Errorf("fbd: unrenderable expression %T", e)
}

// emitBlock maps an IEC standard function/operator block to ST syntax:
// boolean/bit and arithmetic operators become infix, comparisons binary
// infix, NOT prefix, MOVE a pass-through, and everything else a function call
// (resolved against the runtime's standard-function library by st.Lower).
func (nl *netlist) emitBlock(c callExpr, visited []string) (string, error) {
	if c.execControl() {
		line, col := c.pos()
		return "", &ParseError{Line: line, Col: col, Msg: fmt.Sprintf(
			"%s has EN/ENO, so its result is written only while EN is TRUE — it must drive a variable "+
				"(a coil, directly or through its named wire), not feed another block", c.fn)}
	}
	if c.names != nil {
		pos, err := nl.positionalArgs(c)
		if err != nil {
			if !isOperator(c.fn) && err == errUserFormal {
				// A user FUNCTION's own input names: a formal ST call.
				parts := make([]string, len(c.args))
				for i, a := range c.args {
					s, err := nl.emit(a, visited)
					if err != nil {
						return "", err
					}
					parts[i] = c.names[i] + " := " + s
				}
				return c.fn + "(" + strings.Join(parts, ", ") + ")", nil
			}
			if err == errUserFormal {
				err = fmt.Errorf("fbd: %s's inputs are %s", c.fn, strings.Join(blockPins(c.fn, len(c.args)), ", "))
			}
			return "", err
		}
		c.args, c.names = pos, nil
	}
	args := make([]string, len(c.args))
	for i, a := range c.args {
		s, err := nl.emit(a, visited)
		if err != nil {
			return "", err
		}
		args[i] = s
	}
	infix := func(op string) (string, error) {
		if len(args) < 2 {
			return "", fmt.Errorf("fbd: %s needs at least 2 inputs", c.fn)
		}
		return "(" + strings.Join(args, " "+op+" ") + ")", nil
	}
	binary := func(op string) (string, error) {
		if len(args) != 2 {
			return "", fmt.Errorf("fbd: %s needs exactly 2 inputs", c.fn)
		}
		return "(" + args[0] + " " + op + " " + args[1] + ")", nil
	}
	switch c.fn {
	case "AND":
		return infix("AND")
	case "OR":
		return infix("OR")
	case "XOR":
		return infix("XOR")
	case "ADD":
		return infix("+")
	case "MUL":
		return infix("*")
	case "SUB":
		return binary("-")
	case "DIV":
		return binary("/")
	case "MOD":
		return binary("MOD")
	case "GT":
		return binary(">")
	case "GE":
		return binary(">=")
	case "LT":
		return binary("<")
	case "LE":
		return binary("<=")
	case "EQ":
		return binary("=")
	case "NE":
		return binary("<>")
	case "NOT":
		if len(args) != 1 {
			return "", fmt.Errorf("fbd: NOT needs exactly 1 input")
		}
		return "NOT (" + args[0] + ")", nil
	case "MOVE":
		if len(args) != 1 {
			return "", fmt.Errorf("fbd: MOVE needs exactly 1 input")
		}
		return args[0], nil
	default:
		// A standard function (MIN, MAX, ABS, LIMIT, SQRT, ...) — emit as an
		// ST call; st.Lower validates it against the function library.
		return c.fn + "(" + strings.Join(args, ", ") + ")", nil
	}
}

// errUserFormal: the formal names are not the standard ones (a user
// FUNCTION's own input names, or a typo the compiler will name).
var errUserFormal = fmt.Errorf("fbd: formal names are not the standard ones")

// positionalArgs orders a formal call's arguments by the standard formal
// names (ir.FormalNames): `LIMIT(IN := x, MN := 0, MX := 9)` → (0, x, 9).
func (nl *netlist) positionalArgs(c callExpr) ([]expr, error) {
	if c.names == nil {
		return c.args, nil
	}
	formal := blockPins(c.fn, len(c.args))
	out := make([]expr, len(formal))
	for i, n := range c.names {
		idx := -1
		for j, f := range formal {
			if strings.EqualFold(f, n) {
				idx = j
			}
		}
		if idx < 0 || out[idx] != nil {
			return nil, errUserFormal
		}
		out[idx] = c.args[i]
	}
	return out, nil
}

// isOperator reports the blocks transpiled to ST operators (no ST
// function of that name exists).
func isOperator(fn string) bool {
	switch fn {
	case "AND", "OR", "XOR", "ADD", "MUL", "SUB", "DIV", "MOD", "GT", "GE", "LT", "LE", "EQ", "NE", "NOT", "MOVE":
		return true
	}
	return false
}
