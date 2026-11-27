package server

import (
	"context"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	uaid "github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/internal/gopcuatest"
)

// Reads under an auth: block (#1131). The rule is the HTTP API's: `read`
// is a whole-controller permission, so a session reads every tag or none.
// A user is judged as themselves, a mapped certificate as its name, and
// every other session (the None endpoint, a certificate the block does not
// map) as anonymous: what auth.anonymous grants.

// noneClient is an anonymous client on the None endpoint.
func noneClient(t *testing.T, s *Server) *opcua.Client {
	t.Helper()
	c, err := opcua.NewClient(s.URL(), opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := gopcuatest.Connect(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeClient(c) })
	return c
}

func readStatus(t *testing.T, c *opcua.Client, nid *ua.NodeID, attr ua.AttributeID) ua.StatusCode {
	t.Helper()
	resp, err := c.Read(context.Background(), &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{NodeID: nid, AttributeID: attr}}})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Results[0].Status
}

func browseRefs(t *testing.T, c *opcua.Client, nid *ua.NodeID) (ua.StatusCode, []*ua.ReferenceDescription) {
	t.Helper()
	resp, err := c.Browse(context.Background(), &ua.BrowseRequest{
		RequestedMaxReferencesPerNode: 0,
		NodesToBrowse:                 []*ua.BrowseDescription{{NodeID: nid, BrowseDirection: ua.BrowseDirectionForward, ReferenceTypeID: ua.NewNumericNodeID(0, uaid.References), IncludeSubtypes: true, ResultMask: uint32(ua.BrowseResultMaskAll)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Results[0].StatusCode, resp.Results[0].References
}

func TestReadNeedsReadUnderAuth(t *testing.T) {
	id := newFakeIdentity()
	id.anonNone.Store(true) // auth.anonymous: none
	f := startUsers(t, id, Options{})
	speed := f.s.NodeID("Speed")

	// Anonymous, on the None endpoint and as an unmapped certificate: nothing,
	// for every attribute (a node's name is not given away either).
	for name, c := range map[string]*opcua.Client{"none endpoint": noneClient(t, f.s), "unmapped certificate": mustConnect(t, f, "", "")} {
		for _, attr := range []ua.AttributeID{ua.AttributeIDValue, ua.AttributeIDBrowseName, ua.AttributeIDUserAccessLevel} {
			if got := readStatus(t, c, speed, attr); got != ua.StatusBadUserAccessDenied {
				t.Errorf("%s reads attribute %v: %v, want BadUserAccessDenied", name, attr, got)
			}
		}
		// Not the project's: the server's own nodes carry no tag data.
		if got := readStatus(t, c, ua.NewNumericNodeID(0, id2ServerStatus), ua.AttributeIDValue); got != ua.StatusGood {
			t.Errorf("%s reads ServerStatus: %v, want Good", name, got)
		}
	}

	// A user reads; a user the block gives no read does not.
	op := mustConnect(t, f, "op", "op-pass")
	if got := readStatus(t, op, speed, ua.AttributeIDValue); got != ua.StatusGood {
		t.Errorf("a user reads Speed: %v", got)
	}
	id.noRead.Store("user:eng", true)
	eng := mustConnect(t, f, "eng", "eng-pass")
	if got := readStatus(t, eng, speed, ua.AttributeIDValue); got != ua.StatusBadUserAccessDenied {
		t.Errorf("a user with no read reads Speed: %v, want BadUserAccessDenied", got)
	}

	// A mapped certificate reads as its name.
	mid := newFakeIdentity()
	mid.anonNone.Store(true)
	mf := startUsers(t, mid, Options{})
	mid.certs[Thumbprint(mf.der)] = "scada"
	mc := mustConnect(t, mf, "", "")
	if got := readStatus(t, mc, mf.s.NodeID("Speed"), ua.AttributeIDValue); got != ua.StatusGood {
		t.Errorf("a mapped certificate reads Speed: %v", got)
	}
	mid.noRead.Store("opcua-cert:scada", true)
	if got := readStatus(t, mc, mf.s.NodeID("Speed"), ua.AttributeIDValue); got != ua.StatusBadUserAccessDenied {
		t.Errorf("a mapped certificate with no read reads Speed: %v, want BadUserAccessDenied", got)
	}

	// anonymous: viewer keeps today's behaviour (decision 4): the None endpoint reads.
	vid := newFakeIdentity()
	vf := startUsers(t, vid, Options{})
	if got := readStatus(t, noneClient(t, vf.s), vf.s.NodeID("Speed"), ua.AttributeIDValue); got != ua.StatusGood {
		t.Errorf("anonymous: viewer, the None endpoint reads Speed: %v", got)
	}
}

const id2ServerStatus = 2256 // i=2256 Server_ServerStatus

func mustConnect(t *testing.T, f *userFixture, name, pw string) *opcua.Client {
	t.Helper()
	c, err := f.connectUser(t, name, pw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A revoked user, and a user removed mid-session, read nothing at their
// next request; so does a certificate no longer trusted (writes already
// said so, TestRevokedUserStopsWriting).
func TestRevokedSessionStopsReading(t *testing.T) {
	id := newFakeIdentity()
	f := startUsers(t, id, Options{})
	op := mustConnect(t, f, "op", "op-pass")
	if got := readStatus(t, op, f.s.NodeID("Speed"), ua.AttributeIDValue); got != ua.StatusGood {
		t.Fatalf("before: %v", got)
	}
	id.revoked.Store(true)
	if got := readStatus(t, op, f.s.NodeID("Speed"), ua.AttributeIDValue); got != ua.StatusBadUserAccessDenied {
		t.Errorf("after the revocation: %v, want BadUserAccessDenied", got)
	}
}

func TestBrowseHidesTheProjectFromASessionWithNoRead(t *testing.T) {
	id := newFakeIdentity()
	id.anonNone.Store(true)
	f := startUsers(t, id, Options{})
	folder := ua.NewNumericNodeID(f.s.NamespaceIndex(), folderID)
	objects := ua.NewNumericNodeID(0, 85)
	anon := noneClient(t, f.s)
	op := mustConnect(t, f, "op", "op-pass")

	// The folder itself: refused. Under Objects: no reference to it.
	if code, refs := browseRefs(t, anon, folder); code != ua.StatusBadUserAccessDenied || len(refs) != 0 {
		t.Errorf("anonymous browses the project folder: %v, %d refs", code, len(refs))
	}
	if code, refs := browseRefs(t, anon, f.s.NodeID("Speed")); code != ua.StatusBadUserAccessDenied || len(refs) != 0 {
		t.Errorf("anonymous browses a tag: %v, %d refs", code, len(refs))
	}
	_, refs := browseRefs(t, anon, objects)
	for _, r := range refs {
		if r.NodeID.NodeID.Namespace() == f.s.NamespaceIndex() {
			t.Errorf("anonymous sees %v under Objects", r.NodeID.NodeID)
		}
	}
	// The same requests as a user that reads.
	if code, refs := browseRefs(t, op, folder); code != ua.StatusGood || len(refs) == 0 {
		t.Errorf("a user browses the project folder: %v, %d refs", code, len(refs))
	}
	found := false
	_, refs = browseRefs(t, op, objects)
	for _, r := range refs {
		found = found || r.NodeID.NodeID.Namespace() == f.s.NamespaceIndex()
	}
	if !found {
		t.Error("a user does not see the project folder under Objects")
	}

	// TranslateBrowsePaths from Objects to a tag: found for a user, not for anonymous.
	path := &ua.BrowsePath{StartingNode: objects, RelativePath: &ua.RelativePath{Elements: []*ua.RelativePathElement{
		{ReferenceTypeID: ua.NewNumericNodeID(0, uaid.HierarchicalReferences), IncludeSubtypes: true, TargetName: &ua.QualifiedName{NamespaceIndex: f.s.NamespaceIndex(), Name: "lift-station"}},
		{ReferenceTypeID: ua.NewNumericNodeID(0, uaid.HierarchicalReferences), IncludeSubtypes: true, TargetName: &ua.QualifiedName{NamespaceIndex: f.s.NamespaceIndex(), Name: "Speed"}},
	}}}
	translate := func(c *opcua.Client) ua.StatusCode {
		resp, err := send[*ua.TranslateBrowsePathsToNodeIDsResponse](t, c, &ua.TranslateBrowsePathsToNodeIDsRequest{RequestHeader: &ua.RequestHeader{}, BrowsePaths: []*ua.BrowsePath{path}})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Results[0].StatusCode
	}
	if got := translate(op); got != ua.StatusGood {
		t.Errorf("a user translates Objects/lift-station/Speed: %v", got)
	}
	if got := translate(anon); got != ua.StatusBadNoMatch {
		t.Errorf("anonymous translates Objects/lift-station/Speed: %v, want BadNoMatch", got)
	}
}

// A continuation point is rechecked: a user who browsed a page and then
// lost read gets nothing from BrowseNext.
func TestBrowseNextRechecksTheSession(t *testing.T) {
	id := newFakeIdentity()
	f := startUsers(t, id, Options{})
	op := mustConnect(t, f, "op", "op-pass")
	folder := ua.NewNumericNodeID(f.s.NamespaceIndex(), folderID)
	resp, err := op.Browse(context.Background(), &ua.BrowseRequest{
		RequestedMaxReferencesPerNode: 1,
		NodesToBrowse:                 []*ua.BrowseDescription{{NodeID: folder, BrowseDirection: ua.BrowseDirectionForward, ReferenceTypeID: ua.NewNumericNodeID(0, uaid.References), IncludeSubtypes: true, ResultMask: uint32(ua.BrowseResultMaskAll)}},
	})
	if err != nil || len(resp.Results[0].ContinuationPoint) == 0 {
		t.Fatalf("first page: %v %+v", err, resp)
	}
	id.noRead.Store("user:op", true)
	next, err := op.BrowseNext(context.Background(), &ua.BrowseNextRequest{ContinuationPoints: [][]byte{resp.Results[0].ContinuationPoint}})
	if err != nil {
		t.Fatal(err)
	}
	if got := next.Results[0]; got.StatusCode != ua.StatusBadUserAccessDenied || len(got.References) != 0 {
		t.Errorf("BrowseNext after the read was taken away: %v, %d refs", got.StatusCode, len(got.References))
	}
}

const id2References = 31

// A Call on the project's nodes is refused without read, whatever the
// method: even a refusal that said "no such method" would say the node is
// there.
func TestCallOnProjectNodesNeedsRead(t *testing.T) {
	id := newFakeIdentity()
	id.anonNone.Store(true)
	f := startUsers(t, id, Options{})
	call := func(c *opcua.Client) ua.StatusCode {
		resp, err := c.Call(context.Background(), &ua.CallMethodRequest{ObjectID: ua.NewNumericNodeID(f.s.NamespaceIndex(), folderID), MethodID: ua.NewNumericNodeID(0, 11492)})
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	if got := call(noneClient(t, f.s)); got != ua.StatusBadUserAccessDenied {
		t.Errorf("anonymous calls on the project folder: %v, want BadUserAccessDenied", got)
	}
	if got := call(mustConnect(t, f, "op", "op-pass")); got == ua.StatusBadUserAccessDenied {
		t.Errorf("a user's call on the project folder is refused for read")
	}
}

// Subscriptions: an item on the project's nodes is refused to a session
// with no read, and a subscription that has read stops delivering the
// moment it loses it (the sweep judges the owner again at each tick), once
// told so, and resumes with the current value when read is back.
func TestSubscriptionsFollowRead(t *testing.T) {
	id := newFakeIdentity()
	id.anonNone.Store(true)
	f := startUsers(t, id, Options{})
	ctx := context.Background()

	// An anonymous session's items are refused; the server's own are not.
	anon := noneClient(t, f.s)
	an := make(chan *opcua.PublishNotificationData, 16)
	asub, err := anon.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 50 * time.Millisecond}, an)
	if err != nil {
		t.Fatal(err)
	}
	defer asub.Cancel(ctx)
	res, err := asub.Monitor(ctx, ua.TimestampsToReturnBoth,
		opcua.NewMonitoredItemCreateRequestWithDefaults(f.s.NodeID("Speed"), ua.AttributeIDValue, 1),
		opcua.NewMonitoredItemCreateRequestWithDefaults(ua.NewNumericNodeID(0, id2ServerStatus), ua.AttributeIDValue, 2))
	if err != nil {
		t.Fatal(err)
	}
	if res.Results[0].StatusCode != ua.StatusBadUserAccessDenied || res.Results[1].StatusCode != ua.StatusGood {
		t.Errorf("anonymous items: %v, %v; want BadUserAccessDenied, Good", res.Results[0].StatusCode, res.Results[1].StatusCode)
	}

	// A user's subscription delivers, then stops when read is taken away.
	op := mustConnect(t, f, "op", "op-pass")
	notify := make(chan *opcua.PublishNotificationData, 256)
	sub, err := op.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 20 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(ctx)
	if r, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, opcua.NewMonitoredItemCreateRequestWithDefaults(f.s.NodeID("Speed"), ua.AttributeIDValue, 7)); err != nil || r.Results[0].StatusCode != ua.StatusGood {
		t.Fatalf("monitor: %v %+v", err, r)
	}
	// events collects what the item hears for a while: its values and
	// statuses in order.
	events := func(d time.Duration) (good int, denied int) {
		deadline := time.After(d)
		for {
			select {
			case n := <-notify:
				if dcn, ok := n.Value.(*ua.DataChangeNotification); ok {
					for _, it := range dcn.MonitoredItems {
						if it.Value.Status == ua.StatusBadUserAccessDenied {
							denied++
						} else {
							good++
						}
					}
				}
			case <-deadline:
				return good, denied
			}
		}
	}
	if good, denied := events(400 * time.Millisecond); good == 0 || denied != 0 {
		t.Fatalf("before: %d values, %d denied", good, denied)
	}
	id.noRead.Store("user:op", true)
	if good, denied := events(300 * time.Millisecond); denied != 1 { // the sweep tells the item once
		t.Errorf("when the read was taken away: %d values, %d denials; want exactly 1 denial", good, denied)
	}
	for i := 1; i <= 3; i++ {
		f.rt.Tags().Set("Speed", float64(i)+0.5)
		time.Sleep(60 * time.Millisecond)
	}
	if good, denied := events(300 * time.Millisecond); good != 0 || denied != 0 {
		t.Errorf("after the read was taken away: %d values, %d more denials; want none (told once, then silent)", good, denied)
	}
	id.noRead.Delete("user:op")
	if good, _ := events(500 * time.Millisecond); good == 0 {
		t.Error("read is back and the item hears nothing")
	}
}

// A certificate that expires on an open channel reads nothing more, even
// under anonymous: viewer: an invalid credential is never taken as
// anonymous (as the HTTP API's is not).
func TestExpiredCertificateStopsReadingUnderAuth(t *testing.T) {
	id := newFakeIdentity() // anonymous would read
	f := startUsers(t, id, Options{})
	notAfter := time.Now().Add(3 * time.Second)
	der, key := clientCert(t, "urn:test:brief", notAfter.Add(-time.Hour), time.Hour)
	trust(t, f.dir, der)
	ep := endpointFor(t, f.s, ua.SecurityPolicyURIBasic256Sha256, ua.MessageSecurityModeSignAndEncrypt)
	c, err := secureConnect(f.s, ep, der, key, "urn:test:brief")
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient(c)
	if got := readStatus(t, c, f.s.NodeID("Speed"), ua.AttributeIDValue); got != ua.StatusGood {
		t.Fatalf("valid: %v", got)
	}
	time.Sleep(time.Until(notAfter) + 1100*time.Millisecond)
	if got := readStatus(t, c, f.s.NodeID("Speed"), ua.AttributeIDValue); got != ua.StatusBadUserAccessDenied {
		t.Errorf("expired: %v, want BadUserAccessDenied", got)
	}
}
