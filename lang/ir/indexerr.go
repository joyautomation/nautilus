package ir

import "fmt"

// IndexError is an array read whose index lies outside the array: the
// value of the index and the last offset it may take. Its text is
// "index 7 out of bounds [0..3]".
type IndexError struct {
	Index int64
	Last  int
}

func (e *IndexError) Error() string {
	return fmt.Sprintf("index %d out of bounds [0..%d]", e.Index, e.Last)
}

// indexErr is the error for an index read out of bounds in this frame. The
// frame keeps the last one and hands it back while the index and bounds are
// the same, so a read that faults every scan the same way (an expr: member
// held at its last value, #1458) allocates nothing; each error stays
// immutable once returned.
func (f *Frame) indexErr(i int64, last int) error {
	if e := f.idxErr; e != nil && e.Index == i && e.Last == last {
		return e
	}
	e := &IndexError{Index: i, Last: last}
	f.idxErr = e
	return e
}
