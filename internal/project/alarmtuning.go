package project

// The alarm tuning overlay (#1249): the delays a chattering alarm should run
// with, in a file of their own that is reviewed and committed like any
// other (`alarms: {tuning: alarms.tuning.yaml}`), applied over the composed
// definitions when the controller starts. `naut alarms report chatter
// -suggest` says what to put in it; nothing here is live, so a change takes
// effect at the next start, and a pair applies it in a rolling restart.
//
//	tuning:
//	  - id: P101_SealLeak
//	    on-delay: 5s
//	    why: "7 bounces in 60 s become 1"
//	    from-journal: "2026-11-20T08:00Z/2026-11-22T08:00Z"

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/alarm"
)

// maxTuneDelay is the longest delay an overlay may set: an hour is already
// far past anything a bounce needs, and a typo for 5m as 5h is refused.
const maxTuneDelay = time.Hour

// tuneWhyMax is the longest `why`, in characters.
const tuneWhyMax = 200

// AlarmTuning is one overlay row: an alarm's delays, and why.
type AlarmTuning struct {
	ID string `yaml:"id"`
	// OnDelay and OffDelay replace the alarm's own when present; an absent
	// one is left as composed. At least one is required.
	OnDelay  *alarm.Duration `yaml:"on-delay"`
	OffDelay *alarm.Duration `yaml:"off-delay"`
	// Why is required: a reviewer reads it, and the `tuned` journal row
	// carries it.
	Why string `yaml:"why"`
	// FromJournal says which window of the journal the suggestion was made
	// from, free text (the report prints it).
	FromJournal string `yaml:"from-journal"`
}

type alarmTuningFile struct {
	Tuning []AlarmTuning `yaml:"tuning"`
}

// Delays is the row's delays as time.Durations, -1 for one it leaves alone.
func (t AlarmTuning) delays() (on, off time.Duration) {
	on, off = -1, -1
	if t.OnDelay != nil {
		on = time.Duration(*t.OnDelay)
	}
	if t.OffDelay != nil {
		off = time.Duration(*t.OffDelay)
	}
	return on, off
}

// Text is what the `tuned` journal row says: the delays set and why.
func (t AlarmTuning) Text() string {
	on, off := t.delays()
	var parts []string
	if on >= 0 {
		parts = append(parts, "on-delay "+on.String())
	}
	if off >= 0 {
		parts = append(parts, "off-delay "+off.String())
	}
	return strings.Join(parts, ", ") + ": " + t.Why
}

// composeTuning reads the overlay the manifest names, strictly (a mistyped
// key is an error, not silence), and checks each row on its own. Whether the
// ids exist needs the composed definitions, so `naut check` does that.
func composeTuning(fsys fs.FS, m *Manifest) error {
	if m.Alarms == nil || m.Alarms.Tuning == "" {
		return nil
	}
	p, err := projectPath(m.Alarms.Tuning)
	if err != nil {
		return fmt.Errorf("alarms.tuning: %w", err)
	}
	raw, err := fs.ReadFile(fsys, p)
	if err != nil {
		return fmt.Errorf("alarms.tuning: %w", err)
	}
	var f alarmTuningFile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", p, err)
	}
	seen := map[string]bool{}
	for i, t := range f.Tuning {
		at := fmt.Sprintf("%s: row %d", p, i+1)
		if t.ID == "" {
			return fmt.Errorf("%s: id is required", at)
		}
		at = fmt.Sprintf("%s: %s", p, t.ID)
		if seen[t.ID] {
			return fmt.Errorf("%s: listed twice; one row per alarm", at)
		}
		seen[t.ID] = true
		if t.OnDelay == nil && t.OffDelay == nil {
			return fmt.Errorf("%s: needs on-delay or off-delay", at)
		}
		on, off := t.delays()
		for _, d := range []struct {
			name string
			v    time.Duration
		}{{"on-delay", on}, {"off-delay", off}} {
			if d.v == -1 {
				continue
			}
			if d.v < 0 || d.v > maxTuneDelay {
				return fmt.Errorf("%s: %s %s is outside 0 to %s", at, d.name, d.v, maxTuneDelay)
			}
		}
		why := strings.TrimSpace(t.Why)
		switch {
		case why == "":
			return fmt.Errorf("%s: why is required (the journal's tuned row carries it, and a reviewer reads it)", at)
		case strings.ContainsAny(why, "\r\n"):
			return fmt.Errorf("%s: why is one line", at)
		case utf8.RuneCountInString(why) > tuneWhyMax:
			return fmt.Errorf("%s: why is at most %d characters", at, tuneWhyMax)
		}
		f.Tuning[i].Why = why
	}
	m.Alarms.TuningRows = f.Tuning
	return nil
}

// tuneDefs applies the overlay to composed definitions: the delays it sets
// replace the alarm's, the rest stay. It returns the definitions (a copy;
// the input is not touched), the rows it applied, and the ids no definition
// has: `naut check` refuses those, and a start logs and skips them rather
// than stop a plant over a renamed alarm.
func (c *AlarmsConfig) tuneDefs(defs []alarm.Def) (out []alarm.Def, applied []AlarmTuning, missing []string) {
	out = append([]alarm.Def(nil), defs...)
	if c == nil {
		return out, nil, nil
	}
	byID := make(map[string]int, len(out))
	for i, d := range out {
		byID[d.ID] = i
	}
	for _, t := range c.TuningRows {
		i, ok := byID[t.ID]
		if !ok {
			missing = append(missing, t.ID)
			continue
		}
		on, off := t.delays()
		if on >= 0 {
			out[i].OnDelay = on
		}
		if off >= 0 {
			out[i].OffDelay = off
		}
		applied = append(applied, t)
	}
	return out, applied, missing
}
