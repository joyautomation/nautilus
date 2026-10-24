package server

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

func TestParseNumericRange(t *testing.T) {
	good := map[string][]span{"0": {{0, 0}}, "7": {{7, 7}}, "1:2": {{1, 2}}, "0:9": {{0, 9}}, "0:1,2:3": {{0, 1}, {2, 3}}, "4,1": {{4, 4}, {1, 1}}}
	for in, want := range good {
		got, code := parseNumericRange(in)
		if code != ua.StatusGood || !reflect.DeepEqual(got, want) {
			t.Errorf("parseNumericRange(%q) = %v %s, want %v", in, got, code, want)
		}
	}
	for _, in := range []string{"", "x", "-1", "1:", ":2", "2:1", "1:1", "1:2:3", "0,", "1.5", "0:x"} {
		if _, code := parseNumericRange(in); code != ua.StatusBadIndexRangeInvalid {
			t.Errorf("parseNumericRange(%q) = %s, want Bad_IndexRangeInvalid", in, code)
		}
	}
}

// F-28: a Read with an IndexRange returns the slice named, and an index
// the array does not reach, a range on a scalar, or more dimensions than
// the array has is Bad_IndexRangeNoData; a range that is not one is
// Bad_IndexRangeInvalid. gopcua returned the whole value for every range.
func TestReadHonoursIndexRange(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	rd := func(nid *ua.NodeID, attr ua.AttributeID, rng string) *ua.DataValue {
		resp, err := c.Read(ctx, &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{NodeID: nid, AttributeID: attr, IndexRange: rng}}, TimestampsToReturn: ua.TimestampsToReturnBoth})
		if err != nil {
			t.Fatalf("read %s[%s]: %v", nid, rng, err)
		}
		return resp.Results[0]
	}
	levels := s.NodeID("Levels") // [1, 2, 3]
	for rng, want := range map[string][]float64{"0": {1}, "2": {3}, "1:2": {2, 3}, "0:2": {1, 2, 3}} {
		dv := rd(levels, ua.AttributeIDValue, rng)
		if dv.Status != ua.StatusGood || !reflect.DeepEqual(dv.Value.Value(), want) {
			t.Errorf("Levels[%s] = %v %s, want %v", rng, dv.Value, dv.Status, want)
		}
	}
	if dv := rd(levels, ua.AttributeIDArrayDimensions, "0"); dv.Status != ua.StatusGood || !reflect.DeepEqual(dv.Value.Value(), []uint32{3}) {
		t.Errorf("Levels.ArrayDimensions[0] = %v %s", dv.Value, dv.Status)
	}
	noData := map[*ua.NodeID]string{
		levels: "3", s.NodeID("Levels"): "1:9", s.NodeID("P101.Hours"): "0,1",
		s.NodeID("Level"): "0", s.NodeID("Label"): "0", s.NodeID("Running"): "0:1",
	}
	for nid, rng := range noData {
		if dv := rd(nid, ua.AttributeIDValue, rng); dv.Status != ua.StatusBadIndexRangeNoData {
			t.Errorf("%s[%s] = %v %s, want Bad_IndexRangeNoData", nid, rng, dv.Value, dv.Status)
		}
	}
	if dv := rd(s.NodeID("Level"), ua.AttributeIDDisplayName, "0"); dv.Status != ua.StatusBadIndexRangeNoData {
		t.Errorf("Level.DisplayName[0] = %s, want Bad_IndexRangeNoData", dv.Status)
	}
	for _, rng := range []string{"2:1", "x", "1:"} {
		if dv := rd(levels, ua.AttributeIDValue, rng); dv.Status != ua.StatusBadIndexRangeInvalid {
			t.Errorf("Levels[%s] = %s, want Bad_IndexRangeInvalid", rng, dv.Status)
		}
	}
	// A node that is not there keeps its own answer.
	if dv := rd(s.NodeID("Nope"), ua.AttributeIDValue, "0"); dv.Status != ua.StatusBadNodeIDUnknown {
		t.Errorf("Nope[0] = %s, want Bad_NodeIdUnknown", dv.Status)
	}
	// And a built-in array node slices too.
	if dv := rd(ua.NewNumericNodeID(0, id.Server_NamespaceArray), ua.AttributeIDValue, "1"); dv.Status != ua.StatusGood || !reflect.DeepEqual(dv.Value.Value(), []string{"urn:joyautomation:nautilus:lift-station"}) {
		t.Errorf("NamespaceArray[1] = %v %s", dv.Value, dv.Status)
	}
}

// F-30: the Read limit the Server object advertises is the one the server
// enforces. gopcua advertised 32 and accepted 500.
func TestReadOverTheAdvertisedLimitIsRefused(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	dv := read(t, c, ua.NewNumericNodeID(0, id.Server_ServerCapabilities_OperationLimits_MaxNodesPerRead), ua.AttributeIDValue)
	if dv.Status != ua.StatusGood || dv.Value.Value() != uint32(maxNodesPerRead) {
		t.Fatalf("MaxNodesPerRead = %v %s, want %d", dv.Value, dv.Status, maxNodesPerRead)
	}
	nodes := make([]*ua.ReadValueID, maxNodesPerRead+1)
	for i := range nodes {
		nodes[i] = &ua.ReadValueID{NodeID: s.NodeID("Level"), AttributeID: ua.AttributeIDValue}
	}
	if _, err := c.Read(ctx, &ua.ReadRequest{NodesToRead: nodes}); !errors.Is(err, ua.StatusBadTooManyOperations) {
		t.Errorf("a Read of %d nodes: %v, want Bad_TooManyOperations", len(nodes), err)
	}
	resp, err := c.Read(ctx, &ua.ReadRequest{NodesToRead: nodes[:maxNodesPerRead]})
	if err != nil || len(resp.Results) != maxNodesPerRead {
		t.Errorf("a Read of exactly %d nodes: %v", maxNodesPerRead, err)
	}
	if _, err := c.Read(ctx, &ua.ReadRequest{NodesToRead: nil}); !errors.Is(err, ua.StatusBadNothingToDo) {
		t.Errorf("an empty Read: %v, want Bad_NothingToDo", err)
	}
}

// F-29: a Browse lists each (target, reference type, direction) once —
// the Server object's included, which gopcua's own listed two and three
// times over — and the pages of a capped Browse add up to the same list.
func TestBrowseListsEachReferenceOnce(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	desc := func(n uint32) *ua.BrowseDescription {
		return &ua.BrowseDescription{NodeID: ua.NewNumericNodeID(0, n), BrowseDirection: ua.BrowseDirectionForward, ReferenceTypeID: ua.NewNumericNodeID(0, id.HierarchicalReferences), IncludeSubtypes: true, NodeClassMask: uint32(ua.NodeClassAll), ResultMask: uint32(ua.BrowseResultMaskAll)}
	}
	key := func(r *ua.ReferenceDescription) string { return r.NodeID.String() + "|" + r.ReferenceTypeID.String() }
	full := map[uint32]map[string]bool{}
	for _, n := range []uint32{id.Server, id.Server_ServerCapabilities, id.Server_ServerDiagnostics, id.ObjectsFolder, uint32(folderID)} {
		bd := desc(n)
		if n == uint32(folderID) {
			bd.NodeID = ua.NewNumericNodeID(s.NamespaceIndex(), folderID)
		}
		resp, err := c.Browse(ctx, &ua.BrowseRequest{NodesToBrowse: []*ua.BrowseDescription{bd}})
		if err != nil {
			t.Fatal(err)
		}
		res := resp.Results[0]
		if res.StatusCode != ua.StatusGood || len(res.References) == 0 {
			t.Fatalf("browse %s: %s, %d references", bd.NodeID, res.StatusCode, len(res.References))
		}
		seen := map[string]bool{}
		for _, r := range res.References {
			if seen[key(r)] {
				t.Errorf("browse %s lists %s (%s) twice", bd.NodeID, r.BrowseName.Name, key(r))
			}
			seen[key(r)] = true
		}
		full[n] = seen
	}
	// Paged, three at a time, over the Server object.
	resp, err := c.Browse(ctx, &ua.BrowseRequest{RequestedMaxReferencesPerNode: 3, NodesToBrowse: []*ua.BrowseDescription{desc(id.Server)}})
	if err != nil {
		t.Fatal(err)
	}
	res := resp.Results[0]
	paged := map[string]bool{}
	pages := 0
	for {
		pages++
		if len(res.References) > 3 {
			t.Fatalf("page %d holds %d references, cap 3", pages, len(res.References))
		}
		for _, r := range res.References {
			if paged[key(r)] {
				t.Errorf("paged browse lists %s twice", key(r))
			}
			paged[key(r)] = true
		}
		if len(res.ContinuationPoint) == 0 {
			break
		}
		var next *ua.BrowseNextResponse
		err := c.Send(ctx, &ua.BrowseNextRequest{RequestHeader: &ua.RequestHeader{}, ContinuationPoints: [][]byte{res.ContinuationPoint}}, func(v ua.Response) error {
			next, _ = v.(*ua.BrowseNextResponse)
			return nil
		})
		if err != nil || next == nil {
			t.Fatalf("BrowseNext: %v", err)
		}
		res = next.Results[0]
		if res.StatusCode != ua.StatusGood {
			t.Fatalf("BrowseNext page %d: %s", pages, res.StatusCode)
		}
	}
	if !reflect.DeepEqual(paged, full[id.Server]) {
		t.Errorf("%d references over %d pages, %d in one Browse", len(paged), pages, len(full[id.Server]))
	}
	if pages < 2 {
		t.Errorf("the Server object paged in %d page(s) of 3; it has %d references", pages, len(full[id.Server]))
	}
	// A continuation point that is nothing, and a released one.
	var next *ua.BrowseNextResponse
	if err := c.Send(ctx, &ua.BrowseNextRequest{RequestHeader: &ua.RequestHeader{}, ContinuationPoints: [][]byte{[]byte("junk")}}, func(v ua.Response) error {
		next, _ = v.(*ua.BrowseNextResponse)
		return nil
	}); err != nil || next == nil || next.Results[0].StatusCode != ua.StatusBadContinuationPointInvalid {
		t.Errorf("BrowseNext(junk): %v %+v", err, next)
	}
}
