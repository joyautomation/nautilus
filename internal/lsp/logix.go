package lsp

// The Logix deploy target's rules, on the keystroke.
//
// A project whose nautilus.yaml declares `target: logix` is deployed to an
// Allen-Bradley controller through the L5X writer (logix/writer), and a
// construct the writer cannot express must be a squiggle where it is typed,
// not a download failure (docs/design/logix-authoring.md §5.1). `naut check`
// runs the same rules over the same sources; the two must agree.

import (
	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/st"
	logix "github.com/joyautomation/nautilus/logix/writer"
)

const logixSource = "nautilus (logix target)"

// logixDiagnostics checks one program buffer against the Logix target.
// libs are the project's library sources in their original languages (the
// dialect's first), as stproject.PreludeSources returns them. The caller
// runs this only on a buffer that compiles: target rules on a broken file
// are noise.
//
// Libraries are not checked on their own. A library's types and blocks are
// checked where a program uses them, which is also where the writer meets
// them; so a file with no PROGRAM, in any language, has no target
// diagnostics.
func logixDiagnostics(path, text string, libs []string) []Diagnostic {
	if !stproject.DeclaresProgram(text) {
		return nil
	}
	if logix.Language(path) == "" {
		return []Diagnostic{logixDiag(text, programLine(text), "",
			"only ladder (.ld) and structured text (.st) programs are in the Logix subset; FBD and SFC come later")}
	}
	diags, err := logix.CheckProgram(path, text, libs...)
	if err != nil {
		return []Diagnostic{logixDiag(text, programLine(text), "", err.Error())}
	}
	out := make([]Diagnostic, 0, len(diags))
	for _, d := range diags {
		out = append(out, logixDiag(text, d.Line, d.Rule, d.Message))
	}
	return out
}

// logixDiag squiggles a whole line: the writer's diagnostics carry a line
// and no column.
func logixDiag(text string, line int, rule, msg string) Diagnostic {
	if line < 1 {
		line = 1
	}
	return Diagnostic{
		Range:    lineRange(text, line),
		Severity: SeverityError,
		Code:     rule,
		Source:   logixSource,
		Message:  msg,
	}
}

// programLine is the line of the PROGRAM keyword, where a diagnostic about
// the program as a whole belongs.
func programLine(text string) int {
	for _, t := range st.Lex(text) {
		if t.Type == st.TokenProgram {
			return t.Line
		}
	}
	return 1
}

// hasErrors reports whether any diagnostic is an error.
func hasErrors(diags []Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}
