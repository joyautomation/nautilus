package oplog

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// #1029 3c-3c-2b, S2: a new pair whose controller restarted before its
// first catch-up. The boot rule drops the commission's one-shot at the
// start, so the copy is synced but alone among the copies; nothing any
// copy holds can be lost, so a plain vouch (no --accept-loss) grants the
// one-shot again, and only while all four conditions hold: every other
// copy answers, none was lost, every copy is at history 0, every log is
// empty.

// freshPairRestarted is that pair: three empty logs, commission's plain
// vouch on n0, n0 restarted before its first catch-up and leading again.
func freshPairRestarted(t *testing.T) *trio {
	t.Helper()
	tr := newTrio(t, func(i int, o *Options) {
		o.UnmarkedSynced = false // a new pair: no log file, so not synced
		witnessInPlace(i, o)
	})
	tr.lead(0)
	if _, pic, err := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{}); err != nil {
		t.Fatalf("commission's vouch on a new pair: %v %+v", err, pic)
	}
	tr.restart(0)
	tr.lead(0)
	if err := tr.nodes[0].SyncLeader(tr.ctx()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("setup: the restarted copy's catch-up: %v, want read-only", err)
	}
	return tr
}

// The plain vouch grants the one-shot; the copy syncs and writes
// (empty_pair_needs_accept).
func TestANewPairsPlainVouchAfterARestartNeedsNoAcceptLoss(t *testing.T) {
	tr := freshPairRestarted(t)
	if _, pic, err := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{}); err != nil {
		t.Fatalf("plain vouch on a new pair restarted before its first catch-up: %v %+v", err, pic)
	}
	mustSync(t, tr, 0)
	if _, err := tr.propose(0, "X"); err != nil {
		t.Fatal(err)
	}
	if m := tr.mark(0); m.History != 0 {
		t.Fatalf("a catch-up that overrode nothing started a history: %+v", m)
	}
}

// Each condition failing alone refuses the plain vouch, and the refusal
// names it (empty_pair_unchecked).
func TestANewPairsPlainVouchNamesTheConditionThatFails(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		set        func(tr *trio)
	}{
		{"a copy silent", "n1 does not answer", func(tr *trio) { tr.setDown(1, true) }},
		{"a copy lost", "n1's log was lost", func(tr *trio) {
			// Lost under a held mark: it acknowledged entries once.
			tr.editMark(1, func(m *Mark) { m.Held = true })
			tr.loseLog(1)
		}},
		{"a copy that set aside a log", "n1 set aside another identity's log that held entries", func(tr *trio) {
			// n1 restarts under another identity over a log that held
			// something: its entries are set aside, and it answers empty.
			tr.stop(1)
			if err := os.WriteFile(tr.logFile(1), []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			tr.ident[1] = "n1-other"
			tr.start(1)
		}},
		{"a copy that set aside a log, then changed again", "n1 set aside another identity's log that held entries", func(tr *trio) {
			// A second change before it synced: the first change's entries
			// are still set aside (set_aside_forgotten).
			tr.stop(1)
			if err := os.WriteFile(tr.logFile(1), []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			tr.ident[1] = "n1-other"
			tr.start(1)
			tr.stop(1)
			tr.ident[1] = "n1-third"
			tr.start(1)
		}},
		{"a history", "n0 is at history 1", func(tr *trio) {
			tr.editMark(0, func(m *Mark) { m.Lineage = Lineage{History: 1, HistoryID: 0x41, HistoryBy: "op"} })
			tr.lead(0)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := freshPairRestarted(t)
			tc.set(tr)
			plainVouchRefused(t, tr, tc.want)
		})
	}
	// A log holding an entry: the pair committed X on all three, then the
	// other two copies were rebuilt (fresh, empty).
	t.Run("a log not empty", func(t *testing.T) {
		tr := newTrio(t, nil)
		mustSync(t, tr, 0)
		if _, err := tr.propose(0, "X"); err != nil {
			t.Fatal(err)
		}
		tr.wipe(1)
		tr.wipe(2)
		tr.lead(0)
		plainVouchRefused(t, tr, "n0's log holds entries (to seq 1)")
	})
}

func plainVouchRefused(t *testing.T, tr *trio, want string) {
	t.Helper()
	_, pic, err := tr.nodes[0].Vouch(tr.ctx(), "op", nil, VouchAccept{})
	if !errors.Is(err, ErrVouchRefused) || !strings.Contains(strings.Join(pic.Why, "; "), "a plain vouch serves only a new pair") ||
		!strings.Contains(strings.Join(pic.Why, "; "), want) {
		t.Fatalf("plain vouch: %v, needs %v, why %q: want refused, naming %q", err, pic.Needs, pic.Why, want)
	}
}
