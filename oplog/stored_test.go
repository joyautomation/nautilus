package oplog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// An entry another build wrote (its keys in another order, a field this
// build does not know) is the same entry as the one the leader sends: a
// duplicate at a committed seq is skipped, never a conflict (#1455: the
// node compares the bytes it keeps first, then as Entry.same does).
func TestSameEntryFromAnotherBuildIsNotAConflict(t *testing.T) {
	f := filepath.Join(t.TempDir(), "log.jsonl")
	line := `{"e":{"term":1,"seq":1,"by":"op","ids":["X"],"kind":"ack","ts":5,"fromANewerBuild":"kept"}}` + "\n" + `{"c":1}` + "\n"
	if err := os.WriteFile(f, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := New(Options{Self: "a", File: f})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	n.mu.Lock()
	fresh, err := n.acceptLocked([]Entry{{Seq: 1, Term: 1, TS: 5, Kind: KindAck, IDs: []string{"X"}, By: "op"}})
	n.mu.Unlock()
	if err != nil || fresh != 0 {
		t.Fatalf("the same entry, written by another build: fresh %d, error %v; want skipped", fresh, err)
	}
	n.mu.Lock()
	_, err = n.acceptLocked([]Entry{{Seq: 1, Term: 1, TS: 5, Kind: KindAck, IDs: []string{"Y"}, By: "op"}})
	n.mu.Unlock()
	if err == nil {
		t.Fatal("a different entry at a committed seq was not a conflict")
	}
}

// Only entries that may be a lapse are decoded to find what they lapse:
// one written with its kind behind a \u escape is still found.
func TestMayLapseFindsEveryLapse(t *testing.T) {
	for _, raw := range []string{
		`{"seq":3,"kind":"lapse","refs":[1]}`,
		`{"seq":3,"kind":"\u006capse","refs":[1]}`,
	} {
		var e Entry
		if err := json.Unmarshal([]byte(raw), &e); err != nil || e.Kind != KindLapse {
			t.Fatalf("%s: %v", raw, err)
		}
		if !(stored{raw: []byte(raw)}).mayLapse() {
			t.Fatalf("%s is a lapse, and mayLapse says it is not", raw)
		}
	}
	if (stored{raw: []byte(`{"seq":3,"kind":"ack","ids":["X"]}`)}).mayLapse() {
		t.Fatal("an ack is not a lapse")
	}
}

// What the node keeps per entry (#1455): its JSON and some 40 to 100
// bytes more (number, term, slice header, the slice's spare room). A
// decoded Entry kept about 290 bytes more than its JSON (a 304-byte
// struct, most of it empty, and its strings), so a return to decoded
// entries fails the bound.
func TestKeptHeapPerEntry(t *testing.T) {
	const total = 20_000
	f := filepath.Join(t.TempDir(), "log.jsonl")
	out, err := os.Create(f)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(out)
	rawBytes := 0
	for seq := uint64(1); seq <= total; seq++ {
		id := fmt.Sprintf("Area%d/Unit%02d/Pump%03d.HighPressure", seq%4, seq%30, seq%200)
		e := Entry{Seq: seq, Term: 1, TS: 1767225600000 + int64(seq)*36000, Kind: KindAck, IDs: []string{id}, By: "operator1",
			Origin: "http://10.0.0.1:8080", Principal: "user:operator1", Occs: []Occ{{ID: id, Occ: fmt.Sprintf("lzx%d-%d", seq, seq%1000), ActiveMs: 1767225000000}}}
		b, _ := json.Marshal(fileLine{E: &e})
		w.Write(append(b, '\n'))
		rawBytes += len(b) - len(`{"e":}`)
		b, _ = json.Marshal(fileLine{C: &seq})
		w.Write(append(b, '\n'))
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	out.Close()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	n, err := New(Options{Self: "a", File: f, Apply: func(Entry) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	runtime.GC()
	runtime.ReadMemStats(&after)
	per := (int64(after.HeapAlloc) - int64(before.HeapAlloc)) / total
	raw := int64(rawBytes / total)
	t.Logf("kept heap: %d bytes per entry, its JSON %d", per, raw)
	if per > raw+120 {
		t.Fatalf("the open log keeps %d bytes of heap per entry, its JSON %d: want at most 120 more", per, raw)
	}
	runtime.KeepAlive(n)
}
