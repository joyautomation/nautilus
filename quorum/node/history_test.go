package node

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// #1029 3c-3c-2a in the election: the hello carries the log's history, so
// a copy at a lower history (a loss a person accepted) or the same number
// under another id (a fork) is never a log to defer or yield to; and the
// operator's vouch, log only.

// The history rides on the log report only when above 0, so a pair at
// history 0 sends what an older build reads; a malformed suffix is
// dropped whole.
func TestTheHelloCarriesTheLogHistory(t *testing.T) {
	if r := logReport(true, true, true, 0, 0); r != "scm" {
		t.Fatalf("history 0: %q", r)
	}
	r := logReport(true, false, true, 3, 0xabc)
	if r != "sxm3.abc" {
		t.Fatalf("history 3: %q", r)
	}
	e := &Elector{}
	e.heardLog(r)
	if p := e.peerLog; !p.synced || p.canSync || !p.may || p.history != 3 || p.historyID != 0xabc {
		t.Fatalf("heard %q as %+v", r, p)
	}
	for _, bad := range []string{"scm3", "scm.1", "scmx.1", "scm3.zz"} {
		e := &Elector{}
		e.heardLog(bad)
		if !e.peerLog.at.IsZero() {
			t.Fatalf("a malformed report %q was taken: %+v", bad, e.peerLog)
		}
	}
}

// The other controller's synced log is one to defer and yield to only at
// this copy's history, or a higher one (which supersedes this copy): not
// at a lower one, and not forked. lower_history_peer_synced turns it red.
func TestNoDeferOrYieldToALowerOrForkedHistory(t *testing.T) {
	c, l := logCluster(t)
	e := c.elector(l)
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	if !e.c.Leading(now) {
		t.Skip("the leader lapsed meanwhile")
	}
	own := LogState{Stuck: true, History: 2, HistoryID: 7}
	for _, tc := range []struct {
		name        string
		history, id uint64
		yields      bool
	}{
		{"the same history", 2, 7, true},
		{"a higher history", 3, 9, true},
		{"a lower history", 1, 5, false},
		{"history 0 (an older build, or never accepted)", 0, 0, false},
		{"a fork: the same number, another id", 2, 8, false},
	} {
		e.peerLog = peerLog{synced: true, canSync: true, may: true, history: tc.history, historyID: tc.id, at: time.Now()}
		e.yieldedAt, e.yieldHold = time.Time{}, false
		e.maybeYield(now, own, true)
		if e.yieldHold != tc.yields {
			t.Fatalf("%s: yielded %v, want %v", tc.name, e.yieldHold, tc.yields)
		}
		e.yieldHold = false
		standby := own
		standby.Stuck = false
		if got := e.logDefers(now, standby, true) != deferNo; got != tc.yields {
			t.Fatalf("%s: defers %v, want %v", tc.name, got, tc.yields)
		}
	}
}

// naut redundancy vouch: log only, on a commissioned controller that is
// not a spare; the log's picture and refusal come back in the report.
func TestTheVouchIsLogOnly(t *testing.T) {
	c, l := logCluster(t)
	e := c.elector(l)
	var gotInc uint64
	var gotAcc LogAccept
	e.SetVouch(func(_ context.Context, by string, inc uint64, acc LogAccept) (json.RawMessage, error) {
		gotInc, gotAcc = inc, acc
		if acc.Loss == "" {
			return json.RawMessage(`{"digest":"0123456789"}`), errors.New("needs --accept-loss=0123456789")
		}
		return json.RawMessage(`{"digest":"0123456789"}`), nil
	})
	inc := e.Status().Incarnation
	rep, err := e.Vouch(opCtx(t), "op", LogAccept{})
	if !errors.Is(err, ErrCommandRefused) || !strings.Contains(rep.Refused, "--accept-loss=0123456789") || !strings.Contains(string(rep.Picture), "0123456789") {
		t.Fatalf("a refused vouch: %+v, %v", rep, err)
	}
	if gotInc != inc || gotInc == 0 {
		t.Fatalf("the vouch judged the witness at incarnation %x, the pair's is %x", gotInc, inc)
	}
	if rep, err := e.Vouch(opCtx(t), "op", LogAccept{Loss: "0123456789"}); err != nil || gotAcc.Loss != "0123456789" {
		t.Fatalf("an accepting vouch: %+v, %v", rep, err)
	}
	if e.Status().Incarnation != inc || c.leader() != l {
		t.Fatal("the vouch changed the election")
	}

	e.spare.Store(true)
	if rep, err := e.Vouch(opCtx(t), "op", LogAccept{}); !errors.Is(err, ErrCommandRefused) || !strings.Contains(rep.Refused, "spare") {
		t.Fatalf("a vouch on a spare: %+v, %v", rep, err)
	}
	e.spare.Store(false)

}

// An uncommissioned controller's log takes no vouch of its own: commission
// or recommission vouch for it.
func TestAnUncommissionedControllerTakesNoVouch(t *testing.T) {
	u := newCluster(t)
	u.startAll()
	ue := u.elector("a")
	ue.SetVouch(func(context.Context, string, uint64, LogAccept) (json.RawMessage, error) {
		t.Error("an uncommissioned controller's log was vouched for")
		return nil, nil
	})
	if rep, err := ue.Vouch(opCtx(t), "op", LogAccept{}); !errors.Is(err, ErrCommandRefused) || !strings.Contains(rep.Refused, "not commissioned") {
		t.Fatalf("a vouch on an uncommissioned controller: %+v, %v", rep, err)
	}
}
