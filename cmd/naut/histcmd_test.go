package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestSampleOfRecordsNumbers(t *testing.T) {
	tags := map[string]any{
		"pv": 42.5,
		"sp": 50.0,
	}
	got := sampleOf(tags, []string{"*"}, nil)
	want := map[string]float64{"pv": 42.5, "sp": 50}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sampleOf = %v, want %v", got, want)
	}
}

func TestSampleOfRecordsBoolsAsZeroOne(t *testing.T) {
	tags := map[string]any{
		"running": true,
		"faulted": false,
	}
	got := sampleOf(tags, []string{"*"}, nil)
	want := map[string]float64{"running": 1, "faulted": 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sampleOf = %v, want %v", got, want)
	}
}

func TestSampleOfSkipsStringsAndNil(t *testing.T) {
	tags := map[string]any{
		"pv":    42.5,
		"name":  "TIC-101",
		"empty": nil,
	}
	got := sampleOf(tags, []string{"*"}, nil)
	want := map[string]float64{"pv": 42.5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sampleOf = %v, want %v (strings/nil must be skipped)", got, want)
	}
}

// TestSampleOfFlattensStructTag is the core regression test for the
// struct-archiving fix: a UDT tag arrives from GET /api/state as a nested
// JSON object (and may itself nest a struct, and may hold an array
// member), and sampleOf must walk into it and record each numeric/bool
// leaf under a dotted "Tag.MEMBER[.SUB]" name — while still skipping
// string leaves at any depth.
func TestSampleOfFlattensStructTag(t *testing.T) {
	tags := map[string]any{
		// An AnalogInput-shaped UDT tag: scalar reading at .VALUE, a
		// boolean alarm member, a string name member (skip), a nested
		// struct member, and an array member.
		"RTU9_WEL15_FIT_001": map[string]any{
			"VALUE": 42.5,
			"HH":    true,
			"NAME":  "flow transmitter",
			"RAW":   map[string]any{"COUNTS": 12345.0},
			"LOG":   []any{1.0, 2.0, 3.0},
		},
		// A plain top-level scalar sibling, unaffected by flattening.
		"RTU9_WellFlowGpm": 123.4,
	}
	got := sampleOf(tags, []string{"*"}, nil)
	want := map[string]float64{
		"RTU9_WEL15_FIT_001.VALUE":      42.5,
		"RTU9_WEL15_FIT_001.HH":         1,
		"RTU9_WEL15_FIT_001.RAW.COUNTS": 12345,
		"RTU9_WEL15_FIT_001.LOG[0]":     1,
		"RTU9_WEL15_FIT_001.LOG[1]":     2,
		"RTU9_WEL15_FIT_001.LOG[2]":     3,
		"RTU9_WellFlowGpm":              123.4,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sampleOf(struct tag) = %v, want %v", got, want)
	}
}

func TestSampleOfGlobFiltering(t *testing.T) {
	tags := map[string]any{
		"PIT_101": 1.0,
		"PIT_102": 2.0,
		"TIC_101": 3.0,
		"level":   4.0,
	}
	got := sampleOf(tags, []string{"PIT_*"}, nil)
	want := map[string]float64{"PIT_101": 1, "PIT_102": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sampleOf with PIT_* = %v, want %v", got, want)
	}
}

func TestSampleOfMultiplePatterns(t *testing.T) {
	tags := map[string]any{
		"PIT_101": 1.0,
		"level":   2.0,
		"other":   3.0,
	}
	got := sampleOf(tags, []string{"PIT_*", "level"}, nil)
	want := map[string]float64{"PIT_101": 1, "level": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sampleOf = %v, want %v", got, want)
	}
}

// TestSampleOfDottedPatternMatchesNestedMember confirms the fleet-facing
// patterns from the historian's docs actually match dotted leaf names:
// "*.VALUE" reaches any struct's VALUE member regardless of tag prefix,
// and "*_FIT_*" matches the whole dotted leaf name (tag name *and*
// member), not just the top-level tag.
func TestSampleOfDottedPatternMatchesNestedMember(t *testing.T) {
	tags := map[string]any{
		"RTU9_WEL15_FIT_001": map[string]any{
			"VALUE": 42.5,
			"HH":    true,
		},
		"RTU9_WellFlowGpm": 99.0,
	}

	got := sampleOf(tags, []string{"*.VALUE"}, nil)
	want := map[string]float64{"RTU9_WEL15_FIT_001.VALUE": 42.5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sampleOf with *.VALUE = %v, want %v", got, want)
	}

	got = sampleOf(tags, []string{"*_FIT_*"}, nil)
	want = map[string]float64{
		"RTU9_WEL15_FIT_001.VALUE": 42.5,
		"RTU9_WEL15_FIT_001.HH":    1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sampleOf with *_FIT_* = %v, want %v", got, want)
	}
}

// TestSampleOfExcludeTrimsMatchedNoise confirms -exclude patterns can
// veto a leaf that -tags would otherwise pick up, e.g. dropping
// simulation/diagnostic members out of an otherwise-broad "*" pattern.
func TestSampleOfExcludeTrimsMatchedNoise(t *testing.T) {
	tags := map[string]any{
		"FIT_001": map[string]any{
			"VALUE": 42.5,
			"SIM":   true,
			"DB":    7.0,
		},
	}
	got := sampleOf(tags, []string{"*"}, []string{"*.SIM*", "*.DB"})
	want := map[string]float64{"FIT_001.VALUE": 42.5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sampleOf with excludes = %v, want %v", got, want)
	}
}

// fakeSink is a Sink double (satisfies hist.Sink) for testing the
// collector without a real database.
type fakeSink struct {
	mu      sync.Mutex
	inserts []map[string]float64
}

func (f *fakeSink) Insert(ts time.Time, vals map[string]float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserts = append(f.inserts, vals)
	return nil
}

func (f *fakeSink) snapshot() []map[string]float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]float64, len(f.inserts))
	copy(out, f.inserts)
	return out
}

// TestCollectOnceAgainstRealisticFrame drives collectOnce against an
// httptest server serving a realistic /api/state Frame JSON (server.Frame
// shape: {"ts":...,"scans":...,"tags":{...},"scan":{...}}), including a
// struct tag, and asserts what a fake Sink recorded.
func TestCollectOnceAgainstRealisticFrame(t *testing.T) {
	frame := map[string]any{
		"ts":    time.Now().UnixMilli(),
		"scans": 12345,
		"tags": map[string]any{
			"PIT_101":  72.3,
			"PIT_102":  68.9,
			"TempSP":   65.0,
			"HeaterOn": true,
			"Name":     "unit-1",
			"FIT_001": map[string]any{
				"VALUE": 12.5,
				"HH":    false,
			},
		},
		"scan": map[string]any{"count": 12345},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/state" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(frame)
	}))
	defer srv.Close()

	sink := &fakeSink{}
	hc := &http.Client{Timeout: 2 * time.Second}
	collectOnce(hc, sink, srv.URL, []string{"PIT_*", "TempSP", "HeaterOn", "*.VALUE", "*.HH"}, nil, &changeFilter{}, 0, 0)

	inserts := sink.snapshot()
	if len(inserts) != 1 {
		t.Fatalf("got %d inserts, want 1", len(inserts))
	}
	want := map[string]float64{
		"PIT_101":       72.3,
		"PIT_102":       68.9,
		"TempSP":        65.0,
		"HeaterOn":      1,
		"FIT_001.VALUE": 12.5,
		"FIT_001.HH":    0,
	}
	if !reflect.DeepEqual(inserts[0], want) {
		t.Fatalf("recorded sample = %v, want %v", inserts[0], want)
	}
}

// TestCollectOnceSourceUnreachable asserts the skip-and-continue behavior:
// an unreachable source (e.g. mid-failover) must not record anything and
// must not panic or block — collectOnce just returns.
func TestCollectOnceSourceUnreachable(t *testing.T) {
	sink := &fakeSink{}
	hc := &http.Client{Timeout: 200 * time.Millisecond}
	// Nothing listens here.
	collectOnce(hc, sink, "http://127.0.0.1:1", []string{"*"}, nil, &changeFilter{}, 0, 0)

	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("got %d inserts against an unreachable source, want 0", got)
	}
}

// TestChangeFilterDisabledByDefault confirms minInterval <= 0 (the
// historian's default) makes apply a no-op passthrough — every matched
// tag is recorded on every poll, exactly like before -deadband/
// -min-interval existed.
func TestChangeFilterDisabledByDefault(t *testing.T) {
	f := &changeFilter{}
	now := time.Now()
	sample := map[string]float64{"pv": 1.0}

	got := f.apply(now, sample, 0, 0)
	if !reflect.DeepEqual(got, sample) {
		t.Fatalf("apply with minInterval=0 = %v, want unchanged %v", got, sample)
	}
	// Called again immediately with the exact same value — still passed
	// through unchanged, since filtering is disabled.
	got = f.apply(now, sample, 0, 0)
	if !reflect.DeepEqual(got, sample) {
		t.Fatalf("apply with minInterval=0 (2nd call) = %v, want unchanged %v", got, sample)
	}
}

// TestChangeFilterRecordsOnChangeOrHeartbeat exercises the enabled path:
// a tag is dropped when unchanged within the deadband and minInterval
// hasn't elapsed, kept when it moves past the deadband, and kept again
// once minInterval has elapsed even without a qualifying change.
func TestChangeFilterRecordsOnChangeOrHeartbeat(t *testing.T) {
	f := &changeFilter{}
	t0 := time.Now()
	deadband := 0.5
	minInterval := time.Minute

	// First sighting of "pv": always recorded.
	got := f.apply(t0, map[string]float64{"pv": 10.0}, deadband, minInterval)
	if _, ok := got["pv"]; !ok {
		t.Fatalf("first sighting of pv not recorded: %v", got)
	}

	// Small move within the deadband, well before minInterval: dropped.
	got = f.apply(t0.Add(time.Second), map[string]float64{"pv": 10.2}, deadband, minInterval)
	if _, ok := got["pv"]; ok {
		t.Fatalf("in-deadband change before min-interval was recorded: %v", got)
	}

	// Move past the deadband: recorded, and resets the reference value.
	got = f.apply(t0.Add(2*time.Second), map[string]float64{"pv": 11.0}, deadband, minInterval)
	if v, ok := got["pv"]; !ok || v != 11.0 {
		t.Fatalf("past-deadband change not recorded as 11.0: %v", got)
	}

	// No change at all, but minInterval has now elapsed since the last
	// recorded sample (t0+2s): heartbeat recording.
	got = f.apply(t0.Add(2*time.Second+minInterval), map[string]float64{"pv": 11.0}, deadband, minInterval)
	if v, ok := got["pv"]; !ok || v != 11.0 {
		t.Fatalf("heartbeat sample at min-interval elapsed not recorded: %v", got)
	}
}

// TestChangeFilterPerTagIndependent confirms filtering state is tracked
// independently per tag name — one tag's change/heartbeat status never
// affects another's.
func TestChangeFilterPerTagIndependent(t *testing.T) {
	f := &changeFilter{}
	t0 := time.Now()
	f.apply(t0, map[string]float64{"a": 1.0, "b": 1.0}, 0.5, time.Minute)

	// "a" moves past the deadband, "b" doesn't; well before min-interval.
	got := f.apply(t0.Add(time.Second), map[string]float64{"a": 5.0, "b": 1.1}, 0.5, time.Minute)
	if _, ok := got["a"]; !ok {
		t.Fatalf("changed tag 'a' not recorded: %v", got)
	}
	if _, ok := got["b"]; ok {
		t.Fatalf("unchanged tag 'b' recorded early: %v", got)
	}
}
