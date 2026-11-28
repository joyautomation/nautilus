package main

import (
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/quorum/node"
)

// #1455 PR 2a-2 in the pair's wiring: a controller whose log diverged (a
// committed compaction did not fit it) restarts itself under the
// supersede rule, once per start; and the log compaction's conditions.

func TestADivergedControllerRestartsItselfOnlyUnderTheRule(t *testing.T) {
	own := oplog.Lineage{History: 2, HistoryID: 0xbeef}
	peer := node.PeerLog{Synced: true, Leads: true, History: 2, HistoryID: 0xbeef}
	const why = "the compaction at seq 9 does not fit"
	for _, tc := range []struct {
		name                         string
		diverged                     string
		leading                      bool
		pl                           node.PeerLog
		supervised, atStart, restart bool
		want                         string // in the condition; "" none
	}{
		{"not diverged", "", false, peer, true, false, false, ""},
		{"every condition holds", why, false, peer, true, false, true, "restarting now"},
		{"it leads", why, true, peer, true, false, false, "it leads"},
		{"the other is at another history", why, false, node.PeerLog{Synced: true, Leads: true, History: 3}, true, false, false, "does not lead synced"},
		{"no supervisor", why, false, peer, false, false, false, "no supervisor"},
		{"restarted once already", why, false, peer, true, true, false, "already restarted once for a diverged log"},
	} {
		msg, restart := divergedDecision(tc.diverged, own, tc.pl, true, tc.leading, tc.supervised, tc.atStart)
		switch {
		case restart != tc.restart:
			t.Errorf("%s: restart %v, want %v (%q)", tc.name, restart, tc.restart, msg)
		case tc.want == "" && msg != "":
			t.Errorf("%s: condition %q, want none", tc.name, msg)
		case tc.want != "" && (!strings.Contains(msg, tc.want) || !strings.Contains(msg, why) || strings.Contains(msg, "vouch")):
			t.Errorf("%s: condition %q, want %q and why, no vouch", tc.name, msg, tc.want)
		}
	}
}

func TestTheLogCompactionsConditions(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	disk, behind, blocked := compactConditions(oplog.Status{}, now)
	if disk != "" || behind != "" || blocked != "" {
		t.Fatalf("a log with nothing to say: %q %q %q", disk, behind, blocked)
	}
	var st oplog.Status
	st.CompactRefused = "/var/lib/naut: 3 MB free, the log is 9 MB: the compaction waits for at least 18 MB free"
	st.BehindBase = []string{"b: it runs a build from before log compaction and missed the compacted part (it has 0, the base is at 30): update it"}
	st.CompactBlocked = "waiting for the witness"
	st.CompactBlockedSince = now.Add(-6 * 24 * time.Hour).UnixMilli()
	disk, behind, blocked = compactConditions(st, now)
	if !strings.Contains(disk, "3 MB free") || !strings.Contains(behind, "b: it runs a build from before") || blocked != "" {
		t.Fatalf("disk %q, behind %q, blocked %q (6 days: not yet)", disk, behind, blocked)
	}
	st.CompactBlockedSince = now.Add(-8 * 24 * time.Hour).UnixMilli()
	if _, _, blocked = compactConditions(st, now); !strings.Contains(blocked, "8 days") || !strings.Contains(blocked, "waiting for the witness") {
		t.Fatalf("blocked 8 days: %q", blocked)
	}
}

// A diverged log is unsynced, but its condition is oplog-diverged's
// restart, never the unsynced one's vouch.
func TestADivergedLogIsNotToldToVouch(t *testing.T) {
	st := oplog.Status{Unsynced: true}
	if _, unsynced := syncConditions(st); !strings.Contains(unsynced, "vouch") {
		t.Fatalf("an unsynced log: %q", unsynced)
	}
	st.Diverged = "the compaction at seq 9 does not fit"
	if lost, unsynced := syncConditions(st); lost != "" || unsynced != "" {
		t.Fatalf("a diverged log: lost %q, unsynced %q; want neither", lost, unsynced)
	}
}
