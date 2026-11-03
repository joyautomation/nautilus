package server

import (
	"bytes"
	"context"
	"crypto/rsa"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// writeProgram declares one tag of every kind the Write service converts,
// an input, an output, a struct, an array, a tag to force and one left out
// of opcua.writable.
const writeProgram = `
PROGRAM Main
VAR_EXTERNAL
  Flag    : BOOL;
  V_SINT  : SINT;
  V_BYTE  : BYTE;
  V_INT   : INT;
  V_UINT  : UINT;
  V_DINT  : DINT;
  V_LINT  : LINT;
  V_ULINT : ULINT;
  Speed   : REAL;
  Delay   : TIME;
  Label   : STRING;
  State   : Mode;
  P101    : Pump;
  Levels  : ARRAY[0..2] OF REAL;
  Level   : REAL;
  Pump1   : BOOL;
  Locked  : REAL;
  Closed  : REAL;
  Hi      : REAL;
  Lo      : REAL;
END_VAR
END_PROGRAM
`

// writePatterns opens everything above but Closed, and of P101 only Drive.
var writePatterns = []string{"Flag", "V_*", "Speed", "Delay", "Label", "State", "P101.Drive", "Levels", "Level", "Pump1", "Locked", "Hi", "Lo"}

func writeRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	tags := []runtime.TagDef{
		runtime.State("Flag", false),
		runtime.Typed("V_SINT", runtime.RoleSetpoint, "SINT"),
		runtime.Typed("V_BYTE", runtime.RoleSetpoint, "BYTE"),
		runtime.Typed("V_INT", runtime.RoleSetpoint, "INT"),
		runtime.Typed("V_UINT", runtime.RoleSetpoint, "UINT"),
		runtime.Typed("V_DINT", runtime.RoleSetpoint, "DINT"),
		runtime.Typed("V_LINT", runtime.RoleSetpoint, "LINT"),
		runtime.Typed("V_ULINT", runtime.RoleSetpoint, "ULINT"),
		runtime.Setpoint("Speed", 0.0),
		runtime.Typed("Delay", runtime.RoleSetpoint, "TIME"),
		runtime.Setpoint("Label", "Pump 1"),
		runtime.Typed("State", runtime.RoleState, "Mode"),
		runtime.Typed("P101", runtime.RoleSetpoint, "Pump"),
		runtime.Setpoint("Levels", []any{1.0, 2.0, 3.0}),
		runtime.Input("Level", runtime.Comms()),
		runtime.Output("Pump1", runtime.Init(false)),
		runtime.Setpoint("Locked", 0.0),
		runtime.Setpoint("Closed", 0.0),
		runtime.Setpoint("Hi", 0.0),
		runtime.Setpoint("Lo", 0.0),
	}
	tags[14].Type = "REAL"
	rt, err := runtime.New(runtime.Options{
		Program:   writeProgram,
		Libraries: []string{typesST},
		Driver:    nio.NewMemory(),
		Scan:      time.Second,
		Tags:      tags,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// syncBuffer is a log sink the server writes from its own goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// writeFixture is a secure server with opcua.writable, a trusted writer
// client on Basic256Sha256/SignAndEncrypt and an anonymous client on None.
type writeFixture struct {
	rt     *runtime.Runtime
	s      *Server
	dir    string
	der    []byte
	key    *rsa.PrivateKey
	writer *opcua.Client
	anon   *opcua.Client
	log    *syncBuffer
}

const writerURI = "urn:test:scada"

func startWritable(t *testing.T) *writeFixture {
	t.Helper()
	f := &writeFixture{rt: writeRuntime(t), log: &syncBuffer{}}
	f.dir = filepath.Join(t.TempDir(), "pki")
	s, err := New(f.rt, Options{Endpoint: "localhost:0", Name: "lift-station", Sweep: 20 * time.Millisecond, PKI: f.dir,
		Writable: writePatterns, Logger: slog.New(slog.NewTextHandler(f.log, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	f.s = s
	f.der, f.key = clientCert(t, writerURI, time.Now().Add(-time.Hour), 24*time.Hour)
	trust(t, f.dir, f.der)
	f.writer = f.connect(t)
	f.anon = f.connectAnonymous(t)
	return f
}

func (f *writeFixture) connect(t *testing.T) *opcua.Client {
	t.Helper()
	ep := endpointFor(t, f.s, ua.SecurityPolicyURIBasic256Sha256, ua.MessageSecurityModeSignAndEncrypt)
	c, err := secureConnect(f.s, ep, f.der, f.key, writerURI)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeClient(c) })
	return c
}

func (f *writeFixture) connectAnonymous(t *testing.T, extra ...opcua.Option) *opcua.Client {
	t.Helper()
	opts := append([]opcua.Option{opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false)}, extra...)
	c, err := opcua.NewClient(f.s.URL(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeClient(c) })
	return c
}

func closeClient(c *opcua.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.Close(ctx)
}

// wv is a Value write of x to ns=1;s=addr, the DataValue carrying only the
// value.
func wv(addr string, x any) *ua.WriteValue {
	return &ua.WriteValue{NodeID: ua.NewStringNodeID(1, addr), AttributeID: ua.AttributeIDValue,
		Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(x)}}
}

// write sends one Write and returns its per-item results.
func write(t *testing.T, c *opcua.Client, items ...*ua.WriteValue) []ua.StatusCode {
	t.Helper()
	resp, err := c.Write(context.Background(), &ua.WriteRequest{NodesToWrite: items})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != len(items) {
		t.Fatalf("%d results for %d items", len(resp.Results), len(items))
	}
	return resp.Results
}

func stored(t *testing.T, rt *runtime.Runtime, path string) any {
	t.Helper()
	v, ok := rt.Tags().ReadPath(path)
	if !ok {
		t.Fatalf("no %s in the store", path)
	}
	return v
}

// Every row of the status table (opcua-server-writes §3.3) and of the
// conversion table (§3.2), from a writer session, one item per request so
// each stands alone; a Good item is checked in the store, a refused one is
// checked untouched.
func TestWriteStatusTable(t *testing.T) {
	f := startWritable(t)
	if err := f.rt.Tags().Force("Locked", 7.0); err != nil {
		t.Fatal(err)
	}
	withMask := func(w *ua.WriteValue, mask byte, status ua.StatusCode) *ua.WriteValue {
		w.Value.EncodingMask |= mask
		w.Value.Status = status
		w.Value.SourceTimestamp = time.Now().Add(-time.Hour)
		w.Value.ServerTimestamp = time.Now().Add(-time.Hour)
		return w
	}
	ranged := wv("Speed", 4.0)
	ranged.IndexRange = "0"
	desc := wv("Speed", "x")
	desc.AttributeID = ua.AttributeIDDescription
	novalue := wv("Speed", 1.0)
	novalue.Value.EncodingMask = 0
	arr := wv("Speed", []float64{1, 2})

	cases := []struct {
		name  string
		item  *ua.WriteValue
		want  ua.StatusCode
		path  string // checked after: the value now held
		value any
	}{
		{"no such node", wv("Nope", 1.0), ua.StatusBadNodeIDUnknown, "", nil},
		{"server object", &ua.WriteValue{NodeID: ua.NewNumericNodeID(0, id.Server_ServerStatus_BuildInfo_ProductName), AttributeID: ua.AttributeIDValue, Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant("x")}}, ua.StatusBadUserAccessDenied, "", nil},
		{"not the Value attribute", desc, ua.StatusBadNotWritable, "", nil},
		{"an Object", wv("P101", 1.0), ua.StatusBadNotWritable, "", nil},
		{"the folder", &ua.WriteValue{NodeID: ua.NewNumericNodeID(1, folderID), AttributeID: ua.AttributeIDValue, Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(1.0)}}, ua.StatusBadNotWritable, "", nil},
		{"not matched", wv("Closed", 1.0), ua.StatusBadNotWritable, "Closed", 0.0},
		{"member not matched", wv("P101.Running", true), ua.StatusBadNotWritable, "P101.Running", false},
		{"input, matched", wv("Level", 1.0), ua.StatusBadNotWritable, "", nil},
		{"forced", wv("Locked", 1.0), ua.StatusBadLocked, "Locked", 7.0},
		{"array", wv("Levels", []float64{9, 9, 9}), ua.StatusBadWriteNotSupported, "Levels[0]", 1.0},
		{"IndexRange", ranged, ua.StatusBadWriteNotSupported, "Speed", 0.0},
		{"array Variant to a scalar", arr, ua.StatusBadWriteNotSupported, "Speed", 0.0},
		{"Bad StatusCode", withMask(wv("Speed", 2.0), ua.DataValueStatusCode, ua.StatusBadDeviceFailure), ua.StatusBadWriteNotSupported, "Speed", 0.0},
		{"Uncertain StatusCode", withMask(wv("Speed", 2.0), ua.DataValueStatusCode, ua.StatusUncertainLastUsableValue), ua.StatusBadWriteNotSupported, "Speed", 0.0},
		{"no value", novalue, ua.StatusBadTypeMismatch, "Speed", 0.0},
		// A Good StatusCode and timestamps are what most clients send.
		{"Good StatusCode and timestamps", withMask(wv("Speed", 2.5), ua.DataValueStatusCode|ua.DataValueSourceTimestamp|ua.DataValueServerTimestamp, ua.StatusGood), ua.StatusGood, "Speed", 2.5},

		{"Boolean", wv("Flag", true), ua.StatusGood, "Flag", true},
		{"Int32 to Boolean", wv("Flag", int32(0)), ua.StatusBadTypeMismatch, "Flag", true},
		{"String to Boolean", wv("Flag", "false"), ua.StatusBadTypeMismatch, "Flag", true},

		{"SByte low", wv("V_SINT", int8(-128)), ua.StatusGood, "V_SINT", int64(-128)},
		{"Int64 into SINT", wv("V_SINT", int64(127)), ua.StatusGood, "V_SINT", int64(127)},
		{"past SINT", wv("V_SINT", int64(128)), ua.StatusBadOutOfRange, "V_SINT", int64(127)},
		{"Byte high", wv("V_BYTE", uint8(255)), ua.StatusGood, "V_BYTE", int64(255)},
		{"past BYTE", wv("V_BYTE", int16(256)), ua.StatusBadOutOfRange, "V_BYTE", int64(255)},
		{"negative BYTE", wv("V_BYTE", int16(-1)), ua.StatusBadOutOfRange, "V_BYTE", int64(255)},
		{"INT low", wv("V_INT", int64(-32768)), ua.StatusGood, "V_INT", int64(-32768)},
		{"past INT", wv("V_INT", int64(70000)), ua.StatusBadOutOfRange, "V_INT", int64(-32768)},
		{"Double to INT", wv("V_INT", 5.0), ua.StatusBadTypeMismatch, "V_INT", int64(-32768)},
		{"Float to INT", wv("V_INT", float32(5)), ua.StatusBadTypeMismatch, "V_INT", int64(-32768)},
		{"UINT high", wv("V_UINT", uint16(65535)), ua.StatusGood, "V_UINT", int64(65535)},
		{"DINT", wv("V_DINT", int32(math.MinInt32)), ua.StatusGood, "V_DINT", int64(math.MinInt32)},
		{"LINT high", wv("V_LINT", int64(math.MaxInt64)), ua.StatusGood, "V_LINT", int64(math.MaxInt64)},
		{"UInt64 past LINT", wv("V_LINT", uint64(math.MaxInt64)+1), ua.StatusBadOutOfRange, "V_LINT", int64(math.MaxInt64)},
		{"UInt64 into ULINT", wv("V_ULINT", uint64(math.MaxUint64)), ua.StatusGood, "V_ULINT", int64(-1)},

		{"Double", wv("Speed", 12.5), ua.StatusGood, "Speed", 12.5},
		{"Float", wv("Speed", float32(1.5)), ua.StatusGood, "Speed", 1.5},
		{"Int64 at 2^53", wv("Speed", int64(1)<<53), ua.StatusGood, "Speed", float64(1 << 53)},
		{"Int64 past 2^53", wv("Speed", int64(1)<<53+1), ua.StatusBadOutOfRange, "Speed", float64(1 << 53)},
		{"Int64 below -2^53", wv("Speed", -(int64(1)<<53)-1), ua.StatusBadOutOfRange, "Speed", float64(1 << 53)},
		{"UInt64 past 2^53", wv("Speed", uint64(1)<<60), ua.StatusBadOutOfRange, "Speed", float64(1 << 53)},
		{"NaN", wv("Speed", math.NaN()), ua.StatusBadOutOfRange, "Speed", float64(1 << 53)},
		{"Inf", wv("Speed", math.Inf(1)), ua.StatusBadOutOfRange, "Speed", float64(1 << 53)},
		{"Boolean to Double", wv("Speed", true), ua.StatusBadTypeMismatch, "Speed", float64(1 << 53)},

		{"Duration as Double", wv("Delay", 1500.0), ua.StatusGood, "Delay", int64(1500)},
		{"Duration as Int32", wv("Delay", int32(250)), ua.StatusGood, "Delay", int64(250)},
		{"fraction of a ms", wv("Delay", 1.5), ua.StatusBadOutOfRange, "Delay", int64(250)},
		{"String to Duration", wv("Delay", "1s"), ua.StatusBadTypeMismatch, "Delay", int64(250)},

		{"enum member", wv("State", int32(10)), ua.StatusGood, "State", "Run"},
		{"enum non-member", wv("State", int32(5)), ua.StatusBadOutOfRange, "State", "Run"},
		{"Double to enum", wv("State", 0.0), ua.StatusBadTypeMismatch, "State", "Run"},

		{"String", wv("Label", "Pump 2"), ua.StatusGood, "Label", "Pump 2"},
		{"empty (null) String", wv("Label", ""), ua.StatusGood, "Label", ""},
		{"Int32 to String", wv("Label", int32(2)), ua.StatusBadTypeMismatch, "Label", ""},

		{"member under a matched ancestor", wv("P101.Drive.Speed", 1450.0), ua.StatusGood, "P101.Drive.Speed", 1450.0},
		{"output, matched", wv("Pump1", true), ua.StatusGood, "Pump1", true},
		{"another casing", wv("speed", 3.0), ua.StatusGood, "Speed", 3.0},
	}
	for _, c := range cases {
		got := write(t, f.writer, c.item)[0]
		if got != c.want {
			t.Errorf("%s: %s, want %s", c.name, statusName(got), statusName(c.want))
		}
		if c.path != "" {
			if v := stored(t, f.rt, c.path); v != c.value {
				t.Errorf("%s: %s holds %v (%T), want %v (%T)", c.name, c.path, v, v, c.value, c.value)
			}
		}
	}
}

// A session that is not a writer is refused every Value write of a tag
// with Bad_UserAccessDenied, matched or not, as before writes existed;
// anonymous on None, and None with a client certificate in CreateSession
// alike. An Object or another attribute stays Bad_NotWritable.
func TestNonWriterSessionsAreRefused(t *testing.T) {
	f := startWritable(t)
	withCert := f.connectAnonymous(t, opcua.Certificate(f.der), opcua.PrivateKey(f.key), opcua.ApplicationURI(writerURI))
	desc := wv("Speed", "x")
	desc.AttributeID = ua.AttributeIDDescription
	for name, c := range map[string]*opcua.Client{"anonymous on None": f.anon, "None with a certificate": withCert} {
		got := write(t, c, wv("Speed", 9.0), wv("Closed", 9.0), wv("P101.Drive.Speed", 9.0), wv("P101", 1.0), desc)
		want := []ua.StatusCode{ua.StatusBadUserAccessDenied, ua.StatusBadUserAccessDenied, ua.StatusBadUserAccessDenied, ua.StatusBadNotWritable, ua.StatusBadNotWritable}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s item %d: %s, want %s", name, i, statusName(got[i]), statusName(want[i]))
			}
		}
		if v := stored(t, f.rt, "Speed"); v != 0.0 {
			t.Errorf("%s: Speed = %v after a refused write", name, v)
		}
	}
}

// AccessLevel is the node's: CurrentWrite on a matched non-input tag.
// UserAccessLevel is the session's: the node's for a writer, read-only
// for anyone else. AccessLevelEx follows AccessLevel (its low byte is
// AccessLevel's, Part 3 §5.6.2). The Server object stays read-only.
func TestAccessLevelsPerNodeAndSession(t *testing.T) {
	f := startWritable(t)
	const r, rw = uint8(ua.AccessLevelTypeCurrentRead), uint8(ua.AccessLevelTypeCurrentRead | ua.AccessLevelTypeCurrentWrite)
	cases := []struct {
		addr               string
		node, writer, anon uint8
	}{
		{"Speed", rw, rw, r},
		{"P101.Drive.Speed", rw, rw, r},
		{"P101.Running", r, r, r},
		{"Closed", r, r, r},
		{"Level", r, r, r}, // an input, though matched
		{"Pump1", rw, rw, r},
		{"Levels", rw, rw, r}, // matched; the write itself is Bad_WriteNotSupported until slice 4
	}
	for _, c := range cases {
		nid := ua.NewStringNodeID(1, c.addr)
		for who, cl := range map[string]*opcua.Client{"writer": f.writer, "anonymous": f.anon} {
			if got := read(t, cl, nid, ua.AttributeIDAccessLevel).Value.Value(); got != c.node {
				t.Errorf("%s AccessLevel (%s) = %v, want %d", c.addr, who, got, c.node)
			}
			if got := read(t, cl, nid, ua.AttributeIDAccessLevelEx).Value.Value(); got != uint32(c.node) {
				t.Errorf("%s AccessLevelEx (%s) = %v, want %d", c.addr, who, got, c.node)
			}
		}
		if got := read(t, f.writer, nid, ua.AttributeIDUserAccessLevel).Value.Value(); got != c.writer {
			t.Errorf("%s UserAccessLevel (writer) = %v, want %d", c.addr, got, c.writer)
		}
		if got := read(t, f.anon, nid, ua.AttributeIDUserAccessLevel).Value.Value(); got != c.anon {
			t.Errorf("%s UserAccessLevel (anonymous) = %v, want %d", c.addr, got, c.anon)
		}
	}
	ns := ua.NewNumericNodeID(0, id.Server_NamespaceArray)
	if got := read(t, f.writer, ns, ua.AttributeIDUserAccessLevel).Value.Value(); got != r {
		t.Errorf("NamespaceArray UserAccessLevel (writer) = %v, want read-only", got)
	}
	if got := write(t, f.writer, &ua.WriteValue{NodeID: ns, AttributeID: ua.AttributeIDValue, Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant([]string{"x"})}})[0]; got == ua.StatusGood {
		t.Error("a writer rewrote NamespaceArray")
	}
}

// The items of one Write that pass the protocol checks land through one
// front-door batch, under one store lock: a reader of the store never sees
// a Hi/Lo pair half written. Refused items do not hold back the others.
func TestWriteRequestIsOneBatch(t *testing.T) {
	f := startWritable(t)
	stop := make(chan struct{})
	torn := make(chan [2]any, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			snap := f.rt.Tags().Snapshot()
			hi, lo := runtime.Plain(snap["Hi"]), runtime.Plain(snap["Lo"])
			if hi != lo {
				select {
				case torn <- [2]any{hi, lo}:
				default:
				}
				return
			}
		}
	}()
	for i := 1; i <= 300; i++ {
		got := write(t, f.writer, wv("Hi", float64(i)), wv("Closed", 1.0), wv("Lo", float64(i)))
		if got[0] != ua.StatusGood || got[1] != ua.StatusBadNotWritable || got[2] != ua.StatusGood {
			t.Fatalf("write %d: %v", i, got)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case p := <-torn:
		t.Fatalf("a reader saw Hi=%v Lo=%v: the request did not land as one batch", p[0], p[1])
	default:
	}
}

// MaxNodesPerWrite is advertised, and a Write naming more is refused whole.
func TestMaxNodesPerWriteIsAdvertisedAndEnforced(t *testing.T) {
	f := startWritable(t)
	lim := read(t, f.writer, ua.NewNumericNodeID(0, id.Server_ServerCapabilities_OperationLimits_MaxNodesPerWrite), ua.AttributeIDValue)
	if got, _ := lim.Value.Value().(uint32); got != maxNodesPerWrite {
		t.Fatalf("MaxNodesPerWrite reads %v, want %d", lim.Value.Value(), maxNodesPerWrite)
	}
	items := make([]*ua.WriteValue, maxNodesPerWrite+1)
	for i := range items {
		items[i] = wv("Speed", 1.0)
	}
	_, err := f.writer.Write(context.Background(), &ua.WriteRequest{NodesToWrite: items})
	if err == nil || !strings.Contains(err.Error(), "BadTooManyOperations") {
		t.Fatalf("a Write of %d nodes: %v, want Bad_TooManyOperations", len(items), err)
	}
	if got := write(t, f.writer, items[:maxNodesPerWrite]...); got[0] != ua.StatusGood {
		t.Fatalf("a Write of exactly the limit: %s", statusName(got[0]))
	}
}

// A standby replica refuses writes with Bad_InvalidState; the leader takes
// them. Status says which.
func TestStandbyRefusesWrites(t *testing.T) {
	f := startWritable(t)
	var lead bool
	var mu sync.Mutex
	f.s.SetWriteGate(func() bool { mu.Lock(); defer mu.Unlock(); return lead })
	if got := write(t, f.writer, wv("Speed", 5.0))[0]; got != ua.StatusBadInvalidState {
		t.Errorf("standby: %s, want Bad_InvalidState", statusName(got))
	}
	if !f.s.Status().Standby {
		t.Error("Status does not say standby")
	}
	if v := stored(t, f.rt, "Speed"); v != 0.0 {
		t.Errorf("standby stored %v", v)
	}
	mu.Lock()
	lead = true
	mu.Unlock()
	if got := write(t, f.writer, wv("Speed", 5.0))[0]; got != ua.StatusGood {
		t.Errorf("leader: %s", statusName(got))
	}
	if f.s.Status().Standby {
		t.Error("Status says standby on the leader")
	}
}

// Trust is read again on every Write: a certificate taken out of trusted/
// stops writing on the channel it already has, every item refused.
func TestUntrustedCertificateStopsWritingOnItsChannel(t *testing.T) {
	f := startWritable(t)
	if got := write(t, f.writer, wv("Speed", 1.0))[0]; got != ua.StatusGood {
		t.Fatalf("trusted: %s", statusName(got))
	}
	if err := os.Remove(filepath.Join(f.dir, "trusted", "certs", "client.der")); err != nil {
		t.Fatal(err)
	}
	got := write(t, f.writer, wv("Speed", 2.0), wv("Closed", 2.0), wv("Nope", 2.0))
	for i, code := range got {
		if code != ua.StatusBadUserAccessDenied {
			t.Errorf("untrusted item %d: %s, want Bad_UserAccessDenied", i, statusName(code))
		}
	}
	if v := stored(t, f.rt, "Speed"); v != 1.0 {
		t.Errorf("Speed = %v after the certificate was untrusted", v)
	}
	// Reads go on as they were.
	if dv := read(t, f.writer, ua.NewStringNodeID(1, "Speed"), ua.AttributeIDValue); dv.Status != ua.StatusGood {
		t.Errorf("read after untrust: %s", dv.Status)
	}
	trust(t, f.dir, f.der)
	if got := write(t, f.writer, wv("Speed", 3.0))[0]; got != ua.StatusGood {
		t.Errorf("trusted again: %s", statusName(got))
	}
}

// Every landed item is an Info line naming the node, old and new values,
// the session and the client; a refusal is a Warn line once per client,
// node and reason. Status counts both.
func TestWriteAuditAndCounters(t *testing.T) {
	f := startWritable(t)
	write(t, f.writer, wv("Speed", 4.5))
	write(t, f.writer, wv("Closed", 1.0))
	write(t, f.writer, wv("Closed", 2.0))
	write(t, f.writer, wv("V_INT", int64(70000)))
	log := f.log.String()
	thumb := Thumbprint(f.der)[:16]
	var landed, closed, ranged int
	for _, line := range strings.Split(log, "\n") {
		switch {
		case strings.Contains(line, `msg="opcua: write"`):
			landed++
			for _, want := range []string{`node="ns=1;s=Speed"`, "old=0", "new=4.5", "session=", "client=" + writerURI, "thumbprint=" + thumb, "remote=127.0.0.1:"} {
				if !strings.Contains(line, want) {
					t.Errorf("audit line lacks %q: %s", want, line)
				}
			}
			if strings.Contains(line, "session= ") {
				t.Errorf("audit line names no session: %s", line)
			}
		case strings.Contains(line, `msg="opcua: write refused"`) && strings.Contains(line, "Closed"):
			closed++
			if !strings.Contains(line, "level=WARN") || !strings.Contains(line, "status=BadNotWritable") || !strings.Contains(line, "thumbprint="+thumb) {
				t.Errorf("refusal line: %s", line)
			}
		case strings.Contains(line, `msg="opcua: write refused"`) && strings.Contains(line, "V_INT"):
			ranged++
			if !strings.Contains(line, "status=BadOutOfRange") {
				t.Errorf("refusal line: %s", line)
			}
		}
	}
	if landed != 1 || closed != 1 || ranged != 1 {
		t.Errorf("%d landed, %d Closed and %d V_INT refusal lines, want 1 each:\n%s", landed, closed, ranged, log)
	}
	st := f.s.Status()
	if st.Writes != 1 || st.WritesRefused != 3 {
		t.Errorf("Status writes %d refused %d, want 1 and 3", st.Writes, st.WritesRefused)
	}
	if len(st.Writable) != len(writePatterns) {
		t.Errorf("Status writable %v", st.Writable)
	}
}

// A write needs no notification of its own: it moves the tag's generation,
// and the sweep tells every subscriber, another session's included.
func TestWriteReachesAnotherSessionsSubscription(t *testing.T) {
	f := startWritable(t)
	notify := make(chan *opcua.PublishNotificationData, 16)
	sub, err := f.anon.Subscribe(context.Background(), &opcua.SubscriptionParameters{Interval: 20 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(context.Background())
	if _, err := sub.Monitor(context.Background(), ua.TimestampsToReturnBoth, opcua.NewMonitoredItemCreateRequestWithDefaults(ua.NewStringNodeID(1, "Speed"), ua.AttributeIDValue, 7)); err != nil {
		t.Fatal(err)
	}
	if dv := next(t, notify); dv.Value.Value() != 0.0 {
		t.Fatalf("initial %v", dv.Value.Value())
	}
	if got := write(t, f.writer, wv("Speed", 61.5))[0]; got != ua.StatusGood {
		t.Fatal(statusName(got))
	}
	if dv := next(t, notify); dv.Value.Value() != 61.5 {
		t.Fatalf("subscriber heard %v, want 61.5", dv.Value.Value())
	}
}

// Without opcua.writable nothing is writable, a writer included, and every
// tag reads CurrentRead.
func TestNoWritableListIsReadOnly(t *testing.T) {
	rt := writeRuntime(t)
	dir := filepath.Join(t.TempDir(), "pki")
	s, err := New(rt, Options{Endpoint: "localhost:0", Name: "lift-station", Sweep: 20 * time.Millisecond, PKI: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	der, key := clientCert(t, writerURI, time.Now().Add(-time.Hour), 24*time.Hour)
	trust(t, dir, der)
	c, err := secureConnect(s, endpointFor(t, s, ua.SecurityPolicyURIBasic256Sha256, ua.MessageSecurityModeSignAndEncrypt), der, key, writerURI)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient(c)
	if got := write(t, c, wv("Speed", 1.0))[0]; got != ua.StatusBadNotWritable {
		t.Errorf("write with no writable list: %s, want Bad_NotWritable", statusName(got))
	}
	if got := read(t, c, ua.NewStringNodeID(1, "Speed"), ua.AttributeIDUserAccessLevel).Value.Value(); got != uint8(ua.AccessLevelTypeCurrentRead) {
		t.Errorf("UserAccessLevel %v", got)
	}
}

// A malformed pattern is refused by New, not left to match nothing.
func TestNewRefusesAMalformedWritablePattern(t *testing.T) {
	rt := writeRuntime(t)
	if _, err := New(rt, Options{Writable: []string{"["}}); err == nil || !strings.Contains(err.Error(), `opcua.writable pattern "["`) {
		t.Fatalf("New with a malformed pattern: %v", err)
	}
}
