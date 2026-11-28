package oplog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// oracleOpen is open() as it was before it streamed the file (#1455): the
// whole file read, scanned with a 1 MiB line limit. It returns what a node
// would hold and the size the file is cut to (-1: not cut). One rule is
// newer than it: a last line with no newline is torn even when it parses
// (the old code kept it, and the next append ran onto it, so the line
// after a restart was damage). The oracle applies that rule too.
func oracleOpen(data []byte) (es []Entry, commit, term uint64, damaged bool, cut int64, err error) {
	good := 0
	body := data
	if i := bytes.LastIndexByte(data, '\n'); i < len(data)-1 {
		body = data[:i+1] // the newer rule: the unterminated tail is torn
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var l fileLine
		if err := json.Unmarshal(line, &l); err != nil {
			break
		}
		good += len(line) + 1
		switch {
		case l.E != nil:
			es = append(es, *l.E)
			term = max(term, l.E.Term)
		case l.C != nil:
			commit = *l.C
		default:
			var e Entry
			if err := json.Unmarshal(line, &e); err == nil && e.Seq > 0 {
				es = append(es, e)
				commit = e.Seq
				continue
			}
			return nil, 0, 0, false, -1, fmt.Errorf("unrecognised")
		}
	}
	cut = -1
	if good < len(data) {
		rest := data[good:]
		if i := bytes.IndexByte(rest, '\n'); i >= 0 && len(bytes.TrimSpace(rest[i+1:])) > 0 {
			damaged = true
		}
		cut = int64(good)
	}
	return es, commit, term, damaged, cut, nil
}

// randomLog is a valid log: entries, each followed (mostly) by its commit
// line, a few in the first (bare) format at the start.
func randomLog(r *rand.Rand, n int) []byte {
	var b bytes.Buffer
	seq := uint64(0)
	if r.IntN(4) == 0 {
		for range 1 + r.IntN(3) {
			seq++
			fmt.Fprintf(&b, `{"seq":%d,"ts":%d,"kind":"ack","ids":["A%d"],"by":"op"}`+"\n", seq, seq, seq)
		}
	}
	term := uint64(1)
	for range n {
		seq++
		if r.IntN(10) == 0 {
			term++
		}
		e := Entry{Seq: seq, Term: term, TS: int64(seq), Kind: KindShelve, ID: fmt.Sprintf("Alarm%d", r.IntN(50)), By: "op", Until: int64(r.IntN(1e6))}
		if r.IntN(5) == 0 {
			e.Kind, e.Value = KindNote, json.RawMessage(fmt.Sprintf(`{"text":%q}`, strings.Repeat("x", r.IntN(300))))
		}
		l, _ := json.Marshal(fileLine{E: &e})
		b.Write(append(l, '\n'))
		if r.IntN(3) > 0 {
			c := seq
			l, _ := json.Marshal(fileLine{C: &c})
			b.Write(append(l, '\n'))
		}
	}
	return b.Bytes()
}

// spoil damages a log the ways a disk or a crash can: cut anywhere, a byte
// changed, a line over the limit, blank lines, trailing space.
func spoil(r *rand.Rand, data []byte) []byte {
	d := append([]byte(nil), data...)
	if len(d) == 0 {
		return d
	}
	// lineEnd is just past a line's newline at or after a random offset
	// (-1: none).
	lineEnd := func() int {
		off := r.IntN(len(d))
		at := bytes.IndexByte(d[off:], '\n')
		if at < 0 {
			return -1
		}
		return off + at + 1
	}
	switch r.IntN(7) {
	case 0:
		return d[:r.IntN(len(d)+1)]
	case 1:
		if len(d) > 0 {
			d[r.IntN(len(d))] = "x{}\n\"0 "[r.IntN(7)]
		}
		return d
	case 2:
		at := lineEnd()
		long := append([]byte(`{"e":{"seq":1,"kind":"note","value":"`), bytes.Repeat([]byte("y"), maxLine)...)
		long = append(long, `"}}`+"\n"...)
		if at < 0 || r.IntN(3) == 0 {
			return append(d, long...) // the last line, or over the limit at the end
		}
		return append(d[:at:at], append(long, d[at:]...)...)
	case 3:
		return append(d, "\n\n  \n"...)
	case 4:
		at := lineEnd()
		if at < 0 {
			return d
		}
		return append(d[:at:at], append([]byte("\n"), d[at:]...)...)
	case 5:
		return d[:max(0, len(d)-1-r.IntN(40))]
	}
	return d
}

// The streaming open reads every file the way the whole-file open did:
// the same entries, commit index and term, the same damage verdict, the
// same applied entries, and the file cut at the same place.
func TestStreamingOpenMatchesWholeFileOpen(t *testing.T) {
	r := rand.New(rand.NewPCG(1455, 1))
	dir := t.TempDir()
	runs := 300
	if testing.Short() {
		runs = 60
	}
	for i := range runs {
		data := randomLog(r, r.IntN(40))
		if i > 0 {
			data = spoil(r, data)
		}
		wantEs, wantCommit, wantTerm, wantDamaged, wantCut, wantErr := oracleOpen(data)
		f := filepath.Join(dir, fmt.Sprintf("log%d.jsonl", i))
		if err := os.WriteFile(f, data, 0o600); err != nil {
			t.Fatal(err)
		}
		var applied []uint64
		n, err := New(Options{Self: "a", File: f, Apply: func(e Entry) error { applied = append(applied, e.Seq); return nil }})
		if (err != nil) != (wantErr != nil) {
			t.Fatalf("run %d: open error %v, the whole-file open's %v", i, err, wantErr)
		}
		if err != nil {
			continue
		}
		got := n.entriesForTest()
		var wantApplied []uint64
		for _, e := range wantEs {
			if e.Seq <= wantCommit && e.Kind != KindNoop {
				wantApplied = append(wantApplied, e.Seq)
			}
		}
		st, _ := os.Stat(f)
		wantSize := int64(len(data))
		if wantCut >= 0 {
			wantSize = wantCut
		}
		switch {
		case !reflect.DeepEqual(got, wantEs) && !(len(got) == 0 && len(wantEs) == 0):
			t.Fatalf("run %d: %d entries, the whole-file open %d", i, len(got), len(wantEs))
		case n.commit != wantCommit || n.term != wantTerm:
			t.Fatalf("run %d: commit %d term %d, want %d %d", i, n.commit, n.term, wantCommit, wantTerm)
		case !reflect.DeepEqual(applied, wantApplied):
			t.Fatalf("run %d: applied %v, want %v", i, applied, wantApplied)
		case st.Size() != wantSize:
			t.Fatalf("run %d: file is %d bytes after the open, want %d (damaged %v)", i, st.Size(), wantSize, wantDamaged)
		}
		n.Close()

		// With a mark, damage is a lost log: open says so, applies
		// nothing and leaves the file whole (openMarked moves it aside).
		if err := os.WriteFile(f, data, 0o600); err != nil {
			t.Fatal(err)
		}
		m := &Node{o: Options{File: f, Mark: f + ".mark"}, log: slog.New(slog.DiscardHandler)}
		damaged, err := m.open()
		if err != nil {
			t.Fatalf("run %d: marked open: %v", i, err)
		}
		if damaged != wantDamaged {
			t.Fatalf("run %d: damaged %v, the whole-file open %v", i, damaged, wantDamaged)
		}
		if damaged {
			st, _ := os.Stat(f)
			if st.Size() != int64(len(data)) || len(m.entries) != 0 || m.commit != 0 {
				t.Fatalf("run %d: a damaged marked log was cut or loaded", i)
			}
		} else {
			m.Close()
		}
	}
}

// A last line that parses but has no newline was never acknowledged (its
// write ends with the newline, then the sync): it is dropped, and the next
// write starts a line of its own. Before #1455 it was kept, the next
// entry was appended onto it, and the following open found a line it
// could not read with more after it: damage, a lost log with a mark.
func TestUnterminatedLastLineIsTorn(t *testing.T) {
	f := filepath.Join(t.TempDir(), "log.jsonl")
	e1, _ := json.Marshal(fileLine{E: &Entry{Seq: 1, Term: 1, Kind: KindAck, IDs: []string{"A"}, By: "op"}})
	c1, _ := json.Marshal(fileLine{C: ptr(uint64(1))})
	e2, _ := json.Marshal(fileLine{E: &Entry{Seq: 2, Term: 1, Kind: KindAck, IDs: []string{"B"}, By: "op"}})
	data := string(e1) + "\n" + string(c1) + "\n" + string(e2) // no newline: torn
	if err := os.WriteFile(f, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := New(Options{Self: "a", File: f})
	if err != nil {
		t.Fatal(err)
	}
	if n.LastSeq() != 1 {
		t.Fatalf("lastSeq %d after an unterminated last line, want 1 (it is torn)", n.LastSeq())
	}
	n.mu.Lock()
	_, err = n.acceptLocked([]Entry{{Seq: 2, Term: 1, Kind: KindAck, IDs: []string{"C"}, By: "op"}})
	n.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	n.Close()
	n2, err := New(Options{Self: "a", File: f})
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	if es := n2.entriesForTest(); len(es) != 2 || es[1].IDs[0] != "C" {
		t.Fatalf("after a write and a restart: %+v, want seq 1 and the new seq 2", es)
	}
}

// ReadCommitted streams the file too and reads it as open does.
func TestReadCommittedStreams(t *testing.T) {
	r := rand.New(rand.NewPCG(1455, 2))
	dir := t.TempDir()
	for i := range 100 {
		data := spoil(r, randomLog(r, r.IntN(30)))
		wantEs, wantCommit, _, wantDamaged, _, wantErr := oracleOpen(data)
		f := filepath.Join(dir, fmt.Sprintf("log%d.jsonl", i))
		if err := os.WriteFile(f, data, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ReadCommitted(f)
		if (err != nil) != (wantDamaged || wantErr != nil) {
			if wantErr != nil && err == nil {
				continue // ReadCommitted skips a line it does not know; open refuses it
			}
			t.Fatalf("run %d: error %v, damaged %v", i, err, wantDamaged)
		}
		if err != nil {
			continue
		}
		var want []Entry
		for _, e := range wantEs {
			if e.Seq <= wantCommit {
				want = append(want, e)
			}
		}
		if len(got) != len(want) || len(want) > 0 && !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: %d committed entries, want %d", i, len(got), len(want))
		}
	}
}

func ptr[T any](v T) *T { return &v }
