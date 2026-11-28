//go:build !js

package oplog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Builds from before compaction must refuse a compacted file, showing the
// fix, and their ReadCommitted (naut notes export) must still return every
// committed note (#1455, docs/design/oplog-compaction.md §4). Two readers
// shipped: v0.23.0's (the whole file read, then scanned) and the streaming
// one after it (#1469). Each is copied here from its release, as an oracle,
// with the file-line type as it was then (no header field).

type oldFileLine struct {
	E *Entry  `json:"e,omitempty"`
	C *uint64 `json:"c,omitempty"`
}

// v0230Open is v0.23.0's open, the parse only: its error, or nil.
func v0230Open(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var l oldFileLine
		if err := json.Unmarshal(line, &l); err != nil {
			break
		}
		switch {
		case l.E != nil, l.C != nil:
		default:
			var e Entry
			if err := json.Unmarshal(line, &e); err == nil && e.Seq > 0 {
				continue
			}
			return fmt.Errorf("oplog: %s: unrecognised line %q", path, truncateStr(string(line), 80))
		}
	}
	return nil
}

// v0230ReadCommitted is v0.23.0's ReadCommitted.
func v0230ReadCommitted(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var es []Entry
	var commit uint64
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	good := 0
	for sc.Scan() {
		line := sc.Bytes()
		var l oldFileLine
		if err := json.Unmarshal(line, &l); err != nil {
			if rest := data[good:]; bytes.IndexByte(rest, '\n') >= 0 && len(bytes.TrimSpace(rest[bytes.IndexByte(rest, '\n')+1:])) > 0 {
				return nil, fmt.Errorf("oplog: %s: unrecognised line %q", path, truncateStr(string(line), 80))
			}
			break
		}
		good += len(line) + 1
		switch {
		case l.E != nil:
			es = append(es, *l.E)
		case l.C != nil:
			commit = *l.C
		default:
			var e Entry
			if err := json.Unmarshal(line, &e); err == nil && e.Seq > 0 {
				es = append(es, e)
				commit = e.Seq
			}
		}
	}
	out := es[:0]
	for _, e := range es {
		if e.Seq <= commit {
			out = append(out, e)
		}
	}
	return out, nil
}

// streamingOpen is the streaming open from before compaction (#1469), the
// parse only. storedLine is unchanged since.
func streamingOpen(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, _, _, err = scanLines(f, func(line []byte) (bool, error) {
		e, c, _, ok := storedLine(line)
		switch {
		case !ok:
			return false, nil
		case e != nil, c != nil:
		default:
			return false, fmt.Errorf("oplog: %s: unrecognised line %q", path, truncateStr(string(line), 80))
		}
		return true, nil
	})
	return err
}

// streamingReadCommitted is the streaming ReadCommitted from before
// compaction (#1469).
func streamingReadCommitted(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var es []Entry
	var commit uint64
	var bad string
	_, _, damaged, err := scanLines(f, func(line []byte) (bool, error) {
		var l oldFileLine
		if err := json.Unmarshal(line, &l); err != nil {
			bad = truncateStr(string(line), 80)
			return false, nil
		}
		switch {
		case l.E != nil:
			es = append(es, *l.E)
		case l.C != nil:
			commit = *l.C
		default:
			var e Entry
			if err := json.Unmarshal(line, &e); err == nil && e.Seq > 0 {
				es = append(es, e)
				commit = e.Seq
			}
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if damaged {
		return nil, fmt.Errorf("oplog: %s: unrecognised line %q", path, bad)
	}
	out := es[:0]
	for _, e := range es {
		if e.Seq <= commit {
			out = append(out, e)
		}
	}
	return out, nil
}

func TestOldBuildsRefuseACompactedLogAndStillExportItsNotes(t *testing.T) {
	tr := compactTrio(t, nil)
	mustSync(t, tr.trio, 0)
	full := tr.fill(0, 40, 1)
	through := tr.nodes[0].Commit() - 3
	keep := toyKeep(full, through, 0)
	tr.compact(0, through, keep)
	f := tr.logFile(1)
	first, _ := os.ReadFile(f)
	if !bytes.HasPrefix(first, []byte(`{"b":`)) {
		t.Fatalf("the compacted file does not start with its header: %.80q", first)
	}
	for name, open := range map[string]func(string) error{"v0.23.0": v0230Open, "streaming": streamingOpen} {
		err := open(f)
		if err == nil {
			t.Fatalf("%s's reader opened a compacted log", name)
		}
		// What a person sees: the fix, inside the 80 bytes shown.
		if !strings.Contains(err.Error(), "update naut, or export notes and start a new log") {
			t.Fatalf("%s's refusal does not show the fix: %v", name, err)
		}
	}
	wantNotes := notesOf(tr.nodes[0].After(0))
	if len(wantNotes) == 0 {
		t.Fatal("the test log holds no notes")
	}
	for name, read := range map[string]func(string) ([]Entry, error){"v0.23.0": v0230ReadCommitted, "streaming": streamingReadCommitted, "this build": ReadCommitted} {
		es, err := read(f)
		if err != nil {
			t.Fatalf("%s's ReadCommitted: %v", name, err)
		}
		if got := notesOf(es); !sameStrings(got, wantNotes) {
			t.Fatalf("%s's ReadCommitted exports notes %v, the log holds %v", name, got, wantNotes)
		}
	}
}

func notesOf(es []Entry) []string {
	var out []string
	for _, e := range es {
		if n, ok := e.NoteBody(); ok {
			out = append(out, fmt.Sprintf("%s/%d", n.ID, n.Rev))
		}
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestOldBuildsReadAndKeepTheRulesMarker (#1455 2b, the lead's ruling on
// blocked-since): the noop a leader on compaction's rules writes to mark
// its term is read by both readers from before compaction, opened and
// replayed as any noop, and a follower on such a build, which re-marshals
// what it takes and drops fields it doesn't know, keeps the marker: it is
// in Value.
func TestOldBuildsReadAndKeepTheRulesMarker(t *testing.T) {
	tr := decideTrio(t, nil)
	if err := tr.nodes[0].SyncLeader(tr.ctx()); err != nil {
		t.Fatal(err)
	}
	tr.nodes[0].markRules(tr.ctx())
	tr.fill(0, 5, 1)
	tr.beat(0)
	f := tr.logFile(1)
	for name, open := range map[string]func(string) error{"v0.23.0": v0230Open, "streaming": streamingOpen} {
		if err := open(f); err != nil {
			t.Fatalf("%s's reader refuses a log with the marker: %v", name, err)
		}
	}
	for name, read := range map[string]func(string) ([]Entry, error){"v0.23.0": v0230ReadCommitted, "streaming": streamingReadCommitted} {
		es, err := read(f)
		if err != nil {
			t.Fatalf("%s's ReadCommitted: %v", name, err)
		}
		if len(es) != 6 || es[0].Kind != KindNoop || string(es[0].Value) != `{"rules":1}` {
			t.Fatalf("%s's ReadCommitted: %+v", name, es)
		}
	}
	// A build that knows only the fields of before keeps Value whole.
	var old struct {
		Seq    uint64          `json:"seq"`
		Term   uint64          `json:"term"`
		TS     int64           `json:"ts"`
		Kind   string          `json:"kind"`
		By     string          `json:"by"`
		Origin string          `json:"origin,omitempty"`
		Value  json.RawMessage `json:"value,omitempty"`
	}
	raw, _ := json.Marshal(tr.nodes[1].After(0)[0])
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(old)
	var back Entry
	if err := json.Unmarshal(again, &back); err != nil || string(back.Value) != `{"rules":1}` {
		t.Fatalf("re-marshalled by an older build: %s (%v)", again, err)
	}
	if ts, ok := firstRulesTerm([]stored{storedOf(back)}); !ok || ts != back.TS {
		t.Fatalf("the re-marshalled marker is not found: %d %t", ts, ok)
	}
}
