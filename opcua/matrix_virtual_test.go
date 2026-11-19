package opcua_test

// Virtual-device profiles of the server matrix: the IO-Link masters of
// virtdev (#712), each served at the node ids the real master uses, by
// nautilus's own OPC UA server over a plant model. The test starts them in
// process (no docker), behind a relay that can go silent the way a link
// does when a device loses power. They are reached the way a site reaches
// a master in its default setup: anonymously, on the None endpoint.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/devbind"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/iolink"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/opcua"
	"github.com/joyautomation/nautilus/virtdev"
)

var virtualServers = []matrixServer{
	{
		// Pepperl+Fuchs ICE3-8IOL: string node ids with spaces; the PDO
		// nodes are writable.
		name:       "iolink-pf",
		virtual:    "pepperl-fuchs-ice3",
		nsURI:      "urn:virtual:pepperl-fuchs:iolm",
		scalarRoot: "ns=1;s=IOLM/Port 1/Attached Device",
		types:      []string{"Boolean", "UInt32"},
		scalars: map[string]string{
			"Boolean": "ns=1;s=IOLM/Port 1/Attached Device/PDO Valid",
			"UInt32":  "ns=1;s=IOLM/Port 1/Attached Device/PDO Data Unsigned32",
		},
		changing: "ns=1;s=IOLM/Port 2/Attached Device/PDI Data Unsigned32", changingType: "UInt32",
		readOnly: "ns=1;s=IOLM/Model", readOnlyType: "String",
		model:    "D_Port2_PdiU32 := D_Port2_PdiU32 + 1;",
		security: "None",
	},
	{
		// SICK SIG300: numeric node ids, four per port; read only.
		name:     "iolink-sick",
		virtual:  "sick-sig300",
		nsURI:    "urn:virtual:sick:sig300",
		types:    []string{"Boolean"},
		scalars:  map[string]string{"Boolean": "ns=3;i=8028"}, // Port 5 / Pin 4
		changing: "ns=3;i=8031", changingType: "Boolean",      // Port 6 / Pin 2
		readOnlyDevice: true,
		model: `n := n + 1;
IF n MOD 3 = 0 THEN D_Port6_Pin2 := NOT D_Port6_Pin2; END_IF;
D_Port5_Pin4 := TRUE;`,
		modelVars: "n : INT;",
		security:  "None",
	},
	{
		// ifm AL1590: process data as hex text under the master's
		// configured name; read only.
		name:       "iolink-ifm",
		virtual:    "ifm-al1590",
		nsURI:      "urn:virtual:ifm:al1590",
		scalarRoot: "ns=5;s=AL1590/iolinkmaster/port[1]/iolinkdevice",
		types:      []string{"String"},
		scalars:    map[string]string{"String": "ns=5;s=AL1590/iolinkmaster/port[1]/iolinkdevice/pdin"},
		changing:   "ns=5;s=AL1590/iolinkmaster/port[2]/iolinkdevice/pdin", changingType: "String",
		readOnlyDevice: true,
		model:          "D_Port1_Word := 646; D_Port2_Word := D_Port2_Word + 1;",
		security:       "None",
	},
}

func init() { matrixServers = append(matrixServers, virtualServers...) }

// virtualServer is a virtdev run behind a relay, whose listener the test
// holds for its whole life, so the driver's endpoint never changes. A
// restart stops the run and starts it again on a fresh ephemeral port
// (nothing is re-bound, so no other process can take the port meanwhile)
// and points the relay at it; a blackhole silences the relay.
type virtualServer struct {
	set   *virtdev.Set
	run   *virtdev.Running
	relay *relay
}

func startVirtual(t *testing.T, s matrixServer) *virtualServer {
	t.Helper()
	dir := t.TempDir()
	ports := ""
	if s.ports != "" {
		ports = ", ports: " + s.ports
	}
	for name, src := range s.ioddFiles {
		raw, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "iodd"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "iodd", name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		// No listen: the device takes an ephemeral loopback port.
		"virtual-devices.yaml": fmt.Sprintf(`devices:
  - { name: D, profile: %s, endpoint: 10.1.1.21%s }
model:
  program: plant.st
  scan: 100ms
`, s.virtual, ports),
		"plant.st": fmt.Sprintf("PROGRAM Plant\nVAR\n%s\nEND_VAR\n%s\nEND_PROGRAM\n", s.modelVars, s.model),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set, err := virtdev.LoadSet(dir)
	if err != nil {
		t.Fatalf("virtual devices: %v", err)
	}
	v := &virtualServer{set: set, relay: startRelay(t)}
	v.start(t)
	t.Cleanup(func() {
		v.relay.close()
		if v.run != nil {
			v.run.Stop()
		}
	})
	return v
}

func (v *virtualServer) start(t *testing.T) {
	t.Helper()
	run, err := virtdev.Start(context.Background(), v.set, virtdev.StartOptions{})
	if err != nil {
		t.Fatalf("virtual devices: %v", err)
	}
	if len(run.Endpoints) != 1 || run.Endpoints[0].Listen == "" {
		run.Stop()
		t.Fatalf("virtual devices: want one listening device, got %+v", run.Endpoints)
	}
	v.run = run
	// Listen is the server's URL, opc.tcp://host:port.
	v.relay.setTarget(strings.TrimPrefix(run.Endpoints[0].Listen, "opc.tcp://"))
}

func (v *virtualServer) url() string { return "opc.tcp://" + v.relay.addr() }

// restart: the device goes away (its connections close) and comes back
// with its power-up values.
func (v *virtualServer) restart(t *testing.T) func() {
	t.Helper()
	v.relay.setTarget("") // down: new connections are refused
	v.run.Stop()
	v.run = nil
	return func() { v.start(t) }
}

func (v *virtualServer) pause(*testing.T)   { v.relay.setPaused(true) }
func (v *virtualServer) unpause(*testing.T) { v.relay.setPaused(false) }

// relay forwards TCP to target. Paused, it holds every byte without closing
// anything: the peer's TCP stack still acknowledges, the application above
// it says nothing, as when a device behind a switch loses power.
type relay struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	cond   *sync.Cond
	paused bool
	closed bool
	conns  []net.Conn
}

func startRelay(t *testing.T) *relay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &relay{ln: ln}
	r.cond = sync.NewCond(&r.mu)
	go r.accept()
	return r
}

func (r *relay) addr() string { return r.ln.Addr().String() }

func (r *relay) accept() {
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return
		}
		r.mu.Lock()
		target := r.target
		r.mu.Unlock()
		if target == "" {
			c.Close()
			continue
		}
		up, err := net.Dial("tcp", target)
		if err != nil {
			c.Close() // the device is down: refused, as a closed port is
			continue
		}
		r.mu.Lock()
		r.conns = append(r.conns, c, up)
		r.mu.Unlock()
		go r.pipe(c, up)
		go r.pipe(up, c)
	}
}

func (r *relay) pipe(src, dst net.Conn) {
	defer src.Close()
	defer dst.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			r.mu.Lock()
			for r.paused && !r.closed {
				r.cond.Wait()
			}
			r.mu.Unlock()
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// setTarget points new connections at addr ("" refuses them).
func (r *relay) setTarget(addr string) {
	r.mu.Lock()
	r.target = addr
	r.mu.Unlock()
}

func (r *relay) setPaused(p bool) {
	r.mu.Lock()
	r.paused = p
	r.cond.Broadcast()
	r.mu.Unlock()
}

func (r *relay) close() {
	r.mu.Lock()
	r.closed = true
	r.cond.Broadcast()
	conns := r.conns
	r.mu.Unlock()
	r.ln.Close()
	for _, c := range conns {
		c.Close()
	}
}

// A Byte[] written by the driver reaches a virtual master and is read back
// (#705): the SICK profile's process data out is a ByteArray point, served
// as Byte[] with ValueRank 1, where a ByteString server would refuse the
// array. A value over the 232 octets a byte node carries is refused by the
// server and never stored.
func TestVirtualMasterByteArrayWrite(t *testing.T) {
	var s matrixServer
	for _, c := range virtualServers {
		if c.name == "iolink-sick" {
			s = c
		}
	}
	v := startVirtual(t, s)
	const node = "ns=4;s=SIG300/Port 1/Device/ProcessDataOutput"
	m := opcua.Manifest{Tags: []opcua.TagBinding{{
		Name: "Pdo", NodeID: node, Namespace: "http://opcfoundation.org/UA/IOLink/", Type: opcua.ByteArray, Writable: true,
	}}}
	d := startMatrixDriver(t, v.url(), m, opcua.WithSecurity("None", "None"))
	waitUntil(t, "online", 30*time.Second, d.Online)
	_ = d.WriteOutputs(nio.Values{"Pdo": ""}) // the baseline write the driver's rule skips
	if err := d.WriteOutputs(nio.Values{"Pdo": "BEEF01"}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the Byte[] to be read back", 10*time.Second, func() bool { return readAll(t, d)["Pdo"] == "beef01" })
	_ = d.WriteOutputs(nio.Values{"Pdo": strings.Repeat("ab", opcua.MaxBytes+1)})
	time.Sleep(time.Second)
	if got := readAll(t, d)["Pdo"]; got != "beef01" {
		t.Errorf("after a write over %d octets the node holds %v, want the earlier beef01", opcua.MaxBytes, got)
	}
}

// A port device end to end (#705): a P+F master served by virtdev holds a
// level sensor's process data as a ByteString node and its identity as
// integers; a master row with a port derives the sensor from its IODD, the
// driver reads it through the real wire, identity-checks it, and drives the
// sensor's process data out through the gate, the image first and the
// master's PDO Valid after, with no pulse (the first check was Ok), until
// the device is swapped.
func TestPortDeviceThroughAVirtualMaster(t *testing.T) {
	// The sensor is swapped for another model after 60 scans (6 s).
	v := startVirtual(t, matrixServer{
		virtual:   "pepperl-fuchs-ice3",
		modelVars: "n : INT;",
		model: `n := n + 1;
D_Port1_PdiBytes := 'fe0dffd1';
D_Port1_VendorID := 65261;
D_Port1_DeviceID := 1048577;
IF n > 60 THEN D_Port1_DeviceID := 1048999; END_IF;
D_Port1_PdiValid := TRUE;`,
	})
	prof, err := os.ReadFile("../devprofile/profiles/pepperl-fuchs-ice3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	level, err := os.ReadFile("../iolink/iodd/testdata/acme-level100-20261118-IODD1.1.xml")
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{
		"profiles/pepperl-fuchs-ice3.yaml":        {Data: prof},
		"iodd/acme-level100-20261118-IODD1.1.xml": {Data: level},
	}
	set, err := devbind.Resolve(fsys, []devbind.Device{{
		Name: "M", Profile: "pepperl-fuchs-ice3", Endpoint: "h",
		Points: []string{"Port1_*"},
		Ports:  map[int]devbind.Port{1: {Name: "LEVEL1", IODD: "acme-level100-20261118-IODD1.1.xml"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	od := set.OPCUADevices()[0]
	probeCtx, cancelProbe := context.WithCancel(context.Background())
	t.Cleanup(cancelProbe)
	// A second driver on the same nodes reads what reached the master. (The
	// plant serves zeros until its first scan, so the port's first check is
	// NoDevice: a power-up transient that does not use up the first Ok.)
	probe, err := opcua.New(v.url(), opcua.Manifest{Namespaces: od.Manifest.Namespaces, Tags: []opcua.TagBinding{
		{Name: "Pdo", NodeID: "ns=1;s=IOLM/Port 1/Attached Device/PDO Data Byte String", Type: opcua.ByteString},
		{Name: "Valid", NodeID: "ns=1;s=IOLM/Port 1/Attached Device/PDO Valid", Type: "Boolean"},
	}}, opcua.WithName("probe"), opcua.WithScanRate(100*time.Millisecond), opcua.WithSecurity("None", "None"))
	if err != nil {
		t.Fatal(err)
	}
	probe.Start(probeCtx)
	t.Cleanup(probe.Stop)
	d, err := opcua.New(v.url(), od.Manifest, opcua.WithName("M"), opcua.WithOnlineTag("M__Online"),
		opcua.WithScanRate(100*time.Millisecond), opcua.WithTimeout(2*time.Second), opcua.WithSecurity("None", "None"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := set.WrapOPCUA("M", d, "None", "None")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	t.Cleanup(func() { cancel(); d.Stop() })

	waitUntil(t, "the port device to be read through the wire", 30*time.Second, func() bool {
		vals, _ := w.ReadInputs()
		_, ok := vals["LEVEL1"]
		return ok && vals["LEVEL1__Identity"] == int64(iolink.Ok)
	})
	// The right device on the first check: decoded, no fault, and armed
	// with no pulse (a power-up never needs one).
	if vals, _ := w.ReadInputs(); vals["LEVEL1__IdentityFault"] != false {
		t.Errorf("fault %v with the right device on the first check", vals["LEVEL1__IdentityFault"])
	}

	// Drive it: the first check was Ok, so the port is armed and sends the
	// program's current values.
	push := func(teach bool, mode int64, lock, rearm bool) {
		t.Helper()
		if err := w.WriteOutputs(nio.Values{"LEVEL1_Teach": teach, "LEVEL1_Mode": mode, "LEVEL1_Lock": lock, "LEVEL1__Rearm": rearm}); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, "the image 8b with the valid flag on the master", 20*time.Second, func() bool {
		push(true, 5, true, false) // the runtime pushes while anything changes; here, until it lands
		pv, _ := probe.ReadInputs()
		return pv["Pdo"] == "8b" && pv["Valid"] == true
	})

	// The swap: the fault is raised, the sensor's bytes are no longer
	// decoded, and the master's valid flag drops, so the new device is not
	// sent the old image.
	waitUntil(t, "the identity fault after the swap", 30*time.Second, func() bool {
		vals, _ := w.ReadInputs()
		return vals["LEVEL1__IdentityFault"] == true && vals["LEVEL1__Identity"] == int64(2)
	})
	waitUntil(t, "the master's valid flag to drop", 20*time.Second, func() bool {
		push(true, 5, true, false)
		pv, _ := probe.ReadInputs()
		return pv["Valid"] == false
	})
	if q := w.Quality()["LEVEL1"]; q != nio.Bad {
		t.Errorf("the swapped port's quality %v, want Bad", q)
	}
}

// ISDU end to end (#705 PR 5a): a virtual SICK master answers ReadISDU and
// WriteISDU for the level sensor on its port 1 from the sensor's IODD, over
// the wire, to a master row with that port device. The default is the IODD's,
// a write is read back, a write the IODD forbids is refused (by the client's
// checks, and by the master for what the device would refuse), and once the
// sensor is swapped a write is refused naming the state while a read still
// works and says which device is there.
func TestISDUThroughAVirtualMaster(t *testing.T) {
	const levelFile = "acme-level100-20261118-IODD1.1.xml"
	levelPath := "../iolink/iodd/testdata/" + levelFile
	v := startVirtual(t, matrixServer{
		virtual:   "sick-sig300",
		ports:     "{ 1: { iodd: " + levelFile + " } }",
		ioddFiles: map[string]string{levelFile: levelPath},
		modelVars: "n : INT;",
		// The sensor is swapped for another model after 100 scans (10 s).
		model: `n := n + 1;
D_Port1_PdiBytes := 'fe0dffd1';
D_Port1_VendorID := 65261;
D_Port1_DeviceID := 1048577;
IF n > 100 THEN D_Port1_DeviceID := 1048999; END_IF;`,
	})
	prof, err := os.ReadFile("../devprofile/profiles/sick-sig300.yaml")
	if err != nil {
		t.Fatal(err)
	}
	level, err := os.ReadFile(levelPath)
	if err != nil {
		t.Fatal(err)
	}
	set, err := devbind.Resolve(fstest.MapFS{
		"profiles/sick-sig300.yaml": {Data: prof},
		"iodd/" + levelFile:         {Data: level},
	}, []devbind.Device{{Name: "M", Profile: "sick-sig300", Endpoint: "h", Points: []string{"Port1_*"},
		Ports: map[int]devbind.Port{1: {Name: "LEVEL1", IODD: levelFile}}}})
	if err != nil {
		t.Fatal(err)
	}
	od := set.OPCUADevices()[0]
	d, err := opcua.New(v.url(), od.Manifest, opcua.WithName("M"), opcua.WithOnlineTag("M__Online"),
		opcua.WithScanRate(100*time.Millisecond), opcua.WithTimeout(2*time.Second), opcua.WithSecurity("None", "None"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := set.WrapOPCUA("M", d, "None", "None")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	t.Cleanup(func() { cancel(); d.Stop() })
	identity := func() int64 {
		vals, _ := w.ReadInputs()
		if n, ok := vals["LEVEL1__Identity"].(int64); ok {
			return n
		}
		return -1 // nothing from the master yet
	}
	waitUntil(t, "the right device on the port", 30*time.Second, func() bool { return identity() == int64(iolink.Ok) || identity() == int64(iolink.AwaitingRearm) })

	res, err := w.ISDURead(ctx, "LEVEL1", "V_Setpoint")
	if err != nil {
		t.Fatal(err)
	}
	if res.Value != uint64(100) || res.Index != 64 || res.Identity != iolink.Ok {
		t.Errorf("the default setpoint %+v, want the IODD's 100 with the identity Ok", res)
	}
	if res, err := w.ISDURead(ctx, "LEVEL1", "V_VendorName"); err != nil || res.Value != "Acme" {
		t.Errorf("vendor name %+v, %v, want the IODD's Acme", res, err)
	}
	if res, err := w.ISDUWrite(ctx, "LEVEL1", "V_Setpoint", uint64(500)); err != nil || res.Value != uint64(500) {
		t.Fatalf("write: %+v, %v", res, err)
	}
	if res, err := w.ISDURead(ctx, "LEVEL1", "V_Setpoint"); err != nil || res.Value != uint64(500) {
		t.Errorf("read back %+v, %v, want 500", res, err)
	}
	// The client refuses before sending what the IODD forbids.
	if _, err := w.ISDUWrite(ctx, "LEVEL1", "V_Setpoint", uint64(2000)); err == nil || !strings.Contains(err.Error(), "range") {
		t.Errorf("a value outside the IODD's range: %v", err)
	}
	if _, err := w.ISDUWrite(ctx, "LEVEL1", "V_SerialNumber", "X"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("a write to a read-only variable: %v", err)
	}
	// What a device refuses, the master answers with the device's ErrorType:
	// a raw call writes to a read-only variable (index 21) and an index the
	// device does not have (999).
	obj := opcua.NodeRef{NodeID: "ns=3;s=SIG300/Port 1/Device"}
	meth := opcua.NodeRef{NodeID: "ns=3;s=SIG300/Port 1/Device/WriteISDU"}
	idx, _ := ua.NewVariant(uint16(21))
	bad, _ := ua.NewVariant(uint16(999))
	sub, _ := ua.NewVariant(uint8(0))
	data, _ := ua.NewVariant(ua.ByteArray(make([]byte, 16)))
	for name, c := range map[string]struct {
		index *ua.Variant
		want  uint16
	}{"read-only": {idx, 0x8023}, "no such index": {bad, 0x8011}} {
		out, err := d.Call(ctx, obj, meth, c.index, sub, data)
		if err != nil || len(out) != 2 || out[0].Value() != c.want || out[1].Value() != int32(-3) {
			t.Errorf("%s: %v %v, want ErrorType 0x%04X and Status -3", name, out, err, c.want)
		}
	}
	// The device's own range check, which the client's pre-check hides: a raw
	// call writes 2000 (outside 0..1000) to the setpoint, and the master
	// answers the device's ErrorType (0x8030 value out of range), Status -3.
	idx64, _ := ua.NewVariant(uint16(64))
	over, _ := ua.NewVariant(ua.ByteArray{0x07, 0xD0})
	if out, err := d.Call(ctx, obj, meth, idx64, sub, over); err != nil || len(out) != 2 || out[0].Value() != uint16(0x8030) || out[1].Value() != int32(-3) {
		t.Errorf("a value outside the IODD's range, written raw: %v %v, want ErrorType 0x8030 and Status -3", out, err)
	}
	if res, _ := w.ISDURead(ctx, "LEVEL1", "V_Setpoint"); res.Value != uint64(500) {
		t.Errorf("the device accepted what it refused: setpoint %v", res.Value)
	}
	// The sensor is swapped: a read still works and says so, a write is
	// refused by the master's own gate, naming the state.
	waitUntil(t, "the swapped device", 40*time.Second, func() bool { return identity() == int64(iolink.WrongDevice) })
	if res, err := w.ISDURead(ctx, "LEVEL1", "V_Setpoint"); err != nil || res.Value != uint64(500) || res.Identity != iolink.WrongDevice {
		t.Errorf("a read after the swap %+v, %v: want 500 with WrongDevice flagged", res, err)
	}
	if _, err := w.ISDUWrite(ctx, "LEVEL1", "V_Setpoint", uint64(600)); err == nil || !strings.Contains(err.Error(), "WrongDevice") {
		t.Errorf("a write after the swap: %v, want a refusal naming WrongDevice", err)
	}
	if res, _ := w.ISDURead(ctx, "LEVEL1", "V_Setpoint"); res.Value != uint64(500) {
		t.Errorf("the refused write changed the parameter: %v", res.Value)
	}
}

// #705's done-when, in one test: a virtual master of each of three makers is
// BROWSED (every node its profile binds is in its address space), the sensor
// on its port is DECODED from the IODD with no hand-written decode (the test
// supplies only the raw bytes, each in its maker's own spelling: a byte string,
// hex text, a Byte[]), its identity is CHECKED (a swap is WrongDevice and the
// bytes are no longer decoded), and its parameters are READ AND WRITTEN through
// the master by ISDU, each by that maker's mechanism (OPC 30120's Byte[]
// methods, the ifm's hex-text methods); the P+F master offers none, and says so.
func TestIOLinkDoneWhen(t *testing.T) {
	const (
		levelFile  = "acme-level100-20261118-IODD1.1.xml"
		switchFile = "acme-switch30-20261119-IODD1.1.xml"
		dir        = "../iolink/iodd/testdata/"
	)
	// Each plant swaps the sensor for another model after 60 scans (6 s).
	for _, mk := range []struct {
		name, profile, iodd string
		model               string
		// members are what the IODD's decode of the raw bytes must give.
		members map[string]any
		// isduParam, isduDefault, isduWrite: nil isduParam means the master
		// offers no ISDU.
		isduParam   string
		isduDefault any
		isduWrite   any
	}{
		{
			name: "pepperl-fuchs", profile: "pepperl-fuchs-ice3", iodd: levelFile,
			model: `D_Port1_PdiBytes := 'fe0dffd1'; D_Port1_VendorID := 65261; D_Port1_DeviceID := 1048577; D_Port1_PdiValid := TRUE;
IF n > 60 THEN D_Port1_DeviceID := 1048999; END_IF;`,
			members: map[string]any{"Level": -12.5, "Temperature": int64(-3), "Ready": true, "Switch_point_1": true, "Switch_point_2": false, "PdValid": true},
		},
		{
			name: "ifm", profile: "ifm-al1590", iodd: switchFile,
			model: `D_Port1_Word := 646; D_Port1_VendorID := 65261; D_Port1_DeviceID := 3145729;
IF n > 60 THEN D_Port1_DeviceID := 3145999; END_IF;`,
			members:   map[string]any{"Position": int64(40), "Alarm": true, "Closed": false},
			isduParam: "V_Threshold", isduDefault: uint64(2048), isduWrite: uint64(3000),
		},
		{
			name: "sick", profile: "sick-sig300", iodd: levelFile,
			model: `D_Port1_PdiBytes := 'fe0dffd1'; D_Port1_VendorID := 65261; D_Port1_DeviceID := 1048577;
IF n > 60 THEN D_Port1_DeviceID := 1048999; END_IF;`,
			members:   map[string]any{"Level": -12.5, "Temperature": int64(-3), "Ready": true, "Switch_point_1": true, "Switch_point_2": false},
			isduParam: "V_Setpoint", isduDefault: uint64(100), isduWrite: uint64(500),
		},
	} {
		t.Run(mk.name, func(t *testing.T) {
			ms := matrixServer{virtual: mk.profile, modelVars: "n : INT;", model: "n := n + 1;\n" + mk.model}
			if mk.isduParam != "" { // a virtual master answers ISDU for a port that has an IODD; the P+F's has none to answer
				ms.ports, ms.ioddFiles = "{ 1: { iodd: "+mk.iodd+" } }", map[string]string{mk.iodd: dir + mk.iodd}
			}
			v := startVirtual(t, ms)
			prof, err := os.ReadFile("../devprofile/profiles/" + mk.profile + ".yaml")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(dir + mk.iodd)
			if err != nil {
				t.Fatal(err)
			}
			set, err := devbind.Resolve(fstest.MapFS{
				"profiles/" + mk.profile + ".yaml": {Data: prof},
				"iodd/" + mk.iodd:                  {Data: raw},
			}, []devbind.Device{{Name: "M", Profile: mk.profile, Endpoint: "h", Points: []string{"Port1_*"},
				Ports: map[int]devbind.Port{1: {Name: "S1", IODD: mk.iodd}}}})
			if err != nil {
				t.Fatal(err)
			}
			od := set.OPCUADevices()[0]

			// BROWSED: every node the profile binds for the port is in the
			// master's address space.
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			sess, err := opcua.Dial(ctx, v.url(), opcua.Connect{Timeout: 5 * time.Second, PKIDir: t.TempDir(), Security: opcua.Security{Policy: "None", Mode: "None"}})
			if err != nil {
				t.Fatal(err)
			}
			res, err := opcua.Browse(ctx, sess.Client, opcua.ObjectsFolder, 0, nil)
			sess.Close()
			if err != nil {
				t.Fatal(err)
			}
			served := map[string]bool{}
			for _, bv := range res.Variables {
				served[bv.NodeID] = true
			}
			for _, b := range od.Manifest.Tags {
				if !served[b.NodeID] && !strings.HasPrefix(b.Name, "M_") { // a command binding shares its read binding's node
					t.Errorf("the profile binds %s (%s), and the master's browse does not have it", b.Name, b.NodeID)
				}
			}

			d, err := opcua.New(v.url(), od.Manifest, opcua.WithName("M"), opcua.WithOnlineTag("M__Online"),
				opcua.WithScanRate(100*time.Millisecond), opcua.WithTimeout(2*time.Second), opcua.WithSecurity("None", "None"))
			if err != nil {
				t.Fatal(err)
			}
			w, err := set.WrapOPCUA("M", d, "None", "None")
			if err != nil {
				t.Fatal(err)
			}
			d.Start(ctx)
			t.Cleanup(d.Stop)
			read := func() nio.Values { vals, _ := w.ReadInputs(); return vals }

			// DECODED from the IODD, IDENTITY-CHECKED.
			waitUntil(t, "the sensor, decoded and identified", 30*time.Second, func() bool {
				vals := read()
				n, ok := vals["S1__Identity"].(int64)
				_, have := vals["S1"]
				return ok && have && (n == int64(iolink.Ok) || n == int64(iolink.AwaitingRearm))
			})
			// What the IODD's decode of the raw bytes gives, member by member (a
			// maker's simulator may take a scan to carry the bytes: wait for them).
			mismatch := func() []string {
				st, ok := read()["S1"].(ir.Value)
				if !ok {
					return []string{"the sensor is not a struct"}
				}
				var bad []string
				for name, want := range mk.members {
					i, found := st.Struct.FieldIndex[name]
					if !found {
						bad = append(bad, fmt.Sprintf("the derived type %s has no member %s", st.Struct.Name, name))
						continue
					}
					var got any
					switch f := st.Fld[i]; f.Kind {
					case ir.TypeBool:
						got = f.B
					case ir.TypeReal:
						got = f.F
					default:
						got = f.I
					}
					if got != want {
						bad = append(bad, fmt.Sprintf("%s decoded as %v, want %v", name, got, want))
					}
				}
				return bad
			}
			deadline := time.Now().Add(15 * time.Second)
			for len(mismatch()) > 0 && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
			}
			for _, m := range mismatch() {
				t.Error(m)
			}

			// PARAMETERIZED, by this maker's mechanism.
			if mk.isduParam == "" {
				if _, err := w.ISDURead(ctx, "S1", "V_Setpoint"); err == nil || !errors.Is(err, devbind.ErrISDURefused) || !strings.Contains(err.Error(), "offers no ISDU") {
					t.Errorf("a master with no ISDU: %v, want a refusal saying so", err)
				}
			} else {
				if res, err := w.ISDURead(ctx, "S1", mk.isduParam); err != nil || res.Value != mk.isduDefault || res.Identity != iolink.Ok {
					t.Errorf("%s read %+v, %v, want the IODD's default %v", mk.isduParam, res, err, mk.isduDefault)
				}
				if res, err := w.ISDUWrite(ctx, "S1", mk.isduParam, mk.isduWrite); err != nil || res.Value != mk.isduWrite {
					t.Fatalf("%s write %+v, %v", mk.isduParam, res, err)
				}
				if res, err := w.ISDURead(ctx, "S1", mk.isduParam); err != nil || res.Value != mk.isduWrite {
					t.Errorf("%s read back %+v, %v, want %v", mk.isduParam, res, err, mk.isduWrite)
				}
			}

			// The swap: WrongDevice, and a parameter is not written to it.
			waitUntil(t, "the swapped sensor", 40*time.Second, func() bool {
				n, _ := read()["S1__Identity"].(int64)
				return n == int64(iolink.WrongDevice)
			})
			if mk.isduParam != "" {
				if _, err := w.ISDUWrite(ctx, "S1", mk.isduParam, mk.isduDefault); err == nil || !strings.Contains(err.Error(), "WrongDevice") {
					t.Errorf("a write after the swap: %v, want a refusal naming WrongDevice", err)
				}
				if res, err := w.ISDURead(ctx, "S1", mk.isduParam); err != nil || res.Value != mk.isduWrite || res.Identity != iolink.WrongDevice {
					t.Errorf("a read after the swap %+v, %v: want the written value with WrongDevice flagged", res, err)
				}
			}
		})
	}
}
