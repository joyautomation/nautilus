package eip

import (
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/eip/logixserver"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// A broken flush (#608): the link cut at every write of one flush. Every
// write before the cut went out once and another client's value on it
// since stands; the write that broke and every one after it go out once
// more after the reconnect, in send order. A struct cut partway re-sends
// only the members the controller did not confirm.

// requeueSpec is testSpec plus two scalar commands, so a flush in name
// order is ASetCmd, SpeedCmd, TRSCmd (three members), ZCmd.
func requeueSpec() *logixserver.TagSurfaceSpec {
	s := testSpec()
	s.Symbols = append(s.Symbols,
		logixserver.SymbolSpec{Name: "ASetCmd", Datatype: "DINT"},
		logixserver.SymbolSpec{Name: "ZCmd", Datatype: "REAL"})
	s.Tags = append(s.Tags,
		logixserver.TagSpec{Path: "ASetCmd", Datatype: "DINT"},
		logixserver.TagSpec{Path: "ZCmd", Datatype: "REAL"})
	return s
}

func requeueManifest() Manifest {
	m := testManifest()
	m.Tags = append(m.Tags,
		TagBinding{Name: "ASetCmd", Device: "ASetCmd", Type: "DINT", Writable: true},
		TagBinding{Name: "ZCmd", Device: "ZCmd", Type: "REAL", Writable: true})
	return m
}

func startRequeueEmulator(t *testing.T) (addr string, store *logixserver.TagStore) {
	t.Helper()
	schema, tags, name, err := logixserver.CompileSurface(requeueSpec())
	if err != nil {
		t.Fatalf("compile surface: %v", err)
	}
	store = logixserver.NewTagStore()
	for _, tc := range tags {
		store.Set(tc.Path, tc.LeafType, tc.Default)
	}
	srv := logixserver.NewServer(store, schema, name, "127.0.0.1:0", nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("emulator listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Run(ctx) }()
	return srv.Addr(), store
}

// cutProxy forwards to the emulator and counts the write requests it
// passes, by the symbol each names: the commands are never polled (class
// none), so a request naming one is a write of it. On the first
// connection, the request naming cut is not forwarded: both sides are
// closed instead, after onCut has run.
type cutProxy struct {
	names []string // the symbols a write request is told apart by
	cut   string
	onCut func()

	mu     sync.Mutex
	writes map[string]int
	conns  atomic.Int32
}

func (p *cutProxy) count(name string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writes[name]
}

func (p *cutProxy) serve(t *testing.T, target string) int {
	t.Helper()
	p.writes = map[string]int{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			first := p.conns.Add(1) == 1
			go func() {
				buf := make([]byte, 64<<10)
				for {
					n, err := s.Read(buf)
					if err != nil {
						_ = c.Close()
						return
					}
					if _, err := c.Write(buf[:n]); err != nil {
						return
					}
				}
			}()
			go func() {
				buf := make([]byte, 64<<10)
				for {
					n, err := c.Read(buf)
					if err != nil {
						_ = s.Close()
						return
					}
					req := buf[:n]
					named := ""
					for _, name := range p.names {
						if bytes.Contains(req, []byte(name)) {
							named = name
							break
						}
					}
					if first && named != "" && named == p.cut {
						p.onCut()
						_ = c.Close()
						_ = s.Close()
						return
					}
					if named != "" {
						p.mu.Lock()
						p.writes[named]++
						p.mu.Unlock()
					}
					if _, err := s.Write(req); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestBrokenFlushAtEveryPoint(t *testing.T) {
	cmd := nio.Values{
		"ASetCmd":  int64(11),
		"SpeedCmd": 22.5,
		"TRSCmd": ir.Value{Kind: ir.TypeStruct, Fld: []ir.Value{
			{Kind: ir.TypeStruct, Fld: []ir.Value{ir.RealVal(9.75), ir.BoolVal(true)}},
			ir.IntVal(99),
		}},
		"ZCmd": 3.5,
	}
	// One write request per step, in send order (tag name, then member).
	type step struct {
		symbol  string // what its request names (see cutProxy)
		path    string // the controller's tag
		command any    // what this flush writes
		foreign any    // what another client writes over it after the cut
	}
	steps := []step{
		{"ASetCmd", "ASetCmd", 11.0, 4242.0},
		{"SpeedCmd", "SpeedCmd", 22.5, 42.0},
		{"Displacement", "TRSCmd.Header.Displacement", 9.75, -1.5},
		{"Valid", "TRSCmd.Header.Valid", true, false},
		{"Count", "TRSCmd.Count", 99.0, 4242.0},
		{"ZCmd", "ZCmd", 3.5, -3.5},
	}
	var symbols []string
	for _, st := range steps {
		symbols = append(symbols, st.symbol)
	}

	for cutAt, cut := range steps {
		t.Run("cut at "+strconv.Itoa(cutAt)+" "+cut.symbol, func(t *testing.T) {
			addr, store := startRequeueEmulator(t)
			p := &cutProxy{names: symbols, cut: cut.symbol, onCut: func() {
				for _, st := range steps[:cutAt] {
					store.UpdateValue(st.path, st.foreign)
				}
			}}
			port := p.serve(t, addr)
			d, err := New("127.0.0.1", requeueManifest(), WithPort(port), WithScanRate(20*time.Millisecond),
				WithTagClass(NoPoll, "ASetCmd", "SpeedCmd", "TRSCmd", "ZCmd"))
			if err != nil {
				t.Fatal(err)
			}
			// Queued before the first connection: one flush carries them all.
			if err := d.WriteOutputs(cmd); err != nil {
				t.Fatal(err)
			}
			d.Start(context.Background())
			t.Cleanup(d.Stop)

			// The flush after the reconnect sends in the same order, so once
			// every re-sent step has landed and the queue is empty, a re-send
			// of an earlier step would have been counted already.
			waitFor(t, "the re-sent steps land", func() bool {
				if p.conns.Load() < 2 {
					return false
				}
				d.wmu.Lock()
				queued := len(d.pending)
				d.wmu.Unlock()
				if queued != 0 {
					return false
				}
				for _, st := range steps[cutAt:] {
					if storeVal(store, st.path) != st.command {
						return false
					}
				}
				return true
			})
			for i, st := range steps {
				if got := p.count(st.symbol); got != 1 {
					t.Errorf("%s: %d write requests reached the controller, want 1", st.symbol, got)
				}
				if i < cutAt && storeVal(store, st.path) != st.foreign {
					t.Errorf("%s: confirmed before the cut, yet the other client's value was overwritten (%v)", st.symbol, storeVal(store, st.path))
				}
			}
		})
	}
}

// hookProxy forwards to the emulator and runs onRequest once, before it
// forwards the first request naming name (a write, when the tag is never
// polled), with the driver waiting on that request's answer.
type hookProxy struct {
	name      string
	onRequest func()
	once      sync.Once
}

func (p *hookProxy) serve(t *testing.T, target string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			go func() { _, _ = io.Copy(c, s); _ = c.Close() }()
			go func() {
				buf := make([]byte, 64<<10)
				for {
					n, err := c.Read(buf)
					if err != nil {
						_ = s.Close()
						return
					}
					if bytes.Contains(buf[:n], []byte(p.name)) {
						p.once.Do(p.onRequest)
					}
					if _, err := s.Write(buf[:n]); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// The term guard: a write in flight while the gate closes and reopens
// belongs to the previous term. The reopen forgot everything written; the
// write's confirmation must not record it again afterwards, or the push
// that answers the reopen skips the value as unchanged and another
// master's write since stands.
func TestWriteInFlightAcrossAReopenIsNotRecorded(t *testing.T) {
	addr, store := startRequeueEmulator(t)
	g := &gateWatch{}
	g.open.Store(true)
	var d *Driver
	p := &hookProxy{name: "SpeedCmd", onRequest: func() {
		// Leadership lost and regained while the write is on the wire,
		// each seen by a look.
		g.open.Store(false)
		d.gateOpen()
		g.open.Store(true)
		d.gateOpen()
	}}
	port := p.serve(t, addr)
	d, err := New("127.0.0.1", requeueManifest(), WithPort(port), WithScanRate(20*time.Millisecond),
		WithTagClass(NoPoll, "ASetCmd", "SpeedCmd", "TRSCmd", "ZCmd"))
	if err != nil {
		t.Fatal(err)
	}
	d.SetWriteGate(g.gate)
	if err := d.WriteOutputs(nio.Values{"SpeedCmd": 77.0}); err != nil {
		t.Fatal(err)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	waitFor(t, "the write in flight lands", func() bool { return storeVal(store, "SpeedCmd") == 77.0 })
	pollsPast(t, d, 2) // the flush that sent it has returned

	store.UpdateValue("SpeedCmd", 5.0) // another master
	if err := d.WriteOutputs(nio.Values{"SpeedCmd": 77.0}); err != nil {
		t.Fatal(err)
	}
	pollsPast(t, d, 2)
	if v := storeVal(store, "SpeedCmd"); v != 77.0 {
		t.Errorf("SpeedCmd = %v: the push after the reopen was skipped", v)
	}
}

// Last value wins across a broken flush: a tag the program commands again
// while the flush that broke on it is in flight is re-sent with the newer
// value, once, not with the one that broke.
func TestBrokenFlushKeepsANewerCommand(t *testing.T) {
	addr, store := startRequeueEmulator(t)
	var d *Driver
	p := &cutProxy{names: []string{"ASetCmd", "SpeedCmd"}, cut: "SpeedCmd", onCut: func() {
		_ = d.WriteOutputs(nio.Values{"SpeedCmd": 77.0})
	}}
	port := p.serve(t, addr)
	d, err := New("127.0.0.1", requeueManifest(), WithPort(port), WithScanRate(20*time.Millisecond),
		WithTagClass(NoPoll, "ASetCmd", "SpeedCmd", "TRSCmd", "ZCmd"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.WriteOutputs(nio.Values{"ASetCmd": int64(11), "SpeedCmd": 22.5}); err != nil {
		t.Fatal(err)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	waitFor(t, "the newer command lands", func() bool {
		return p.conns.Load() >= 2 && storeVal(store, "SpeedCmd") == 77.0
	})
	pollsPast(t, d, 2)
	if got := p.count("SpeedCmd"); got != 1 {
		t.Errorf("SpeedCmd: %d write requests reached the controller, want 1 (the newer value)", got)
	}
	if got := p.count("ASetCmd"); got != 1 {
		t.Errorf("ASetCmd: %d write requests reached the controller, want 1", got)
	}
}
