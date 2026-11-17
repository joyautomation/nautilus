package device_test

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	. "github.com/joyautomation/nautilus/profinet/device"
	"github.com/joyautomation/nautilus/profinet/pntest"
	"github.com/joyautomation/nautilus/profinet/transport"
	"github.com/joyautomation/nautilus/profinet/wire"
)

var (
	devMAC  = net.HardwareAddr{0x02, 0, 0, 0, 0xDE, 0x01}
	ctrlMAC = net.HardwareAddr{0x02, 0, 0, 0, 0xC0, 0x01}
	ctrl2   = net.HardwareAddr{0x02, 0, 0, 0, 0xC0, 0x02}
	loIP    = pntest.Loopback
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
	ctrl tctl
	rpc  *net.UDPAddr
	stop func()
}

func newRig(t *testing.T) *rig {
	t.Helper()
	hub := transport.NewHub()
	devRPC, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loIP})
	if err != nil {
		t.Fatal(err)
	}
	ctrl, err := pntest.NewController(hub, ctrlMAC, devMAC, devRPC.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	dev, err := New(Config{
		Identity: Identity{Station: "dev-1", VendorName: "nautilus", VendorID: 0x1234, DeviceID: 0x0001, OrderNumber: "NAUT-PN"},
		Subslots: layout,
		Port: hub.Port(devMAC, transport.Options{
			EtherTypes: []uint16{wire.EtherTypePNIO}, Multicast: []net.HardwareAddr{wire.DCPMulticastAddr},
		}),
		RPC:              devRPC,
		ControllerCMPort: ctrl.CMPort(),
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
	r := &rig{t: t, hub: hub, dev: dev, ctrl: tctl{ctrl, t}, rpc: devRPC.LocalAddr().(*net.UDPAddr)}
	r.stop = func() { cancel(); <-done; ctrl.Close() }
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
	if s := r.dev.Snapshot(); !bytes.Equal(s.Outputs[Addr{1, 1}], []byte{0xAB, 0xCD}) {
		t.Fatalf("last outputs not held: % x", s.Outputs[Addr{1, 1}])
	}
}

func TestOneARAtATime(t *testing.T) {
	r := newRig(t)
	r.ctrl.connect(t, layout, false)
	o, err := pntest.NewController(r.hub, ctrl2, devMAC, r.rpc)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	other := tctl{o, t}
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
	if code := c.setIP(net.IPv4(192, 0, 2, 9)); code != 0x03 {
		t.Fatalf("foreign IP: code %d", code)
	}
	if s := r.dev.Snapshot(); s.SetIPRefused != 1 {
		t.Fatalf("refusal not counted: %+v", s)
	}
	if _, ip := c.identify("dev-1"); !ip.Equal(net.IPv4zero) {
		t.Fatalf("advertises %v after a refused Set", ip)
	}
	if code := c.setIP(loIP); code != 0x00 {
		t.Fatalf("interface IP: code %d", code)
	}
	if _, ip := c.identify("dev-1"); !ip.Equal(loIP) {
		t.Fatalf("advertises %v, want the interface's", ip)
	}
	if code := c.setName("renamed"); code != 0x00 {
		t.Fatalf("set name: %d", code)
	}
	if n, _ := c.identify("renamed"); n != "renamed" {
		t.Fatal("new name not answered")
	}
	if s := r.dev.Snapshot(); s.Station != "renamed" || s.NameSets != 1 {
		t.Fatalf("snapshot %+v", s)
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
	c.Props = 0x11 | 1<<30 // Advanced Startup
	c.connect(t, layout, false)
	if !c.SawARServer() {
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
