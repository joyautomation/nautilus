package opcua_test

// Virtual-device profiles of the server matrix: the IO-Link masters of
// virtdev (#712), each served at the node ids the real master uses, by
// nautilus's own OPC UA server over a plant model. The test starts them in
// process (no docker), behind a relay that can go silent the way a link
// does when a device loses power. They are reached the way a site reaches
// a master in its default setup: anonymously, on the None endpoint.

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/devbind"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/iolink"
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
	files := map[string]string{
		// No listen: the device takes an ephemeral loopback port.
		"virtual-devices.yaml": fmt.Sprintf(`devices:
  - { name: D, profile: %s, endpoint: 10.1.1.21 }
model:
  program: plant.st
  scan: 100ms
`, s.virtual),
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
