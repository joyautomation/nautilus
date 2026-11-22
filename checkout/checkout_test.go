package checkout

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sheet() *Sheet {
	return &Sheet{Title: "Test", Points: []Point{
		{ID: "XV101", Tag: "XV101_Cmd", Kind: DO, Method: Force, Device: "rio1", Area: "RCV"},
		{ID: "LSH101", Tag: "LSH101", Kind: DI, Method: Observe, Device: "rio1", Area: "RCV"},
		{ID: "FIT201", Tag: "FIT201", Kind: AI, Method: Observe, Device: "rio2", Area: "LOD"},
	}}
}

func TestSheetValidate(t *testing.T) {
	if err := sheet().Validate(); err != nil {
		t.Fatal(err)
	}
	bad := &Sheet{Points: []Point{
		{ID: "A", Tag: "a", Kind: DI, Method: Force},
		{ID: "A", Tag: "a", Kind: "XX", Method: Observe},
		{ID: "", Tag: "", Kind: AO, Method: "poke"},
	}}
	err := bad.Validate()
	if err == nil {
		t.Fatal("a bad sheet validated")
	}
	for _, want := range []string{"only an output", "used twice", `kind "XX"`, "no id", "no tag", `method "poke"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if err := (&Sheet{}).Validate(); err == nil {
		t.Error("an empty sheet validated")
	}
}

func TestSheetHashFollowsContent(t *testing.T) {
	a, b := sheet(), sheet()
	if a.Hash() != b.Hash() {
		t.Fatal("equal sheets hash apart")
	}
	b.Points[1].Expect = "closed when the float is down"
	if a.Hash() == b.Hash() {
		t.Error("a changed point left the hash alone")
	}
}

func TestProgressByDevice(t *testing.T) {
	s := sheet()
	got := s.ProgressBy(func(p Point) string { return p.Device }, map[string]Result{
		"XV101": {Outcome: Pass}, "LSH101": {Outcome: Fail},
	})
	if got["rio1"] != (Progress{Total: 2, Pass: 1, Fail: 1}) || got["rio2"] != (Progress{Total: 1, Pending: 1}) {
		t.Errorf("progress %+v", got)
	}
}

func openStore(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "checkout.jsonl")
	n := int64(0)
	s, err := Open(p, func() time.Time { n++; return time.UnixMilli(1_000_000 + n) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, p
}

func TestRunsAndResults(t *testing.T) {
	s, p := openStore(t)
	run, err := s.Begin(true, sheet().Hash(), "Test", "alice", "user:alice")
	if err != nil || run.State != StateOpen || !run.Rehearsal {
		t.Fatalf("begin: %+v %v", run, err)
	}
	if _, err := s.Begin(false, "x", "", "bob", "user:bob"); !errors.Is(err, ErrRunOpen) {
		t.Errorf("a second open run: %v", err)
	}
	if _, err := s.Record(run.ID, "XV101", Fail, "", nil, "", "alice", "user:alice"); err == nil || !strings.Contains(err.Error(), "says why") {
		t.Errorf("a fail with no note: %v", err)
	}
	if _, err := s.Record(run.ID, "XV101", "maybe", "", nil, "", "alice", ""); err == nil {
		t.Error("an unknown outcome was recorded")
	}
	if _, err := s.Record("nope", "XV101", Pass, "", nil, "", "alice", ""); !errors.Is(err, ErrNoRun) {
		t.Errorf("no such run: %v", err)
	}
	r1, err := s.Record(run.ID, "XV101", Fail, "valve did not move", 0.0, "good", "alice", "user:alice")
	if err != nil || r1.Outcome != Fail || r1.Seen != 0.0 {
		t.Fatalf("record: %+v %v", r1, err)
	}
	r2, _ := s.Record(run.ID, "XV101", Pass, "retested after the air line was fixed", true, "good", "alice", "user:alice")
	if got := s.Latest(run.ID)["XV101"]; got.Seq != r2.Seq || got.Outcome != Pass {
		t.Errorf("latest %+v, want the pass", got)
	}
	if h := s.History(run.ID, "XV101"); len(h) != 2 || h[0].Outcome != Fail {
		t.Errorf("history %+v: the superseded fail must stay", h)
	}
	if v, _ := s.Get(run.ID); v.Results != 2 {
		t.Errorf("results %d", v.Results)
	}

	// reopen: the same view, from the file
	s.Close()
	s2, err := Open(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if cur, ok := s2.Current(); !ok || cur.ID != run.ID || cur.Results != 2 || !cur.Rehearsal {
		t.Errorf("reopened current %+v %v", cur, ok)
	}
	if got := s2.Latest(run.ID)["XV101"]; got.Outcome != Pass {
		t.Errorf("reopened latest %+v", got)
	}

	// abandoning needs a reason, ends the run, keeps the results
	if _, err := s2.Abandon(run.ID, "", "alice", ""); err == nil {
		t.Error("abandoned with no reason")
	}
	if _, err := s2.Abandon(run.ID, "wrong sheet", "alice", "user:alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Record(run.ID, "LSH101", Pass, "", nil, "", "alice", ""); !errors.Is(err, ErrNotOpen) {
		t.Errorf("a result for an abandoned run: %v", err)
	}
	if _, ok := s2.Current(); ok {
		t.Error("an abandoned run is current")
	}
	if len(s2.Results(run.ID)) != 2 {
		t.Error("abandoning lost results")
	}
	if _, err := s2.Begin(false, "y", "", "bob", ""); err != nil {
		t.Errorf("a new run after abandoning: %v", err)
	}
}

// An edited line, a removed line and a reordered pair are damage, and the
// store refuses to open rather than show a record that was changed.
func TestOpenRefusesATamperedFile(t *testing.T) {
	s, p := openStore(t)
	run, _ := s.Begin(false, "h", "", "alice", "")
	s.Record(run.ID, "A", Pass, "", nil, "", "alice", "")
	s.Record(run.ID, "B", Pass, "", nil, "", "alice", "")
	s.Close()
	orig, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimRight(string(orig), "\n"), "\n")
	cases := map[string]string{
		"edited":  strings.Replace(string(orig), `"outcome":"pass"`, `"outcome":"fail"`, 1),
		"removed": strings.Join(append([]string{lines[0]}, lines[2:]...), "\n") + "\n",
		"swapped": strings.Join([]string{lines[0], lines[2], lines[1]}, "\n") + "\n",
	}
	for name, content := range cases {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(p, nil); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: %v, want ErrCorrupt", name, err)
		}
	}
}

// A last line with no newline is a write cut short before it was
// acknowledged: it is cut off and the rest opens.
func TestOpenDropsATornLastLine(t *testing.T) {
	s, p := openStore(t)
	run, _ := s.Begin(false, "h", "", "alice", "")
	s.Record(run.ID, "A", Pass, "", nil, "", "alice", "")
	s.Close()
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"seq":3,"kind":"res`)
	f.Close()
	s2, err := Open(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if v, _ := s2.Get(run.ID); v.Results != 1 {
		t.Errorf("results %d", v.Results)
	}
	if _, err := s2.Record(run.ID, "B", Pass, "", nil, "", "alice", ""); err != nil {
		t.Fatal(err)
	}
	s2.Close()
	if _, err := Open(p, nil); err != nil {
		t.Errorf("reopened after a record past the cut: %v", err)
	}
}

// failFile is the store's file with writes and syncs that fail on demand.
type failFile struct {
	appendFile
	short, syncErr, truncErr bool
}

func (f *failFile) Write(b []byte) (int, error) {
	if f.short {
		f.short = false
		f.appendFile.Write(b[:len(b)/2]) // part of the line reaches the disk
		return len(b) / 2, errors.New("no space left on device")
	}
	return f.appendFile.Write(b)
}

func (f *failFile) Sync() error {
	if f.syncErr {
		f.syncErr = false
		return errors.New("input/output error")
	}
	return f.appendFile.Sync()
}

func (f *failFile) Truncate(n int64) error {
	if f.truncErr {
		return errors.New("read-only file system")
	}
	return f.appendFile.Truncate(n)
}

// A write or sync that fails leaves nothing behind: the next entry is not
// glued onto half a line, nor does it reuse a sequence number, and the file
// still opens.
func TestAFailedAppendLeavesNothingBehind(t *testing.T) {
	for _, mode := range []string{"short write", "failed sync"} {
		s, p := openStore(t)
		ff := &failFile{appendFile: s.f}
		s.f = ff
		run, err := s.Begin(false, "h", "", "alice", "")
		if err != nil {
			t.Fatal(err)
		}
		ff.short, ff.syncErr = mode == "short write", mode == "failed sync"
		if _, err := s.Record(run.ID, "A", Pass, "", nil, "", "alice", ""); err == nil {
			t.Fatalf("%s: the record was acknowledged", mode)
		}
		if _, err := s.Record(run.ID, "B", Pass, "", nil, "", "alice", ""); err != nil {
			t.Fatalf("%s: the next record: %v", mode, err)
		}
		s.Close()
		s2, err := Open(p, nil)
		if err != nil {
			t.Fatalf("%s: the file no longer opens: %v", mode, err)
		}
		if got := s2.Latest(run.ID); len(got) != 1 || got["B"].Outcome != Pass {
			t.Errorf("%s: after reopening, results %+v, want only B", mode, got)
		}
		s2.Close()
	}
}

// If the file cannot be cut back after a failed write, the store takes no
// more entries rather than write after a half line.
func TestTheStoreStopsWhenItCannotCutBack(t *testing.T) {
	s, _ := openStore(t)
	ff := &failFile{appendFile: s.f}
	s.f = ff
	run, _ := s.Begin(false, "h", "", "alice", "")
	ff.short, ff.truncErr = true, true
	s.Record(run.ID, "A", Pass, "", nil, "", "alice", "")
	ff.truncErr = false
	if _, err := s.Record(run.ID, "B", Pass, "", nil, "", "alice", ""); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Errorf("a record after an uncut failure: %v", err)
	}
}

// A reading too large to read back is shortened, never acknowledged and then
// refused at the next start.
func TestAHugeReadingIsShortened(t *testing.T) {
	s, p := openStore(t)
	run, _ := s.Begin(false, "h", "", "alice", "")
	big := make([]float64, 200_000)
	r, err := s.Record(run.ID, "A", Pass, "", big, "good", "alice", "")
	if err != nil || r.Seen != "(too large to record)" {
		t.Fatalf("record: seen %T, %v", r.Seen, err)
	}
	s.Close()
	if _, err := Open(p, nil); err != nil {
		t.Errorf("reopen: %v", err)
	}
}

func TestValidationErrorsAreMarked(t *testing.T) {
	s, _ := openStore(t)
	run, _ := s.Begin(false, "h", "", "alice", "")
	if _, err := s.Record(run.ID, "A", Fail, "", nil, "", "alice", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("a fail with no note: %v", err)
	}
	if _, err := s.Abandon(run.ID, "", "alice", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("an abandon with no reason: %v", err)
	}
}

func TestForcesInTheRecord(t *testing.T) {
	s, p := openStore(t)
	run, _ := s.Begin(false, "h", "", "alice", "user:alice")
	if _, err := s.StartForce(run.ID, "XV101", true, 0, "alice", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("a force of no seconds: %v", err)
	}
	f, err := s.StartForce(run.ID, "XV101", true, 30, "alice", "user:alice")
	if err != nil || f.Seq == 0 || f.Seconds != 30 {
		t.Fatalf("start: %+v %v", f, err)
	}
	if _, err := s.StartForce(run.ID, "XV102", true, 30, "alice", ""); !errors.Is(err, ErrForceActive) {
		t.Errorf("a second force: %v", err)
	}
	if got, ok := s.Standing(); !ok || got.Point != "XV101" {
		t.Errorf("standing %+v %v", got, ok)
	}
	// a result while forced carries the force; one after it ended says how
	r1, _ := s.Record(run.ID, "XV101", Pass, "", true, "good", "alice", "")
	if r1.Force == nil || r1.Force.Seq != f.Seq || r1.Force.Ended != "" || r1.Force.Value != true {
		t.Errorf("result while forced: %+v", r1.Force)
	}
	if err := s.EndForce(run.ID, ForceExpired, "controller"); err != nil {
		t.Fatal(err)
	}
	if err := s.EndForce(run.ID, ForceExpired, "controller"); !errors.Is(err, ErrNoForce) {
		t.Errorf("ending a force that is not standing: %v", err)
	}
	r2, _ := s.Record(run.ID, "XV101", Pass, "retest", true, "good", "alice", "")
	if r2.Force == nil || r2.Force.Ended != ForceExpired {
		t.Errorf("result after expiry: %+v", r2.Force)
	}
	// a point never forced has no force
	if r3, _ := s.Record(run.ID, "LSH101", Pass, "", false, "good", "alice", ""); r3.Force != nil {
		t.Errorf("an unforced point carries a force: %+v", r3.Force)
	}

	// a standing force is interrupted when the file is opened by a new
	// process, and abandoning a run ends one first
	s.StartForce(run.ID, "XV101", false, 10, "alice", "")
	s.Close()
	s2, err := Open(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Standing(); !ok {
		t.Fatal("the standing force was lost on reopen")
	}
	if n, err := s2.InterruptStanding(); err != nil || n != 1 {
		t.Fatalf("interrupt: %d %v", n, err)
	}
	if _, ok := s2.Standing(); ok {
		t.Error("a force still stands after it was interrupted")
	}
	s2.StartForce(run.ID, "XV101", true, 10, "alice", "")
	if _, err := s2.Abandon(run.ID, "wrong sheet", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Standing(); ok {
		t.Error("an abandoned run still has a standing force")
	}
	s2.Close()
	if _, err := Open(p, nil); err != nil {
		t.Errorf("the file with forces does not reopen: %v", err)
	}
}

// The HMI's CheckoutPage reads these field names (hmi/src/lib/checkout.ts):
// a renamed one would leave the page showing blanks, not an error.
func TestWireFieldNamesTheHMIReads(t *testing.T) {
	keys := func(v any) map[string]bool {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for k := range m {
			out[k] = true
		}
		return out
	}
	for _, c := range []struct {
		name string
		v    any
		want []string
	}{
		{"Progress", Progress{Total: 1}, []string{"total", "pass", "fail", "noted", "pending"}},
		{"Run", Run{ID: "r"}, []string{"id", "state", "sheet", "at", "by", "results"}},
		{"Result", Result{Run: "r"}, []string{"run", "seq", "at", "point", "outcome", "by"}},
		{"Point", Point{ID: "p", Kind: DO, Method: Force}, []string{"id", "tag", "kind", "method"}},
	} {
		got := keys(c.v)
		for _, w := range c.want {
			if !got[w] {
				t.Errorf("%s has no JSON field %q (the HMI reads it): has %v", c.name, w, got)
			}
		}
	}
}
