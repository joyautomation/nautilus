package alarm

import (
	"fmt"
	"strings"
	"time"
)

// KindTuned is the journal kind of the row written for an alarm whose
// definition was tuned by the overlay (#1249): its history before the row
// was produced under other delays, so a suggestion's window starts after
// the last change. The row is the alarm's own (id, name's delays, priority,
// site), By "system", Reason the overlay's why. Like the detection rows it
// is journal-only (no notifier, no OnEvent) and left out of a journal query
// unless named.
const KindTuned = "tuned"

// Tuned names an alarm whose definition in Options.Defs already carries the
// overlay's delays, and why: the engine journals one KindTuned row for it at
// the first Evaluate of each leadership (the engine's start, and a
// takeover's reset), so the journal says under which delays each stretch of
// history ran. The delays are the definition's own; they are not applied
// here.
type Tuned struct {
	ID  string
	Why string
}

// checkTuned refuses a Tuned that names no definition, as a definition
// naming nothing would be refused.
func (e *Engine) checkTuned(t []Tuned) error {
	for _, x := range t {
		if _, ok := e.byID[x.ID]; !ok {
			return fmt.Errorf("alarm tuning: %q is not an alarm of this project", x.ID)
		}
	}
	return nil
}

// tunedName is what the row says: the alarm's delays in force, so a reader
// can compare runs ("tuned: on-delay 5s, off-delay 0s").
func tunedName(d *Def) string {
	return "tuned: on-delay " + d.OnDelay.String() + ", off-delay " + d.OffDelay.String()
}

// journalTuned writes the tuned rows. Under e.mu, once per leadership: the
// first Evaluate after New or a reset.
func (e *Engine) journalTuned(now time.Time) {
	for _, t := range e.tuned {
		in := &e.inst[e.byID[t.ID]]
		e.appendSystem(Event{
			TS: now.UnixMilli(), ID: in.def.ID, Name: tunedName(&in.def), Kind: KindTuned,
			Priority: in.def.Priority, Site: in.def.Site, State: in.state.String(),
			By: "system", Reason: strings.TrimSpace(t.Why),
		})
	}
}
