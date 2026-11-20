package main

import (
	"strings"
	"testing"
)

// naut check compiles every program language, not only Structured Text. A
// help line that said ".st" only (blackbox 020 F-01) told a ladder programmer
// the command ignored their rungs.
func TestUsageSaysCheckCompilesEveryLanguage(t *testing.T) {
	_, rest, ok := strings.Cut(usage, "naut check [path]")
	if !ok {
		t.Fatal("usage has no naut check entry")
	}
	entry, _, _ := strings.Cut(rest, "naut new [name]")
	for _, ext := range []string{".st", ".ld", ".fbd", ".sfc"} {
		if !strings.Contains(entry, ext) {
			t.Errorf("the naut check entry in the usage does not mention %s:\n%s", ext, entry)
		}
	}
}
