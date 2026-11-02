package uaserver

import (
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
// A send counts when it is a send statement on a NotifyChannel, or a call to a function of the
// same package that (transitively) contains one. A goroutine started under the lock does not hold
// it. The lock is MonitoredItemService.Mu: Mu reached through the receiver of a
// MonitoredItemService method, through a .MonitoredItemService selector, or through a variable
// assigned from one.
func TestNoNotifyChannelSendUnderMonitoredItemLock(t *testing.T) {
	for _, dir := range []string{".", filepath.Join("..", "..", "server")} {
		fset := token.NewFileSet()
		files := parseDir(t, fset, dir)
		for _, v := range notifySendsUnderLock(fset, files) {
			t.Errorf("%s: sends on NotifyChannel while holding MonitoredItemService.Mu", v)
		}
	}
}

// The check above passes trivially if it cannot see the pattern. It must flag the deleted
// ChangeNotification as it was, and leave the sweep's shape (copy out under the lock, send
// after) alone.
func TestLockGuardFlagsTheDeletedChangeNotification(t *testing.T) {
	const src = `package p

type MonitoredItemService struct{ Mu interface{ Lock(); Unlock() } }

func (s *MonitoredItemService) ChangeNotification(n int) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	notify(nil, nil)
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
	sub.NotifyChannel <- nil
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
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := notifySendsUnderLock(fset, []*ast.File{f})
	want := []string{"p.go:21:2 in direct", "p.go:8:2 in ChangeNotification"} // sorted as strings
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("flagged %q, want %q", got, want)
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
// through a same-package function that sends) made while MonitoredItemService.Mu is held.
func notifySendsUnderLock(fset *token.FileSet, files []*ast.File) []string {
	var funcs []*ast.FuncDecl
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				funcs = append(funcs, fd)
			}
		}
	}

	// Functions that send, to a fixed point over same-package calls by name.
	senders := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, fd := range funcs {
			if !senders[fd.Name.Name] && sends(fd.Body, senders) {
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
		visit(fd.Body, senders, func(at token.Pos) {
			for _, r := range held {
				if at > r[0] && at < r[1] {
					p := fset.Position(at)
					out = append(out, p.String()+" in "+fd.Name.Name)
					return
				}
			}
		})
	}
	sort.Strings(out)
	return out
}

// sends reports whether body sends on a NotifyChannel or calls a known sender, outside a go
// statement.
func sends(body ast.Node, senders map[string]bool) bool {
	found := false
	visit(body, senders, func(token.Pos) { found = true })
	return found
}

// visit calls at for each send on a NotifyChannel and each call to a sender in n, skipping go
// statements (their body runs without the caller's locks).
func visit(n ast.Node, senders map[string]bool, at func(token.Pos)) {
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.GoStmt:
			return false
		case *ast.SendStmt:
			if sel, ok := n.Chan.(*ast.SelectorExpr); ok && sel.Sel.Name == "NotifyChannel" {
				at(n.Pos())
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

// heldRanges are the spans of fd in which MonitoredItemService.Mu is held: from each Lock to the
// next Unlock of the same expression, or to the end of the function when the Unlock is deferred
// or missing.
func heldRanges(fd *ast.FuncDecl) [][2]token.Pos {
	isMI := miNames(fd)
	type ev struct {
		pos      token.Pos
		key      string
		lock     bool
		deferred bool
	}
	var evs []ev
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit, *ast.GoStmt:
			return false
		case *ast.DeferStmt:
			if key, op := muOp(n.Call, isMI); op == "Unlock" {
				evs = append(evs, ev{pos: n.Pos(), key: key, deferred: true})
			}
			return false
		case *ast.CallExpr:
			if key, op := muOp(n, isMI); op != "" {
				evs = append(evs, ev{pos: n.Pos(), key: key, lock: op == "Lock"})
			}
		}
		return true
	})
	sort.Slice(evs, func(i, j int) bool { return evs[i].pos < evs[j].pos })
	var out [][2]token.Pos
	for i, e := range evs {
		if !e.lock {
			continue
		}
		end := fd.Body.End()
		for _, u := range evs[i+1:] {
			if u.key == e.key && !u.lock {
				if !u.deferred {
					end = u.pos
				}
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

// miNames says which expressions in fd are the MonitoredItemService: its receiver when fd is a
// MonitoredItemService method, any .MonitoredItemService selector, and identifiers assigned
// from one.
func miNames(fd *ast.FuncDecl) func(ast.Expr) bool {
	names := map[string]bool{}
	if fd.Recv != nil && len(fd.Recv.List) == 1 && len(fd.Recv.List[0].Names) == 1 {
		typ := fd.Recv.List[0].Type
		if star, ok := typ.(*ast.StarExpr); ok {
			typ = star.X
		}
		if id, ok := typ.(*ast.Ident); ok && id.Name == "MonitoredItemService" {
			names[fd.Recv.List[0].Names[0].Name] = true
		}
	}
	isSel := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "MonitoredItemService"
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
			for i, r := range as.Rhs {
				if id, ok := as.Lhs[i].(*ast.Ident); ok && isSel(r) {
					names[id.Name] = true
				}
			}
		}
		return true
	})
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
