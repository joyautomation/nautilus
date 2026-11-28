//go:build js

package oplog

import "context"

// Carrying out a compaction is not built for the browser playground
// (compact_worker.go): its node is standalone and in memory, and never sees
// one. Were it to, it fails closed: the copy diverges and stops.

func (n *Node) compactLoop() { n.worker.Done() }

func (n *Node) carryOutLocked(e Entry) {
	n.divergeLocked(e.Seq, "this build (the browser playground) does not carry out compactions")
}

func (n *Node) resetDivergedLocked() error { return errDiverged }

func (n *Node) proposeCompact(context.Context, uint64, []uint64) (Entry, error) {
	return Entry{}, errCompactOff
}
