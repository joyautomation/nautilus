package ld

import (
	"fmt"

	"github.com/joyautomation/nautilus/lang/internal/seed"
)

// skeleton is the POU a blank .ld starts as: the smallest source Graph
// accepts, so "+ rung" and the vars panel have a body and header to land in.
func skeleton(name string) string {
	return fmt.Sprintf("PROGRAM %s\nLD\nEND_LD\nEND_PROGRAM\n", seed.PouName(name))
}

// seedEdit resolves op against a blank file (see ApplyEdit).
func seedEdit(src string, op EditOp, libs []string) ([]TextEdit, error) {
	var apply func(string) ([]seed.Edit, error)
	if op.Type != "init" {
		apply = func(skel string) ([]seed.Edit, error) {
			edits, err := ApplyEdit(skel, op, libs...)
			out := make([]seed.Edit, len(edits))
			for i, e := range edits {
				out[i] = seed.Edit(e)
			}
			return out, err
		}
	}
	res, err := seed.Seed(src, skeleton(op.Pou), apply)
	if err != nil {
		return nil, err
	}
	out := make([]TextEdit, len(res))
	for i, e := range res {
		out[i] = TextEdit(e)
	}
	return out, nil
}
