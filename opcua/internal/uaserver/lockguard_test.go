package uaserver

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The publish loop takes publishMu and then MonitoredItemService.Mu (dropUnreported) before it
// drains NotifyChannel again. Code that sends on NotifyChannel while holding Mu therefore waits
// for a loop that waits for it, once the channel is full (design: opcua-server-writes §3.6).
// gopcua's ChangeNotification did exactly that and was deleted; this keeps it from coming back,
// in this package and in opcua/server, which drives the same lock and channel.
//
// A send counts when it is a send statement on a NotifyChannel (or on a variable assigned from
// one), or a call to a function of either package that (transitively) contains one. A goroutine
// started under the lock does not hold it. The lock is MonitoredItemService.Mu: Mu reached
// through the receiver of a MonitoredItemService method, a parameter of that type, a
// .MonitoredItemService selector, or a variable assigned from one. An Unlock in a block that
// ends by returning is an early exit and does not end the held span after it.
//
// It matches by name, not by type: a guard against the shapes this code is written in, not a
// proof.
func TestNoNotifyChannelSendUnderMonitoredItemLock(t *testing.T) {
	fset := token.NewFileSet()
	var files []*ast.File
	for _, dir := range []string{".", filepath.Join("..", "..", "server")} {
		files = append(files, parseDir(t, fset, dir)...)
	}
	for _, v := range notifySendsUnderLock(fset, files) {
		t.Errorf("%s: sends on NotifyChannel while holding MonitoredItemService.Mu", v)
	}
}

// The check above passes trivially if it cannot see the pattern. Each line marked "want" must
// be flagged and no other: the deleted ChangeNotification as it was, the shapes a review found
// the first version missed, and the sweep's shape (copy out under the lock, send after) left
// alone.
func TestLockGuardFlagsTheDeletedChangeNotification(t *testing.T) {
	const a = `package p

type MonitoredItemService struct{ Mu interface{ Lock(); Unlock() } }

func (s *MonitoredItemService) ChangeNotification(n int) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	notify(nil, nil) // want
}

func notify(sub *Subscription, val any) {
	select {
	case sub.NotifyChannel <- val:
	case <-sub.done:
	}
}

func direct(srv *Server) {
	mi := srv.MonitoredItemService
	mi.Mu.Lock()
	sub.NotifyChannel <- nil // want
	mi.Mu.Unlock()
}

func sweepShape(srv *Server) {
	mi := srv.MonitoredItemService
	mi.Mu.Lock()
	copied := 1
	mi.Mu.Unlock()
	sub.NotifyChannel <- copied
}

func goroutine(srv *Server) {
	srv.MonitoredItemService.Mu.Lock()
	defer srv.MonitoredItemService.Mu.Unlock()
	go notify(nil, nil)
}

func otherLock(sub *Subscription) {
	sub.Mu.Lock()
	sub.NotifyChannel <- nil
	sub.Mu.Unlock()
}

func earlyReturn(srv *Server, x bool) {
	mi := srv.MonitoredItemService
	mi.Mu.Lock()
	if x {
		mi.Mu.Unlock()
		return
	}
	sub.NotifyChannel <- nil // want
	mi.Mu.Unlock()
}

func alias(srv *Server) {
	mi := srv.MonitoredItemService
	ch := sub.NotifyChannel
	mi.Mu.Lock()
	ch <- nil // want
	mi.Mu.Unlock()
}

func lockInLiteral(srv *Server) {
	f := func() {
		srv.MonitoredItemService.Mu.Lock()
		defer srv.MonitoredItemService.Mu.Unlock()
		sub.NotifyChannel <- nil // want
	}
	f()
	sub.NotifyChannel <- nil
}

func typedParam(mi *MonitoredItemService) {
	mi.Mu.Lock()
	sub.NotifyChannel <- nil // want
	mi.Mu.Unlock()
}

func varDecl(srv *Server) {
	var mi = srv.MonitoredItemService
	mi.Mu.Lock()
	sub.NotifyChannel <- nil // want
	mi.Mu.Unlock()
}

func (s *Server) Push(item *MonitoredItem) {
	item.Sub.NotifyChannel <- nil
}
`
	// Another package calling a sender of the first under the lock: the old
	// Server.ChangeNotification called from outside.
	const b = `package q

func crossPackage(srv *opcserver.Server) {
	mi := srv.MonitoredItemService
	mi.Mu.Lock()
	srv.Push(nil) // want
	mi.Mu.Unlock()
}

func typedParamOtherPackage(mi *opcserver.MonitoredItemService) {
	mi.Mu.Lock()
	defer mi.Mu.Unlock()
	sub.NotifyChannel <- nil // want
}
`
	fset := token.NewFileSet()
	var files []*ast.File
	var want []string
	for _, src := range []struct{ name, text string }{{"a.go", a}, {"b.go", b}} {
		f, err := parser.ParseFile(fset, src.name, src.text, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
		for i, line := range strings.Split(src.text, "\n") {
			if strings.HasSuffix(line, "// want") {
				want = append(want, fmt.Sprintf("%s:%d", src.name, i+1))
			}
		}
	}
	var got []string
	for _, v := range notifySendsUnderLock(fset, files) {
		got = append(got, strings.Join(strings.Split(v, ":")[:2], ":"))
	}
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("flagged %v\nwant    %v", got, want)
	}
}

func parseDir(t *testing.T, fset *token.FileSet, dir string) []*ast.File {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatalf("no Go files in %s", dir)
	}
	return files
}

// notifySendsUnderLock returns "file:line:col in Func" for every NotifyChannel send (direct, or
// through a function of files that sends) made while MonitoredItemService.Mu is held.
func notifySendsUnderLock(fset *token.FileSet, files []*ast.File) []string {
	var funcs []*ast.FuncDecl
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				funcs = append(funcs, fd)
			}
		}
	}
	aliases := map[*ast.FuncDecl]map[string]bool{}
	for _, fd := range funcs {
		aliases[fd] = chanAliases(fd)
	}

	// Functions that send, to a fixed point over calls by name, across all of files.
	senders := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, fd := range funcs {
			if senders[fd.Name.Name] {
				continue
			}
			found := false
			visit(fd.Body, senders, aliases[fd], func(token.Pos) { found = true })
			if found {
				senders[fd.Name.Name] = true
				changed = true
			}
		}
	}

	var out []string
	for _, fd := range funcs {
		held := heldRanges(fd)
		if len(held) == 0 {
			continue
		}
		visit(fd.Body, senders, aliases[fd], func(at token.Pos) {
			for _, r := range held {
				if at > r[0] && at < r[1] {
					out = append(out, fset.Position(at).String()+" in "+fd.Name.Name)
					return
				}
			}
		})
	}
	sort.Strings(out)
	return out
}

// visit calls at for each send on a NotifyChannel (or an alias of one) and each call to a
// sender in n, skipping go statements (their body runs without the caller's locks).
func visit(n ast.Node, senders, aliases map[string]bool, at func(token.Pos)) {
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.GoStmt:
			return false
		case *ast.SendStmt:
			switch ch := n.Chan.(type) {
			case *ast.SelectorExpr:
				if ch.Sel.Name == "NotifyChannel" {
					at(n.Pos())
				}
			case *ast.Ident:
				if aliases[ch.Name] {
					at(n.Pos())
				}
			}
		case *ast.CallExpr:
			if name := calleeName(n.Fun); name != "" && senders[name] {
				at(n.Pos())
			}
		}
		return true
	})
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// chanAliases are the identifiers fd assigns from a .NotifyChannel selector.
func chanAliases(fd *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	assigned(fd.Body, func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "NotifyChannel"
	}, out)
	return out
}

// assigned adds to names each identifier n assigns (:=, = or var) from an expression match
// accepts.
func assigned(n ast.Node, match func(ast.Expr) bool, names map[string]bool) {
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) == len(n.Rhs) {
				for i, r := range n.Rhs {
					if id, ok := n.Lhs[i].(*ast.Ident); ok && match(r) {
						names[id.Name] = true
					}
				}
			}
		case *ast.ValueSpec:
			if len(n.Names) == len(n.Values) {
				for i, v := range n.Values {
					if match(v) {
						names[n.Names[i].Name] = true
					}
				}
			}
		}
		return true
	})
}

// heldRanges are the spans of fd in which MonitoredItemService.Mu is held: from each Lock to
// the next Unlock of the same expression in the same function (fd or a function literal in
// it), or to that function's end when the Unlock is deferred or missing. An Unlock in a nested
// block that ends in return, break, continue or goto is an early exit and ends nothing.
func heldRanges(fd *ast.FuncDecl) [][2]token.Pos {
	isMI := miNames(fd)
	type ev struct {
		pos, scopeEnd token.Pos
		key           string
		lock, skip    bool
	}
	var evs []ev
	var stack []ast.Node
	// scope is the innermost function body on the stack; block the innermost block.
	scope := func() *ast.BlockStmt {
		for i := len(stack) - 1; i >= 0; i-- {
			if fl, ok := stack[i].(*ast.FuncLit); ok {
				return fl.Body
			}
		}
		return fd.Body
	}
	block := func() *ast.BlockStmt {
		for i := len(stack) - 1; i >= 0; i-- {
			if b, ok := stack[i].(*ast.BlockStmt); ok {
				return b
			}
		}
		return fd.Body
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		switch n := n.(type) {
		case *ast.GoStmt:
			return false
		case *ast.DeferStmt:
			if key, op := muOp(n.Call, isMI); op == "Unlock" {
				// Deferred: the span runs to the end of the function; an event that ends nothing.
				evs = append(evs, ev{pos: n.Pos(), scopeEnd: scope().End(), key: key, skip: true})
			}
			return false
		case *ast.CallExpr:
			if key, op := muOp(n, isMI); op != "" {
				e := ev{pos: n.Pos(), scopeEnd: scope().End(), key: key, lock: op == "Lock"}
				if b := block(); !e.lock && b != scope() && len(b.List) > 0 {
					switch b.List[len(b.List)-1].(type) {
					case *ast.ReturnStmt, *ast.BranchStmt:
						e.skip = true
					}
				}
				evs = append(evs, e)
			}
		}
		stack = append(stack, n)
		return true
	})
	sort.Slice(evs, func(i, j int) bool { return evs[i].pos < evs[j].pos })
	var out [][2]token.Pos
	for i, e := range evs {
		if !e.lock {
			continue
		}
		end := e.scopeEnd
		for _, u := range evs[i+1:] {
			if u.key == e.key && !u.lock && !u.skip && u.pos < e.scopeEnd {
				end = u.pos
				break
			}
		}
		out = append(out, [2]token.Pos{e.pos, end})
	}
	return out
}

// muOp recognises X.Mu.Lock() and X.Mu.Unlock() where X is the MonitoredItemService, returning
// X's source text as the key.
func muOp(call *ast.CallExpr, isMI func(ast.Expr) bool) (string, string) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Lock" && sel.Sel.Name != "Unlock") {
		return "", ""
	}
	mu, ok := sel.X.(*ast.SelectorExpr)
	if !ok || mu.Sel.Name != "Mu" || !isMI(mu.X) {
		return "", ""
	}
	return exprText(mu.X), sel.Sel.Name
}

// miNames says which expressions in fd are the MonitoredItemService: its receiver or a
// parameter of that type (in fd or a function literal in it), any .MonitoredItemService
// selector, and identifiers assigned from one.
func miNames(fd *ast.FuncDecl) func(ast.Expr) bool {
	names := map[string]bool{}
	isMIType := func(typ ast.Expr) bool {
		if star, ok := typ.(*ast.StarExpr); ok {
			typ = star.X
		}
		switch t := typ.(type) {
		case *ast.Ident:
			return t.Name == "MonitoredItemService"
		case *ast.SelectorExpr:
			return t.Sel.Name == "MonitoredItemService"
		}
		return false
	}
	addFields := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			if isMIType(f.Type) {
				for _, n := range f.Names {
					names[n.Name] = true
				}
			}
		}
	}
	addFields(fd.Recv)
	addFields(fd.Type.Params)
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if fl, ok := n.(*ast.FuncLit); ok {
			addFields(fl.Type.Params)
		}
		return true
	})
	isSel := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "MonitoredItemService"
	}
	assigned(fd.Body, isSel, names)
	return func(e ast.Expr) bool {
		if id, ok := e.(*ast.Ident); ok {
			return names[id.Name]
		}
		return isSel(e)
	}
}

func exprText(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprText(e.X) + "." + e.Sel.Name
	}
	return "?"
}
