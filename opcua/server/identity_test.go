package server

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// The server describes nautilus, not the library it is built on: every
// endpoint's ApplicationDescription carries an ApplicationUri (Part 4
// §7.1) and nautilus's ProductUri; ServerStatus.BuildInfo, the BuildInfo
// variable and its children carry the same ProductUri, the version the CLI
// hands over, a build number and a real build date; and the ServerType
// Mandatory variables ServerArray, ServiceLevel and Auditing (Part 5
// §6.3.1) have Values. Black-box F-04, nautilus#424: the endpoints had no
// ApplicationUri and gopcua's ProductUri, BuildInfo said gopcua 0.0.0-dev
// built in 1601, the BuildInfo variable was an empty value, and the three
// variables answered Bad_AttributeIdInvalid.
func TestServerObjectDescribesNautilus(t *testing.T) {
	rt, _ := newRuntime(t)
	s, err := New(rt, Options{Endpoint: "localhost:0", Name: "lift-station"})
	if err != nil {
		t.Fatal(err)
	}
	s.SetSoftwareVersion("1.2.3")
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	appURI := ApplicationURI("lift-station")
	if appURI != "urn:joyautomation:nautilus:lift-station" {
		t.Fatalf("ApplicationURI = %q", appURI)
	}

	eps, err := opcua.GetEndpoints(ctx, s.URL())
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) == 0 {
		t.Fatal("no endpoints")
	}
	for _, ep := range eps {
		if ep.Server.ApplicationURI != appURI {
			t.Errorf("endpoint %s ApplicationUri = %q, want %q", ep.EndpointURL, ep.Server.ApplicationURI, appURI)
		}
		if ep.Server.ProductURI != ProductURI {
			t.Errorf("endpoint %s ProductUri = %q, want %q", ep.EndpointURL, ep.Server.ProductURI, ProductURI)
		}
	}

	c, err := opcua.NewClient(s.URL(), opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(ctx); err != nil {
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
			t.Errorf("i=%d Value: %s", nid, dv.Status)
			return nil
		}
		return dv.Value.Value()
	}
	checkBuild := func(where string, bi *ua.BuildInfo) {
		t.Helper()
		if bi == nil {
			t.Errorf("%s: no BuildInfo", where)
			return
		}
		if bi.ProductURI != ProductURI || bi.ManufacturerName != "Joy Automation" || bi.ProductName != "Nautilus" || bi.SoftwareVersion != "1.2.3" {
			t.Errorf("%s: %+v", where, bi)
		}
		if bi.BuildDate.Year() < 2020 {
			t.Errorf("%s: BuildDate %v", where, bi.BuildDate)
		}
	}

	// ServerStatus and the BuildInfo variable carry the whole structure.
	if eo, ok := value(id.Server_ServerStatus).(*ua.ExtensionObject); !ok {
		t.Errorf("ServerStatus is not a structure")
	} else if st, ok := eo.Value.(*ua.ServerStatusDataType); !ok {
		t.Errorf("ServerStatus holds %T", eo.Value)
	} else {
		checkBuild("ServerStatus.BuildInfo", st.BuildInfo)
	}
	if eo, ok := value(id.Server_ServerStatus_BuildInfo).(*ua.ExtensionObject); !ok {
		t.Errorf("BuildInfo (i=2260) is not a structure")
	} else if bi, ok := eo.Value.(*ua.BuildInfo); !ok {
		t.Errorf("BuildInfo (i=2260) holds %T", eo.Value)
	} else {
		checkBuild("BuildInfo (i=2260)", bi)
	}
	// And its children one by one.
	if v := value(id.Server_ServerStatus_BuildInfo_ProductURI); v != ProductURI {
		t.Errorf("BuildInfo.ProductUri = %v", v)
	}
	if v := value(id.Server_ServerStatus_BuildInfo_ManufacturerName); v != "Joy Automation" {
		t.Errorf("BuildInfo.ManufacturerName = %v", v)
	}
	if v := value(id.Server_ServerStatus_BuildInfo_SoftwareVersion); v != "1.2.3" {
		t.Errorf("BuildInfo.SoftwareVersion = %v", v)
	}
	if v, ok := value(id.Server_ServerStatus_BuildInfo_BuildDate).(time.Time); !ok || v.Year() < 2020 {
		t.Errorf("BuildInfo.BuildDate = %v", v)
	}
	if v := value(id.Server_ServerStatus_BuildInfo_BuildNumber); v == nil {
		t.Errorf("BuildInfo.BuildNumber has no value")
	}
	if v := value(id.Server_ServerStatus_SecondsTillShutdown); v != uint32(0) {
		t.Errorf("SecondsTillShutdown = %#v, want uint32 0", v)
	}
	if _, ok := value(id.Server_ServerStatus_ShutdownReason).(*ua.LocalizedText); !ok {
		t.Errorf("ShutdownReason is not a LocalizedText")
	}
	if b := read(t, c, ua.NewNumericNodeID(0, id.Server_ServerStatus_BuildInfo_ManufacturerName), ua.AttributeIDBrowseName); b.Value.Value().(*ua.QualifiedName).Name != "ManufacturerName" {
		t.Errorf("ManufacturerName's BrowseName = %v", b.Value.Value())
	}

	// The address space is the standard's: BuildInfo's six fields hang
	// under BuildInfo (they used to hang flat under ServerStatus, with
	// BuildInfo childless), BuildInfo and the other fields of ServerStatus
	// under ServerStatus, each by HasComponent and under its standard
	// BrowseName.
	children := func(parent uint32, want ...string) {
		t.Helper()
		got := browse(t, c, ua.NewNumericNodeID(0, parent))
		names := make([]string, 0, len(got))
		for n := range got {
			names = append(names, n)
		}
		slices.Sort(names)
		slices.Sort(want)
		if !slices.Equal(names, want) {
			t.Errorf("i=%d's children %v, want %v", parent, names, want)
		}
		for _, r := range got {
			if r.ReferenceTypeID.IntID() != id.HasComponent {
				t.Errorf("i=%d → %s by %s, want HasComponent", parent, r.BrowseName.Name, r.ReferenceTypeID)
			}
		}
	}
	children(id.Server_ServerStatus_BuildInfo, "ProductUri", "ManufacturerName", "ProductName", "SoftwareVersion", "BuildNumber", "BuildDate")
	children(id.Server_ServerStatus, "StartTime", "CurrentTime", "State", "BuildInfo", "SecondsTillShutdown", "ShutdownReason")
	for nid, want := range map[uint32]string{
		id.Server_ServerStatus:                           "ServerStatus",
		id.Server_ServerStatus_State:                     "State",
		id.Server_ServerStatus_BuildInfo_ProductURI:      "ProductUri",
		id.Server_NamespaceArray:                         "NamespaceArray",
		id.Server_ServerStatus_BuildInfo_SoftwareVersion: "SoftwareVersion",
	} {
		if b := read(t, c, ua.NewNumericNodeID(0, nid), ua.AttributeIDBrowseName); b.Status != ua.StatusGood || b.Value.Value().(*ua.QualifiedName).Name != want {
			t.Errorf("i=%d BrowseName = %v %s, want %s", nid, b.Value.Value(), b.Status, want)
		}
		if nc := read(t, c, ua.NewNumericNodeID(0, nid), ua.AttributeIDNodeClass); nc.Value.Value() != int32(ua.NodeClassVariable) {
			t.Errorf("i=%d NodeClass = %v, want Variable", nid, nc.Value.Value())
		}
	}
	// And each Variable has the DataType and ValueRank the standard gives
	// it (they answered Bad_AttributeIdInvalid).
	for nid, want := range map[uint32]struct {
		dt   uint32
		rank int32
	}{
		id.Server_ServerArray:                       {id.String, 1},
		id.Server_NamespaceArray:                    {id.String, 1},
		id.Server_ServerStatus:                      {id.ServerStatusDataType, -1},
		id.Server_ServerStatus_BuildInfo:            {id.BuildInfo, -1},
		id.Server_ServerStatus_BuildInfo_ProductURI: {id.String, -1},
		id.Server_ServerStatus_BuildInfo_BuildDate:  {id.UtcTime, -1},
		id.Server_ServerStatus_SecondsTillShutdown:  {id.UInt32, -1},
		id.Server_ServiceLevel:                      {id.Byte, -1},
		id.Server_Auditing:                          {id.Boolean, -1},
	} {
		dt := read(t, c, ua.NewNumericNodeID(0, nid), ua.AttributeIDDataType)
		if got, ok := dt.Value.Value().(*ua.NodeID); dt.Status != ua.StatusGood || !ok || got.IntID() != want.dt || got.Namespace() != 0 {
			t.Errorf("i=%d DataType = %v %s, want i=%d", nid, dt.Value.Value(), dt.Status, want.dt)
		}
		vr := read(t, c, ua.NewNumericNodeID(0, nid), ua.AttributeIDValueRank)
		if vr.Status != ua.StatusGood || vr.Value.Value() != want.rank {
			t.Errorf("i=%d ValueRank = %v %s, want %d", nid, vr.Value.Value(), vr.Status, want.rank)
		}
	}

	// The ServerType Mandatory variables.
	if v := value(id.Server_ServerArray); !slices.Equal(asStrings(v), []string{appURI}) {
		t.Errorf("ServerArray = %#v, want [%q]", v, appURI)
	}
	if v := value(id.Server_ServiceLevel); v != byte(255) {
		t.Errorf("ServiceLevel = %#v, want byte 255 while running", v)
	}
	if v := value(id.Server_Auditing); v != false {
		t.Errorf("Auditing = %#v, want false", v)
	}
}

// Unset, the version is the binary's own (a test binary's module version
// is "dev" or a pseudo-version), never gopcua's placeholder.
// The ApplicationUri names the controller, not the machine: the default
// is the project's, percent-encoded so any project name makes a valid URI
// (nautilus#424 review: a host name differed between a Deployment's
// replicas and changed with every pod), and opcua.application-uri
// replaces it.
func TestApplicationURIIsStableAndOverridable(t *testing.T) {
	for name, want := range map[string]string{
		"lift-station": "urn:joyautomation:nautilus:lift-station",
		"Lift Station": "urn:joyautomation:nautilus:Lift%20Station",
		"a/b?c#d":      "urn:joyautomation:nautilus:a%2Fb%3Fc%23d",
	} {
		got := ApplicationURI(name)
		if got != want {
			t.Errorf("ApplicationURI(%q) = %q, want %q", name, got, want)
		}
		if err := CheckApplicationURI(got); err != nil {
			t.Errorf("ApplicationURI(%q): %v", name, err)
		}
	}
	for _, bad := range []string{"lift-station", "urn:", "urn:lift station", "://x"} {
		if err := CheckApplicationURI(bad); err == nil {
			t.Errorf("CheckApplicationURI(%q) passed", bad)
		}
	}
	rt, _ := newRuntime(t)
	if _, err := New(rt, Options{Name: "x", ApplicationURI: "not a uri"}); err == nil {
		t.Error("New took a bad ApplicationURI")
	}
	s, err := New(rt, Options{Endpoint: "localhost:0", Name: "lift-station", ApplicationURI: "urn:acme:plant7:lift-station"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	eps, err := opcua.GetEndpoints(ctx, s.URL())
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range eps {
		if ep.Server.ApplicationURI != "urn:acme:plant7:lift-station" {
			t.Errorf("endpoint %s ApplicationUri = %q", ep.EndpointURL, ep.Server.ApplicationURI)
		}
	}
}

func TestBinaryBuildIsNeverThePlaceholder(t *testing.T) {
	b := binaryBuild()
	if b.version == "" || b.version == "0.0.0-dev" {
		t.Errorf("version %q", b.version)
	}
	if b.date.IsZero() {
		t.Errorf("no build date")
	}
}

func asStrings(v any) []string {
	s, _ := v.([]string)
	return s
}
