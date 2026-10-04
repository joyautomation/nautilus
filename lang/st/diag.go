package st

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// parseLineRE recovers a line number from a parser error message. Parse
// errors are currently plain strings that usually embed "line N"; see
// ParseErrorPos.
var parseLineRE = regexp.MustCompile(`line (\d+)`)

// ParseErrorPos best-effort extracts a source position from an error
// returned by Parse. It exists so every consumer (the LSP diagnostics and
// the `naut check` CLI) anchors parse errors the same way instead of
// each re-deriving it. ok is false when the message carries no position
// (callers should fall back to line 1). Lowering errors already carry a
// structured Pos — use AsLowerError for those.
func ParseErrorPos(err error) (Pos, bool) {
	if err == nil {
		return Pos{}, false
	}
	m := parseLineRE.FindStringSubmatch(err.Error())
	if m == nil {
		return Pos{}, false
	}
	line, convErr := strconv.Atoi(m[1])
	if convErr != nil || line < 1 {
		return Pos{}, false
	}
	return Pos{Line: line, Col: 1}, true
}

// LowerError is a structured error produced by the ST → IR lowering pass.
// It carries the source position of the offending node so the LSP and the
// /validate endpoint can render diagnostics that land on the right line
// instead of the top of the file.
//
// Pos is the most specific position the lowering pass knew: the offending
// name itself when the error is about one (an undeclared identifier, an
// unknown FB member or function, a mistyped operand), otherwise the start
// of the statement. End, when non-zero, is the position just past the
// offending token (exclusive, same line), so a diagnostic can cover the
// whole name; zero means only the start is known.
type LowerError struct {
	Pos Pos
	End Pos
	Err error
}

func (e *LowerError) Error() string {
	if e.Pos.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Pos.Line, e.Err.Error())
	}
	return e.Err.Error()
}

func (e *LowerError) Unwrap() error { return e.Err }

// errAt wraps err with a source position. If err is already a LowerError
// the existing position is kept (inner-most wins) — that way the deepest
// AST node that knew its own location stays visible. If an expression
// inside the statement pinned the error to a narrower span (see errSpan),
// that span wins over pos, while the message stays the full chain.
func errAt(pos Pos, err error) error {
	if err == nil {
		return nil
	}
	var le *LowerError
	if errors.As(err, &le) {
		return err
	}
	var se *spanError
	if errors.As(err, &se) {
		return &LowerError{Pos: se.pos, End: se.end, Err: err}
	}
	return &LowerError{Pos: pos, Err: err}
}

// spanError pins an expression-level error to the source span of the
// offending token. It is internal to lowering: it adds no text to the
// message (callers keep wrapping it with fmt.Errorf("...: %w")), and the
// statement-level errAt lifts its span onto the LowerError it returns.
type spanError struct {
	pos, end Pos
	err      error
}

func (e *spanError) Error() string { return e.err.Error() }
func (e *spanError) Unwrap() error { return e.err }

// errSpan pins err to [pos, end). An error that already carries a span is
// returned unchanged (inner-most wins, as with errAt), and an unknown pos
// (a synthesized node) leaves err for the statement to position.
func errSpan(pos, end Pos, err error) error {
	if err == nil || pos.Line <= 0 {
		return err
	}
	var se *spanError
	if errors.As(err, &se) {
		return err
	}
	if end.Line != pos.Line || end.Col <= pos.Col {
		end = Pos{}
	}
	return &spanError{pos: pos, end: end, err: err}
}

// errName pins err to a name token of the given spelling starting at pos.
// A dotted name (a member-call callee) only pins the start: the source may
// space it differently from the flattened spelling.
func errName(pos Pos, name string, err error) error {
	end := Pos{}
	if name != "" && !strings.Contains(name, ".") {
		end = Pos{Line: pos.Line, Col: pos.Col + len(name)}
	}
	return errSpan(pos, end, err)
}

// errNode pins err to the start of an AST node; the end is left to the
// consumer (the LSP extends it over the identifier found there).
func errNode(n Node, err error) error {
	return errSpan(nodePos(n), Pos{}, err)
}

// AsLowerError walks the error chain and returns the first LowerError it
// finds. The boolean is false when the error has no positional payload.
func AsLowerError(err error) (*LowerError, bool) {
	var le *LowerError
	if errors.As(err, &le) {
		return le, true
	}
	return nil, false
}
