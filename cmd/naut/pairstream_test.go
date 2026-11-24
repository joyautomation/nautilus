package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/quorum/node"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
)

// pairStreamProject is pairProject's pair with a retain: section and what
// a takeover must keep (#710 S1b): a retained scalar setpoint, a UDT
// setpoint, an array setpoint (a long one, so an image has some size), a
// VAR RETAIN local, and room for an online edit; and Mode, a state tag
// only an operator writes, not retained, which only a hot takeover keeps
// (#710 S2).
func pairStreamProject(a, b, w string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(fmt.Sprintf(`name: pairstream
retain: {}
tasks:
  - program: main.st
    scan: 50ms
tags:
  - { name: SP, role: setpoint, init: 0.0 }
  - { name: Rec, role: setpoint, type: Recipe }
  - { name: Table, role: setpoint, type: 'ARRAY[1..2000] OF REAL' }
  - { name: Mode, role: state, init: 0 }
redundancy:
  experimental: true
  peers:
    a: %s
    b: %s
  witness: %s
  lease: 500ms
`, a, b, w))},
		"main.st": &fstest.MapFile{Data: []byte(streamMain)},
	}
}

const streamMain = "TYPE\n  Recipe : STRUCT\n    Count : INT;\n    Temp : REAL;\n  END_STRUCT;\nEND_TYPE\n\nPROGRAM Main\nVAR_EXTERNAL SP : REAL; Rec : Recipe; Table : ARRAY[1..2000] OF REAL; END_VAR\nVAR RETAIN Runs : DINT; END_VAR\nRuns := Runs + 1;\nEND_PROGRAM\n"

// newStreamRig is a loopback pair running pairStreamProject.
func newStreamRig(t *testing.T, pmod func(n string, p *project.Project)) *pairRig {
	t.Helper()
	testPairFiles = pairStreamProject
	t.Cleanup(func() { testPairFiles = nil })
	return newPairRigFull(t, pmod, nil)
}

func (r *pairRig) streamStatus(n string) *leader.StreamStatus { return r.nodes[n].m.q.Status().Stream }

// streamValues are what the test writes on a leader and expects on the
// next one.
type streamValues struct {
	sp, temp, cell float64
	runs           int64
	src            string
}

// write sets v on member n's tags and program (an online edit), and
// reads back the VAR RETAIN counter.
func (r *pairRig) writeStream(t *testing.T, n string, sp, temp, cell float64, edit string) streamValues {
	t.Helper()
	tags := r.nodes[n].rt.Tags()
	for path, v := range map[string]any{"SP": sp, "Rec.Temp": temp, "Table[1500]": cell} {
		if err := tags.SetPath(path, v); err != nil {
			t.Fatalf("%s on %s: %v", path, n, err)
		}
	}
	p := r.nodes[n].rt.TaskProgram(runtime.MainTaskName)
	if edit != "" {
		if _, err := p.SwapWarm(strings.Replace(streamMain, "Runs := Runs + 1;", "Runs := Runs + 1; (* "+edit+" *)", 1)); err != nil {
			t.Fatal(err)
		}
	}
	return streamValues{sp: sp, temp: temp, cell: cell, runs: r.runs(n), src: p.Source()}
}

func (r *pairRig) runs(n string) int64 {
	v, ok := r.nodes[n].rt.Tags().ReadPath("Main.Runs")
	if !ok {
		v = r.nodes[n].rt.TaskProgram(runtime.MainTaskName).RetainedLocals()["Runs"]
	}
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return -1
}

// imaged waits until the standby's retain file holds an image the leader
// copied after now: the leader's next image, persisted at the standby.
// Never a sleep: the reported positions.
func (r *pairRig) imaged(t *testing.T, lead string) {
	t.Helper()
	ls := r.streamStatus(lead)
	if ls == nil || ls.Role != "leader" {
		t.Fatalf("%s's stream: %+v, want a leader's", lead, ls)
	}
	after := ls.Seq
	var want uint64
	pairWait(t, "the leader's next image", 5*time.Second, func() bool {
		want = r.streamStatus(lead).Seq
		return want > after
	})
	term := r.streamStatus(lead).Term
	pairWait(t, "the standby to keep it in its retain file", 10*time.Second, func() bool {
		s := r.streamStatus(other(lead))
		return s != nil && s.PersistedTerm == term && s.PersistedSeq >= want
	})
}

// check fails unless member n runs with v: the setpoints, the online
// edit, and a VAR RETAIN counter that went on from v's (not from 0).
func (r *pairRig) checkStream(t *testing.T, n, when string, v streamValues) {
	t.Helper()
	tags := r.nodes[n].rt.Tags()
	for path, want := range map[string]float64{"SP": v.sp, "Rec.Temp": v.temp, "Table[1500]": v.cell} {
		got, _ := tags.ReadPath(path)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %s on %s = %v, want %v", when, path, n, got, want)
		}
	}
	if got := r.nodes[n].rt.TaskProgram(runtime.MainTaskName).Source(); got != v.src {
		t.Errorf("%s: %s runs %q, want the online edit %q", when, n, got, v.src)
	}
	if got := r.runs(n); got < v.runs {
		t.Errorf("%s: VAR RETAIN Runs on %s = %d, want at least the old leader's %d", when, n, got, v.runs)
	}
}

// T-S1b, end to end through naut run's wiring (#710 S1b, James's
// "critical" item): what the leader's retain holds survives every
// takeover, because its standby keeps the leader's images in its own
// retain file. A hard kill, a takeover back, a standby restarted before
// the takeover (the file, not its memory), and a standby out of sync,
// whose cold takeover loads the older image and says how old.
func TestPairRetainedStateSurvivesTakeovers(t *testing.T) {
	r := newStreamRig(t, nil)
	lead := r.leader()
	stand := other(lead)

	// The leader sends, the standby follows; neither does the other's job.
	pairWait(t, "the standby in sync", 10*time.Second, func() bool {
		s := r.streamStatus(stand)
		return s != nil && s.State == "in-sync"
	})
	if r.nodes[lead].m.stream.following() {
		t.Error("the leader runs a stream follower")
	}
	if s := r.streamStatus(lead); s.State != "sending" || s.Bytes == 0 {
		t.Errorf("the leader's stream: %+v, want sending", s)
	}

	// 1. A hard kill.
	v1 := r.writeStream(t, lead, 61.5, 71.25, 3.5, "edit 1")
	r.imaged(t, lead)
	r.nodes[lead].crash()
	if got := r.leader(); got != stand {
		t.Fatalf("after the kill: leader %q, want %s", got, stand)
	}
	r.checkStream(t, stand, "after a hard kill", v1)
	if s := r.streamStatus(stand); s.TakeoverImageAgeMs < 0 || s.TakeoverImageAgeMs > 5000 {
		t.Errorf("the takeover's image age: %d ms, want a recent image's", s.TakeoverImageAgeMs)
	}

	// 2. A takeover back: the old leader returns as the standby.
	lead, stand = stand, lead
	r.restart(stand)
	pairWait(t, "the returned controller in sync", 10*time.Second, func() bool {
		s := r.streamStatus(stand)
		return s != nil && s.State == "in-sync"
	})
	// An image of a term the leader never asked at is refused, and changes
	// nothing the standby keeps.
	st := r.nodes[stand].m.stream
	st.mu.Lock()
	pend := st.pendTerm
	st.mu.Unlock()
	bogus := r.streamStatus(lead).Term + 50
	st.take(&runtime.Image{Term: bogus, Seq: 1, Retain: []byte(`{"tags":{"SP":-1}}`)}, time.Now(), 100, 10)
	st.mu.Lock()
	refusedState, kept := st.state, st.pendTerm
	st.mu.Unlock()
	if refusedState != "refused" || kept != pend {
		t.Errorf("an image of term %d: state %q, keeps term %d (was %d); want refused and nothing kept", bogus, refusedState, kept, pend)
	}
	v2 := r.writeStream(t, lead, 62.5, 72.25, 4.5, "edit 2")
	r.imaged(t, lead)
	r.nodes[lead].crash()
	if got := r.leader(); got != stand {
		t.Fatalf("the second takeover: leader %q, want %s", got, stand)
	}
	r.checkStream(t, stand, "after a takeover back", v2)

	// 3. The standby restarted before the takeover: what it took over with
	// came from its retain file, not its memory.
	lead, stand = stand, lead
	r.restart(stand)
	pairWait(t, "the returned controller in sync", 10*time.Second, func() bool {
		s := r.streamStatus(stand)
		return s != nil && s.State == "in-sync"
	})
	v3 := r.writeStream(t, lead, 63.5, 73.25, 5.5, "")
	r.imaged(t, lead)
	r.nodes[stand].crash()
	r.nodes[lead].crash()
	r.restart(stand) // alone, with the witness: it takes over from its file
	if got := r.leader(); got != stand {
		t.Fatalf("the restarted standby alone: leader %q, want %s", got, stand)
	}
	r.checkStream(t, stand, "after a restart and a takeover", v3)

	// 4. Out of sync: the leader stops sending; the standby says it is
	// behind, and its cold takeover loads the last image it kept, older
	// than the leader's newest values, and says how old.
	lead, stand = stand, lead
	r.restart(stand)
	pairWait(t, "the returned controller in sync", 10*time.Second, func() bool {
		s := r.streamStatus(stand)
		return s != nil && s.State == "in-sync"
	})
	v4 := r.writeStream(t, lead, 64.5, 74.25, 6.5, "")
	r.imaged(t, lead)
	r.nodes[lead].m.stream.hold.Store(true)
	pairWait(t, "the leader's send loop to hold", 5*time.Second, r.nodes[lead].m.stream.held.Load)
	r.writeStream(t, lead, 99, 99, 99, "")
	var behindMs int64
	pairWait(t, "the standby to report it is behind", 10*time.Second, func() bool {
		s := r.streamStatus(stand)
		behindMs = s.AgeMs
		return s.State == "behind"
	})
	pairWait(t, "the standby's warning that it is behind", 5*time.Second, func() bool { return r.hasWarning(stand, "state stream behind") })
	r.nodes[lead].crash()
	if got := r.leader(); got != stand {
		t.Fatalf("the out-of-sync takeover: leader %q, want %s", got, stand)
	}
	r.checkStream(t, stand, "after an out-of-sync takeover", v4)
	if s := r.streamStatus(stand); s.TakeoverImageAgeMs < behindMs {
		t.Errorf("the out-of-sync takeover reports its image %d ms old, want at least the %d ms it was behind", s.TakeoverImageAgeMs, behindMs)
	}
}

// The stream keeps to its budget (redundancy.stream-budget): an image
// too big for 100 ms at the budget goes out less often, never dropped
// (#710 S1b).
func TestPairStreamBudgetStretchesTheInterval(t *testing.T) {
	r := newStreamRig(t, func(_ string, p *project.Project) { p.Redundancy.StreamBudget = "10kB/s" })
	lead := r.leader()
	var s *leader.StreamStatus
	pairWait(t, "the leader to send", 10*time.Second, func() bool {
		s = r.streamStatus(lead)
		return s != nil && s.State == "sending" && s.Bytes > 0
	})
	want := int64(s.Bytes) * 1000 / 10_000
	if want <= streamMinInterval.Milliseconds() {
		t.Fatalf("an image of %d bytes fits 100 ms at 10 kB/s: the case proves nothing", s.Bytes)
	}
	if s.IntervalMs < want-1 {
		t.Errorf("an image of %d bytes goes every %d ms at 10 kB/s, want at least %d", s.Bytes, s.IntervalMs, want)
	}
	pairWait(t, "the standby in sync", 10*time.Second, func() bool {
		ss := r.streamStatus(other(lead))
		return ss != nil && ss.State == "in-sync"
	})
}

// naut check sizes a pair's state stream image (#710 S1b): nothing to say
// within the budget, a warning when ten a second are over it, and one
// when the image is over the limit, so the leader would send none.
func TestStreamWarnings(t *testing.T) {
	size := func(files fstest.MapFS, budget string) []string {
		t.Helper()
		p := loadPair(t, files)
		p.Redundancy.StreamBudget = budget
		return streamWarnings(p)
	}
	files := pairStreamProject("ctl-a:9450", "ctl-b:9450", "wit:9460")
	if w := size(files, ""); len(w) != 0 {
		t.Errorf("a 4 kB image at 1 MB/s: %q, want no warning", w)
	}
	if w := size(files, "10kB/s"); len(w) != 1 || !strings.Contains(w[0], "over redundancy.stream-budget") {
		t.Errorf("a 4 kB image at 10 kB/s: %q, want the budget warning", w)
	}
	big := fstest.MapFS{"nautilus.yaml": files["nautilus.yaml"], "main.st": &fstest.MapFile{Data: []byte(strings.Replace(streamMain, "VAR RETAIN Runs : DINT; END_VAR", "VAR RETAIN Runs : DINT; END_VAR\nVAR Big : ARRAY[1..1000000] OF REAL; END_VAR", 1))}}
	if w := size(big, ""); len(w) != 1 || !strings.Contains(w[0], "over the 8388608-byte limit") {
		t.Errorf("a 9 MB image: %q, want the limit warning", w)
	}
}

// naut redundancy status says how the state stream stands (#710 S1b):
// a standby's image age, lag and retain-file position; a leader's size
// and pace; the last takeover's image age and flush.
func TestStatusShowsTheStateStream(t *testing.T) {
	var out bytes.Buffer
	printClusterStatus(&out, []byte(`{"mode":"quorum","pod":"b","leader":"a","stream":{"role":"standby","state":"behind","detail":"no image for 2.1s","term":1003,"seq":42,"ageMs":2100,"lagMs":3,"intervalMs":100,"bytes":4165,"persistedTerm":1003,"persistedSeq":40,"takeoverImageAgeMs":-1}}`))
	for _, want := range []string{"state stream: behind; image 42 of term 1003, 2.1s old (lag 3ms), 4165 bytes every 100ms; retain file at image 40 of term 1003 (no image for 2.1s)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("want %q in\n%s", want, out.String())
		}
	}
	out.Reset()
	printClusterStatus(&out, []byte(`{"mode":"quorum","pod":"a","leader":"a","isLeader":true,"stream":{"role":"leader","state":"sending","term":1004,"seq":7,"ageMs":-1,"intervalMs":416,"bytes":4165,"budgetBytes":10000,"takeoverImageAgeMs":120,"flushMs":3.4,"takeover":"hot","retainInMemory":true}}`))
	if want := "state stream: sending; image 7 of term 1004, 4165 bytes every 416ms (budget 10000 B/s); last takeover loaded an image 120ms old, flushed in 3.4 ms; last takeover hot (oldest task state 0s old); retained values only in memory"; !strings.Contains(out.String(), want) {
		t.Fatalf("want %q in\n%s", want, out.String())
	}
	out.Reset()
	printClusterStatus(&out, []byte(`{"mode":"quorum","pod":"a","leader":"a","isLeader":true,"stream":{"role":"leader","state":"sending","ageMs":-1,"takeoverImageAgeMs":-1,"takeover":"cold","takeoverCold":"the image is of another nautilus build"}}`))
	if want := "state stream: sending; last takeover cold (the image is of another nautilus build)"; !strings.Contains(out.String(), want) {
		t.Fatalf("want %q in\n%s", want, out.String())
	}
}

// The leader's scan time with the state stream running and held (#710
// S1b ruling 8: RetainImage takes the programs' and the tags' locks at
// 10 Hz, and each scan the stream asked copies its frame). Measurement
// only: STREAM_MEASURE=1. Two sizes: the test project (2000 retained
// REALs, a 4 kB image), and one with 30 000 more frame leaves.
func TestMeasureScanWithTheStream(t *testing.T) {
	if os.Getenv("STREAM_MEASURE") == "" {
		t.Skip("measurement: STREAM_MEASURE=1")
	}
	for _, big := range []bool{false, true} {
		files := pairStreamProject
		if big {
			files = func(a, b, w string) fstest.MapFS {
				f := pairStreamProject(a, b, w)
				f["main.st"] = &fstest.MapFile{Data: []byte(strings.Replace(streamMain, "VAR RETAIN Runs : DINT; END_VAR", "VAR RETAIN Runs : DINT; END_VAR\nVAR Big : ARRAY[1..30000] OF REAL; END_VAR", 1))}
				return f
			}
		}
		testPairFiles = files
		r := newPairRigFull(t, nil, nil)
		testPairFiles = nil
		lead := r.leader()
		pairWait(t, "the standby in sync", 10*time.Second, func() bool {
			s := r.streamStatus(other(lead))
			return s != nil && s.State == "in-sync"
		})
		sample := func(n int) []float64 {
			var mu sync.Mutex
			var ms []float64
			done := make(chan struct{})
			cancel := r.nodes[lead].rt.OnScan(func(*runtime.Tags) {
				mu.Lock()
				defer mu.Unlock()
				if len(ms) < n {
					ms = append(ms, r.nodes[lead].rt.Stats().LastMs)
					if len(ms) == n {
						close(done)
					}
				}
			})
			<-done
			cancel()
			sort.Float64s(ms)
			return ms
		}
		pct := func(ms []float64, p float64) float64 { return ms[int(p*float64(len(ms)-1))] }
		on := sample(600)
		r.nodes[lead].m.stream.hold.Store(true)
		pairWait(t, "the send loop to hold", 5*time.Second, r.nodes[lead].m.stream.held.Load)
		off := sample(600)
		s := r.streamStatus(lead)
		t.Logf("big=%v image %d B every %d ms: scan with the stream p50/p99/max %.3f/%.3f/%.3f ms; held %.3f/%.3f/%.3f ms",
			big, s.Bytes, s.IntervalMs, pct(on, .5), pct(on, .99), on[len(on)-1], pct(off, .5), pct(off, .99), off[len(off)-1])
		r.nodes["a"].crash()
		r.nodes["b"].crash()
	}
}

// retainFile is member n's retain file in the stream rig.
func (r *pairRig) retainFile(n string) string {
	return filepath.Join(r.env[n]["STATE_DIRECTORY"], "retain.json")
}

func readRetain(t *testing.T, path string) retain.State {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st retain.State
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func writeRetain(t *testing.T, path string, st retain.State) {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// inSync waits for member n's stream to be in sync.
func (r *pairRig) inSync(t *testing.T, n string) {
	t.Helper()
	pairWait(t, n+"'s stream in sync", 10*time.Second, func() bool {
		s := r.streamStatus(n)
		return s != nil && s.State == "in-sync"
	})
}

// A takeover keeps an image the standby applied but has not yet written
// (its 2 s persist held): the flush writes it before the store is read,
// with the follower already stopped, and empties the shadow (review F1).
func TestPairTakeoverFlushesAnImageNotYetPersisted(t *testing.T) {
	r := newStreamRig(t, nil)
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	st := r.nodes[stand].m.stream
	st.persistHold.Store(true)
	v := r.writeStream(t, lead, 81.5, 82.25, 8.5, "")
	after := r.streamStatus(lead).Seq
	var want uint64
	pairWait(t, "the leader's next image", 5*time.Second, func() bool { want = r.streamStatus(lead).Seq; return want > after })
	pairWait(t, "the standby to apply it", 5*time.Second, func() bool { return r.streamStatus(stand).Seq >= want })
	if s := r.streamStatus(stand); s.PersistedSeq >= want && s.PersistedTerm == s.Term {
		t.Fatalf("the standby's file already holds image %d: the case proves nothing", s.PersistedSeq)
	}
	r.nodes[lead].crash()
	if got := r.leader(); got != stand {
		t.Fatalf("leader %q, want %s", got, stand)
	}
	r.checkStream(t, stand, "after a takeover on an unpersisted image", v)
	st.mu.Lock()
	following := st.flushFollowing
	st.mu.Unlock()
	if following {
		t.Error("the follower still ran when the takeover's flush wrote")
	}
	if term, seq := st.shadow.Position(); term != 0 || seq != 0 {
		t.Errorf("the shadow after the takeover stands at (%d, %d), want empty", term, seq)
	}
}

// A leader sends nothing once it no longer leads: its stream ends at
// once, not after a frame-copy wait (review F1).
func TestPairStreamEndsWhenTheLeaderStepsDown(t *testing.T) {
	r := newStreamRig(t, nil)
	lead := r.leader()
	r.inSync(t, other(lead))
	req, _ := http.NewRequest(http.MethodGet, r.peerScheme()+r.peer[lead]+streamPath(r.cluster()), nil)
	req.Header.Set(node.TokenHeader, r.token)
	resp, err := r.peerHTTPAs(other(lead), lead).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the leader's stream: %d", resp.StatusCode)
	}
	var hdr [9]byte
	if _, err := io.ReadFull(resp.Body, hdr[:]); err != nil || hdr[0] != streamImage {
		t.Fatalf("the first frame: %v %q", err, hdr[0])
	}
	ended := make(chan time.Time, 1)
	go func() { io.Copy(io.Discard, resp.Body); ended <- time.Now() }()
	stepped := time.Now()
	r.nodes[lead].m.q.Release()
	select {
	case at := <-ended:
		if d := at.Sub(stepped); d > 2*time.Second {
			t.Fatalf("the stream ended %s after the step-down, want at once", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not end after the step-down")
	}
	// A controller that does not lead answers 503.
	req2, _ := http.NewRequest(http.MethodGet, r.peerScheme()+r.peer[lead]+streamPath(r.cluster()), nil)
	req2.Header.Set(node.TokenHeader, r.token)
	if resp2, err := r.peerHTTPAs(other(lead), lead).Do(req2); err == nil {
		resp2.Body.Close()
		if resp2.StatusCode != http.StatusServiceUnavailable && !r.nodes[lead].m.q.IsLeader() {
			t.Errorf("a controller that does not lead answers %d, want 503", resp2.StatusCode)
		}
	}
}

// A standby whose retain file stands after the leader's images takes
// none (review F2): a file of a later term is a floor from the start, so
// its shadow applies nothing; a file of the same term at a later image
// refuses the carry. Either way the file is untouched and the stream says
// refused, never in sync.
func TestPairStandbyTakesNothingBelowItsFile(t *testing.T) {
	r := newStreamRig(t, nil)
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	leadTerm := r.streamStatus(lead).Term
	for _, c := range []struct {
		name string
		pos  retain.StreamPos
		why  string // the refusal's own reason, not a restart's first moments
	}{
		{"a later term", retain.StreamPos{Term: leadTerm + 1000}, "below one this controller already took"},
		{"the same term, a later image", retain.StreamPos{Term: leadTerm, Seq: 1 << 40}, "the retain file stands at"},
	} {
		r.nodes[stand].crash()
		st := readRetain(t, r.retainFile(stand))
		st.Stream, st.Tags["SP"] = &c.pos, -7.0
		writeRetain(t, r.retainFile(stand), st)
		r.restart(stand)
		pairWait(t, c.name+": the stream refused for it", 10*time.Second, func() bool {
			s := r.streamStatus(stand)
			return s != nil && s.State == "refused" && strings.Contains(s.Detail, c.why)
		})
		got := readRetain(t, r.retainFile(stand))
		if got.Stream == nil || *got.Stream != c.pos || got.Tags["SP"] != -7.0 {
			t.Errorf("%s: the file moved to %+v, SP %v; want it untouched", c.name, got.Stream, got.Tags["SP"])
		}
		if c.pos.Term > leadTerm {
			if s := r.streamStatus(stand); s.Seq != 0 {
				t.Errorf("%s: the shadow applied image %d of term %d below the file's term", c.name, s.Seq, s.Term)
			}
		}
	}
}

// A standby whose retain file takes no image says so (review F3): not in
// sync but not-persisted, until a carry succeeds again.
func TestPairStandbySaysItsFileTakesNoImage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	dirs := map[string]string{"a": t.TempDir(), "b": t.TempDir()}
	r := newStreamRig(t, func(n string, p *project.Project) { p.Retain.File = filepath.Join(dirs[n], "retain.json") })
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	os.Chmod(dirs[stand], 0o500)
	t.Cleanup(func() { os.Chmod(dirs[stand], 0o700) })
	r.writeStream(t, lead, 91.5, 92.25, 9.5, "")
	pairWait(t, "the standby to say its file takes no image", 10*time.Second, func() bool {
		return r.streamStatus(stand).State == "not-persisted"
	})
	if !r.hasWarning(stand, "not-persisted") {
		pairWait(t, "the warning", 5*time.Second, func() bool { return r.hasWarning(stand, "not-persisted") })
	}
	os.Chmod(dirs[stand], 0o700)
	r.inSync(t, stand)
}

// A leader whose takeover could not read its retain store runs on initial
// values and streams nothing, so the standby's file keeps the last good
// values (review F5); both say why.
func TestPairALeaderWithAnUnreadableStoreStreamsNothing(t *testing.T) {
	r := newStreamRig(t, nil)
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	v := r.writeStream(t, lead, 71.5, 72.25, 7.5, "")
	r.imaged(t, lead)
	r.nodes[stand].crash()
	r.nodes[lead].crash()
	// The old standby comes back first, alone, with its file unreadable.
	if err := os.WriteFile(r.retainFile(stand), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.restart(stand)
	if got := r.leader(); got != stand {
		t.Fatalf("leader %q, want %s", got, stand)
	}
	r.restart(lead) // it follows now, with a file holding v
	pairWait(t, "the new standby to hear why nothing comes", 10*time.Second, func() bool {
		s := r.streamStatus(lead)
		return s != nil && strings.Contains(s.Detail, "retain store could not be read")
	})
	if s := r.streamStatus(stand); s.State != "held" {
		t.Errorf("the leader's stream: %q, want held", s.State)
	}
	if got := readRetain(t, r.retainFile(lead)); got.Tags["SP"] != v.sp {
		t.Errorf("the standby's file holds SP %v, want the last good %v", got.Tags["SP"], v.sp)
	}
}

// A leader that closes (a crash in the rig, a stop in naut run) ends the
// streams it serves at once: its peer listener's shutdown must not wait
// on a stream that never ends by itself (full-suite finding: the wait
// let the standby take over before a test's next step). Stopping the
// stream ends its handlers while the leader still leads, and a crash then
// takes no more than a moment.
func TestPairAClosingLeaderEndsItsStreamsAtOnce(t *testing.T) {
	r := newStreamRig(t, nil)
	lead := r.leader()
	r.inSync(t, other(lead))
	st := r.nodes[lead].m.stream
	if st.sending.Load() == 0 {
		t.Fatal("no stream is served: the case proves nothing")
	}
	r.nodes[lead].m.streamStop()
	<-r.nodes[lead].m.streamDone
	pairWait(t, "the served streams to end", time.Second, func() bool { return st.sending.Load() == 0 })
	if !r.nodes[lead].m.q.IsLeader() {
		t.Fatal("the leader lost its lease meanwhile: the case proves nothing")
	}
	start := time.Now()
	r.nodes[lead].crash()
	if d := time.Since(start); d > time.Second {
		t.Errorf("the leader took %s to close, want well under a second", d)
	}
}

// #710: a pair member's status carries the writes cut at the send
// deadline: on the controller's own Status and on GET /api/cluster.
func TestPairStatusShowsWritesCut(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	fence.ResetCutsForTest()
	t.Cleanup(fence.ResetCutsForTest)
	fence.Cut("plc9")
	if got := r.nodes[lead].m.q.Status().WritesCut["plc9"]; got != 1 {
		t.Fatalf("Status().WritesCut = %v, want plc9 1", r.nodes[lead].m.q.Status().WritesCut)
	}
	code, body, _ := r.call(lead, "GET", "/api/cluster", "")
	if code != 200 || !strings.Contains(body, `"writesCut":{"plc9":1}`) {
		t.Fatalf("GET /api/cluster = %d %s; want writesCut with plc9", code, body)
	}
}

// The leader sends each standby the newest image format both read (#710
// S2): format 1, with no tags, to one that names none (a build before S2,
// mid rolling update, which reads only 1 and must still carry its retained
// values); format 2, with the carried tags, to one that names 2 and not 3;
// format 3, with the takeover hold too (#710 (b2)), to one of this build.
func TestPairStreamSendsEachStandbyAFormatItReads(t *testing.T) {
	r := newStreamRig(t, nil)
	lead := r.leader()
	r.inSync(t, other(lead))
	first := func(formats string) *runtime.Image {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, r.peerScheme()+r.peer[lead]+streamPath(r.cluster()), nil)
		req.Header.Set(node.TokenHeader, r.token)
		if formats != "" {
			req.Header.Set(streamFormatsHeader, formats)
		}
		resp, err := r.peerHTTPAs(other(lead), lead).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("formats %q: the leader answers %d", formats, resp.StatusCode)
		}
		var hdr [9]byte
		if _, err := io.ReadFull(resp.Body, hdr[:]); err != nil || hdr[0] != streamImage {
			t.Fatalf("formats %q: the first frame: %v %q", formats, err, hdr[0])
		}
		body := make([]byte, int(hdr[5])<<24|int(hdr[6])<<16|int(hdr[7])<<8|int(hdr[8]))
		if _, err := io.ReadFull(resp.Body, body); err != nil {
			t.Fatal(err)
		}
		var img runtime.Image
		if err := runtime.DecodeImage(body, &img); err != nil {
			t.Fatalf("formats %q: %v", formats, err)
		}
		return &img
	}
	if img := first(""); img.Format != 1 || img.TagsLayout != "" || len(img.Retain) == 0 {
		t.Errorf("no formats named: format %d, tags layout %q, %d retain bytes; want format 1 with the retain section", img.Format, img.TagsLayout, len(img.Retain))
	}
	if img := first("1,2"); img.Format != 2 || img.TagsLayout == "" || img.Tags.Len() == 0 {
		t.Errorf("formats 1,2: format %d, tags layout %q, %d store tags; want format 2 with the tags (Mode)", img.Format, img.TagsLayout, img.Tags.Len())
	}
	if img := first(streamFormats()); img.Format != 3 || img.TagsLayout == "" || img.Tags.Len() == 0 {
		t.Errorf("formats %q: format %d, tags layout %q, %d store tags; want format 3 with the tags (Mode)", streamFormats(), img.Format, img.TagsLayout, img.Tags.Len())
	}
}

// askedFormat reads the standby's formats leniently: the newest this build
// writes that the standby names, else 1.
func TestStreamAskedFormat(t *testing.T) {
	for h, want := range map[string]byte{"": 1, "1": 1, "1,2": 2, " 2 , 1 ": 2, "1,2,9": 2, "9": 1, "x,y": 1} {
		if got := askedFormat(h); got != want {
			t.Errorf("askedFormat(%q) = %d, want %d", h, got, want)
		}
	}
}
