package writer

import "fmt"

// A gap is a construct nautilus runs that the writer does not translate
// yet. It is the writer's shortcoming, not the person's: the message says
// so and names the issue that will close it (#789). A refusal that is a
// real limit of the CPU or of TIA (an interval over 60 s, a block name
// collision) is not a gap and keeps its own wording.
type gap int

const (
	gapStructured gap = iota // enums, arrays, what structs.go does not write
	gapSFC                   // SFC programs
	gapString                // STRING and the string functions
	gapStringConv            // conversions to and from STRING
	gapTime                  // TIME arithmetic, LTIME, dates
	gapOther                 // the rest
)

// gapIssue is the tracking issue of each kind of gap.
var gapIssue = map[gap]int{
	gapStructured: 1299,
	gapSFC:        1302,
	gapString:     1303,
	gapStringConv: 1375,
	gapTime:       1304,
	gapOther:      1305,
}

// gapMessage words a gap: "the Siemens writer doesn't translate <what>
// yet (#N)", then why, when there is more to say.
func gapMessage(g gap, what, why string) string {
	msg := fmt.Sprintf("the Siemens writer doesn't translate %s yet (#%d)", what, gapIssue[g])
	if why != "" {
		msg += ": " + why
	}
	return msg
}

// gap reports a gap on a line of the program.
func (pw *progWriter) gap(rule string, line int, g gap, what, why string) {
	pw.diag(rule, line, gapMessage(g, what, why))
}
