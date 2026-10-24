package server

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

func TestEndpointHosts(t *testing.T) {
	hn, _ := os.Hostname()
	cases := []struct {
		host   string
		listen string
		want   []string // aliases that must be present, in this order
		exact  bool     // and nothing else
	}{
		{"localhost", "127.0.0.1", []string{"localhost"}, true},
		{"127.0.0.1", "127.0.0.1", []string{"localhost"}, true},
		{"::1", "[::1]", []string{"localhost"}, true},
		{"10.1.2.3", "10.1.2.3", nil, true},
		{"0.0.0.0", "0.0.0.0", []string{hn, "localhost", "127.0.0.1", "[::1]"}, false},
		{"::", "[::]", []string{hn, "localhost", "127.0.0.1", "[::1]"}, false},
		{"", "0.0.0.0", []string{hn, "localhost", "127.0.0.1", "[::1]"}, false},
	}
	for _, c := range cases {
		listen, aliases := endpointHosts(c.host)
		if listen != c.listen {
			t.Errorf("%q listens on %q, want %q", c.host, listen, c.listen)
		}
		if c.exact {
			if !slices.Equal(aliases, c.want) {
				t.Errorf("%q aliases %q, want %q", c.host, aliases, c.want)
			}
			continue
		}
		for _, w := range c.want {
			if !slices.Contains(aliases, w) {
				t.Errorf("%q aliases %q lack %q", c.host, aliases, w)
			}
		}
		if slices.Contains(aliases, c.host) || slices.Contains(aliases, listen) {
			t.Errorf("%q advertises its own wildcard among the aliases: %q", c.host, aliases)
		}
		seen := map[string]bool{}
		for _, a := range aliases {
			if seen[a] {
				t.Errorf("%q advertises %q twice", c.host, a)
			}
			seen[a] = true
			if strings.Contains(a, ":") && !strings.HasPrefix(a, "[") {
				t.Errorf("%q: IPv6 alias %q is not bracketed", c.host, a)
			}
		}
	}
}

// The default host, localhost, listens on 127.0.0.1 — the loopback a
// Windows SCADA host dials — and a client that dials either name finds
// its endpoint; one that dials a name the server never heard of gets the
// whole list rather than nothing.
func TestEndpointsMatchEveryAliasAndFallBackToAll(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	if !strings.HasPrefix(s.URL(), "opc.tcp://127.0.0.1:") {
		t.Fatalf("localhost listens at %s", s.URL())
	}
	all := s.srv.Endpoints()
	if len(all) != 2 {
		t.Fatalf("%d endpoints advertised for localhost, want 2 (127.0.0.1 and localhost): %+v", len(all), urls(all))
	}
	port := s.URL()[strings.LastIndex(s.URL(), ":"):]
	for _, url := range []string{s.URL(), "opc.tcp://localhost" + port, "OPC.TCP://LOCALHOST" + port + "/"} {
		resp, err := getEndpoints(s.srv, &ua.GetEndpointsRequest{RequestHeader: &ua.RequestHeader{}, EndpointURL: url})
		if err != nil {
			t.Fatal(err)
		}
		eps := resp.(*ua.GetEndpointsResponse).Endpoints
		if len(eps) != 1 || !strings.EqualFold(strings.TrimSuffix(eps[0].EndpointURL, "/"), strings.TrimSuffix(url, "/")) {
			t.Errorf("GetEndpoints(%s) → %v", url, urls(eps))
		}
	}
	resp, err := getEndpoints(s.srv, &ua.GetEndpointsRequest{RequestHeader: &ua.RequestHeader{}, EndpointURL: "opc.tcp://elsewhere.example:4840"})
	if err != nil {
		t.Fatal(err)
	}
	if eps := resp.(*ua.GetEndpointsResponse).Endpoints; len(eps) != len(all) {
		t.Errorf("GetEndpoints for an unknown URL → %v, want every endpoint", urls(eps))
	}
	// Through the wire: the client dialled s.URL() and asks with it.
	got, err := c.GetEndpoints(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Endpoints) != 1 || got.Endpoints[0].EndpointURL != s.URL() {
		t.Errorf("client GetEndpoints → %v", urls(got.Endpoints))
	}
	if got.Endpoints[0].Server.ApplicationName.Text != "Nautilus · lift-station" {
		t.Errorf("application name %q", got.Endpoints[0].Server.ApplicationName.Text)
	}
}

func urls(eps []*ua.EndpointDescription) []string {
	out := make([]string, 0, len(eps))
	for _, ep := range eps {
		out = append(out, ep.EndpointURL)
	}
	return out
}

func TestProjectNamespaceIsIndexOne(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	if s.NamespaceIndex() != 1 {
		t.Fatalf("namespace index %d", s.NamespaceIndex())
	}
	arr := read(t, c, ua.NewNumericNodeID(0, id.Server_NamespaceArray), ua.AttributeIDValue)
	want := []string{"http://opcfoundation.org/UA/", "urn:joyautomation:nautilus:lift-station"}
	if got, _ := arr.Value.Value().([]string); !slices.Equal(got, want) {
		t.Errorf("NamespaceArray %q, want %q", got, want)
	}
	if dv := read(t, c, ua.NewStringNodeID(1, "Level"), ua.AttributeIDValue); dv.Status != ua.StatusGood || dv.Value.Value() != 3.5 {
		t.Errorf("ns=1;s=Level = %v %s", dv.Value, dv.Status)
	}
	if dv := read(t, c, ua.NewStringNodeID(2, "Level"), ua.AttributeIDValue); dv.Status != ua.StatusBadNodeIDUnknown {
		t.Errorf("ns=2;s=Level (no such namespace) read %s, want Bad_NodeIdUnknown", dv.Status)
	}
}

// Nothing is writable, gopcua's Server object included: an anonymous
// client that could rewrite NamespaceArray would break namespace-mapping
// for every other client until restart.
func TestServerObjectIsReadOnly(t *testing.T) {
	rt, _ := newRuntime(t)
	_, c := startServer(t, rt)
	nsArr := ua.NewNumericNodeID(0, id.Server_NamespaceArray)
	product := ua.NewNumericNodeID(0, id.Server_ServerStatus_BuildInfo_ProductName)
	before := read(t, c, nsArr, ua.AttributeIDValue)
	wv := func(nid *ua.NodeID, attr ua.AttributeID, v any) *ua.WriteValue {
		return &ua.WriteValue{NodeID: nid, AttributeID: attr, Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(v)}}
	}
	resp, err := c.Write(context.Background(), &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{
		wv(nsArr, ua.AttributeIDValue, []string{"x"}),
		wv(product, ua.AttributeIDValue, "Hacked"),
		wv(product, ua.AttributeIDDisplayName, text("Hacked")),
		wv(ua.NewNumericNodeID(0, 987654321), ua.AttributeIDValue, 1.0),
		wv(ua.NewStringNodeID(9, "Level"), ua.AttributeIDValue, 1.0),
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []ua.StatusCode{ua.StatusBadUserAccessDenied, ua.StatusBadUserAccessDenied, ua.StatusBadNotWritable, ua.StatusBadNodeIDUnknown, ua.StatusBadNodeIDUnknown}
	for i, w := range want {
		if resp.Results[i] != w {
			t.Errorf("write %d: %s, want %s", i, resp.Results[i], w)
		}
	}
	if after := read(t, c, nsArr, ua.AttributeIDValue); !slices.Equal(after.Value.Value().([]string), before.Value.Value().([]string)) {
		t.Errorf("NamespaceArray changed: %v → %v", before.Value.Value(), after.Value.Value())
	}
	if dv := read(t, c, product, ua.AttributeIDValue); dv.Value.Value() != "Nautilus" {
		t.Errorf("ProductName = %v", dv.Value.Value())
	}
	// And the Server object's variables say so, in the attributes Part 3
	// makes mandatory and gopcua's nodes lack.
	for _, attr := range []ua.AttributeID{ua.AttributeIDAccessLevel, ua.AttributeIDUserAccessLevel} {
		if dv := read(t, c, nsArr, attr); dv.Status != ua.StatusGood || dv.Value.Value() != uint8(ua.AccessLevelTypeCurrentRead) {
			t.Errorf("NamespaceArray %v = %v %s", attr, dv.Value, dv.Status)
		}
	}
	for _, attr := range []ua.AttributeID{ua.AttributeIDWriteMask, ua.AttributeIDUserWriteMask} {
		if dv := read(t, c, ua.NewNumericNodeID(0, id.Server), attr); dv.Status != ua.StatusGood || dv.Value.Value() != uint32(0) {
			t.Errorf("Server %v = %v %s", attr, dv.Value, dv.Status)
		}
	}
	if dv := read(t, c, ua.NewNumericNodeID(0, id.Server), ua.AttributeIDAccessLevel); dv.Status != ua.StatusBadAttributeIDInvalid {
		t.Errorf("an Object answered AccessLevel: %v %s", dv.Value, dv.Status)
	}
}

// CreateMonitoredItems says at once which items cannot be monitored —
// a node that is not there, a Value on an Object or the folder — with the
// code a Read gives, and creates only the rest.
func TestCreateMonitoredItemsRejectsWhatCannotBeMonitored(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	notify := make(chan *opcua.PublishNotificationData, 16)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 50 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(ctx)
	item := func(nid *ua.NodeID, handle uint32) *ua.MonitoredItemCreateRequest {
		return opcua.NewMonitoredItemCreateRequestWithDefaults(nid, ua.AttributeIDValue, handle)
	}
	resp, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth,
		item(s.NodeID("Level"), 1),
		item(s.NodeID("P101"), 2),
		item(ua.NewNumericNodeID(s.NamespaceIndex(), folderID), 3),
		item(s.NodeID("Nope"), 4),
		item(ua.NewStringNodeID(9, "Level"), 5),
		item(ua.NewNumericNodeID(0, 987654321), 6),
		item(ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime), 7),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []ua.StatusCode{ua.StatusGood, ua.StatusBadAttributeIDInvalid, ua.StatusBadAttributeIDInvalid, ua.StatusBadNodeIDUnknown, ua.StatusBadNodeIDUnknown, ua.StatusBadNodeIDUnknown, ua.StatusGood}
	if len(resp.Results) != len(want) {
		t.Fatalf("%d results for %d items", len(resp.Results), len(want))
	}
	for i, w := range want {
		r := resp.Results[i]
		if r.StatusCode != w {
			t.Errorf("item %d: %s, want %s", i, r.StatusCode, w)
		}
		if w == ua.StatusGood && r.MonitoredItemID == 0 {
			t.Errorf("item %d created with no id", i)
		}
		if w != ua.StatusGood && r.MonitoredItemID != 0 {
			t.Errorf("item %d refused yet given id %d", i, r.MonitoredItemID)
		}
	}
	if st := s.Status(); st.MonitoredItems != 2 {
		t.Errorf("%d monitored items booked, want the 2 that passed", st.MonitoredItems)
	}
	// The one that passed is live.
	rt.Tags().Set("Level", 7.25)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case n := <-notify:
			if d, ok := n.Value.(*ua.DataChangeNotification); ok {
				for _, m := range d.MonitoredItems {
					if m.ClientHandle == 1 && m.Value.Value.Value() == 7.25 {
						return
					}
				}
			}
		case <-deadline:
			t.Fatal("no notification for Level")
		}
	}
}

// A value nothing has changed since the server started was first seen
// at start, not when a client first happened to read it.
func TestSourceTimestampIsSeededAtStart(t *testing.T) {
	rt, _ := newRuntime(t)
	s, err := New(rt, Options{Endpoint: "localhost:0", Name: "p", Sweep: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t1 := time.Now()
	t.Cleanup(s.Stop)
	time.Sleep(150 * time.Millisecond)
	c, err := opcua.NewClient(s.URL(), opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	dv := read(t, c, s.NodeID("Label"), ua.AttributeIDValue)
	if dv.SourceTimestamp.Before(t0) || dv.SourceTimestamp.After(t1) {
		t.Errorf("SourceTimestamp %v outside start window [%v, %v]", dv.SourceTimestamp, t0, t1)
	}
	if dv.ServerTimestamp.Sub(dv.SourceTimestamp) < 100*time.Millisecond {
		t.Errorf("SourceTimestamp %v is the read time %v", dv.SourceTimestamp, dv.ServerTimestamp)
	}
}

// readDV reads one node's Value with both timestamps through the client.
func readDV(t *testing.T, c *opcua.Client, nid *ua.NodeID) *ua.DataValue {
	t.Helper()
	resp, err := c.Read(context.Background(), &ua.ReadRequest{
		NodesToRead:        []*ua.ReadValueID{{NodeID: nid, AttributeID: ua.AttributeIDValue}},
		TimestampsToReturn: ua.TimestampsToReturnBoth,
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Results[0]
}

// A client told "10 ms" that sees 100 ms updates has nothing true to plan
// on: the revised sampling interval is a multiple of the sweep — the sweep
// for "fastest" and for anything faster, the nearest multiple otherwise —
// and the item is then sampled at that rate (retest run 2026-10-24-001,
// F-20).
func TestRevisedSamplingIntervalIsAMultipleOfTheSweep(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt) // Sweep: 20 ms
	ctx := context.Background()
	notify := make(chan *opcua.PublishNotificationData, 1024)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 5 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(ctx)
	for _, tc := range []struct{ asked, want float64 }{{0, 20}, {-1, 20}, {7, 20}, {1000, 1000}, {150, 160}} {
		item := opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Label"), ua.AttributeIDValue, 1)
		item.RequestedParameters.SamplingInterval = tc.asked
		res, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, item)
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Results[0].RevisedSamplingInterval; got != tc.want {
			t.Errorf("asked %v ms on a 5 ms subscription: revised %v, want %v", tc.asked, got, tc.want)
		}
		if _, err := sub.Unmonitor(ctx, res.Results[0].MonitoredItemID); err != nil {
			t.Fatal(err)
		}
	}

	// Two items on the same node, one at the sweep and one at 200 ms, while
	// the value moves every 25 ms for 600 ms: the slow one is held to its
	// rate, the fast one hears most changes.
	fast := opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Level"), ua.AttributeIDValue, 10)
	slow := opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Level"), ua.AttributeIDValue, 20)
	slow.RequestedParameters.SamplingInterval = 200
	if _, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, fast, slow); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	for len(notify) > 0 {
		<-notify
	}
	for i := 0; i < 24; i++ {
		rt.Tags().Set("Level", 100.0+float64(i))
		time.Sleep(25 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	counts := map[uint32]int{}
	for len(notify) > 0 {
		if dcn, ok := (<-notify).Value.(*ua.DataChangeNotification); ok {
			for _, it := range dcn.MonitoredItems {
				counts[it.ClientHandle]++
			}
		}
	}
	if counts[10] < 12 {
		t.Errorf("item sampled at the sweep: %d notifications for 24 changes over 600 ms, want at least 12", counts[10])
	}
	if counts[20] < 2 || counts[20] > 5 {
		t.Errorf("item sampled at 200 ms: %d notifications over 600 ms, want 2–5", counts[20])
	}
}

// The store's generation is per tag, so one member moving moves its idle
// siblings' generation: they must not be notified, and their
// SourceTimestamp must not move (retest run 2026-10-24-001, F-21, F-22).
func TestIdleUDTMemberStaysQuietWhenASiblingMoves(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()
	amps, speed := s.NodeID("P101.Drive.Amps"), s.NodeID("P101.Drive.Speed")
	notify := make(chan *opcua.PublishNotificationData, 256)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 20 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(ctx)
	if _, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth,
		opcua.NewMonitoredItemCreateRequestWithDefaults(amps, ua.AttributeIDValue, 1),
		opcua.NewMonitoredItemCreateRequestWithDefaults(speed, ua.AttributeIDValue, 2)); err != nil {
		t.Fatal(err)
	}
	drain := func(d time.Duration) map[uint32]int {
		counts := map[uint32]int{}
		deadline := time.After(d)
		for {
			select {
			case n := <-notify:
				if dcn, ok := n.Value.(*ua.DataChangeNotification); ok {
					for _, it := range dcn.MonitoredItems {
						counts[it.ClientHandle]++
					}
				}
			case <-deadline:
				return counts
			}
		}
	}
	drain(200 * time.Millisecond) // the initial values
	before := readDV(t, c, amps)
	for i := 0; i < 5; i++ {
		if err := rt.Tags().SetPath("P101.Drive.Speed", 10.0+float64(i)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	counts := drain(200 * time.Millisecond)
	if counts[2] < 3 {
		t.Errorf("Speed moved five times: %d notifications, want at least 3", counts[2])
	}
	if counts[1] != 0 {
		t.Errorf("Amps never moved: %d notifications, want none", counts[1])
	}
	after := readDV(t, c, amps)
	if !after.SourceTimestamp.Equal(before.SourceTimestamp) {
		t.Errorf("Amps SourceTimestamp moved %v → %v while only Speed changed", before.SourceTimestamp, after.SourceTimestamp)
	}
	if sp := readDV(t, c, speed); !sp.SourceTimestamp.After(before.SourceTimestamp) {
		t.Errorf("Speed SourceTimestamp %v did not move past %v", sp.SourceTimestamp, before.SourceTimestamp)
	}
}

// 0.0.0.0 is the address the server binds, not a name a client can dial:
// it appears in neither GetEndpoints nor FindServers (retest run
// 2026-10-24-001, F-24).
func TestWildcardIsNotAdvertised(t *testing.T) {
	rt, _ := newRuntime(t)
	s, err := New(rt, Options{Endpoint: "0.0.0.0:0", Name: "lift-station", Sweep: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	resp, err := getEndpoints(s.srv, &ua.GetEndpointsRequest{RequestHeader: &ua.RequestHeader{}, EndpointURL: "opc.tcp://nowhere.invalid:1"})
	if err != nil {
		t.Fatal(err)
	}
	eps := urls(resp.(*ua.GetEndpointsResponse).Endpoints)
	if len(eps) < 2 {
		t.Fatalf("GetEndpoints for an unknown URL: %q, want the whole advertised list", eps)
	}
	for _, u := range eps {
		if wildcardURL(u) {
			t.Errorf("GetEndpoints advertises the wildcard: %q", eps)
		}
	}
	fresp, err := findServers(s.srv, &ua.FindServersRequest{RequestHeader: &ua.RequestHeader{}})
	if err != nil {
		t.Fatal(err)
	}
	servers := fresp.(*ua.FindServersResponse).Servers
	if len(servers) != 1 || len(servers[0].DiscoveryURLs) == 0 {
		t.Fatalf("FindServers: %+v, want one server with discovery URLs", servers)
	}
	for _, u := range servers[0].DiscoveryURLs {
		if wildcardURL(u) {
			t.Errorf("FindServers lists the wildcard as a discovery URL: %q", servers[0].DiscoveryURLs)
		}
	}
}
