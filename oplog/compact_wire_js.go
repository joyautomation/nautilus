//go:build js

package oplog

import (
	"context"
	"net/http"
)

// Compaction on the wire is not built for the browser playground
// (compact_wire.go): it never constructs a log node. Were one built there,
// it fails closed: no install is pushed or staged, and no base is served.

type compactWire struct{}
type installReq struct{}
type wireNode struct{}
type wireStatus struct{}

func (n *Node) notePeerWireLocked(string, compactWire, uint64, uint64) {}
func (n *Node) wireStatusLocked(*Status)                               {}
func (n *Node) wireLocked() compactWire                                { return compactWire{} }
func (n *Node) with409(body map[string]any) map[string]any             { return body }
func (n *Node) basePageLocked(*afterResp, uint64, bool) bool           { return false }

func (n *Node) installStep(_ context.Context, _ string, resp replicateResp, _ func([]Entry, *installReq) (replicateResp, error)) (replicateResp, error, bool) {
	return resp, errBehindBase, true
}

func (n *Node) takeInstallLocked(w http.ResponseWriter, _ *replicateReq) {
	n.mu.Unlock()
	writeJSON(w, http.StatusConflict, map[string]any{"error": "install", "detail": errBehindBase.Error()})
}
