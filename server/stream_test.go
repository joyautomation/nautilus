package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// A stream fixture that gives the test the broadcast trigger. Frames are
// pumped by calling srv.broadcast() directly rather than by running the
// ticker, so "the third frame" means the third one this test asked for and
// nothing depends on wall-clock timing.
type streamFixture struct {
	t    *testing.T
	srv  *Server
	rt   *runtime.Runtime
	ts   *httptest.Server
	resp *http.Response
	sc   *bufio.Scanner
}

func openStream(t *testing.T, rt *runtime.Runtime, query string, opts ...Options) *streamFixture {
	t.Helper()
	srv := New(rt, opts...)
	ts := httptest.NewServer(srv.Handler())
	resp, err := ts.Client().Get(ts.URL + "/api/stream" + query)
	if err != nil {
		ts.Close()
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		ts.Close()
		t.Fatalf("stream status %d", resp.StatusCode)
	}
	f := &streamFixture{t: t, srv: srv, rt: rt, ts: ts, resp: resp,
		sc: bufio.NewScanner(resp.Body)}
	f.sc.Buffer(make([]byte, 1<<20), 1<<24)
	t.Cleanup(func() { resp.Body.Close(); ts.Close() })
	return f
}

// next reads the next frame off the wire.
func (f *streamFixture) next() Frame {
	f.t.Helper()
	for f.sc.Scan() {
		line := f.sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var fr Frame
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &fr); err != nil {
			f.t.Fatalf("bad frame: %v", err)
		}
		return fr
	}
	f.t.Fatalf("stream ended (%v)", f.sc.Err())
	return Frame{}
}

// tick broadcasts once, waiting for the client to have registered first —
// a connect is two goroutines and the test must not race the handler.
func (f *streamFixture) tick() {
	f.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.srv.mu.Lock()
		n := len(f.srv.clients)
		f.srv.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			f.t.Fatal("client never registered")
		}
		time.Sleep(time.Millisecond)
	}
	f.srv.broadcast()
}

// ── deltas ────────────────────────────────────────────────────────────────

func TestDeltaStreamSendsOnlyWhatChanged(t *testing.T) {
	rt := newTestRuntime(t)
	f := openStream(t, rt, "?delta=1")

	first := f.next()
	if !first.Full || first.Seq != 1 {
		t.Fatalf("first frame full=%v seq=%d, want true/1", first.Full, first.Seq)
	}
	if _, ok := first.Tags["Level"]; !ok {
		t.Fatalf("first frame is not a whole snapshot: %v", first.Tags)
	}
	nTags := len(first.Tags)
	if nTags < 3 {
		t.Fatalf("expected the whole store, got %d tags", nTags)
	}

	// Nothing moved: an empty delta, not a repeat of the store.
	f.tick()
	quiet := f.next()
	if quiet.Full {
		t.Error("a steady-state frame claimed to be full")
	}
	if len(quiet.Tags) != 0 {
		t.Errorf("quiet frame carried %v", quiet.Tags)
	}
	if quiet.Seq != 2 {
		t.Errorf("seq = %d, want 2", quiet.Seq)
	}

	// One tag moves: exactly that tag, and nothing else — in particular not
	// the tags whose values happen to equal each other.
	rt.Tags().SetReal("SP", 71)
	f.tick()
	d := f.next()
	if len(d.Tags) != 1 {
		t.Fatalf("delta = %v, want just SP", d.Tags)
	}
	if got := d.Tags["SP"]; got != 71.0 {
		t.Errorf("SP = %v, want 71", got)
	}
	if d.Full {
		t.Error("a delta frame claimed to be full")
	}
	if d.Seq != 3 {
		t.Errorf("seq = %d, want 3", d.Seq)
	}

	// A write of the SAME value is not a change — the write-generation
	// property the whole delta scheme rests on, verified end to end.
	rt.Tags().SetReal("SP", 71)
	f.tick()
	if again := f.next(); len(again.Tags) != 0 {
		t.Errorf("re-writing the same value produced a delta: %v", again.Tags)
	}
}

// A delta frame's HEADLINE diagnostics ride every tick — the timestamp and
// the scan counter a dashboard watches to see the loop turning — and the
// block behind them arrives on its cadence rather than four times a second
// (see blocks_test.go, and Options.DiagnosticsInterval for why the two are
// separated at all).
func TestDeltaFrameKeepsDiagnostics(t *testing.T) {
	rt := newTestRuntime(t)
	f := openStream(t, rt, "?delta=1")
	f.next()
	rt.Scan()
	f.tick()
	d := f.next()
	if d.Scans == 0 || d.TS == 0 {
		t.Errorf("delta frame lost its headline diagnostics: scans=%d ts=%d", d.Scans, d.TS)
	}
	if d.Scan == nil || d.Scan.TargetMs == 0 {
		t.Errorf("the first broadcast after connect did not re-offer the scan block: %+v", d.Scan)
	}
}

// The tag SET changing is the one thing a delta cannot express, so it
// forces a full frame rather than leaving the client holding a tag the
// controller no longer has.
func TestDeltaResyncsWhenTheTagSetChanges(t *testing.T) {
	rt := newTestRuntime(t)
	f := openStream(t, rt, "?delta=1")
	f.next()

	f.tick()
	if quiet := f.next(); quiet.Full {
		t.Fatal("unexpected resync")
	}

	rt.Tags().SetReal("NewlyAppeared", 3)
	f.tick()
	full := f.next()
	if !full.Full {
		t.Fatal("a new tag did not force a full frame")
	}
	if _, ok := full.Tags["Level"]; !ok {
		t.Errorf("the resync frame is not whole: %v", full.Tags)
	}
	if _, ok := full.Tags["NewlyAppeared"]; !ok {
		t.Error("the resync frame is missing the new tag")
	}

	// And it settles straight back into deltas.
	f.tick()
	if after := f.next(); after.Full || len(after.Tags) != 0 {
		t.Errorf("did not return to deltas: full=%v tags=%v", after.Full, after.Tags)
	}
}

// The periodic resync bounds how long a client can stay wrong if anything
// outside the protocol's guarantees ever does go wrong.
func TestDeltaPeriodicResync(t *testing.T) {
	rt := newTestRuntime(t)
	// A resync interval short enough that the next tick is already due.
	f := openStream(t, rt, "?delta=1", Options{ResyncInterval: time.Nanosecond})
	if first := f.next(); !first.Full {
		t.Fatal("first frame not full")
	}
	f.tick()
	if second := f.next(); !second.Full {
		t.Error("the periodic resync did not fire")
	}
}

func TestResyncCanBeDisabled(t *testing.T) {
	rt := newTestRuntime(t)
	f := openStream(t, rt, "?delta=1", Options{ResyncInterval: -1})
	f.next()
	f.tick()
	if second := f.next(); second.Full {
		t.Error("a negative ResyncInterval still resynced")
	}
}

// ?full=1 is the escape hatch: a client that distrusts its own merge asks
// for whole frames even while passing delta=1.
func TestFullParamOverridesDelta(t *testing.T) {
	rt := newTestRuntime(t)
	f := openStream(t, rt, "?delta=1&full=1")
	first := f.next()
	if first.Full || first.Seq != 0 {
		t.Errorf("full=1 stream marked itself as a delta stream: full=%v seq=%d",
			first.Full, first.Seq)
	}
	f.tick()
	second := f.next()
	if _, ok := second.Tags["Level"]; !ok {
		t.Errorf("full=1 stream sent a partial frame: %v", second.Tags)
	}
}

// The default stream is byte-for-byte what it always was: no delta, no seq,
// no full marker, every tag every time. Every client this repo does not own
// depends on exactly this.
func TestDefaultStreamIsUnchanged(t *testing.T) {
	rt := newTestRuntime(t)
	f := openStream(t, rt, "")
	first := f.next()
	if first.Seq != 0 || first.Full {
		t.Errorf("plain stream grew delta fields: seq=%d full=%v", first.Seq, first.Full)
	}
	rt.Tags().SetReal("SP", 90)
	f.tick()
	second := f.next()
	if _, ok := second.Tags["Level"]; !ok {
		t.Errorf("plain stream sent a partial frame: %v", second.Tags)
	}
	if second.Seq != 0 {
		t.Errorf("plain stream carried a seq: %d", second.Seq)
	}
}

// Two clients at different points in the stream must each get their own
// correct delta — the case the shared per-tick sweep has to get right.
func TestDeltaIsPerClient(t *testing.T) {
	rt := newTestRuntime(t)
	a := openStream(t, rt, "?delta=1")
	a.next()

	// A moves ahead by one change while B is not yet connected.
	rt.Tags().SetReal("SP", 71)
	a.tick()
	if d := a.next(); len(d.Tags) != 1 {
		t.Fatalf("A's delta = %v", d.Tags)
	}

	// B connects to the SAME server and gets a full frame including that
	// change; then one more change must reach both, and only that one.
	srv := a.srv
	resp, err := a.ts.Client().Get(a.ts.URL + "/api/stream?delta=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b := &streamFixture{t: t, srv: srv, rt: rt, ts: a.ts, resp: resp,
		sc: bufio.NewScanner(resp.Body)}
	b.sc.Buffer(make([]byte, 1<<20), 1<<24)
	if first := b.next(); !first.Full || first.Tags["SP"] != 71.0 {
		t.Fatalf("B's first frame = full:%v %v", first.Full, first.Tags)
	}

	// Wait for both to be registered, then one tick for both.
	deadline := time.Now().Add(2 * time.Second)
	for {
		srv.mu.Lock()
		n := len(srv.clients)
		srv.mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("both clients never registered")
		}
		time.Sleep(time.Millisecond)
	}
	rt.Tags().SetReal("SP", 72)
	srv.broadcast()
	for _, f := range []*streamFixture{a, b} {
		d := f.next()
		if len(d.Tags) != 1 || d.Tags["SP"] != 72.0 {
			t.Errorf("delta = %v, want just SP:72", d.Tags)
		}
	}
}

// ── filters ───────────────────────────────────────────────────────────────

func TestStreamTagFilter(t *testing.T) {
	rt := newTestRuntime(t)
	f := openStream(t, rt, "?tags=S*")
	first := f.next()
	if _, ok := first.Tags["SP"]; !ok {
		t.Errorf("filtered frame missing SP: %v", first.Tags)
	}
	if _, ok := first.Tags["Level"]; ok {
		t.Errorf("filter let Level through: %v", first.Tags)
	}
	// The filter applies to every frame, not just the first.
	rt.Tags().SetReal("Level", 41)
	f.tick()
	if second := f.next(); len(second.Tags) != 1 {
		t.Errorf("second frame = %v, want only SP", second.Tags)
	}
}

func TestFilteredDeltaStream(t *testing.T) {
	rt := newTestRuntime(t)
	f := openStream(t, rt, "?delta=1&tags=SP")
	if first := f.next(); len(first.Tags) != 1 || !first.Full {
		t.Fatalf("first = full:%v %v", first.Full, first.Tags)
	}
	// A change OUTSIDE the filter produces an empty delta, not a dropped
	// subscription: the client stays connected and stays quiet.
	rt.Tags().SetReal("Level", 42)
	f.tick()
	if d := f.next(); len(d.Tags) != 0 {
		t.Errorf("out-of-filter change leaked: %v", d.Tags)
	}
	rt.Tags().SetReal("SP", 73)
	f.tick()
	if d := f.next(); len(d.Tags) != 1 || d.Tags["SP"] != 73.0 {
		t.Errorf("in-filter delta = %v", d.Tags)
	}
}

func TestStateTagFilter(t *testing.T) {
	srv := New(newTestRuntime(t))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/state?tags=Lev*,Out", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var f Frame
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Tags) != 2 {
		t.Fatalf("tags = %v, want Level and Out", f.Tags)
	}
	if _, ok := f.Tags["Level"]; !ok {
		t.Error("Level missing")
	}
	if _, ok := f.Tags["SP"]; ok {
		t.Error("SP leaked through the filter")
	}
	// Diagnostics are never filtered — a subscription narrows tags, not the
	// controller's own health.
	if f.Scan.TargetMs == 0 {
		t.Error("filtered state lost its scan stats")
	}
}

// A pattern path.Match cannot compile must be an error, not a silent
// match-nothing that renders as an empty plant.
func TestBadTagPatternIsRejected(t *testing.T) {
	srv := New(newTestRuntime(t))
	for _, path := range []string{"/api/state?tags=[bad", "/api/stream?tags=[bad"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 400 {
			t.Errorf("%s → %d, want 400", path, rec.Code)
		}
	}
}

func TestTagFilterParsing(t *testing.T) {
	got, err := parseTagFilter(" A* , ,B.C ")
	if err != nil || len(got) != 2 || got[0] != "A*" || got[1] != "B.C" {
		t.Fatalf("parseTagFilter = %v, %v", got, err)
	}
	if got, err := parseTagFilter("   "); err != nil || got != nil {
		t.Errorf("empty filter = %v, %v; want nil,nil", got, err)
	}
	long := strings.Repeat("a,", maxTagPatterns+2)
	if _, err := parseTagFilter(long); err == nil {
		t.Error("an unbounded pattern list was accepted")
	}
}

// A dot is an ordinary character to path.Match (its separator is "/"), so a
// glob spans a whole dotted name — which is what a screen bound to UDT
// members needs.
func TestMatchAnySpansDottedNames(t *testing.T) {
	cases := []struct {
		pats []string
		name string
		want bool
	}{
		{nil, "anything", true},
		{[]string{"Tank*"}, "Tank101", true},
		{[]string{"Tank*"}, "Tank101.Level", true},
		{[]string{"*.Level"}, "Tank101.Level", true},
		{[]string{"Tank*"}, "Pump101", false},
		{[]string{"A", "B*"}, "B9", true},
		{[]string{"A", "B*"}, "C9", false},
	}
	for _, c := range cases {
		if got := matchAny(c.pats, c.name); got != c.want {
			t.Errorf("matchAny(%v, %q) = %v", c.pats, c.name, got)
		}
	}
}

func TestFlagParsing(t *testing.T) {
	cases := map[string]bool{
		"/x":             false,
		"/x?delta":       true,
		"/x?delta=1":     true,
		"/x?delta=true":  true,
		"/x?delta=0":     false,
		"/x?delta=false": false,
	}
	for url, want := range cases {
		q := httptest.NewRequest("GET", url, nil).URL.Query()
		if got := flag(q, "delta"); got != want {
			t.Errorf("flag(%s) = %v, want %v", url, got, want)
		}
	}
}

// ── quality on the wire ───────────────────────────────────────────────────

func newQualityServer(t *testing.T) (*Server, *runtime.Runtime, *nio.Memory) {
	t.Helper()
	drv := nio.NewMemory()
	rt, err := runtime.New(runtime.Options{
		Program: testProgram,
		Driver:  drv,
		Inputs:  []string{"Level"},
		Outputs: []string{"Out"},
		Seed:    nio.Values{"Level": 40.0, "SP": 65.0, "Out": 0.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	return New(rt), rt, drv
}

func TestFrameOmitsQualityWhenEverythingIsGood(t *testing.T) {
	srv, _, _ := newQualityServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	if strings.Contains(rec.Body.String(), `"quality"`) {
		t.Errorf("a healthy frame carries a quality field: %s", rec.Body.String())
	}
}

func TestFrameCarriesQuality(t *testing.T) {
	srv, rt, drv := newQualityServer(t)
	drv.SetQuality("Level", nio.Stale)
	drv.SetQuality("Ghost", nio.NotConnected)
	rt.Scan()

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	var f Frame
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if f.Quality["Level"] != "stale" {
		t.Errorf("quality = %v, want Level:stale", f.Quality)
	}
	// A never-connected source has no value in the store, and must still be
	// reported — that is the whole point of NotConnected.
	if f.Quality["Ghost"] != "notConnected" {
		t.Errorf("quality = %v, want Ghost:notConnected", f.Quality)
	}
	if _, ok := f.Tags["Ghost"]; ok {
		t.Error("a never-connected source invented a tag value")
	}
	// Good tags are never named.
	if _, said := f.Quality["SP"]; said {
		t.Errorf("a Good tag appears in quality: %v", f.Quality)
	}
}

func TestQualityIsFiltered(t *testing.T) {
	srv, rt, drv := newQualityServer(t)
	drv.SetQuality("Level", nio.Stale)
	rt.Scan()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/state?tags=SP", nil))
	var f Frame
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Quality) != 0 {
		t.Errorf("quality survived a filter that excluded its tag: %v", f.Quality)
	}
}

// Quality rides on delta frames too, whole every time: it is the non-Good
// entries only, so it is already small, and a client that had to merge it
// would need a way to say "this one went back to Good".
func TestDeltaFrameCarriesQuality(t *testing.T) {
	drv := nio.NewMemory()
	rt, err := runtime.New(runtime.Options{
		Program: testProgram,
		Driver:  drv,
		Inputs:  []string{"Level"},
		Outputs: []string{"Out"},
		Seed:    nio.Values{"Level": 40.0, "SP": 65.0, "Out": 0.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	f := openStream(t, rt, "?delta=1")
	if first := f.next(); len(first.Quality) != 0 {
		t.Fatalf("healthy first frame carried quality %v", first.Quality)
	}
	drv.SetQuality("Level", nio.Bad)
	f.tick()
	d := f.next()
	if d.Quality["Level"] != "bad" {
		t.Errorf("delta frame quality = %v", d.Quality)
	}
	// It clears the same way — the map is replaced, never merged.
	drv.SetQuality("Level", nio.Good)
	f.tick()
	if d := f.next(); len(d.Quality) != 0 {
		t.Errorf("quality did not clear: %v", d.Quality)
	}
}

func TestMetaAdvertisesQualityAndDeltas(t *testing.T) {
	srv, _, _ := newQualityServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/meta", nil))
	var m metaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if !m.Quality {
		t.Error("meta.quality = false on a driver-bound controller")
	}
	if !m.Deltas {
		t.Error("meta.deltas = false")
	}
}

// ── the property everything else is in service of ─────────────────────────

// A delta client's merged state must equal the controller's store, always.
// Writes run CONCURRENTLY with the broadcasts, which is the case that
// catches a stamp read on the wrong side of the frame: credit a client with
// a generation its frame does not contain and the change is never resent,
// so one value freezes on screen until the next resync — silently, on a
// page that otherwise looks alive.
func TestDeltaMergedStateMatchesTheStore(t *testing.T) {
	const n = 3000
	rt := newTestRuntime(t)
	tags := rt.Tags()
	for i := 0; i < n; i++ {
		tags.SetReal(fmt.Sprintf("T%04d", i), 0)
	}
	// No resync to hide behind, and no repeated writes to repair a loss:
	// each tag is written EXACTLY ONCE during the concurrent phase, so a
	// change dropped by a mis-ordered generation stamp stays dropped and
	// this test sees it. (With tags written repeatedly the next write
	// silently repairs the last one, and the bug hides.)
	f := openStream(t, rt, "?delta=1", Options{ResyncInterval: -1})

	merged := map[string]any{}
	apply := func(fr Frame) {
		if fr.Full {
			merged = map[string]any{}
		}
		for k, v := range fr.Tags {
			merged[k] = v
		}
	}
	apply(f.next())

	// Walk every tag once from another goroutine while ticks go out.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			tags.SetReal(fmt.Sprintf("T%04d", i), float64(i)+0.5)
		}
	}()
	for i := 0; i < 80; i++ {
		f.tick()
		apply(f.next())
	}
	<-done

	// Quiesce: two more ticks with nothing writing must bring the client
	// exactly level with the store.
	f.tick()
	apply(f.next())
	f.tick()
	apply(f.next())

	want := tags.All()
	if len(merged) != len(want) {
		t.Fatalf("merged %d tags, store has %d", len(merged), len(want))
	}
	missed := 0
	for k, v := range want {
		got, ok := merged[k]
		if !ok {
			t.Fatalf("merged state is missing %s", k)
		}
		// JSON round-trips REALs as float64, which is what the store holds.
		if got != v {
			if missed < 5 {
				t.Errorf("%s = %v, store has %v", k, got, v)
			}
			missed++
		}
	}
	if missed > 0 {
		t.Fatalf("%d of %d tags were never delivered", missed, len(want))
	}
}
