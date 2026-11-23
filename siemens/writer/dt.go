package writer

import (
	"fmt"
	"strconv"
	"strings"
)

// A task's dt-tag (#1310).
//
// nautilus writes a task's dt-tag into the store at the start of each
// scan, before the program runs (runtime.go, scanTaskAt and step): the
// seconds since the task's previous scan started, measured on the wall
// clock. On the task's first scan after the runtime starts there is no
// previous one, and the tag gets the task's interval instead.
//
// The task's OB does the same as its first statements. RUNTIME returns
// the seconds since its previous call with the same MEM (an LREAL it keeps
// its start time in), so one call per scan at the OB's start is the time
// between this OB start and the last. On the first scan its return is
// thrown away: Siemens does not say what a first call returns, and
// nautilus's first scan is the interval, which the OB writes instead. The
// call still runs then, so MEM holds a start time by the second scan.
//
// "First" is a flag next to MEM in a NON_RETAIN clock DB: the CPU resets
// both to their start values (FALSE, 0.0) whenever it goes to RUN, so
// every RUN start is a first scan, as every runtime start is in nautilus.
//
// RUNTIME measures on an internal counter; whether a wrap shows as a
// return of zero or less is not confirmed from a Siemens source. nautilus's
// dt is never zero or negative, so such a return is treated as the first
// scan is: the tag gets the interval.
//
// Measured on the bench S7-1500 (1510SP, FW V3.1, 2026-11-23): a 100 ms
// task's first scan wrote 0.1 and its next 600 averaged 0.099999986 s
// (0.0997 to 0.1003); a 10 ms task's, 0.01 and 0.0100000 over 6007
// (0.0097 to 0.0103); and the first scan after a STOP to RUN wrote the
// interval again.

// dtOf is what a task's OB needs to write its dt-tag.
type dtOf struct {
	tag      string // the tag's member of the tags DB, as SCL spells it
	lreal    bool   // the tag is an LREAL; else a REAL
	mem      string // the task's member of the clock DB, quoted DB included
	interval string // the task's interval in seconds, an LREAL literal
}

// dtFor checks task t's dt-tag and says how its OB writes it. The tag is
// a REAL (or an LREAL) nautilus writes; another type is refused.
func (w *writer) dtFor(t Task, clock string) (*dtOf, string) {
	g, ok := w.globals[strings.ToLower(t.DtTag)]
	if !ok {
		return nil, "the writer has no type for it"
	}
	d := &dtOf{
		tag: sclName(g.Name),
		// Always quoted: a task name comes from the manifest or a file name
		// (fast-loop), not through the ST lexer.
		mem:      quote(clock) + "." + quote(t.Name),
		interval: secondsLiteral(t.Interval.Seconds()),
	}
	switch strings.ToUpper(strings.TrimSpace(g.Type)) {
	case "REAL":
	case "LREAL":
		d.lreal = true
	default:
		return nil, fmt.Sprintf("it is a %s; nautilus writes seconds into a REAL or an LREAL", g.Type)
	}
	return d, ""
}

// secondsLiteral spells s as an SCL floating-point literal: always with a
// decimal point, never an exponent.
func secondsLiteral(s float64) string {
	lit := strconv.FormatFloat(s, 'f', -1, 64)
	if !strings.Contains(lit, ".") {
		lit += ".0"
	}
	return lit
}

// temps declares the OB's temporary the measurement lands in (not "dt": TIA reads that as DATE_AND_TIME).
func (d *dtOf) temps() string {
	return "   VAR_TEMP \n      elapsed : LReal;\n   END_VAR\n"
}

// statements are the OB's first statements: the measurement, the first
// scan's interval, and the write into the tags DB.
func (d *dtOf) statements(tagsDB string) string {
	var b strings.Builder
	b.WriteString("\t// The dt-tag: the seconds since this OB last started. RUNTIME runs every scan so it always has\n")
	b.WriteString("\t// a start time; on the first scan after RUN (and on a counter wrap) the tag gets the interval.\n")
	fmt.Fprintf(&b, "\t#elapsed := RUNTIME(MEM := %s.mem);\n", d.mem)
	fmt.Fprintf(&b, "\tIF NOT %s.started OR #elapsed <= 0.0 THEN\n", d.mem)
	fmt.Fprintf(&b, "\t\t#elapsed := %s;\n", d.interval)
	fmt.Fprintf(&b, "\t\t%s.started := TRUE;\n", d.mem)
	b.WriteString("\tEND_IF;\n")
	val := "LREAL_TO_REAL(#elapsed)"
	if d.lreal {
		val = "#elapsed"
	}
	fmt.Fprintf(&b, "\t%s.%s := %s;\n", quote(tagsDB), d.tag, val)
	return b.String()
}

// clockDB holds each dt-tag task's RUNTIME start time and first-scan
// flag. NON_RETAIN: going to RUN resets both, so the scan after is a
// first scan.
func clockDB(name string, tasks []Task, dts map[string]*dtOf) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "DATA_BLOCK %s\n", quote(name))
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\n")
	b.WriteString("VERSION : 0.1\n")
	b.WriteString("NON_RETAIN\n")
	b.WriteString("   VAR \n")
	for _, t := range tasks {
		if dts[t.Name] == nil {
			continue
		}
		fmt.Fprintf(&b, "      %s : Struct\n", quote(t.Name))
		b.WriteString("         mem : LReal;\n")
		b.WriteString("         started : Bool;\n")
		b.WriteString("      END_STRUCT;\n")
	}
	b.WriteString("   END_VAR\n")
	b.WriteString("\nBEGIN\n\nEND_DATA_BLOCK\n")
	return []byte(b.String())
}
