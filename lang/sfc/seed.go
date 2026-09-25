package sfc

import (
	"fmt"

	"github.com/joyautomation/nautilus/lang/internal/seed"
)

// skeleton is the POU a blank .sfc starts as: the smallest source Graph
// accepts, so "+ step" and "+ comment" have a chart body to land in.
func skeleton(name string) string {
	return fmt.Sprintf("PROGRAM %s\nSFC\nEND_SFC\nEND_PROGRAM\n", seed.PouName(name))
}

// runnableSkeleton is skeleton with the one INITIAL_STEP every chart needs,
// so "initialize" (and any seeding op that adds no step) writes a file
// naut check accepts — as FBD's and LD's skeletons already are. "+ step"
// and a paste seed the bare skeleton instead: their own (entry) step
// becomes the initial one.
func runnableSkeleton(name string) string {
	return fmt.Sprintf("PROGRAM %s\nSFC\n  INITIAL_STEP Start:\n  END_STEP\nEND_SFC\nEND_PROGRAM\n", seed.PouName(name))
}

// seedEdit resolves op against a blank file (see ApplyEdit).
func seedEdit(src string, op EditOp) ([]TextEdit, error) {
	skel := runnableSkeleton(op.Pou)
	switch op.Type {
	case "addStep":
		// A blank chart's first step is its INITIAL_STEP, whatever the op
		// says — a chart without one fails check.
		skel = skeleton(op.Pou)
		op.Initial = true
	case "pasteSteps":
		skel = skeleton(op.Pou) // the paste's entry step becomes initial
	}
	var apply func(string) ([]seed.Edit, error)
	if op.Type != "init" {
		apply = func(skel string) ([]seed.Edit, error) {
			edits, err := ApplyEdit(skel, op)
			out := make([]seed.Edit, len(edits))
			for i, e := range edits {
				out[i] = seed.Edit(e)
			}
			return out, err
		}
	}
	res, err := seed.Seed(src, skel, apply)
	if err != nil {
		return nil, err
	}
	out := make([]TextEdit, len(res))
	for i, e := range res {
		out[i] = TextEdit(e)
	}
	return out, nil
}
