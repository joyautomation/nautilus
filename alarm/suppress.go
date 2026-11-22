package alarm

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// Suppression by state (#715 PR 5). An alarm is suppressed, beyond the
// enable: and dark-site cases evalOne already had, when
//
//   - its equipment is out of service: an operator action, time-boxed
//     like a shelf, never permanent (TakeOutOfService);
//   - its suppress-when: BOOL reads true (designed suppression);
//   - its condition reads a substituted signal (#707): the value is one a
//     person chose, so the alarm's state would be a fiction.
//
// Suppression hides alarms, so every rule here fails toward showing
// them: a suppress-when that cannot be read suppresses nothing (and the
// record says so), an out-of-service window always ends, and an alarm
// whose suppression ends with its condition present activates afresh,
// unacknowledged, as a new occurrence.

// Suppression kinds: Record.Suppression on a suppressed alarm.
const (
	SuppressOutOfService = "out-of-service" // its equipment, by an operator
	SuppressDesigned     = "designed"       // suppress-when true, or enable false
	SuppressSubstituted  = "substituted"    // its condition reads a substitute (#707)
	SuppressUnreadable   = "unreadable"     // no condition (or enable) to read
)

// Journal kinds of the out-of-service action. Rows are system rows (ID
// SystemAlarmsID), one per action, naming the equipment; each covered
// alarm also journals its own suppress and unsuppress.
const (
	KindOutOfService = "out-of-service"
	KindInService    = "in-service"
)

// SystemAlarmsID is the journal ID of the engine's own system rows.
const SystemAlarmsID = "__nautilus.alarms"

// MaxOutOfService is the longest one out-of-service window may run. It is
// renewed by taking the equipment out of service again, never extended
// silently: a week of hidden alarms is a decision someone makes again.
const MaxOutOfService = 7 * 24 * time.Hour

// MaxOutOfServiceReason bounds the reason's length, in characters.
const MaxOutOfServiceReason = 200

// ErrNoEquipment is the error the out-of-service calls wrap when no alarm
// belongs to the named equipment.
var ErrNoEquipment = fmt.Errorf("no alarm belongs to equipment")

// outOfService is one piece of equipment's out-of-service window.
//
// It ends at until, or once left has run since setAt on the engine's
// clock, whichever comes first, and at once if that clock goes back more
// than ClockSlack past setAt: a clock set back never lengthens a window
// by more than ClockSlack (Fable review of #1222).
type outOfService struct {
	name    string // the equipment as it was named (the map key is its NameKey)
	text    string // the reason each covered alarm shows, built once
	until   time.Time
	setAt   time.Time     // the engine's clock when it was set or restored
	left    time.Duration // what was left of it at setAt
	by      string
	reason  string
	startAt time.Time
}

// ClockSlack is how far the engine's clock may step back under a window
// before the window ends (a time sync's small corrections do not end
// every window; a clock set back by hours does).
const ClockSlack = time.Minute

// remaining is what is left of o at now, and whether it has ended (and
// why: "" when it has not).
func (o outOfService) remaining(now time.Time) (time.Duration, string) {
	if now.Before(o.setAt.Add(-ClockSlack)) {
		return 0, "clock set back"
	}
	// The deadline, or the time left run out, whichever comes first.
	left := min(o.until.Sub(now), o.left-now.Sub(o.setAt))
	if left <= 0 {
		return 0, "expired"
	}
	return left, ""
}

// OutOfService is one out-of-service window, as the API and the CLI list it.
type OutOfService struct {
	Equipment string   `json:"equipment"`
	By        string   `json:"by"`
	Reason    string   `json:"reason"`
	StartMs   int64    `json:"startMs"`
	UntilMs   int64    `json:"untilMs"`
	Alarms    []string `json:"alarms"` // the alarm IDs it covers
}

// systemAlarm reports whether d is one of the controller's own alarms
// (an audit journal down, notifications failing): they exist to make a
// lockout visible, so they are never taken out of service.
func systemAlarm(d Def) bool {
	return strings.HasPrefix(d.Tag, "__nautilus.") || strings.HasPrefix(d.ID, "nautilus.")
}

// defaultEquipment is a definition's equipment when it names none: the
// root of its condition path.
func defaultEquipment(d Def) string {
	if systemAlarm(d) {
		return d.ID // each its own, and refused anyway
	}
	root, _ := ir.SplitAddress(d.Tag)
	return root
}

// checkReason refuses an empty, overlong or multi-line reason: it is shown
// on an operator's row and journalled, so it must be one line of text.
func checkReason(reason string) error {
	r := strings.TrimSpace(reason)
	switch {
	case r == "":
		return fmt.Errorf("out of service needs a reason (what is being done to the equipment), shown on each alarm it hides")
	case len([]rune(r)) > MaxOutOfServiceReason:
		return fmt.Errorf("reason is longer than %d characters", MaxOutOfServiceReason)
	case strings.IndexFunc(r, unicode.IsControl) >= 0:
		return fmt.Errorf("reason must be one line of text")
	}
	return nil
}

// covered is the instances of equipment eq (named as names are, case
// aside), or an error naming why it cannot be taken out of service.
// Under e.mu.
func (e *Engine) covered(eq string) ([]*instance, error) {
	key := ir.NameKey(eq)
	var out []*instance
	for i := range e.inst {
		in := &e.inst[i]
		if in.eqKey != key {
			continue
		}
		if systemAlarm(in.def) {
			return nil, fmt.Errorf("equipment %q holds the controller's own alarm %q: it is never taken out of service", eq, in.def.ID)
		}
		out = append(out, in)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w %q", ErrNoEquipment, eq)
	}
	return out, nil
}

// EquipmentTags is the condition paths of every alarm equipment eq
// holds: the targets an area-scoped permission is checked against.
func (e *Engine) EquipmentTags(eq string) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ins, err := e.covered(eq)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ins))
	for _, in := range ins {
		out = append(out, in.def.Tag)
	}
	return out, nil
}

// CheckOutOfService answers what TakeOutOfService would refuse, changing
// nothing (a pair validates on the leader, then proposes).
func (e *Engine) CheckOutOfService(eq string, until time.Time, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.checkOutOfService(eq, until, reason, e.now())
}

func (e *Engine) checkOutOfService(eq string, until time.Time, reason string, now time.Time) error {
	if _, err := e.covered(eq); err != nil {
		return err
	}
	if !until.After(now) {
		return fmt.Errorf("equipment %q: out of service until %s is not in the future (there is no permanent out of service)", eq, until.Format(time.RFC3339))
	}
	if until.Sub(now) > MaxOutOfService {
		return fmt.Errorf("equipment %q: out of service for at most %s at a time (take it out again to renew)", eq, MaxOutOfService)
	}
	return checkReason(reason)
}

// TakeOutOfService suppresses every alarm of equipment eq until until,
// with reason shown on each, as by. Taking equipment already out of
// service out again replaces its window (a renewal, journalled as one).
func (e *Engine) TakeOutOfService(eq string, until time.Time, by, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	if err := e.checkOutOfService(eq, until, reason, now); err != nil {
		return err
	}
	ins, _ := e.covered(eq)
	o := newOutOfService(ins[0].def.Equipment, until, until.Sub(now), by, reason, now, now)
	if e.oos == nil {
		e.oos = map[string]outOfService{}
	}
	e.oos[ins[0].eqKey] = o
	e.rev++
	e.system(now, KindOutOfService, o.by,
		"equipment "+o.name+" out of service until "+o.until.UTC().Format(time.RFC3339)+": "+o.reason)
	for _, in := range ins {
		e.suppressOOS(in, now, o)
	}
	return nil
}

// newOutOfService is a window, its shown text built once.
func newOutOfService(name string, until time.Time, left time.Duration, by, reason string, start, now time.Time) outOfService {
	reason = strings.TrimSpace(reason)
	return outOfService{name: name, until: until, setAt: now, left: left, by: by, reason: reason, startAt: start,
		text: "out of service until " + until.UTC().Format(time.RFC3339) + " by " + by + ": " + reason}
}

// retained writes o, as it stands at now, onto r: the time left (never
// what was granted: a restore caps the window at it).
func (o outOfService) retained(r *retain.AlarmRetain, now time.Time) {
	left, ended := o.remaining(now)
	if ended != "" {
		return
	}
	r.OOSEquipment, r.OOSUntilMs, r.OOSLeftMs = o.name, o.until.UnixMilli(), left.Milliseconds()
	r.OOSStartMs, r.OOSBy, r.OOSReason = o.startAt.UnixMilli(), o.by, o.reason
}

// restoreOutOfService takes r's out-of-service window back, from the
// retain store or a pair's log, if its equipment still has alarms and
// holds no system alarm, and it has not ended. It runs for no longer than
// was left when it was saved (OOSLeftMs), whatever the clock says now: a
// clock set back at a restart does not lengthen it. Each covered alarm
// carries the same window; the first restored wins. Under e.mu, before
// the first Evaluate (which suppresses).
func (e *Engine) restoreOutOfService(r retain.AlarmRetain, now time.Time) {
	ins, err := e.covered(r.OOSEquipment)
	if err != nil {
		return
	}
	key := ins[0].eqKey
	if _, done := e.oos[key]; done {
		return
	}
	until := time.UnixMilli(r.OOSUntilMs)
	left := min(time.Duration(r.OOSLeftMs)*time.Millisecond, MaxOutOfService)
	if limit := now.Add(left); until.After(limit) {
		until = limit
	}
	if left <= 0 || !until.After(now) {
		return
	}
	if e.oos == nil {
		e.oos = map[string]outOfService{}
	}
	e.oos[key] = newOutOfService(ins[0].def.Equipment, until, until.Sub(now), r.OOSBy, r.OOSReason, time.UnixMilli(r.OOSStartMs), now)
	e.rev++
}

// CheckInService answers what ReturnToService would refuse.
func (e *Engine) CheckInService(eq string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.oos[ir.NameKey(eq)]; !ok {
		return fmt.Errorf("equipment %q is not out of service", eq)
	}
	return nil
}

// ReturnToService ends equipment eq's out-of-service window early. Its
// alarms are evaluated again at the next Evaluate: a condition still
// present activates, unacknowledged.
func (e *Engine) ReturnToService(eq, by string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := ir.NameKey(eq)
	o, ok := e.oos[key]
	if !ok {
		return fmt.Errorf("equipment %q is not out of service", eq)
	}
	delete(e.oos, key)
	e.rev++
	e.system(e.now(), KindInService, by, "equipment "+o.name+" returned to service")
	return nil
}

// OutOfServiceList is every out-of-service window, the soonest to end first.
func (e *Engine) OutOfServiceList() []OutOfService {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	out := make([]OutOfService, 0, len(e.oos))
	for key, o := range e.oos {
		left, _ := o.remaining(now)
		w := OutOfService{Equipment: o.name, By: o.by, Reason: o.reason,
			StartMs: o.startAt.UnixMilli(), UntilMs: now.Add(left).UnixMilli(), Alarms: []string{}}
		for i := range e.inst {
			if e.inst[i].eqKey == key {
				w.Alarms = append(w.Alarms, e.inst[i].def.ID)
			}
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UntilMs != out[j].UntilMs {
			return out[i].UntilMs < out[j].UntilMs
		}
		return out[i].Equipment < out[j].Equipment
	})
	return out
}

// expireOutOfService ends every window whose time has come (or whose
// clock went back), journalling each, before the pass evaluates: an
// alarm whose condition stands re-annunciates in the same pass. Under
// e.mu.
func (e *Engine) expireOutOfService(now time.Time) {
	for key, o := range e.oos {
		_, ended := o.remaining(now)
		if ended == "" {
			continue
		}
		delete(e.oos, key)
		e.rev++
		e.system(now, KindInService, "system", "equipment "+o.name+" returned to service: "+ended)
	}
}

// suppressOOS suppresses in for its equipment's window o: the occurrence
// it was on ends, as with any designed suppression. Under e.mu.
func (e *Engine) suppressOOS(in *instance, now time.Time, o outOfService) {
	if in.carry != nil {
		e.endCarry(in, "its equipment was taken out of service")
	}
	e.suppressBy(in, now, SuppressOutOfService, o.text, o.by)
}

// system journals one system row of kind (an out-of-service action) under
// e.mu: not a state change of any alarm, so it reaches no notifier.
func (e *Engine) system(now time.Time, kind, by, name string) {
	ev := Event{TS: now.UnixMilli(), ID: SystemAlarmsID, Name: name, Kind: kind, By: by}
	if err := e.jrn.Append(ev); err != nil {
		e.log.Warn("alarm journal append failed", "kind", kind, "err", err)
	}
}

// suppressWhen reads in's suppress-when. suppressed is true only for a
// readable BOOL TRUE: a path that cannot be read (absent), is not a BOOL
// (a number is not: a speed or a status word named by mistake would hide
// the alarm whenever it is nonzero), or is itself substituted (a person
// chose its value: a substitute is not a way to hide alarms) sets the
// record's warning and suppresses nothing. Under e.mu.
func (e *Engine) suppressWhen(in *instance) (suppressed bool) {
	if in.def.SuppressWhen == "" {
		return false
	}
	v, ok := e.read(in.def.SuppressWhen)
	b, isBool := strictBool(v)
	switch {
	case !ok:
		in.warn = warnMissing
	case !isBool:
		in.warn = warnNotBool
	case b && e.substituted != nil:
		if _, sub := e.substituted(in.def.SuppressWhen); sub {
			in.warn = warnSubstituted
			return false
		}
		return true
	default:
		return b
	}
	return false
}

// strictBool is v as a BOOL: a Go bool or a BOOL ir.Value, nothing else.
func strictBool(v any) (val, ok bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case ir.Value:
		if x.Kind == ir.TypeBool {
			return x.B, true
		}
	}
	return false, false
}

// Record.Warning codes (instance.warn).
const (
	warnNone uint8 = iota
	warnMissing
	warnNotBool
	warnSubstituted
)

// warning is in.warn as text.
func (in *instance) warning() string {
	switch in.warn {
	case warnMissing:
		return "suppress-when " + in.def.SuppressWhen + " is not in this project: the alarm is evaluated anyway"
	case warnNotBool:
		return "suppress-when " + in.def.SuppressWhen + " is not a BOOL: the alarm is evaluated anyway"
	case warnSubstituted:
		return "suppress-when " + in.def.SuppressWhen + " is substituted: the alarm is evaluated anyway"
	}
	return ""
}

// derive fills in's fields that follow from its definition alone.
func (in *instance) derive() {
	in.eqKey = ir.NameKey(in.def.Equipment)
	in.swReason = in.def.SuppressReason
	if in.swReason == "" && in.def.SuppressWhen != "" {
		in.swReason = in.def.SuppressWhen + " is true"
	}
}
