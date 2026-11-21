package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// An anchor is the journal's head (a Seq and its hash) kept where the
// controller's service user cannot rewrite it: today a row the Postgres
// copy writes (#709 PR 9b, docs/design/identity-audit.md §4). With the
// copy's rows, which still hold what the journal has pruned, it lets
// VerifyAnchored find what the chain alone cannot: lines cut from the end,
// lines rewritten under a recomputed chain, and a forged audit.prune.

// Anchor is one anchor row.
type Anchor struct {
	Seq    uint64
	Hash   string
	At     time.Time // the database's clock, never the controller's
	Reason string    // start, interval, prune, stop
	Writer string    // the database role that wrote it
}

// CopyRow is one row of the copy as verify reads it. Line is the stored
// line; what it says is read from it, never from the copy's other columns.
type CopyRow struct {
	Seq      uint64
	Hash     string
	File     string
	CopiedAt time.Time // the database's clock
	Line     []byte
}

// WriterCheck is whether an anchor's writer role could rewrite the copy or
// the anchors (a superuser, a table owner, UPDATE/DELETE/TRUNCATE, or
// INSERT into a column whose default is the evidence), and why.
type WriterCheck struct {
	Protected bool
	Why       string
}

// AnchorInput is what VerifyAnchored compares.
type AnchorInput struct {
	Dir string // the journal's directory
	// Copy returns the copy's rows one at a time in Seq order; ok false
	// at the end.
	Copy    func() (row CopyRow, ok bool, err error)
	Gaps    []Gap
	Anchors []Anchor
	// Writers holds the check of every role that wrote an anchor; a
	// writer missing from it is unprotected.
	Writers map[string]WriterCheck
	// ClockSkew is the tolerance between the controller's clock (entry
	// times) and the database's (default 5 minutes).
	ClockSkew time.Duration
	// Controller is the controller's own database role and its check:
	// an anchor's writer column is only as good as the roles that could
	// rewrite it, and the controller's is the one that matters. Nil: not
	// known, which is never verified.
	Controller *RoleCheck
}

// RoleCheck is a database role and whether it could rewrite the evidence.
type RoleCheck struct {
	Role string
	WriterCheck
	// FromConnection: the role is the connection's, taken as the
	// controller's because verify used the controller's connection
	// string.
	FromConnection bool
}

// Verdicts.
const (
	Verified    = "verified"
	Unprotected = "UNPROTECTED"
	Broken      = "BROKEN"
)

// MinKeep is the shortest retention naut accepts (audit.keep-days is whole
// days): a prune by less is not one naut would write.
const MinKeep = 24 * time.Hour

// AnchorReport is what VerifyAnchored found.
type AnchorReport struct {
	Chain Report // the journal's own chain walk
	// JournalFiles, JournalLines and JournalHead are the journal as read
	// line by line against the copy: every file, every entry, and the
	// last entry's seq. Unlike Chain's counts they do not stop where the
	// chain breaks, so a BROKEN report still says how far the journal goes.
	// Lines the walk skips (not an entry, or a seq not above the one
	// before) are not counted.
	JournalFiles int
	JournalLines int
	JournalHead  uint64
	Verdict      string
	Problems     []string // each one makes the verdict BROKEN
	// Warnings are printed whatever the verdict: every lowered
	// retention, prunes that predate 9b, a lowering pruned unanchored.
	Warnings []string
	// Unprotected names each anchor writer that could rewrite the
	// evidence, and why.
	Unprotected []string
	Notes       []string
	Anchors     int
	CopyRows    int
	Compared    int // lines both the journal and the copy hold, compared hash for hash
	// NewestAnchor is the newest anchor's Seq; FirstAnchor the first
	// anchor's time.
	NewestAnchor uint64
	FirstAnchor  time.Time
}

func (r *AnchorReport) problem(format string, a ...any) {
	if len(r.Problems) == maxProblems {
		r.Problems = append(r.Problems, "… further problems not listed")
	}
	if len(r.Problems) > maxProblems {
		return
	}
	r.Problems = append(r.Problems, fmt.Sprintf(format, a...))
}

const maxProblems = 50

// seen is one line of the merged stream: the journal's line, the copy's,
// or both.
type seen struct {
	seq      uint64
	e        Entry
	file     string // the journal file it is in (the copy's when only there)
	copied   bool
	copiedAt time.Time
}

type pruneRec struct {
	seen
	hasKeep    bool
	keep       int64
	last       string
	lastSeq    uint64
	unanchored bool
	prevTS     time.Time
	havePrev   bool
}

type retentionRec struct {
	seq  uint64
	keep int64
	ts   time.Time
	by   string
}

// fileRange is the Seqs the copy holds from one journal file, and the hash
// of its last.
type fileRange struct {
	min, max uint64
	lastHash string
}

// VerifyAnchored checks the journal in.Dir against its copy and anchors:
//
//   - the journal's own chain (Verify);
//   - each anchor's line, in the journal or (for a pruned line) the copy,
//     hashes as the anchor says, and the journal reaches the newest anchor;
//   - every line both hold is the same line, and the copy holds nothing
//     past the journal's head;
//   - every hole in the copy is a recorded gap, and every gap is explained
//     by the audit.prune naming its last missing line (else UNEXPLAINED
//     GAP);
//   - every audit.prune is one naut would have written: its keep is the
//     journalled retention's, at least MinKeep, not lowered without the
//     lowering anchored first; its time within the clock skew of the line
//     before it and of the database's clock; and every file it covers old
//     enough to prune by that keep and time (else PRUNED EARLY);
//   - every anchor's writer could not have rewritten the evidence (else
//     UNPROTECTED).
//
// Nothing in a line is trusted for time beyond ClockSkew: the database's
// clock (an anchor's At, a row's CopiedAt) bounds it.
func VerifyAnchored(in AnchorInput) (AnchorReport, error) {
	r := AnchorReport{Anchors: len(in.Anchors)}
	skew := in.ClockSkew
	if skew <= 0 {
		skew = 5 * time.Minute
	}
	chain, err := Verify(in.Dir)
	if err != nil {
		return r, err
	}
	r.Chain = chain
	if !chain.OK() {
		r.problem("the journal's chain breaks at %v", chain.Break)
	}

	anchors := append([]Anchor(nil), in.Anchors...)
	sort.SliceStable(anchors, func(a, b int) bool {
		if !anchors[a].At.Equal(anchors[b].At) {
			return anchors[a].At.Before(anchors[b].At)
		}
		return anchors[a].Seq < anchors[b].Seq
	})
	type hashes struct{ file, copy string }
	atAnchor := map[uint64]*hashes{}
	for _, a := range anchors {
		atAnchor[a.Seq] = &hashes{}
		if a.Seq > r.NewestAnchor {
			r.NewestAnchor = a.Seq
		}
	}

	files, err := openLines(in.Dir)
	if err != nil {
		return r, err
	}
	gapAt := map[uint64]Gap{} // by AfterSeq
	for _, g := range in.Gaps {
		gapAt[g.AfterSeq] = g
	}

	var (
		prunes     []pruneRec
		retentions []retentionRec
		ranges     = map[string]*fileRange{}
		prevTS     time.Time
		havePrevTS bool
		lastCopy   uint64
		// beforeAnchors counts entries dated before the first anchor.
		beforeAnchors int
		lastHash      string // the hash of the copy's row at lastCopy
		beforeFiles   struct{ n, lo, hi uint64 }
		pastHead      struct{ n, lo, hi uint64 }
		// fileHead is the journal's last line read so far (its head once
		// the files are read): the chain walk stops at a break, this
		// does not.
		fileHead uint64
	)
	r.JournalFiles = len(files.files)
	fl, fok, err := files.next()
	if err != nil {
		return r, err
	}
	cr, cok, err := nextRow(in.Copy)
	if err != nil {
		return r, err
	}
	for fok || cok {
		if cok && cr.Seq == 0 {
			// No journal writes a seq below 1: a row put in by hand
			// (sorted first, so it is at the start).
			r.problem("the copy holds a row at a seq below 1, which no journal writes: rows were put into the copy by hand")
			if cr, cok, err = nextRow(in.Copy); err != nil {
				return r, err
			}
			continue
		}
		var s seen
		var fLine *Line
		var row *CopyRow
		switch {
		case fok && (!cok || fl.Seq < cr.Seq):
			fLine = &fl
		case cok && (!fok || cr.Seq < fl.Seq):
			row = &cr
		default:
			fLine, row = &fl, &cr
		}
		if row != nil {
			r.CopyRows++
			var e Entry
			if h := lineHash(row.Line); h != row.Hash {
				r.problem("the copy's row for seq %d holds a line that does not hash as the row says (%s, line %s)", row.Seq, row.Hash, h)
			} else if err := json.Unmarshal(row.Line, &e); err != nil || e.Seq != row.Seq {
				r.problem("the copy's row for seq %d does not hold that entry", row.Seq)
			}
			if lastCopy > 0 && row.Seq > lastCopy+1 {
				if g, ok := gapAt[lastCopy]; !ok || g.NextSeq != row.Seq {
					r.problem("the copy is missing seq %d to %d and no gap records it: rows were removed from the copy", lastCopy+1, row.Seq-1)
				}
			}
			// The copy's own chain: a row next to another chains on from
			// it, as the tailer checked when it copied them.
			if lastCopy > 0 && row.Seq == lastCopy+1 && e.Prev != lastHash {
				r.problem("the copy's row for seq %d does not chain on from its row for seq %d: rows were put into the copy by hand", row.Seq, lastCopy)
			}
			lastCopy, lastHash = row.Seq, row.Hash
			fr := ranges[row.File]
			if fr == nil {
				fr = &fileRange{min: row.Seq}
				ranges[row.File] = fr
			}
			fr.max, fr.lastHash = row.Seq, row.Hash
			if h := atAnchor[row.Seq]; h != nil {
				h.copy = row.Hash
			}
			if fLine == nil {
				switch {
				case !fok:
					// The files ended before it.
					if pastHead.n == 0 {
						pastHead.lo = row.Seq
					}
					pastHead.n++
					pastHead.hi = row.Seq
				case fileHead == 0:
					// Before the journal's first line: pruned from the
					// journal, kept in the copy.
					if beforeFiles.n == 0 {
						beforeFiles.lo = row.Seq
					}
					beforeFiles.n++
					beforeFiles.hi = row.Seq
				default:
					r.problem("the copy holds seq %d, which the journal's files do not: a line was removed", row.Seq)
				}
				s = seen{seq: row.Seq, e: e, file: row.File, copied: true, copiedAt: row.CopiedAt}
			} else {
				r.Compared++
				if fLine.Hash != row.Hash {
					r.problem("seq %d differs between the journal (%s:%s) and the copy (%s): the line was rewritten after it was copied", row.Seq, fLine.File, fLine.Hash, row.Hash)
				}
				// The copy's line is what was anchored: read it.
				s = seen{seq: row.Seq, e: e, file: fLine.File, copied: true, copiedAt: row.CopiedAt}
			}
		} else {
			s = seen{seq: fLine.Seq, e: fLine.Entry, file: fLine.File}
		}
		if fLine != nil {
			fileHead = fLine.Seq
			r.JournalLines++
			if h := atAnchor[fLine.Seq]; h != nil {
				h.file = fLine.Hash
			}
		}

		switch s.e.Action {
		case ActionRetention:
			k, _ := keepOf(s.e)
			by, _ := s.e.Detail["by"].(string)
			retentions = append(retentions, retentionRec{seq: s.seq, keep: k, ts: s.e.TS, by: by})
		case ActionPrune:
			p := pruneRec{seen: s, prevTS: prevTS, havePrev: havePrevTS}
			p.keep, p.hasKeep = keepOf(s.e)
			p.last, _ = s.e.Detail["last"].(string)
			n, _ := s.e.Detail["seq"].(float64)
			p.lastSeq = uint64(n)
			p.unanchored, _ = s.e.Detail["unanchored"].(bool)
			prunes = append(prunes, p)
		}
		prevTS, havePrevTS = s.e.TS, true
		if len(anchors) > 0 && s.e.TS.Before(anchors[0].At) {
			beforeAnchors++
		}

		if fLine != nil {
			if fl, fok, err = files.next(); err != nil {
				return r, err
			}
		}
		if row != nil {
			if cr, cok, err = nextRow(in.Copy); err != nil {
				return r, err
			}
		}
	}

	if beforeFiles.n > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("the copy holds %d entries from before the journal's files (seq %d to %d): pruned from the journal, kept in the copy", beforeFiles.n, beforeFiles.lo, beforeFiles.hi))
	}
	r.JournalHead = fileHead
	if pastHead.n > 0 {
		r.problem("the copy holds %d entries past the journal's head seq %d (seq %d to %d): lines were cut from the end of the journal", pastHead.n, fileHead, pastHead.lo, pastHead.hi)
	}

	// Anchors.
	for _, a := range anchors {
		h := atAnchor[a.Seq]
		switch {
		case a.Seq > fileHead:
			r.problem("an anchor (%s, %s) holds seq %d, but the journal ends at seq %d: lines were cut from the end", a.Reason, a.At.UTC().Format(time.RFC3339), a.Seq, fileHead)
		case h.file != "" && h.file != a.Hash:
			r.problem("the anchor for seq %d (%s) does not match the journal's line (%s): it was rewritten after it was anchored", a.Seq, a.Hash, h.file)
		case h.file == "" && h.copy == "":
			r.problem("the anchor for seq %d names a line neither the journal nor the copy holds", a.Seq)
		case h.file == "" && h.copy != a.Hash:
			r.problem("the anchor for seq %d (%s) does not match the copy's row (%s)", a.Seq, a.Hash, h.copy)
		}
	}

	// Gaps: each is explained by the prune naming its last missing line.
	pruneByLast := map[uint64]bool{}
	for _, p := range prunes {
		pruneByLast[p.lastSeq] = true
	}
	gaps := append([]Gap(nil), in.Gaps...)
	sort.Slice(gaps, func(a, b int) bool { return gaps[a].NextSeq < gaps[b].NextSeq })
	for _, g := range gaps {
		if !pruneByLast[g.NextSeq-1] {
			r.problem("UNEXPLAINED GAP: the copy skipped seq %d to %d (after %s), and no audit.prune records pruning seq %d: files were removed without a prune", g.AfterSeq+1, g.NextSeq-1, g.AfterFile, g.NextSeq-1)
		}
	}

	// Retention lowerings: listed whatever the verdict.
	for i := 1; i < len(retentions); i++ {
		was, now := retentions[i-1], retentions[i]
		if lower(now.keep, was.keep) {
			who := now.by
			if who == "" {
				who = "naut run"
			}
			r.Warnings = append(r.Warnings, fmt.Sprintf("retention LOWERED from %s to %s at seq %d (%s, by %s): look at what was pruned after it", keepText(was.keep), keepText(now.keep), now.seq, now.ts.UTC().Format(time.RFC3339), who))
		}
	}
	firstRet := uint64(0)
	if len(retentions) > 0 {
		firstRet = retentions[0].seq
	}

	// Prunes.
	legacy := 0
	covered := uint64(0) // the highest Seq an earlier prune removed
	for _, p := range prunes {
		// A prune removes lines written before it, never itself or later.
		if p.lastSeq >= p.seq {
			r.problem("the audit.prune at seq %d records removing up to seq %d, which is not before it: a prune naut does not write", p.seq, p.lastSeq)
			continue
		}
		lo := covered
		if p.lastSeq > covered {
			covered = p.lastSeq
		}
		if !p.hasKeep {
			if firstRet == 0 || p.seq < firstRet {
				legacy++
				continue
			}
			r.problem("the audit.prune at seq %d records no keep, after the journal's first audit.retention (seq %d): a prune naut does not write", p.seq, firstRet)
			continue
		}
		var ret *retentionRec
		for i := range retentions {
			if retentions[i].seq < p.seq {
				ret = &retentions[i]
			}
		}
		switch {
		case ret == nil:
			r.problem("the audit.prune at seq %d (keep %s) follows no audit.retention entry", p.seq, keepText(p.keep))
		case ret.keep != p.keep:
			r.problem("the audit.prune at seq %d prunes by keep %s, but the retention in force (seq %d) is %s", p.seq, keepText(p.keep), ret.seq, keepText(ret.keep))
		}
		if p.keep < int64(MinKeep/time.Second) {
			r.problem("PRUNED EARLY: the audit.prune at seq %d prunes by keep %s, below the shortest naut accepts (%s)", p.seq, keepText(p.keep), keepText(int64(MinKeep/time.Second)))
			continue
		}

		// The first anchor covering the prune, by the database's clock.
		var aP *Anchor
		for i := range anchors {
			if anchors[i].Seq >= p.seq {
				aP = &anchors[i]
				break
			}
		}
		ts := p.e.TS
		bound, haveBound := time.Time{}, false
		if aP != nil {
			bound, haveBound = aP.At, true
		}
		if p.copied && (!haveBound || p.copiedAt.Before(bound)) {
			bound, haveBound = p.copiedAt, true
		}
		if aP == nil {
			r.problem("the audit.prune at seq %d is not anchored (the copy anchors each prune as it copies it): verify again once the copy is current; if it stays, anchors are not being written", p.seq)
		}
		if haveBound && ts.After(bound.Add(skew)) {
			r.problem("PRUNED EARLY: the audit.prune at seq %d is stamped %s, after the database first held it (%s) by more than %s: its time was set ahead", p.seq, ts.Format(time.RFC3339), bound.UTC().Format(time.RFC3339), skew)
		}
		if p.havePrev && ts.Before(p.prevTS.Add(-skew)) {
			r.problem("PRUNED EARLY: the audit.prune at seq %d is stamped %s, before the line it follows (%s) by more than %s", p.seq, ts.Format(time.RFC3339), p.prevTS.UTC().Format(time.RFC3339), skew)
		}
		cutoff := ts.Add(-time.Duration(p.keep) * time.Second).UTC().Format("20060102")
		if d := dayOf(p.e.Target); d == "" || d >= cutoff {
			r.problem("PRUNED EARLY: the audit.prune at seq %d removed %q, not older than its keep %s at %s", p.seq, p.e.Target, keepText(p.keep), ts.Format(time.RFC3339))
		}
		for name, fr := range ranges {
			if fr.max <= lo || fr.min > p.lastSeq {
				continue
			}
			if d := dayOf(name); d == "" || d >= cutoff {
				r.problem("PRUNED EARLY: the audit.prune at seq %d removed entries the copy holds from %s (seq %d to %d), not older than its keep %s at %s", p.seq, name, max(fr.min, lo+1), min(fr.max, p.lastSeq), keepText(p.keep), ts.Format(time.RFC3339))
			}
			if fr.max == p.lastSeq && fr.lastHash != p.last {
				r.problem("the audit.prune at seq %d names seq %d's hash as %s, but the copy holds %s", p.seq, p.lastSeq, p.last, fr.lastHash)
			}
		}

		// A lowered keep: the lowering must have been anchored before the
		// anchor that covers the prune.
		if aP != nil {
			base := uint64(0)
			for _, a := range anchors {
				if a.At.Before(aP.At) && a.Seq > base {
					base = a.Seq
				}
			}
			var anchored *retentionRec
			for i := range retentions {
				if retentions[i].seq <= base {
					anchored = &retentions[i]
				}
			}
			if anchored != nil && lower(p.keep, anchored.keep) {
				if p.unanchored {
					r.Warnings = append(r.Warnings, fmt.Sprintf("UNANCHORED LOWERING: the audit.prune at seq %d pruned by keep %s, below the anchored %s, before the lowering was anchored (the database was unreachable and the disk filling): nothing proves this lowering came before the prune", p.seq, keepText(p.keep), keepText(anchored.keep)))
				} else {
					r.problem("the audit.prune at seq %d prunes by keep %s, below the anchored retention %s (seq %d), and the lowering was not anchored before the prune", p.seq, keepText(p.keep), keepText(anchored.keep), anchored.seq)
				}
			}
		}
	}
	if legacy > 0 {
		at := "every prune"
		if firstRet > 0 {
			at = fmt.Sprintf("entries before seq %d", firstRet)
		}
		r.Warnings = append(r.Warnings, fmt.Sprintf("%s predate the journalled retention (PR 9b): their %d prunes are not checked", at, legacy))
	}

	// Writers.
	var writers []string
	seenW := map[string]bool{}
	for _, a := range anchors {
		if !seenW[a.Writer] {
			seenW[a.Writer] = true
			writers = append(writers, a.Writer)
		}
	}
	sort.Strings(writers)
	for _, w := range writers {
		c, ok := in.Writers[w]
		if !ok {
			c = WriterCheck{Why: "not checked"}
		}
		if !c.Protected {
			r.Unprotected = append(r.Unprotected, fmt.Sprintf("anchor present but not protected: it proves nothing against someone with role %q (%s); run naut audit pg-schema --role %s", w, c.Why, w))
		}
	}

	switch c := in.Controller; {
	case c == nil:
		r.Unprotected = append(r.Unprotected, "the controller's database role is not known, so whether it could rewrite the copy or the anchors (their writer column included) is not known: give --controller-role <the role in the controller's connection string>")
	case !seenW[c.Role] && !c.Protected:
		msg := fmt.Sprintf("the controller's role %q could rewrite the copy or the anchors, their writer column included (%s): nothing here proves anything against it; run naut audit pg-schema --role %s", c.Role, c.Why, c.Role)
		if c.FromConnection {
			msg += " (the role of this connection string, taken as the controller's; if it is not, give --controller-role)"
		}
		r.Unprotected = append(r.Unprotected, msg)
	case seenW[c.Role] && !c.Protected:
		// Already listed as an anchor's writer.
	}

	// When the anchors began, by the database's clock: entries dated
	// before it were copied, and first anchored, after they were written.
	if len(anchors) > 0 {
		r.FirstAnchor = anchors[0].At
		r.Notes = append(r.Notes, fmt.Sprintf("anchored since %s; %d entries are dated (by their own times, which the journal's writer sets) before that, so they rest on the journal as it stood then", r.FirstAnchor.UTC().Format(time.RFC3339), beforeAnchors))
	}

	switch {
	case len(r.Problems) > 0:
		r.Verdict = Broken
	case len(r.Unprotected) > 0:
		r.Verdict = Unprotected
	default:
		r.Verdict = Verified
	}
	return r, nil
}

func nextRow(next func() (CopyRow, bool, error)) (CopyRow, bool, error) {
	if next == nil {
		return CopyRow{}, false, nil
	}
	return next()
}

// keepOf reads an entry's detail.keep (seconds, -1 for forever).
func keepOf(e Entry) (int64, bool) {
	v, ok := e.Detail["keep"].(float64)
	if !ok {
		return 0, false
	}
	return int64(v), true
}

// lower reports whether keep a is shorter than b; -1 is forever.
func lower(a, b int64) bool {
	if a < 0 {
		return false
	}
	return b < 0 || a < b
}

func keepText(s int64) string {
	switch {
	case s < 0:
		return "forever"
	case s%86400 == 0:
		return fmt.Sprintf("%dd", s/86400)
	}
	return (time.Duration(s) * time.Second).String()
}

// lineReader yields the journal's complete entry lines in file order, one
// file in memory at a time. Lines that are not entries, or that go back
// in Seq, are skipped: the chain walk reports them.
type lineReader struct {
	dir   string
	files []string
	i     int
	data  []byte
	name  string
	last  uint64
}

func openLines(dir string) (*lineReader, error) {
	files, err := listFiles(dir)
	if err != nil {
		return nil, err
	}
	return &lineReader{dir: dir, files: files}, nil
}

func (l *lineReader) next() (Line, bool, error) {
	for {
		if len(l.data) == 0 {
			if l.i >= len(l.files) {
				return Line{}, false, nil
			}
			l.name = l.files[l.i]
			l.i++
			d, err := os.ReadFile(filepath.Join(l.dir, l.name))
			if err != nil {
				return Line{}, false, err
			}
			l.data = d[:bytes.LastIndexByte(d, '\n')+1]
			continue
		}
		k := bytes.IndexByte(l.data, '\n')
		raw := l.data[:k]
		l.data = l.data[k+1:]
		var e Entry
		if err := json.Unmarshal(raw, &e); err != nil || e.Seq <= l.last {
			continue
		}
		l.last = e.Seq
		return Line{File: l.name, Seq: e.Seq, Hash: lineHash(raw), Prev: e.Prev, Raw: raw, Entry: e}, true, nil
	}
}

// KeepSeconds is a retention as the journal records it: whole seconds, -1
// for keeping every file.
func KeepSeconds(keep time.Duration) int64 { return keepSeconds(keep) }

// ShorterKeep reports whether retention a (seconds, -1 forever) is
// shorter than b.
func ShorterKeep(a, b int64) bool { return lower(a, b) }
