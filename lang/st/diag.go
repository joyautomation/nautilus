package st

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// parseLineRE recovers a line number from a parser error message. Parse
// errors are currently plain strings that usually embed "line N"; see
// ParseErrorPos.
var parseLineRE = regexp.MustCompile(`line (\d+)`)

// ParseErrorPos best-effort extracts a source position from an error
// returned by Parse. It exists so every consumer (the LSP diagnostics and
// the `nautilus check` CLI) anchors parse errors the same way instead of
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
type LowerError struct {
	Pos Pos
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
// AST node that knew its own location stays visible.
func errAt(pos Pos, err error) error {
	if err == nil {
		return nil
	}
	var le *LowerError
	if errors.As(err, &le) {
		return err
	}
	return &LowerError{Pos: pos, Err: err}
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
