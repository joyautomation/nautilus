package oplog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// simnet is an in-process network for three replicas (#710 3b-4): every
// request goes straight to the addressed replica's handler, through the
// faults the test sets. A replica is named n0, n1, n2 (its URL
// http://nI).
type simnet struct {
	mu        sync.Mutex
	handlers  map[int]http.Handler
	down      [3]bool    // every request to or from it fails at once
	silent    [3]bool    // a request to it blocks until its context ends
	dropReply [3]bool    // it handles the request, its answer is lost
	delay     [3]bool    // a request to it is handled simDelay late, even after its sender gave up
	dropPush  [3]bool    // a replication to it that carries no entries (a heartbeat, a commit push) is lost
	cutAfter  [3]int     // when > 0: it takes this many more requests, then goes down
	cut       [3][3]bool // the link from i to j fails
	late      sync.WaitGroup
}

// simDelay is longer than a fan-out's grace (lease/8, at most 50 ms) and
// shorter than the call timeout the simulation runs with (300 ms), so a
// delayed answer can arrive in time, or after the leader stopped waiting,
// and the delayed handling can land after the next step (a new leader).
const simDelay = 80 * time.Millisecond

func (s *simnet) set(i int, h http.Handler) {
	s.mu.Lock()
	if s.handlers == nil {
		s.handlers = map[int]http.Handler{}
	}
	if h == nil {
		delete(s.handlers, i)
	} else {
		s.handlers[i] = h
	}
	s.mu.Unlock()
}

// hop is one replica's transport into the network.
type hop struct {
	s    *simnet
	from int
}

var errSimDown = errors.New("simnet: unreachable")

func (h hop) RoundTrip(r *http.Request) (*http.Response, error) {
	var to int
	if _, err := fmt.Sscanf(r.URL.Host, "n%d", &to); err != nil || to < 0 || to > 2 {
		return nil, fmt.Errorf("simnet: no replica %q", r.URL.Host)
	}
	h.s.mu.Lock()
	down := h.s.down[h.from] || h.s.down[to] || h.s.cut[h.from][to]
	silent, drop, delay, dropPush := h.s.silent[to], h.s.dropReply[to], h.s.delay[to], h.s.dropPush[to]
	if !down && h.s.cutAfter[to] > 0 {
		h.s.cutAfter[to]--
		if h.s.cutAfter[to] == 0 {
			h.s.down[to] = true
		}
	}
	hd := h.s.handlers[to]
	h.s.mu.Unlock()
	if r.Body != nil {
		defer r.Body.Close()
	}
	switch {
	case down || hd == nil:
		return nil, errSimDown
	case silent:
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	if dropPush && strings.HasSuffix(r.URL.Path, "/api/oplog/replicate") {
		var q replicateReq
		if json.Unmarshal(body, &q) == nil && len(q.Entries) == 0 {
			return nil, errors.New("simnet: the push was lost")
		}
	}
	serve := func(ctx context.Context, hd http.Handler) *http.Response {
		in := httptest.NewRequest(r.Method, r.URL.String(), bytes.NewReader(body)).WithContext(ctx)
		in.Header = r.Header.Clone()
		rec := httptest.NewRecorder()
		hd.ServeHTTP(rec, in)
		return rec.Result()
	}
	if delay {
		// On the wire: handled late whatever the sender does meanwhile,
		// by whichever process then answers at that address.
		done := make(chan *http.Response, 1)
		h.s.late.Add(1)
		go func() {
			defer h.s.late.Done()
			time.Sleep(simDelay)
			h.s.mu.Lock()
			hd := h.s.handlers[to]
			h.s.mu.Unlock()
			if hd == nil {
				done <- nil
				return
			}
			done <- serve(context.Background(), hd)
		}()
		select {
		case resp := <-done:
			if resp == nil || drop {
				return nil, errSimDown
			}
			return resp, nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	resp := serve(r.Context(), hd)
	if drop {
		return nil, errors.New("simnet: the answer was lost")
	}
	return resp, nil
}

// trio is three marked replicas on a simnet, with a leadership the test
// moves: n0 is A, n1 is B, n2 is W (by name only; they are alike unless a
// test sets Admit).
type trio struct {
	t        *testing.T
	dir      string
	net      *simnet
	urls     []string
	m        *moving
	nodes    [3]*Node
	ident    [3]string
	gen      [3]int
	timeout  time.Duration
	unmarked [3]bool // an absent mark starts synced (UnmarkedSynced)
	opts     func(i int, o *Options)

	mu      sync.Mutex
	applied [3][]Entry
}

func newTrio(t *testing.T, opts func(i int, o *Options)) *trio {
	t.Helper()
	tr := &trio{t: t, dir: t.TempDir(), net: &simnet{}, m: &moving{}, opts: opts, timeout: 2 * time.Second}
	for i := range 3 {
		tr.urls = append(tr.urls, fmt.Sprintf("http://n%d", i))
		tr.ident[i] = fmt.Sprintf("n%d-1", i)
		tr.gen[i] = 1
		tr.unmarked[i] = true // a new cluster's first start: nothing is committed yet
	}
	tr.m.set(tr.urls[0], 1)
	for i := range 3 {
		tr.start(i)
	}
	t.Cleanup(func() {
		tr.net.late.Wait() // delayed requests land before the replicas close
		for i := range 3 {
			tr.stop(i)
		}
	})
	return tr
}

func (tr *trio) logFile(i int) string  { return filepath.Join(tr.dir, fmt.Sprintf("n%d.jsonl", i)) }
func (tr *trio) markFile(i int) string { return filepath.Join(tr.dir, fmt.Sprintf("n%d.mark", i)) }

func (tr *trio) options(i int) Options {
	o := Options{
		Self: tr.urls[i], Peers: tr.urls, Leadership: view{self: tr.urls[i], m: tr.m},
		File: tr.logFile(i), Mark: tr.markFile(i), Identity: tr.ident[i], UnmarkedSynced: tr.unmarked[i],
		Token: "t", Timeout: tr.timeout, Node: fmt.Sprintf("n%d", i),
		Apply: func(e Entry) error {
			tr.mu.Lock()
			tr.applied[i] = append(tr.applied[i], e)
			tr.mu.Unlock()
			return nil
		},
		transport: hop{tr.net, i},
	}
	if !simTrace {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if tr.opts != nil {
		tr.opts(i, &o)
	}
	return o
}

// start opens replica i (its applied list restarts with the process).
func (tr *trio) start(i int) {
	tr.t.Helper()
	tr.mu.Lock()
	tr.applied[i] = nil
	tr.mu.Unlock()
	n, err := New(tr.options(i))
	if err != nil {
		tr.t.Fatalf("n%d: %v", i, err)
	}
	tr.nodes[i] = n
	tr.net.set(i, n.Handler())
}

func (tr *trio) stop(i int) {
	if tr.nodes[i] != nil {
		tr.net.set(i, nil)
		tr.nodes[i].Close()
		tr.nodes[i] = nil
	}
}

func (tr *trio) restart(i int) { tr.stop(i); tr.start(i) }

// loseLog deletes replica i's log file (its mark stays) and restarts it.
func (tr *trio) loseLog(i int) {
	tr.t.Helper()
	tr.stop(i)
	if err := os.Remove(tr.logFile(i)); err != nil {
		tr.t.Fatal(err)
	}
	tr.start(i)
}

// wipe deletes replica i's log and mark and restarts it as a new identity
// (a witness rebuilt from scratch): no mark, so unsynced.
func (tr *trio) wipe(i int) {
	tr.t.Helper()
	tr.stop(i)
	os.Remove(tr.logFile(i))
	os.Remove(tr.markFile(i))
	tr.gen[i]++
	tr.ident[i] = fmt.Sprintf("n%d-%d", i, tr.gen[i])
	tr.unmarked[i] = false
	tr.start(i)
}

// lead makes replica i the leader of the next term.
func (tr *trio) lead(i int) uint64 {
	tr.m.mu.Lock()
	tr.m.leader = tr.urls[i]
	tr.m.term++
	term := tr.m.term
	tr.m.mu.Unlock()
	return term
}

func (tr *trio) setDown(i int, v bool) {
	tr.net.mu.Lock()
	tr.net.down[i] = v
	tr.net.mu.Unlock()
}

func (tr *trio) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	tr.t.Cleanup(cancel)
	return ctx
}

// propose an ack of id at replica i.
func (tr *trio) propose(i int, id string) (Entry, error) {
	return tr.nodes[i].Propose(tr.ctx(), Entry{Kind: KindAck, IDs: []string{id}, By: "op"})
}

// holds reports the seq at which replica i's log holds the ack of id (0:
// none), and how many times it holds it.
func (tr *trio) holds(i int, id string) (seq uint64, times int) {
	for _, e := range tr.nodes[i].After(0) {
		for _, x := range e.IDs {
			if x == id && e.Kind == KindAck {
				seq = e.Seq
				times++
			}
		}
	}
	return seq, times
}

// appliedTimes is how many times replica i applied the ack of id in this
// process.
func (tr *trio) appliedTimes(i int, id string) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	k := 0
	for _, e := range tr.applied[i] {
		for _, x := range e.IDs {
			if x == id && e.Kind == KindAck {
				k++
			}
		}
	}
	return k
}
