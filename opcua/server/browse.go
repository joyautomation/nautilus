package server

import (
	"encoding/binary"

	"github.com/gopcua/opcua/ua"
	opcserver "github.com/joyautomation/nautilus/opcua/internal/uaserver"
)

// browseNodes is the Browse service: each node's references from its namespace,
// with every (target, reference type, direction) listed once. gopcua's
// built-in Server object carries most of its references two or three times
// over (the node-set import adds them, then server_nodes.go adds them
// again), and a client that builds its tree from a Browse — UAExpert, any
// gateway — showed ServerStatus three times (black-box F-29). Paging is
// gopcua's scheme: a stateless continuation point that re-browses the node
// and skips to an offset, so the deduplicated list is what is paged.
func browseNodes(srv *opcserver.Server, r ua.Request) (ua.Response, error) {
	req, ok := r.(*ua.BrowseRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	if len(req.NodesToBrowse) == 0 {
		return nil, ua.StatusBadNothingToDo
	}
	results := make([]*ua.BrowseResult, len(req.NodesToBrowse))
	for i, bd := range req.NodesToBrowse {
		results[i] = browsePage(srv, bd, 0, req.RequestedMaxReferencesPerNode)
	}
	return &ua.BrowseResponse{ResponseHeader: header(req.RequestHeader), Results: results, DiagnosticInfos: []*ua.DiagnosticInfo{}}, nil
}

// browseNextNodes continues a Browse from its continuation point, or releases
// it (nothing to release: the point is the request itself).
func browseNextNodes(srv *opcserver.Server, r ua.Request) (ua.Response, error) {
	req, ok := r.(*ua.BrowseNextRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	if len(req.ContinuationPoints) == 0 {
		return nil, ua.StatusBadNothingToDo
	}
	results := make([]*ua.BrowseResult, len(req.ContinuationPoints))
	for i, cp := range req.ContinuationPoints {
		bd, offset, max, ok := decodeContinuation(cp)
		switch {
		case !ok:
			results[i] = &ua.BrowseResult{StatusCode: ua.StatusBadContinuationPointInvalid, References: []*ua.ReferenceDescription{}}
		case req.ReleaseContinuationPoints:
			results[i] = &ua.BrowseResult{StatusCode: ua.StatusGood, References: []*ua.ReferenceDescription{}}
		default:
			results[i] = browsePage(srv, bd, offset, max)
		}
	}
	return &ua.BrowseNextResponse{ResponseHeader: header(req.RequestHeader), Results: results, DiagnosticInfos: []*ua.DiagnosticInfo{}}, nil
}

// browsePage is one node's distinct references from offset, at most max
// of them when max is not 0, with a continuation point when more follow.
func browsePage(srv *opcserver.Server, bd *ua.BrowseDescription, offset, max uint32) *ua.BrowseResult {
	if bd == nil || bd.NodeID == nil {
		return &ua.BrowseResult{StatusCode: ua.StatusBadNodeIDInvalid, References: []*ua.ReferenceDescription{}}
	}
	space, err := srv.Namespace(int(bd.NodeID.Namespace()))
	if err != nil {
		return &ua.BrowseResult{StatusCode: ua.StatusBadNodeIDUnknown, References: []*ua.ReferenceDescription{}}
	}
	res := space.Browse(bd)
	if res == nil {
		return &ua.BrowseResult{StatusCode: ua.StatusBadNodeIDUnknown, References: []*ua.ReferenceDescription{}}
	}
	if res.StatusCode != ua.StatusGood {
		return res
	}
	refs := distinctRefs(res.References)
	if int(offset) > len(refs) {
		offset = uint32(len(refs))
	}
	refs = refs[offset:]
	out := &ua.BrowseResult{StatusCode: ua.StatusGood, References: refs}
	if max > 0 && uint32(len(refs)) > max {
		out.References = refs[:max]
		out.ContinuationPoint = encodeContinuation(bd, offset+max, max)
	}
	return out
}

// distinctRefs keeps the first of each (target, reference type, direction)
// in the order browsed.
func distinctRefs(refs []*ua.ReferenceDescription) []*ua.ReferenceDescription {
	out := make([]*ua.ReferenceDescription, 0, len(refs))
	seen := map[string]bool{}
	for _, r := range refs {
		if r == nil || r.NodeID == nil {
			continue
		}
		key := r.NodeID.String() + "|" + r.ReferenceTypeID.String()
		if r.IsForward {
			key += "|f"
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// A continuation point is [offset uint32][max uint32][the BrowseDescription,
// binary-encoded], gopcua's layout.
func encodeContinuation(bd *ua.BrowseDescription, offset, max uint32) []byte {
	body, err := ua.Encode(bd)
	if err != nil {
		return nil
	}
	cp := make([]byte, 8, 8+len(body))
	binary.LittleEndian.PutUint32(cp[0:4], offset)
	binary.LittleEndian.PutUint32(cp[4:8], max)
	return append(cp, body...)
}

func decodeContinuation(cp []byte) (bd *ua.BrowseDescription, offset, max uint32, ok bool) {
	if len(cp) < 8 {
		return nil, 0, 0, false
	}
	offset, max = binary.LittleEndian.Uint32(cp[0:4]), binary.LittleEndian.Uint32(cp[4:8])
	if max == 0 {
		return nil, 0, 0, false
	}
	bd = new(ua.BrowseDescription)
	if _, err := ua.Decode(cp[8:], bd); err != nil || bd.NodeID == nil {
		return nil, 0, 0, false
	}
	return bd, offset, max, true
}
