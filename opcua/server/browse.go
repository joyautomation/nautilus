package server

import (
	"encoding/binary"
	"math"

	"github.com/gopcua/opcua/id"
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
	if len(req.NodesToBrowse) > maxNodesPerBrowse {
		return nil, ua.StatusBadTooManyOperations
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
	if len(req.ContinuationPoints) > maxNodesPerBrowse { // MaxNodesPerBrowse covers BrowseNext too
		return nil, ua.StatusBadTooManyOperations
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

// Every service runs on the server's one message loop, so a request that
// asks for a great deal of work holds up every other client's requests
// meanwhile. These limits bound that; each is advertised in the Server
// object's OperationLimits (server.go), and a request past one is
// Bad_TooManyOperations. A Browse of an ns=0 node with many references
// (the Server object) costs about a millisecond, a project node far less;
// a path element about a twentieth of that.
const (
	// maxNodesPerBrowse is the most nodes one Browse, or continuation
	// points one BrowseNext, may name: a client building its tree browses
	// a few at a time, and 500 is room for a whole project's tags in one
	// call. A standard (ns=0) node costs about 1.2 ms to browse, so 500 of
	// them hold the message loop about 0.7 s; 1000 held it 1.4 s.
	maxNodesPerBrowse = 500
	// maxNodesPerTranslate is the most browse paths one
	// TranslateBrowsePathsToNodeIds may carry.
	maxNodesPerTranslate = 100
	// maxPathElements is the most elements one browse path may have; a
	// longer one is Bad_QueryTooComplex in its own result. The deepest
	// path into a project is a few levels of UDTs and arrays.
	maxPathElements = 32
)

// translateBrowsePaths is the TranslateBrowsePathsToNodeIds service (Part
// 4 §5.9.4): each path is walked from its starting node one element at a
// time, over the references a Browse lists — the same references, in ns=0
// and in the project namespace alike, filtered the same way by reference
// type, subtypes and direction. A target name matches a reference's
// BrowseName exactly, namespace index and name, case and all (Part 3
// §8.3: a QualifiedName is compared as it is), so `1:Level` finds the tag
// Browse lists as Level and `1:level` does not, though a node id of
// either spelling reads it. Every node a path reaches is a target, fully
// resolved (RemainingPathIndex is the maximum, nothing is in another
// server).
//
// A path with no elements is Bad_NothingToDo, an element other than the
// last without a target name Bad_BrowseNameInvalid, one of more than
// maxPathElements elements Bad_QueryTooComplex, a starting node that is
// not there Bad_NodeIdUnknown, and a path that reaches nothing
// Bad_NoMatch, each in its own result; more than maxNodesPerTranslate
// paths is Bad_TooManyOperations for the request.
func translateBrowsePaths(srv *opcserver.Server, r ua.Request) (ua.Response, error) {
	req, ok := r.(*ua.TranslateBrowsePathsToNodeIDsRequest)
	if !ok {
		return nil, ua.StatusBadRequestTypeInvalid
	}
	if len(req.BrowsePaths) == 0 {
		return nil, ua.StatusBadNothingToDo
	}
	if len(req.BrowsePaths) > maxNodesPerTranslate {
		return nil, ua.StatusBadTooManyOperations
	}
	results := make([]*ua.BrowsePathResult, len(req.BrowsePaths))
	for i, p := range req.BrowsePaths {
		results[i] = translatePath(srv, p)
	}
	return &ua.TranslateBrowsePathsToNodeIDsResponse{ResponseHeader: header(req.RequestHeader), Results: results, DiagnosticInfos: []*ua.DiagnosticInfo{}}, nil
}

// translatePath walks one browse path.
func translatePath(srv *opcserver.Server, p *ua.BrowsePath) *ua.BrowsePathResult {
	fail := func(code ua.StatusCode) *ua.BrowsePathResult {
		return &ua.BrowsePathResult{StatusCode: code, Targets: []*ua.BrowsePathTarget{}}
	}
	if p == nil || p.StartingNode == nil {
		return fail(ua.StatusBadNodeIDInvalid)
	}
	var elems []*ua.RelativePathElement
	if p.RelativePath != nil {
		elems = p.RelativePath.Elements
	}
	if len(elems) == 0 {
		return fail(ua.StatusBadNothingToDo)
	}
	if len(elems) > maxPathElements {
		return fail(ua.StatusBadQueryTooComplex)
	}
	for i, el := range elems {
		if el == nil || (i < len(elems)-1 && (el.TargetName == nil || el.TargetName.Name == "")) {
			return fail(ua.StatusBadBrowseNameInvalid)
		}
	}
	// The starting node must be there; a Browse of it says so.
	if res := browsePage(srv, &ua.BrowseDescription{NodeID: p.StartingNode, BrowseDirection: ua.BrowseDirectionForward, ReferenceTypeID: ua.NewNumericNodeID(0, id.HasTypeDefinition), ResultMask: uint32(ua.BrowseResultMaskAll)}, 0, 0); res.StatusCode != ua.StatusGood {
		return fail(res.StatusCode)
	}
	current := []*ua.NodeID{p.StartingNode}
	for _, el := range elems {
		dir := ua.BrowseDirectionForward
		if el.IsInverse {
			dir = ua.BrowseDirectionInverse
		}
		refType, subtypes := el.ReferenceTypeID, el.IncludeSubtypes
		if refType == nil || isNS0(refType, 0) {
			// No reference type: every reference (Part 4 §7.31), subtypes or not.
			refType, subtypes = ua.NewNumericNodeID(0, id.References), true
		}
		anyName := el.TargetName == nil || el.TargetName.Name == "" // the last element only, checked above
		var next []*ua.NodeID
		seen := map[string]bool{}
		for _, from := range current {
			res := browsePage(srv, &ua.BrowseDescription{NodeID: from, BrowseDirection: dir, ReferenceTypeID: refType, IncludeSubtypes: subtypes, ResultMask: uint32(ua.BrowseResultMaskAll)}, 0, 0)
			if res.StatusCode != ua.StatusGood {
				continue
			}
			for _, ref := range res.References {
				if ref.NodeID == nil || ref.NodeID.NodeID == nil {
					continue
				}
				if !anyName && (ref.BrowseName == nil || ref.BrowseName.NamespaceIndex != el.TargetName.NamespaceIndex || ref.BrowseName.Name != el.TargetName.Name) {
					continue
				}
				if k := ref.NodeID.NodeID.String(); !seen[k] {
					seen[k] = true
					next = append(next, ref.NodeID.NodeID)
				}
			}
		}
		if len(next) == 0 {
			return fail(ua.StatusBadNoMatch)
		}
		current = next
	}
	targets := make([]*ua.BrowsePathTarget, len(current))
	for i, nid := range current {
		targets[i] = &ua.BrowsePathTarget{TargetID: ua.NewExpandedNodeID(nid, "", 0), RemainingPathIndex: math.MaxUint32}
	}
	return &ua.BrowsePathResult{StatusCode: ua.StatusGood, Targets: targets}
}
