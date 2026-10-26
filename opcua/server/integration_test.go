package server

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

func TestFBPinsAreChildNodes(t *testing.T) {
	rt, _ := newRuntime(t)
	tonTag(t, rt, "T1")
	s, c := startServer(t, rt)
	folder := browse1(t, c, ua.NewNumericNodeID(s.NamespaceIndex(), folderID))
	if folder["T1"] == nil || folder["T1"].NodeClass != ua.NodeClassObject {
		t.Fatalf("T1 under the folder: %v", folder["T1"])
	}
	t1 := browse1(t, c, s.NodeID("T1"))
	for _, pin := range []string{"IN", "PT", "Q", "ET"} {
		if t1[pin] == nil {
			t.Errorf("T1 lacks pin %s: %v", pin, keys(t1))
		}
	}
	for name := range t1 {
		if strings.HasPrefix(name, "_") {
			t.Errorf("internal exposed: %s", name)
		}
	}
	if dt := read(t, c, s.NodeID("T1.PT"), ua.AttributeIDDataType).Value.Value().(*ua.NodeID).IntID(); dt != id.Duration {
		t.Errorf("T1.PT data type i=%d", dt)
	}
	if q := read(t, c, s.NodeID("T1.Q"), ua.AttributeIDValue); q.Status != ua.StatusGood || q.Value.Value() != false {
		t.Errorf("T1.Q = %v %s", q.Value, q.Status)
	}
	if st := read(t, c, s.NodeID("T1"), ua.AttributeIDValue).Status; st != ua.StatusBadAttributeIDInvalid {
		t.Errorf("T1 itself has a value: %s", st)
	}
}

// browse1 is browse without the ResultMask/ReferenceType plumbing noise:
// forward hierarchical references of one node, by browse name.
func browse1(t *testing.T, c *opcua.Client, nid *ua.NodeID) map[string]*ua.ReferenceDescription {
	t.Helper()
	return browseWith(t, c, &ua.BrowseDescription{
		NodeID:          nid,
		BrowseDirection: ua.BrowseDirectionForward,
		ReferenceTypeID: ua.NewNumericNodeID(0, id.HierarchicalReferences),
		IncludeSubtypes: true,
		NodeClassMask:   uint32(ua.NodeClassAll),
		ResultMask:      uint32(ua.BrowseResultMaskAll),
	})
}

func browseWith(t *testing.T, c *opcua.Client, bd *ua.BrowseDescription) map[string]*ua.ReferenceDescription {
	t.Helper()
	resp, err := c.Browse(context.Background(), &ua.BrowseRequest{NodesToBrowse: []*ua.BrowseDescription{bd}})
	if err != nil {
		t.Fatalf("browse %s: %v", bd.NodeID, err)
	}
	if st := resp.Results[0].StatusCode; st != ua.StatusGood {
		t.Fatalf("browse %s: %s", bd.NodeID, st)
	}
	out := map[string]*ua.ReferenceDescription{}
	for _, r := range resp.Results[0].References {
		out[r.BrowseName.Name] = r
	}
	return out
}

// widthsRuntime declares one tag per IEC integer width.
func widthsRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	widths := []string{"SINT", "USINT", "BYTE", "INT", "UINT", "WORD", "DINT", "UDINT", "DWORD", "LINT", "ULINT", "LWORD"}
	var decls strings.Builder
	var tags []runtime.TagDef
	for _, w := range widths {
		fmt.Fprintf(&decls, "  V_%s : %s;\n", w, w)
		tags = append(tags, runtime.Typed("V_"+w, runtime.RoleState, w))
	}
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL\n" + decls.String() + "  A_BYTE : ARRAY[0..1] OF BYTE;\nEND_VAR\nEND_PROGRAM\n",
		Driver:  nio.NewMemory(),
		Scan:    time.Second,
		Tags:    append(tags, runtime.Typed("A_BYTE", runtime.RoleState, "ARRAY[0..1] OF BYTE")),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range widths {
		rt.Tags().Set("V_"+w, int64(7))
	}
	for i, v := range []int{1, 255} {
		if err := rt.Tags().SetPath(fmt.Sprintf("A_BYTE[%d]", i), v); err != nil {
			t.Fatal(err)
		}
	}
	return rt
}

func TestEveryIntegerWidthReadsAsItsOwnType(t *testing.T) {
	rt := widthsRuntime(t)
	s, c := startServer(t, rt)
	want := map[string]struct {
		dt  uint32
		val any
	}{
		"V_SINT": {id.SByte, int8(7)}, "V_USINT": {id.Byte, uint8(7)}, "V_BYTE": {id.Byte, uint8(7)},
		"V_INT": {id.Int16, int16(7)}, "V_UINT": {id.UInt16, uint16(7)}, "V_WORD": {id.UInt16, uint16(7)},
		"V_DINT": {id.Int32, int32(7)}, "V_UDINT": {id.UInt32, uint32(7)}, "V_DWORD": {id.UInt32, uint32(7)},
		"V_LINT": {id.Int64, int64(7)}, "V_ULINT": {id.UInt64, uint64(7)}, "V_LWORD": {id.UInt64, uint64(7)},
	}
	var nodes []*ua.ReadValueID
	var names []string
	for name := range want {
		names = append(names, name)
		nodes = append(nodes, &ua.ReadValueID{NodeID: s.NodeID(name), AttributeID: ua.AttributeIDValue},
			&ua.ReadValueID{NodeID: s.NodeID(name), AttributeID: ua.AttributeIDDataType})
	}
	// One Read for all of them: 24 nodes, below gopcua's MaxNodesPerRead.
	resp, err := c.Read(context.Background(), &ua.ReadRequest{NodesToRead: nodes})
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		val, dt := resp.Results[2*i], resp.Results[2*i+1]
		if val.Status != ua.StatusGood || val.Value.Value() != want[name].val {
			t.Errorf("%s = %v (%T) %s, want %v (%T)", name, val.Value.Value(), val.Value.Value(), val.Status, want[name].val, want[name].val)
		}
		if got := dt.Value.Value().(*ua.NodeID).IntID(); got != want[name].dt {
			t.Errorf("%s DataType i=%d, want i=%d", name, got, want[name].dt)
		}
	}
	// An ARRAY OF BYTE arrives as an array of Byte, not a ByteString.
	bytes := read(t, c, s.NodeID("A_BYTE"), ua.AttributeIDValue)
	if !bytes.Value.Has(ua.VariantArrayValues) || bytes.Value.Type() != ua.TypeIDByte {
		t.Errorf("A_BYTE encoded as %v (type %v)", bytes.Value.Value(), bytes.Value.Type())
	}
	switch got := bytes.Value.Value().(type) {
	case []uint8:
		if len(got) != 2 || got[1] != 255 {
			t.Errorf("A_BYTE = %v", got)
		}
	case ua.ByteArray:
		if len(got) != 2 || got[1] != 255 {
			t.Errorf("A_BYTE = %v", got)
		}
	default:
		t.Errorf("A_BYTE = %v (%T)", got, got)
	}
}

func TestBrowseFiltersThroughTheClient(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	// Inverse: a member's parent; a tag's folder; the folder's Objects.
	inv := browseWith(t, c, &ua.BrowseDescription{NodeID: s.NodeID("P101.Drive.Speed"), BrowseDirection: ua.BrowseDirectionInverse, ResultMask: uint32(ua.BrowseResultMaskAll)})
	if len(inv) != 1 || inv["Drive"] == nil || inv["Drive"].NodeID.NodeID.StringID() != "P101.Drive" {
		t.Errorf("Speed inverse %v", keys(inv))
	}
	inv = browseWith(t, c, &ua.BrowseDescription{NodeID: s.NodeID("Level"), BrowseDirection: ua.BrowseDirectionInverse, ResultMask: uint32(ua.BrowseResultMaskAll)})
	if len(inv) != 1 || inv["lift-station"] == nil {
		t.Errorf("Level inverse %v", keys(inv))
	}
	inv = browseWith(t, c, &ua.BrowseDescription{NodeID: ua.NewNumericNodeID(s.NamespaceIndex(), folderID), BrowseDirection: ua.BrowseDirectionInverse, ResultMask: uint32(ua.BrowseResultMaskAll)})
	if len(inv) != 1 || inv["Objects"] == nil {
		t.Errorf("folder inverse %v", keys(inv))
	}
	// Type definitions only.
	td := browseWith(t, c, &ua.BrowseDescription{NodeID: s.NodeID("Level"), BrowseDirection: ua.BrowseDirectionForward, ReferenceTypeID: ua.NewNumericNodeID(0, id.HasTypeDefinition), ResultMask: uint32(ua.BrowseResultMaskAll)})
	if len(td) != 1 || td["BaseDataVariableType"] == nil || td["BaseDataVariableType"].NodeID.NodeID.IntID() != id.BaseDataVariableType {
		t.Errorf("Level type definition %v", keys(td))
	}
	td = browseWith(t, c, &ua.BrowseDescription{NodeID: s.NodeID("P101"), BrowseDirection: ua.BrowseDirectionForward, ReferenceTypeID: ua.NewNumericNodeID(0, id.HasTypeDefinition), ResultMask: uint32(ua.BrowseResultMaskAll)})
	if len(td) != 1 || td["BaseObjectType"] == nil {
		t.Errorf("P101 type definition %v", keys(td))
	}
	// Node class mask: objects only under the folder.
	objs := browseWith(t, c, &ua.BrowseDescription{NodeID: ua.NewNumericNodeID(s.NamespaceIndex(), folderID), BrowseDirection: ua.BrowseDirectionForward, ReferenceTypeID: ua.NewNumericNodeID(0, id.HierarchicalReferences), IncludeSubtypes: true, NodeClassMask: uint32(ua.NodeClassObject), ResultMask: uint32(ua.BrowseResultMaskAll)})
	if len(objs) != 2 || objs["P101"] == nil || objs["Pumps"] == nil {
		t.Errorf("objects under the folder %v", keys(objs))
	}
	// Both directions on a member: parent and type definition and children.
	both := browseWith(t, c, &ua.BrowseDescription{NodeID: s.NodeID("P101.Drive"), BrowseDirection: ua.BrowseDirectionBoth, ResultMask: uint32(ua.BrowseResultMaskAll)})
	for _, want := range []string{"P101", "BaseObjectType", "Speed", "Amps"} {
		if both[want] == nil {
			t.Errorf("P101.Drive both-directions lacks %s: %v", want, keys(both))
		}
	}
	// Paging: a cap smaller than the folder returns a continuation point,
	// and BrowseNext resumes in the same stable order with nothing
	// skipped or repeated.
	bd := &ua.BrowseDescription{NodeID: ua.NewNumericNodeID(s.NamespaceIndex(), folderID), BrowseDirection: ua.BrowseDirectionForward, ReferenceTypeID: ua.NewNumericNodeID(0, id.Organizes), ResultMask: uint32(ua.BrowseResultMaskAll)}
	resp, err := c.Browse(context.Background(), &ua.BrowseRequest{RequestedMaxReferencesPerNode: 4, NodesToBrowse: []*ua.BrowseDescription{bd}})
	if err != nil {
		t.Fatal(err)
	}
	res := resp.Results[0]
	if len(res.References) != 4 || len(res.ContinuationPoint) == 0 {
		t.Fatalf("paged browse: %d refs, cp %d bytes", len(res.References), len(res.ContinuationPoint))
	}
	var paged []string
	for _, r := range res.References {
		paged = append(paged, r.BrowseName.Name)
	}
	cp := res.ContinuationPoint
	for len(cp) > 0 {
		next, err := c.BrowseNext(context.Background(), &ua.BrowseNextRequest{ContinuationPoints: [][]byte{cp}})
		if err != nil {
			t.Fatal(err)
		}
		if next.Results[0].StatusCode != ua.StatusGood {
			t.Fatalf("browse next: %s", next.Results[0].StatusCode)
		}
		for _, r := range next.Results[0].References {
			paged = append(paged, r.BrowseName.Name)
		}
		cp = next.Results[0].ContinuationPoint
	}
	all := browseWith(t, c, bd)
	if len(paged) != len(all) {
		t.Fatalf("paging returned %d names, one browse %d: %v", len(paged), len(all), paged)
	}
	seen := map[string]bool{}
	for _, n := range paged {
		if seen[n] || all[n] == nil {
			t.Errorf("paging repeated or invented %s", n)
		}
		seen[n] = true
	}
}

func TestSubscriptionCoversArraysElementsAndStaysQuiet(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	notify := make(chan *opcua.PublishNotificationData, 64)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 50 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(ctx)
	handles := map[uint32]string{1: "Levels", 2: "Pumps[2].Hours", 3: "Pumps[1].Drive.Speed", 4: "Label"}
	var items []*ua.MonitoredItemCreateRequest
	for h, addr := range handles {
		items = append(items, opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID(addr), ua.AttributeIDValue, h))
	}
	if _, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, items...); err != nil {
		t.Fatal(err)
	}
	// Every item reports its current value once on creation; wait those
	// out so only real changes are counted below.
	initial := map[uint32]bool{}
	deadline := time.After(5 * time.Second)
	for len(initial) < len(handles) {
		select {
		case n := <-notify:
			if dcn, ok := n.Value.(*ua.DataChangeNotification); ok {
				for _, it := range dcn.MonitoredItems {
					initial[it.ClientHandle] = true
				}
			}
		case <-deadline:
			t.Fatalf("initial values for only %v", initial)
		}
	}
	// Three changes; Label (handle 4) does not move and must stay quiet.
	if err := rt.Tags().SetPath("Levels[1]", 42.0); err != nil {
		t.Fatal(err)
	}
	if err := rt.Tags().SetPath("Pumps[2].Hours[2]", 5); err != nil {
		t.Fatal(err)
	}
	if err := rt.Tags().SetPath("Pumps[1].Drive.Speed", 33.0); err != nil {
		t.Fatal(err)
	}
	got := map[uint32]*ua.DataValue{}
	deadline = time.After(5 * time.Second)
	for len(got) < 3 {
		select {
		case n := <-notify:
			if n.Error != nil {
				t.Fatal(n.Error)
			}
			if dcn, ok := n.Value.(*ua.DataChangeNotification); ok {
				for _, it := range dcn.MonitoredItems {
					got[it.ClientHandle] = it.Value
				}
			}
		case <-deadline:
			t.Fatalf("after 5s only %v", got)
		}
	}
	if lv, ok := got[1].Value.Value().([]float64); !ok || lv[1] != 42 {
		t.Errorf("Levels notified %v", got[1].Value.Value())
	}
	if hv, ok := got[2].Value.Value().([]int32); !ok || hv[1] != 5 {
		t.Errorf("Pumps[2].Hours notified %v", got[2].Value.Value())
	}
	if got[3].Value.Value() != 33.0 {
		t.Errorf("Pumps[1].Drive.Speed notified %v", got[3].Value.Value())
	}
	if got[4] != nil {
		t.Errorf("Label was notified without changing: %v", got[4].Value.Value())
	}
	// The notification's SourceTimestamp is the sweep's stamp — the same
	// one a later read of the same tag returns.
	later := read(t, c, s.NodeID("Pumps[1].Drive.Speed"), ua.AttributeIDValue)
	if !later.SourceTimestamp.Equal(got[3].SourceTimestamp) {
		t.Errorf("notification stamped %v, read stamped %v", got[3].SourceTimestamp, later.SourceTimestamp)
	}
	// Nothing else arrives while nothing changes.
	select {
	case n := <-notify:
		if dcn, ok := n.Value.(*ua.DataChangeNotification); ok && len(dcn.MonitoredItems) > 0 {
			t.Errorf("unprompted notification: %v", dcn.MonitoredItems[0].Value.Value.Value())
		}
	case <-time.After(300 * time.Millisecond):
	}
}

func TestSubscriptionFollowsTheRunningProgram(t *testing.T) {
	mem := nio.NewMemory()
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL\n Count : DINT;\nEND_VAR\n Count := Count + 1;\nEND_PROGRAM\n",
		Driver:  mem,
		Scan:    10 * time.Millisecond,
		Tags:    []runtime.TagDef{runtime.State("Count", int64(0))},
	})
	if err != nil {
		t.Fatal(err)
	}
	s, c := startServer(t, rt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt.Run(ctx)

	notify := make(chan *opcua.PublishNotificationData, 64)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 50 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(context.Background())
	if _, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Count"), ua.AttributeIDValue, 1)); err != nil {
		t.Fatal(err)
	}
	var last int32 = -1
	rises := 0
	deadline := time.After(5 * time.Second)
	for rises < 3 {
		select {
		case n := <-notify:
			if dcn, ok := n.Value.(*ua.DataChangeNotification); ok {
				for _, it := range dcn.MonitoredItems {
					v := it.Value.Value.Value().(int32)
					if v <= last {
						t.Errorf("Count went %d → %d", last, v)
					}
					last = v
					rises++
				}
			}
		case <-deadline:
			t.Fatalf("only %d notifications of the counting program", rises)
		}
	}
}

// The scenario that killed #278's first cut: thousands of tags written
// every scan while clients browse, read and subscribe. Under -race, every
// path the server has is exercised against a store that never stops
// moving. The work is a fixed amount — so many browses, so many reads —
// rather than a timed spin, because gopcua serves every request on one
// goroutine and an open-ended loop would just measure the CPU.
func TestThousandsOfMovingTagsUnderConcurrentClients(t *testing.T) {
	const n = 3000
	var decls, body strings.Builder
	tags := make([]runtime.TagDef, 0, n)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&decls, "  T%d : REAL;\n", i)
		fmt.Fprintf(&body, "  T%d := T%d + 1.0;\n", i, i)
		tags = append(tags, runtime.State(fmt.Sprintf("T%d", i), 0.0))
	}
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL\n" + decls.String() + "END_VAR\n" + body.String() + "END_PROGRAM\n",
		Driver:  nio.NewMemory(),
		Scan:    50 * time.Millisecond,
		Tags:    tags,
	})
	if err != nil {
		t.Fatal(err)
	}
	s, c1 := startServer(t, rt)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	go rt.Run(ctx)
	// An operator hammering writes from outside the scan, too.
	go func() {
		for i := 0; ctx.Err() == nil; i++ {
			rt.Tags().Set(fmt.Sprintf("T%d", i%n), float64(-i))
			time.Sleep(time.Millisecond)
		}
	}()
	c2, err := opcua.NewClient(s.URL(), opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := c2.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer ccancel()
		c2.Close(cctx)
	}()

	// A subscriber on a hundred of the moving tags, first, so the sweep's
	// notifications run under the read and browse load.
	notify := make(chan *opcua.PublishNotificationData, 1024)
	sub, err := c1.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 100 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(context.Background())
	var items []*ua.MonitoredItemCreateRequest
	for i := 0; i < 100; i++ {
		items = append(items, opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID(fmt.Sprintf("T%d", i*7)), ua.AttributeIDValue, uint32(i+1)))
	}
	if _, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, items...); err != nil {
		t.Fatal(err)
	}
	var notified atomic.Int64
	go func() {
		for {
			select {
			case n := <-notify:
				if dcn, ok := n.Value.(*ua.DataChangeNotification); ok {
					notified.Add(int64(len(dcn.MonitoredItems)))
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	fail := make(chan error, 8)
	const readBatches, browses = 40, 5
	for _, c := range []*opcua.Client{c1, c2} {
		wg.Add(2)
		go func(c *opcua.Client) { // reader: 40 batches of 30 tags
			defer wg.Done()
			for i := 0; i < readBatches; i++ {
				var nodes []*ua.ReadValueID
				for j := 0; j < 30; j++ {
					nodes = append(nodes, &ua.ReadValueID{NodeID: s.NodeID(fmt.Sprintf("T%d", (i*30+j)%n)), AttributeID: ua.AttributeIDValue})
				}
				resp, err := c.Read(ctx, &ua.ReadRequest{NodesToRead: nodes})
				if err != nil {
					fail <- err
					return
				}
				for _, r := range resp.Results {
					if r.Status != ua.StatusGood {
						fail <- fmt.Errorf("read status %s", r.Status)
						return
					}
				}
			}
		}(c)
		go func(c *opcua.Client) { // browser: the whole folder, 5 times
			defer wg.Done()
			for i := 0; i < browses; i++ {
				resp, err := c.Browse(ctx, &ua.BrowseRequest{NodesToBrowse: []*ua.BrowseDescription{{
					NodeID: ua.NewNumericNodeID(s.NamespaceIndex(), folderID), BrowseDirection: ua.BrowseDirectionForward,
					ReferenceTypeID: ua.NewNumericNodeID(0, id.Organizes), ResultMask: uint32(ua.BrowseResultMaskAll)}}})
				if err != nil {
					fail <- err
					return
				}
				if got := len(resp.Results[0].References); got != n {
					fail <- fmt.Errorf("browse listed %d of %d tags", got, n)
					return
				}
			}
		}(c)
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case err := <-fail:
		t.Fatal(err)
	case <-time.After(60 * time.Second):
		t.Fatal("clients did not finish their reads and browses within 60s")
	}
	select {
	case err := <-fail:
		t.Fatal(err)
	default:
	}
	deadline := time.Now().Add(5 * time.Second)
	for notified.Load() < 100 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if notified.Load() < 100 {
		t.Errorf("only %d data changes for 100 moving monitored items", notified.Load())
	}
	if st := s.Status(); st.Tags != n || st.MonitoredItems != 100 || !st.Listening {
		t.Errorf("status %+v", st)
	}
	t.Logf("%d reads of 30, %d browses of %d, %d notifications", 2*readBatches, 2*browses, n, notified.Load())
}

func TestStopDropsClientsAndRestartReusesThePort(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	url := s.URL()
	ctx := context.Background()
	if st := s.Status(); !st.Listening || st.Endpoint != url {
		t.Fatalf("status before stop %+v", st)
	}
	s.Stop()
	if st := s.Status(); st.Listening || st.Subscriptions != 0 {
		t.Errorf("status after stop %+v", st)
	}
	s.Stop() // idempotent
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := c.Read(rctx, &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{NodeID: s.NodeID("Level"), AttributeID: ua.AttributeIDValue}}}); err == nil {
		t.Error("a read succeeded after Stop")
	}
	// Start again on the same explicit port.
	host, port, err := ParseEndpoint(url)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := New(rt, Options{Endpoint: fmt.Sprintf("%s:%d", host, port), Name: "again", Sweep: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s2.Stop)
	if s2.URL() != url {
		t.Errorf("restarted on %s, want %s", s2.URL(), url)
	}
	c2, err := opcua.NewClient(url, opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := c2.Connect(ctx); err != nil {
		t.Fatalf("reconnect after restart: %v", err)
	}
	defer c2.Close(ctx)
	objects := browse1(t, c2, ua.NewNumericNodeID(0, id.ObjectsFolder))
	if objects["again"] == nil {
		t.Errorf("restarted server's folder missing: %v", keys(objects))
	}
	if dv := read(t, c2, s2.NodeID("Level"), ua.AttributeIDValue); dv.Value.Value() != 3.5 {
		t.Errorf("Level after restart %v", dv.Value.Value())
	}
}

func TestStartReportsAPortInUse(t *testing.T) {
	rt, _ := newRuntime(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	s, err := New(rt, Options{Endpoint: l.Addr().String(), Name: "clash"})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Start(context.Background())
	if err == nil {
		s.Stop()
		t.Fatal("Start succeeded on a port in use")
	}
	if !strings.Contains(err.Error(), "opcua: listen") {
		t.Errorf("error %v", err)
	}
	st := s.Status()
	if st.Listening || st.LastError == "" || st.Endpoint != "opc.tcp://"+l.Addr().String() {
		t.Errorf("status after failed start %+v", st)
	}
	s.Stop() // a no-op, not a panic
}

func TestStatusCountsWhatClientsHold(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	if st := s.Status(); st.Tags != 9 || st.Subscriptions != 0 || st.MonitoredItems != 0 {
		t.Errorf("fresh status %+v", st)
	}
	notify := make(chan *opcua.PublishNotificationData, 8)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 100 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth,
		opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Level"), ua.AttributeIDValue, 1),
		opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Count"), ua.AttributeIDValue, 2),
		opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Label"), ua.AttributeIDValue, 3)); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.Subscriptions != 1 || st.MonitoredItems != 3 {
		t.Errorf("status with a subscription %+v", st)
	}
	if err := sub.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := s.Status()
		if st.Subscriptions == 0 && st.MonitoredItems == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status after cancel %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A tag created after start is served: the store's shape is read live.
	rt.Tags().Set("Late", ir.RealVal(1))
	if st := s.Status(); st.Tags != 10 {
		t.Errorf("tags after a new one %+v", st)
	}
	if dv := read(t, c, s.NodeID("Late"), ua.AttributeIDValue); dv.Status != ua.StatusGood || dv.Value.Value() != 1.0 {
		t.Errorf("Late = %v %s", dv.Value, dv.Status)
	}
}

func TestNewRefusesNothingButABadEndpoint(t *testing.T) {
	rt, _ := newRuntime(t)
	if _, err := New(nil, Options{}); err == nil {
		t.Error("nil runtime accepted")
	}
	if _, err := New(rt, Options{Endpoint: "nope"}); err == nil {
		t.Error("bad endpoint accepted")
	}
	s, err := New(rt, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.opts.Endpoint != DefaultEndpoint || s.opts.Sweep != DefaultSweep || s.opts.Name != "nautilus" || s.host != "localhost" || s.port != 4840 {
		t.Errorf("defaults %+v %s %d", s.opts, s.host, s.port)
	}
	if st := s.Status(); st.Listening || st.Endpoint != DefaultEndpoint {
		t.Errorf("status before start %+v", st)
	}
	if s.URL() != "" || s.NamespaceIndex() != 0 {
		t.Errorf("url %q ns %d before start", s.URL(), s.NamespaceIndex())
	}
}
