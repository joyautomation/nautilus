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
