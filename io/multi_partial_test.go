package io

import (
	"errors"
	"strings"
	"testing"
)

var errSocket = errors.New("socket ate it")

type failingWith struct{ failing }

func (f failingWith) WriteOutputs(Values) error { return errSocket }

// A push one child fails is a PartialWriteError: the keys of the children
// that took their share are Taken, the failing child's are not, and the
// error still names the child and wraps its cause (#1497). A push every
// child takes is no error at all.
func TestMultiPartialWrite(t *testing.T) {
	a := named{Memory: NewMemory(), outs: []string{"Acmd"}}
	m, err := NewMulti(NamedDriver{Name: "a", Driver: a}, NamedDriver{Name: "b", Driver: failingWith{failing{}}})
	if err != nil {
		t.Fatal(err)
	}
	err = m.WriteOutputs(Values{"Acmd": int64(1), "Bcmd": int64(2)})
	var p *PartialWriteError
	if !errors.As(err, &p) {
		t.Fatalf("WriteOutputs = %v (%T), want a *PartialWriteError", err, err)
	}
	if !p.Taken("Acmd") || p.Taken("Bcmd") {
		t.Errorf("Taken(Acmd) = %v, Taken(Bcmd) = %v; want true, false", p.Taken("Acmd"), p.Taken("Bcmd"))
	}
	if !errors.Is(err, errSocket) || !strings.Contains(err.Error(), `driver "b"`) {
		t.Errorf("the error lost its child or cause: %v", err)
	}
	if v, _ := a.Memory.ReadInputs(); v["Acmd"] != int64(1) {
		t.Errorf("the healthy child did not take its share: %v", v)
	}
	if err := m.WriteOutputs(Values{"Acmd": int64(3)}); err != nil {
		t.Errorf("a push every child takes: %v", err)
	}
}
