package server

// The services black-box F-31 found answering Bad_ServiceUnsupported
// (#431): ModifyMonitoredItems, SetPublishingMode, TransferSubscriptions,
// TranslateBrowsePathsToNodeIds and Call. Each test drives the service
// through a real gopcua client, raw where gopcua's client keeps
// bookkeeping of its own, the way a foreign client sends it.

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// send is one raw request and its response, or the service fault's code.
func send[T ua.Response](t *testing.T, c *opcua.Client, req ua.Request) (T, error) {
	t.Helper()
	var out T
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.Send(ctx, req, func(r ua.Response) error {
		if v, ok := r.(T); ok {
			out = v
		}
		return nil
	})
	return out, err
}

// moving sets name to a new value every few milliseconds until the test
// ends.
func moving(t *testing.T, set func(int64)) {
	t.Helper()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for i := int64(1); ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(3 * time.Millisecond):
				set(i)
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
}

// rawSubscription creates a subscription and one Reporting item on nid
// with raw requests, so gopcua's client runs no publish loop for it, and
// returns the subscription and item ids.
func rawSubscription(t *testing.T, c *opcua.Client, interval float64, enabled bool, nid *ua.NodeID, handle uint32) (uint32, uint32) {
	t.Helper()
	cs, err := send[*ua.CreateSubscriptionResponse](t, c, &ua.CreateSubscriptionRequest{RequestHeader: &ua.RequestHeader{}, RequestedPublishingInterval: interval, RequestedLifetimeCount: 300, RequestedMaxKeepAliveCount: 3, PublishingEnabled: enabled})
	if err != nil || cs == nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	cm, err := send[*ua.CreateMonitoredItemsResponse](t, c, &ua.CreateMonitoredItemsRequest{RequestHeader: &ua.RequestHeader{}, SubscriptionID: cs.SubscriptionID, TimestampsToReturn: ua.TimestampsToReturnBoth,
		ItemsToCreate: []*ua.MonitoredItemCreateRequest{opcua.NewMonitoredItemCreateRequestWithDefaults(nid, ua.AttributeIDValue, handle)}})
	if err != nil || cm == nil || cm.Results[0].StatusCode != ua.StatusGood {
		t.Fatalf("CreateMonitoredItems: %v %v", err, cm)
	}
	return cs.SubscriptionID, cm.Results[0].MonitoredItemID
}

// publish sends one Publish and returns what came back for subscription
// sub within d: the data changes by client handle, any status change, or
// nothing (a keep-alive, or no answer in time, reported by ok).
func publish(t *testing.T, c *opcua.Client, sub uint32, d time.Duration) (values map[uint32][]*ua.DataValue, status *ua.StatusChangeNotification, ok bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var resp *ua.PublishResponse
	err := c.Send(ctx, &ua.PublishRequest{RequestHeader: &ua.RequestHeader{TimeoutHint: uint32(d / time.Millisecond)}, SubscriptionAcknowledgements: []*ua.SubscriptionAcknowledgement{}}, func(r ua.Response) error {
		resp, _ = r.(*ua.PublishResponse)
		return nil
	})
	if err != nil || resp == nil {
		return nil, nil, false
	}
	if resp.SubscriptionID != sub {
		t.Fatalf("a publish for subscription %d, want %d", resp.SubscriptionID, sub)
	}
	values = map[uint32][]*ua.DataValue{}
	if resp.NotificationMessage != nil {
		for _, eo := range resp.NotificationMessage.NotificationData {
			switch n := eo.Value.(type) {
			case *ua.DataChangeNotification:
				for _, m := range n.MonitoredItems {
					values[m.ClientHandle] = append(values[m.ClientHandle], m.Value)
				}
			case *ua.StatusChangeNotification:
				status = n
			}
		}
	}
	return values, status, true
}

// F-31: ModifyMonitoredItems changes an item's sampling interval, which
// the sweep then samples it at, and its client handle, which the next
// notification carries; it answers with the values granted, revised as
// CreateMonitoredItems revises them. An id that is not an item of the
// subscription is refused in its own result.
func TestModifyMonitoredItemsResamplesAndRehandles(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	moving(t, func(i int64) { rt.Tags().Set("Count", i) })

	notify := make(chan *opcua.PublishNotificationData, 256)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 20 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(ctx)
	created, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth,
		opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Count"), ua.AttributeIDValue, 1),
		opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Level"), ua.AttributeIDValue, 5))
	if err != nil {
		t.Fatal(err)
	}
	countID, levelID := created.Results[0].MonitoredItemID, created.Results[1].MonitoredItemID
	window := 1500 * time.Millisecond
	count(notify, 200*time.Millisecond)
	fast := count(notify, window)[1]

	modify := func(items ...*ua.MonitoredItemModifyRequest) []*ua.MonitoredItemModifyResult {
		t.Helper()
		resp, err := send[*ua.ModifyMonitoredItemsResponse](t, c, &ua.ModifyMonitoredItemsRequest{RequestHeader: &ua.RequestHeader{}, SubscriptionID: sub.SubscriptionID, TimestampsToReturn: ua.TimestampsToReturnBoth, ItemsToModify: items})
		if err != nil || resp == nil || len(resp.Results) != len(items) {
			t.Fatalf("ModifyMonitoredItems: %v %v", err, resp)
		}
		return resp.Results
	}
	params := func(handle uint32, sampling float64) *ua.MonitoringParameters {
		return &ua.MonitoringParameters{ClientHandle: handle, SamplingInterval: sampling, QueueSize: 10, DiscardOldest: true}
	}
	res := modify(
		&ua.MonitoredItemModifyRequest{MonitoredItemID: countID, RequestedParameters: params(9, 285)},
		&ua.MonitoredItemModifyRequest{MonitoredItemID: 424242, RequestedParameters: params(3, 0)},
	)
	if r := res[0]; r.StatusCode != ua.StatusGood || r.RevisedSamplingInterval != 280 || r.RevisedQueueSize != 1 {
		t.Errorf("modified item: %s, sampling %v, queue %d; want Good, 280 (the nearest multiple of the 20 ms sweep), 1", r.StatusCode, r.RevisedSamplingInterval, r.RevisedQueueSize)
	}
	if res[1].StatusCode != ua.StatusBadMonitoredItemIDInvalid {
		t.Errorf("an id that is no item: %s", res[1].StatusCode)
	}
	count(notify, 300*time.Millisecond) // what was on its way
	got := count(notify, window)
	t.Logf("notifications in %v: %d sampled at 20 ms; after the change, %d under the old handle and %d under the new at 280 ms", window, fast, got[1], got[9])
	if got[1] != 0 {
		t.Errorf("%d notifications under the old client handle after the change", got[1])
	}
	if got[9] == 0 || got[9] > 8 {
		t.Errorf("%d notifications in %v at 280 ms under the new handle, want 1..8 (5 or 6 samples, and slack)", got[9], window)
	}
	if fast < 3*got[9] || fast < 20 {
		t.Errorf("at 20 ms %d notifications; at 280 ms %d; want the first well above the second", fast, got[9])
	}

	// An idle tag given a new handle hears its value under it once.
	if r := modify(&ua.MonitoredItemModifyRequest{MonitoredItemID: levelID, RequestedParameters: params(6, 0)}); r[0].StatusCode != ua.StatusGood || r[0].RevisedSamplingInterval != 20 {
		t.Fatalf("modify Level: %s %v", r[0].StatusCode, r[0].RevisedSamplingInterval)
	}
	if got := count(notify, 400*time.Millisecond); got[6] != 1 || got[5] != 0 {
		t.Errorf("Level after its handle changed: %d under the new, %d under the old, want 1 and 0", got[6], got[5])
	}

	// Another session's subscription, or none, is refused as a whole.
	if _, err := send[*ua.ModifyMonitoredItemsResponse](t, c, &ua.ModifyMonitoredItemsRequest{RequestHeader: &ua.RequestHeader{}, SubscriptionID: sub.SubscriptionID + 100, ItemsToModify: []*ua.MonitoredItemModifyRequest{{MonitoredItemID: countID, RequestedParameters: params(1, 0)}}}); !errors.Is(err, ua.StatusBadSubscriptionIDInvalid) {
		t.Errorf("a subscription that is not there: %v", err)
	}
	if _, err := send[*ua.ModifyMonitoredItemsResponse](t, c, &ua.ModifyMonitoredItemsRequest{RequestHeader: &ua.RequestHeader{}, SubscriptionID: sub.SubscriptionID}); !errors.Is(err, ua.StatusBadNothingToDo) {
		t.Errorf("no items: %v", err)
	}
}

// F-31: SetPublishingMode(false) stops a subscription publishing — keep-
// alives only — and (true) starts it again with what its items hold; a
// subscription created with PublishingEnabled false is silent until it is
// enabled. gopcua answered Bad_ServiceUnsupported and ignored
// CreateSubscription's flag.
func TestSetPublishingModeHoldsAndResumes(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	moving(t, func(i int64) { rt.Tags().Set("Count", i) })

	setMode := func(enabled bool, ids ...uint32) []ua.StatusCode {
		t.Helper()
		resp, err := send[*ua.SetPublishingModeResponse](t, c, &ua.SetPublishingModeRequest{RequestHeader: &ua.RequestHeader{}, PublishingEnabled: enabled, SubscriptionIDs: ids})
		if err != nil || resp == nil {
			t.Fatalf("SetPublishingMode(%v): %v", enabled, err)
		}
		return resp.Results
	}

	subID, _ := rawSubscription(t, c, 40, true, s.NodeID("Count"), 1)
	if v, _, ok := publish(t, c, subID, 2*time.Second); !ok || len(v[1]) == 0 {
		t.Fatalf("enabled: no data change (%v, %v)", ok, v)
	}
	// Some cycles go by with no Publish outstanding, so the subscription
	// is waiting with values to send when it is disabled: they must wait
	// with it.
	time.Sleep(150 * time.Millisecond)
	if r := setMode(false, subID, 424242); r[0] != ua.StatusGood || r[1] != ua.StatusBadSubscriptionIDInvalid {
		t.Fatalf("SetPublishingMode(false, sub, bogus) = %v", r)
	}
	// Disabled: what comes is keep-alives (3 × 40 ms), never a value,
	// though Count moves all along.
	for i := range 4 {
		v, _, ok := publish(t, c, subID, 2*time.Second)
		if !ok {
			t.Fatalf("disabled publish %d: no keep-alive", i)
		}
		if len(v) != 0 {
			t.Fatalf("disabled publish %d carried data: %v", i, v)
		}
	}
	if r := setMode(true, subID); r[0] != ua.StatusGood {
		t.Fatalf("SetPublishingMode(true) = %v", r)
	}
	if v, _, ok := publish(t, c, subID, 2*time.Second); !ok || len(v[1]) == 0 {
		t.Fatalf("enabled again: no data change (%v, %v)", ok, v)
	}

	// Created disabled.
	if _, err := send[*ua.DeleteSubscriptionsResponse](t, c, &ua.DeleteSubscriptionsRequest{RequestHeader: &ua.RequestHeader{}, SubscriptionIDs: []uint32{subID}}); err != nil {
		t.Fatal(err)
	}
	quiet, _ := rawSubscription(t, c, 40, false, s.NodeID("Count"), 2)
	for i := range 2 {
		if v, _, ok := publish(t, c, quiet, 2*time.Second); !ok || len(v) != 0 {
			t.Fatalf("created disabled, publish %d: %v %v, want a keep-alive", i, ok, v)
		}
	}
	setMode(true, quiet)
	if v, _, ok := publish(t, c, quiet, 2*time.Second); !ok || len(v[2]) == 0 {
		t.Fatalf("created disabled, then enabled: no data change (%v, %v)", ok, v)
	}
	if _, err := send[*ua.SetPublishingModeResponse](t, c, &ua.SetPublishingModeRequest{RequestHeader: &ua.RequestHeader{}, PublishingEnabled: true}); !errors.Is(err, ua.StatusBadNothingToDo) {
		t.Errorf("no ids: %v", err)
	}
}

// F-31: TransferSubscriptions moves a subscription to another session of
// the same client application — a signed channel and the same
// ApplicationUri, Part 4 §5.14.7's rule for an anonymous user — which then
// publishes it, with the current values when asked; the old session hears
// Good_SubscriptionTransferred and can no longer use it, and closing it
// leaves the subscription alone. A session on None, or another
// application, is refused (Bad_UserAccessDenied); a subscription the
// session owns already is Bad_NothingToDo.
func TestTransferSubscriptionsToTheSameApplication(t *testing.T) {
	f := startWritable(t)
	old := f.connect(t)
	subID, _ := rawSubscription(t, old, 40, true, ua.NewStringNodeID(1, "Speed"), 7)
	if v, _, ok := publish(t, old, subID, 2*time.Second); !ok || len(v[7]) == 0 {
		t.Fatalf("the first session's first publish: %v %v", ok, v)
	}

	transfer := func(c *opcua.Client, initial bool, ids ...uint32) []*ua.TransferResult {
		t.Helper()
		resp, err := send[*ua.TransferSubscriptionsResponse](t, c, &ua.TransferSubscriptionsRequest{RequestHeader: &ua.RequestHeader{}, SubscriptionIDs: ids, SendInitialValues: initial})
		if err != nil || resp == nil || len(resp.Results) != len(ids) {
			t.Fatalf("TransferSubscriptions: %v %v", err, resp)
		}
		return resp.Results
	}

	// Who may not take it.
	if r := transfer(f.anon, true, subID); r[0].StatusCode != ua.StatusBadUserAccessDenied {
		t.Errorf("an anonymous session on None took it: %s", r[0].StatusCode)
	}
	otherDer, otherKey := clientCert(t, "urn:test:historian", time.Now().Add(-time.Hour), 24*time.Hour)
	d := filepath.Join(f.dir, "trusted", "certs")
	if err := os.WriteFile(filepath.Join(d, "historian.der"), otherDer, 0o644); err != nil {
		t.Fatal(err)
	}
	other, err := secureConnect(f.s, endpointFor(t, f.s, ua.SecurityPolicyURIBasic256Sha256, ua.MessageSecurityModeSignAndEncrypt), otherDer, otherKey, "urn:test:historian")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeClient(other) })
	if r := transfer(other, true, subID); r[0].StatusCode != ua.StatusBadUserAccessDenied {
		t.Errorf("another application took it: %s", r[0].StatusCode)
	}
	if r := transfer(old, true, subID); r[0].StatusCode != ua.StatusBadNothingToDo {
		t.Errorf("transferred to the session that has it: %s", r[0].StatusCode)
	}

	// The same application, on a new session, takes it.
	f.rt.Tags().Set("Speed", 12.5)
	time.Sleep(100 * time.Millisecond)
	publish(t, old, subID, 2*time.Second) // the change, to the old session
	next := f.connect(t)
	r := transfer(next, true, subID, 424242)
	if r[0].StatusCode != ua.StatusGood || r[1].StatusCode != ua.StatusBadSubscriptionIDInvalid {
		t.Fatalf("transfer(sub, bogus) = %s, %s", r[0].StatusCode, r[1].StatusCode)
	}
	if len(r[0].AvailableSequenceNumbers) != 0 {
		t.Errorf("available sequence numbers %v; nothing is kept for retransmission", r[0].AvailableSequenceNumbers)
	}
	// sendInitialValues: the new session hears Speed's value, unchanged.
	v, _, ok := publish(t, next, subID, 2*time.Second)
	if !ok || len(v[7]) == 0 || v[7][0].Value == nil || v[7][0].Value.Value() != float64(12.5) {
		t.Fatalf("the new session's first publish: %v %v, want Speed = 12.5", ok, v)
	}
	// And changes after it.
	f.rt.Tags().Set("Speed", 20.0)
	if v, _, ok := publish(t, next, subID, 2*time.Second); !ok || len(v[7]) == 0 || v[7][len(v[7])-1].Value.Value() != float64(20) {
		t.Fatalf("the new session after a change: %v %v", ok, v)
	}
	// The old session is told, and the subscription is no longer its own.
	if _, st, ok := publish(t, old, subID, 2*time.Second); !ok || st == nil || st.Status != ua.StatusGoodSubscriptionTransferred {
		t.Errorf("the old session's next publish: %v %v, want Good_SubscriptionTransferred", ok, st)
	}
	if resp, err := send[*ua.SetPublishingModeResponse](t, old, &ua.SetPublishingModeRequest{RequestHeader: &ua.RequestHeader{}, PublishingEnabled: true, SubscriptionIDs: []uint32{subID}}); err != nil || resp.Results[0] != ua.StatusBadSubscriptionIDInvalid {
		t.Errorf("the old session still uses it: %v %v", err, resp)
	}
	closeClient(old) // deleteSubscriptions: its own, which is none now
	time.Sleep(100 * time.Millisecond)
	if st := f.s.Status(); st.Subscriptions != 1 {
		t.Errorf("%d subscriptions after the old session closed, want the transferred one", st.Subscriptions)
	}
	f.rt.Tags().Set("Speed", 30.0)
	if v, _, ok := publish(t, next, subID, 2*time.Second); !ok || len(v[7]) == 0 {
		t.Fatalf("the new session after the old one closed: %v %v", ok, v)
	}
}

// The usual hand-over: a session closes with deleteSubscriptions false
// and the client's next session takes the subscription. Without
// sendInitialValues an idle tag says nothing until it changes.
func TestTransferSubscriptionsAfterCloseSession(t *testing.T) {
	f := startWritable(t)
	old := f.connect(t)
	subID, _ := rawSubscription(t, old, 40, true, ua.NewStringNodeID(1, "Speed"), 7)
	publish(t, old, subID, 2*time.Second)
	if _, err := send[*ua.CloseSessionResponse](t, old, &ua.CloseSessionRequest{RequestHeader: &ua.RequestHeader{}, DeleteSubscriptions: false}); err != nil {
		t.Fatal(err)
	}
	next := f.connect(t)
	resp, err := send[*ua.TransferSubscriptionsResponse](t, next, &ua.TransferSubscriptionsRequest{RequestHeader: &ua.RequestHeader{}, SubscriptionIDs: []uint32{subID}})
	if err != nil || resp.Results[0].StatusCode != ua.StatusGood {
		t.Fatalf("transfer after CloseSession: %v %v", err, resp)
	}
	if v, _, ok := publish(t, next, subID, 2*time.Second); !ok || len(v) != 0 {
		t.Fatalf("without sendInitialValues: %v %v, want a keep-alive", ok, v)
	}
	f.rt.Tags().Set("Speed", 4.0)
	if v, _, ok := publish(t, next, subID, 2*time.Second); !ok || len(v[7]) == 0 {
		t.Fatalf("a change after the transfer: %v %v", ok, v)
	}
	if _, err := send[*ua.TransferSubscriptionsResponse](t, next, &ua.TransferSubscriptionsRequest{RequestHeader: &ua.RequestHeader{}}); !errors.Is(err, ua.StatusBadNothingToDo) {
		t.Errorf("no ids: %v", err)
	}
}

// translate sends one path and returns its result.
func translate(t *testing.T, c *opcua.Client, start *ua.NodeID, elems ...*ua.RelativePathElement) *ua.BrowsePathResult {
	t.Helper()
	resp, err := send[*ua.TranslateBrowsePathsToNodeIDsResponse](t, c, &ua.TranslateBrowsePathsToNodeIDsRequest{RequestHeader: &ua.RequestHeader{}, BrowsePaths: []*ua.BrowsePath{{StartingNode: start, RelativePath: &ua.RelativePath{Elements: elems}}}})
	if err != nil || resp == nil || len(resp.Results) != 1 {
		t.Fatalf("TranslateBrowsePathsToNodeIds: %v %v", err, resp)
	}
	return resp.Results[0]
}

// hier is a forward hierarchical step to name, as asyncua's get_child
// sends it.
func hier(ns uint16, name string) *ua.RelativePathElement {
	return &ua.RelativePathElement{ReferenceTypeID: ua.NewNumericNodeID(0, id.HierarchicalReferences), IncludeSubtypes: true, TargetName: &ua.QualifiedName{NamespaceIndex: ns, Name: name}}
}

// F-31: TranslateBrowsePathsToNodeIds resolves a path of BrowseNames over
// the references Browse lists, in the project namespace and in ns=0, and
// says why a path that does not resolve does not. gopcua answered
// Bad_ServiceUnsupported, and asyncua's get_child failed.
func TestTranslateBrowsePathsFollowsBrowse(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	objects := ua.NewNumericNodeID(0, id.ObjectsFolder)
	target := func(r *ua.BrowsePathResult) []string {
		var out []string
		for _, tg := range r.Targets {
			if tg.RemainingPathIndex != math.MaxUint32 {
				t.Errorf("target %s has RemainingPathIndex %d", tg.TargetID, tg.RemainingPathIndex)
			}
			out = append(out, tg.TargetID.NodeID.String())
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		start *ua.NodeID
		path  []*ua.RelativePathElement
		want  string
	}{
		{"a tag", objects, []*ua.RelativePathElement{hier(1, "lift-station"), hier(1, "Level")}, s.NodeID("Level").String()},
		{"a UDT member", objects, []*ua.RelativePathElement{hier(1, "lift-station"), hier(1, "P101"), hier(1, "Drive"), hier(1, "Speed")}, s.NodeID("P101.Drive.Speed").String()},
		{"an array element", s.NodeID("Pumps"), []*ua.RelativePathElement{hier(1, "Pumps[2]"), hier(1, "Drive")}, s.NodeID("Pumps[2].Drive").String()},
		{"ns=0", objects, []*ua.RelativePathElement{hier(0, "Server"), hier(0, "ServerStatus"), hier(0, "State")}, ua.NewNumericNodeID(0, id.Server_ServerStatus_State).String()},
		{"inverse", s.NodeID("Level"), []*ua.RelativePathElement{{ReferenceTypeID: ua.NewNumericNodeID(0, id.Organizes), IsInverse: true, TargetName: &ua.QualifiedName{NamespaceIndex: 1, Name: "lift-station"}}}, ua.NewNumericNodeID(1, 1).String()},
		// A concrete reference type without subtypes is matched exactly.
		{"Organizes, no subtypes, ns=0", objects, []*ua.RelativePathElement{{ReferenceTypeID: ua.NewNumericNodeID(0, id.Organizes), TargetName: &ua.QualifiedName{Name: "Server"}}}, ua.NewNumericNodeID(0, id.Server).String()},
	} {
		r := translate(t, c, tc.start, tc.path...)
		if r.StatusCode != ua.StatusGood {
			t.Errorf("%s: %s", tc.name, r.StatusCode)
			continue
		}
		if got := target(r); len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s: targets %v, want %s", tc.name, got, tc.want)
		}
	}
	// The project folder's node id is what Browse says it is.
	if got := browse(t, c, objects)["lift-station"]; got == nil || got.NodeID.NodeID.String() != ua.NewNumericNodeID(1, 1).String() {
		t.Fatalf("the project folder is %v", got)
	}

	// A last element with no name: every target of the reference.
	if r := translate(t, c, s.NodeID("P101"), hier(1, "Drive"), &ua.RelativePathElement{ReferenceTypeID: ua.NewNumericNodeID(0, id.HasComponent), TargetName: &ua.QualifiedName{}}); r.StatusCode != ua.StatusGood || len(r.Targets) != 2 {
		t.Errorf("P101/Drive/*: %s %v, want Speed and Amps", r.StatusCode, target(r))
	}
	for _, tc := range []struct {
		name  string
		start *ua.NodeID
		path  []*ua.RelativePathElement
		want  ua.StatusCode
	}{
		{"names are compared as they are", objects, []*ua.RelativePathElement{hier(1, "lift-station"), hier(1, "level")}, ua.StatusBadNoMatch},
		{"a name in another namespace", objects, []*ua.RelativePathElement{hier(1, "lift-station"), hier(0, "Level")}, ua.StatusBadNoMatch},
		{"no such tag", objects, []*ua.RelativePathElement{hier(1, "lift-station"), hier(1, "Nope")}, ua.StatusBadNoMatch},
		{"no such start", ua.NewStringNodeID(1, "Nope"), []*ua.RelativePathElement{hier(1, "x")}, ua.StatusBadNodeIDUnknown},
		{"no elements", objects, nil, ua.StatusBadNothingToDo},
		{"an unnamed step before the last", objects, []*ua.RelativePathElement{{ReferenceTypeID: ua.NewNumericNodeID(0, id.Organizes), TargetName: &ua.QualifiedName{}}, hier(1, "Level")}, ua.StatusBadBrowseNameInvalid},
	} {
		if r := translate(t, c, tc.start, tc.path...); r.StatusCode != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, r.StatusCode, tc.want)
		}
	}
}

// call sends one method call and returns its result.
func call(t *testing.T, c *opcua.Client, object, method uint32, args ...*ua.Variant) *ua.CallMethodResult {
	t.Helper()
	return callNode(t, c, ua.NewNumericNodeID(0, object), ua.NewNumericNodeID(0, method), args...)
}

func callNode(t *testing.T, c *opcua.Client, object, method *ua.NodeID, args ...*ua.Variant) *ua.CallMethodResult {
	t.Helper()
	resp, err := send[*ua.CallResponse](t, c, &ua.CallRequest{RequestHeader: &ua.RequestHeader{}, MethodsToCall: []*ua.CallMethodRequest{{ObjectID: object, MethodID: method, InputArguments: args}}})
	if err != nil || resp == nil || len(resp.Results) != 1 {
		t.Fatalf("Call: %v %v", err, resp)
	}
	return resp.Results[0]
}

// F-31: Call answers each method: the Server object's GetMonitoredItems
// and ResendData run, its other methods are Bad_NotExecutable (and their
// Executable reads false), a method of some other object is
// Bad_MethodInvalid, and an object that is not one is Bad_NodeIdUnknown or
// Bad_NodeIdInvalid. gopcua refused the whole service.
func TestCallAnswersEachMethod(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	subID, itemID := rawSubscription(t, c, 40, true, s.NodeID("Level"), 33)
	u32 := func(v uint32) *ua.Variant { return ua.MustVariant(v) }

	r := call(t, c, id.Server, id.Server_GetMonitoredItems, u32(subID))
	if r.StatusCode != ua.StatusGood || len(r.OutputArguments) != 2 {
		t.Fatalf("GetMonitoredItems: %s %v", r.StatusCode, r.OutputArguments)
	}
	if sh, ch := r.OutputArguments[0].Value(), r.OutputArguments[1].Value(); !equalU32(sh, []uint32{itemID}) || !equalU32(ch, []uint32{33}) {
		t.Errorf("GetMonitoredItems = %v, %v; want [%d], [33]", sh, ch, itemID)
	}
	// Ids in their compact four-byte form, as asyncua sends them.
	if r := callNode(t, c, ua.NewFourByteNodeID(0, id.Server), ua.NewFourByteNodeID(0, id.Server_GetMonitoredItems), u32(subID)); r.StatusCode != ua.StatusGood {
		t.Errorf("GetMonitoredItems by four-byte ids: %s", r.StatusCode)
	}
	// By the ServerType declaration it instantiates, too.
	if r := call(t, c, id.Server, id.ServerType_GetMonitoredItems, u32(subID)); r.StatusCode != ua.StatusGood {
		t.Errorf("GetMonitoredItems by its declaration: %s", r.StatusCode)
	}

	// ResendData: the idle Level is sent again.
	if v, _, ok := publish(t, c, subID, 2*time.Second); !ok || len(v[33]) == 0 {
		t.Fatalf("the first value: %v %v", ok, v)
	}
	if v, _, ok := publish(t, c, subID, 2*time.Second); !ok || len(v) != 0 {
		t.Fatalf("idle: %v %v, want a keep-alive", ok, v)
	}
	if r := call(t, c, id.Server, id.Server_ResendData, u32(subID)); r.StatusCode != ua.StatusGood {
		t.Fatalf("ResendData: %s", r.StatusCode)
	}
	if v, _, ok := publish(t, c, subID, 2*time.Second); !ok || len(v[33]) != 1 {
		t.Errorf("after ResendData: %v %v, want Level once", ok, v)
	}

	// Another session's subscription.
	other, err := opcua.NewClient(s.URL(), opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeClient(other) })
	theirs, _ := rawSubscription(t, other, 100, true, s.NodeID("Count"), 1)

	for _, tc := range []struct {
		name           string
		object, method *ua.NodeID
		args           []*ua.Variant
		want           ua.StatusCode
	}{
		{"another session's subscription", ua.NewNumericNodeID(0, id.Server), ua.NewNumericNodeID(0, id.Server_GetMonitoredItems), []*ua.Variant{u32(theirs)}, ua.StatusBadUserAccessDenied},
		{"ResendData of another session's", ua.NewNumericNodeID(0, id.Server), ua.NewNumericNodeID(0, id.Server_ResendData), []*ua.Variant{u32(theirs)}, ua.StatusBadUserAccessDenied},
		{"no such subscription", ua.NewNumericNodeID(0, id.Server), ua.NewNumericNodeID(0, id.Server_GetMonitoredItems), []*ua.Variant{u32(424242)}, ua.StatusBadSubscriptionIDInvalid},
		{"no argument", ua.NewNumericNodeID(0, id.Server), ua.NewNumericNodeID(0, id.Server_GetMonitoredItems), nil, ua.StatusBadArgumentsMissing},
		{"two arguments", ua.NewNumericNodeID(0, id.Server), ua.NewNumericNodeID(0, id.Server_GetMonitoredItems), []*ua.Variant{u32(subID), u32(subID)}, ua.StatusBadTooManyArguments},
		{"a string argument", ua.NewNumericNodeID(0, id.Server), ua.NewNumericNodeID(0, id.Server_GetMonitoredItems), []*ua.Variant{ua.MustVariant("1")}, ua.StatusBadInvalidArgument},
		{"a method not implemented", ua.NewNumericNodeID(0, id.Server), ua.NewNumericNodeID(0, id.Server_SetSubscriptionDurable), []*ua.Variant{u32(subID), u32(1)}, ua.StatusBadNotExecutable},
		{"a method of another object", ua.NewNumericNodeID(0, id.ObjectsFolder), ua.NewNumericNodeID(0, id.Server_GetMonitoredItems), []*ua.Variant{u32(subID)}, ua.StatusBadMethodInvalid},
		{"a method id that is no method", ua.NewNumericNodeID(0, id.Server), ua.NewNumericNodeID(0, id.Server_ServerStatus), nil, ua.StatusBadMethodInvalid},
		{"the project folder", ua.NewNumericNodeID(1, 1), ua.NewNumericNodeID(0, id.Server_GetMonitoredItems), []*ua.Variant{u32(subID)}, ua.StatusBadMethodInvalid},
		{"no such object", ua.NewStringNodeID(1, "Nope"), ua.NewNumericNodeID(0, id.Server_GetMonitoredItems), nil, ua.StatusBadNodeIDUnknown},
		{"a variable", s.NodeID("Level"), ua.NewNumericNodeID(0, id.Server_GetMonitoredItems), nil, ua.StatusBadNodeIDInvalid},
	} {
		r := callNode(t, c, tc.object, tc.method, tc.args...)
		if r.StatusCode != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, r.StatusCode, tc.want)
		}
		if tc.want == ua.StatusBadInvalidArgument && (len(r.InputArgumentResults) != 1 || r.InputArgumentResults[0] != ua.StatusBadTypeMismatch) {
			t.Errorf("%s: input argument results %v, want [Bad_TypeMismatch]", tc.name, r.InputArgumentResults)
		}
	}
	if _, err := send[*ua.CallResponse](t, c, &ua.CallRequest{RequestHeader: &ua.RequestHeader{}}); !errors.Is(err, ua.StatusBadNothingToDo) {
		t.Errorf("no calls: %v", err)
	}

	// Executable says which run.
	for nid, want := range map[uint32]bool{id.Server_GetMonitoredItems: true, id.Server_ResendData: true, id.Server_SetSubscriptionDurable: false, id.Server_RequestServerStateChange: false} {
		for _, attr := range []ua.AttributeID{ua.AttributeIDExecutable, ua.AttributeIDUserExecutable} {
			if dv := read(t, c, ua.NewNumericNodeID(0, nid), attr); dv.Status != ua.StatusGood || dv.Value == nil || dv.Value.Value() != want {
				t.Errorf("i=%d attribute %d: %s %v, want %v", nid, attr, dv.Status, dv.Value, want)
			}
		}
	}
}

func equalU32(v any, want []uint32) bool {
	got, ok := v.([]uint32)
	if !ok || len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
