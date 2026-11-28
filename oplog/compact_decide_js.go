//go:build js

package oplog

// The compaction decision is not built for the browser playground
// (compact_decide.go): its node is standalone and never compacts.

type decideLimits struct{}
type decideState struct{}

func (n *Node) startDecide() {}

func (n *Node) rulesMarkedNow() {}
