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
	got := sampleOf(tags, []string{"*"})
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
	got := sampleOf(tags, []string{"*"})
	want := map[string]float64{"running": 1, "faulted": 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sampleOf = %v, want %v", got, want)
	}
}

func TestSampleOfSkipsStringsObjectsArrays(t *testing.T) {
	tags := map[string]any{
		"pv":     42.5,
		"name":   "TIC-101",
		"nested": map[string]any{"a": 1.0},
		"list":   []any{1.0, 2.0},
		"empty":  nil,
	}
	got := sampleOf(tags, []string{"*"})
	want := map[string]float64{"pv": 42.5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sampleOf = %v, want %v (strings/objects/arrays/nil must be skipped)", got, want)
	}
}

func TestSampleOfGlobFiltering(t *testing.T) {
	tags := map[string]any{
		"PIT_101": 1.0,
		"PIT_102": 2.0,
		"TIC_101": 3.0,
		"level":   4.0,
	}
	got := sampleOf(tags, []string{"PIT_*"})
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
	got := sampleOf(tags, []string{"PIT_*", "level"})
	want := map[string]float64{"PIT_101": 1, "level": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sampleOf = %v, want %v", got, want)
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
// shape: {"ts":...,"scans":...,"tags":{...},"scan":{...}}) and asserts
// what a fake Sink recorded.
func TestCollectOnceAgainstRealisticFrame(t *testing.T) {
	frame := map[string]any{
		"ts":    time.Now().UnixMilli(),
		"scans": 12345,
		"tags": map[string]any{
			"PIT_101":   72.3,
			"PIT_102":   68.9,
			"TempSP":    65.0,
			"HeaterOn":  true,
			"Name":      "unit-1",
			"AlarmList": []any{"HIGH_LEVEL"},
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
	collectOnce(hc, sink, srv.URL, []string{"PIT_*", "TempSP", "HeaterOn"})

	inserts := sink.snapshot()
	if len(inserts) != 1 {
		t.Fatalf("got %d inserts, want 1", len(inserts))
	}
	want := map[string]float64{
		"PIT_101":  72.3,
		"PIT_102":  68.9,
		"TempSP":   65.0,
		"HeaterOn": 1,
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
	collectOnce(hc, sink, "http://127.0.0.1:1", []string{"*"})

	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("got %d inserts against an unreachable source, want 0", got)
	}
}
