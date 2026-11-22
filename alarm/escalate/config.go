// Package escalate pages people about alarms nobody has acknowledged
// (#715): escalation policies, on-call rosters, and the dispatcher that
// sends each level when it falls due.
//
// The pieces, in dependency order:
//
//   - Oncall holds the rosters: who is on call at an instant, by a fixed
//     list, a daily or weekly rotation, or shifts by time of day. OnCall
//     is a pure function of the roster and the instant.
//   - Policy says which alarms it covers (priority, id, site, area, class,
//     and an optional time-of-day window), its levels (who, by which
//     channel, how long after the alarm began waiting), how the last
//     level repeats, and what stops it. Steps and Escalating are pure.
//   - Dispatcher reads the engine's records on the scan, works out what is
//     due and hands each send to one sender goroutine, which checks the
//     write gate immediately before every send.
//
// An escalation runs from when the alarm began waiting on a person, not
// from its last activation: a latched alarm that flaps between
// unack-active and unack-rtn keeps climbing. An acknowledgement, a shelf,
// a suppression or (with stop: ack-or-rtn) a return to normal ends it. An
// alarm with ack-required: false never waits on a person: it escalates
// from its activation while it is active, and a return to normal, a
// shelf or a suppression ends it.
//
// Addresses (an email, a phone number) never reach this package's
// journal rows: a recipient is a principal (a user name or oidc:<name>),
// and a Channel resolves it.
package escalate

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// Oncall is `alarms.oncall:`.
type Oncall struct {
	// Timezone is the IANA zone rosters and `when:` windows are read in,
	// e.g. America/Chicago. Default UTC.
	Timezone string   `yaml:"timezone" json:"timezone,omitempty"`
	Rosters  []Roster `yaml:"rosters" json:"rosters,omitempty"`

	loc *time.Location
}

// Roster is one on-call roster. Exactly one form:
//
//   - members alone: everyone listed is on call all the time;
//   - members + rotate (daily or weekly) + start: one member at a time,
//     in order, the first from start's handover;
//   - shifts: whoever's shift covers the instant.
type Roster struct {
	ID      string   `yaml:"id" json:"id"`
	Members []string `yaml:"members" json:"members,omitempty"`
	// Rotate is "daily" or "weekly"; "" means no rotation.
	Rotate string `yaml:"rotate" json:"rotate,omitempty"`
	// Start is the date (YYYY-MM-DD) the first member's first turn begins.
	// A weekly rotation hands over on Start's weekday.
	Start string `yaml:"start" json:"start,omitempty"`
	// Handover is the time of day (HH:MM) a turn changes. Default 00:00.
	Handover string  `yaml:"handover" json:"handover,omitempty"`
	Shifts   []Shift `yaml:"shifts" json:"shifts,omitempty"`

	start    civil
	handover time.Duration
	shifts   []window
}

// Shift is one shift of a roster: its window and who works it.
type Shift struct {
	Window  `yaml:",inline"`
	Members []string `yaml:"members" json:"members"`
}

// Window is a time-of-day window on some days of the week: `days:`
// (mon, tue, … sun, or a range like mon-fri; empty is every day), `from:`
// and `to:` (HH:MM). A window whose to is not after its from runs past
// midnight, and belongs to the day it starts on. from == to is 24 hours
// from from.
type Window struct {
	Days []string `yaml:"days" json:"days,omitempty"`
	From string   `yaml:"from" json:"from,omitempty"`
	To   string   `yaml:"to" json:"to,omitempty"`
}

// Policy is one entry of `alarms.escalation:`.
type Policy struct {
	ID    string  `yaml:"id" json:"id"`
	Match Match   `yaml:"match" json:"match"`
	When  *Window `yaml:"when" json:"when,omitempty"`
	// Levels, in order of their `after:`. No fixed cap.
	Levels []Level `yaml:"levels" json:"levels"`
	Repeat Repeat  `yaml:"repeat" json:"repeat,omitzero"`
	// Stop is "ack" (default: only an acknowledgement, a shelf or a
	// suppression stops it) or "ack-or-rtn" (a return to normal does too).
	Stop string `yaml:"stop" json:"stop,omitempty"`

	when  *window
	steps []Step
}

// Match picks the alarms a policy covers. Every field is optional and
// they AND together; Priority is a list (any of), the rest are globs on
// the alarm's own fields, case-insensitive.
type Match struct {
	Priority []string `yaml:"priority" json:"priority,omitempty"`
	ID       string   `yaml:"id" json:"id,omitempty"`
	Site     string   `yaml:"site" json:"site,omitempty"`
	Area     string   `yaml:"area" json:"area,omitempty"`
	Class    string   `yaml:"class" json:"class,omitempty"`

	prio []alarm.Priority
}

// Level is one escalation level.
type Level struct {
	// After is how long after the alarm began waiting this level is sent.
	After alarm.Duration `yaml:"after" json:"after"`
	// Notify lists who: roster:<id> (whoever is on call when it is sent)
	// or a principal (alice, oidc:carol).
	Notify []string `yaml:"notify" json:"notify"`
	// Via lists the channel kinds; each recipient gets one send per kind.
	Via []string `yaml:"via" json:"via"`
}

// Repeat repeats the last level Times more times, Every apart.
type Repeat struct {
	Every alarm.Duration `yaml:"every" json:"every,omitempty"`
	Times int            `yaml:"times" json:"times,omitempty"`
}

// Stop rules.
const (
	StopAck      = "ack"
	StopAckOrRTN = "ack-or-rtn"
)

// RosterPrefix marks a level's notify entry as a roster.
const RosterPrefix = "roster:"

// Config is the whole of escalation as the manifest gives it.
type Config struct {
	Oncall   Oncall
	Policies []Policy
}

// Prepare validates c and readies it for use, in place. channels is the
// set of channel kinds this controller has; a level naming another is an
// error. Errors name the manifest key (alarms.escalation[1].levels[0].via).
func (c *Config) Prepare(channels []string) error {
	o := &c.Oncall
	o.loc = time.UTC
	if o.Timezone != "" {
		loc, err := time.LoadLocation(o.Timezone)
		if err != nil {
			return fmt.Errorf("alarms.oncall.timezone: %q is not an IANA time zone (want e.g. America/Chicago)", o.Timezone)
		}
		o.loc = loc
	}
	ids := map[string]bool{}
	for i := range o.Rosters {
		r := &o.Rosters[i]
		at := fmt.Sprintf("alarms.oncall.rosters[%d]", i)
		if err := r.prepare(at); err != nil {
			return err
		}
		if ids[r.ID] {
			return fmt.Errorf("%s: roster id %q is used twice", at, r.ID)
		}
		ids[r.ID] = true
	}
	pids := map[string]bool{}
	for i := range c.Policies {
		p := &c.Policies[i]
		at := fmt.Sprintf("alarms.escalation[%d]", i)
		if p.ID == "" {
			return fmt.Errorf("%s: id is required", at)
		}
		if pids[p.ID] {
			return fmt.Errorf("%s: policy id %q is used twice", at, p.ID)
		}
		pids[p.ID] = true
		if err := p.prepare(at, ids, channels); err != nil {
			return err
		}
	}
	return nil
}

// Location is the zone rosters and windows are read in (UTC until
// Prepare has run).
func (o *Oncall) Location() *time.Location {
	if o.loc == nil {
		return time.UTC
	}
	return o.loc
}

// Roster returns the roster with id, or nil.
func (o *Oncall) Roster(id string) *Roster {
	for i := range o.Rosters {
		if o.Rosters[i].ID == id {
			return &o.Rosters[i]
		}
	}
	return nil
}

func (r *Roster) prepare(at string) error {
	if r.ID == "" {
		return fmt.Errorf("%s: id is required", at)
	}
	at = fmt.Sprintf("%s (%s)", at, r.ID)
	for j, m := range r.Members {
		if err := checkPrincipal(m); err != nil {
			return fmt.Errorf("%s.members[%d]: %w", at, j, err)
		}
	}
	switch {
	case len(r.Shifts) > 0:
		if len(r.Members) > 0 || r.Rotate != "" || r.Start != "" || r.Handover != "" {
			return fmt.Errorf("%s: a roster is either shifts or members (with an optional rotate), not both", at)
		}
		r.shifts = make([]window, len(r.Shifts))
		for j, s := range r.Shifts {
			w, err := s.Window.parse(fmt.Sprintf("%s.shifts[%d]", at, j))
			if err != nil {
				return err
			}
			if len(s.Members) == 0 {
				return fmt.Errorf("%s.shifts[%d].members: a shift needs at least one member", at, j)
			}
			for k, m := range s.Members {
				if err := checkPrincipal(m); err != nil {
					return fmt.Errorf("%s.shifts[%d].members[%d]: %w", at, j, k, err)
				}
			}
			r.shifts[j] = w
		}
	case len(r.Members) == 0:
		return fmt.Errorf("%s: a roster needs members or shifts", at)
	case r.Rotate == "":
		if r.Start != "" || r.Handover != "" {
			return fmt.Errorf("%s: start and handover only mean something with rotate: daily or weekly", at)
		}
	case r.Rotate == "daily" || r.Rotate == "weekly":
		if r.Start == "" {
			return fmt.Errorf("%s.start: rotate: %s needs start (YYYY-MM-DD), the date the first member's turn begins", at, r.Rotate)
		}
		d, err := time.Parse(time.DateOnly, r.Start)
		if err != nil {
			return fmt.Errorf("%s.start: %q is not a date (want YYYY-MM-DD)", at, r.Start)
		}
		r.start = civil{d.Year(), d.Month(), d.Day()}
		if r.Handover != "" {
			if r.handover, err = clock(r.Handover); err != nil {
				return fmt.Errorf("%s.handover: %w", at, err)
			}
		}
	default:
		return fmt.Errorf("%s.rotate: %q is not daily or weekly", at, r.Rotate)
	}
	return nil
}

func (p *Policy) prepare(at string, rosters map[string]bool, channels []string) error {
	at = fmt.Sprintf("%s (%s)", at, p.ID)
	p.Match.prio = p.Match.prio[:0]
	for j, s := range p.Match.Priority {
		pr, err := alarm.ParsePriority(s)
		if err != nil {
			return fmt.Errorf("%s.match.priority[%d]: %w", at, j, err)
		}
		p.Match.prio = append(p.Match.prio, pr)
	}
	for _, g := range []struct{ k, v string }{{"id", p.Match.ID}, {"site", p.Match.Site}, {"area", p.Match.Area}, {"class", p.Match.Class}} {
		if g.v == "" {
			continue
		}
		if _, err := pathMatch(g.v, ""); err != nil {
			return fmt.Errorf("%s.match.%s: %q is not a valid glob", at, g.k, g.v)
		}
	}
	p.when = nil
	if p.When != nil {
		w, err := p.When.parse(at + ".when")
		if err != nil {
			return err
		}
		p.when = &w
	}
	if len(p.Levels) == 0 {
		return fmt.Errorf("%s.levels: a policy needs at least one level", at)
	}
	var prev time.Duration
	for j, l := range p.Levels {
		lat := fmt.Sprintf("%s.levels[%d]", at, j)
		a := time.Duration(l.After)
		if a < 0 {
			return fmt.Errorf("%s.after: must not be negative", lat)
		}
		if j > 0 && a < prev {
			return fmt.Errorf("%s.after: %s is before level %d's %s; levels go in order", lat, a, j, prev)
		}
		prev = a
		if len(l.Notify) == 0 {
			return fmt.Errorf("%s.notify: name at least one roster:<id> or person", lat)
		}
		for k, n := range l.Notify {
			if id, ok := strings.CutPrefix(n, RosterPrefix); ok {
				if !rosters[id] {
					return fmt.Errorf("%s.notify[%d]: no roster %q in alarms.oncall.rosters", lat, k, id)
				}
				continue
			}
			if err := checkPrincipal(n); err != nil {
				return fmt.Errorf("%s.notify[%d]: %w", lat, k, err)
			}
		}
		if len(l.Via) == 0 {
			return fmt.Errorf("%s.via: name at least one channel (%s)", lat, strings.Join(channels, ", "))
		}
		for k, v := range l.Via {
			if !slices.Contains(channels, v) {
				return fmt.Errorf("%s.via[%d]: no channel %q on this controller (have: %s)", lat, k, v, strings.Join(channels, ", "))
			}
		}
	}
	if p.Repeat.Times < 0 {
		return fmt.Errorf("%s.repeat.times: must not be negative", at)
	}
	if p.Repeat.Times > 0 && p.Repeat.Every <= 0 {
		return fmt.Errorf("%s.repeat.every: repeat.times needs a positive every (e.g. 15m)", at)
	}
	switch p.Stop {
	case "":
		p.Stop = StopAck
	case StopAck, StopAckOrRTN:
	default:
		return fmt.Errorf("%s.stop: %q is not ack or ack-or-rtn", at, p.Stop)
	}
	p.steps = buildSteps(p)
	return nil
}

// checkPrincipal accepts a local user name or oidc:<name>, the two
// principals a person can be (#708).
func checkPrincipal(s string) error {
	name := s
	if rest, ok := strings.CutPrefix(s, "oidc:"); ok {
		name = rest
	} else if strings.Contains(s, ":") {
		return fmt.Errorf("%q is not a person: want a user name (alice), oidc:<name>, or roster:<id> in a level", s)
	}
	if name == "" || strings.ContainsAny(name, " \t\r\n") {
		return fmt.Errorf("%q is not a person: want a user name (alice) or oidc:<name>", s)
	}
	return nil
}
