//go:build !js

package project

// Keeping a live I/O configuration across a restart and a takeover (#706
// PR 5). What an apply changed rides in the retain store (retain.IOState):
// per unit, the deployed manifest's hash it was applied over and its own,
// and the files it was read from that differ from the deployed ones. The
// runtime asks for it on every save (RetainedIO) and, on a leadership edge,
// before the first scan, has it re-applied (IOCurrent, RestoreIO). A standby
// follows the store while it stands by (Follow), so its edge finds what
// runs already current and holds nothing.
//
// The rule is the online edit's: a kept unit comes back only while the
// deployed manifest still says what it was applied over; a deploy that
// moved it wins, and the change is dropped and reported (Discarded).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/retain"
)

// ErrNotKept is an apply on a redundant project the retain store could not
// keep: it was put back, as a takeover would otherwise have.
var ErrNotKept = errors.New("the I/O apply was not kept")

const notKeptNoRetain = "not kept across a restart: the project has no retain: block, so a restart comes back on the manifest's configuration"

// keepLimit bounds the files an apply keeps in the retain store: a
// ConfigMap holds 1 MiB in all, and the store carries setpoints, edits and
// alarms besides.
var keepLimit = 512 << 10

// keepTimeout bounds the save an apply waits for before it answers.
var keepTimeout = 5 * time.Second

// putBackWait bounds the save of a put-back apply (ErrNotKept).
var putBackWait = 10 * time.Second

// followInterval is how often a standby reads the store to follow what the
// leader applied (Follow); the store's own save cadence.
var followInterval = 2 * time.Second

// IODiscard is a kept change a restart or takeover did not bring back, and
// why: the deployed manifest moved, or what was kept no longer rebuilds.
// One with no Driver is an apply not carried at all (a pending revert).
type IODiscard struct {
	Driver   string    `json:"driver,omitempty"`
	Device   string    `json:"device,omitempty"`
	Base     string    `json:"base,omitempty"`     // what it was applied over
	Manifest string    `json:"manifest,omitempty"` // what the deployed manifest says now
	From     string    `json:"from,omitempty"`
	At       time.Time `json:"at,omitzero"`
	By       string    `json:"by,omitempty"`
	Why      string    `json:"why"`
}

// Discarded is what the last restore did not bring back, empty when it
// brought back everything (or none has run).
func (io *IOConfig) Discarded() []IODiscard {
	io.mu.Lock()
	defer io.mu.Unlock()
	return append([]IODiscard(nil), io.dropped...)
}

// NotKept says why what runs would not come back after a restart: "" when
// it would (or what runs is the manifest's).
func (io *IOConfig) NotKept() string {
	if len(io.Differs()) == 0 {
		return ""
	}
	if io.Keep == nil {
		return notKeptNoRetain
	}
	io.mu.Lock()
	defer io.mu.Unlock()
	if io.unsaved != "" {
		return io.unsaved
	}
	if io.provisional != nil {
		return "the last apply is kept once its changed devices answer (revertIfSilent)"
	}
	if !sameHashes(io.kept, io.running) {
		return "what runs is not what the retain store keeps"
	}
	return ""
}

// recordFS records the files a candidate is read from.
type recordFS struct {
	base fs.FS
	mu   sync.Mutex
	seen map[string]bool
}

func (r *recordFS) Open(name string) (fs.File, error) {
	r.mu.Lock()
	r.seen[name] = true
	r.mu.Unlock()
	return r.base.Open(name)
}

// differingFiles are the files rec was read from whose content is not the
// boot files': what an apply of it keeps.
func (io *IOConfig) differingFiles(rec *recordFS) map[string]string {
	rec.mu.Lock()
	names := sortedKeys(rec.seen)
	rec.mu.Unlock()
	var out map[string]string
	for _, n := range names {
		b, err := fs.ReadFile(rec.base, n)
		if err != nil {
			continue // a directory, or a file it looked for and did not find
		}
		if io.bootFS != nil {
			if was, err := fs.ReadFile(io.bootFS, n); err == nil && bytes.Equal(was, b) {
				continue
			}
		}
		if out == nil {
			out = map[string]string{}
		}
		out[n] = string(b)
	}
	return out
}

func copyUnits(m map[string]ioUnit) map[string]ioUnit {
	out := make(map[string]ioUnit, len(m))
	for k, u := range m {
		out[k] = u
	}
	return out
}

// keptProblems refuses a candidate the store cannot keep where it must.
func (io *IOConfig) keptProblems(c *IOCandidate) []string {
	var out []string
	switch {
	case io.p.Redundancy != nil && io.Keep == nil:
		out = append(out, "this redundant project has no retain: block, so nothing keeps an applied configuration across a takeover and a standby would come up on the old addresses: add a retain: block, or deploy the change instead")
	case io.p.Redundancy != nil && io.KeepStore == "file":
		out = append(out, "this redundant project's retain store is a file, which only this replica reads: a standby would come up on the old addresses; deploy the change instead")
	}
	n := 0
	for p, b := range c.files {
		n += len(p) + len(b)
	}
	if io.Keep != nil && n > keepLimit {
		out = append(out, fmt.Sprintf("the files this apply keeps come to %d KiB, over the %d KiB the retain store keeps for I/O: deploy the change instead", n>>10, keepLimit>>10))
	}
	return out
}

// keepNow saves the store and says whether it now holds the apply.
func (io *IOConfig) keepNow(provisional bool) (bool, string) {
	if io.Keep == nil {
		return false, notKeptNoRetain
	}
	// Not the request's context: a client that goes away must not make an
	// apply unkept (and, on a redundant project, put back).
	ctx, cancel := context.WithTimeout(context.Background(), keepTimeout)
	defer cancel()
	if err := io.Keep(ctx); err != nil {
		why := "saving the retain store failed: " + err.Error()
		io.retryKeep(why)
		return false, why
	}
	io.mu.Lock()
	io.unsaved = ""
	io.mu.Unlock()
	if provisional {
		return false, "kept once its changed devices answer (revertIfSilent): a restart or takeover before that comes back on the configuration before it"
	}
	return true, ""
}

// retryKeep records why the store does not hold what is kept and retries
// the save, every followInterval, until one lands or the controller closes.
func (io *IOConfig) retryKeep(why string) {
	io.mu.Lock()
	io.unsaved = why
	start := !io.retrying
	io.retrying = true
	io.mu.Unlock()
	if !start {
		return
	}
	go func() {
		defer func() {
			io.mu.Lock()
			io.retrying = false
			io.mu.Unlock()
		}()
		t := time.NewTicker(followInterval)
		defer t.Stop()
		for {
			select {
			case <-io.closed:
				return
			case <-t.C:
			}
			ctx, cancel := context.WithTimeout(context.Background(), keepTimeout)
			err := io.Keep(ctx)
			cancel()
			if err == nil {
				io.mu.Lock()
				io.unsaved = ""
				io.mu.Unlock()
				return
			}
		}
	}()
}

// watchEnded settles what the store keeps once a revert-if-silent apply's
// watch is over: answered or cancelled, the apply is kept; reverted (or
// failed to revert), it never was, and the configuration before it stays
// the kept one.
func (io *IOConfig) watchEnded(w IOWatch) {
	if !w.Revert {
		return
	}
	io.mu.Lock()
	changed := false
	if (w.State == WatchAnswered || w.State == WatchCancelled) && io.gen == w.Gen {
		io.kept, changed = copyUnits(io.running), true
	}
	if io.provisional != nil && io.provisional.Gen == w.Gen && w.State != WatchSuperseded {
		io.provisional, changed = nil, true
	}
	if changed {
		io.seq++
	}
	io.mu.Unlock()
	if !changed || io.Keep == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), keepTimeout)
		defer cancel()
		if err := io.Keep(ctx); err != nil {
			slog.Warn("config.apply: saving the settled apply to the retain store failed; the saver retries", "watch", w.State, "err", err)
		}
	}()
}

// RetainedIO is what the retain store keeps of the I/O configuration: nil
// while what is kept is the manifest's, so a project that never applied
// writes what it wrote before (runtime.IOKeeper).
func (io *IOConfig) RetainedIO() *retain.IOState {
	io.mu.Lock()
	defer io.mu.Unlock()
	st := &retain.IOState{}
	for _, k := range sortedKeys(io.kept) {
		u := io.kept[k]
		b, ok := io.boot[k]
		if !ok || u.hash == b.hash || u.hash == unknownHash {
			continue
		}
		if st.Units == nil {
			st.Units = map[string]retain.IOUnit{}
		}
		st.Units[k] = retain.IOUnit{Base: b.hash, Hash: u.hash, From: u.from, At: u.at, By: u.by}
		for p, c := range u.files {
			if st.Files == nil {
				st.Files = map[string]string{}
			}
			st.Files[p] = c
		}
	}
	if len(io.discarded) > 0 {
		st.Discarded = make(map[string]retain.IOUnit, len(io.discarded))
		for k, u := range io.discarded {
			st.Discarded[k] = u
		}
		st.DiscardedFiles = io.discardedFiles
	}
	if io.provisional != nil {
		p := *io.provisional
		st.Provisional = &p
	}
	st.Seq = io.seq
	if st.Units == nil && st.Discarded == nil && st.Provisional == nil && st.Seq == 0 {
		return nil
	}
	return st
}

// IOCurrent reports whether what runs is what st keeps: the last restore
// made it run, and no apply has changed what runs since (runtime.IOKeeper).
func (io *IOConfig) IOCurrent(st *retain.IOState) bool {
	if st != nil && st.Provisional != nil && io.leads() {
		// The leader's pending apply was not carried: the replica that
		// now leads says so (RestoreIO), and the store forgets it.
		return false
	}
	h := hashOf(st)
	io.mu.Lock()
	defer io.mu.Unlock()
	current := h == io.memo.st && io.gen == io.memo.gen
	if current {
		io.edgeSeen = true // the runtime asks only at an edge
	}
	return current
}

// leads reports whether this replica leads (always, standalone).
func (io *IOConfig) leads() bool { return io.Gate == nil || io.Gate() }

// RestoreIO makes what runs what st keeps, at a leadership edge
// (runtime.IOKeeper): each kept unit whose base is still the deployed
// manifest's, rebuilt from the kept files and checked to hash to what was
// applied, composed with the manifest's units for the rest, checked
// statically against what runs (no live probes: it ran before; a silent
// device reads Bad, as after any apply) and applied, every unit back on
// the origin it was kept with. A unit st does not keep goes back to the
// manifest's (a replica that led before and stood by while the store
// changed). What is dropped is reported (Discarded) and kept in the store
// for recovery by hand. At an edge the store is what the last leader
// left, and it wins.
//
// Every unit it changes is held first, before it waits on anything: not
// polled, its tags Bad, nothing sent (holdUnit). The apply replaces each
// held unit with what the store keeps; any way out short of that (a
// failure, ctx's end, an apply in progress) leaves it held, with Fault
// saying why, until an apply re-establishes what runs (decision 2 on
// #706), and the store's state is kept as it was.
func (io *IOConfig) RestoreIO(ctx context.Context, st *retain.IOState) error {
	return io.restoreFrom(ctx, st, false)
}

// restoreFrom is RestoreIO, or with follow a standby following the store
// (Follow): nothing held (a standby neither scans nor sends), and acting
// only on a store newer than what it runs, while it still stands by, as
// checked again under the apply lock.
func (io *IOConfig) restoreFrom(ctx context.Context, st *retain.IOState, follow bool) error {
	if follow && io.followSkip(st) {
		return nil
	}
	io.mu.Lock()
	boot, running := copyUnits(io.boot), copyUnits(io.running)
	io.restoring = true
	io.mu.Unlock()
	defer func() {
		io.mu.Lock()
		io.restoring = false
		if !follow {
			io.edgeSeen = true
		}
		io.mu.Unlock()
	}()

	r := restore{st: st, discarded: map[string]retain.IOUnit{}}
	if st != nil {
		for k, u := range st.Discarded {
			r.discarded[k] = u
		}
		r.discardedFiles = st.DiscardedFiles
		if p := st.Provisional; p != nil && !follow {
			r.dropped = append(r.dropped, IODiscard{From: p.From, At: p.At, By: p.By,
				Why: fmt.Sprintf("an apply was waiting for its changed devices to answer (revertIfSilent, until %s) when the controller restarted or another took over; it was not carried, and the configuration before it runs", p.Deadline.Format(time.RFC3339))})
		}
	}
	keep, cand := io.restoreUnits(&r, boot)
	target := copyUnits(boot)
	for k, u := range keep {
		target[k] = u
	}
	if !follow {
		io.holdUnits(changedKeys(target, running), running)
	}

	for !io.applying.TryLock() {
		select {
		case <-ctx.Done():
			return io.restoreFailed(&r, target, running, !follow, fmt.Errorf("waiting for an apply in progress: %w", ctx.Err()))
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer io.applying.Unlock()
	if follow && io.followSkip(st) {
		return nil // it leads now, or what it runs is newer: the edge decides
	}
	io.mu.Lock()
	running = copyUnits(io.running)
	io.mu.Unlock()

	var c *IOCandidate
	if !sameHashes(target, running) {
		var err error
		if c, err = io.restoreCandidate(cand, target); err == nil {
			if v := io.validate(c, false); !v.Valid && len(keep) > 0 {
				// Together they are a configuration nothing validated (a
				// deploy moved some of them): none comes back.
				why := "the kept units, with what the deployed manifest says for the rest, fail validation: " + failedChecks(v)
				for _, k := range sortedKeys(keep) {
					r.drop(k, boot[k].hash, why)
				}
				keep, target = nil, copyUnits(boot)
				c = nil
				if !sameHashes(target, running) {
					c, err = io.restoreCandidate(nil, target)
				}
			}
		}
		if err != nil {
			return io.restoreFailed(&r, target, running, !follow, err)
		}
	}
	if c != nil {
		v := io.validate(c, false)
		if !v.Valid {
			return io.restoreFailed(&r, target, running, !follow, errors.New("the configuration to restore fails validation: "+failedChecks(v)))
		}
		io.atStart = !follow && io.p.Redundancy == nil
		_, err := io.apply(ctx, c, "retain", ApplyOptions{})
		io.atStart = false
		if err != nil {
			return io.restoreFailed(&r, target, running, !follow, err)
		}
		io.mu.Lock()
		w := io.watch
		io.mu.Unlock()
		if w != nil {
			w.finish(WatchSuperseded, "the configuration was re-applied from the retain store")
		}
	}
	// A unit held above that the apply did not replace (what runs changed
	// while it waited) goes back to what runs, unheld.
	io.liftHeld(ctx)
	io.settle(&r, target, follow)
	for _, d := range r.dropped {
		slog.Warn("config.io: a kept I/O change was not brought back", "driver", d.Driver, "device", d.Device, "from", d.From, "by", d.By, "why", d.Why)
	}
	if c != nil {
		slog.Info("config.io: kept I/O configuration re-applied", "units", len(keep), "following", follow)
	}
	return nil
}

// followSkip: a follower does nothing while it leads (the edge re-applies,
// and a leader's applies are newer than any store it could read), nor for a
// store no newer than what it runs (a slow read from before an apply).
func (io *IOConfig) followSkip(st *retain.IOState) bool {
	return io.leads() || seqOf(st) <= io.seqNow()
}

func seqOf(st *retain.IOState) uint64 {
	if st == nil {
		return 0
	}
	return st.Seq
}

func (io *IOConfig) seqNow() uint64 {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.seq
}

// changedKeys are the units whose configuration differs between a and b.
func changedKeys(a, b map[string]ioUnit) []string {
	var out []string
	for _, k := range sortedKeys(a) {
		if a[k].hash != b[k].hash {
			out = append(out, k)
		}
	}
	return out
}

// restore is one RestoreIO's bookkeeping.
type restore struct {
	st             *retain.IOState
	dropped        []IODiscard
	discarded      map[string]retain.IOUnit
	discardedFiles map[string]string
}

func (r *restore) drop(k string, manifest, why string) {
	ru := r.st.Units[k]
	driver, device, _ := strings.Cut(k, "/")
	r.dropped = append(r.dropped, IODiscard{Driver: driver, Device: device, Base: ru.Base, Manifest: manifest,
		From: ru.From, At: ru.At, By: ru.By, Why: why})
	ru.Why = why
	r.discarded[k] = ru
	r.discardedFiles = r.st.Files
}

// restoreUnits applies the base-hash rule to st's units: those that come
// back, rebuilt from the kept files, and the candidate they were read from.
func (io *IOConfig) restoreUnits(r *restore, boot map[string]ioUnit) (map[string]ioUnit, *IOCandidate) {
	st := r.st
	if st == nil || len(st.Units) == 0 {
		return nil, nil
	}
	files := make(map[string][]byte, len(st.Files))
	for p, b := range st.Files {
		files[p] = []byte(b)
	}
	cand := io.Stage(OverlayFS(io.bootFS, files), "retain", "")
	keep := map[string]ioUnit{}
	for _, k := range sortedKeys(st.Units) {
		ru := st.Units[k]
		b, ok := boot[k]
		switch {
		case !ok:
			r.drop(k, "", "the deployed manifest no longer has it")
		case b.hash == ru.Hash:
			// Committed and deployed: the build already says it, so there
			// is nothing to keep (and nothing differs any more).
			slog.Info("config.io: a kept I/O change is in this build; clearing it", "unit", k)
		case b.hash != ru.Base:
			r.drop(k, b.hash, "the deployed manifest changed it since it was applied: a deploy replaces a live change (pull the kept files with naut pull --io before deploying to keep it)")
		case cand.err != nil:
			r.drop(k, b.hash, "the kept files no longer load: "+cand.err.Error())
		default:
			cu, ok := cand.units[k]
			if !ok || cu.hash != ru.Hash {
				r.drop(k, b.hash, "the kept files do not rebuild what was applied")
				continue
			}
			cu.from, cu.at, cu.by = ru.From, ru.At, ru.By
			keep[k] = cu
		}
	}
	// A Modbus driver's own settings swap it whole, with every device it
	// has: not over a device that did not come back.
	for _, k := range sortedKeys(keep) {
		u := keep[k]
		if u.device != "" {
			continue
		}
		for _, d := range r.dropped {
			if d.Driver == u.driver && d.Device != "" {
				delete(keep, k)
				r.drop(k, boot[k].hash, fmt.Sprintf("device %s of the driver did not come back, and a change to the driver's own settings swaps it with all its devices", d.Device))
				break
			}
		}
	}
	return keep, cand
}

// restoreCandidate is the candidate a restore applies: target's units, with
// cand's manifest and built drivers (the kept files'), or the boot files'
// when nothing kept comes back.
func (io *IOConfig) restoreCandidate(cand *IOCandidate, target map[string]ioUnit) (*IOCandidate, error) {
	if cand == nil || cand.err != nil {
		cand = io.Stage(io.bootFS, "manifest", "")
		if cand.err != nil {
			return nil, fmt.Errorf("the deployed manifest does not load: %w", cand.err)
		}
	}
	return &IOCandidate{From: "retain", restore: true, fsys: cand.fsys, m: cand.m, configs: cand.configs,
		built: cand.built, units: target}, nil
}

// restoreFailed holds every unit target changes (again: a failed apply
// put back what it had replaced, unheld), unless hold is false (a standby
// following: its edge re-applies), marks those units' configuration not
// known (Fault), and still records what the store keeps: a leader's next
// save must not drop it.
func (io *IOConfig) restoreFailed(r *restore, target, running map[string]ioUnit, hold bool, err error) error {
	why := "the kept I/O configuration was not re-applied (" + err.Error() + ")"
	lost := changedKeys(target, running)
	if hold {
		why += ": what it changes is held, reading Bad and sending nothing, until an apply re-establishes it, or a restart"
		io.holdUnits(lost, running)
		// A held driver's devices are held with it.
		for _, k := range lost {
			if u := target[k]; u.device == "" {
				for kk, uu := range running {
					if uu.driver == u.driver && !slices.Contains(lost, kk) {
						lost = append(lost, kk)
					}
				}
			}
		}
	}
	io.markUnknown(lost, "retain", why)
	io.mu.Lock()
	io.kept = copyUnits(target)
	io.provisional = nil
	io.discarded, io.discardedFiles, io.dropped = r.discarded, r.discardedFiles, r.dropped
	io.mu.Unlock()
	slog.Error("config.io: "+why, "units", lost)
	return errors.New(why)
}

// settle records a restore that made target run. A follower takes the
// store's sequence; a leader's edge moves past it, so what it saves next is
// newer than anything a standby followed.
func (io *IOConfig) settle(r *restore, target map[string]ioUnit, follow bool) {
	io.mu.Lock()
	defer io.mu.Unlock()
	io.kept = copyUnits(target)
	io.provisional = nil
	io.discarded, io.discardedFiles, io.dropped = r.discarded, r.discardedFiles, r.dropped
	if len(io.discarded) == 0 {
		io.discarded, io.discardedFiles = nil, nil
	}
	io.memo.st, io.memo.gen = hashOf(r.st), io.gen
	if follow {
		io.seq = seqOf(r.st)
	} else {
		io.seq = max(io.seq, seqOf(r.st)) + 1
	}
}

func sameHashes(a, b map[string]ioUnit) bool {
	if len(a) != len(b) {
		return false
	}
	for k, u := range a {
		if b[k].hash != u.hash {
			return false
		}
	}
	return true
}

func failedChecks(v *IOValidation) string {
	var out []string
	for _, ck := range v.Checks {
		if !ck.OK {
			out = append(out, ck.Name+": "+ck.Detail)
		}
	}
	return strings.Join(out, "; ")
}

// heldDriver stands in for a driver a restore could not re-apply: the tags
// of the driver it replaced (which is stopped), every input Bad, nothing
// read and nothing sent.
type heldDriver struct{ inner nio.Driver }

func (h *heldDriver) names() (in, out []string) {
	if t, ok := h.inner.(tagNames); ok {
		return t.InputNames(), t.OutputNames()
	}
	return nil, nil
}
func (h *heldDriver) InputNames() []string            { in, _ := h.names(); return in }
func (h *heldDriver) OutputNames() []string           { _, out := h.names(); return out }
func (h *heldDriver) ReadInputs() (nio.Values, error) { return nio.Values{}, nil }
func (h *heldDriver) WriteOutputs(nio.Values) error   { return nil }
func (h *heldDriver) InputType(name string) string {
	if t, ok := h.inner.(inputTyper); ok {
		return t.InputType(name)
	}
	return ""
}
func (h *heldDriver) Quality() map[string]nio.Quality {
	out := map[string]nio.Quality{}
	for _, n := range h.InputNames() {
		out[n] = nio.Bad
	}
	return out
}

// holdUnits holds each unit of keys as it runs (running[k]): a Modbus
// device is replaced by itself, held (modbus.ReplaceOptions.Held); a driver
// by a heldDriver. Best effort: a unit it cannot hold is logged.
func (io *IOConfig) holdUnits(keys []string, running map[string]ioUnit) {
	drivers := map[string]bool{}
	for _, k := range keys {
		if u := running[k]; u.device == "" {
			drivers[u.driver] = true
		}
	}
	for _, k := range keys {
		u := running[k]
		if drivers[u.driver] && u.device != "" {
			continue // held with its driver
		}
		var err error
		if u.device != "" {
			if mb := modbusOf(io.currentDriver(u.driver)); mb != nil {
				err = io.replaceSource(mb, u.src, u.tags, modbus.ReplaceOptions{Held: true})
			}
		} else if d := io.currentDriver(u.driver); d != nil {
			if _, held := d.(*heldDriver); !held {
				ctx, cancel := context.WithTimeout(context.Background(), undoTimeout)
				var prev nio.Driver
				prev, err = io.rt.ReplaceDriver(ctx, u.driver, &heldDriver{inner: d})
				cancel()
				if err == nil {
					stopDriver(prev)
				}
			}
		}
		if err != nil {
			slog.Error("config.io: could not hold a unit", "unit", k, "err", err)
			continue
		}
		io.mu.Lock()
		if io.held == nil {
			io.held = map[string]bool{}
		}
		io.held[k] = true
		io.mu.Unlock()
	}
}

// liftHeld gives each held unit that an apply has not replaced since back
// what runs there, unheld; one an apply replaced is unheld already.
func (io *IOConfig) liftHeld(ctx context.Context) {
	io.mu.Lock()
	held, running := io.held, copyUnits(io.running)
	io.held = nil
	io.mu.Unlock()
	for _, k := range sortedKeys(held) {
		u := running[k]
		if u.hash == unknownHash {
			continue
		}
		switch d := io.currentDriver(u.driver).(type) {
		case *heldDriver:
			if _, err := io.swapDriver(ctx, u.driver, u.fsys, u.cfg); err != nil {
				slog.Error("config.io: could not release a held driver", "driver", u.driver, "err", err)
			}
		default:
			if u.device == "" {
				continue
			}
			if mb := modbusOf(d); mb != nil && mb.Held(u.device) {
				if err := io.replaceSource(mb, u.src, u.tags, modbus.ReplaceOptions{}); err != nil {
					slog.Error("config.io: could not release a held device", "unit", k, "err", err)
				}
			}
		}
	}
}

// Follow keeps a standby's I/O configuration what the store keeps while it
// stands by (its drivers run with their write gate closed, and it does not
// scan, so nothing is commanded): every followInterval, until ctx ends,
// while leading says it does not lead. The leadership edge then finds what
// runs current and re-applies nothing (runtime.IOKeeper.IOCurrent), so a
// takeover holds no longer than it did before #706 PR 5.
func (io *IOConfig) Follow(ctx context.Context, load func() (retain.State, error), leading func() bool) {
	t := time.NewTicker(followInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if leading() {
			continue
		}
		st, err := load()
		if err != nil || seqOf(st.IO) <= io.seqNow() {
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := io.restoreFrom(rctx, st.IO, true); err != nil {
			slog.Warn("config.io: the standby could not follow the kept I/O configuration; its takeover re-applies it", "err", err)
		}
		cancel()
	}
}
