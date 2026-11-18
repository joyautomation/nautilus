package testmqtt

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
)

const mochiPath = "github.com/mochi-mqtt/server/v2"

// TestTestBrokersCloseThroughTestmqtt holds every test in the module to
// closing its mochi brokers through Close: no X.Close() (nor defer
// X.Close()) where X is a mochi *Server, from <mochi>.New or from a
// function of its package that returns one. Six tests in sparkplug/host
// bypassed the old per-package helper, and one of them hung CI (#810).
func TestTestBrokersCloseThroughTestmqtt(t *testing.T) {
	root := "../.." // the module root
	byDir := map[string][]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, "_test.go") {
			byDir[filepath.Dir(p)] = append(byDir[filepath.Dir(p)], p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var bad []string
	scanned := 0
	for _, files := range byDir {
		parsed := map[string]*ast.File{}
		ctors := map[string]bool{} // this package's funcs returning a mochi *Server
		for _, f := range files {
			file, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			alias := mochiAlias(file)
			if alias == "" {
				continue
			}
			parsed[f] = file
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Type.Results != nil {
					for _, r := range fn.Type.Results.List {
						if isServerType(r.Type, alias) {
							ctors[fn.Name.Name] = true
						}
					}
				}
			}
		}
		for _, file := range parsed {
			scanned++
			alias := mochiAlias(file)
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				brokers := map[string]bool{}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					as, ok := n.(*ast.AssignStmt)
					if !ok || len(as.Rhs) != 1 {
						return true
					}
					call, ok := as.Rhs[0].(*ast.CallExpr)
					if !ok || !isBrokerCall(call.Fun, alias, ctors) {
						return true
					}
					if id, ok := as.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
						brokers[id.Name] = true
					}
					return true
				})
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Close" {
						return true
					}
					if id, ok := sel.X.(*ast.Ident); ok && brokers[id.Name] {
						bad = append(bad, fmt.Sprintf("%s: %s.Close()", fset.Position(call.Pos()), id.Name))
					}
					return true
				})
			}
		}
	}
	if scanned < 5 {
		t.Fatalf("found only %d test files importing mochi: the walk lost the module", scanned)
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("%s closes a mochi test broker with its own Close, which can deadlock in v2.7.9: use testmqtt.Close", b)
	}
}

// mochiAlias is the name file uses for the mochi server package, or "".
func mochiAlias(file *ast.File) string {
	for _, imp := range file.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == mochiPath {
			if imp.Name != nil {
				return imp.Name.Name
			}
			return "mqtt" // the package's own name
		}
	}
	return ""
}

func isServerType(e ast.Expr, alias string) bool {
	star, ok := e.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == alias && sel.Sel.Name == "Server"
}

func isBrokerCall(fun ast.Expr, alias string, ctors map[string]bool) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return ctors[f.Name]
	case *ast.SelectorExpr:
		x, ok := f.X.(*ast.Ident)
		return ok && x.Name == alias && f.Sel.Name == "New"
	}
	return false
}

// TestCloseUnderClientChurn takes a broker down while thirty clean-session
// clients disconnect at the same moment, the shape of a test's cleanup.
// mochi's own Close deadlocks on this within a few hundred rounds under
// -race -cpu 2 or 4 (CI runs this test so, at 1000 rounds); the window is
// too narrow to hit without -race. Close must never hang.
func TestCloseUnderClientChurn(t *testing.T) {
	rounds := 100
	if raceEnabled {
		rounds = 1000
	}
	if testing.Short() {
		rounds = 20
	}
	for i := range rounds {
		srv := mqttsrv.New(&mqttsrv.Options{Logger: quiet()})
		if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
			t.Fatal(err)
		}
		tcp := listeners.NewTCP(listeners.Config{ID: "t", Address: "127.0.0.1:0"})
		if err := srv.AddListener(tcp); err != nil {
			t.Fatal(err)
		}
		go func() { _ = srv.Serve() }()
		// No cleanup: a hang must fail below, not hang again in cleanup.
		var clients []mqtt.Client
		for k := range 30 {
			c := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + tcp.Address()).
				SetClientID(fmt.Sprintf("churn-%d-%d", i, k)).
				SetCleanSession(true).SetAutoReconnect(false).SetConnectRetry(false))
			if tok := c.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
				t.Fatalf("round %d: client %d connect: %v", i, k, tok.Error())
			}
			clients = append(clients, c)
		}
		var wg sync.WaitGroup
		for _, c := range clients {
			wg.Add(1)
			go func(c mqtt.Client) { defer wg.Done(); c.Disconnect(0) }(c)
		}
		done := make(chan error, 1)
		go func() { done <- Close(srv) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			buf := make([]byte, 1<<20)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("round %d: closing the broker hung with clients disconnecting (the mochi Close deadlock)\n%s",
				i, strings.TrimSpace(string(buf)))
		}
		wg.Wait()
	}
}

// Close is idempotent: a second call returns at once instead of panicking.
func TestCloseTwice(t *testing.T) {
	srv := mqttsrv.New(&mqttsrv.Options{Logger: quiet()})
	tcp := listeners.NewTCP(listeners.Config{ID: "t", Address: "127.0.0.1:0"})
	if err := srv.AddListener(tcp); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	if err := Close(srv); err != nil {
		t.Fatal(err)
	}
	if err := Close(srv); err != nil {
		t.Fatal(err)
	}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
