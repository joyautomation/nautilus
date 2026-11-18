package testmqtt

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
)

var _ listeners.Listener = (*TCP)(nil)

// TestTCPCountsAClientBeforeEstablish: the server's client WaitGroup counts
// a connection before establish runs and until it returns, so CloseAll's
// Wait waits for it. mochi's listener leaves the count to attachClient,
// inside establish: with it, the Wait below returns at once.
func TestTCPCountsAClientBeforeEstablish(t *testing.T) {
	srv := mqttsrv.New(&mqttsrv.Options{Logger: quiet()})
	l := NewTCP(srv, listeners.Config{ID: "t", Address: "127.0.0.1:0"})
	if err := l.Init(quiet()); err != nil {
		t.Fatal(err)
	}
	in, release := make(chan struct{}), make(chan struct{})
	go l.Serve(func(_ string, c net.Conn) error {
		close(in)
		<-release
		return c.Close()
	})
	t.Cleanup(func() { l.Close(func(string) {}) })
	conn, err := net.Dial("tcp", l.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-in:
	case <-time.After(5 * time.Second):
		t.Fatal("establish never ran")
	}
	waited := make(chan struct{})
	go func() { srv.Listeners.ClientsWg.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("ClientsWg.Wait returned while establish was still running: the listener does not count its clients")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("ClientsWg.Wait still blocked after establish returned: the listener leaked a count")
	}
}

// TestTCPDropsAConnectionAcceptedAfterClose: once Close has run, an
// accepted connection is closed, never established (no Add after Close).
func TestTCPDropsAConnectionAcceptedAfterClose(t *testing.T) {
	srv := mqttsrv.New(&mqttsrv.Options{Logger: quiet()})
	l := NewTCP(srv, listeners.Config{ID: "t", Address: "127.0.0.1:0"})
	if err := l.Init(quiet()); err != nil {
		t.Fatal(err)
	}
	// Close before Serve, but keep the socket open so Accept still returns
	// a connection: the closed flag, not the socket, must stop it.
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	conn, err := net.Dial("tcp", l.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	established := make(chan struct{}, 1)
	served := make(chan struct{})
	go func() {
		l.Serve(func(string, net.Conn) error { established <- struct{}{}; return nil })
		close(served)
	}()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve kept accepting after Close")
	}
	select {
	case <-established:
		t.Fatal("a connection accepted after Close was established")
	default:
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("the dropped connection was not closed")
	}
	l.mu.Lock()
	ln := l.listen
	l.mu.Unlock()
	_ = ln.Close()
}

// TestCloseDoesNotRaceAnAttachingClient reproduces #909 deterministically:
// a client is inside attachClient (mochi has run its ClientsWg.Add, from
// zero) when the broker closes, and CloseAll's Wait finds it counted. With
// mochi's listeners.NewTCP nothing orders that Add before the Wait, and
// under -race this test fails with "DATA RACE" between
// listeners.(*Listeners).CloseAll and Server.attachClient, as go-race did
// on CI. With NewTCP the listener's own Add, under the lock Close takes,
// comes first. The waits poll goroutine stacks, which creates no
// happens-before edge the race detector would see.
func TestCloseDoesNotRaceAnAttachingClient(t *testing.T) {
	srv := mqttsrv.New(&mqttsrv.Options{Logger: quiet()})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	tcp := NewTCP(srv, listeners.Config{ID: "t", Address: "127.0.0.1:0"})
	if err := srv.AddListener(tcp); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	// A raw connection that never sends CONNECT parks its goroutine in
	// readConnectionPacket, after attachClient's Add.
	conn, err := net.Dial("tcp", tcp.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitForStack(t, "mochi-mqtt/server/v2.(*Server).readConnectionPacket")
	done := make(chan error, 1)
	go func() { done <- Close(srv) }()
	// CloseAll is now parked in Wait, for that client.
	waitForStack(t, "listeners.(*Listeners).CloseAll", "sync.(*WaitGroup).Wait")
	_ = conn.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close hung")
	}
}

// TestCloseWaitsForAConnectionNotYetEstablished: Close cannot slip in
// between the accept loop's check of the closed flag and its count of the
// connection. The loop is held just before it spawns establish; Close must
// wait for that connection, so the count was taken under the lock.
func TestCloseWaitsForAConnectionNotYetEstablished(t *testing.T) {
	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	testHookBeforeSpawn = func() { once.Do(func() { close(paused); <-resume }) }
	t.Cleanup(func() { testHookBeforeSpawn = nil })
	srv := mqttsrv.New(&mqttsrv.Options{Logger: quiet()})
	tcp := NewTCP(srv, listeners.Config{ID: "t", Address: "127.0.0.1:0"})
	if err := srv.AddListener(tcp); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	conn, err := net.Dial("tcp", tcp.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-paused:
	case <-time.After(5 * time.Second):
		t.Fatal("the accept loop never reached the spawn")
	}
	done := make(chan error, 1)
	go func() { done <- Close(srv) }()
	select {
	case <-done:
		close(resume)
		t.Fatal("Close returned while an accepted connection was still uncounted")
	case <-time.After(200 * time.Millisecond):
	}
	close(resume)
	waitForStack(t, "mochi-mqtt/server/v2.(*Server).readConnectionPacket")
	_ = conn.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close hung after the connection ended")
	}
}

// waitForStack waits until one goroutine's stack holds every frame.
func waitForStack(t *testing.T, frames ...string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	buf := make([]byte, 8<<20)
	for time.Now().Before(deadline) {
		n := runtime.Stack(buf, true)
		if n == len(buf) {
			t.Fatal("goroutine dump truncated")
		}
	goroutines:
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			for _, f := range frames {
				if !strings.Contains(g, f) {
					continue goroutines
				}
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no goroutine reached %s", strings.Join(frames, " + "))
}

// TestTCPSweepsClientsOnlyOnFirstClose: like mochi's listener, Close runs
// the server's client sweep once. testmqtt.Close closes every listener
// with a no-op sweep first, so srv.Close's own sweep (which can deadlock)
// must not run on the second Close.
func TestTCPSweepsClientsOnlyOnFirstClose(t *testing.T) {
	srv := mqttsrv.New(&mqttsrv.Options{Logger: quiet()})
	l := NewTCP(srv, listeners.Config{ID: "t", Address: "127.0.0.1:0"})
	if err := l.Init(quiet()); err != nil {
		t.Fatal(err)
	}
	sweeps := 0
	l.Close(func(string) { sweeps++ })
	l.Close(func(string) { sweeps++ })
	if sweeps != 1 {
		t.Fatalf("Close ran the client sweep %d times, want 1", sweeps)
	}
}

const listenersPath = "github.com/mochi-mqtt/server/v2/listeners"

// TestTestBrokersListenThroughTestmqtt holds every test in the module to
// building its mochi broker listeners with NewTCP: no call to the
// listeners package's NewTCP in a _test.go file, nor NewNet, NewUnixSock
// or NewWebsocket: each of those listeners hands a connection to establish
// on a goroutine of its own, so each races CloseAll (#909).
func TestTestBrokersListenThroughTestmqtt(t *testing.T) {
	forbidden := map[string]bool{"NewTCP": true, "NewNet": true, "NewUnixSock": true, "NewWebsocket": true}
	fset := token.NewFileSet()
	var bad []string
	scanned := 0
	err := filepath.WalkDir("../..", func(p string, d fs.DirEntry, err error) error { // the module root
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		alias := ""
		for _, imp := range file.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == listenersPath {
				alias = "listeners"
				if imp.Name != nil {
					alias = imp.Name.Name
				}
			}
		}
		if alias == "" {
			return nil
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || !forbidden[sel.Sel.Name] {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == alias {
				bad = append(bad, fmt.Sprintf("%s: %s.%s", fset.Position(sel.Pos()), alias, sel.Sel.Name))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 5 {
		t.Fatalf("found only %d test files importing mochi's listeners: the walk lost the module", scanned)
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("%s builds a test broker listener that races CloseAll in mochi v2.7.9: use testmqtt.NewTCP", b)
	}
}
