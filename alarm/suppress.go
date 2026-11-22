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
type outOfService struct {
	text    string // the reason each covered alarm shows, built once
	until   time.Time
	granted time.Duration // until - the start, for a restore's cap
	by      string
	reason  string
	startAt time.Time
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

// covered is the instances of equipment eq, or an error naming why it
// cannot be taken out of service. Under e.mu.
func (e *Engine) covered(eq string) ([]*instance, error) {
	var out []*instance
	for i := range e.inst {
		in := &e.inst[i]
		if in.def.Equipment != eq {
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
// holds: the targets an area-scoped permission is checked against, all of
// them, since one action hides each.
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
	e.setOutOfService(eq, newOutOfService(until, until.Sub(now), by, reason, now), now)
	return nil
}

// newOutOfService is a window, its shown text built once.
func newOutOfService(until time.Time, granted time.Duration, by, reason string, start time.Time) outOfService {
	reason = strings.TrimSpace(reason)
	return outOfService{until: until, granted: granted, by: by, reason: reason, startAt: start,
		text: "out of service until " + until.UTC().Format(time.RFC3339) + " by " + by + ": " + reason}
}

// setOutOfService records o for eq, journals it and suppresses its alarms
// now. Under e.mu.
func (e *Engine) setOutOfService(eq string, o outOfService, now time.Time) {
	if e.oos == nil {
		e.oos = map[string]outOfService{}
	}
	e.oos[eq] = o
	e.rev++
	e.system(now, KindOutOfService, o.by,
		"equipment "+eq+" out of service until "+o.until.UTC().Format(time.RFC3339)+": "+o.reason)
	for i := range e.inst {
		in := &e.inst[i]
		if in.def.Equipment == eq {
			e.suppressOOS(in, now, o)
		}
	}
}

// restoreOutOfService takes r's out-of-service window back, from the
// retain store or a pair's log, if its equipment still has alarms (an
// alarm that moved to other equipment since restores its old one's
// window, which its old fellows restore anyway), it holds no system
// alarm, and it has not ended. What remains is never
// more than was granted: a clock set back at a restart must not lengthen
// it. Each covered alarm carries the same window; the first restored
// wins. Under e.mu, before the first Evaluate (which suppresses).
func (e *Engine) restoreOutOfService(r retain.AlarmRetain, now time.Time) {
	eq := r.OOSEquipment
	if _, done := e.oos[eq]; done {
		return
	}
	if _, err := e.covered(eq); err != nil {
		return
	}
	until := time.UnixMilli(r.OOSUntilMs)
	granted := time.Duration(r.OOSGrantedMs) * time.Millisecond
	if granted <= 0 || granted > MaxOutOfService {
		granted = MaxOutOfService
	}
	if limit := now.Add(granted); until.After(limit) {
		until = limit
	}
	if !until.After(now) {
		return
	}
	if e.oos == nil {
		e.oos = map[string]outOfService{}
	}
	e.oos[eq] = newOutOfService(until, granted, r.OOSBy, r.OOSReason, time.UnixMilli(r.OOSStartMs))
	e.rev++
}

// CheckInService answers what ReturnToService would refuse.
func (e *Engine) CheckInService(eq string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.oos[eq]; !ok {
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
	if _, ok := e.oos[eq]; !ok {
		return fmt.Errorf("equipment %q is not out of service", eq)
	}
	delete(e.oos, eq)
	e.rev++
	e.system(e.now(), KindInService, by, "equipment "+eq+" returned to service")
	return nil
}

// OutOfServiceList is every out-of-service window, the soonest to end first.
func (e *Engine) OutOfServiceList() []OutOfService {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]OutOfService, 0, len(e.oos))
	for eq, o := range e.oos {
		w := OutOfService{Equipment: eq, By: o.by, Reason: o.reason,
			StartMs: o.startAt.UnixMilli(), UntilMs: o.until.UnixMilli(), Alarms: []string{}}
		for i := range e.inst {
			if e.inst[i].def.Equipment == eq {
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

// expireOutOfService ends every window whose time has come, journalling
// each, before the pass evaluates: an alarm whose condition stands
// re-annunciates in the same pass. Under e.mu.
func (e *Engine) expireOutOfService(now time.Time) {
	for eq, o := range e.oos {
		if now.Before(o.until) {
			continue
		}
		delete(e.oos, eq)
		e.rev++
		e.system(now, KindInService, "system", "equipment "+eq+" returned to service: expired")
	}
}

// suppressOOS suppresses in for its equipment's window o: the occurrence
// it was on ends, as with any designed suppression. Under e.mu.
func (e *Engine) suppressOOS(in *instance, now time.Time, o outOfService) {
	e.endCarry(in, "its equipment was taken out of service")
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
// readable TRUE; a path that cannot be read (absent, not a BOOL) sets the
// record's warning and suppresses nothing. Under e.mu.
func (e *Engine) suppressWhen(in *instance) (suppressed bool) {
	if in.def.SuppressWhen == "" {
		return false
	}
	v, ok := e.read(in.def.SuppressWhen)
	b, isBool := truth(v)
	switch {
	case !ok:
		in.warn = warnMissing
	case !isBool:
		in.warn = warnNotBool
	default:
		return b
	}
	return false
}

// Record.Warning codes (instance.warn).
const (
	warnNone uint8 = iota
	warnMissing
	warnNotBool
)

// warning is in.warn as text.
func (in *instance) warning() string {
	switch in.warn {
	case warnMissing:
		return "suppress-when " + in.def.SuppressWhen + " is not in this project: the alarm is evaluated anyway"
	case warnNotBool:
		return "suppress-when " + in.def.SuppressWhen + " is not a BOOL: the alarm is evaluated anyway"
	}
	return ""
}

// suppressWhenReason is the reason a designed suppression shows.
func (in *instance) suppressWhenReason() string {
	if in.def.SuppressReason != "" {
		return in.def.SuppressReason
	}
	return in.def.SuppressWhen + " is true"
}
