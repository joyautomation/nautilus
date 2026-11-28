//go:build !js

package oplog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

// Carrying out a compaction (compact.go): the checks against this copy's
// log, the background rewrite of its file, and the leader's seam that
// proposes one. Not in the browser playground's build: its node is
// standalone and in memory, so it never sees one (compact_worker_js.go).

// compactDelay is the wait before a physical compaction: random up to a
// minute, so the three copies don't fsync at once.
func (n *Node) compactDelay() time.Duration {
	if n.o.lim.compactDelay != nil {
		return n.o.lim.compactDelay()
	}
	return rand.N(time.Minute)
}

// compactLoop rewrites the file to each new base, one at a time, until it
// is closed.
func (n *Node) compactLoop() {
	defer n.worker.Done()
	retry := n.o.lim.compactRetry
	if retry <= 0 {
		retry = time.Hour
	}
	wait := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-n.done:
			return false
		case <-t.C:
			return true
		}
	}
	for {
		select {
		case <-n.done:
			return
		case <-n.kick:
		}
		for {
			if !wait(n.compactDelay()) {
				return
			}
			done, err := n.compactOnce()
			if err != nil {
				n.log.Warn("oplog: compacting the log file", "error", err)
				if !wait(retry) {
					return
				}
				continue
			}
			if done {
				break
			}
		}
	}
}

// crashAt is the tests' crash point: true stops the compaction there, with
// nothing cleaned up (a temp file closed, never removed).
func (n *Node) crashAt(step string) bool {
	return n.o.compactCrash != nil && n.o.compactCrash(step)
}

// errCrashed: a test's crash point stopped the compaction.
var errCrashed = errors.New("oplog: crashed (test)")

// compactOnce rewrites the file to the newest committed base it has not
// written yet. done: nothing more to write (or the copy stopped or closed).
//
// The new file is built outside mu from the entries up to the commit index
// as they were (committed entries never change); under mu only the lines
// written since are added (the generation counter says no rewrite replaced
// the file meanwhile, else it is built again), then it is renamed in,
// memory swapped, and the mark records the base. A crash anywhere leaves
// the old file (logically compacted, not physically: the next start carries
// out the committed K again) or the new one.
func (n *Node) compactOnce() (done bool, err error) {
	n.mu.Lock()
	if n.closed || n.stoppedLocked() || n.cut.Compact <= n.disk.Compact {
		n.mu.Unlock()
		return true, nil
	}
	target := n.cut
	es, commit, gen, hist := n.entries, n.commit, n.gen, n.mark.Lineage
	path := n.o.File
	n.mu.Unlock()

	controller := path != "" && !n.o.ResetInPlace
	if controller && !mutOplog("compact_disk_unchecked") {
		if fi, err := os.Stat(path); err == nil {
			free, ferr := n.freeDisk(filepath.Dir(path))
			if ferr == nil && free >= 0 && free < 2*fi.Size() {
				why := fmt.Sprintf("%s: %d MB free, the log is %d MB: the compaction waits for at least %d MB free", filepath.Dir(path), free>>20, fi.Size()>>20, 2*fi.Size()>>20)
				n.mu.Lock()
				n.compactRefused = why
				n.mu.Unlock()
				return false, errors.New("oplog: too little free disk to compact: " + why)
			}
		}
	}

	committed := es[:sort.Search(len(es), func(i int) bool { return es[i].Seq > commit })]
	next := make([]stored, 0, len(target.Kept)+len(committed))
	k := 0
	for _, s := range committed {
		if s.Seq <= target.Through {
			if k < len(target.Kept) && target.Kept[k] == s.Seq {
				next = append(next, s)
				k++
			}
			continue
		}
		next = append(next, s)
	}
	if k != len(target.Kept) {
		return true, fmt.Errorf("oplog: %d of the base's %d kept entries are in memory", k, len(target.Kept))
	}

	var tmp *os.File
	tmpName := ""
	if path != "" {
		tmp, err = os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.compact")
		if err != nil {
			return false, err
		}
		tmpName = tmp.Name()
		w := bufio.NewWriter(tmp)
		w.Write(appendLine(nil, fileLine{B: &baseHeader{Why: compactWhy, Through: target.Through, ThroughTerm: target.ThroughTerm,
			Compact: target.Compact, Kept: len(target.Kept), Digest: target.Digest, Rules: target.Rules, Lineage: hist}}))
		var line []byte
		for _, s := range next {
			line = s.line(line[:0])
			w.Write(line)
		}
		c := commit
		w.Write(appendLine(nil, fileLine{C: &c}))
		if err := w.Flush(); err != nil {
			tmp.Close()
			os.Remove(tmpName)
			return false, err
		}
		if n.crashAt("written") {
			tmp.Close()
			return true, errCrashed
		}
		if err := n.syncFile(tmp); err != nil {
			tmp.Close()
			os.Remove(tmpName)
			return false, err
		}
		if n.crashAt("synced") {
			tmp.Close()
			return true, errCrashed
		}
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	drop := func() {
		if tmp != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}
	if n.closed || n.stoppedLocked() {
		drop()
		return true, nil
	}
	if n.gen != gen && !mutOplog("compact_gen_unchecked") {
		drop() // the file was rewritten meanwhile: build again
		return false, nil
	}
	tail := n.entries[n.indexAfterLocked(commit):]
	if tmp != nil {
		var buf []byte
		for _, s := range tail {
			buf = s.line(buf)
		}
		c := n.commit
		buf = appendLine(buf, fileLine{C: &c})
		if _, err := tmp.Write(buf); err != nil {
			drop()
			return false, err
		}
		if err := n.syncFile(tmp); err != nil {
			drop()
			return false, err
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmpName)
			return false, err
		}
		if n.crashAt("delta") {
			return true, errCrashed
		}
		pre := ""
		if controller {
			// A link, never a rename of the live file: under a held mark a
			// missing file is a lost log. A copy that can't link keeps none.
			pre = fmt.Sprintf("%s.precompact-%d", path, n.o.Now().UnixNano())
			if mutOplog("compact_precompact_renamed") {
				n.file.Close()
				err = os.Rename(path, pre)
			} else {
				err = os.Link(path, pre)
			}
			if err != nil {
				n.log.Warn("oplog: can't keep the log from before the compaction", "error", err)
				pre = ""
			}
			if n.crashAt("precompact") {
				return true, errCrashed
			}
		}
		// Closed before the rename (Windows refuses to replace an open
		// file, as rewrite).
		n.file.Close()
		if err := os.Rename(tmpName, path); err != nil {
			os.Remove(tmpName)
			if f, oerr := os.OpenFile(path, os.O_RDWR, 0o600); oerr == nil {
				f.Seek(0, io.SeekEnd)
				n.file = f
			}
			if pre != "" {
				os.Remove(pre)
			}
			return false, err
		}
		if n.crashAt("renamed") {
			return true, errCrashed
		}
		syncDir(filepath.Dir(path))
		if n.crashAt("dirsynced") {
			return true, errCrashed
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0o600)
		if err != nil {
			return true, fmt.Errorf("%w: %w", errRewritten, err)
		}
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			f.Close()
			return true, err
		}
		n.file = f
		if pre != "" {
			olds, _ := filepath.Glob(path + ".precompact-*")
			for _, o := range olds {
				if o != pre {
					os.Remove(o)
				}
			}
		}
	}
	n.entries = append(next, tail...)
	n.disk = target
	n.gen++
	n.compactRefused = ""
	if n.o.Mark != "" {
		m := n.mark
		m.Base = &MarkBase{Through: target.Through, Digest: target.Digest}
		if err := n.setMarkLocked(m); err != nil {
			n.log.Warn("oplog: can't record the compacted base in the log's mark", "error", err)
		}
	}
	n.log.Info("oplog: the log file is compacted", "through", target.Through, "kept", len(target.Kept), "entries", len(n.entries))
	return n.cut.Compact <= n.disk.Compact, nil
}

// syncFile syncs a file through the tests' hook.
func (n *Node) syncFile(f *os.File) error {
	if n.o.fileSync != nil {
		return n.o.fileSync(f)
	}
	return f.Sync()
}

// freeDisk is the free space where the log lives, through the tests' hook
// (-1: unknown).
func (n *Node) freeDisk(dir string) (int64, error) {
	if n.o.freeDisk != nil {
		return n.o.freeDisk(dir)
	}
	return diskFree(dir)
}

// runs encodes sorted, distinct seqs as [first, last] runs.
func runs(seqs []uint64) [][2]uint64 {
	var out [][2]uint64
	for _, s := range seqs {
		if k := len(out); k > 0 && out[k-1][1]+1 == s {
			out[k-1][1] = s
			continue
		}
		out = append(out, [2]uint64{s, s})
	}
	return out
}

// unruns decodes runs, refusing ones out of order or overlapping, and more
// than limit seqs (a body can't name more entries than the log holds).
func unruns(rs [][2]uint64, limit int) ([]uint64, error) {
	var out []uint64
	var prev uint64
	for i, r := range rs {
		if r[0] > r[1] || i > 0 && r[0] <= prev {
			return nil, fmt.Errorf("runs out of order at %v", r)
		}
		if r[1]-r[0] >= uint64(limit-len(out)) {
			return nil, fmt.Errorf("runs name more seqs than the log holds")
		}
		for s := r[0]; ; s++ {
			out = append(out, s)
			if s == r[1] {
				break
			}
		}
		prev = r[1]
	}
	return out, nil
}

// carryOutLocked carries out a committed K logically: the copy holds the
// new base (cut) at once, checked against its own log, and the background
// worker rewrites the file. A K already carried out is skipped. One that
// does not fit this log (a digest, a count, a term or the previous base
// differs) means this copy's log is not the others': it has diverged.
// Caller holds mu.
func (n *Node) carryOutLocked(e Entry) {
	if e.Seq <= n.cut.Compact {
		return
	}
	var b compactBody
	if err := json.Unmarshal(e.Value, &b); err != nil || b.V != 1 {
		n.divergeLocked(e.Seq, fmt.Sprintf("its body can't be read (version %d, %v)", b.V, err))
		return
	}
	next, why := n.nextBaseLocked(e.Seq, b)
	if why != "" {
		n.divergeLocked(e.Seq, why)
		return
	}
	n.cut = next
	n.kickCompact()
}

// nextBaseLocked is the base K (at seq, body b) makes of this copy's
// current one, or why it does not fit. Caller holds mu.
func (n *Node) nextBaseLocked(seq uint64, b compactBody) (baseState, string) {
	cur := n.cut
	switch {
	case b.PrevThrough != cur.Through:
		return baseState{}, fmt.Sprintf("it follows a base at %d, this copy's is at %d", b.PrevThrough, cur.Through)
	case b.Through <= b.PrevThrough || b.Through >= seq:
		return baseState{}, fmt.Sprintf("its range (%d, %d] at seq %d is not one", b.PrevThrough, b.Through, seq)
	case n.termAtLocked(b.Through) != b.ThroughTerm:
		return baseState{}, fmt.Sprintf("its Through %d is at term %d here, %d in it", b.Through, n.termAtLocked(b.Through), b.ThroughTerm)
	}
	limit := len(n.entries) + 1
	drop, err := unruns(b.Drop, limit)
	if err != nil {
		return baseState{}, "its drop list: " + err.Error()
	}
	keep, err := unruns(b.Keep, limit)
	if err != nil {
		return baseState{}, "its keep list: " + err.Error()
	}
	kept := make([]uint64, 0, len(cur.Kept)+len(keep))
	d := 0
	for _, s := range cur.Kept {
		if d < len(drop) && drop[d] == s {
			d++
			continue
		}
		kept = append(kept, s)
	}
	if d != len(drop) {
		return baseState{}, "it drops entries this copy's base does not keep"
	}
	for _, s := range keep {
		if s <= b.PrevThrough || s > b.Through {
			return baseState{}, fmt.Sprintf("it keeps seq %d outside its range", s)
		}
		if n.termAtLocked(s) == 0 {
			return baseState{}, fmt.Sprintf("it keeps seq %d, which this copy does not hold", s)
		}
		kept = append(kept, s)
	}
	if len(kept) != b.Kept {
		return baseState{}, fmt.Sprintf("it keeps %d entries, this copy would keep %d", b.Kept, len(kept))
	}
	if dg := pairDigest(kept, n.termLocked); dg != b.Digest && !mutOplog("compact_digest_unchecked") {
		return baseState{}, fmt.Sprintf("its digest %.12s is %.12s over this copy's log", b.Digest, dg)
	}
	return baseState{Through: b.Through, ThroughTerm: b.ThroughTerm, Compact: seq, Kept: kept, Digest: b.Digest, Rules: b.Rules}, ""
}

// resetDivergedLocked moves a diverged log aside now and starts empty
// (ResetInPlace: the witness, which applies nothing). Caller holds mu.
func (n *Node) resetDivergedLocked() error {
	if n.file != nil {
		n.file.Close()
	}
	n.file, n.entries, n.commit, n.syncedTerm = nil, nil, 0, 0
	kept, err := n.moveAside(".diverged-")
	if err != nil {
		return err
	}
	m := n.mark
	nm := Mark{Identity: m.Identity, Term: m.Term, Lineage: m.Lineage, Lost: cmp(kept, "(missing)"), Superseded: m.Superseded}
	if err := n.setMarkLocked(nm); err != nil {
		return err
	}
	n.resets++
	_, err = n.open()
	return err
}

// proposeCompact sequences a K: the entries up to through that are not in
// keep (sorted, distinct, every one held and at or below through) are
// dropped. The decision is outside this package (#1455 PR 2b); this is its
// seam, checked here against the log as it stands: the leader caught up for
// its term, through committed and above the current base, the previous
// base's kept entries kept or named as dropped, and no KindLapse above
// through naming an entry at or below it (so a lapse is never split from
// the entries it names). Off unless the node's rules version is set.
func (n *Node) proposeCompact(ctx context.Context, through uint64, keep []uint64) (Entry, error) {
	if n.o.compactRules == 0 {
		return Entry{}, errCompactOff
	}
	n.seqMu.Lock()
	defer n.seqMu.Unlock()
	if !n.isLeader() {
		return Entry{}, fmt.Errorf("%w: not the leader", ErrReadOnly)
	}
	term := n.currentTerm()
	n.mu.Lock()
	e, err := n.compactEntryLocked(term, through, keep)
	n.mu.Unlock()
	if err != nil {
		return Entry{}, err
	}
	if err := checkSize(e); err != nil {
		return Entry{}, fmt.Errorf("%w: compact a smaller range", err)
	}
	return n.sequenceLocked(ctx, e)
}

// compactEntryLocked builds K for proposeCompact. Caller holds seqMu and mu.
func (n *Node) compactEntryLocked(term, through uint64, keep []uint64) (Entry, error) {
	cur := n.cut
	switch {
	case n.stoppedLocked():
		return Entry{}, n.stopErrLocked()
	case n.syncedTerm != term || !n.syncedLocked():
		return Entry{}, errTermNotSynced
	case through <= cur.Through:
		return Entry{}, fmt.Errorf("oplog: compact through %d: the base is already at %d", through, cur.Through)
	case through > n.commit:
		return Entry{}, fmt.Errorf("oplog: compact through %d: only %d is committed", through, n.commit)
	case n.lastLocked() > n.commit && !mutOplog("compact_over_uncommitted"):
		// An earlier K may be among them: this one would follow the base
		// from before it, and every copy that commits both diverges
		// (Fable, 2a-1 review). The next write commits them first.
		return Entry{}, fmt.Errorf("oplog: entries up to %d are not committed yet: compact after they are", n.lastLocked())
	case n.termAtLocked(through) == 0:
		return Entry{}, fmt.Errorf("oplog: compact through %d: no entry there", through)
	case cur.Rules > n.o.compactRules:
		return Entry{}, fmt.Errorf("oplog: the log was compacted under keep rules %d, this build's are %d", cur.Rules, n.o.compactRules)
	}
	if !slices.IsSorted(keep) || len(slices.Compact(slices.Clone(keep))) != len(keep) {
		return Entry{}, errors.New("oplog: the keep list is not sorted and distinct")
	}
	for _, s := range n.entries[n.indexAfterLocked(through):] {
		if !s.mayLapse() {
			continue
		}
		if l := s.entry(); l.Kind == KindLapse && !mutOplog("compact_splits_lapse") {
			for _, r := range l.Refs {
				if r <= through {
					return Entry{}, fmt.Errorf("oplog: compact through %d: the lapse at %d names %d", through, l.Seq, r)
				}
			}
		}
	}
	// Nor may a kept entry lose the lapse that names it: replayed without
	// it, the entry would be carried out (KindLapse). The decision (PR 2b)
	// keeps them together by its closure rule; the seam refuses one that
	// does not.
	old := map[uint64]bool{}
	for _, s := range cur.Kept {
		old[s] = true
	}
	kept := map[uint64]bool{}
	for _, s := range keep {
		kept[s] = true
	}
	for _, s := range n.entries[:n.indexAfterLocked(through)] {
		if s.Seq <= cur.Through && !old[s.Seq] || kept[s.Seq] || !s.mayLapse() {
			continue
		}
		if l := s.entry(); l.Kind == KindLapse && !mutOplog("compact_drops_named_lapse") {
			for _, r := range l.Refs {
				if kept[r] {
					return Entry{}, fmt.Errorf("oplog: the keep list keeps %d but drops the lapse at %d that names it", r, l.Seq)
				}
			}
		}
	}
	var fresh, drop []uint64
	inKeep := map[uint64]bool{}
	for _, s := range keep {
		inKeep[s] = true
		switch {
		case s > through:
			return Entry{}, fmt.Errorf("oplog: the keep list names %d, past %d", s, through)
		case s <= cur.Through && !old[s]:
			return Entry{}, fmt.Errorf("oplog: the keep list names %d, which an earlier compaction dropped", s)
		case n.termAtLocked(s) == 0:
			return Entry{}, fmt.Errorf("oplog: the keep list names %d, which this log does not hold", s)
		case s > cur.Through:
			fresh = append(fresh, s)
		}
	}
	for _, s := range cur.Kept {
		if !inKeep[s] {
			drop = append(drop, s)
		}
	}
	b := compactBody{V: 1, Rules: n.o.compactRules, Through: through, ThroughTerm: n.termAtLocked(through), PrevThrough: cur.Through,
		Keep: runs(fresh), Drop: runs(drop), Kept: len(keep), Digest: pairDigest(keep, n.termLocked)}
	v, _ := json.Marshal(b)
	return Entry{Kind: KindCompact, By: "system", Origin: n.o.Self, Value: v}, nil
}
