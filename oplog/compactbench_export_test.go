//go:build oplogbench

package oplog

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// What the compaction bench (compactbench_test.go, package oplog_test,
// which imports the server's keep decision) needs from the package.

// BenchTrio is three copies over the same causalMix log of n entries, n0
// leading a new term, deciding with compact.
type BenchTrio struct{ tr *trio }

func NewBenchTrio(t *testing.T, n int, compact Compactor) *BenchTrio {
	t.Helper()
	tr := newTrio(t, func(i int, o *Options) {
		o.CompactRules = 1
		o.Compact = compact
		o.decide = decideLimits{every: 24 * time.Hour, paceWork: benchPace.work, paceDuty: benchPace.duty}
		o.lim.compactDelay = func() time.Duration { return 0 }
	})
	for i := range 3 {
		tr.stop(i)
		os.Remove(tr.markFile(i))
	}
	f, err := os.Create(tr.logFile(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := causalMix(bufio.NewWriterSize(f, 1<<20), n); err != nil {
		t.Fatal(err)
	}
	f.Close()
	b, _ := os.ReadFile(tr.logFile(0))
	for i := 1; i < 3; i++ {
		if err := os.WriteFile(tr.logFile(i), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		tr.start(i)
	}
	tr.lead(0)
	if err := tr.nodes[0].SyncLeader(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &BenchTrio{tr: tr}
}

// benchPace is the pacing the bench runs under: OPLOG_BENCH_PACE=work,duty
// (e.g. 2ms,0.25; duty 1 is unpaced); unset, the defaults.
var benchPace = func() (p struct {
	work time.Duration
	duty float64
}) {
	w, d, ok := strings.Cut(os.Getenv("OPLOG_BENCH_PACE"), ",")
	if !ok {
		return p
	}
	p.work, _ = time.ParseDuration(w)
	p.duty, _ = strconv.ParseFloat(d, 64)
	return p
}()

// Pacing is the pacing a look runs under, for the table.
func (b *BenchTrio) Pacing() string {
	l := b.tr.nodes[0].limits()
	if l.paceDuty >= 1 {
		return "unpaced"
	}
	return fmt.Sprintf("%s at %.0f%%", l.paceWork, l.paceDuty*100)
}

// Node is copy i.
func (b *BenchTrio) Node(i int) *Node { return b.tr.nodes[i] }

// Decide is one look by the leader: its outcome's name.
func (b *BenchTrio) Decide(ctx context.Context) string {
	return []string{"not leader", "not wanted", "blocked", "refused", "waiting", "discarded", "proposed"}[b.tr.nodes[0].decideForTest(ctx)]
}

// Settled waits for every copy's file to hold its newest base.
func (b *BenchTrio) Settled(ctx context.Context) error {
	for _, n := range b.tr.nodes {
		if err := n.compactedForTest(ctx); err != nil {
			return err
		}
	}
	return nil
}

// File is copy i's log file.
func (b *BenchTrio) File(i int) string { return b.tr.logFile(i) }

// Restart restarts copy i.
func (b *BenchTrio) Restart(i int) { b.tr.restart(i) }

// causalMix writes n entries of a busy plant whose operator intent ends
// as it does in a live plant: each alarm activation an occurrence start,
// an ack of it, an ack-clear and an end, on one of 2,000 alarms; shelves
// of 8 h, sessions opened and ended, substitutes started and stopped,
// notes. About 2,400 entries a day. What stands at the end is what a live
// plant's view holds, so the kept part is the bounded one of §2.
func causalMix(w *bufio.Writer, n int) error {
	r := rand.New(rand.NewPCG(1455, 8))
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	alarm := func(i int) string { return fmt.Sprintf("Area%d/Unit%02d/Pump%03d.HighPressure", i%4+1, i%30+1, i) }
	var queue []Entry // entries owed later (the activation's next steps)
	seq := uint64(0)
	write := func(e Entry) error {
		seq++
		ts += int64(30_000 + r.IntN(15_000))
		e.Seq, e.Term, e.TS = seq, 1, ts
		if e.By == "" {
			e.By = "system"
		}
		for _, l := range []fileLine{{E: &e}, {C: &seq}} {
			b, _ := json.Marshal(l)
			if _, err := w.Write(append(b, '\n')); err != nil {
				return err
			}
		}
		return nil
	}
	for int(seq) < n {
		if len(queue) > 0 && r.IntN(3) > 0 {
			e := queue[0]
			queue = queue[1:]
			if err := write(e); err != nil {
				return err
			}
			continue
		}
		a := alarm(r.IntN(2000))
		occ := fmt.Sprintf("o%d", seq)
		switch k := r.IntN(100); {
		case k < 94:
			o := Occ{ID: a, Occ: occ, ActiveMs: ts}
			if err := write(Entry{Kind: KindOccurrenceStart, Occs: []Occ{o}}); err != nil {
				return err
			}
			queue = append(queue,
				Entry{Kind: KindAck, IDs: []string{a}, Occs: []Occ{o}, By: "operator1", Link: fmt.Sprintf("L%d", seq)},
				Entry{Kind: KindAckClear, IDs: []string{a}, Occs: []Occ{{ID: a, Occ: occ}}},
				Entry{Kind: KindOccurrenceEnd, Occs: []Occ{{ID: a, Occ: occ}}})
		case k < 95:
			if err := write(Entry{Kind: KindShelve, ID: a, Until: ts + 8*3600_000, By: "operator1"}); err != nil {
				return err
			}
		case k < 96:
			v := fmt.Sprintf(`{"v":1,"id":"n%d","rev":1,"target":{"kind":"equipment","id":"Area2/Unit07/Pump012"},"text":"Pump 12 seal weeping again; maintenance called."}`, seq)
			if err := write(Entry{Kind: KindNote, By: "operator1", Value: json.RawMessage(v)}); err != nil {
				return err
			}
		case k < 98:
			h := fmt.Sprintf("%064x", r.Uint64())
			open := SessionEntryOf("operator1", SessionEntry{Op: SessionOpen, Hash: h, User: "operator1", Created: ts, Expires: ts + 12*3600_000})
			if err := write(open); err != nil {
				return err
			}
			end := SessionEntryOf("operator1", SessionEntry{Op: SessionEnd, Hash: h, Expires: ts + 12*3600_000})
			queue = append(queue, end)
		default:
			if err := write(Entry{Kind: KindSubstitute, Tag: "plc1/" + a, Value: json.RawMessage(`42.5`), GrantedMs: 3600_000, Until: ts + 3600_000, By: "operator1"}); err != nil {
				return err
			}
			queue = append(queue, Entry{Kind: KindUnsubstitute, Ref: seq, By: "operator1"})
		}
	}
	return w.Flush()
}

// Sections times, on the leader, what a compaction runs under its locks:
// building K (compactEntryLocked, under the sequencing lock and mu) and
// carrying it out at commit (nextBaseLocked, under mu), for keep up to
// through, without proposing anything.
func (b *BenchTrio) Sections(keep []uint64, through uint64) (build, carry time.Duration, err error) {
	n := b.tr.nodes[0]
	term := n.currentTerm()
	// K's body as proposeDecided builds it: outside the locks.
	d, _, _, _ := n.plan(n.limits(), through, nil)
	pre, perr := compactBodyOf(d.base, d.snap, through, keep, n.o.CompactRules, d.termAt, nil)
	if perr != nil {
		return 0, 0, perr
	}
	n.seqMu.Lock()
	defer n.seqMu.Unlock()
	n.mu.Lock()
	defer n.mu.Unlock()
	t0 := time.Now()
	e, err := n.compactEntryLocked(term, through, keep, &pre)
	build = time.Since(t0)
	if err != nil {
		return build, 0, err
	}
	var body compactBody
	if err := json.Unmarshal(e.Value, &body); err != nil {
		return build, 0, err
	}
	t1 := time.Now()
	if _, why := n.nextBaseLocked(n.lastLocked()+1, body); why != "" {
		return build, time.Since(t1), fmt.Errorf("carry: %s", why)
	}
	return build, time.Since(t1), nil
}
