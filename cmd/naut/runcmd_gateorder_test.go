package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// On a pair, runProject wires the elector's write and send gates into the
// driver (electorWith -> wireRedundancy) BEFORE it starts the driver
// (startDriverWhenDecided). A driver whose first session is dialled before
// its send gate is installed has no socket it can reset (opcua dials fenced
// only when paired at dial time), so a standby's first session could not be
// fenced. runProject is too big to run with a recording driver, so this pins
// the order in its source: swap the two steps and it goes red.
func TestRunProjectWiresTheGatesBeforeItStartsTheDriver(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "runcmd.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	firstCall := func(fn string, names ...string) token.Pos {
		var pos token.Pos
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name.Name != fn {
				continue
			}
			ast.Inspect(fd, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := c.Fun.(*ast.Ident); ok {
					for _, nm := range names {
						if id.Name == nm && (pos == 0 || c.Pos() < pos) {
							pos = c.Pos()
						}
					}
				}
				return true
			})
		}
		return pos
	}
	wire := firstCall("runProject", "electorWith", "electorFor")
	start := firstCall("runProject", "startDriverWhenDecided")
	if wire == 0 || start == 0 {
		t.Fatalf("runProject no longer calls electorWith (%v) and startDriverWhenDecided (%v) directly: update this test to follow the code", wire != 0, start != 0)
	}
	if wire > start {
		t.Fatalf("runProject starts the driver (%s) before it wires the pair's gates (%s)", fset.Position(start), fset.Position(wire))
	}
	// electorWith wires on every path that returns an elector.
	if firstCall("electorWith", "wireRedundancy") == 0 {
		t.Fatal("electorWith no longer calls wireRedundancy")
	}
}
