package fbd

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/lint"
)

// Lint reports the FBD structure warnings (lang/lint, nautilus#561) for one
// .fbd source: FBD001 to FBD003, at cfg's thresholds, less what cfg
// disables and what an `(* @allow … *)` pragma switches off. A source that
// does not parse has no findings: its parse error is the diagnostic.
//
// Only a .fbd file is linted this way. A ladder file's FBD netlist is the
// compiler's intermediate form, and the ladder rules (ld.Lint) judge the
// text its author wrote.
func Lint(src string, cfg lint.Config) []lint.Finding {
	blocks, err := splitBlocks(src)
	if err != nil {
		return nil
	}
	lines := strings.Split(src, "\n")
	var fs []lint.Finding
	for _, blk := range blocks {
		nl, err := parseNetlist(strings.Join(lines[blk.start+1:blk.end], "\n"), blk.start+1)
		if err != nil {
			return nil
		}
		fs = append(fs, nl.lint(cfg)...)
	}
	return lint.Filter(src, cfg, fs)
}

// stmt is one netlist statement, for the structure rules: its id in the
// function graph, its network, its line, and the expressions it reads.
type lintStmt struct {
	id    string
	net   int
	line  int
	reads []expr
	call  bool // an FB invocation: the block itself is one nesting level
}

func (nl *netlist) lintStmts() []lintStmt {
	var out []lintStmt
	for _, w := range nl.wireSrc {
		out = append(out, lintStmt{id: "w:" + ir.NameKey(w), net: nl.wireNet[w], line: nl.wirePos[w].line, reads: []expr{nl.wires[w]}})
	}
	for i, n := range nl.nodes {
		s := lintStmt{id: fmt.Sprintf("n:%d", i), net: n.net, line: n.line, call: n.isCall}
		if n.isCall {
			for _, a := range n.args {
				if !a.out {
					s.reads = append(s.reads, a.val)
				}
			}
		} else {
			s.reads = []expr{n.source}
		}
		out = append(out, s)
	}
	return out
}

func (nl *netlist) lint(cfg lint.Config) []lint.Finding {
	var fs []lint.Finding
	stmts := nl.lintStmts()

	// FBD001: nesting. A wire name is the readable alternative, so a wire
	// reference counts as nothing; NOT is pin negation, not a block.
	for _, s := range stmts {
		d := 0
		for _, e := range s.reads {
			if n := nestDepth(e); n > d {
				d = n
			}
		}
		if s.call {
			d++
		}
		if d > cfg.MaxNesting {
			fs = append(fs, lint.Finding{Rule: "FBD001", Line: s.line, Col: 1, Message: fmt.Sprintf(
				"this statement nests blocks %d deep (the limit is %d): give the inner results named "+
					"wires, one block per line (hiLevel = GE(Level, HighLevelSP)), and pass the wire names",
				d, cfg.MaxNesting)})
		}
	}

	// FBD003: a comparison against a literal threshold.
	for _, s := range stmts {
		for _, e := range s.reads {
			eachCall(e, func(c callExpr) {
				if !lint.IsThresholdCompare(c.fn) {
					return
				}
				for _, a := range c.args {
					if l, ok := a.(litExpr); ok && lint.MagicNumber(l.text) {
						fs = append(fs, lint.Finding{Rule: "FBD003", Line: c.line, Col: c.col,
							Message: lint.ThresholdMessage(lintText(c), l.text)})
						return
					}
				}
			})
		}
	}

	// FBD002: an untitled network holding more than one function. Functions
	// are the connected parts of the network's graph: statements joined by
	// a wire, by an FB instance's pins, or by a variable one of them
	// assigns and another reads (Scaled := MUL(Level, 2.0), then
	// Valve := GE(Scaled, SP) is one function). Two statements that only
	// read the same input are not joined: that input feeds both, it does
	// not make them one. A network of three statements or fewer
	// (minFunctionsStmts) is not reported: it reads without a title.
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if p, ok := parent[x]; !ok || p == x {
			parent[x] = x
			return x
		}
		r := find(parent[x])
		parent[x] = r
		return r
	}
	union := func(a, b string) { parent[find(a)] = find(b) }
	written := map[string]bool{} // "v:" keys of the variables a statement assigns
	for i, n := range nl.nodes {
		id := fmt.Sprintf("n:%d", i)
		if n.isCall {
			union(id, "i:"+instKey(n.inst))
			for _, a := range n.args {
				if a.out {
					if v := varKey(a.val); v != "" {
						written[v] = true
						union(id, v)
					}
				}
			}
		} else if n.target != "" {
			v := "v:" + instKey(n.target)
			written[v] = true
			union(id, v)
		}
	}
	for _, s := range stmts {
		find(s.id)
		for _, e := range s.reads {
			nl.eachRef(e, func(key string) {
				if _, ok := parent[key]; ok || strings.HasPrefix(key, "w:") {
					union(s.id, key)
				}
			})
			eachVar(e, func(v string) {
				if written[v] {
					union(s.id, v)
				}
			})
		}
	}
	for k, nw := range nl.networks {
		if nw.title != "" {
			continue
		}
		roots := map[string]bool{}
		first, size := 0, 0
		for _, s := range stmts {
			if s.net != k {
				continue
			}
			size++
			roots[find(s.id)] = true
			if first == 0 || s.line < first {
				first = s.line
			}
		}
		if len(roots) < 2 || size < minFunctionsStmts {
			continue
		}
		line := nw.line
		where := "this NETWORK has no title"
		if nw.implicit {
			line = first
			where = "these statements are in no titled NETWORK"
		}
		fs = append(fs, lint.Finding{Rule: "FBD002", Line: line, Col: 1, Message: fmt.Sprintf(
			"%s, and they hold %d separate functions: start each function with its own "+
				"NETWORK 'what it does' line (NETWORK 'LAH-101: high level, 10 s on-delay'), so the "+
				"diagram draws it as its own titled band", where, len(roots))})
	}
	return fs
}

// minFunctionsStmts is the smallest untitled network FBD002 judges: a
// body of three statements or fewer reads without a title, whatever it
// holds.
const minFunctionsStmts = 4

// varKey is the function-graph key ("v:…") of the variable an expression
// names, "" when it names none (a literal, a block).
func varKey(e expr) string {
	switch x := e.(type) {
	case refExpr:
		return "v:" + ir.NameKey(x.name)
	case accExpr:
		return "v:" + ir.NameKey(x.base)
	}
	return ""
}

// eachVar calls fn with the "v:" key of every variable e reads.
func eachVar(e expr, fn func(string)) {
	switch x := e.(type) {
	case refExpr, accExpr:
		fn(varKey(x))
	case notExpr:
		eachVar(x.inner, fn)
	case callExpr:
		for _, a := range x.reads() {
			eachVar(a, fn)
		}
	}
}

// instKey is an FB instance's identity in the function graph: the variable
// at the head of its accessor (Timers[2] is the Timers array).
func instKey(inst string) string {
	if i := strings.IndexAny(inst, ".["); i >= 0 {
		inst = inst[:i]
	}
	return ir.NameKey(inst)
}

// eachRef calls fn with the function-graph key of every wire ("w:…") and
// FB instance ("i:…") e reads.
func (nl *netlist) eachRef(e expr, fn func(string)) {
	switch x := e.(type) {
	case refExpr:
		if _, ok := nl.wires[x.name]; ok {
			fn("w:" + ir.NameKey(x.name))
		}
	case pinExpr:
		fn("i:" + ir.NameKey(x.inst))
	case accExpr:
		fn("i:" + ir.NameKey(x.base))
	case notExpr:
		nl.eachRef(x.inner, fn)
	case callExpr:
		for _, a := range x.reads() {
			nl.eachRef(a, fn)
		}
	}
}

// nestDepth is how many blocks deep an expression nests.
func nestDepth(e expr) int {
	switch x := e.(type) {
	case notExpr:
		return nestDepth(x.inner)
	case callExpr:
		d := 0
		for _, a := range x.reads() {
			if n := nestDepth(a); n > d {
				d = n
			}
		}
		return d + 1
	}
	return 0
}

func eachCall(e expr, fn func(callExpr)) {
	switch x := e.(type) {
	case notExpr:
		eachCall(x.inner, fn)
	case callExpr:
		fn(x)
		for _, a := range x.reads() {
			eachCall(a, fn)
		}
	}
}

// lintText renders an expression for a message, as it reads in the source.
func lintText(e expr) string {
	switch x := e.(type) {
	case litExpr:
		return x.text
	case notExpr:
		return "NOT " + lintText(x.inner)
	case callExpr:
		args := make([]string, len(x.args))
		for i, a := range x.args {
			args[i] = lintText(a)
			if i < len(x.names) && x.names[i] != "" {
				args[i] = x.names[i] + " := " + args[i]
			}
		}
		return x.fn + "(" + strings.Join(args, ", ") + ")"
	}
	return exprText(e)
}
