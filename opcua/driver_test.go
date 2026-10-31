// driver_test.go is the acceptance suite: the whole driver against gopcua's
// in-process server over 127.0.0.1 — no build tags, no environment gating
// (the eip/logixserver and modbus/slave precedent). The server is a foreign
// implementation of the protocol, which is the point: the driver is tested
// against something it did not write.
package opcua

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
)

// testServer is one gopcua server with a small address space:
//
//	Objects/Plant (folder)
//	  Speed   Double   (ns=2;s=Speed)
//	  Count   Int32    (ns=2;s=Count)
//	  Run     Boolean  (ns=2;s=Run)
//	  Name    String   (ns=2;s=Name)
//	  Setpoint Double  (ns=2;s=Setpoint)   written by the driver
type testServer struct {
	srv  *server.Server
	ns   *server.NodeNameSpace
	url  string
	port int
	vars map[string]*server.Node
	sess *Session
}

// freePort asks the kernel for a port and gives it back — the gopcua server
// does not report the port it bound when asked for :0.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startServer(t *testing.T, port int) *testServer {
	t.Helper()
	srv := server.New(
		server.EnableSecurity("None", ua.MessageSecurityModeNone),
		server.EnableAuthMode(ua.UserTokenTypeAnonymous),
		server.EndPoint("127.0.0.1", port),
	)
	ns := server.NewNodeNameSpace(srv, "urn:nautilus:test")
	plant := ns.AddNode(server.NewFolderNode(ua.NewStringNodeID(ns.ID(), "Plant"), "Plant"))
	// gopcua routes a Browse by the node's namespace, so the ns=0 Objects
	// folder (what every client starts from) must reference our folder.
	srv.Node(server.ObjectsFolder).AddRef(plant, id.Organizes, true)
	ts := &testServer{srv: srv, ns: ns, port: port, url: fmt.Sprintf("opc.tcp://127.0.0.1:%d", port), vars: map[string]*server.Node{}}
	add := func(name string, v any, typ uint32, access uint8) {
		n := ns.AddNewVariableStringNode(name, v)
		_ = n.SetAttribute(ua.AttributeIDDataType, server.DataValueFromValue(ua.NewNumericNodeID(0, typ)))
		_ = n.SetAttribute(ua.AttributeIDAccessLevel, server.DataValueFromValue(access))
		plant.AddRef(n, id.HasComponent, true)
		ts.vars[name] = n
	}
	const rd, rw = uint8(ua.AccessLevelTypeCurrentRead), uint8(ua.AccessLevelTypeCurrentRead | ua.AccessLevelTypeCurrentWrite)
	add("Speed", 12.5, id.Double, rd)
	add("Count", int32(7), id.Int32, rd)
	add("Run", true, id.Boolean, rd)
	add("Name", "pump-1", id.String, rd)
	add("Setpoint", 0.0, id.Double, rw)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return ts
}

// set and get go through an OPC UA session of the test's own, never the
// server's Node directly: gopcua's nodes are unsynchronized and its server
// handles every service on one goroutine, so a request is the only
// race-free way in. A set therefore meets the access check (rw nodes only).
func (ts *testServer) session(t *testing.T) *Session {
	t.Helper()
	if ts.sess == nil {
		sess, err := Dial(context.Background(), ts.url, Connect{Timeout: 2 * time.Second, SessionName: "fixture"})
		if err != nil {
			t.Fatalf("fixture session: %v", err)
		}
		ts.sess = sess
		t.Cleanup(sess.Close)
	}
	return ts.sess
}

func (ts *testServer) set(t *testing.T, name string, v any) {
	t.Helper()
	variant, err := ua.NewVariant(v)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ts.session(t).Client.Write(context.Background(), &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{
		NodeID: ts.vars[name].ID(), AttributeID: ua.AttributeIDValue,
		Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: variant},
	}}})
	if err != nil || len(resp.Results) != 1 || resp.Results[0] != ua.StatusOK {
		t.Fatalf("fixture write %s: %v %v", name, err, resp)
	}
}

func (ts *testServer) get(t *testing.T, name string) any {
	t.Helper()
	resp, err := ts.session(t).Client.Read(context.Background(), &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{
		NodeID: ts.vars[name].ID(), AttributeID: ua.AttributeIDValue}}})
	if err != nil || len(resp.Results) != 1 || resp.Results[0].Value == nil {
		t.Fatalf("fixture read %s: %v", name, err)
	}
	return resp.Results[0].Value.Value()
}

// manifest binds the five variables; nsIndex lets a test claim the wrong
// index to prove the URI remap works.
func manifest(nsIndex int) Manifest {
	nid := func(s string) string { return fmt.Sprintf("ns=%d;s=%s", nsIndex, s) }
	return Manifest{
		Namespaces: []string{"http://opcfoundation.org/UA/", "urn:nautilus:test"},
		Tags: []TagBinding{
			{Name: "Speed", NodeID: nid("Speed"), Namespace: "urn:nautilus:test", Type: "Double"},
			{Name: "Count", NodeID: nid("Count"), Namespace: "urn:nautilus:test", Type: "Int32"},
			{Name: "Run", NodeID: nid("Run"), Namespace: "urn:nautilus:test", Type: "Boolean"},
			{Name: "Name", NodeID: nid("Name"), Namespace: "urn:nautilus:test", Type: "String"},
			{Name: "Setpoint", NodeID: nid("Setpoint"), Namespace: "urn:nautilus:test", Type: "Double", Writable: true},
		},
	}
}

func fastOpts(extra ...Option) []Option {
	return append([]Option{
		WithName("plant"),
		WithScanRate(50 * time.Millisecond),
		WithTimeout(2 * time.Second),
		WithRetry(20*time.Millisecond, 100*time.Millisecond),
	}, extra...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func startDriver(t *testing.T, url string, m Manifest, opts ...Option) *Driver {
	t.Helper()
	d, err := New(url, m, fastOpts(opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	t.Cleanup(func() { cancel(); d.Stop() })
	return d
}

func read(t *testing.T, d *Driver) nio.Values {
	t.Helper()
	v, err := d.ReadInputs()
	if err != nil {
		t.Fatalf("ReadInputs: %v", err)
	}
	return v
}

func TestNewNeverDials(t *testing.T) {
	d, err := New("opc.tcp://192.0.2.1:4840", manifest(2), WithName("plant"), WithScanClass("slow", time.Minute), WithTagClass("slow", "Name"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := d.ReadInputs(); err == nil {
		t.Fatal("ReadInputs before Start must error")
	}
	want := map[string][]string{"default": {"Count", "Run", "Setpoint", "Speed"}, "slow": {"Name"}}
	if got := d.ScanClasses(); !reflect.DeepEqual(got, want) {
		t.Errorf("ScanClasses = %v, want %v", got, want)
	}
	if got := d.InputNames(); !reflect.DeepEqual(got, []string{"Count", "Name", "Run", "Setpoint", "Speed", "plant__Online"}) {
		t.Errorf("InputNames = %v", got)
	}
	if got := d.OutputNames(); !reflect.DeepEqual(got, []string{"Setpoint"}) {
		t.Errorf("OutputNames = %v", got)
	}
}

func TestValidate(t *testing.T) {
	m := Manifest{Tags: []TagBinding{
		{Name: "", NodeID: "ns=2;s=A", Type: "Double"},
		{Name: "B", NodeID: "garbage", Type: "Double"},
		{Name: "C", NodeID: "ns=2;s=C", Type: "Dobule"},
		{Name: "C", NodeID: "ns=2;s=C2", Type: "Double"},
	}}
	err := m.Validate()
	if err == nil {
		t.Fatal("a broken manifest validated")
	}
	for _, want := range []string{"missing name", "nodeid \"garbage\"", "unknown type \"Dobule\" (did you mean Double?)", "duplicate tag name \"C\""} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

func TestReadsAndQuality(t *testing.T) {
	ts := startServer(t, freePort(t))
	d := startDriver(t, ts.url, manifest(2))

	waitFor(t, "first poll", func() bool { return read(t, d)["plant__Online"] == true })
	got := read(t, d)
	want := nio.Values{"Speed": 12.5, "Count": int64(7), "Run": true, "Name": "pump-1", "Setpoint": 0.0, "plant__Online": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot = %v, want %v", got, want)
	}
	if q := d.Quality(); len(q) != 0 {
		t.Errorf("quality on a healthy session = %v, want empty", q)
	}

	ts.set(t, "Setpoint", 99.0)
	waitFor(t, "a changed value", func() bool { return read(t, d)["Setpoint"] == 99.0 })

	h := d.Health()
	if h.State != "connected" || h.Policy != "None" || h.Tags != 5 || h.Reads == 0 {
		t.Errorf("health = %+v", h)
	}

	// ReadInputsInto refills in place and prunes nothing it still holds.
	dst := nio.Values{"stale-key": 1}
	if err := d.ReadInputsInto(dst); err != nil {
		t.Fatal(err)
	}
	if _, ok := dst["stale-key"]; ok {
		t.Error("ReadInputsInto kept a key the driver does not deliver")
	}
	if dst["Setpoint"] != 99.0 {
		t.Errorf("ReadInputsInto Setpoint = %v", dst["Setpoint"])
	}
}

func TestNamespaceRemap(t *testing.T) {
	ts := startServer(t, freePort(t))
	// The manifest claims ns=7; the URI resolves to the server's real index.
	d := startDriver(t, ts.url, manifest(7))
	waitFor(t, "remapped read", func() bool { return read(t, d)["Count"] == int64(7) })
}

func TestBadNodeIsBadNotFatal(t *testing.T) {
	ts := startServer(t, freePort(t))
	m := manifest(2)
	m.Tags = append(m.Tags, TagBinding{Name: "Ghost", NodeID: "ns=2;s=Ghost", Type: "Double"})
	d := startDriver(t, ts.url, m)
	waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Speed"]; return ok })
	q := d.Quality()
	if q["Ghost"] != nio.Bad {
		t.Errorf("Ghost quality = %v, want bad", q["Ghost"])
	}
	if _, ok := q["Speed"]; ok {
		t.Errorf("Speed quality = %v, want good", q["Speed"])
	}
	if _, ok := read(t, d)["Ghost"]; ok {
		t.Error("a refused node delivered a value")
	}
	if h := d.Health(); h.BadTags != 1 || !strings.Contains(h.LastError, "Ghost") {
		t.Errorf("health = %+v", h)
	}
}

func TestWrites(t *testing.T) {
	ts := startServer(t, freePort(t))
	d := startDriver(t, ts.url, manifest(2))
	waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Setpoint"]; return ok })

	// Baseline: recorded, never sent.
	if err := d.WriteOutputs(nio.Values{"Setpoint": 5.0}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := ts.get(t, "Setpoint"); got != 0.0 {
		t.Fatalf("baseline wrote %v to the server", got)
	}
	// A change is a command.
	if err := d.WriteOutputs(nio.Values{"Setpoint": 42.5}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "write to land", func() bool { return ts.get(t, "Setpoint") == 42.5 })
	waitFor(t, "read-back", func() bool { return read(t, d)["Setpoint"] == 42.5 })
	if h := d.Health(); h.Writes != 1 {
		t.Errorf("writes = %d, want 1", h.Writes)
	}
	// A non-writable binding is ignored; a wrong-typed value is dropped.
	_ = d.WriteOutputs(nio.Values{"Speed": 1.0, "Setpoint": "not a number"})
	time.Sleep(100 * time.Millisecond)
	if got := ts.get(t, "Setpoint"); got != 42.5 {
		t.Errorf("a bad value reached the server: %v", got)
	}
}

func TestWriteGate(t *testing.T) {
	ts := startServer(t, freePort(t))
	d := startDriver(t, ts.url, manifest(2))
	waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Setpoint"]; return ok })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 0.0}) // baseline
	open := false
	d.SetWriteGate(func() bool { return open })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 9.0})
	time.Sleep(100 * time.Millisecond)
	if ts.get(t, "Setpoint") != 0.0 {
		t.Fatal("a closed gate let a command through")
	}
	open = true
	_ = d.WriteOutputs(nio.Values{"Setpoint": 9.0})
	waitFor(t, "gated write", func() bool { return ts.get(t, "Setpoint") == 9.0 })
}

func TestReconnectAndStale(t *testing.T) {
	port := freePort(t)
	ts := startServer(t, port)
	d := startDriver(t, ts.url, manifest(2))
	waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Speed"]; return ok })

	_ = ts.srv.Close()
	waitFor(t, "offline", func() bool { return read(t, d)["plant__Online"] == false })
	if v := read(t, d)["Speed"]; v != 12.5 {
		t.Errorf("value did not hold across the outage: %v", v)
	}
	if q := d.Quality(); q["Speed"] != nio.Stale {
		t.Errorf("Speed quality during outage = %v, want stale", q["Speed"])
	}
	// A write queued while down waits for the reconnect.
	_ = d.WriteOutputs(nio.Values{"Setpoint": 0.0}) // baseline
	_ = d.WriteOutputs(nio.Values{"Setpoint": 3.0})
	if h := d.Health(); h.QueuedWrites != 1 || h.State == "connected" {
		t.Errorf("health during outage = %+v", h)
	}

	ts2 := startServer(t, port)
	waitFor(t, "reconnect", func() bool { return read(t, d)["plant__Online"] == true })
	waitFor(t, "queued write flushed", func() bool { return ts2.get(t, "Setpoint") == 3.0 })
	if q := d.Quality(); len(q) != 0 {
		t.Errorf("quality after reconnect = %v", q)
	}
}

func TestBrowseFindsTheVariables(t *testing.T) {
	ts := startServer(t, freePort(t))
	sess, err := Dial(context.Background(), ts.url, Connect{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	br, err := Browse(context.Background(), sess.Client, ObjectsFolder, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Variable{}
	for _, v := range br.Variables {
		byName[strings.Join(v.Path, "/")] = v
	}
	for name, typ := range map[string]string{"Plant/Speed": "Double", "Plant/Count": "Int32", "Plant/Run": "Boolean", "Plant/Name": "String"} {
		v, ok := byName[name]
		if !ok {
			t.Errorf("browse missed %s (found %v)", name, keys(byName))
			continue
		}
		if v.Type != typ || !v.Scalar || v.Namespace != "urn:nautilus:test" || v.NodeID != fmt.Sprintf("ns=%d;s=%s", ts.ns.ID(), strings.TrimPrefix(name, "Plant/")) || v.Writable || !v.Readable {
			t.Errorf("%s = %+v", name, v)
		}
	}
	if sp := byName["Plant/Setpoint"]; !sp.Writable {
		t.Errorf("Setpoint should browse as writable: %+v", sp)
	}
	if len(br.Namespaces) < 2 || br.Namespaces[1] != "urn:nautilus:test" {
		t.Errorf("namespaces = %v", br.Namespaces)
	}
}

func keys(m map[string]Variable) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSelectEndpoint(t *testing.T) {
	eps := []*ua.EndpointDescription{
		{SecurityPolicyURI: ua.SecurityPolicyURINone, SecurityMode: ua.MessageSecurityModeNone, SecurityLevel: 0},
		{SecurityPolicyURI: ua.SecurityPolicyURIBasic256Sha256, SecurityMode: ua.MessageSecurityModeSign, SecurityLevel: 3},
		{SecurityPolicyURI: ua.SecurityPolicyURIBasic256Sha256, SecurityMode: ua.MessageSecurityModeSignAndEncrypt, SecurityLevel: 4},
	}
	ep, err := selectEndpoint(eps, Security{})
	if err != nil || ep.SecurityMode != ua.MessageSecurityModeSignAndEncrypt {
		t.Errorf("open request picked %v, %v (want the strongest)", ep, err)
	}
	ep, err = selectEndpoint(eps, Security{Policy: "basic256sha256", Mode: "Sign"})
	if err != nil || ep.SecurityMode != ua.MessageSecurityModeSign {
		t.Errorf("explicit request picked %v, %v", ep, err)
	}
	ep, err = selectEndpoint(eps, Security{Policy: "None"})
	if err != nil || ep.SecurityPolicyURI != ua.SecurityPolicyURINone {
		t.Errorf("None picked %v, %v", ep, err)
	}
	if _, err = selectEndpoint(eps, Security{Policy: "Basic128Rsa15"}); err == nil || !strings.Contains(err.Error(), "offers") {
		t.Errorf("an unoffered policy did not list what is offered: %v", err)
	}
}

func TestValueCodec(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		in   any
		want any
		bad  bool
	}{
		{"Int16", int64(300), int16(300), false},
		{"Int16", int64(70000), nil, true},
		{"UInt16", int64(-1), nil, true},
		{"Byte", float64(200), uint8(200), false},
		{"Float", 1.5, float32(1.5), false},
		{"Double", int64(2), 2.0, false},
		{"Boolean", int64(1), true, false},
		{"String", 12, "12", false},
		{"DateTime", int64(1_700_000_000_000), time.UnixMilli(1_700_000_000_000).UTC(), false},
	} {
		v, err := toVariant(tc.typ, tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("%s(%v) encoded as %v, want an error", tc.typ, tc.in, v.Value())
			}
			continue
		}
		if err != nil {
			t.Errorf("%s(%v): %v", tc.typ, tc.in, err)
			continue
		}
		if !reflect.DeepEqual(v.Value(), tc.want) {
			t.Errorf("%s(%v) = %v (%T), want %v (%T)", tc.typ, tc.in, v.Value(), v.Value(), tc.want, tc.want)
		}
	}
	arr := ua.MustVariant([]float64{1, 2})
	if _, ok := toValue(arr); ok {
		t.Error("an array decoded as a scalar")
	}
}

// The loader's contract: the companion named as every single-connection
// driver names it, the IEC type each input seeds as, security typos caught
// offline, and the check-time warnings.
func TestLoaderContract(t *testing.T) {
	for in, want := range map[string]string{"": "opcua__Online", "opcua-2": "opcua_2__Online", "Line 3": "Line_3__Online", "2nd": "_nd__Online"} {
		if got := OnlineTagFor(in); got != want {
			t.Errorf("OnlineTagFor(%q) = %q, want %q", in, got, want)
		}
	}
	m := manifest(2)
	m.Tags = append(m.Tags,
		TagBinding{Name: "Big", NodeID: "ns=2;s=Big", Type: "UInt32"},
		TagBinding{Name: "Ratio", NodeID: "ns=2;s=Ratio", Type: "Float"},
		TagBinding{Name: "When", NodeID: "ns=2;s=When", Type: "DateTime"},
		TagBinding{Name: "Off", NodeID: "ns=2;s=Off", Type: "Double", ScanClass: NoPoll})
	d, err := New("opc.tcp://192.0.2.1:4840", m, WithName("gw-1"))
	if err != nil {
		t.Fatal(err)
	}
	if d.OnlineTagName() != "gw_1__Online" {
		t.Errorf("companion = %q", d.OnlineTagName())
	}
	for name, want := range map[string]string{
		"Speed": "LREAL", "Count": "DINT", "Run": "BOOL", "Name": "STRING", "Setpoint": "LREAL",
		"Big": "LINT", "Ratio": "REAL", "When": "LINT", "gw_1__Online": "BOOL", "Off": "", "Nope": "",
	} {
		if got := d.InputType(name); got != want {
			t.Errorf("InputType(%s) = %q, want %q", name, got, want)
		}
	}
	if d.Online() {
		t.Error("Online before Start")
	}
	if w := d.Warnings(); len(w) != 1 || !strings.Contains(w[0], "pki-dir") {
		t.Errorf("warnings = %q, want one about pki-dir", w)
	}
	d, err = New("opc.tcp://192.0.2.1:4840", m, WithOnlineTag("Gateway__Online"), WithSecurity("None", ""), WithAuth("op", ""))
	if err != nil {
		t.Fatal(err)
	}
	if d.OnlineTagName() != "Gateway__Online" || d.InputType("Gateway__Online") != "BOOL" {
		t.Errorf("WithOnlineTag: companion %q", d.OnlineTagName())
	}
	if w := d.Warnings(); len(w) != 1 || !strings.Contains(w[0], "NAUTILUS_OPCUA_PASSWORD") {
		t.Errorf("warnings = %q, want one about the password", w)
	}
	for _, sec := range [][2]string{{"Basic256Sha265", ""}, {"", "Encrypt"}} {
		if _, err := New("opc.tcp://192.0.2.1:4840", m, WithSecurity(sec[0], sec[1])); err == nil {
			t.Errorf("New accepted security %q/%q", sec[0], sec[1])
		}
	}
	if _, err := New("opc.tcp://192.0.2.1:4840", m, WithSecurity("aes128_sha256_rsaoaep", "SignAndEncrypt")); err != nil {
		t.Errorf("New refused a valid policy spelling: %v", err)
	}
}
