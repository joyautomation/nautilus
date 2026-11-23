package server

// GET /api/alarms/chatter: each chattering alarm with a delay suggestion
// computed from its own journal rows (#1249). Read-only and advisory: it
// changes nothing; an engineer takes the row it prints into the tuning
// overlay (alarms.tuning), which is reviewed and applied at the next start.

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/rationalize"
)

// adviceSpan is how far back the suggestion reads each alarm's journal.
const adviceSpan = 7 * 24 * time.Hour

// adviceKinds are the rows a suggestion reads: what starts and ends an
// occurrence, the spans it leaves out, and where the last tuning was.
var adviceKinds = []string{alarm.KindActive, alarm.KindRTN, alarm.KindShelve, alarm.KindUnshelve,
	alarm.KindSuppress, alarm.KindUnsuppress, alarm.KindTuned}

// adviceLimits is the honest limits of the replay, shown beside every
// suggestion.
const adviceLimits = "Only longer delays can be judged: bounces the current delays already filter are not in the journal, " +
	"and a drop-out shorter than the off-delay is hidden. Time while the alarm was shelved or suppressed is left out, and the " +
	"history starts after the last tuning. Alarms are on/off conditions, so a deadband belongs where the analog value becomes the " +
	"condition (the program, or the rule's condition), not here."

// adviceAlarm is one chattering alarm and what would have helped it.
type adviceAlarm struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Priority   alarm.Priority   `json:"priority"`
	Chatter    alarm.Chatter    `json:"chatter"`
	OnDelayMs  int64            `json:"onDelayMs"`
	OffDelayMs int64            `json:"offDelayMs"`
	Suggestion *adviceSuggested `json:"suggestion,omitempty"`
	// Row is the suggestion as a tuning overlay row, ready to paste into
	// the file `alarms.tuning` names.
	Row string `json:"row,omitempty"`
}

type adviceSuggested struct {
	OnDelayMs  int64  `json:"onDelayMs"`
	OffDelayMs int64  `json:"offDelayMs"`
	Before     int    `json:"before"`
	After      int    `json:"after"`
	Bounces    int    `json:"bounces"`
	SinceMs    int64  `json:"sinceMs"`
	Why        string `json:"why"`
}

type adviceAnswer struct {
	Alarms []adviceAlarm `json:"alarms"`
	Note   string        `json:"note"`
}

// adviceFor builds the answer from the chattering records and a way to read
// one alarm's journal rows, oldest first. Pure, so it has a test.
func adviceFor(recs []alarm.Record, rows func(id string) []alarm.Event) adviceAnswer {
	out := adviceAnswer{Alarms: []adviceAlarm{}, Note: adviceLimits}
	for _, r := range recs {
		if r.Chatter == nil {
			continue
		}
		cur := rationalize.Delays{On: r.OnDelay, Off: r.OffDelay}
		a := adviceAlarm{ID: r.ID, Name: r.Def.Name, Priority: r.Priority, Chatter: *r.Chatter,
			OnDelayMs: r.OnDelay.Milliseconds(), OffDelayMs: r.OffDelay.Milliseconds()}
		if a.Name == "" {
			a.Name = r.ID
		}
		s, ok := rationalize.Suggest(rows(r.ID), cur, time.Duration(r.Chatter.WindowMs)*time.Millisecond, r.Chatter.Count)
		if ok {
			a.Suggestion = &adviceSuggested{
				OnDelayMs: time.Duration(s.On).Milliseconds(), OffDelayMs: time.Duration(s.Off).Milliseconds(),
				Before: s.Before, After: s.After, Bounces: s.Bounces, SinceMs: s.Since, Why: s.Why,
			}
			a.Row = overlayRow(r.ID, cur, s)
		}
		out.Alarms = append(out.Alarms, a)
	}
	slices.SortFunc(out.Alarms, func(x, y adviceAlarm) int {
		if x.Chatter.Activations != y.Chatter.Activations {
			return y.Chatter.Activations - x.Chatter.Activations // the worst first
		}
		return strings.Compare(x.ID, y.ID)
	})
	return out
}

// overlayRow is a suggestion as one tuning overlay row: only the delay the
// suggestion lengthens, and the why in one line.
func overlayRow(id string, cur rationalize.Delays, s rationalize.Suggestion) string {
	var parts []string
	if on := time.Duration(s.On); on > cur.On {
		parts = append(parts, "on-delay: "+on.String())
	}
	if off := time.Duration(s.Off); off > cur.Off {
		parts = append(parts, "off-delay: "+off.String())
	}
	if len(parts) == 0 {
		return ""
	}
	why := strings.ReplaceAll(strings.TrimSpace(s.Why), `"`, `'`)
	return fmt.Sprintf(`- { id: %s, %s, why: "%s" }`, id, strings.Join(parts, ", "), why)
}

func (s *Server) handleAlarmChatterAdvice(w http.ResponseWriter, r *http.Request) {
	if !s.alarmsOK(w) {
		return
	}
	now := time.Now()
	recs := s.alarms.Records()
	out := adviceFor(recs, func(id string) []alarm.Event {
		evs, err := s.alarms.Journal(now.Add(-adviceSpan), now, alarm.Filter{IDs: []string{id}, Kinds: adviceKinds, Limit: 5000})
		if err != nil {
			return nil
		}
		slices.Reverse(evs) // the journal answers newest first
		return evs
	})
	writeJSON(w, http.StatusOK, out)
}
