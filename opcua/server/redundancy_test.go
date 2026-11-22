package server

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/internal/gopcuatest"
)

// A member of a redundant pair tells a client the set (RedundancySupport,
// ServerUriArray) and how good a server it is now (ServiceLevel), live: a
// client that reads both boxes picks the higher one (#716). Not in a set,
// the standard's defaults stand and ServiceLevel is 255.
func TestRedundancyNodesFollowTheMember(t *testing.T) {
	rt, _ := newRuntime(t)
	s, err := New(rt, Options{Endpoint: "localhost:0", Name: "lift-station"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	c, err := opcua.NewClient(s.URL(), opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := gopcuatest.Connect(ctx, c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		c.Close(cctx)
	})
	value := func(nid uint32) any {
		t.Helper()
		dv := read(t, c, ua.NewNumericNodeID(0, nid), ua.AttributeIDValue)
		if dv.Status != ua.StatusGood {
			t.Fatalf("i=%d Value: %s", nid, dv.Status)
		}
		return dv.Value.Value()
	}

	if v := value(id.Server_ServiceLevel); v != byte(255) {
		t.Errorf("alone: ServiceLevel = %#v, want 255", v)
	}
	if v := value(id.Server_ServerRedundancy_RedundancySupport); v != int32(0) {
		t.Errorf("alone: RedundancySupport = %#v, want None", v)
	}

	level := byte(250)
	s.SetRedundancy(&Redundancy{
		Support:      1,
		ServerURIs:   []string{"urn:x:a", "urn:x:b"},
		ServiceLevel: func() byte { return level },
	})
	if v := value(id.Server_ServiceLevel); v != byte(250) {
		t.Errorf("leader: ServiceLevel = %#v, want 250", v)
	}
	level = 100
	if v := value(id.Server_ServiceLevel); v != byte(100) {
		t.Errorf("standby: ServiceLevel = %#v, want 100 (it follows the member, read by read)", v)
	}
	if v := value(id.Server_ServerRedundancy_RedundancySupport); v != int32(1) {
		t.Errorf("RedundancySupport = %#v, want Cold", v)
	}
	if v := value(id.Server_ServerRedundancy_ServerURIArray); !slices.Equal(asStrings(v), []string{"urn:x:a", "urn:x:b"}) {
		t.Errorf("ServerUriArray = %#v", v)
	}
	s.SetRedundancy(nil)
	if v := value(id.Server_ServiceLevel); v != byte(255) {
		t.Errorf("left the set: ServiceLevel = %#v, want 255", v)
	}
}
