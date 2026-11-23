package writer

import "fmt"

// A gap is a construct nautilus runs that the writer does not translate
// yet. It is the writer's shortcoming, not the person's: the message says
// so and names the issue that will close it (#789). A refusal that is a
// real limit of the CPU or of TIA (an interval over 60 s, a block name
// collision) is not a gap and keeps its own wording.
type gap int

const (
	gapStructured gap = iota // UDTs, enums, arrays, structured start values
	gapTasks                 // more than one task
	gapPOUs                  // user FUNCTIONs and FBs, library FBs, VAR CONSTANT
	gapSFC                   // SFC programs
	gapString                // STRING and the string functions
	gapTime                  // TIME arithmetic, LTIME, dates
	gapOther                 // the rest
	gapDtTag                 // a task's dt-tag
)

// gapIssue is the tracking issue of each kind of gap.
var gapIssue = map[gap]int{
	gapStructured: 1299,
	gapTasks:      1300,
	gapPOUs:       1301,
	gapSFC:        1302,
	gapString:     1303,
	gapTime:       1304,
	gapOther:      1305,
	gapDtTag:      1310,
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
