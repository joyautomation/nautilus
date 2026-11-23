package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// seven activations of one alarm, each held `hold` and clear for `gap`, as the
// journal records them (oldest first).
func bounceRows(id string, n int, hold, gap time.Duration) []alarm.Event {
	var out []alarm.Event
	t := time.UnixMilli(1_000_000)
	for i := 0; i < n; i++ {
		out = append(out, alarm.Event{TS: t.UnixMilli(), ID: id, Kind: alarm.KindActive})
		t = t.Add(hold)
		out = append(out, alarm.Event{TS: t.UnixMilli(), ID: id, Kind: alarm.KindRTN})
		t = t.Add(gap)
	}
	return out
}

func chattering(id string, acts int, on, off time.Duration) alarm.Record {
	return alarm.Record{
		Def:     alarm.Def{ID: id, Name: id + " name", Priority: alarm.High, OnDelay: on, OffDelay: off},
		Chatter: &alarm.Chatter{SinceMs: 1, LastMs: 2, Activations: acts, Episodes: 1, WindowMs: 60_000, Count: 3},
	}
}

func TestAdviceSuggestsForAChatteringAlarmAndSaysWhenNothingHelps(t *testing.T) {
	rows := map[string][]alarm.Event{
		"seal":  bounceRows("seal", 7, 3*time.Second, 4*time.Second),
		"slow":  bounceRows("slow", 7, 120*time.Second, 1*time.Second), // holds longer than the whole ladder
		"quiet": bounceRows("quiet", 7, 3*time.Second, 4*time.Second),
	}
	recs := []alarm.Record{
		chattering("slow", 4, 0, 0),
		chattering("seal", 9, 0, 0),
		{Def: alarm.Def{ID: "quiet", Priority: alarm.Low}}, // not chattering: left out
	}
	got := adviceFor(recs, func(id string) []alarm.Event { return rows[id] })
	if len(got.Alarms) != 2 || got.Alarms[0].ID != "seal" || got.Alarms[1].ID != "slow" {
		t.Fatalf("alarms = %+v, want the two chattering ones, most activations first", got.Alarms)
	}
	seal := got.Alarms[0]
	if seal.Suggestion == nil || seal.Suggestion.After >= 3 || seal.Suggestion.Before < 3 ||
		!strings.Contains(seal.Row, "id: seal") || !strings.Contains(seal.Row, "on-delay: 5s") ||
		strings.Contains(seal.Row, "off-delay") {
		t.Errorf("seal = %+v row %q, want a suggestion that cuts the bounces, as an on-delay row", seal, seal.Row)
	}
	if slow := got.Alarms[1]; slow.Suggestion != nil || slow.Row != "" {
		t.Errorf("slow = %+v: nothing on the ladder helps, so there is no suggestion and no row", slow)
	}
	if !strings.Contains(got.Note, "Only longer delays can be judged") {
		t.Errorf("the limits are not stated: %q", got.Note)
	}
}

// A row never repeats the delays the alarm already has, and quotes cannot
// break the YAML.
func TestOverlayRowOnlyLengthensAndKeepsItsWhyOnOneLine(t *testing.T) {
	// 10 s holds with 3 s gaps: an off-delay of 5 s is the cheapest cure, and the
	// row must not also say "on-delay: 0s"
	row := adviceFor([]alarm.Record{chattering("x", 5, 0, 0)}, func(string) []alarm.Event {
		return bounceRows("x", 7, 10*time.Second, 3*time.Second)
	}).Alarms[0].Row
	if !strings.Contains(row, "off-delay: 5s") || strings.Contains(row, "on-delay") || strings.Contains(row, "\n") || strings.Count(row, `"`) != 2 {
		t.Errorf("row = %q: want an off-delay of 5s, no on-delay, one line, one quoted why", row)
	}
}

func TestAdviceRouteAnswersWithNoChatter(t *testing.T) {
	srv, _ := alarmServer(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false})
	rec := do(t, srv, "GET", "/api/alarms/chatter", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var a adviceAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil || a.Alarms == nil || len(a.Alarms) != 0 || a.Note == "" {
		t.Fatalf("answer = %s (%v), want an empty alarms list and the note", rec.Body, err)
	}
}
