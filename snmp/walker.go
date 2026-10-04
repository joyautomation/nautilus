package snmp

import (
	"context"
	"errors"
	"fmt"

	"github.com/joyautomation/nautilus/snmp/walk"
)

// WalkSubtree GetBulks everything under root into a walk — what `naut snmp
// browse` prints, `browse --record` writes and a live `import` expands.
// The walk ends at the first varbind outside root or at endOfMibView; an
// agent that answers a non-increasing OID is refused rather than looped on
// forever. A subtree the agent does not hold is an empty walk, not an
// error.
func WalkSubtree(ctx context.Context, g Getter, root string, maxRep int) (walk.Walk, error) {
	root, err := walk.ParseOID(root)
	if err != nil {
		return nil, err
	}
	if maxRep <= 0 {
		maxRep = DefaultMaxRepetitions
	}
	var out walk.Walk
	cur := root
	for {
		vbs, err := g.GetBulk(ctx, cur, maxRep)
		if err != nil {
			var se *StatusError
			if errors.As(err, &se) && se.Status == 1 && maxRep > 1 { // tooBig
				maxRep /= 2
				continue
			}
			return out, err
		}
		if len(vbs) == 0 {
			return out, nil
		}
		for _, vb := range vbs {
			if vb.Type == walk.EndOfMibView || !walk.HasPrefix(vb.OID, root) {
				return out, nil
			}
			if walk.Compare(vb.OID, cur) <= 0 {
				return out, fmt.Errorf("agent returned %s after %s — OIDs not increasing, walk abandoned", vb.OID, cur)
			}
			if !vb.Type.IsException() {
				out = append(out, vb)
			}
			cur = vb.OID
		}
	}
}
