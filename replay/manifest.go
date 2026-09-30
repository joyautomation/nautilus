// Package replay is a driver that plays a recording back as live tags: a
// history of TAG.Member series (history.json.gz, sampled every step
// seconds) delivered as the IT-hardware struct tags they were recorded
// from, at a replay clock the project can steer — speed, pause, seek — by
// writing tags. It is the baseline of a plant simulation: `Rec_SW1_Port25`
// moves as SW1 port 25 really moved, and the plant's logic bends it
// (docs/design/it-drivers.md §8; randd handoff NODE-3D-PLANT-SIM).
//
// The recording is read when the driver starts, not when it is built, so
// `naut check` and `naut build` pass where the recording is absent (CI, a
// public checkout of an example whose data is private): until it loads,
// the driver reports the error and its tags read NotConnected.
package replay

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/joyautomation/nautilus/hw"
	"gopkg.in/yaml.v3"
)

// The clock tags every replay driver owns. Speed, Pause and SeekS are
// OUTPUTS the project (an HMI, `naut`, a test) writes; At, From and To are
// INPUTS the driver keeps current, so a scrubber can draw itself.
const (
	TagSpeed = "Replay_Speed" // REAL: recorded seconds per wall second (1 = real time)
	TagPause = "Replay_Pause" // BOOL: hold the clock
	TagSeek  = "Replay_SeekS" // DINT: unix seconds; a change jumps the clock there
	TagAt    = "Replay_At"    // DINT: unix seconds, the recorded moment being delivered
	TagFrom  = "Replay_From"  // DINT: unix seconds, where the loop starts
	TagTo    = "Replay_To"    // DINT: unix seconds, where it wraps
)

// Manifest is one replay driver's configuration.
type Manifest struct {
	// History is the recording, relative to the project (it may be a
	// symlink out of it, for data that is not committed).
	History string
	// From and To bound the loop; zero = from the first moment every
	// replayed series has a real sample, to the recording's end.
	From, To time.Time
	// Start is where the clock begins; zero = From.
	Start time.Time
	Tags  []Tag
}

// Tag is one struct tag delivered from the recording: its contract type,
// the series prefix its members were recorded under ("SW1_Port25" for
// SW1_Port25.InBps), and constants for the members the recording does not
// carry (a port's Name and Index, a sensor's thresholds).
type Tag struct {
	Name   string
	Type   string
	Series string
	Const  map[string]any
}

type manifestYAML struct {
	History string    `yaml:"history"`
	From    string    `yaml:"from"`
	To      string    `yaml:"to"`
	Start   string    `yaml:"start"`
	Tags    []tagYAML `yaml:"tags"`
}

type tagYAML struct {
	Name   string         `yaml:"name"`
	Type   string         `yaml:"type"`
	Series string         `yaml:"series"`
	Const  map[string]any `yaml:"const"`
}

// ParseManifest decodes and validates a replay_manifest.yaml. A typo
// anywhere is an error with a line number (KnownFields).
func ParseManifest(data []byte) (Manifest, error) {
	var y manifestYAML
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&y); err != nil {
		if errors.Is(err, io.EOF) {
			return Manifest{}, fmt.Errorf("replay: manifest is empty")
		}
		return Manifest{}, fmt.Errorf("replay: manifest: %w", err)
	}
	m := Manifest{History: y.History}
	var errs []error
	for _, f := range []struct {
		key string
		src string
		dst *time.Time
	}{{"from", y.From, &m.From}, {"to", y.To, &m.To}, {"start", y.Start, &m.Start}} {
		if f.src == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, f.src)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %q is not RFC 3339 (2026-09-28T18:00:00Z)", f.key, f.src))
		}
		*f.dst = t
	}
	for _, t := range y.Tags {
		m.Tags = append(m.Tags, Tag{Name: t.Name, Type: t.Type, Series: t.Series, Const: t.Const})
	}
	if err := errors.Join(append(errs, m.Validate())...); err != nil {
		return Manifest{}, fmt.Errorf("replay: %w", err)
	}
	return m, nil
}

// Validate checks everything that needs no recording: types, members,
// constants, the loop bounds, duplicate and clashing names.
func (m Manifest) Validate() error {
	var errs []error
	if m.History == "" {
		errs = append(errs, errors.New("history: is required (the history.json.gz to replay)"))
	}
	if !m.From.IsZero() && !m.To.IsZero() && !m.To.After(m.From) {
		errs = append(errs, errors.New("to: must be after from:"))
	}
	names := map[string]bool{TagSpeed: true, TagPause: true, TagSeek: true, TagAt: true, TagFrom: true, TagTo: true}
	for i, t := range m.Tags {
		switch {
		case t.Name == "":
			errs = append(errs, fmt.Errorf("tags[%d]: missing name", i))
			continue
		case names[t.Name]:
			errs = append(errs, fmt.Errorf("tag %s: duplicate (or a clock tag's name)", t.Name))
			continue
		}
		names[t.Name] = true
		if _, ok := hw.TypeByName(t.Type); !ok {
			errs = append(errs, fmt.Errorf("tag %s: unknown type %q (the IT-hardware set is %s)", t.Name, t.Type, hw.TypeNames()))
			continue
		}
		if t.Series == "" {
			errs = append(errs, fmt.Errorf("tag %s: series: is required (the recording's tag name)", t.Name))
		}
		keys := make([]string, 0, len(t.Const))
		for k := range t.Const {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			_, f, ok := hw.FieldOf(t.Type, k)
			if !ok {
				errs = append(errs, fmt.Errorf("tag %s: type %s has no member %q", t.Name, t.Type, k))
				continue
			}
			if _, err := hw.Coerce(t.Const[k], f); err != nil {
				errs = append(errs, fmt.Errorf("tag %s: const %s: %w", t.Name, k, err))
			}
		}
	}
	return errors.Join(errs...)
}
