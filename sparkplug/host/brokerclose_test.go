package host

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// mochi-mqtt v2.7.9's Server.Close can deadlock (closeBroker's comment has
// the mechanism): its client sweep read-locks the client map, then read-locks
// it again through Len, so a client finishing its disconnect at that moment
// (a queued Clients.Delete) blocks both forever. CI hung ten minutes on it in
// TestEdgeToHostStartKeepsSetpoints, one of six tests that closed their broker
// with srv.Close instead of closeBroker.

// TestTestBrokersCloseThroughCloseBroker holds every test file in this
// package to closing its brokers through closeBroker: no srv.Close() (nor
// defer srv.Close()) on a broker from startBroker or mqttsrv.New.
func TestTestBrokersCloseThroughCloseBroker(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test files found: %v", err)
	}
	fset := token.NewFileSet()
	var bad []string
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "closeBrokerNow" {
				continue // closeBrokerNow is the one place srv.Close belongs
			}
			brokers := map[string]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok || len(as.Rhs) != 1 {
					return true
				}
				call, ok := as.Rhs[0].(*ast.CallExpr)
				if !ok || !isBrokerConstructor(call.Fun) {
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
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("%s closes a test broker with mochi's Close, which can deadlock: "+
			"startBroker already closes it through closeBroker (call closeBroker to take it down mid-test)", b)
	}
}

func isBrokerConstructor(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name == "startBroker" || f.Name == "newBroker"
	case *ast.SelectorExpr:
		x, ok := f.X.(*ast.Ident)
		return ok && x.Name == "mqttsrv" && f.Sel.Name == "New"
	}
	return false
}

// TestCloseBrokerUnderClientChurn takes a broker down while thirty
// clean-session clients disconnect at the same moment, the shape of a test's
// cleanup. mochi's own Close deadlocks on this within a few hundred rounds
// under -race -cpu 2 or 4 (CI runs this test so, at 1000 rounds); the window
// is too narrow to hit without -race. closeBroker must never hang.
func TestCloseBrokerUnderClientChurn(t *testing.T) {
	rounds := 100
	if raceEnabled {
		rounds = 1000
	}
	if testing.Short() {
		rounds = 20
	}
	for i := range rounds {
		srv, addr := newBroker(t, "") // no cleanup: a hang must fail here, not hang there
		var clients []mqtt.Client
		for k := 0; k < 30; k++ {
			o := mqtt.NewClientOptions().AddBroker(brokerURL(addr)).
				SetClientID(fmt.Sprintf("churn-%d-%d", i, k)).
				SetCleanSession(true).SetAutoReconnect(false).SetConnectRetry(false)
			c := mqtt.NewClient(o)
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
		go func() { done <- closeBroker(srv) }()
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
