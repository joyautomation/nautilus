package escalate

import (
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// A policy's match globs read the alarm's fields ignoring case, class
// included: `class: safety` must cover an alarm declared `class: Safety`,
// or that alarm is covered by no policy and pages nobody (#1249). id, site
// and area fold case too today; this pins it.
func TestMatchClassIgnoresCase(t *testing.T) {
	c := prepare(t, `
escalation:
  - id: safety
    match: { class: safety }
    levels: [{after: 0s, notify: [alice], via: [sms]}]
  - id: glob
    match: { class: "Proc*", id: "tt*", area: "Roast*" }
    levels: [{after: 0s, notify: [bob], via: [sms]}]
`)
	for _, tc := range []struct {
		d    alarm.Def
		want string
	}{
		{alarm.Def{ID: "X", Class: "safety"}, "safety"},
		{alarm.Def{ID: "X", Class: "Safety"}, "safety"},
		{alarm.Def{ID: "X", Class: "SAFETY"}, "safety"},
		{alarm.Def{ID: "X", Class: "process"}, ""},
		{alarm.Def{ID: "TT1", Class: "PROCESS", Area: "roast2"}, "glob"},
		{alarm.Def{ID: "tt1", Class: "process-x", Area: "ROAST"}, "glob"},
		{alarm.Def{ID: "LT1", Class: "process", Area: "roast"}, ""},
	} {
		got := ""
		if p := c.Select(tc.d, time.Now()); p != nil {
			got = p.ID
		}
		if got != tc.want {
			t.Errorf("%+v: policy %q, want %q", tc.d, got, tc.want)
		}
	}
}
