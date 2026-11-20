package tiadtest

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// TestPlanProtection is ConfigProtection.Plan's table (RulesTest.cs) for
// the Go fake's copy, which the contract cannot reach in full: no fake
// CPU lacks the configurator.
func TestPlanProtection(t *testing.T) {
	const W, P, N, A = ProtectionWithoutPassword, ProtectionWithPassword, ProtectionNone, ProtectionAbsent
	pw, none, keep := tiad.ProtectPassword, tiad.ProtectNone, tiad.ProtectKeep
	for _, c := range []struct {
		current, want string
		have          bool
		act, refusal  string
	}{
		{N, keep, false, actNothing, ""},
		{P, keep, false, actNothing, ""},
		{A, keep, false, actNothing, ""},
		{W, pw, true, actProtect, ""},
		{N, pw, true, actProtect, ""},
		{P, pw, true, actNothing, ""},
		{N, none, false, actNothing, ""},
		{W, none, false, actProtectThenUnprotect, ""},
		{P, none, true, actUnprotectWithPassword, ""},
		{W, keep, true, "", "keep has nothing to keep: set cpu.configurationProtection to password or none"},
		{A, pw, true, "", "has no configuration protection (firmware 3.1 and later have it): set it to keep"},
		{A, none, true, "", "set it to keep"},
		{W, pw, false, "", "secrets.configurationPassword is empty"},
		{P, none, false, "", "turning that off needs it in secrets.configurationPassword"},
	} {
		act, fl := planProtection(c.current, c.want, c.have)
		what := c.current + "/" + c.want
		if c.refusal == "" {
			if fl != nil || act != c.act {
				t.Errorf("%s: %q %v, want %q", what, act, fl, c.act)
			}
		} else if fl == nil || fl.kind != tiad.KindBadRequest || !strings.Contains(fl.msg, c.refusal) {
			t.Errorf("%s: %q %+v, want a refusal naming %q", what, act, fl, c.refusal)
		}
	}
}
