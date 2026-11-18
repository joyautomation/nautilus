package device

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/profinet/transport"
	"github.com/joyautomation/nautilus/profinet/wire"
)

var (
	devMAC  = net.HardwareAddr{0x02, 0, 0, 0, 0xDE, 0x01}
	ctrlMAC = net.HardwareAddr{0x02, 0, 0, 0, 0xC0, 0x01}
	ctrl2   = net.HardwareAddr{0x02, 0, 0, 0, 0xC0, 0x02}
	loIP    = net.IPv4(127, 0, 0, 1)
)

// layout is the device under test: slot 1 in/out, slot 2 input only.
var layout = []Subslot{
	{Addr: Addr{1, 1}, ModuleIdent: 0x101, SubmoduleIdent: 0x1, InputLen: 4, OutputLen: 2},
	{Addr: Addr{2, 1}, ModuleIdent: 0x102, SubmoduleIdent: 0x1, InputLen: 2},
}

type rig struct {
	t    *testing.T
	hub  *transport.Hub
	dev  *Device
	ctrl *testController
	stop func()
}

func newRig(t *testing.T) *rig {
	t.Helper()
	hub := transport.NewHub()
	devRPC, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loIP})
	if err != nil {
		t.Fatal(err)
	}
	ctrl := newController(t, hub, ctrlMAC, devRPC.LocalAddr().(*net.UDPAddr))
	dev, err := New(Config{
		Identity: Identity{Station: "dev-1", VendorName: "nautilus", VendorID: 0x1234, DeviceID: 0x0001, OrderNumber: "NAUT-PN"},
		Subslots: layout,
		Port: hub.Port(devMAC, transport.Options{
			EtherTypes: []uint16{wire.EtherTypePNIO}, Multicast: []net.HardwareAddr{wire.DCPMulticastAddr},
		}),
		RPC:              devRPC,
		ControllerCMPort: ctrl.cm.LocalAddr().(*net.UDPAddr).Port,
		// Two addresses, the one the controller sets second: a bench NIC
		// often holds several.
		InterfaceAddrs: func() []net.IP { return []net.IP{net.IPv4(127, 0, 0, 2), loIP} },
		MinWatchdog:    60 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { dev.Run(ctx); close(done) }()
	r := &rig{t: t, hub: hub, dev: dev, ctrl: ctrl}
	r.stop = func() { cancel(); <-done; ctrl.close() }
	t.Cleanup(r.stop)
	return r
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// expectedFor is what a controller configured with layout expects.
func expectedFor(subs []Subslot) []wire.IOSubmodule {
	io := []wire.IOSubmodule{{Slot: 0, Subslot: 1}, {Slot: 0, Subslot: InterfaceSubslot}, {Slot: 0, Subslot: PortSubslot}}
	for _, s := range subs {
		io = append(io, wire.IOSubmodule{Slot: s.Slot, Subslot: s.Subslot, InputLen: s.InputLen, OutputLen: s.OutputLen})
	}
	return io
}

func TestIdentifyAndStartup(t *testing.T) {
	r := newRig(t)
	c := r.ctrl

	name, ip := c.identify("dev-1")
	if name != "dev-1" || !ip.Equal(net.IPv4zero) {
		t.Fatalf("identify: %q %v (no IP until one is set)", name, ip)
	}
	if n, _ := c.identify("someone-else"); n != "" {
		t.Fatal("answered an Identify for another name")
	}

	diff := c.connect(t, layout, false)
	if len(diff) != 0 {
		t.Fatalf("diff %+v for a matching layout", diff)
	}
	c.prmEnd(t)
	eventually(t, "DATA", func() bool { return r.dev.Snapshot().State == StateData })
	eventually(t, "ApplicationReady", func() bool { return c.appReadySeen() })

	// Outputs from a running controller land in the image.
	c.startOutputs(map[Addr][]byte{{1, 1}: {0xAB, 0xCD}}, wire.DataStatusRunNormal)
	eventually(t, "outputs", func() bool {
		s := r.dev.Snapshot()
		return bytes.Equal(s.Outputs[Addr{1, 1}], []byte{0xAB, 0xCD}) && s.OutputGood[Addr{1, 1}] && s.Meaning == wire.StatusRun
	})

	// Until the driver says the inputs are in, they go out IOPS Bad, never
	// as zeros that look valid.
	eventually(t, "IOPS Bad before the first push", func() bool {
		_, iops, _, ok := c.lastInput(Addr{1, 1})
		return ok && iops == wire.IOxSBad
	})

	// Inputs reach the controller at their FrameOffsets with IOPS Good, and
	// Run once the controller acknowledged ApplicationReady.
	r.dev.SetInput(Addr{1, 1}, []byte{1, 2, 3, 4})
	r.dev.SetInputsValid(true)
	r.dev.SetInput(Addr{2, 1}, []byte{9, 9})
	eventually(t, "inputs", func() bool {
		in, iops, ds, ok := c.lastInput(Addr{1, 1})
		in2, _, _, _ := c.lastInput(Addr{2, 1})
		return ok && bytes.Equal(in, []byte{1, 2, 3, 4}) && iops == wire.IOxSGood &&
			bytes.Equal(in2, []byte{9, 9}) && ds&wire.DataStatusRun != 0
	})

	// Inputs not valid (gate closed): IOPS Bad, data withheld.
	r.dev.SetInputsValid(false)
	eventually(t, "IOPS Bad", func() bool {
		in, iops, _, ok := c.lastInput(Addr{1, 1})
		return ok && iops == wire.IOxSBad && bytes.Equal(in, []byte{0, 0, 0, 0})
	})
	r.dev.SetInputsValid(true)

	// The controller in STOP: its outputs are not live commands.
	c.setOutputs(map[Addr][]byte{{1, 1}: {0xAB, 0xCD}}, wire.DataStatusNormal)
	eventually(t, "stop", func() bool { return r.dev.Snapshot().Meaning == wire.StatusStop })

	// The controller's IOPS Bad for a subslot: that subslot is not good.
	c.setOutputIOPS(Addr{1, 1}, wire.IOxSBad, wire.DataStatusRunNormal)
	eventually(t, "output IOPS bad", func() bool { return !r.dev.Snapshot().OutputGood[Addr{1, 1}] })

	// The controller goes quiet: the watchdog ends the AR.
	c.stopOutputs()
	eventually(t, "watchdog abort", func() bool {
		s := r.dev.Snapshot()
		return s.State == StateIdle && s.Aborts == 1
	})
	// The image forgets the ended AR's outputs (the driver holds its own
	// last good value as Stale): nothing reads as good with old bytes.
	if s := r.dev.Snapshot(); !bytes.Equal(s.Outputs[Addr{1, 1}], []byte{0, 0}) || s.OutputGood[Addr{1, 1}] {
		t.Fatalf("outputs after the AR ended: % x good=%v", s.Outputs[Addr{1, 1}], s.OutputGood[Addr{1, 1}])
	}
}

func TestOneARAtATime(t *testing.T) {
	r := newRig(t)
	r.ctrl.connect(t, layout, false)
	other := newController(t, r.hub, ctrl2, r.ctrl.devRPC)
	defer other.close()
	if st := other.connectStatus(t, layout); st.IsOK() || st.ErrorCode1 != 0x40 || st.ErrorCode2 != 0x04 {
		t.Fatalf("second controller: %+v, want out of AR resources", st)
	}
	if s := r.dev.Snapshot(); s.RefusedARs != 1 || s.Controller != "ctrl" {
		t.Fatalf("snapshot %+v", s)
	}
	// The same controller reconnecting replaces its AR.
	r.ctrl.connect(t, layout, false)
	if s := r.dev.Snapshot(); s.ARs != 2 || s.State != StateStartup {
		t.Fatalf("reconnect: %+v", s)
	}
}

func TestRetransmittedConnectRunsOnce(t *testing.T) {
	r := newRig(t)
	r.ctrl.connect(t, layout, true) // sends the same datagram twice
	time.Sleep(50 * time.Millisecond)
	if s := r.dev.Snapshot(); s.ARs != 1 || s.RefusedARs != 0 {
		t.Fatalf("snapshot %+v", s)
	}
}

func TestModuleDiff(t *testing.T) {
	r := newRig(t)
	wrong := append([]Subslot(nil), layout...)
	wrong[0].SubmoduleIdent = 0x99                                                    // wrong submodule in slot 1
	wrong = append(wrong, Subslot{Addr: Addr{3, 1}, ModuleIdent: 0x103, InputLen: 1}) // no slot 3 here
	diff := r.ctrl.connect(t, wrong, false)
	if len(diff) != 2 {
		t.Fatalf("diff %+v", diff)
	}
	if diff[0].Slot != 1 || diff[0].State != wire.ModuleStateProperModule || len(diff[0].Submodules) != 1 ||
		diff[0].Submodules[0].State != wire.SubmoduleStateWrong || diff[0].Submodules[0].SubmoduleIdent != 0x1 {
		t.Fatalf("slot 1 diff %+v", diff[0])
	}
	if diff[1].Slot != 3 || diff[1].State != wire.ModuleStateNoModule {
		t.Fatalf("slot 3 diff %+v", diff[1])
	}
	if len(r.dev.Snapshot().Diff) != 2 {
		t.Fatal("diff not on the snapshot")
	}
	// The wrong subslot's inputs go out Bad; the right one's Good.
	r.ctrl.prmEnd(t)
	r.dev.SetInput(Addr{2, 1}, []byte{7, 7})
	r.dev.SetInputsValid(true)
	eventually(t, "IOPS per subslot", func() bool {
		_, bad, _, ok1 := r.ctrl.lastInput(Addr{1, 1})
		in2, good, _, ok2 := r.ctrl.lastInput(Addr{2, 1})
		return ok1 && ok2 && bad == wire.IOxSBad && good == wire.IOxSGood && bytes.Equal(in2, []byte{7, 7})
	})
}

func TestDCPSetNeverTouchesTheHost(t *testing.T) {
	r := newRig(t)
	c := r.ctrl
	if code := c.setIP(net.IPv4(192, 0, 2, 9)); code != dcpNotSet {
		t.Fatalf("foreign IP: code %d", code)
	}
	if s := r.dev.Snapshot(); s.SetIPRefused != 1 {
		t.Fatalf("refusal not counted: %+v", s)
	}
	if _, ip := c.identify("dev-1"); !ip.Equal(net.IPv4zero) {
		t.Fatalf("advertises %v after a refused Set", ip)
	}
	if code := c.setIP(loIP); code != dcpOK {
		t.Fatalf("interface IP: code %d", code)
	}
	if _, ip := c.identify("dev-1"); !ip.Equal(loIP) {
		t.Fatalf("advertises %v, want the interface's", ip)
	}
	if code := c.setName("renamed"); code != dcpOK {
		t.Fatalf("set name: %d", code)
	}
	if n, _ := c.identify("renamed"); n != "renamed" {
		t.Fatal("new name not answered")
	}
	if s := r.dev.Snapshot(); s.Station != "renamed" || s.NameSets != 1 {
		t.Fatalf("snapshot %+v", s)
	}
	// A name that breaks NameOfStation is refused and changes nothing; one
	// over 511 bytes would not even fit LLDP's 9-bit TLV length.
	for _, bad := range []string{"Bad_Name", strings.Repeat("a", 600), "port-001"} {
		if code := c.setName(bad); code != dcpNotSet {
			t.Fatalf("set name %.20q: code %d, want refused", bad, code)
		}
	}
	if s := r.dev.Snapshot(); s.Station != "renamed" || s.NameSets != 1 {
		t.Fatalf("after refused names: %+v", s)
	}
}

// A new AR that leaves a subslot out must not inherit the last AR's good
// status and bytes for it (review on #732: the driver read those as Good
// with frozen data).
func TestReARForgetsOmittedSubslot(t *testing.T) {
	r := newRig(t)
	c := r.ctrl
	c.connect(t, layout, false)
	c.prmEnd(t)
	c.startOutputs(map[Addr][]byte{{1, 1}: {0xAB, 0xCD}}, wire.DataStatusRunNormal)
	eventually(t, "outputs", func() bool { return r.dev.Snapshot().OutputGood[Addr{1, 1}] })

	c.stopOutputs()
	c.connect(t, layout[1:], false) // same controller, slot 1 left out
	c.prmEnd(t)
	c.startOutputs(map[Addr][]byte{}, wire.DataStatusRunNormal)
	eventually(t, "DATA again", func() bool { s := r.dev.Snapshot(); return s.State == StateData && s.Fresh })
	time.Sleep(50 * time.Millisecond)
	if s := r.dev.Snapshot(); s.OutputGood[Addr{1, 1}] || !bytes.Equal(s.Outputs[Addr{1, 1}], []byte{0, 0}) {
		t.Fatalf("slot 1 after a re-AR without it: good=%v % x", s.OutputGood[Addr{1, 1}], s.Outputs[Addr{1, 1}])
	}
}

// A Connect that never reaches PrmEnd ends after the controller's
// CMInitiatorActivityTimeout, so it cannot hold the one AR for good and
// lock the real PLC out.
func TestStartupTimeoutFreesTheAR(t *testing.T) {
	r := newRig(t)
	r.ctrl.activity = 3 // 300 ms
	r.ctrl.connect(t, layout, false)
	other := newController(t, r.hub, ctrl2, r.ctrl.devRPC)
	defer other.close()
	if st := other.connectStatus(t, layout); st.IsOK() {
		t.Fatal("second controller admitted while the first is in start-up")
	}
	eventually(t, "start-up timeout", func() bool { s := r.dev.Snapshot(); return s.State == StateIdle && s.Aborts == 1 })
	if st := other.connectStatus(t, layout); !st.IsOK() {
		t.Fatalf("after the timeout: %+v", st)
	}
}

// Both IOCRs are required: without the output IOCR the AR would have no
// watchdog.
func TestConnectNeedsOutputIOCR(t *testing.T) {
	r := newRig(t)
	r.ctrl.noOutput = true
	if st := r.ctrl.connectStatus(t, layout); st.IsOK() || st.ErrorCode1 != 0x02 {
		t.Fatalf("connect without an output IOCR: %+v", st)
	}
}

func TestReadRecords(t *testing.T) {
	r := newRig(t)
	r.ctrl.connect(t, layout, false)
	im0 := r.ctrl.read(t, 0, 1, 0xAFF0)
	if len(im0) < 6+54 || !bytes.HasPrefix(im0[6+2:], []byte("NAUT-PN")) {
		t.Fatalf("I&M0 % x", im0)
	}
	ident := r.ctrl.read(t, 0, 0, 0xF000)
	blocks, err := wire.ParsePNIOBlocks(ident)
	if err != nil || len(blocks) != 1 || blocks[0].Type != wire.BlockTypeRealIdentData {
		t.Fatalf("real identification: %v %v", blocks, err)
	}
	// 1 API, API 0, three slots (DAP, 1, 2).
	if p := blocks[0].Data; p[1] != 1 || p[7] != 3 {
		t.Fatalf("real identification % x", p)
	}
	if ar := r.ctrl.read(t, 0, 0, 0xF820); len(ar) == 0 {
		t.Fatal("empty ARData")
	}
	if st := r.ctrl.readStatus(t, 1, 1, 0x0042); st.IsOK() {
		t.Fatal("unknown user index answered OK")
	}
	if got := r.ctrl.read(t, 0, 0, 0xF842); len(got) != 0 {
		t.Fatalf("PDExpData % x, want empty success", got)
	}
}

func TestReleaseAndWrite(t *testing.T) {
	r := newRig(t)
	r.ctrl.connect(t, layout, false)
	if st := r.ctrl.write(t, []wire.Write{{Header: wire.RecordHeader{Slot: 1, Subslot: 1, Index: 1}, Data: []byte{1, 2, 3}}}); !st.IsOK() {
		t.Fatalf("write %+v", st)
	}
	r.ctrl.release(t)
	eventually(t, "idle", func() bool { return r.dev.Snapshot().State == StateIdle })
}

// TestAdvancedStartup is the start-up a TIA Portal controller uses
// (ARProperties bit 30): ARServerBlockRes in the Connect response, cyclic
// input frames from Connect on (before PrmEnd), Run only after the
// controller acknowledges ApplicationReady.
func TestAdvancedStartup(t *testing.T) {
	r := newRig(t)
	c := r.ctrl
	c.props = 0x11 | startupModeBit
	c.connect(t, layout, false)
	if !c.sawARServer {
		t.Fatal("no ARServerBlockRes in an Advanced Startup Connect response")
	}
	eventually(t, "input frames before PrmEnd", func() bool {
		_, _, ds, ok := c.lastInput(Addr{1, 1})
		return ok && ds&wire.DataStatusRun == 0
	})
	if s := r.dev.Snapshot(); s.State != StateStartup {
		t.Fatalf("state %v before PrmEnd", s.State)
	}
	// A slow parameterisation with the controller's frames flowing does
	// not trip the watchdog.
	c.startOutputs(map[Addr][]byte{{1, 1}: {1, 1}}, wire.DataStatusNormal)
	time.Sleep(300 * time.Millisecond)
	c.prmEnd(t)
	eventually(t, "Run after ApplicationReady", func() bool {
		_, _, ds, ok := c.lastInput(Addr{1, 1})
		return ok && ds&wire.DataStatusRun != 0 && c.appReadySeen()
	})
	if s := r.dev.Snapshot(); s.State != StateData || s.Aborts != 0 {
		t.Fatalf("snapshot %+v", s)
	}
}

func TestNewValidates(t *testing.T) {
	hub := transport.NewHub()
	conn, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: loIP})
	defer conn.Close()
	base := Config{Identity: Identity{Station: "d"}, Port: hub.Port(devMAC, transport.Options{}), RPC: conn}
	for name, subs := range map[string][]Subslot{
		"slot 0":           {{Addr: Addr{0, 2}}},
		"duplicate":        {{Addr: Addr{1, 1}, ModuleIdent: 1}, {Addr: Addr{1, 1}, ModuleIdent: 1}},
		"two module ident": {{Addr: Addr{1, 1}, ModuleIdent: 1}, {Addr: Addr{1, 2}, ModuleIdent: 2}},
		"too many":         make([]Subslot, MaxSubslots+1),
		"none":             nil,
	} {
		cfg := base
		cfg.Subslots = subs
		if name == "too many" {
			for i := range cfg.Subslots {
				cfg.Subslots[i] = Subslot{Addr: Addr{uint16(i + 1), 1}}
			}
		}
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// ListenRPC (the field mode): no DCE/RPC socket until a controller sets
// the station address, then one bound to exactly that address, moved when
// the address changes. So RPC is not open on the host's other networks,
// and replies leave from the address the controller talks to (review on
// #738).
func TestRPCListensOnTheStationAddress(t *testing.T) {
	hub := transport.NewHub()
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loIP})
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()
	var mu sync.Mutex
	var bound []string
	station := net.IPv4(127, 0, 0, 2) // not the loopback default source
	ctrl := newController(t, hub, ctrlMAC, &net.UDPAddr{IP: station, Port: port})
	defer ctrl.close()
	dev, err := New(Config{
		Identity: Identity{Station: "dev-1", VendorName: "nautilus", VendorID: 0x1234, DeviceID: 0x0001, OrderNumber: "NAUT-PN"},
		Subslots: layout,
		Port: hub.Port(devMAC, transport.Options{
			EtherTypes: []uint16{wire.EtherTypePNIO}, Multicast: []net.HardwareAddr{wire.DCPMulticastAddr},
		}),
		ListenRPC: func(ip net.IP) (net.PacketConn, error) {
			c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: port})
			if err == nil {
				mu.Lock()
				bound = append(bound, c.LocalAddr().String())
				mu.Unlock()
			}
			return c, err
		},
		ControllerCMPort: ctrl.cm.LocalAddr().(*net.UDPAddr).Port,
		InterfaceAddrs:   func() []net.IP { return []net.IP{net.IPv4(127, 0, 0, 2), loIP} },
		MinWatchdog:      60 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { dev.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	got := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), bound...) }

	time.Sleep(50 * time.Millisecond)
	if b := got(); len(b) != 0 {
		t.Fatalf("RPC open before any address was set: %v", b)
	}
	if code := ctrl.setIP(loIP); code != dcpOK {
		t.Fatalf("set IP: %d", code)
	}
	eventually(t, "bound to .1", func() bool { b := got(); return len(b) == 1 })
	if code := ctrl.setIP(station); code != dcpOK {
		t.Fatalf("set IP: %d", code)
	}
	eventually(t, "moved to .2", func() bool { b := got(); return len(b) == 2 })
	want := []string{fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf("127.0.0.2:%d", port)}
	if b := got(); b[0] != want[0] || b[1] != want[1] {
		t.Fatalf("bound %v, want %v", b, want)
	}
	// The controller connects to the station address and the AR runs.
	ctrl.connect(t, layout, false)
	ctrl.prmEnd(t)
	eventually(t, "ApplicationReady", func() bool { return ctrl.appReadySeen() })
	if from := ctrl.lastCControlFrom(); !from.Equal(station) {
		t.Fatalf("CControl from %v, want the station address %v", from, station)
	}
}

// The start-up limit follows CMInitiatorActivityTimeout within its range
// (1-1000, i.e. up to 100 s) and is clamped above it: 0xFFFF would let a
// half-open Connect hold the one AR for almost two hours (re-check on #732).
func TestStartupTimeoutRange(t *testing.T) {
	for _, c := range []struct {
		factor uint16
		want   time.Duration
	}{
		{0, 30 * time.Second}, {1, 100 * time.Millisecond}, {3, 300 * time.Millisecond},
		{1000, 100 * time.Second}, {1001, 100 * time.Second}, {0xFFFF, 100 * time.Second},
	} {
		if got := StartupTimeout(c.factor); got != c.want {
			t.Errorf("factor %d: %v, want %v", c.factor, got, c.want)
		}
	}
}
