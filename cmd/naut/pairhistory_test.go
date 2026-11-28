package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/quorum/node"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
)

// #1029 3c-3c-2a in the pair's wiring: a superseded controller restarts
// itself only under ruling 20d: standby, the other controller leading
// synced at the history it takes, under a supervisor, and not already
// restarted for a superseded log since it started.

func TestASupersededControllerRestartsItselfOnlyUnderTheRule(t *testing.T) {
	to := oplog.Lineage{History: 2, HistoryID: 0xbeef}
	peer := node.PeerLog{Synced: true, Leads: true, History: 2, HistoryID: 0xbeef}
	for _, tc := range []struct {
		name                        string
		leading                     bool
		pl                          node.PeerLog
		heard                       bool
		supervised, restartedForOne bool
		want                        string // "" restarts
	}{
		{"every condition holds", false, peer, true, true, false, ""},
		{"it leads", true, peer, true, true, false, "it leads"},
		{"the other is not heard", false, peer, false, true, false, "does not lead synced"},
		{"the other does not lead", false, node.PeerLog{Synced: true, History: 2, HistoryID: 0xbeef}, true, true, false, "does not lead synced"},
		{"the other is not synced", false, node.PeerLog{Leads: true, History: 2, HistoryID: 0xbeef}, true, true, false, "does not lead synced"},
		{"the other is at another history", false, node.PeerLog{Synced: true, Leads: true, History: 3, HistoryID: 1}, true, true, false, "does not lead synced"},
		{"the other is at a fork of it", false, node.PeerLog{Synced: true, Leads: true, History: 2, HistoryID: 1}, true, true, false, "does not lead synced"},
		{"no supervisor", false, peer, true, false, false, "no supervisor"},
		{"restarted once already", false, peer, true, true, true, "already restarted once"},
	} {
		got := supersedeRule(tc.leading, tc.pl, tc.heard, to, tc.supervised, tc.restartedForOne, "a superseded log")
		if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// fakeLearner is a log for learnFromHello.
type fakeLearner struct {
	own     oplog.Lineage
	learned []oplog.Lineage
}

func (f *fakeLearner) History() (oplog.Lineage, oplog.Lineage) { return f.own, oplog.Lineage{} }
func (f *fakeLearner) LearnHistory(l oplog.Lineage) error {
	f.learned = append(f.learned, l)
	return nil
}

// The other controller's hello carries its log history: a higher one is
// learnt from first contact (the copy then counts for nothing), a lower or
// equal one, or a report older than a lease, is not.
func TestAHigherHistoryInTheHelloIsLearnt(t *testing.T) {
	f := &fakeLearner{own: oplog.Lineage{History: 1, HistoryID: 5}}
	learnFromHello(f, node.PeerLog{Synced: true, History: 2, HistoryID: 9}, true)
	if len(f.learned) != 1 || f.learned[0].History != 2 || f.learned[0].HistoryID != 9 {
		t.Fatalf("learned %+v", f.learned)
	}
	f.learned = nil
	learnFromHello(f, node.PeerLog{History: 3, HistoryID: 1}, false)
	learnFromHello(f, node.PeerLog{History: 1, HistoryID: 7}, true)
	learnFromHello(f, node.PeerLog{History: 0}, true)
	if len(f.learned) != 0 {
		t.Fatalf("learned %+v from a stale, forked or lower report", f.learned)
	}
}

// A start that moved a superseded log aside drops the retain store's
// substitutes (the old history's) before the runtime runs, and records it
// done (until then the mark keeps it owed, so a start that dies in
// between drops them at the next).
func TestAStartAfterASupersedeDropsTheRetainedSubstitutes(t *testing.T) {
	dir := t.TempDir()
	logFile, markFile, retainFile := filepath.Join(dir, "oplog.jsonl"), filepath.Join(dir, "oplog.mark"), filepath.Join(dir, "retain.json")
	store := retain.New("", retainFile)
	if err := store.Save(retain.State{Substitutes: &retain.SubstituteState{Entries: []retain.SubstituteRetain{{ID: 3, Name: "Level", Value: 95.0}}, HighID: 3}}); err != nil {
		t.Fatal(err)
	}
	mark, _ := json.Marshal(oplog.Mark{Identity: "c/a/box", Held: true, Synced: true, SupersedeTo: 1, SupersedeID: 7})
	if err := os.WriteFile(markFile, mark, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logFile, []byte(`{"e":{"seq":3,"term":1,"ts":1,"kind":"substitute","tag":"Level","by":"op"}}`+"\n"+`{"c":3}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	open := func() *oplog.Node {
		n, err := oplog.New(oplog.Options{Self: "a", File: logFile, Mark: markFile, Identity: "c/a/box"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { n.Close() })
		return n
	}
	rt, err := runtime.New(runtime.Options{Program: "PROGRAM Main\nEND_PROGRAM\n", Retain: store})
	if err != nil {
		t.Fatal(err)
	}
	n := open()
	if !n.RetainedStale() {
		t.Fatal("setup: nothing owed after the supersede")
	}
	if err := dropStaleRetained(n, rt); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Load()
	if st.Substitutes == nil || len(st.Substitutes.Entries) != 0 || st.Substitutes.HighID != 3 {
		t.Fatalf("the retain store after the start: %+v", st.Substitutes)
	}
	if n.RetainedStale() {
		t.Fatal("the drop is still owed after it was done")
	}
	if m, _ := os.ReadFile(markFile); strings.Contains(string(m), "dropRetained") {
		t.Fatalf("the mark still owes the drop: %s", m)
	}
}

// naut redundancy vouch: a plain run prints each copy, what would be lost
// and the exact command that accepts it; the accepting run sends the
// digest; --from-spare takes no acceptance.
func TestTheVouchCommandPrintsThePictureAndTheCommand(t *testing.T) {
	var got []map[string]any
	pic := oplog.Picture{
		Self:   oplog.CopyState{Name: "controller a", Answers: true, Counts: true, Synced: true, LastTerm: 3, LastSeq: 12},
		Copies: []oplog.CopyState{{Name: "controller b", Err: "connection refused"}, {Name: "the witness", Answers: true}},
		Digest: "0123456789", Needs: []string{oplog.NeedLoss},
		Why: []string{"controller b does not answer: it may hold entries this log lacks"},
	}
	raw, _ := json.Marshal(pic)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(req.Body).Decode(&b)
		got = append(got, b)
		rep := map[string]any{"cluster": "c", "picture": json.RawMessage(raw)}
		if b["acceptLoss"] == "0123456789" {
			json.NewEncoder(w).Encode(map[string]any{"outcome": "done", "report": rep})
			return
		}
		rep["refused"] = "needs --accept-loss=0123456789"
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{"outcome": "refused", "error": "needs --accept-loss=0123456789", "report": rep})
	}))
	defer srv.Close()
	run := func(args ...string) (int, string) {
		var out bytes.Buffer
		env := map[string]string{"NAUTILUS_TOKEN": "t", "NAUTILUS_URL": srv.URL}
		return redundancyMain(args, func(k string) string { return env[k] }, &out, &out), out.String()
	}
	code, out := run("vouch")
	for _, want := range []string{"controller b", "DOES NOT ANSWER", "would be lost: controller b does not answer", "naut redundancy vouch --accept-loss=0123456789", "REFUSED"} {
		if code != 1 || !strings.Contains(out, want) {
			t.Fatalf("plain vouch: exit %d, want %q in\n%s", code, want, out)
		}
	}
	if code, out := run("vouch", "--accept-loss=0123456789"); code != 0 || !strings.Contains(out, "vouched") || got[1]["acceptLoss"] != "0123456789" {
		t.Fatalf("accepting vouch: exit %d, sent %v\n%s", code, got, out)
	}
	if code, out := run("recommission", "--from-spare", "--copy-stopped", "--accept-loss=0123456789"); code != 1 || !strings.Contains(out, "takes no --accept-loss") {
		t.Fatalf("--from-spare with an acceptance: exit %d\n%s", code, out)
	}
}

// naut run exits 75 (EX_TEMPFAIL, so systemd's Restart=on-failure starts
// it again) when the pair asks to restart, after stopping as a signal
// would; a plain stop exits 0.
func TestNautRunExitsForARestartRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	restart := make(chan struct{})
	close(restart)
	var out bytes.Buffer
	if code := awaitStop(ctx, restart, cancel, &out); code != 75 || ctx.Err() == nil || !strings.Contains(out.String(), "restarting") {
		t.Fatalf("a restart request: exit %d, stopped %v, %q", code, ctx.Err() != nil, out.String())
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if code := awaitStop(ctx2, nil, func() {}, &out); code != 0 {
		t.Fatalf("a stop: exit %d", code)
	}
}

// naut redundancy status shows the log's history, a pending supersede,
// the kept superseded copy and a fork.
func TestStatusShowsTheLogHistory(t *testing.T) {
	var out bytes.Buffer
	printClusterStatus(&out, []byte(`{"mode":"quorum","pod":"a","leader":"a","isLeader":true,"log":{"synced":false,"history":2,"historyBy":"user:ann","supersedeTo":3,"superseded":"/var/lib/nautilus/oplog.jsonl.superseded-1700000000","forked":["controller b"]}}`))
	for _, want := range []string{"operator-intent log: NOT SYNCED; history 2 (a loss accepted by user:ann)", "SUPERSEDED by history 3", "kept at /var/lib/nautilus/oplog.jsonl.superseded-1700000000", "FORKED: the log of controller b forked"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("want %q in\n%s", want, out.String())
		}
	}
}
