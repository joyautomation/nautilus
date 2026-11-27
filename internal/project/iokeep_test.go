//go:build !js

package project

// Keeping a live I/O apply across a restart and a takeover (#706 PR 5),
// against real Modbus slaves with the scan running: re-applied before the
// first scan, the base-hash rule, a standby that follows the store so its
// takeover re-applies nothing, durable answers, and a pending revert that
// is not carried.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/slave"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
)

// memStore is a retain store in memory, encoded as a real one would be.
type memStore struct {
	mu       sync.Mutex
	enc      []byte
	failSave error
	failLoad error
}

func (s *memStore) Load() (retain.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failLoad != nil {
		return retain.State{}, s.failLoad
	}
	var st retain.State
	if s.enc == nil {
		return st, nil
	}
	err := json.Unmarshal(s.enc, &st)
	return st, err
}

func (s *memStore) Save(st retain.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failSave != nil {
		return s.failSave
	}
	b, err := json.Marshal(st)
	s.enc = b
	return err
}

func (s *memStore) Kind() string { return "memory" }

func (s *memStore) io(t *testing.T) *retain.IOState {
	t.Helper()
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	return st.IO
}

func (s *memStore) edit(t *testing.T, fn func(st *retain.State)) {
	t.Helper()
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	fn(&st)
	b, _ := json.Marshal(st)
	s.mu.Lock()
	s.enc = b
	s.mu.Unlock()
}

// keepCoord is a Termed coordinator a test flips.
type keepCoord struct {
	lead atomic.Bool
	term atomic.Uint64
}

func (c *keepCoord) IsLeader() bool { return c.lead.Load() }
func (c *keepCoord) Term() uint64   { return c.term.Load() }

// countingKeeper is an IOConfig whose re-applies a test counts, by whether
// the replica led when asked.
type countingKeeper struct {
	*IOConfig
	coord          *keepCoord
	leading, stood atomic.Int32
}

func (k *countingKeeper) RestoreIO(ctx context.Context, st *retain.IOState) error {
	if k.coord == nil || k.coord.IsLeader() {
		k.leading.Add(1)
	} else {
		k.stood.Add(1)
	}
	return k.IOConfig.RestoreIO(ctx, st)
}

type keepRig struct {
	*ioRig
	keeper *countingKeeper
	scans  atomic.Int64
	// firstDiffers is how many units differed from the manifest when the
	// first scan ran (-1 before it).
	firstDiffers atomic.Int32
	stop         func()
}

type keepOpts struct {
	store     retain.Store // nil: no retain: block
	coord     *keepCoord   // nil: standalone
	noFollow  bool
	redundant bool // the manifest has redundancy: (implied by coord), a pair's
	leaseMode bool // redundancy: without peers:, the Kubernetes Lease (#1013)
	prep      func(*IOConfig)
	hold      bool // Options.HoldOutputsOnSwap
}

// startKeepRig is startIORig wired as naut run wires a controller: a retain
// store, the IOConfig registered with the runtime before its first scan,
// Keep = SaveRetained, and with a coordinator the drivers' write gate and a
// standby following the store.
// rigRedundancy is the rig's redundancy: section: a pair's (peers:), or
// with leaseMode the Kubernetes Lease's (no peers:).
func rigRedundancy(o keepOpts) *RedundancyConfig {
	if o.leaseMode {
		return &RedundancyConfig{}
	}
	return &RedundancyConfig{Peers: map[string]string{"a": "127.0.0.1:7001", "b": "127.0.0.1:7002"}}
}

func startKeepRig(t *testing.T, files fstest.MapFS, o keepOpts) *keepRig {
	t.Helper()
	p, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	if o.store != nil {
		p.Runtime.Retain = o.store
	}
	p.Runtime.HoldOutputsOnSwap = o.hold
	var gate func() bool
	if o.coord != nil {
		p.Runtime.Coordinator = o.coord
		p.Redundancy = rigRedundancy(o)
		gate = o.coord.IsLeader
		if g, ok := p.Runtime.Driver.(interface{ SetWriteGate(func() bool) }); ok {
			g.SetWriteGate(o.coord.IsLeader)
		}
	} else if o.redundant || o.leaseMode {
		p.Redundancy = rigRedundancy(o)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	io, err := NewIOConfig(p, rt, files, "")
	if err != nil {
		t.Fatal(err)
	}
	io.Gate = gate
	if o.store != nil {
		io.Keep = rt.SaveRetained
		io.KeepStore = o.store.Kind()
	}
	if o.prep != nil {
		o.prep(io)
	}
	r := &keepRig{keeper: &countingKeeper{IOConfig: io, coord: o.coord}}
	r.firstDiffers.Store(-1)
	rt.SetIO(r.keeper)
	rt.OnScan(func(*runtime.Tags) {
		// Recorded before the count, so a waiter on the count reads it.
		r.firstDiffers.CompareAndSwap(-1, int32(len(io.Differs())))
		r.scans.Add(1)
	})
	ctx, cancel := context.WithCancel(context.Background())
	if s, ok := p.Runtime.Driver.(interface{ Start(context.Context) }); ok {
		s.Start(ctx)
	}
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	if o.coord != nil && o.store != nil && !o.noFollow {
		go io.Follow(ctx, o.store.Load, o.coord.IsLeader)
	}
	var once sync.Once
	r.stop = func() {
		once.Do(func() {
			io.Close()
			cancel()
			<-done
			if s, ok := p.Runtime.Driver.(interface{ Stop() }); ok {
				s.Stop()
			}
		})
	}
	t.Cleanup(r.stop)
	r.ioRig = &ioRig{p: p, rt: rt, io: io, ctx: ctx}
	return r
}

// staticFiles is ioFiles validated statically, so a candidate pointing at
// nothing passes validation, as a device that answered its probe and
// dropped after would.
func staticFiles(portA, portB int) fstest.MapFS {
	base := ioFiles(portA, portB, "")
	return withFile(base, "nautilus.yaml", strings.Replace(string(base["nautilus.yaml"].Data), "  scan-rate: 10ms\n", "  scan-rate: 10ms\n  validate: static\n", 1))
}

func shortFollow(t *testing.T) {
	saved := followInterval
	followInterval = 50 * time.Millisecond
	t.Cleanup(func() { followInterval = saved })
}

// applyMove stages files with DEV_A moved to portA (and DEV_B to portB),
// validates and applies it.
func (r *keepRig) applyMove(t *testing.T, files fstest.MapFS, portA, portB int, opts ApplyOptions) *IOResult {
	t.Helper()
	c := r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(portA, portB, "", "")), "upload", "tester")
	if v := r.io.Validate(c); !v.Valid {
		t.Fatalf("validate: %v %+v", failed(v), v.Checks)
	}
	res, err := r.io.ApplyWith(r.ctx, c, "tester", opts)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// leading waits until the rig's runtime leads (its first scan has taken
// over), so the store takes its saves.
func (r *keepRig) leading(t *testing.T) {
	t.Helper()
	eventually(t, "the first scan", func() bool { return r.scans.Load() > 0 })
}

func unitOf(d []IOUnitState, key string) (IOUnitState, bool) {
	for _, u := range d {
		if unitKey(u.Driver, u.Device) == key {
			return u, true
		}
	}
	return IOUnitState{}, false
}

// A device moved live comes back after a restart, from where it was moved
// to, before the first scan: the old address is never polled into the
// logic nor written. Its origin comes back with it.
func TestIOKeptAcrossARestart(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	store := &memStore{}
	r := startKeepRig(t, files, keepOpts{store: store})
	r.leading(t)
	eventually(t, "DEV_A polled", func() bool { return r.value("A_In") == "4" })
	res := r.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{})
	if !res.Kept || res.KeptWhy != "" {
		t.Fatalf("kept %v (%s), want kept on answer", res.Kept, res.KeptWhy)
	}
	st := store.io(t)
	if st == nil || len(st.Units) != 1 || st.Units["modbus/DEV_A"].From != "upload" || st.Units["modbus/DEV_A"].By != "tester" {
		t.Fatalf("store io = %+v, want DEV_A kept from upload by tester", st)
	}
	if !strings.Contains(st.Files["modbus.yaml"], fmt.Sprintf("port: %d", slavePort(s3))) || len(st.Files) != 1 {
		t.Fatalf("store files = %v, want modbus.yaml with DEV_A's new port, alone", st.Files)
	}
	before, _ := unitOf(r.io.Differs(), "modbus/DEV_A")
	r.stop()

	s1.Unit(1).SetHolding(10, 999) // a write to the old device would change it
	r2 := startKeepRig(t, files, keepOpts{store: store})
	r2.leading(t)
	if n := r2.firstDiffers.Load(); n != 1 {
		t.Fatalf("%d units differed at the first scan, want DEV_A re-applied before it", n)
	}
	eventually(t, "A_In from the new device", func() bool { return r2.value("A_In") == "9" })
	s1.Unit(1).SetHolding(0, 5)
	s3.Unit(1).SetHolding(0, 11)
	eventually(t, "A_Cmd to the new device", func() bool { return s3.Unit(1).Holding(10) == 12 })
	time.Sleep(100 * time.Millisecond)
	if got := s1.Unit(1).Holding(10); got != 999 {
		t.Fatalf("the old device was written after the restart: Cmd %d", got)
	}
	after, ok := unitOf(r2.io.Differs(), "modbus/DEV_A")
	if !ok || after.AppliedFrom != "upload" || after.By != "tester" || !after.At.Equal(before.At) || after.RunningHash != before.RunningHash {
		t.Fatalf("after the restart DEV_A = %+v, want %+v", after, before)
	}
	if d := r2.io.Discarded(); len(d) != 0 {
		t.Fatalf("discarded = %+v, want none", d)
	}
}

// keptThenRestart applies DEV_A → s3 on one controller, stops it, and
// returns the store and the slaves.
func keptThenRestart(t *testing.T) (store *memStore, s1, s2, s3 *slave.Server, files fstest.MapFS) {
	t.Helper()
	s1, s2, s3 = newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files = ioFiles(slavePort(s1), slavePort(s2), "")
	store = &memStore{}
	r := startKeepRig(t, files, keepOpts{store: store})
	r.leading(t)
	if res := r.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{}); !res.Kept {
		t.Fatalf("not kept: %s", res.KeptWhy)
	}
	r.stop()
	return
}

// The base-hash rule, per unit: a deploy that moved the kept device wins
// (dropped, reported, kept for recovery); one that moved another device
// does not touch it; one that deployed the change clears it.
func TestIOKeptBaseHashRule(t *testing.T) {
	t.Run("a deploy moved the kept device", func(t *testing.T) {
		store, _, s2, _, _ := keptThenRestart(t)
		s4 := newSlave(t, 13)
		r := startKeepRig(t, ioFiles(slavePort(s4), slavePort(s2), ""), keepOpts{store: store})
		r.leading(t)
		if d := r.io.Differs(); len(d) != 0 {
			t.Fatalf("differs = %+v, want the deployed manifest running", d)
		}
		eventually(t, "A_In from the deployed address", func() bool { return r.value("A_In") == "13" })
		d := r.io.Discarded()
		if len(d) != 1 || d[0].Driver != "modbus" || d[0].Device != "DEV_A" || !strings.Contains(d[0].Why, "deployed manifest changed it") || d[0].By != "tester" {
			t.Fatalf("discarded = %+v, want DEV_A, the manifest moved", d)
		}
		r.stop()
		st := store.io(t)
		if st == nil || len(st.Units) != 0 || st.Discarded["modbus/DEV_A"].Why == "" || st.DiscardedFiles["modbus.yaml"] == "" {
			t.Fatalf("store io = %+v, want DEV_A discarded, with its files, and nothing kept", st)
		}
	})
	t.Run("a deploy moved another device", func(t *testing.T) {
		store, s1, _, _, _ := keptThenRestart(t)
		s5 := newSlave(t, 17)
		r := startKeepRig(t, ioFiles(slavePort(s1), slavePort(s5), ""), keepOpts{store: store})
		r.leading(t)
		if d := r.io.Differs(); len(d) != 1 || d[0].Device != "DEV_A" {
			t.Fatalf("differs = %+v, want DEV_A still applied", d)
		}
		eventually(t, "A_In kept, B_In deployed", func() bool { return r.value("A_In") == "9" && r.value("B_In") == "17" })
		if d := r.io.Discarded(); len(d) != 0 {
			t.Fatalf("discarded = %+v, want none", d)
		}
	})
	t.Run("the change was deployed", func(t *testing.T) {
		store, _, s2, s3, _ := keptThenRestart(t)
		r := startKeepRig(t, ioFiles(slavePort(s3), slavePort(s2), ""), keepOpts{store: store})
		r.leading(t)
		if d := r.io.Differs(); len(d) != 0 {
			t.Fatalf("differs = %+v, want none: the build says it", d)
		}
		if d := r.io.Discarded(); len(d) != 0 {
			t.Fatalf("discarded = %+v, want none: it was deployed, not replaced", d)
		}
		r.stop()
		if st := store.io(t); st != nil && (len(st.Units) != 0 || len(st.Discarded) != 0) {
			t.Fatalf("store io = %+v, want nothing kept", st)
		}
	})
	t.Run("the kept files do not rebuild it", func(t *testing.T) {
		store, s1, s2, _, files := keptThenRestart(t)
		s4 := newSlave(t, 13)
		store.edit(t, func(st *retain.State) {
			st.IO.Files["modbus.yaml"] = modbusYAML(slavePort(s4), slavePort(s2), "", "")
		})
		r := startKeepRig(t, files, keepOpts{store: store})
		r.leading(t)
		if d := r.io.Differs(); len(d) != 0 {
			t.Fatalf("differs = %+v, want the manifest", d)
		}
		eventually(t, "A_In from the manifest's address", func() bool { return r.value("A_In") == "4" })
		_ = s1
		if d := r.io.Discarded(); len(d) != 1 || !strings.Contains(d[0].Why, "do not rebuild") {
			t.Fatalf("discarded = %+v, want DEV_A, not rebuilt", d)
		}
	})
}

// Kept units that, with what a deploy says for the rest, would bind one
// output twice by address: nothing validated that set, so none comes back.
func TestIOKeptSetThatFailsValidationIsDropped(t *testing.T) {
	s1, s2, s3, s4 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9), newSlave(t, 11)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	store := &memStore{}
	r := startKeepRig(t, files, keepOpts{store: store})
	r.leading(t)
	r.applyMove(t, files, slavePort(s3), slavePort(s4), ApplyOptions{}) // DEV_A → s3, DEV_B → s4
	r.stop()

	// The deploy moves DEV_B onto s3, where the kept DEV_A writes its Cmd.
	r2 := startKeepRig(t, ioFiles(slavePort(s1), slavePort(s3), ""), keepOpts{store: store})
	r2.leading(t)
	if d := r2.io.Differs(); len(d) != 0 {
		t.Fatalf("differs = %+v, want the deployed manifest alone", d)
	}
	var whyA string
	for _, d := range r2.io.Discarded() {
		if d.Device == "DEV_A" {
			whyA = d.Why
		}
	}
	if !strings.Contains(whyA, "fail validation") || !strings.Contains(whyA, "outputs") {
		t.Fatalf("discarded = %+v, want DEV_A dropped with the outputs check named", r2.io.Discarded())
	}
}

// Durable on answer: a redundant project's apply the store cannot keep is
// put back; a standalone one stays applied and says it is not kept; one
// with no retain: block says why.
func TestIOKeptDurable(t *testing.T) {
	t.Run("redundant, the save fails", func(t *testing.T) {
		s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
		files := ioFiles(slavePort(s1), slavePort(s2), "")
		store := &memStore{}
		coord := &keepCoord{}
		coord.lead.Store(true)
		coord.term.Store(1)
		r := startKeepRig(t, files, keepOpts{store: store, coord: coord})
		r.leading(t)
		eventually(t, "DEV_A polled", func() bool { return r.value("A_In") == "4" })
		store.mu.Lock()
		store.failSave = errors.New("disk full")
		store.mu.Unlock()
		c := r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", "")), "upload", "tester")
		if v := r.io.Validate(c); !v.Valid {
			t.Fatalf("validate: %v", failed(v))
		}
		_, err := r.io.Apply(r.ctx, c, "tester")
		if !errors.Is(err, ErrNotKept) || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("apply err = %v, want ErrNotKept naming the save", err)
		}
		if d := r.io.Differs(); len(d) != 0 {
			t.Fatalf("differs = %+v, want the apply put back", d)
		}
		s1.Unit(1).SetHolding(0, 6)
		eventually(t, "A_In from the old device again", func() bool { return r.value("A_In") == "6" })
	})
	t.Run("standalone, the save fails", func(t *testing.T) {
		s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
		files := ioFiles(slavePort(s1), slavePort(s2), "")
		store := &memStore{failSave: errors.New("disk full")}
		r := startKeepRig(t, files, keepOpts{store: store})
		r.leading(t)
		res := r.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{})
		if res.Kept || !strings.Contains(res.KeptWhy, "disk full") {
			t.Fatalf("kept %v (%s), want not kept, the save named", res.Kept, res.KeptWhy)
		}
		if d := r.io.Differs(); len(d) != 1 {
			t.Fatalf("differs = %+v, want it applied", d)
		}
		if r.io.NotKept() == "" {
			t.Fatal("NotKept is empty, want why what runs is not kept")
		}
	})
	t.Run("no retain: block", func(t *testing.T) {
		s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
		files := ioFiles(slavePort(s1), slavePort(s2), "")
		r := startKeepRig(t, files, keepOpts{})
		r.leading(t)
		res := r.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{})
		if res.Kept || res.KeptWhy != notKeptNoRetain || r.io.NotKept() != notKeptNoRetain {
			t.Fatalf("kept %v (%s), NotKept %q; want %q", res.Kept, res.KeptWhy, r.io.NotKept(), notKeptNoRetain)
		}
	})
}

// A redundant pair: the standby follows what the leader applied while it
// stands by, so its takeover re-applies nothing (#710's takeover bound) and
// commands the applied address first; a change back to the manifest is
// followed too. A leader that fails before the standby followed has the
// edge re-apply it, before the first scan.
func TestIOKeptAcrossATakeover(t *testing.T) {
	shortFollow(t)
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	store := &memStore{}
	ca, cb := &keepCoord{}, &keepCoord{}
	ca.lead.Store(true)
	ca.term.Store(1)
	a := startKeepRig(t, files, keepOpts{store: store, coord: ca})
	b := startKeepRig(t, files, keepOpts{store: store, coord: cb})
	a.leading(t)
	eventually(t, "DEV_A polled", func() bool { return a.value("A_In") == "4" })
	if res := a.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{}); !res.Kept {
		t.Fatalf("not kept: %s", res.KeptWhy)
	}
	eventually(t, "the standby to follow", func() bool { return len(b.io.Differs()) == 1 })
	// Differs() turns 1 inside the follower's apply; what the takeover edge
	// compares (IOCurrent) is recorded only when the follow settles, a moment
	// later. An edge that lands between the two calls RestoreIO (which then
	// finds nothing to change), so wait for the settled state.
	eventually(t, "the follow to settle on the store", func() bool {
		st, err := store.Load()
		if err != nil {
			return false
		}
		b.io.mu.Lock()
		defer b.io.mu.Unlock()
		return b.io.memo.st == hashOf(st.IO) && b.io.memo.gen == b.io.gen
	})
	if b.scans.Load() != 0 {
		t.Fatal("the standby scanned")
	}

	s1.Unit(1).SetHolding(10, 999)
	ca.lead.Store(false)
	cb.term.Store(2)
	t0 := time.Now()
	cb.lead.Store(true)
	b.leading(t)
	gap := time.Since(t0)
	if n := b.keeper.leading.Load(); n != 0 {
		t.Fatalf("the takeover re-applied the I/O configuration %d time(s), want none: the standby had followed", n)
	}
	if n := b.firstDiffers.Load(); n != 1 {
		t.Fatalf("%d units differed at the new leader's first scan, want 1", n)
	}
	if gap > time.Second {
		t.Fatalf("the takeover took %v", gap)
	}
	t.Logf("takeover with a kept unit, lead to first scan: %v", gap)
	s3.Unit(1).SetHolding(0, 20)
	eventually(t, "the new leader commands the applied address", func() bool { return s3.Unit(1).Holding(10) == 21 })
	if got := s1.Unit(1).Holding(10); got != 999 {
		t.Fatalf("the old address was written: Cmd %d", got)
	}

	// Back to the manifest on the new leader: the ex-leader follows it back.
	c := b.io.Stage(files, "upload", "tester")
	if v := b.io.Validate(c); !v.Valid {
		t.Fatalf("validate: %v", failed(v))
	}
	if _, err := b.io.Apply(b.ctx, c, "tester"); err != nil {
		t.Fatal(err)
	}
	if st := store.io(t); st != nil && len(st.Units) != 0 {
		t.Fatalf("store io = %+v, want nothing kept", st)
	}
	eventually(t, "the ex-leader to follow back to the manifest", func() bool { return len(a.io.Differs()) == 0 })
}

// A leader that applied and failed before its standby followed: the edge
// re-applies it, holding the first scan until it runs.
func TestIOKeptTakeoverBeforeTheStandbyFollowed(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	store := &memStore{}
	ca, cb := &keepCoord{}, &keepCoord{}
	ca.lead.Store(true)
	ca.term.Store(1)
	a := startKeepRig(t, files, keepOpts{store: store, coord: ca})
	// Held on swap: a takeover's push is still the full set, restore or not.
	b := startKeepRig(t, files, keepOpts{store: store, coord: cb, noFollow: true, hold: true})
	a.leading(t)
	a.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{})
	a.stop()
	s3.Unit(1).SetHolding(10, 999)
	cb.term.Store(2)
	cb.lead.Store(true)
	b.leading(t)
	if n := b.keeper.leading.Load(); n != 1 {
		t.Fatalf("the takeover re-applied %d time(s), want once", n)
	}
	if n := b.firstDiffers.Load(); n != 1 {
		t.Fatalf("%d units differed at the first scan, want DEV_A re-applied before it", n)
	}
	eventually(t, "A_In from the applied address", func() bool { return b.value("A_In") == "9" })
	eventually(t, "the takeover's push to the applied address", func() bool { return s3.Unit(1).Holding(10) == 10 })
}

// An apply waiting to revert if its devices stay silent is not kept until
// its watch settles: a restart meanwhile comes back on the configuration
// before it and says so; answered or cancelled, it is kept.
func TestIOKeptPendingRevert(t *testing.T) {
	t.Run("not carried across a restart", func(t *testing.T) {
		s1, s2 := newSlave(t, 4), newSlave(t, 7)
		files := staticFiles(slavePort(s1), slavePort(s2))
		store := &memStore{}
		r := startKeepRig(t, files, keepOpts{store: store})
		r.leading(t)
		res := r.applyMove(t, files, deadPort(t), slavePort(s2), ApplyOptions{RevertIfSilent: time.Minute, ID: "io-9"})
		if res.Kept || !strings.Contains(res.KeptWhy, "revertIfSilent") {
			t.Fatalf("kept %v (%s), want not yet", res.Kept, res.KeptWhy)
		}
		if st := store.io(t); st == nil || st.Provisional == nil || len(st.Units) != 0 {
			t.Fatalf("store io = %+v (watch %+v), want the provisional apply recorded and nothing kept", st, r.io.Watch())
		}
		r.stop()
		r2 := startKeepRig(t, files, keepOpts{store: store})
		r2.leading(t)
		if d := r2.io.Differs(); len(d) != 0 {
			t.Fatalf("differs = %+v, want the configuration before the pending apply", d)
		}
		d := r2.io.Discarded()
		if len(d) != 1 || d[0].Driver != "" || !strings.Contains(d[0].Why, "not carried") || d[0].By != "tester" {
			t.Fatalf("discarded = %+v, want the pending apply reported", d)
		}
		eventually(t, "A_In from the manifest's address", func() bool { return r2.value("A_In") == "4" })
		// The journal must not say ok while the old configuration runs: the
		// restore reports the dropped apply (registered after the restore, as
		// the server is at start-up), once.
		var mu sync.Mutex
		var got []IOWatch
		r2.io.OnReverted(func(w IOWatch) { mu.Lock(); got = append(got, w); mu.Unlock() })
		eventually(t, "the dropped apply reported", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) > 0 })
		time.Sleep(200 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		if len(got) != 1 || got[0].State != WatchDropped || got[0].ID != "io-9" || got[0].By != "tester" || got[0].From != "upload" {
			t.Fatalf("reports %+v, want one dropped apply io-9 by tester", got)
		}
		// Cleared from the store once the restore settles: restarting
		// again with no apply in between journals nothing more.
		eventually(t, "the store to forget the dropped apply", func() bool {
			st := store.io(t)
			return st != nil && st.Provisional == nil
		})
		r2.stop()
		r3 := startKeepRig(t, files, keepOpts{store: store})
		var got3 []IOWatch
		var mu3 sync.Mutex
		r3.io.OnReverted(func(w IOWatch) { mu3.Lock(); got3 = append(got3, w); mu3.Unlock() })
		r3.leading(t)
		time.Sleep(300 * time.Millisecond)
		mu3.Lock()
		defer mu3.Unlock()
		if len(got3) != 0 || len(r3.io.Discarded()) != 0 && strings.Contains(r3.io.Discarded()[0].Why, "not carried") {
			t.Fatalf("a second restart reported %+v (discarded %+v), want nothing", got3, r3.io.Discarded())
		}
	})
	t.Run("a standby following the pending apply journals nothing", func(t *testing.T) {
		shortFollow(t)
		s1, s2 := newSlave(t, 4), newSlave(t, 7)
		files := staticFiles(slavePort(s1), slavePort(s2))
		store := &memStore{}
		ca, cb := &keepCoord{}, &keepCoord{}
		ca.lead.Store(true)
		ca.term.Store(1)
		a := startKeepRig(t, files, keepOpts{store: store, coord: ca})
		b := startKeepRig(t, files, keepOpts{store: store, coord: cb})
		var mu sync.Mutex
		var got []IOWatch
		b.io.OnReverted(func(w IOWatch) { mu.Lock(); got = append(got, w); mu.Unlock() })
		a.leading(t)
		a.applyMove(t, files, deadPort(t), slavePort(s2), ApplyOptions{RevertIfSilent: time.Minute, ID: "io-5"})
		eventually(t, "the store to hold the provisional apply", func() bool {
			st := store.io(t)
			return st != nil && st.Provisional != nil
		})
		time.Sleep(500 * time.Millisecond) // the standby follows the store a few times
		mu.Lock()
		defer mu.Unlock()
		if len(got) != 0 {
			t.Fatalf("a following standby reported %+v, want nothing", got)
		}
	})
	t.Run("answered, then kept", func(t *testing.T) {
		s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
		files := ioFiles(slavePort(s1), slavePort(s2), "")
		store := &memStore{}
		r := startKeepRig(t, files, keepOpts{store: store})
		r.leading(t)
		r.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{RevertIfSilent: 5 * time.Second})
		if w := waitWatch(t, r.ioRig); w.State != WatchAnswered {
			t.Fatalf("watch %s, want answered", w.State)
		}
		eventually(t, "the store to keep it", func() bool {
			st := store.io(t)
			return st != nil && st.Provisional == nil && st.Units["modbus/DEV_A"].Hash != ""
		})
	})
	t.Run("cancelled, then kept", func(t *testing.T) {
		s1, s2 := newSlave(t, 4), newSlave(t, 7)
		files := staticFiles(slavePort(s1), slavePort(s2))
		store := &memStore{}
		r := startKeepRig(t, files, keepOpts{store: store})
		r.leading(t)
		r.applyMove(t, files, deadPort(t), slavePort(s2), ApplyOptions{RevertIfSilent: time.Minute})
		if _, err := r.io.CancelRevert("tester"); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the store to keep it", func() bool {
			st := store.io(t)
			return st != nil && st.Provisional == nil && st.Units["modbus/DEV_A"].Hash != ""
		})
	})
}

// Close waits for a revert under way (naut-lead's carry-forward (a)).
func TestIOCloseWaitsForARevert(t *testing.T) {
	s1, s2 := newSlave(t, 4), newSlave(t, 7)
	files := staticFiles(slavePort(s1), slavePort(s2))
	r := startKeepRig(t, files, keepOpts{})
	r.leading(t)
	real := r.io.replaceSource
	r.io.replaceSource = func(mb *modbus.Driver, src modbus.Source, tags []modbus.TagBinding, opts modbus.ReplaceOptions) error {
		if src.Port == slavePort(s1) {
			time.Sleep(400 * time.Millisecond) // putting DEV_A back is slow
		}
		return real(mb, src, tags, opts)
	}
	r.applyMove(t, files, deadPort(t), slavePort(s2), ApplyOptions{RevertIfSilent: 200 * time.Millisecond})
	eventually(t, "the revert to start", func() bool { w := r.io.Watch(); return w != nil && w.State == WatchReverting })
	r.io.Close()
	if w := r.io.Watch(); w.State != WatchReverted {
		t.Fatalf("watch %s after Close, want reverted: Close cut the revert short", w.State)
	}
}

// A replica that stepped down never reverts: the store never held the
// pending apply, and the leader runs what it keeps.
func TestIOWatchDoesNotRevertOffLeader(t *testing.T) {
	s1, s2 := newSlave(t, 4), newSlave(t, 7)
	files := staticFiles(slavePort(s1), slavePort(s2))
	coord := &keepCoord{}
	coord.lead.Store(true)
	coord.term.Store(1)
	r := startKeepRig(t, files, keepOpts{store: &memStore{}, coord: coord, noFollow: true})
	r.leading(t)
	r.applyMove(t, files, deadPort(t), slavePort(s2), ApplyOptions{RevertIfSilent: 300 * time.Millisecond})
	coord.lead.Store(false)
	w := waitWatch(t, r.ioRig)
	if w.State != WatchSuperseded || !strings.Contains(w.Reason, "no longer leads") {
		t.Fatalf("watch %s (%s), want superseded: no longer leads", w.State, w.Reason)
	}
	if d := r.io.Differs(); len(d) != 1 {
		t.Fatalf("differs = %+v, want the apply left as it is", d)
	}
}

// A device that answers while the watch waits for the apply lock is not
// silent: the watch ends answered with Silent empty (carry-forward (b)).
func TestIOWatchClearsSilentWhenTheResampleAnswers(t *testing.T) {
	s1, s2 := newSlave(t, 4), newSlave(t, 7)
	files := staticFiles(slavePort(s1), slavePort(s2))
	r := startKeepRig(t, files, keepOpts{})
	r.leading(t)
	// The device's own server holds its port from the start (a port picked,
	// closed and bound again later was taken by another test's connection in
	// CI); with no unit 1 it answers every read with exception 0x0B, which
	// reads as silence. Coming up is creating the unit.
	late := slave.NewServer("", nil)
	if err := late.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(late.Stop)
	r.applyMove(t, files, slavePort(late), slavePort(s2), ApplyOptions{RevertIfSilent: 300 * time.Millisecond})
	r.io.applying.Lock() // the revert waits here
	eventually(t, "the deadline to find DEV_A silent", func() bool { w := r.io.Watch(); return len(w.Silent) == 1 })
	late.Unit(1).SetHolding(0, 8)
	eventually(t, "DEV_A to answer late", func() bool { return r.value("A_In") == "8" })
	r.io.applying.Unlock()
	w := waitWatch(t, r.ioRig)
	if w.State != WatchAnswered || len(w.Silent) != 0 {
		t.Fatalf("watch %s, silent %v; want answered, none silent", w.State, w.Silent)
	}
}

// A kept change that cannot be re-applied leaves its driver holding its
// outputs (James's decision 2 on #706): the boot address is polled but
// never commanded, Fault says why, and an apply lifts the hold.
func TestIOKeptRestoreFailureHoldsOutputs(t *testing.T) {
	store, s1, s2, s3, files := keptThenRestart(t)
	s1.Unit(1).SetHolding(10, 999)
	r := startKeepRig(t, files, keepOpts{store: store, prep: func(io *IOConfig) {
		real := io.replaceSource
		io.replaceSource = func(mb *modbus.Driver, src modbus.Source, tags []modbus.TagBinding, opts modbus.ReplaceOptions) error {
			if src.Port == slavePort(s3) {
				return errors.New("refused")
			}
			return real(mb, src, tags, opts)
		}
	}})
	r.leading(t)
	if f := r.io.Fault(); !strings.Contains(f, "not re-applied") || !strings.Contains(f, "reading Bad") {
		t.Fatalf("fault = %q, want the failed re-apply and the hold", f)
	}
	// Held: not polled at the manifest's address, its tags Bad, nothing sent.
	s1.Unit(1).SetHolding(0, 6)
	time.Sleep(200 * time.Millisecond)
	if q := r.rt.TagQuality("A_In"); q != nio.Bad {
		t.Fatalf("A_In quality %v, want Bad: the held device must not be trusted", q)
	}
	if got := r.value("A_In"); got == "6" {
		t.Fatal("the held device was polled at the manifest's address")
	}
	if got := s1.Unit(1).Holding(10); got != 999 {
		t.Fatalf("the held device was commanded at the manifest's address: Cmd %d", got)
	}
	if q := r.rt.TagQuality("B_In"); !q.IsGood() {
		t.Fatalf("B_In quality %v, want Good: only DEV_A is held", q)
	}
	// An apply establishes what runs again, and lifts the hold.
	s4 := newSlave(t, 13)
	r.applyMove(t, files, slavePort(s4), slavePort(s2), ApplyOptions{})
	eventually(t, "the applied device commanded", func() bool { return s4.Unit(1).Holding(10) == 14 })
	if f := r.io.Fault(); f != "" {
		t.Fatalf("fault = %q after the apply, want none", f)
	}
	if q := r.rt.TagQuality("A_In"); !q.IsGood() {
		t.Fatalf("A_In quality %v after the apply, want Good", q)
	}
}

// A kept change to a Modbus driver's own settings swaps it whole, with
// every device: when a deploy moved one of its devices, the driver change
// is dropped with it rather than swapped in over the deployed address.
func TestIOKeptDriverDroppedWithItsDevice(t *testing.T) {
	s1, s2, s3, s4 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9), newSlave(t, 13)
	ua := uaServer(t, 1.5)
	files := ioFiles(slavePort(s1), slavePort(s2), ua)
	store := &memStore{}
	r := startKeepRig(t, files, keepOpts{store: store})
	r.leading(t)
	cand := withFile(files, "nautilus.yaml", strings.Replace(string(files["nautilus.yaml"].Data), "scan-rate: 10ms", "scan-rate: 15ms", 1))
	cand = withFile(cand, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", ""))
	c := r.io.Stage(cand, "upload", "tester")
	if v := r.io.Validate(c); !v.Valid {
		t.Fatalf("validate: %v %+v", failed(v), v.Checks)
	}
	if res, err := r.io.Apply(r.ctx, c, "tester"); err != nil || !res.Kept {
		t.Fatalf("apply: %v %+v", err, res)
	}
	if st := store.io(t); st == nil || st.Units["modbus"].Hash == "" || st.Units["modbus/DEV_A"].Hash == "" {
		t.Fatalf("store io = %+v, want the driver and DEV_A kept", st)
	}
	r.stop()

	r2 := startKeepRig(t, ioFiles(slavePort(s4), slavePort(s2), ua), keepOpts{store: store})
	r2.leading(t)
	eventually(t, "A_In from the deployed address", func() bool { return r2.value("A_In") == "13" })
	var why string
	for _, d := range r2.io.Discarded() {
		if d.Driver == "modbus" && d.Device == "" {
			why = d.Why
		}
	}
	if !strings.Contains(why, "device DEV_A") {
		t.Fatalf("discarded = %+v, want driver modbus dropped with DEV_A", r2.io.Discarded())
	}
	if d := r2.io.Differs(); len(d) != 0 {
		t.Fatalf("differs = %+v, want the deployed manifest", d)
	}
}

// A standalone restart with a kept unit behaves as a boot does: the first
// scan's outputs are the start-up baseline, decided against what each device
// holds (#1123), on the re-applied device and on the untouched one alike: a
// device holding another value is commanded once, and a change is sent.
func TestIOKeptRestartDecidesTheStartBaselineAgainstTheDevice(t *testing.T) {
	store, _, s2, s3, files := keptThenRestart(t)
	s3.Unit(1).SetHolding(10, 999)
	s2.Unit(1).SetHolding(10, 999)
	// Inputs off their seed (5, not 0): the first scan computes Cmd from the
	// seed (1) before anything was read, and that value must never go out.
	s3.Unit(1).SetHolding(0, 5)
	s2.Unit(1).SetHolding(0, 5)
	r := startKeepRig(t, files, keepOpts{store: store})
	r.leading(t)
	eventually(t, "both devices polled", func() bool {
		return r.rt.TagQuality("A_In").IsGood() && r.rt.TagQuality("B_In").IsGood()
	})
	time.Sleep(150 * time.Millisecond)
	eventually(t, "the live-input value decided against both devices", func() bool {
		if s3.Unit(1).Holding(10) == 1 || s2.Unit(1).Holding(10) == 1 {
			t.Fatal("the init-derived first value went out before the inputs were read")
		}
		return s3.Unit(1).Holding(10) == 6 && s2.Unit(1).Holding(10) == 6
	})
	s3.Unit(1).SetHolding(0, 11)
	eventually(t, "a change sent", func() bool { return s3.Unit(1).Holding(10) == 12 })
}

// The same for a driver swapped whole by the restore (a kept change to its
// own settings): its start-up baseline is not disarmed, so it decides
// against the device as a boot does.
func TestIOKeptDriverSwapAtStartDecidesTheBaselineAgainstTheDevice(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	ua := uaServer(t, 1.5)
	files := ioFiles(slavePort(s1), slavePort(s2), ua)
	store := &memStore{}
	r := startKeepRig(t, files, keepOpts{store: store})
	r.leading(t)
	cand := withFile(files, "nautilus.yaml", strings.Replace(string(files["nautilus.yaml"].Data), "scan-rate: 10ms", "scan-rate: 15ms", 1))
	cand = withFile(cand, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", ""))
	c := r.io.Stage(cand, "upload", "tester")
	if v := r.io.Validate(c); !v.Valid {
		t.Fatalf("validate: %v", failed(v))
	}
	if _, err := r.io.Apply(r.ctx, c, "tester"); err != nil {
		t.Fatal(err)
	}
	r.stop()
	s3.Unit(1).SetHolding(10, 999)
	s2.Unit(1).SetHolding(10, 999)
	s3.Unit(1).SetHolding(0, 5) // off the seed: see TestIOKeptRestartDecidesTheStartBaselineAgainstTheDevice
	s2.Unit(1).SetHolding(0, 5)
	r2 := startKeepRig(t, files, keepOpts{store: store})
	r2.leading(t)
	if d, ok := unitOf(r2.io.Differs(), "modbus"); !ok || d.RunningHash == d.ManifestHash {
		t.Fatalf("differs = %+v, want the modbus driver swapped whole", r2.io.Differs())
	}
	eventually(t, "both devices polled", func() bool {
		return r2.rt.TagQuality("A_In").IsGood() && r2.rt.TagQuality("B_In").IsGood()
	})
	time.Sleep(150 * time.Millisecond)
	eventually(t, "the live-input value decided against both devices", func() bool {
		if s3.Unit(1).Holding(10) == 1 || s2.Unit(1).Holding(10) == 1 {
			t.Fatal("the init-derived first value went out before the inputs were read")
		}
		return s3.Unit(1).Holding(10) == 6 && s2.Unit(1).Holding(10) == 6
	})
}

// A redundant project whose store is a file (one replica's own) cannot
// keep an apply: refused at validate.
func TestIOKeptRefusesAFileStoreUnderRedundancy(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	r := startKeepRig(t, files, keepOpts{store: &memStore{}, redundant: true, prep: func(io *IOConfig) {}})
	r.io.KeepStore = "file"
	c := r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", "")), "upload", "tester")
	v := r.io.Validate(c)
	if v.Valid || fmt.Sprint(failed(v)) != "[kept]" {
		t.Fatalf("failed %v, want kept alone", failed(v))
	}
}

// A leader's pending revert across a takeover: the standby that follows
// the store does not report it while the leader still watches; the replica
// that takes over reports it and runs the configuration before it.
func TestIOKeptPendingRevertAcrossATakeover(t *testing.T) {
	shortFollow(t)
	s1, s2 := newSlave(t, 4), newSlave(t, 7)
	files := staticFiles(slavePort(s1), slavePort(s2))
	store := &memStore{}
	ca, cb := &keepCoord{}, &keepCoord{}
	ca.lead.Store(true)
	ca.term.Store(1)
	a := startKeepRig(t, files, keepOpts{store: store, coord: ca})
	b := startKeepRig(t, files, keepOpts{store: store, coord: cb})
	a.leading(t)
	a.applyMove(t, files, deadPort(t), slavePort(s2), ApplyOptions{RevertIfSilent: time.Minute})
	eventually(t, "the provisional apply in the store", func() bool { st := store.io(t); return st != nil && st.Provisional != nil })
	time.Sleep(4 * followInterval) // the standby follows it
	if d := b.io.Discarded(); len(d) != 0 {
		t.Fatalf("the standby reported %+v while the leader still watches", d)
	}
	ca.lead.Store(false)
	cb.term.Store(2)
	cb.lead.Store(true)
	b.leading(t)
	if d := b.io.Discarded(); len(d) != 1 || !strings.Contains(d[0].Why, "not carried") {
		t.Fatalf("discarded = %+v, want the pending apply reported by the new leader", d)
	}
	if d := b.io.Differs(); len(d) != 0 {
		t.Fatalf("differs = %+v, want the configuration before the pending apply", d)
	}
}

// A standby's follow never acts on the leader it has become, nor on a store
// older than what it runs (naut-lead's review of #1010, finding 1): a slow
// read of the store from before an apply must not undo the apply.
func TestIOFollowNeverUndoesANewerApply(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	store := &memStore{}
	coord := &keepCoord{}
	coord.lead.Store(true)
	coord.term.Store(1)
	r := startKeepRig(t, files, keepOpts{store: store, coord: coord, noFollow: true})
	r.leading(t)
	before := store.io(t) // the store as a slow read would return it
	r.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{})
	if len(r.io.Differs()) != 1 {
		t.Fatal("the apply did not land")
	}
	ctx := context.Background()
	// Leading: a follow does nothing, whatever the store says, even one
	// that looks newer (it would take DEV_A back to the manifest).
	if err := r.io.restoreFrom(ctx, &retain.IOState{Seq: r.io.seqNow() + 1}, true); err != nil {
		t.Fatal(err)
	}
	if len(r.io.Differs()) != 1 {
		t.Fatal("a follow on the leader undid the apply")
	}
	// Stood down, a follow of an older store still does nothing.
	coord.lead.Store(false)
	stale := &retain.IOState{Seq: 0}
	if before != nil {
		stale = before
	}
	if err := r.io.restoreFrom(ctx, stale, true); err != nil {
		t.Fatal(err)
	}
	if len(r.io.Differs()) != 1 {
		t.Fatal("a follow of an older store undid the apply")
	}
	// While a newer one is followed.
	newer := *store.io(t)
	newer.Units, newer.Files = nil, nil
	newer.Seq++
	if err := r.io.restoreFrom(ctx, &newer, true); err != nil {
		t.Fatal(err)
	}
	if len(r.io.Differs()) != 0 {
		t.Fatalf("differs = %+v, want a newer store followed back to the manifest", r.io.Differs())
	}
}

// A restore that cannot take the apply lock before its deadline still
// holds what it would change, reports it, and keeps the store's state
// (finding 4).
func TestIORestoreLockTimeoutHolds(t *testing.T) {
	store, s1, _, _, files := keptThenRestart(t)
	kept := store.io(t)
	s1.Unit(1).SetHolding(10, 999)
	r := startKeepRig(t, files, keepOpts{store: &memStore{}}) // its own store: nothing to restore at its edge
	r.leading(t)
	r.io.applying.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err := r.io.RestoreIO(ctx, kept)
	cancel()
	r.io.applying.Unlock()
	if err == nil || !strings.Contains(err.Error(), "an apply in progress") {
		t.Fatalf("err = %v, want the lock wait named", err)
	}
	if f := r.io.Fault(); !strings.Contains(f, "reading Bad") {
		t.Fatalf("fault = %q, want the hold", f)
	}
	s1.Unit(1).SetHolding(0, 6)
	time.Sleep(200 * time.Millisecond)
	if q := r.rt.TagQuality("A_In"); q != nio.Bad || r.value("A_In") == "6" {
		t.Fatalf("A_In %s quality %v, want held: not polled, Bad", r.value("A_In"), q)
	}
	if got := r.io.RetainedIO(); got == nil || got.Units["modbus/DEV_A"].Hash != kept.Units["modbus/DEV_A"].Hash {
		t.Fatalf("retained %+v, want the store's DEV_A kept, not dropped", got)
	}
}

// slowStore saves after a delay.
type slowStore struct {
	memStore
	delay time.Duration
}

func (s *slowStore) Save(st retain.State) error {
	time.Sleep(s.delay)
	return s.memStore.Save(st)
}

// A save slower than the apply's own wait is still a definite answer: the
// apply waits for the store, and is kept (finding 3).
func TestIOKeptSlowStoreIsDefinite(t *testing.T) {
	saved := keepTimeout
	keepTimeout = 50 * time.Millisecond
	t.Cleanup(func() { keepTimeout = saved })
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	store := &slowStore{delay: 300 * time.Millisecond}
	coord := &keepCoord{}
	coord.lead.Store(true)
	coord.term.Store(1)
	r := startKeepRig(t, files, keepOpts{store: store, coord: coord, noFollow: true})
	r.leading(t)
	res := r.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{})
	if !res.Kept {
		t.Fatalf("kept false (%s), want the slow save waited for", res.KeptWhy)
	}
	if st := store.io(t); st == nil || st.Units["modbus/DEV_A"].Hash == "" {
		t.Fatalf("store io = %+v, want DEV_A kept", st)
	}
}

// An apply waits for the controller's leadership edge to have re-applied
// what the store keeps: until then it is refused.
func TestIOApplyRefusedBeforeTheEdge(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	r := startIORig(t, files, false)
	r.io.Keep = func(context.Context) error { return nil }
	c := r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", "")), "upload", "tester")
	if v := r.io.Validate(c); !v.Valid {
		t.Fatalf("validate: %v", failed(v))
	}
	if _, err := r.io.Apply(r.ctx, c, "tester"); !errors.Is(err, ErrApplyBusy) || !strings.Contains(err.Error(), "not yet re-applied") {
		t.Fatalf("apply before the edge: %v, want refused", err)
	}
	r.io.IOCurrent(nil) // the edge: nothing kept, what runs is current
	if _, err := r.io.Apply(r.ctx, c, "tester"); err != nil {
		t.Fatalf("apply after the edge: %v", err)
	}
}

// The follow's checks hold under the apply lock too: a follow that passed
// them while standing by, then waited for the lock while this replica took
// over and applied, does nothing (the reviewer's sequence on #1010).
func TestIOFollowRechecksUnderTheLock(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	store := &memStore{}
	coord := &keepCoord{}
	coord.lead.Store(true)
	coord.term.Store(1)
	r := startKeepRig(t, files, keepOpts{store: store, coord: coord, noFollow: true})
	r.leading(t)
	r.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{})
	// A store that would take DEV_A back to the manifest, newer than what
	// runs as far as a standby could tell when it read it.
	stale := &retain.IOState{Seq: r.io.seqNow() + 1}
	coord.lead.Store(false)
	r.io.applying.Lock() // an apply in progress: the follow waits here
	done := make(chan error, 1)
	go func() { done <- r.io.restoreFrom(context.Background(), stale, true) }()
	time.Sleep(50 * time.Millisecond) // past its first check, waiting
	coord.lead.Store(true)            // this replica took over meanwhile
	r.io.applying.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if d := r.io.Differs(); len(d) != 1 {
		t.Fatalf("differs = %+v: the follow acted on the leader", d)
	}
}

// A store that cannot be read at the edge does not block applies for the
// life of the process: standalone, an apply is allowed and answered not
// kept; on a redundant project it is refused (#1010 re-review, 1).
func TestIOKeptStoreUnreadableAtTheEdge(t *testing.T) {
	t.Run("standalone", func(t *testing.T) {
		s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
		files := ioFiles(slavePort(s1), slavePort(s2), "")
		store := &memStore{failLoad: errors.New("store down")}
		r := startKeepRig(t, files, keepOpts{store: store})
		eventually(t, "the first scan", func() bool { return r.scans.Load() > 0 })
		res := r.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{})
		if res.Kept || !strings.Contains(res.KeptWhy, "could not be read") {
			t.Fatalf("kept %v (%s), want applied and not kept, the unreadable store named", res.Kept, res.KeptWhy)
		}
		if nk := r.io.NotKept(); !strings.Contains(nk, "could not be read") {
			t.Fatalf("NotKept = %q", nk)
		}
	})
	t.Run("redundant", func(t *testing.T) {
		s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
		files := ioFiles(slavePort(s1), slavePort(s2), "")
		store := &memStore{failLoad: errors.New("store down")}
		coord := &keepCoord{}
		coord.lead.Store(true)
		coord.term.Store(1)
		r := startKeepRig(t, files, keepOpts{store: store, coord: coord, noFollow: true})
		eventually(t, "the first scan", func() bool { return r.scans.Load() > 0 })
		c := r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", "")), "upload", "tester")
		v := r.io.Validate(c)
		if v.Valid || fmt.Sprint(failed(v)) != "[kept]" || !strings.Contains(fmt.Sprint(v.Checks), "could not be read") {
			t.Fatalf("failed %v, want kept: the store could not be read", failed(v))
		}
	})
}

// Two replicas never take one sequence number for two states: a leader
// whose apply was never saved (the store down, so put back) does not move
// its number on, so it still follows the next leader's first save, and
// its own takeover after holds nothing (#1010 re-review, 2).
func TestIOFollowAfterAnUnsavedApply(t *testing.T) {
	shortFollow(t)
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	store := &memStore{}
	ca, cb := &keepCoord{}, &keepCoord{}
	ca.lead.Store(true)
	ca.term.Store(1)
	a := startKeepRig(t, files, keepOpts{store: store, coord: ca})
	b := startKeepRig(t, files, keepOpts{store: store, coord: cb})
	a.leading(t)
	// A applies with the store down: its save and its put-back both fail.
	store.mu.Lock()
	store.failSave = errors.New("store down")
	store.mu.Unlock()
	c := a.io.Stage(withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", "")), "upload", "tester")
	if v := a.io.Validate(c); !v.Valid {
		t.Fatalf("validate: %v", failed(v))
	}
	if _, err := a.io.Apply(a.ctx, c, "tester"); !errors.Is(err, ErrNotKept) {
		t.Fatalf("apply on a down store: %v, want put back", err)
	}
	store.mu.Lock()
	store.failSave = nil
	store.mu.Unlock()
	// A steps down; B takes over and applies the move, kept.
	ca.lead.Store(false)
	cb.term.Store(2)
	cb.lead.Store(true)
	b.leading(t)
	if res := b.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{}); !res.Kept {
		t.Fatalf("B's apply not kept: %s", res.KeptWhy)
	}
	if st := store.io(t); st == nil || st.Seq != 1 || b.io.seqNow() != 1 || !b.io.IOCurrent(st) {
		t.Fatalf("store seq %v, B's %d, B current %v: want B's save to be the store's sequence 1, and B current with it",
			st, b.io.seqNow(), st != nil && b.io.IOCurrent(st))
	}
	eventually(t, "A to follow B's apply", func() bool { return len(a.io.Differs()) == 1 })
	// A takes over again: nothing to re-apply, so no hold.
	cb.lead.Store(false)
	ca.term.Store(3)
	ca.lead.Store(true)
	before := a.keeper.leading.Load()
	time.Sleep(4 * followInterval)
	if n := a.keeper.leading.Load() - before; n != 0 {
		t.Fatalf("A's takeover re-applied %d time(s), want none", n)
	}
	// And B, whose own save is what the store holds, takes over again with
	// nothing to re-apply either.
	ca.lead.Store(false)
	cb.term.Store(4)
	cb.lead.Store(true)
	before = b.keeper.leading.Load()
	time.Sleep(4 * followInterval)
	if n := b.keeper.leading.Load() - before; n != 0 {
		t.Fatalf("B's takeover re-applied %d time(s), want none: it saved what the store holds", n)
	}
}

// A follower acts on a store with its own sequence number but a different
// state: the number was taken twice, and the store's state is the one.
func TestIOFollowActsOnAnEqualSeqWithADifferentState(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	store := &memStore{}
	coord := &keepCoord{}
	coord.lead.Store(true)
	coord.term.Store(1)
	r := startKeepRig(t, files, keepOpts{store: store, coord: coord, noFollow: true})
	r.leading(t)
	r.applyMove(t, files, slavePort(s3), slavePort(s2), ApplyOptions{})
	coord.lead.Store(false)
	other := &retain.IOState{Seq: r.io.seqNow()} // same number, nothing kept
	if err := r.io.restoreFrom(context.Background(), other, true); err != nil {
		t.Fatal(err)
	}
	if d := r.io.Differs(); len(d) != 0 {
		t.Fatalf("differs = %+v, want the store's state followed", d)
	}
}

// #1013: a redundant pair where the other replica's build does not keep a
// live I/O apply across a takeover refuses the apply at validate, naming
// why; once it does, the apply validates.
func TestIOKeptRefusesAMixedVersionPair(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	const why = "the peer controller runs a build that does not keep a live I/O apply across a takeover (#1013)"
	peer := why
	r := startKeepRig(t, files, keepOpts{store: &memStore{}, redundant: true, prep: func(io *IOConfig) {
		io.PeersKeepIO = func() string { return peer }
	}})
	stage := func() *IOValidation {
		c := r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", "")), "upload", "tester")
		return r.io.Validate(c)
	}
	v := stage()
	if v.Valid || fmt.Sprint(failed(v)) != "[kept]" || !strings.Contains(fmt.Sprint(v), why) {
		t.Fatalf("a mixed-version pair: failed %v; want kept, naming the peer's build\n%+v", failed(v), v)
	}
	peer = ""
	if v := stage(); !v.Valid {
		t.Fatalf("both builds keep it: failed %v", failed(v))
	}
}

// #1013, the Kubernetes Lease mode: its replicas exchange no hello, so
// this build cannot see whether every replica keeps a live I/O apply. One
// on v0.22.0 or older does not: if it took over it would run the
// manifest's addresses and its next save would clear what was kept. So a
// live apply in Lease mode is refused at validate (kept), naming why and
// what to do, while a pair whose peer said keeps-live-io still validates.
//
// Pinned (RELEASING.md, "#1013"): the refusal stays through the first
// release that keeps a live apply, and is lifted only in the release after
// it, when no replica a rolling upgrade can pair it with predates it.
func TestIOKeptRefusesLeaseMode(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 9)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	stage := func(r *keepRig) *IOValidation {
		c := r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", "")), "upload", "tester")
		return r.io.Validate(c)
	}
	lease := startKeepRig(t, files, keepOpts{store: &memStore{}, leaseMode: true, prep: func(io *IOConfig) {
		io.KeepStore = "configmap"
	}})
	v := stage(lease)
	if v.Valid || fmt.Sprint(failed(v)) != "[kept]" || !strings.Contains(fmt.Sprint(v), "Kubernetes Lease") || !strings.Contains(fmt.Sprint(v), "deploy the change") {
		t.Fatalf("a live apply in Lease mode: failed %v; want kept, naming the Lease mode and to deploy instead (lift this only per RELEASING.md, #1013)\n%+v", failed(v), v)
	}
	pair := startKeepRig(t, files, keepOpts{store: &memStore{}, redundant: true, prep: func(io *IOConfig) {
		io.KeepStore = "configmap"
		io.PeersKeepIO = func() string { return "" }
	}})
	if v := stage(pair); !v.Valid {
		t.Fatalf("a pair whose peer keeps a live apply: failed %v", failed(v))
	}
}
