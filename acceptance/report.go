package acceptance

// Reporting. Two audiences: a person reading a CI log, and the VS Code
// extension's test explorer. Both get the same facts; only the encoding
// differs.
//
// Elapsed times are VIRTUAL — the number that means something. "106 scans,
// 10.5s" describes the process the test exercised; the wall time it took
// to do that is a few milliseconds and tells you nothing.

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// FormatFailure renders a failed result's detail block: what broke, when in
// virtual time, and what the traced tags were doing as it happened.
func FormatFailure(r Result) string {
	f := r.Failure
	if f == nil {
		return ""
	}
	var b strings.Builder
	loc := fmt.Sprintf("%s:%d", r.Suite, f.Line)
	if f.Step > 0 {
		fmt.Fprintf(&b, "%s — step %d, t=%s\n", loc, f.Step, vtime(f.At))
	} else {
		fmt.Fprintf(&b, "%s — t=%s\n", loc, vtime(f.At))
	}
	if f.Detail != "" {
		fmt.Fprintf(&b, "  %s   (%s)\n", f.Detail, f.Reason)
	} else {
		fmt.Fprintf(&b, "  %s\n", f.Reason)
	}
	for _, t := range f.Trace {
		head := t.Name
		if t.Unit != "" {
			head += "  " + t.Unit
		}
		if t.Desc != "" {
			head += "  — " + t.Desc
		}
		fmt.Fprintf(&b, "\n  %s\n", head)
		var at, vals strings.Builder
		for i := range t.At {
			fmt.Fprintf(&at, " %10s", vtime(time.Duration(t.At[i])*time.Millisecond))
			fmt.Fprintf(&vals, " %10s", t.Values[i])
		}
		fmt.Fprintf(&b, "   %s\n   %s\n", strings.TrimRight(at.String(), " "), strings.TrimRight(vals.String(), " "))
	}
	return b.String()
}

// Report writes the human-readable run, grouped by suite, and returns the
// number of failures.
func Report(w io.Writer, results []Result, verbose bool) int {
	bySuite := map[string][]Result{}
	var order []string
	for _, r := range results {
		if _, seen := bySuite[r.Suite]; !seen {
			order = append(order, r.Suite)
		}
		bySuite[r.Suite] = append(bySuite[r.Suite], r)
	}
	sort.Strings(order)

	failed := 0
	for _, suite := range order {
		fmt.Fprintln(w, suite)
		for _, r := range bySuite[suite] {
			if r.Passed {
				if verbose {
					fmt.Fprintf(w, "  ok    %-46s %s\n", r.Name, scanNote(r))
				} else {
					fmt.Fprintf(w, "  ok    %s\n", r.Name)
				}
				continue
			}
			failed++
			fmt.Fprintf(w, "  FAIL  %s\n", r.Name)
			fmt.Fprintln(w, indent(FormatFailure(r), "        "))
		}
	}
	fmt.Fprintf(w, "\n%d tests, %d passed, %d failed\n", len(results), len(results)-failed, failed)
	return failed
}

// ReportJSON writes one NDJSON event per test for the extension's test
// explorer. Line-delimited so it can be consumed as the run streams.
func ReportJSON(w io.Writer, results []Result) int {
	enc := json.NewEncoder(w)
	failed := 0
	for _, r := range results {
		if !r.Passed {
			failed++
		}
		type event struct {
			Result
			ElapsedMs float64 `json:"elapsedMs"`
		}
		if err := enc.Encode(event{r, float64(r.Elapsed) / float64(time.Millisecond)}); err != nil {
			return failed
		}
	}
	return failed
}

func scanNote(r Result) string {
	unit := "scans"
	if r.Scans == 1 {
		unit = "scan"
	}
	return fmt.Sprintf("%d %s, %s", r.Scans, unit, vtime(r.Elapsed))
}

// vtime renders virtual time compactly: sub-minute spans read as seconds
// with millisecond resolution, which is the scale process logic lives at.
func vtime(d time.Duration) string {
	if d < time.Minute {
		return trim(d.Seconds()) + "s"
	}
	return d.String()
}
