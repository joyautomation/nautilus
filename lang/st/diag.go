
package st

import (
	"errors"
	"fmt"
)

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
