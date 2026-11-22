package oplog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Operator notes (#720) are kept for years, so a copy that was wiped and
// rejoins must be brought every one back, not the recent tail: the log is
// never compacted, and catch-up sends it all, in pages. Both the witness
// (rebuilt from scratch) and a controller (its log lost) are filled, and
// the note body arrives whole (a 4 KiB text, its revisions, in order).
func TestEveryNoteComesBackToAWipedReplica(t *testing.T) {
	// small pages: the catch-up must page, not send one request
	tr := newTrio(t, withLimits(limits{chunkEntries: 7, chunkBytes: 1 << 15, body: 64 << 10, resp: 64 << 10}))
	mustSync(t, tr, 0)
	long := strings.Repeat("é", 4096)
	const notes = 60
	for i := 0; i < notes; i++ {
		id := fmt.Sprintf("note-%03d", i)
		text := fmt.Sprintf("note %d", i)
		if i%10 == 0 {
			text = long
		}
		if _, err := tr.nodes[0].Propose(tr.ctx(), withPrincipal(NoteEntryOf("alice", NoteEntry{ID: id, Rev: 1, TargetKind: "log", Text: text}))); err != nil {
			t.Fatalf("note %d: %v", i, err)
		}
		if i%3 == 0 { // a revision and an acknowledgement between
			if _, err := tr.nodes[0].Propose(tr.ctx(), withPrincipal(NoteEntryOf("alice", NoteEntry{ID: id, Rev: 2, TargetKind: "log", Text: text + " (edited)"}))); err != nil {
				t.Fatal(err)
			}
			if _, err := tr.propose(0, fmt.Sprintf("ack-%d", i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := noteEntries(tr.nodes[0].After(0))
	if len(want) != notes+notes/3 {
		t.Fatalf("the leader holds %d note entries, want %d", len(want), notes+notes/3)
	}

	tr.wipe(2) // the witness rebuilt from scratch: filled by the leader's heartbeats
	tr.lead(0)
	mustSync(t, tr, 0)
	for i := 0; i < 80 && tr.nodes[2].LastSeq() < tr.nodes[0].LastSeq(); i++ {
		tr.nodes[0].Heartbeat(tr.ctx())
	}
	tr.wipe(1) // a controller that lost everything: its own paged catch-up
	if err := tr.nodes[1].CatchUp(tr.ctx()); err != nil {
		t.Fatalf("the controller's catch-up: %v", err)
	}
	tr.nodes[0].Heartbeat(tr.ctx()) // the commit index
	for _, i := range []int{1, 2} {
		got := noteEntries(tr.nodes[i].After(0))
		if len(got) != len(want) {
			t.Fatalf("replica %d holds %d note entries after rejoining, want %d (the whole history, not a tail)", i, len(got), len(want))
		}
		for k := range want {
			g, _ := got[k].NoteBody()
			w, _ := want[k].NoteBody()
			if g != w || got[k].Principal != want[k].Principal {
				t.Fatalf("replica %d entry %d differs: %+v vs %+v", i, k, g, w)
			}
		}
		if n := len(noteEntries(tr.applied[i])); n != len(want) {
			t.Errorf("replica %d applied %d note entries, want %d", i, n, len(want))
		}
	}
	// and a restart replays them all into Apply again, the way the index is rebuilt
	tr.restart(1)
	if n := len(noteEntries(tr.applied[1])); n != len(want) {
		t.Errorf("a restart replayed %d note entries into Apply, want %d", n, len(want))
	}
}

func noteEntries(es []Entry) []Entry {
	var out []Entry
	for _, e := range es {
		if _, ok := e.NoteBody(); ok {
			out = append(out, e)
		}
	}
	return out
}

func withPrincipal(e Entry) Entry {
	e.Principal = "user:alice"
	return e
}

// legacyEntry is Entry as a build from before notes had it: it knows the
// substitute fields, among them Value, and not a note's. A replica of that
// build decodes the replicated entries into it and writes them back out.
type legacyEntry struct {
	Seq       uint64          `json:"seq"`
	Term      uint64          `json:"term"`
	TS        int64           `json:"ts"`
	Kind      string          `json:"kind"`
	IDs       []string        `json:"ids,omitempty"`
	ID        string          `json:"id,omitempty"`
	Until     int64           `json:"until,omitempty"`
	By        string          `json:"by"`
	Origin    string          `json:"origin,omitempty"`
	Principal string          `json:"principal,omitempty"`
	Tag       string          `json:"tag,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
	GrantedMs int64           `json:"grantedMs,omitempty"`
	Ref       uint64          `json:"ref,omitempty"`
	Refs      []uint64        `json:"refs,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Req       string          `json:"req,omitempty"`
}

// A replica from before notes stores and sends a note entry whole: the
// body travels in Value, a field it preserves, so a rolling upgrade, a
// failover to an old controller, or a witness not yet upgraded loses no
// note text (a field it did not know would be dropped by its typed
// re-marshal, for good).
func TestAnOlderReplicaKeepsANoteEntryWhole(t *testing.T) {
	e := withPrincipal(NoteEntryOf("alice", NoteEntry{ID: "n1", Rev: 2, TargetKind: "alarm", TargetID: "HiTemp", Occurrence: "00ff", Text: "bearing, \"checked\"", Re: "n0"}))
	e.Seq, e.Term, e.TS, e.Origin = 7, 1, 1700000000000, "http://a"
	wire, _ := json.Marshal(e)
	var old legacyEntry
	if err := json.Unmarshal(wire, &old); err != nil {
		t.Fatal(err)
	}
	stored, _ := json.Marshal(old) // what the old replica writes and sends on
	var back Entry
	if err := json.Unmarshal(stored, &back); err != nil {
		t.Fatal(err)
	}
	got, ok := back.NoteBody()
	want, _ := e.NoteBody()
	if !ok || got != want {
		t.Fatalf("the body came back as %+v (ok %v), want %+v", got, ok, want)
	}
	if back.Principal != "user:alice" || back.Seq != 7 {
		t.Errorf("%+v", back)
	}
	// and an entry that lost its body is not a note: the notes say so loudly
	back.Value = nil
	if _, ok := back.NoteBody(); ok {
		t.Error("a bodiless note entry reads as a note")
	}
}
