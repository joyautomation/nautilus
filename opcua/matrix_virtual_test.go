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
	"sync"
	"testing"

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

// virtualServer is a virtdev run behind a relay on a fixed port: a restart
// stops the run and starts it again on the same listen address, a
// blackhole silences the relay.
type virtualServer struct {
	set    *virtdev.Set
	run    *virtdev.Running
	listen string
	relay  *relay
}

func startVirtual(t *testing.T, s matrixServer) *virtualServer {
	t.Helper()
	dir := t.TempDir()
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	files := map[string]string{
		"virtual-devices.yaml": fmt.Sprintf(`devices:
  - { name: D, profile: %s, endpoint: 10.1.1.21, listen: "%s" }
model:
  program: plant.st
  scan: 100ms
`, s.virtual, listen),
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
	v := &virtualServer{set: set, listen: listen}
	v.start(t)
	v.relay = startRelay(t, listen)
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
	v.run = run
}

func (v *virtualServer) url() string { return "opc.tcp://" + v.relay.addr() }

// restart: the device goes away (its connections close) and comes back
// with its power-up values.
func (v *virtualServer) restart(t *testing.T) func() {
	t.Helper()
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

func startRelay(t *testing.T, target string) *relay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &relay{ln: ln, target: target}
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
		up, err := net.Dial("tcp", r.target)
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
