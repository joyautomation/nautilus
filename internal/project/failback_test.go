//go:build !js

package project

import (
	"strings"
	"testing"
	"time"
)

// redundancy.failback (#710 d2): its defaults, and what naut check refuses.
func TestFailbackLoadsAndRefuses(t *testing.T) {
	p, err := Load(pairFS("retain: {}\n"+pairOK+"  failback:\n    prefer: a\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	f := p.Redundancy.Failback
	if f == nil || f.Prefer != "a" || f.AfterOr() != 5*time.Minute || f.MaxOr() != 3 {
		t.Fatalf("failback %+v: want prefer a, after 5m, max 3", f)
	}
	for _, c := range []struct{ name, red, want string }{
		{"prefer not a peer", "retain: {}\n" + pairOK + "  failback:\n    prefer: c\n", "want one of the pair's controllers"},
		{"after under 10 leases", "retain: {}\n" + pairOK + "  failback:\n    prefer: a\n    after: 5s\n", "at least 10 leases"},
		{"max negative", "retain: {}\n" + pairOK + "  failback:\n    prefer: a\n    max: -1\n", "want 1 or more"},
		{"no retain", pairOK + "  failback:\n    prefer: a\n", "needs a retain: section"},
		{"no pair", "retain: {}\nredundancy:\n  failback:\n    prefer: a\n", "for a redundant pair"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(pairFS(c.red), "")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
		})
	}
}
