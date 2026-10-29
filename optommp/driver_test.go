package optommp

import (
	"context"
	"math"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// benchManifest is what `naut optommp import` writes for the bench
// chassis, with the options given.
func benchManifest(t *testing.T, d *fakeDevice, o ManifestOptions) Manifest {
	t.Helper()
	ch, err := Scan(dialFake(t, d))
	if err != nil {
		t.Fatal(err)
	}
	if o.ID == "" {
		o.ID = "EPIC"
	}
	o.Host = "192.0.2.10"
	m, err := GenerateManifest(ch, o)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// startDriver runs a driver against the fake, every dial a fresh pipe.
func startDriver(t *testing.T, d *fakeDevice, m Manifest) *Driver {
	t.Helper()
	drv, err := New(m, WithScanRate(5*time.Millisecond), WithDialer(func(context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		go d.Serve(b)
		return a, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	drv.retryMin = 10 * time.Millisecond
	drv.chassis = 20 * time.Millisecond
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	return drv
}

// eventually polls cond until it holds or a second passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func value(t *testing.T, drv *Driver, name string) any {
	t.Helper()
	v, _ := drv.ReadInputs()
	return v[name]
}

func TestDriverReadsTheBenchChassis(t *testing.T) {
	dev := newBenchDevice()
	drv := startDriver(t, dev, benchManifest(t, dev, ManifestOptions{}))
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	if !drv.Delivered() {
		t.Error("the unit has answered a scan but Delivered is false")
	}

	v, _ := drv.ReadInputs()
	if v["EPIC_M0_DI03"] != true || v["EPIC_M0_DI02"] != false {
		t.Errorf("DI03/DI02 = %v/%v", v["EPIC_M0_DI03"], v["EPIC_M0_DI02"])
	}
	if v["EPIC_M1_DO11"] != true {
		t.Errorf("DO11 read back %v, want true", v["EPIC_M1_DO11"])
	}
	if f := v["EPIC_M2_AI00"].(float64); math.Abs(f-20.19) > 0.01 {
		t.Errorf("AI00 = %g", f)
	}
	q := drv.Quality()
	for name, want := range map[string]nio.Quality{
		"EPIC_M0_DI03": nio.Good, "EPIC_M1_DO11": nio.Good, "EPIC_M2_AI00": nio.Good, "EPIC_M3_AI00": nio.Good,
		"EPIC_M2_AI01": nio.Bad, // unwired ICTD: NaN
	} {
		if q[name] != want {
			t.Errorf("quality %s = %s, want %s", name, q[name], want)
		}
	}
	h := drv.Health()
	if h.Requests != 2 {
		t.Errorf("a plain scan took %d requests, want 2 (packed values + packed digital)", h.Requests)
	}
	if h.Unit != "GRV-EPIC-PR1" || h.Firmware != "3.6.1.0" || !h.Connected || h.BadTags != 11+23 { // the fake leaves every unwired analog NaN
		t.Errorf("health: %+v", h)
	}
}

func TestDriverNotConnectedBeforeFirstScan(t *testing.T) {
	dev := newBenchDevice()
	drv, err := New(benchManifest(t, dev, ManifestOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	if q := drv.Quality()["EPIC_M0_DI00"]; q != nio.NotConnected {
		t.Errorf("before Start: %s", q)
	}
	// Only the driver's own status, which a program may read from scan one.
	want := nio.Values{"EPIC__Online": false, "EPIC__Outages": int64(0), "EPIC__WatchdogTrips": int64(0)}
	if v, _ := drv.ReadInputs(); !reflect.DeepEqual(v, want) {
		t.Errorf("before Start the driver delivered %v, want %v", v, want)
	}
	if q, ok := drv.Quality()["EPIC__Online"]; ok {
		t.Errorf("a status point has quality %s; it is the driver's own and always Good", q)
	}
	// Whatever the driver's image holds before the unit answers (its own
	// status tags), it is not a delivery: the runtime's start-up hold keys
	// on Delivered, not on a read that succeeds.
	if drv.Delivered() {
		t.Error("Delivered before the unit answered")
	}
}

func TestDriverWritesOnlyTheCommandedChannels(t *testing.T) {
	dev := newBenchDevice()
	drv := startDriver(t, dev, benchManifest(t, dev, ManifestOptions{}))
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })

	// DO11 is on at the device and is not commanded: it must stay on.
	if err := drv.WriteOutputs(nio.Values{"EPIC_M1_DO00": true}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "DO00 on at the device", func() bool { return dev.U32(addrDigitalPacked+digitalModuleStride)&1 == 1 })
	if dev.U32(addrDigitalPacked+digitalModuleStride)&(1<<11) == 0 {
		t.Fatal("writing DO00 switched DO11 off")
	}
	eventually(t, "DO00 read back", func() bool { return value(t, drv, "EPIC_M1_DO00") == true })

	// The same value again is not a command; a change is.
	writes := drv.Health().Writes
	drv.WriteOutputs(nio.Values{"EPIC_M1_DO00": true})
	time.Sleep(20 * time.Millisecond)
	if drv.Health().Writes != writes {
		t.Error("an unchanged output was written again")
	}
	drv.WriteOutputs(nio.Values{"EPIC_M1_DO00": false, "EPIC_M1_DO11": false})
	eventually(t, "DO00 and DO11 off", func() bool { return dev.U32(addrDigitalPacked+digitalModuleStride) == 0 })
	if got := drv.Health().Writes - writes; got != 1 {
		t.Errorf("two channels on one module took %d writes, want 1", got)
	}
}

func TestDriverWriteGate(t *testing.T) {
	dev := newBenchDevice()
	drv := startDriver(t, dev, benchManifest(t, dev, ManifestOptions{}))
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	drv.SetWriteGate(func() bool { return false })
	drv.WriteOutputs(nio.Values{"EPIC_M1_DO00": true})
	time.Sleep(20 * time.Millisecond)
	if dev.U32(addrDigitalPacked+digitalModuleStride)&1 != 0 {
		t.Fatal("a closed gate let a command through")
	}
}

func TestDriverHoldsCommandsAcrossADrop(t *testing.T) {
	dev := newBenchDevice()
	drv := startDriver(t, dev, benchManifest(t, dev, ManifestOptions{}))
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })

	dev.Drop()
	eventually(t, "Stale after the drop", func() bool { return drv.Quality()["EPIC_M0_DI00"] == nio.Stale })
	drv.WriteOutputs(nio.Values{"EPIC_M1_DO05": true})
	eventually(t, "Good after the reconnect", func() bool { return drv.Quality()["EPIC_M0_DI00"] == nio.Good })
	eventually(t, "DO05 written on reconnect", func() bool { return dev.U32(addrDigitalPacked+digitalModuleStride)&(1<<5) != 0 })
}

func TestDriverAnalogNaNIsBadAndRecovers(t *testing.T) {
	dev := newBenchDevice()
	drv := startDriver(t, dev, benchManifest(t, dev, ManifestOptions{}))
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	dev.PutU32(addrPackedValues+(2*32+1)*4, math.Float32bits(21.5))
	eventually(t, "AI01 Good once wired", func() bool { return drv.Quality()["EPIC_M2_AI01"] == nio.Good })
	if f := value(t, drv, "EPIC_M2_AI01").(float64); f != 21.5 {
		t.Errorf("AI01 = %g", f)
	}
}

func TestDriverDigitalQualityBit(t *testing.T) {
	dev := newBenchDevice()
	drv := startDriver(t, dev, benchManifest(t, dev, ManifestOptions{}))
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	dev.PutU32(addrDigitalPacked+4, 1<<7) // slot 0 quality mask: channel 7 bad
	eventually(t, "DI07 Bad", func() bool { return drv.Quality()["EPIC_M0_DI07"] == nio.Bad })
	if q := drv.Quality()["EPIC_M0_DI06"]; q != nio.Good {
		t.Errorf("DI06 = %s, only channel 7 is flagged", q)
	}
}

func TestDriverSwappedOrEmptySlotIsBad(t *testing.T) {
	dev := newBenchDevice()
	drv := startDriver(t, dev, benchManifest(t, dev, ManifestOptions{}))
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })

	cfg := uint32(addrChannelConfig + 3*configModuleStride)
	dev.PutU32(cfg, 0x60000001) // a GRV-IMA-24 where the GRV-IV-24 was
	eventually(t, "slot 3 Bad", func() bool { return drv.Quality()["EPIC_M3_AI00"] == nio.Bad })
	if h := drv.Health(); !strings.Contains(h.Faults, "slot 3 holds GRV-IMA-24, the manifest expects GRV-IV-24") {
		t.Errorf("faults: %q", h.Faults)
	}
	if q := drv.Quality()["EPIC_M2_AI00"]; q != nio.Good {
		t.Errorf("slot 2 = %s; only slot 3 changed", q)
	}

	dev.PutU32(cfg, 0) // pulled
	eventually(t, "empty slot named", func() bool { return strings.Contains(drv.Health().Faults, "slot 3 is empty") })

	dev.PutU32(cfg, 0x60000002) // back
	eventually(t, "slot 3 Good again", func() bool { return drv.Quality()["EPIC_M3_AI00"] == nio.Good })
}

func TestDriverAbsentChannelIsBad(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{})
	// A binding for a channel the 12-channel output module does not have.
	m.Tags = append(m.Tags, Binding{Name: "EPIC_M1_DO20", Point: PointDO, Slot: 1, Channel: 20})
	drv := startDriver(t, dev, m)
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	if q := drv.Quality()["EPIC_M1_DO20"]; q != nio.Bad {
		t.Errorf("absent channel = %s", q)
	}
}

func TestDriverLatchesCatchAPulseBetweenScans(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{Latches: true})
	drv, err := New(m, WithDialer(func(context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		go dev.Serve(b)
		return a, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	// Drive the scans by hand: one session, poll by poll.
	a, b := net.Pipe()
	go dev.Serve(b)
	c := NewClient(a, time.Second)
	defer c.Close()
	if err := drv.poll(c); err != nil {
		t.Fatal(err)
	}
	if value(t, drv, "EPIC_M0_DI05_OnLatch") != false {
		t.Fatal("latch set before any pulse")
	}
	dev.SetDigital(0, 5, true) // a pulse shorter than the scan
	dev.SetDigital(0, 5, false)
	if err := drv.poll(c); err != nil {
		t.Fatal(err)
	}
	v, _ := drv.ReadInputs()
	if v["EPIC_M0_DI05"] != false || v["EPIC_M0_DI05_OnLatch"] != true || v["EPIC_M0_DI05_OffLatch"] != true {
		t.Errorf("after the pulse: state %v on-latch %v off-latch %v", v["EPIC_M0_DI05"], v["EPIC_M0_DI05_OnLatch"], v["EPIC_M0_DI05_OffLatch"])
	}
	if err := drv.poll(c); err != nil {
		t.Fatal(err)
	}
	if value(t, drv, "EPIC_M0_DI05_OnLatch") != false {
		t.Error("the latch was not cleared after it was delivered")
	}
}

func TestDriverScratchpad(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{ScratchBits: 2, ScratchInts: 2, ScratchFloats: 2})
	drv := startDriver(t, dev, m)
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	dev.PutU32(addrScratchInts+4, uint32(0xFFFFFFFF)) // int 1 = -1, from another client
	eventually(t, "int 1 read", func() bool { return value(t, drv, "EPIC_SP_I1") == int64(-1) })

	drv.WriteOutputs(nio.Values{"EPIC_SP_B1": true, "EPIC_SP_I0": int64(-42), "EPIC_SP_F1": 2.5})
	eventually(t, "scratch writes read back", func() bool {
		v, _ := drv.ReadInputs()
		return v["EPIC_SP_B1"] == true && v["EPIC_SP_B0"] == false && v["EPIC_SP_I0"] == int64(-42) && v["EPIC_SP_F1"] == 2.5
	})
	if h := drv.Health(); h.Requests != 5 {
		t.Errorf("scan with three scratch areas: %d requests, want 5", h.Requests)
	}
}

func TestDriverAnalogOutput(t *testing.T) {
	dev := newBenchDevice()
	// A GRV-OVMALC-8 in slot 4, channels at 0-10 V.
	for i := 0; i < 8; i++ {
		row := addrChannelConfig + uint32(4*configModuleStride+i*configChannelStride)
		dev.PutU32(row, 0xA0000003)
		dev.PutU32(row+4, 0xA0000020)
		dev.PutU32(addrPackedValues+uint32(4*32+i)*4, 0)
	}
	m := benchManifest(t, dev, ManifestOptions{})
	drv := startDriver(t, dev, m)
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	drv.WriteOutputs(nio.Values{"EPIC_M4_AO02": 7.25})
	eventually(t, "AO02 read back", func() bool { return value(t, drv, "EPIC_M4_AO02") == 7.25 })
	if got := dev.U32(addrAnalogWrite + 4*pointModuleStride + 2*pointChannelStride); math.Float32frombits(got) != 7.25 {
		t.Errorf("analog write area holds %g", math.Float32frombits(got))
	}
}

func TestDriverSendsThePowerupClear(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{})
	dev.PutU32(addrPUCFlag, 1)
	drv := startDriver(t, dev, m)
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	if got := dev.U32(addrPowerupClear); got != 1 {
		t.Errorf("power-up clear area = %d, want 1", got)
	}
}

func TestDriverQualityCodes(t *testing.T) {
	dev := newBenchDevice()
	dev.PutU32(addrQualityCodes+2*qualityModuleStride+1*4, 9)
	drv := startDriver(t, dev, benchManifest(t, dev, ManifestOptions{Quality: true}))
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	if v := value(t, drv, "EPIC_M2_AI01_Quality"); v != int64(9) {
		t.Errorf("AI01 quality code = %v", v)
	}
	if h := drv.Health(); h.Requests != 3 {
		t.Errorf("with quality codes: %d requests, want 3", h.Requests)
	}
}
