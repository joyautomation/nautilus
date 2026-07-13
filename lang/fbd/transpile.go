package fbd

import (
	"fmt"
	"strings"
)

// transpile turns the netlist into ordered ST statement strings plus the FB
// instance declarations to inject as a VAR block. lines[i] is the 1-based
// .fbd source line stmts[i] came from, so diagnostics can be mapped back.
func (nl *netlist) transpile() (stmts []string, lines []int, fbDecls []fbDecl, err error) {
	// Order nodes (FB calls + coils) so an FB call precedes any node reading
	// its outputs; ties keep source order. Cycles fall back to source order.
	order, err := nl.order()
	if err != nil {
		return nil, nil, nil, err
	}
	for _, i := range order {
		n := nl.nodes[i]
		if n.isCall {
			s, err := nl.emitCall(n)
			if err != nil {
				return nil, nil, nil, err
			}
			stmts = append(stmts, s)
		} else {
			e, err := nl.emit(n.source, nil)
			if err != nil {
				return nil, nil, nil, err
			}
			stmts = append(stmts, fmt.Sprintf("%s := %s;", n.target, e))
		}
		lines = append(lines, n.line)
	}
	return stmts, lines, nl.fbDecls, nil
}

// order returns node indices in dependency order (FB call before readers of
// its pins), stable by source order, tolerant of cycles.
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
			if j, ok := callOf[inst]; ok && j != i {
				deps[i][j] = true
			}
		}
	}
	emitted := make([]bool, len(nl.nodes))
	var out []int
	for len(out) < len(nl.nodes) {
		progress := false
		for i := range nl.nodes {
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
			for i := range nl.nodes {
				if !emitted[i] {
					emitted[i] = true
					out = append(out, i)
					break
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
		for _, a := range x.args {
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

// emitCall renders an FB invocation as an ST call statement.
func (nl *netlist) emitCall(n node) (string, error) {
	var args []string
	for _, a := range n.args {
		e, err := nl.emit(a.val, nil)
		if err != nil {
			return "", err
		}
		args = append(args, fmt.Sprintf("%s := %s", a.pin, e))
	}
	return fmt.Sprintf("%s(%s);", n.inst, strings.Join(args, ", ")), nil
}

// emit renders an FBD expression as ST source, inlining wire references.
func (nl *netlist) emit(e expr, visited []string) (string, error) {
	switch x := e.(type) {
	case litExpr:
		return x.text, nil
	case pinExpr:
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
