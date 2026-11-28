//go:build js

package oplog

import (
	"context"
	"fmt"
)

// Reading peers' logs is not built for the browser playground
// (catchup.go): it never constructs a log node. Were one built there, it
// fails closed: read-only, nothing caught up, nothing sequenced.

func (n *Node) leaderCatchUp(context.Context, uint64) error {
	return fmt.Errorf("%w: this build (the browser playground) does not catch a log up", ErrReadOnly)
}

// CatchUp is a no-op here, as on a standalone node.
func (n *Node) CatchUp(context.Context) error { return nil }
