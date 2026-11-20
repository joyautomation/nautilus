package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/quorum"
	"github.com/joyautomation/nautilus/quorum/node"
)

// The witness keeps the third copy of each pair's operator-intent log
// (#710 PR 3b-4b, design §1): one passive oplog replica per bound cluster,
// at node.Prefix + cluster + "/log" on the witness's peer listener, behind
// the peer token. It never leads (its Leadership names no one, so a
// propose gets 409) and never dials a controller: the leader's
// replication and heartbeats fill it, and a leader's catch-up reads it.
//
// It takes replication only from the controller its own vote for the
// cluster names, at a term no higher than that vote's (Admit): a deposed
// leader, the other controller, or a term it never granted are refused.
// The log's identity is the cluster's witness incarnation, so a witness
// rebuilt (a new incarnation) moves its old log aside and starts empty
// and unsynced; the leader counts it again once it has synced it, at the
// incarnation the election counts (pairLeadership's Counts).
//
// Files, beside the cluster's voter state: <cluster>.oplog.jsonl and
// <cluster>.oplog.mark.

// witnessLogs hosts the pairs' logs on one witness.
type witnessLogs struct {
	w       *node.Witness
	dir     string
	token   string
	timeout time.Duration
	log     *slog.Logger

	mu   sync.Mutex
	logs map[string]*witnessLog
}

type witnessLog struct {
	n *oplog.Node
	h http.Handler
}

func newWitnessLogs(w *node.Witness, dir, token string, lease time.Duration, lg *slog.Logger) *witnessLogs {
	wl := &witnessLogs{w: w, dir: dir, token: token, timeout: max(lease/2, 500*time.Millisecond), log: lg, logs: map[string]*witnessLog{}}
	w.SetLogInfo(wl.info)
	return wl
}

// Handler serves Prefix + {cluster} + "/log/..." (mount it behind Guard).
func (wl *witnessLogs) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(node.Prefix+"{cluster}/log/", func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		l, code, err := wl.open(cluster)
		if err != nil {
			http.Error(w, err.Error(), code)
			return
		}
		http.StripPrefix(node.Prefix+cluster+"/log", l.h).ServeHTTP(w, r)
	})
	return mux
}

// open returns the cluster's log, opening it at its first request: only
// for a cluster bound by a vote, and only once its voter runs this boot
// (its incarnation is the log's identity). Nothing here binds a cluster or
// starts a voter.
func (wl *witnessLogs) open(cluster string) (*witnessLog, int, error) {
	members, bound := wl.w.Bound(cluster)
	if !bound {
		return nil, http.StatusNotFound, errors.New("this witness serves no such cluster")
	}
	wl.mu.Lock()
	defer wl.mu.Unlock()
	if l, ok := wl.logs[cluster]; ok {
		return l, 0, nil
	}
	_, _, inc, ok := wl.w.Grant(cluster)
	if !ok {
		return nil, http.StatusServiceUnavailable, errors.New("this witness has had no vote for the cluster since it started; the leader's next renewal brings one")
	}
	base := filepath.Join(wl.dir, cluster+".oplog")
	file, mark := base+".jsonl", base+".mark"
	self := "witness"
	n, err := oplog.New(oplog.Options{
		// Symbolic: the witness never calls a peer, and three of them keep
		// it from ever being standalone (which would sequence alone).
		Self: self, Peers: []string{"a", "b", self},
		Leadership: witnessLeadership{w: wl.w, cluster: cluster},
		File:       file, Mark: mark, Identity: strconv.FormatUint(inc, 10),
		Token: wl.token, Timeout: wl.timeout, Log: wl.log.With("cluster", cluster, "log", "operator-intent"),
		Admit: func(from string, term uint64) error { return wl.admit(cluster, members, from, term) },
	})
	if err != nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("the cluster's operator-intent log: %w", err)
	}
	if lost := n.Lost(); lost != "" {
		wl.log.Error("witness: a pair's operator-intent log was lost; it counts for nothing until the leader has synced it", "cluster", cluster, "kept", lost)
	}
	l := &witnessLog{n: n, h: n.Handler()}
	wl.logs[cluster] = l
	return l, 0, nil
}

// admit is the witness log's Admit: replication only from the controller
// its vote for the cluster names, at a term no higher than it promised
// (a renewal may have re-termed the vote above the leader's epoch, the
// log's term; a lower, stale one the log refuses by its own term).
func (wl *witnessLogs) admit(cluster, members, from string, term uint64) error {
	granted, promised, _, ok := wl.w.Grant(cluster)
	switch {
	case !ok:
		return errors.New("no vote for this cluster since the witness started")
	case memberPairOf(quorum.NodeID(from), members) == "" && !mutNaut("witness_log_unbound"):
		return fmt.Errorf("%q is not one of this cluster's controllers (%s)", from, members)
	case string(granted) != from && !mutNaut("witness_log_unbound"):
		return fmt.Errorf("this witness's vote names %q, not %q", granted, from)
	case (term == 0 || term > promised) && !mutNaut("witness_log_term_unbound"):
		return fmt.Errorf("term %d: this witness promised %d", term, promised)
	}
	return nil
}

// memberPairOf returns members when from is one of its two names.
func memberPairOf(from quorum.NodeID, members string) string {
	for i := 0; i < len(members); i++ {
		if members[i] == ',' {
			if string(from) == members[:i] || string(from) == members[i+1:] {
				return members
			}
			return ""
		}
	}
	return ""
}

// info is Witness.Pairs' log report.
func (wl *witnessLogs) info(cluster string) (last, commit uint64, ok bool) {
	wl.mu.Lock()
	l, ok := wl.logs[cluster]
	wl.mu.Unlock()
	if !ok {
		return 0, 0, false
	}
	return l.n.LastSeq(), l.n.Commit(), true
}

// Close closes every log.
func (wl *witnessLogs) Close() {
	wl.mu.Lock()
	defer wl.mu.Unlock()
	for _, l := range wl.logs {
		l.n.Close()
	}
}

// witnessLeadership is the witness log's view: it never leads, names no
// leader (Admit decides who replicates), and its term is the vote's.
type witnessLeadership struct {
	w       *node.Witness
	cluster string
}

func (witnessLeadership) IsLeader() bool     { return false }
func (witnessLeadership) LeaderAddr() string { return "" }
func (l witnessLeadership) Term() uint64 {
	_, promised, _, _ := l.w.Grant(l.cluster)
	return promised
}
