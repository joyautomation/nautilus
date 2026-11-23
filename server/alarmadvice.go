package server

// GET /api/alarms/chatter: each chattering alarm with a delay suggestion
// computed from its own journal rows (#1249). Read-only and advisory: it
// changes nothing; an engineer takes the row it prints into the tuning
// overlay (alarms.tuning), which is reviewed and applied at the next start.

import (
	"encoding/json"
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

// adviceRowLimit is the most journal rows read for one alarm, and
// adviceMaxAlarms the most alarms read for: a noisier alarm than that has
// its oldest rows cut (and then no suggestion is made: the last tuning may
// be among them), and one past the cap is not examined.
const (
	adviceRowLimit  = 5000
	adviceMaxAlarms = 20
)

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
	// Cut says the journal read for this alarm was cut (more rows than
	// could be read, or the alarm is past the cap), so no suggestion was
	// made: one from part of the history could be wrong.
	Cut bool `json:"cut,omitempty"`
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
func adviceFor(recs []alarm.Record, rows func(id string) ([]alarm.Event, bool)) adviceAnswer {
	out := adviceAnswer{Alarms: []adviceAlarm{}, Note: adviceLimits}
	var chat []alarm.Record
	for _, r := range recs {
		if r.Chatter != nil {
			chat = append(chat, r)
		}
	}
	// the worst first, and only the first few are read for
	slices.SortFunc(chat, func(x, y alarm.Record) int {
		if x.Chatter.Activations != y.Chatter.Activations {
			return y.Chatter.Activations - x.Chatter.Activations
		}
		return strings.Compare(x.ID, y.ID)
	})
	for i, r := range chat {
		cur := rationalize.Delays{On: r.OnDelay, Off: r.OffDelay}
		a := adviceAlarm{ID: r.ID, Name: r.Def.Name, Priority: r.Priority, Chatter: *r.Chatter,
			OnDelayMs: r.OnDelay.Milliseconds(), OffDelayMs: r.OffDelay.Milliseconds()}
		if a.Name == "" {
			a.Name = r.ID
		}
		var evs []alarm.Event
		cut := i >= adviceMaxAlarms
		if !cut {
			evs, cut = rows(r.ID)
		}
		if cut {
			a.Cut = true
			out.Alarms = append(out.Alarms, a)
			continue
		}
		s, ok := rationalize.Suggest(evs, cur, time.Duration(r.Chatter.WindowMs)*time.Millisecond, r.Chatter.Count)
		if ok {
			a.Suggestion = &adviceSuggested{
				OnDelayMs: time.Duration(s.On).Milliseconds(), OffDelayMs: time.Duration(s.Off).Milliseconds(),
				Before: s.Before, After: s.After, Bounces: s.Bounces, SinceMs: s.Since, Why: s.Why,
			}
			a.Row = overlayRow(r.ID, cur, s)
		}
		out.Alarms = append(out.Alarms, a)
	}
	return out
}

// overlayRow is a suggestion as one tuning overlay row: only the delay the
// suggestion lengthens, and the why in one line. The id and the why are
// written as JSON strings, which are YAML double-quoted scalars: an alarm id
// is free-form text and may hold a comma, a colon or a brace.
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
	why := strings.Join(strings.Fields(s.Why), " ") // one line
	if r := []rune(why); len(r) > 200 {
		why = string(r[:199]) + "…"
	}
	jid, _ := json.Marshal(id)
	jwhy, _ := json.Marshal(why)
	return "- { id: " + string(jid) + ", " + strings.Join(parts, ", ") + ", why: " + string(jwhy) + " }"
}

// readAdviceRows reads one alarm's rows oldest first through query (which
// answers newest first, as the journal does), and says when more matched
// than the limit: then the oldest were cut.
func readAdviceRows(query func(alarm.Filter) ([]alarm.Event, error), id string) ([]alarm.Event, bool) {
	evs, err := query(alarm.Filter{IDs: []string{id}, Kinds: adviceKinds, Limit: adviceRowLimit})
	if err != nil {
		return nil, true
	}
	cut := len(evs) >= adviceRowLimit
	out := slices.Clone(evs)
	slices.Reverse(out)
	return out, cut
}

func (s *Server) handleAlarmChatterAdvice(w http.ResponseWriter, r *http.Request) {
	if !s.alarmsOK(w) {
		return
	}
	now := time.Now()
	recs := s.alarms.Records()
	out := adviceFor(recs, func(id string) ([]alarm.Event, bool) {
		return readAdviceRows(func(f alarm.Filter) ([]alarm.Event, error) {
			return s.alarms.Journal(now.Add(-adviceSpan), now, f)
		}, id)
	})
	writeJSON(w, http.StatusOK, out)
}
