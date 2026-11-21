package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// anchorStore is memStore with the database's side of 9b: each row's
// copied_at and each anchor's at come from the database clock (here the
// test's clock), never from the line.
type anchorStore struct {
	memStore
	c        *clock
	copiedAt map[uint64]time.Time
	anchors  []Anchor
	failNext error
}

func (s *anchorStore) Apply(ctx context.Context, b CopyBatch) error {
	if err := s.memStore.Apply(ctx, b); err != nil {
		return err
	}
	if s.copiedAt == nil {
		s.copiedAt = map[uint64]time.Time{}
	}
	for _, l := range b.Lines {
		s.copiedAt[l.Seq] = s.c.t
	}
	return nil
}

func (s *anchorStore) Anchor(_ context.Context, reason string) (uint64, error) {
	if s.failNext != nil {
		err := s.failNext
		s.failNext = nil
		return 0, err
	}
	if len(s.rows) == 0 {
		return 0, nil
	}
	l := s.rows[len(s.rows)-1]
	s.anchors = append(s.anchors, Anchor{Seq: l.Seq, Hash: l.Hash, At: s.c.t, Reason: reason, Writer: "naut_sink"})
	return l.Seq, nil
}

// site is a controller with a journal, its copy and anchors.
type site struct {
	t   *testing.T
	dir string
	c   *clock
	j   *FileJournal
	s   *anchorStore
	tl  *Tailer
}

func newSite(t *testing.T, keep time.Duration) *site {
	t.Helper()
	dir := t.TempDir()
	c := &clock{t0()}
	s := &site{t: t, dir: dir, c: c, s: &anchorStore{c: c}}
	s.start(keep)
	return s
}

// start opens the journal (as naut run does: the retention is recorded at
// once) and a tailer that anchors.
func (s *site) start(keep time.Duration) {
	s.t.Helper()
	if s.j != nil {
		s.j.Close()
	}
	s.j = open(s.t, s.dir, s.c, FileOptions{Keep: keep})
	if _, err := s.j.RecordRetention("op"); err != nil {
		s.t.Fatal(err)
	}
	s.tl = NewTailer(s.dir, s.s, s.j.Head)
	s.tl.now = s.c.now
	s.tl.AnchorTo(s.s, 5*time.Minute)
}

func (s *site) step() {
	s.t.Helper()
	s.c.t = s.c.t.Add(time.Second)
	if _, err := s.tl.Step(context.Background()); err != nil {
		s.t.Fatalf("Step: %v", err)
	}
}

// days writes one entry a day for n days, copying each.
func (s *site) days(n int) {
	s.t.Helper()
	for i := 0; i < n; i++ {
		write(s.t, s.j, "tag.write", "SP")
		s.step()
		s.c.t = s.c.t.Add(24 * time.Hour)
	}
}

func (s *site) input() AnchorInput {
	rows := s.s.rows
	i := 0
	return AnchorInput{
		Dir: s.dir,
		Copy: func() (CopyRow, bool, error) {
			if i >= len(rows) {
				return CopyRow{}, false, nil
			}
			l := rows[i]
			i++
			return CopyRow{Seq: l.Seq, Hash: l.Hash, File: l.File, CopiedAt: s.s.copiedAt[l.Seq], Line: l.Raw}, true, nil
		},
		Gaps:    s.s.gaps,
		Anchors: s.s.anchors,
		Writers: map[string]WriterCheck{"naut_sink": {Protected: true}},
	}
}

func (s *site) verify() AnchorReport {
	s.t.Helper()
	r, err := VerifyAnchored(s.input())
	if err != nil {
		s.t.Fatal(err)
	}
	return r
}

// forge appends a correctly chained line, as anyone who can write the
// journal's files can (here through the journal itself).
func (s *site) forge(e Entry) Entry {
	s.t.Helper()
	if e.User == "" {
		e.User, e.Source, e.Result = "system", SourceSystem, ResultOK
	}
	out, err := s.j.Record(e)
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}

// forgePrune removes file and appends an audit.prune naming its true last
// hash and seq, with the detail given (keep nil: none, as before 9b).
func (s *site) forgePrune(file string, keep any, ts time.Time) {
	s.t.Helper()
	files, _ := listFiles(s.dir)
	i := -1
	for k, f := range files {
		if f == file {
			i = k
		}
	}
	data, err := os.ReadFile(filepath.Join(s.dir, file))
	if err != nil || i < 0 {
		s.t.Fatalf("forgePrune %s: %v", file, err)
	}
	last, seq, _ := lastLine(s.dir, files, i, data[:bytes.LastIndexByte(data, '\n')+1])
	d := map[string]any{"last": last, "seq": seq}
	if keep != nil {
		d["keep"] = keep
	}
	s.forge(Entry{TS: ts, Action: ActionPrune, Target: file, Detail: d})
	if err := os.Remove(filepath.Join(s.dir, file)); err != nil {
		s.t.Fatal(err)
	}
}

func wantVerdict(t *testing.T, r AnchorReport, verdict string, problems ...string) {
	t.Helper()
	all := strings.Join(r.Problems, "\n")
	if r.Verdict != verdict {
		t.Fatalf("verdict %s, want %s\nproblems:\n%s\nwarnings: %v\nunprotected: %v", r.Verdict, verdict, all, r.Warnings, r.Unprotected)
	}
	for _, p := range problems {
		if !strings.Contains(all, p) {
			t.Errorf("problems lack %q:\n%s", p, all)
		}
	}
}

func hasWarning(r AnchorReport, s string) bool {
	return strings.Contains(strings.Join(r.Warnings, "\n"), s)
}

const day = 24 * time.Hour

// An honest life: writes, daily prunes by the retention in force, each
// anchored. It verifies, and the copy still holds what was pruned.
func TestAnchoredHonestPruneVerifies(t *testing.T) {
	s := newSite(t, 2*day)
	s.days(5)
	removed, err := s.j.Prune()
	if err != nil || len(removed) == 0 {
		t.Fatalf("prune: %v %v", removed, err)
	}
	s.step()
	r := s.verify()
	wantVerdict(t, r, Verified)
	if len(r.Warnings) != 0 {
		t.Errorf("warnings %v, want none", r.Warnings)
	}
	if !strings.Contains(strings.Join(r.Notes, "\n"), "from before the journal's files") {
		t.Errorf("notes %v, want the pruned entries the copy keeps listed", r.Notes)
	}
	if r.Compared == 0 || r.CopyRows <= r.Compared {
		t.Errorf("compared %d of %d rows", r.Compared, r.CopyRows)
	}
}

// MUST pin: the copy's file and the ones after it deleted, with no prune:
// the copy takes the next line after a gap, and verify finds no prune for
// it: UNEXPLAINED GAP.
func TestAnchoredUnexplainedGap(t *testing.T) {
	s := newSite(t, 30*day)
	s.days(1) // day 1: retention + seq 2, copied
	write(t, s.j, "tag.write", "Day2")
	s.c.t = s.c.t.Add(day)
	write(t, s.j, "tag.write", "Day3")
	for _, f := range []string{"audit-20261118.jsonl", "audit-20261119.jsonl"} {
		os.Remove(filepath.Join(s.dir, f))
	}
	s.step()
	if len(s.s.gaps) != 1 {
		t.Fatalf("gaps %+v, want the copy to record one", s.s.gaps)
	}
	wantVerdict(t, s.verify(), Broken, "UNEXPLAINED GAP", "the journal's chain breaks")
}

// MUST pin: recent files deleted and a correctly chained audit.prune
// appended, with the true keep: the file it names is not old enough for
// that keep: PRUNED EARLY.
func TestAnchoredForgedPruneTrueKeep(t *testing.T) {
	s := newSite(t, 30*day)
	s.days(3)
	s.forgePrune("audit-20261118.jsonl", float64(30*86400), s.c.t)
	s.step()
	wantVerdict(t, s.verify(), Broken, "PRUNED EARLY", "removed \"audit-20261118.jsonl\"")
}

// MUST pin: the same with keep 0: below the floor, and not the retention
// in force.
func TestAnchoredForgedPruneKeepZero(t *testing.T) {
	s := newSite(t, 30*day)
	s.days(3)
	s.forgePrune("audit-20261118.jsonl", float64(0), s.c.t)
	s.step()
	wantVerdict(t, s.verify(), Broken, "but the retention in force", "below the shortest naut accepts")
}

// MUST pin: the prune stamped ahead (so the file reads old enough): its
// time is after the database first held it.
func TestAnchoredPruneStampedAhead(t *testing.T) {
	s := newSite(t, 2*day)
	s.days(3)
	s.forgePrune("audit-20261120.jsonl", float64(2*86400), s.c.t.Add(10*day))
	s.step()
	wantVerdict(t, s.verify(), Broken, "PRUNED EARLY", "its time was set ahead")
}

// A prune stamped back, before the line it follows, is caught too.
func TestAnchoredPruneStampedBack(t *testing.T) {
	s := newSite(t, 2*day)
	s.days(4)
	s.forgePrune("audit-20261118.jsonl", float64(2*86400), s.c.t.Add(-2*day))
	s.step()
	wantVerdict(t, s.verify(), Broken, "before the line it follows")
}

// MUST pin: the keep lowered with an audit.retention the forger journals
// just before the prune: the lowering is anchored only with the prune.
func TestAnchoredLoweringNotAnchoredFirst(t *testing.T) {
	s := newSite(t, 30*day)
	s.days(4)
	s.forge(Entry{Action: ActionRetention, Detail: map[string]any{"keep": float64(86400)}})
	s.forgePrune("audit-20261118.jsonl", float64(86400), s.c.t)
	s.step()
	r := s.verify()
	wantVerdict(t, r, Broken, "below the anchored retention 30d", "the lowering was not anchored before the prune")
	if !hasWarning(r, "retention LOWERED from 30d to 1d") {
		t.Errorf("warnings %v, want the lowering listed", r.Warnings)
	}
	// With no retention journalled at all, the prune's keep is the
	// retention's no longer.
	s2 := newSite(t, 30*day)
	s2.days(4)
	s2.forgePrune("audit-20261118.jsonl", float64(86400), s2.c.t)
	s2.step()
	wantVerdict(t, s2.verify(), Broken, "but the retention in force (seq 1) is 30d")
}

// MUST pin: an honest lowering (a restart with a shorter keep, its
// retention anchored before the prune, as naut run's gate makes it)
// verifies, and is still listed.
func TestAnchoredHonestLoweringVerifies(t *testing.T) {
	s := newSite(t, 30*day)
	s.days(4)
	s.start(2 * day)
	if !s.j.Lowered() {
		t.Fatal("the journal does not see its retention lowered")
	}
	s.step() // the run's first anchor covers the lowered retention
	if got := s.tl.Status().Anchored; got < s.j.RetentionSeq() {
		t.Fatalf("anchored %d, retention at %d", got, s.j.RetentionSeq())
	}
	s.c.t = s.c.t.Add(time.Minute)
	if removed, err := s.j.Prune(); err != nil || len(removed) == 0 {
		t.Fatalf("prune: %v %v", removed, err)
	}
	s.step()
	r := s.verify()
	wantVerdict(t, r, Verified)
	if !hasWarning(r, "retention LOWERED from 30d to 2d") || !hasWarning(r, "by op") {
		t.Errorf("warnings %v, want the lowering listed, with who", r.Warnings)
	}
}

// naut-lead's bound: a lowered prune that could not wait for its anchor
// (the disk filling) is journalled unanchored: verify does not call it
// forged, but names it, whatever the verdict.
func TestAnchoredUnanchoredLoweringIsAWarning(t *testing.T) {
	s := newSite(t, 30*day)
	s.days(4)
	s.start(2 * day)
	if _, err := s.j.PruneUnanchored(); err != nil {
		t.Fatal(err)
	}
	s.step() // one anchor covers the lowering and the prune
	r := s.verify()
	wantVerdict(t, r, Verified)
	if !hasWarning(r, "UNANCHORED LOWERING") {
		t.Errorf("warnings %v, want the unanchored lowering", r.Warnings)
	}
	// The same prune without the mark is the forgery above.
	s2 := newSite(t, 30*day)
	s2.days(4)
	s2.start(2 * day)
	s2.j.Prune()
	s2.step()
	wantVerdict(t, s2.verify(), Broken, "the lowering was not anchored before the prune")
}

// naut-lead's legacy rule: a prune without keep before the first
// audit.retention predates 9b (a warning, the verdict unaffected); after
// it, it is a prune naut does not write.
func TestAnchoredLegacyPrunes(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	s := &site{t: t, dir: dir, c: c, s: &anchorStore{c: c}}
	s.j = open(t, dir, c, FileOptions{Keep: 2 * day}) // before 9b: no retention entry
	s.tl = NewTailer(dir, s.s, s.j.Head)
	s.tl.now = c.now
	s.tl.AnchorTo(s.s, 5*time.Minute)
	s.days(4)
	s.forgePrune("audit-20261118.jsonl", nil, c.t) // as Prune wrote before 9b
	s.step()
	s.start(2 * day) // upgraded: the retention is recorded from now on
	s.days(1)
	r := s.verify()
	wantVerdict(t, r, Verified)
	if !hasWarning(r, "predate the journalled retention") || !hasWarning(r, "their 1 prunes are not checked") {
		t.Errorf("warnings %v, want the legacy prune named", r.Warnings)
	}
	// A keep-less prune after the first retention entry is no legacy.
	s.forgePrune("audit-20261119.jsonl", nil, c.t)
	s.step()
	wantVerdict(t, s.verify(), Broken, "records no keep, after the journal's first audit.retention")
}

// Lines cut from the end: the copy and the anchors hold seqs past the
// journal's head.
func TestAnchoredCutOffTheEnd(t *testing.T) {
	s := newSite(t, 30*day)
	s.days(2)
	s.j.Close()
	name := "audit-20261119.jsonl"
	os.WriteFile(filepath.Join(s.dir, name), nil, 0o600) // the day's lines gone
	wantVerdict(t, s.verify(), Broken, "past the journal's head", "lines were cut from the end", "an anchor (")
}

// A file missing from the middle: the chain breaks, and the copy holds
// lines the files do not.
func TestAnchoredMissingMiddleFile(t *testing.T) {
	s := newSite(t, 30*day)
	s.days(3)
	os.Remove(filepath.Join(s.dir, "audit-20261119.jsonl"))
	wantVerdict(t, s.verify(), Broken, "the journal's chain breaks", "which the journal's files do not")
}

// A copy row rewritten in the database with its hash recomputed (a role
// with UPDATE): where the journal no longer holds the line, the anchor
// still catches it.
func TestAnchoredRewrittenCopyRow(t *testing.T) {
	s := newSite(t, 2*day)
	s.days(5)
	s.j.Prune()
	s.step()
	first := s.s.anchors[0].Seq // anchored at start, since pruned from the files
	for i, r := range s.s.rows {
		if r.Seq == first {
			raw := bytes.Replace(r.Raw, []byte(`"result":"ok"`), []byte(`"result":"refused"`), 1)
			s.s.rows[i].Raw, s.s.rows[i].Hash = raw, lineHash(raw)
		}
	}
	wantVerdict(t, s.verify(), Broken, "does not match the copy's row")
}

// A prune with a keep but no retention journalled at all is not one naut
// writes.
func TestAnchoredPruneWithoutRetention(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	s := &site{t: t, dir: dir, c: c, s: &anchorStore{c: c}}
	s.j = open(t, dir, c, FileOptions{Keep: 2 * day})
	s.tl = NewTailer(dir, s.s, s.j.Head)
	s.tl.now = c.now
	s.tl.AnchorTo(s.s, 5*time.Minute)
	s.days(4)
	s.j.Prune()
	s.step()
	wantVerdict(t, s.verify(), Broken, "follows no audit.retention entry")
}

// A forged prune that names an old file it never removed, while the files
// it did remove are recent: the copy's rows say which files they were in.
// It also names the wrong hash for the last line it removed.
func TestAnchoredPruneNamesAnotherFile(t *testing.T) {
	s := newSite(t, 3*day)
	s.days(4)
	s.forge(Entry{Action: ActionPrune, Target: "audit-20200101.jsonl",
		Detail: map[string]any{"last": "sha256:00", "seq": float64(3), "keep": float64(3 * 86400)}})
	os.Remove(filepath.Join(s.dir, "audit-20261118.jsonl"))
	os.Remove(filepath.Join(s.dir, "audit-20261119.jsonl"))
	s.step()
	wantVerdict(t, s.verify(), Broken, "removed entries the copy holds from audit-20261119.jsonl", "names seq 3's hash as sha256:00")
}

// A journal rewritten with its chain recomputed passes the chain walk,
// not the copy.
func TestAnchoredRewrittenJournal(t *testing.T) {
	s := newSite(t, 30*day)
	s.days(3)
	s.j.Close()
	// Rewrite every file with one value changed, chain recomputed.
	files, _ := listFiles(s.dir)
	prev := ""
	for _, f := range files {
		data, _ := os.ReadFile(filepath.Join(s.dir, f))
		var out []byte
		for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'}) {
			line = bytes.Replace(line, []byte(`"after":55`), []byte(`"after":99`), 1)
			if prev != "" {
				line = rechain(t, line, prev)
			}
			prev = lineHash(line)
			out = append(append(out, line...), '\n')
		}
		os.WriteFile(filepath.Join(s.dir, f), out, 0o600)
	}
	r := s.verify()
	if !r.Chain.OK() {
		t.Fatalf("the rewritten chain should walk clean: %v", r.Chain.Break)
	}
	wantVerdict(t, r, Broken, "differs between the journal", "does not match the journal's line")
}

func rechain(t *testing.T, line []byte, prev string) []byte {
	t.Helper()
	i := bytes.Index(line, []byte(`"prev":"`))
	if i < 0 {
		t.Fatalf("no prev in %s", line)
	}
	j := bytes.IndexByte(line[i+8:], '"')
	return append(append(append([]byte(nil), line[:i+8]...), prev...), line[i+8+j:]...)
}

// A writer that could rewrite the anchors makes the verdict UNPROTECTED,
// never verified; a writer not checked reads the same.
func TestAnchoredUnprotectedWriter(t *testing.T) {
	s := newSite(t, 30*day)
	s.days(2)
	in := s.input()
	in.Writers = map[string]WriterCheck{"naut_sink": {Why: "it is a superuser"}}
	r, err := VerifyAnchored(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != Unprotected || len(r.Unprotected) != 1 || !strings.Contains(r.Unprotected[0], "anchor present but not protected") ||
		!strings.Contains(r.Unprotected[0], "naut audit pg-schema --role naut_sink") {
		t.Errorf("verdict %s %v", r.Verdict, r.Unprotected)
	}
	in = s.input()
	in.Writers = nil
	if r, _ := VerifyAnchored(in); r.Verdict != Unprotected {
		t.Errorf("an unchecked writer: %s", r.Verdict)
	}
}

// A prune newer than the newest anchor is never verified: the copy
// anchors each prune as it copies it, so its absence is a failure.
func TestAnchoredUnanchoredPruneIsNotVerified(t *testing.T) {
	s := newSite(t, 2*day)
	s.days(4)
	s.s.failNext = errors.New("database refused")
	s.j.Prune()
	s.step() // the prune is copied, its anchor fails
	wantVerdict(t, s.verify(), Broken, "is not anchored")
	s.step() // retried on the next step
	if got := s.s.anchors[len(s.s.anchors)-1].Reason; got != AnchorPrune {
		t.Errorf("the retried anchor's reason %q, want prune", got)
	}
	wantVerdict(t, s.verify(), Verified)
}

// A hole in the copy that no gap records means rows were removed from it.
func TestAnchoredCopyHoleWithoutGap(t *testing.T) {
	s := newSite(t, 30*day)
	s.days(4)
	s.s.rows = append(s.s.rows[:2:2], s.s.rows[3:]...)
	wantVerdict(t, s.verify(), Broken, "no gap records it")
}

// A copy row whose line was changed in the database no longer hashes as
// it says.
func TestAnchoredCopyRowEdited(t *testing.T) {
	s := newSite(t, 30*day)
	s.days(2)
	s.s.rows[1].Raw = bytes.Replace(s.s.rows[1].Raw, []byte("55"), []byte("56"), 1)
	wantVerdict(t, s.verify(), Broken, "does not hash as the row says")
}

// The tailer anchors at once on its first step, then only when the copy
// moved and the interval passed, at once after a copied prune, and on
// AnchorNow when the copy moved; a failed anchor is owed and retried with
// its reason.
func TestTailerAnchors(t *testing.T) {
	s := newSite(t, 2*day)
	reasons := func() []string {
		var out []string
		for _, a := range s.s.anchors {
			out = append(out, a.Reason)
		}
		return out
	}
	want := func(r ...string) {
		t.Helper()
		if got := strings.Join(reasons(), ","); got != strings.Join(r, ",") {
			t.Fatalf("anchors %s, want %s", got, strings.Join(r, ","))
		}
	}
	s.step()
	want(AnchorStart)
	if a := s.tl.Status(); a.Anchored != 1 || a.AnchoredAt.IsZero() {
		t.Fatalf("status %+v, want anchored at seq 1", a)
	}
	write(t, s.j, "tag.write", "SP")
	s.step() // moved, but the interval has not passed
	want(AnchorStart)
	s.c.t = s.c.t.Add(5 * time.Minute)
	s.step()
	want(AnchorStart, AnchorInterval)
	s.c.t = s.c.t.Add(5 * time.Minute)
	s.step() // the interval passed, but nothing moved
	want(AnchorStart, AnchorInterval)
	if err := s.tl.AnchorNow(context.Background(), AnchorStop); err != nil {
		t.Fatal(err)
	}
	want(AnchorStart, AnchorInterval) // nothing to anchor at stop
	s.c.t = s.c.t.Add(3 * day)
	write(t, s.j, "tag.write", "SP")
	s.j.Prune()
	s.s.failNext = errors.New("refused")
	s.step()
	want(AnchorStart, AnchorInterval)
	if st := s.tl.Status(); st.AnchorError != "refused" {
		t.Fatalf("status %+v, want the anchor's error", st)
	}
	s.step()
	want(AnchorStart, AnchorInterval, AnchorPrune)
	if st := s.tl.Status(); st.AnchorError != "" {
		t.Errorf("the error stays after the retry: %+v", st)
	}
	write(t, s.j, "tag.write", "SP")
	s.step()
	if err := s.tl.AnchorNow(context.Background(), AnchorStop); err != nil {
		t.Fatal(err)
	}
	want(AnchorStart, AnchorInterval, AnchorPrune, AnchorStop)
}

// The retention is journalled with who, every prune names its keep, a
// lowered retention is seen against the one the journal recorded before,
// and a prune journals the retention again before removing the file that
// holds it.
func TestRetentionIsJournalled(t *testing.T) {
	s := newSite(t, 3*day)
	if s.j.Lowered() {
		t.Error("a first retention reads as lowered")
	}
	s.days(5)
	s.start(30 * day)
	if s.j.Lowered() {
		t.Error("a longer retention reads as lowered")
	}
	s.start(2 * day)
	if !s.j.Lowered() {
		t.Error("a shorter retention does not read as lowered")
	}
	s.start(-1)
	if s.j.Lowered() {
		t.Error("keeping forever reads as lowered")
	}
	s.start(2 * day)
	before := s.j.RetentionSeq()
	// Rotate so the newest retention sits in a file old enough to prune.
	s.c.t = s.c.t.Add(5 * day)
	write(t, s.j, "tag.write", "Later")
	if _, err := s.j.PruneUnanchored(); err != nil {
		t.Fatal(err)
	}
	var rets, prunes []Entry
	files, _ := listFiles(s.dir)
	for _, f := range files {
		data, _ := os.ReadFile(filepath.Join(s.dir, f))
		for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'}) {
			var e Entry
			jsonUnmarshal(t, line, &e)
			switch e.Action {
			case ActionRetention:
				rets = append(rets, e)
			case ActionPrune:
				prunes = append(prunes, e)
			}
		}
	}
	if len(rets) == 0 || rets[len(rets)-1].Seq <= before {
		t.Fatalf("retentions %+v: want one journalled again before the file holding seq %d was pruned", rets, before)
	}
	if k, _ := keepOf(rets[len(rets)-1]); k != 2*86400 {
		t.Errorf("the retention's keep %d", k)
	}
	if len(prunes) == 0 {
		t.Fatal("no prune")
	}
	for _, p := range prunes {
		if k, ok := keepOf(p); !ok || k != 2*86400 || p.Detail["unanchored"] != true {
			t.Errorf("prune %+v, want keep 2d and unanchored", p.Detail)
		}
	}
}

func jsonUnmarshal(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
}
