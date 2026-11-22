package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

const liftStation = "../../examples/lift-station"

// Export then check is clean; an edited master is reported with exit 1,
// and a master that cannot be read is exit 2, not "differs".
func TestAlarmsMasterExportThenCheck(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"master.csv", "master.yaml"} {
		file := filepath.Join(dir, name)
		var out, errb bytes.Buffer
		if code := alarmsMaster([]string{"export", liftStation, "-f", file}, &out, &errb); code != 0 {
			t.Fatalf("export %s: exit %d: %s", name, code, errb.String())
		}
		if !strings.Contains(errb.String(), "wrote 14 alarm(s)") {
			t.Errorf("export said %q", errb.String())
		}
		out.Reset()
		if code := alarmsMaster([]string{"check", liftStation, "-f", file}, &out, &errb); code != 0 {
			t.Fatalf("check %s right after export: exit %d:\n%s", name, code, out.String())
		}
	}

	file := filepath.Join(dir, "master.csv")
	raw, _ := os.ReadFile(file)
	edited := strings.Replace(string(raw), "LSHH101,Wet-well high-high (LSHH-101),LSHH101,critical",
		"LSHH101,Wet-well high-high (LSHH-101),LSHH101,high", 1)
	if edited == string(raw) {
		t.Fatal("the fixture row moved; the edit changed nothing")
	}
	os.WriteFile(file, []byte(edited), 0o644)
	var out, errb bytes.Buffer
	if code := alarmsMaster([]string{"check", liftStation, "-f", file}, &out, &errb); code != 1 {
		t.Fatalf("edited master: exit %d, want 1:\n%s%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), `LSHH101: priority: master "high", manifest "critical"`) {
		t.Errorf("the difference is not named:\n%s", out.String())
	}

	os.WriteFile(file, []byte("id,priorty\nA,low\n"), 0o644)
	if code := alarmsMaster([]string{"check", liftStation, "-f", file}, &out, &errb); code != 2 {
		t.Errorf("unreadable master: exit %d, want 2", code)
	}
	if code := alarmsMaster([]string{"check", liftStation}, &out, &errb); code != 2 {
		t.Errorf("check with no -f: exit %d, want 2", code)
	}
}

var reportNow = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

// journalFixture is two hours ago: a flood of twelve alarms in one ten-minute
// window, a standing alarm whose restart re-journals `active`, and one
// chattering alarm.
func journalFixture() []alarm.Event {
	base := reportNow.Add(-2 * time.Hour).Truncate(10 * time.Minute)
	ts := func(d time.Duration) int64 { return base.Add(d).UnixMilli() }
	var evs []alarm.Event
	for i := range 12 {
		evs = append(evs, alarm.Event{TS: ts(time.Duration(i) * time.Second), ID: "F" + strconv.Itoa(i), Kind: alarm.KindActive, Priority: alarm.High})
	}
	evs = append(evs,
		alarm.Event{TS: ts(-48 * time.Hour), ID: "STUCK", Kind: alarm.KindActive, Priority: alarm.Low},
		alarm.Event{TS: ts(time.Hour), ID: "STUCK", Kind: alarm.KindActive, Priority: alarm.Low}, // restart
		alarm.Event{TS: ts(time.Hour), ID: "CH", Kind: alarm.KindActive},
		alarm.Event{TS: ts(time.Hour + time.Second), ID: "CH", Kind: alarm.KindRTN},
		alarm.Event{TS: ts(time.Hour + 2*time.Second), ID: "CH", Kind: alarm.KindActive},
		alarm.Event{TS: ts(time.Hour + 3*time.Second), ID: "CH", Kind: alarm.KindRTN},
		alarm.Event{TS: ts(time.Hour + 4*time.Second), ID: "CH", Kind: alarm.KindActive},
		alarm.Event{TS: ts(time.Hour + 5*time.Second), ID: "CH", Kind: alarm.KindAck},
	)
	slices.SortStableFunc(evs, func(a, b alarm.Event) int { return int(a.TS - b.TS) })
	return evs
}

func writeJournal(t *testing.T, evs []alarm.Event) string {
	path := filepath.Join(t.TempDir(), "alarms.jsonl")
	j, err := alarm.NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		j.Append(e)
	}
	j.Close()
	return path
}

func reportJSON(t *testing.T, args ...string) (map[string]any, string) {
	t.Helper()
	var out, errb bytes.Buffer
	if code := alarmsReport(append(args, "-o", "json"), reportNow, &out, &errb); code != 0 {
		t.Fatalf("%v: exit %d: %s", args, code, errb.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return doc, out.String()
}

func TestAlarmsReportFromAJournalFile(t *testing.T) {
	// An event after -to (the clock's now) is outside every range.
	path := writeJournal(t, append(journalFixture(),
		alarm.Event{TS: reportNow.Add(time.Minute).UnixMilli(), ID: "LATER", Kind: alarm.KindActive}))

	doc, raw := reportJSON(t, "flood", "-journal", path)
	rep := doc["report"].(map[string]any)
	// 12 flood alarms, STUCK once (its restart is not new), CH three times.
	if rep["activations"].(float64) != 16 || len(rep["floods"].([]any)) != 1 {
		t.Errorf("flood report:\n%s", raw)
	}
	if read := doc["read"].(map[string]any); read["events"].(float64) != 19 {
		t.Errorf("read %v events in the last 7 days, want 19: every active, rtn and suppress, not the ack", read["events"])
	}

	// The range is applied: a day back leaves out STUCK's first activation.
	doc, _ = reportJSON(t, "flood", "-journal", path, "-since", "1d")
	if read := doc["read"].(map[string]any); read["events"].(float64) != 18 {
		t.Errorf("read %v events in the last day, want 18", read["events"])
	}

	// STUCK began two days ago and a restart re-journalled it inside the
	// last day: read with a lead-in, that is the same occurrence, so a
	// one-day report counts only the flood and CH, and STUCK's age runs
	// from its first activation.
	doc, raw = reportJSON(t, "flood", "-journal", path, "-since", "1d")
	if rep := doc["report"].(map[string]any); rep["activations"].(float64) != 15 {
		t.Errorf("one-day flood counted a restart as a new alarm:\n%s", raw)
	}
	doc, raw = reportJSON(t, "bad-actors", "-journal", path, "-since", "1d", "-top", "50")
	if rep := doc["report"].(map[string]any); rep["activations"].(float64) != 15 {
		t.Errorf("one-day bad actors counted an activation from the lead-in:\n%s", raw)
	}
	doc, raw = reportJSON(t, "stale", "-journal", path, "-since", "1d")
	if ss := doc["report"].([]any); len(ss) != 1 || ss[0].(map[string]any)["forNs"].(float64) < float64(48*time.Hour) {
		t.Errorf("one-day stale report lost STUCK's first activation:\n%s", raw)
	}

	doc, raw = reportJSON(t, "chatter", "-journal", path)
	if cs := doc["report"].([]any); len(cs) != 1 || cs[0].(map[string]any)["id"] != "CH" {
		t.Errorf("chatter report:\n%s", raw)
	}

	doc, raw = reportJSON(t, "stale", "-journal", path)
	ss := doc["report"].([]any)
	if len(ss) != 1 || ss[0].(map[string]any)["id"] != "STUCK" {
		t.Fatalf("stale report:\n%s", raw)
	}

	var out, errb bytes.Buffer
	if code := alarmsReport([]string{"bad-actors", "-journal", path, "-top", "2"}, reportNow, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	if !strings.Contains(out.String(), "   1  CH ") || !strings.Contains(out.String(), "these 2 of the alarms") {
		t.Errorf("bad actors table:\n%s", out.String())
	}

	if code := alarmsReport([]string{"flood", "-journal", path + ".typo"}, reportNow, &out, &errb); code != 2 {
		t.Errorf("a missing journal: exit %d, want 2", code)
	}
	if _, err := os.Stat(path + ".typo"); !os.IsNotExist(err) {
		t.Error("the report created the journal it was pointed at")
	}
	if code := alarmsReport([]string{"bad-actors", "-journal", path, "-top", "0"}, reportNow, &out, &errb); code != 2 {
		t.Errorf("-top 0: exit %d, want 2", code)
	}
	if code := alarmsReport([]string{"flood", "-journal", path, "-from", "yesterday"}, reportNow, &out, &errb); code != 2 {
		t.Errorf("a bad -from: exit %d, want 2", code)
	}
	if code := alarmsReport([]string{"flood", "-h"}, reportNow, &out, &errb); code != 0 {
		t.Errorf("-h: exit %d, want 0", code)
	}
	if code := alarmsReport([]string{"flood"}, reportNow, &out, &errb); code != 2 {
		t.Errorf("no journal named: exit %d, want 2", code)
	}
}

// fakeJournalAPI serves GET /api/alarms/journal the way the server does:
// from and to inclusive, kind filtered, newest first, at most limit rows,
// truncated when more match.
func fakeJournalAPI(evs []alarm.Event, requests *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests++
		q := r.URL.Query()
		from, _ := strconv.ParseInt(q.Get("from"), 10, 64)
		to, _ := strconv.ParseInt(q.Get("to"), 10, 64)
		limit, _ := strconv.Atoi(q.Get("limit"))
		kinds := strings.Split(q.Get("kind"), ",")
		var match []alarm.Event
		for i := len(evs) - 1; i >= 0; i-- {
			e := evs[i]
			if e.TS >= from && e.TS <= to && slices.Contains(kinds, e.Kind) {
				match = append(match, e)
			}
		}
		truncated := len(match) > limit
		if truncated {
			match = match[:limit]
		}
		json.NewEncoder(w).Encode(map[string]any{"events": match, "truncated": truncated})
	}))
}

// A range holding more than a page is halved until each half fits, and an
// event on a half's boundary is read once, not twice.
func TestAlarmsReportFromAControllerSplitsATruncatedRange(t *testing.T) {
	evs := journalFixture()
	from := time.UnixMilli(evs[0].TS)
	to := time.UnixMilli(evs[len(evs)-1].TS + 1)
	// Put an event exactly on the first split point.
	mid := from.UnixMilli() + (to.UnixMilli()-from.UnixMilli())/2
	evs = append(evs, alarm.Event{TS: mid, ID: "MID", Kind: alarm.KindActive})
	slices.SortStableFunc(evs, func(a, b alarm.Event) int { return int(a.TS - b.TS) })

	var requests int
	srv := fakeJournalAPI(evs, &requests)
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")

	old, oldLead := journalPage, leadIn
	journalPage, leadIn = 4, 0 // no lead-in, so the first split falls on MID
	defer func() { journalPage, leadIn = old, oldLead }()

	doc, raw := reportJSON(t, "bad-actors", "-host", host, "-port", port,
		"-from", from.Format(time.RFC3339Nano), "-to", to.Format(time.RFC3339Nano), "-top", "50")
	if requests < 3 {
		t.Errorf("%d requests: the range was never split", requests)
	}
	read := doc["read"].(map[string]any)
	// Every active/rtn row once: the ack is never asked for.
	want := 0
	for _, e := range evs {
		if e.Kind != alarm.KindAck {
			want++
		}
	}
	if int(read["events"].(float64)) != want {
		t.Fatalf("read %v events, want %d (each once):\n%s", read["events"], want, raw)
	}
	// Oldest first: each page comes back newest first and is turned round.
	earliest, _ := time.Parse(time.RFC3339Nano, read["earliest"].(string))
	if !earliest.Equal(from) {
		t.Errorf("earliest %v, want %v (the first event)", earliest, from)
	}
	rep := doc["report"].(map[string]any)
	if rep["activations"].(float64) != 17 { // 12 + STUCK + CH×3 + MID
		t.Errorf("activations %v, want 17:\n%s", rep["activations"], raw)
	}
}

// A journal minutes old, read over the default week, is not a quiet week:
// the header says where the journal starts, that the rates are over the
// whole range, and the -from that reads the journal's span alone. A
// journal that covers the range, or a gap under an hour, says nothing.
func TestAlarmsReportSaysWhenTheJournalIsYoungerThanTheRange(t *testing.T) {
	table := func(path string, extra ...string) string {
		t.Helper()
		var out, errb bytes.Buffer
		if code := alarmsReport(append([]string{"flood", "-journal", path}, extra...), reportNow, &out, &errb); code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
		return out.String()
	}
	first := reportNow.Add(-5*time.Minute + 17*time.Second) // -from floors to the minute
	young := writeJournal(t, []alarm.Event{
		{TS: first.UnixMilli(), ID: "A", Kind: alarm.KindActive},
		{TS: reportNow.Add(-time.Minute).UnixMilli(), ID: "A", Kind: alarm.KindRTN},
	})
	out := table(young)
	at := first.Local().Truncate(time.Minute).Format(reportTime)
	for _, want := range []string{
		"the first event is at " + at + ", 6d 23h into the range",
		"Rates and window counts below are over the whole range",
		`add -from "` + at + `"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("young journal over a week: want %q in\n%s", want, out)
		}
	}
	// A day's range on the same journal: 23h55m in, which is no quiet day.
	if out := table(young, "-since", "1d"); !strings.Contains(out, "the first event is at "+at+", 23h55m into the range") {
		t.Errorf("young journal over a day: not noted\n%s", out)
	}
	if out := table(young, "-since", "1h"); strings.Contains(out, "first event is at") {
		t.Errorf("a 55-minute gap in an hour's range is noted:\n%s", out)
	}
	covers := writeJournal(t, []alarm.Event{
		{TS: reportNow.Add(-7*24*time.Hour + 2*time.Hour).UnixMilli(), ID: "A", Kind: alarm.KindActive}, // 2h of 7d
		{TS: reportNow.Add(-time.Minute).UnixMilli(), ID: "A", Kind: alarm.KindRTN},
	})
	if out := table(covers); strings.Contains(out, "first event is at") {
		t.Errorf("a journal that covers the range is noted:\n%s", out)
	}
}
