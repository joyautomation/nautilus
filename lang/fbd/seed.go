package fbd

import (
	"fmt"

	"github.com/joyautomation/nautilus/lang/internal/seed"
)

// skeleton is the POU a blank .fbd starts as: the smallest source Graph
// accepts, so every palette/declare op has a body and header to land in.
func skeleton(name string) string {
	return fmt.Sprintf("PROGRAM %s\nFBD\nEND_FBD\nEND_PROGRAM\n", seed.PouName(name))
}

// seedEdit resolves op against a blank file (see ApplyEdit).
func seedEdit(src string, op EditOp) ([]TextEdit, error) {
	var apply func(string) ([]seed.Edit, error)
	if op.Type != "init" {
		apply = func(skel string) ([]seed.Edit, error) {
			edits, err := ApplyEdit(skel, op)
			return toSeed(edits), err
		}
	}
	out, err := seed.Seed(src, skeleton(op.Pou), apply)
	if err != nil {
		return nil, err
	}
	return fromSeed(out), nil
}

func toSeed(edits []TextEdit) []seed.Edit {
	out := make([]seed.Edit, len(edits))
	for i, e := range edits {
		out[i] = seed.Edit(e)
	}
	return out
}

func fromSeed(edits []seed.Edit) []TextEdit {
	out := make([]TextEdit, len(edits))
	for i, e := range edits {
		out[i] = TextEdit(e)
	}
	return out
}

// normalize keeps the model's arrays JSON arrays ([] rather than null), so
// a consumer never has to special-case an empty diagram.
func (m *Model) normalize() *Model {
	if m.Nodes == nil {
		m.Nodes = []*Node{}
	}
	if m.Edges == nil {
		m.Edges = []*Edge{}
	}
	return m
}
