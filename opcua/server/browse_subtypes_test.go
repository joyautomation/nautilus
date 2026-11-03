package server

import (
	"context"
	"testing"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// A Browse that names a reference type with IncludeSubtypes false matches
// that type exactly (Part 4, 5.8.2). The ns=0 browse used to delete
// HasSubtype from the list of subtypes in a loop that never advanced, so
// a client asking for HierarchicalReferences without subtypes (whose
// subtypes include HasSubtype) panicked the handler, and with no recover
// on the service path, the whole controller. Any anonymous client on the
// None endpoint could send it.
func TestBrowseWithoutSubtypesMatchesExactlyAndNeverPanics(t *testing.T) {
	rt, _ := newRuntime(t)
	_, c := startServer(t, rt)
	ask := func(refType uint32, subtypes bool) []*ua.ReferenceDescription {
		t.Helper()
		resp, err := c.Browse(context.Background(), &ua.BrowseRequest{
			View: &ua.ViewDescription{ViewID: ua.NewTwoByteNodeID(0)},
			NodesToBrowse: []*ua.BrowseDescription{{
				NodeID:          ua.NewNumericNodeID(0, id.ObjectsFolder),
				BrowseDirection: ua.BrowseDirectionForward,
				ReferenceTypeID: ua.NewNumericNodeID(0, refType),
				IncludeSubtypes: subtypes,
				ResultMask:      uint32(ua.BrowseResultMaskAll),
			}},
		})
		if err != nil {
			t.Fatalf("browse Objects (ref type i=%d, subtypes %v): %v", refType, subtypes, err)
		}
		if len(resp.Results) != 1 {
			t.Fatalf("browse returned %d results", len(resp.Results))
		}
		return resp.Results[0].References
	}
	for _, rt := range []uint32{id.HierarchicalReferences, id.HasChild, id.References} {
		for _, r := range ask(rt, false) {
			if r.ReferenceTypeID.IntID() != rt {
				t.Errorf("ref type i=%d without subtypes returned a i=%d reference (%s)", rt, r.ReferenceTypeID.IntID(), r.BrowseName.Name)
			}
		}
	}
	// With subtypes, HierarchicalReferences still finds Objects' Organizes
	// references (Server, the project folder).
	if len(ask(id.HierarchicalReferences, true)) == 0 {
		t.Error("HierarchicalReferences with subtypes found nothing under Objects")
	}
	// Organizes without subtypes, exactly: the same Organizes references.
	if len(ask(id.Organizes, false)) == 0 {
		t.Error("Organizes without subtypes found nothing under Objects")
	}
}
