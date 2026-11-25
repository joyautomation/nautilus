package main

import (
	"fmt"
	"sort"

	swriter "github.com/joyautomation/nautilus/siemens/writer"
)

// siemensWarningLines prints the writer's warnings (Output.Warnings) the
// way check and the deploy show them. A MUX or shift that can differ on
// the CPU is a warning at its line: rare, and surprising where it bites.
// REAL → integer conversions (siemens/conversion-range) and string results
// that may pass TIA's 254 bytes (siemens/string-length) are everywhere and
// seldom provably in range, so each program gets ONE summary line per rule
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
	type key struct{ rule, path string }
	grouped := map[key][]swriter.Diag{}
	var keys []key
	for _, w := range ws {
		if _, ok := summarized[w.Rule]; !ok {
			out = append(out, site(w))
			continue
		}
		k := key{w.Rule, w.Path}
		if grouped[k] == nil {
			keys = append(keys, k)
		}
		grouped[k] = append(grouped[k], w)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].path != keys[j].path {
			return keys[i].path < keys[j].path
		}
		return keys[i].rule < keys[j].rule
	})
	for _, k := range keys {
		n := len(grouped[k])
		line := fmt.Sprintf("%s: warning: siemens target: %s: see the Siemens target guide", where(k.path), summarized[k.rule](n))
		if !verbose {
			out = append(out, line+"; -v lists them ["+k.rule+"]")
			continue
		}
		out = append(out, line+" ["+k.rule+"]")
		for _, w := range grouped[k] {
			out = append(out, "  "+site(w))
		}
	}
	return out
}

// summarized words the summary line of each rule check and the deploy
// summarize per program, for n sites.
var summarized = map[string]func(n int) string{
	swriter.ConversionRangeRule: func(n int) string {
		return fmt.Sprintf("%d REAL → integer %s can differ on Siemens if a value exceeds its range (nautilus faults the scan; Siemens sets ENO FALSE and continues)", n, map[bool]string{true: "conversion", false: "conversions"}[n == 1])
	},
	swriter.StringLengthRule: func(n int) string {
		if n == 1 {
			return "1 string result can differ on Siemens if it grows past 254 bytes (TIA keeps the first 254 bytes; nautilus keeps them all)"
		}
		return fmt.Sprintf("%d string results can differ on Siemens if they grow past 254 bytes (TIA keeps the first 254 bytes; nautilus keeps them all)", n)
	},
}
