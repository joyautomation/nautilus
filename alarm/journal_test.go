package alarm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ev(ms int64, id, kind string) Event {
	return Event{TS: ms, ID: id, Name: id, Kind: kind, Priority: High, Site: "RTU9", State: "unack-active"}
}

func kindsOf(evs []Event) string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return strings.Join(out, ",")
}

func TestRingJournalRoundTrip(t *testing.T) {
	r := NewRing(3)
	for i, k := range []string{"active", "ack", "rtn", "active"} {
		if err := r.Append(ev(int64(i+1)*1000, "A", k)); err != nil {
			t.Fatal(err)
		}
	}
	if r.Len() != 3 {
		t.Fatalf("ring holds %d, want its cap of 3", r.Len())
	}
	got, err := r.Query(time.Time{}, time.Time{}, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	// Newest first, and the oldest event has aged out.
	if k := kindsOf(got); k != "active,rtn,ack" {
		t.Fatalf("ring query = %s, want active,rtn,ack", k)
	}
}

func TestJournalFiltering(t *testing.T) {
	r := NewRing(10)
	events := []Event{
		{TS: 1000, ID: "A", Kind: "active", Priority: High, Site: "RTU9"},
		{TS: 2000, ID: "B", Kind: "ack", Priority: Low, Site: "RTU9"},
		{TS: 3000, ID: "C", Kind: "active", Priority: Low, Site: "RTU12"},
		{TS: 4000, ID: "A", Kind: "rtn", Priority: High, Site: "RTU9"},
	}
	for _, e := range events {
		if err := r.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name     string
		from, to time.Time
		f        Filter
		want     string
	}{
		{"everything", time.Time{}, time.Time{}, Filter{}, "rtn,active,ack,active"},
		{"by site", time.Time{}, time.Time{}, Filter{Sites: []string{"RTU12"}}, "active"},
		{"by id", time.Time{}, time.Time{}, Filter{IDs: []string{"A"}}, "rtn,active"},
		{"by kind", time.Time{}, time.Time{}, Filter{Kinds: []string{"active"}}, "active,active"},
		{"by priority", time.Time{}, time.Time{}, Filter{Priorities: []string{"low"}}, "active,ack"},
		{"by range", time.UnixMilli(2000), time.UnixMilli(3000), Filter{}, "active,ack"},
		{"limit", time.Time{}, time.Time{}, Filter{Limit: 2}, "rtn,active"},
		{"and across fields", time.Time{}, time.Time{}, Filter{Sites: []string{"RTU9"}, Kinds: []string{"active"}}, "active"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := r.Query(c.from, c.to, c.f)
			if err != nil {
				t.Fatal(err)
			}
			if k := kindsOf(got); k != c.want {
				t.Fatalf("= %q, want %q", k, c.want)
			}
		})
	}
}

func TestFileJournalRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarms.jsonl")
	j, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range []string{"active", "ack", "rtn"} {
		if err := j.Append(ev(int64(i+1)*1000, "A", k)); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopened: the events are still there, which is the whole point of a
	// file sink on a box with no database.
	j2, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j2.Close()
	got, err := j2.Query(time.Time{}, time.Time{}, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if k := kindsOf(got); k != "rtn,ack,active" {
		t.Fatalf("= %q, want rtn,ack,active", k)
	}
	if got[0].Priority != High || got[0].Site != "RTU9" {
		t.Errorf("fields did not round-trip: %+v", got[0])
	}
}

func TestFileJournalRotatesAndStillQueriesAcross(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarms.jsonl")
	// A cap small enough that a handful of events rotates it.
	j, err := NewFileSize(path, 200)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for i := 0; i < 20; i++ {
		if err := j.Append(ev(int64(i+1)*1000, fmt.Sprintf("A%02d", i), "active")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected a rotated generation: %v", err)
	}
	got, err := j.Query(time.Time{}, time.Time{}, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	// Both generations are read, so the query spans the rotation. Only the
	// most recent two generations survive by design.
	if len(got) < 2 {
		t.Fatalf("query after rotation returned %d events", len(got))
	}
	if got[0].ID != "A19" {
		t.Errorf("newest event = %s, want A19", got[0].ID)
	}
}

func TestFileJournalSkipsATornLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarms.jsonl")
	if err := os.WriteFile(path, []byte(`{"ts":1000,"id":"A","kind":"active"}`+"\n{\"ts\":2000,\"id\""), 0o644); err != nil {
		t.Fatal(err)
	}
	j, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	got, err := j.Query(time.Time{}, time.Time{}, Filter{})
	if err != nil {
		t.Fatalf("a torn last line must not make the journal unreadable: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want the one intact line", len(got))
	}
}

// errJournal fails everything, standing in for a database that has gone
// away.
type errJournal struct{ appends int }

func (e *errJournal) Append(Event) error { e.appends++; return errors.New("down") }
func (e *errJournal) Query(time.Time, time.Time, Filter) ([]Event, error) {
	return nil, errors.New("down")
}

func TestMultiJournalFansOutAndDegrades(t *testing.T) {
	ring := NewRing(10)
	bad := &errJournal{}
	m := NewMulti(ring, bad)

	if err := m.Append(ev(1000, "A", "active")); err == nil {
		t.Error("Append should report the failing sink")
	}
	if ring.Len() != 1 {
		t.Fatal("a failing sink must not cost the ring its copy")
	}
	if bad.appends != 1 {
		t.Fatal("every sink should have been attempted")
	}

	// The durable sink is tried first and fails, so the query degrades to
	// the ring instead of erroring.
	got, err := m.Query(time.Time{}, time.Time{}, Filter{})
	if err != nil {
		t.Fatalf("Query should have degraded to the ring: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events", len(got))
	}
}

func TestMultiJournalWithNoJournalsErrors(t *testing.T) {
	if _, err := NewMulti().Query(time.Time{}, time.Time{}, Filter{}); err == nil {
		t.Fatal("want an error")
	}
}

func TestEventJSONShape(t *testing.T) {
	got := mustJSON(t, Event{TS: 1000, ID: "A.HH", Kind: KindActive, Priority: Critical, Site: "RTU9", State: "unack-active", By: "operator1"})
	want := `{"ts":1000,"id":"A.HH","kind":"active","priority":"critical","site":"RTU9","state":"unack-active","by":"operator1"}`
	if got != want {
		t.Fatalf("Event JSON =\n%s\nwant\n%s", got, want)
	}
	var back Event
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatal(err)
	}
	if back.Priority != Critical || back.Kind != KindActive {
		t.Fatalf("round trip lost fields: %+v", back)
	}
}

func TestStateWireTokens(t *testing.T) {
	for i, want := range stateNames {
		if got := State(i).String(); got != want {
			t.Errorf("State(%d) = %q, want %q", i, got, want)
		}
		s, err := ParseState(strings.ToUpper(want))
		if err != nil || s != State(i) {
			t.Errorf("ParseState(%q) = %v, %v", want, s, err)
		}
	}
	if _, err := ParseState("angry"); err == nil {
		t.Error("ParseState should reject an unknown token")
	}
}

// A notify row's fields survive the file journal, and ExceptKinds drops a
// kind after Kinds has chosen.
func TestNotifyRowRoundTripAndExcept(t *testing.T) {
	path := t.TempDir() + "/alarms.jsonl"
	j, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ts := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).UnixMilli()
	row := Event{TS: ts, ID: "TT1", Kind: KindNotify, By: "system", State: "unack-active",
		Level: 2, To: "oidc:carol", Via: "log", Result: NotifyFailed, Reason: "joined late: x"}
	j.Append(Event{TS: ts - 1, ID: "TT1", Kind: KindActive})
	j.Append(row)
	got, err := j.Query(time.Time{}, time.Time{}, Filter{Kinds: []string{KindNotify}})
	if err != nil || len(got) != 1 || got[0] != row {
		t.Fatalf("notify row = %+v, %v; want %+v", got, err, row)
	}
	got, _ = j.Query(time.Time{}, time.Time{}, Filter{ExceptKinds: []string{KindNotify}})
	if len(got) != 1 || got[0].Kind != KindActive {
		t.Fatalf("ExceptKinds notify = %+v, want the activation only", got)
	}
}

// The offline reader returns both generations oldest first and never
// creates a journal at a path that has none.
func TestReadFileJournal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alarms.jsonl")
	if _, err := ReadFileJournal(path); err == nil {
		t.Fatal("read a journal that does not exist")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("reading created %s", path)
	}
	j, err := NewFileSize(path, 200)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		if err := j.Append(Event{TS: int64(i), ID: "A", Kind: KindActive}); err != nil {
			t.Fatal(err)
		}
	}
	j.Close()
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("no rotation happened, so the test checks nothing: %v", err)
	}
	got, err := ReadFileJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("read %d events", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].TS < got[i-1].TS {
			t.Fatalf("not oldest first: %v", got)
		}
	}
	if got[len(got)-1].TS != 5 {
		t.Errorf("last event ts %d, want 5", got[len(got)-1].TS)
	}
}
