package main

import (
	"fmt"
	"sort"

	swriter "github.com/joyautomation/nautilus/siemens/writer"
)

// siemensWarningLines prints the writer's warnings (Output.Warnings) the
// way check and the deploy show them. A MUX or shift that can differ on
// the CPU is a warning at its line: rare, and surprising where it bites.
// REAL → integer conversions (siemens/conversion-range) are everywhere
// and seldom provably in range, so each program gets ONE summary line
// instead, and verbose (-v) lists the sites under it. where names a
// diagnostic's file as the caller reports it.
func siemensWarningLines(ws []swriter.Diag, where func(string) string, verbose bool) []string {
	var out []string
	site := func(w swriter.Diag) string {
		if w.Line > 0 {
			return fmt.Sprintf("%s:%d:1: warning: siemens target: %s [%s]", where(w.Path), w.Line, w.Message, w.Rule)
		}
		return fmt.Sprintf("%s: warning: siemens target: %s [%s]", where(w.Path), w.Message, w.Rule)
	}
	conv := map[string][]swriter.Diag{}
	var paths []string
	for _, w := range ws {
		if w.Rule != swriter.ConversionRangeRule {
			out = append(out, site(w))
			continue
		}
		if conv[w.Path] == nil {
			paths = append(paths, w.Path)
		}
		conv[w.Path] = append(conv[w.Path], w)
	}
	sort.Strings(paths)
	for _, p := range paths {
		n := len(conv[p])
		what := "REAL → integer conversion"
		if n > 1 {
			what += "s"
		}
		line := fmt.Sprintf("%s: warning: siemens target: %d %s can differ on Siemens if a value exceeds its range (nautilus faults the scan; Siemens sets ENO FALSE and continues): see the Siemens target guide", where(p), n, what)
		if !verbose {
			out = append(out, line+"; -v lists them ["+swriter.ConversionRangeRule+"]")
			continue
		}
		out = append(out, line+" ["+swriter.ConversionRangeRule+"]")
		for _, w := range conv[p] {
			out = append(out, "  "+site(w))
		}
	}
	return out
}
