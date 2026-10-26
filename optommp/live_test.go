package optommp

// live_test.go runs the driver against a real processor. Skipped unless
// NAUT_OPTOMMP_LIVE names one:
//
//	NAUT_OPTOMMP_LIVE=192.168.82.243 go test ./optommp -run Live -v
//
// Read-only by default. NAUT_OPTOMMP_LOOPBACK="1.3>0.6" also drives
// output slot 1 channel 3 on and off and expects input slot 0 channel 6
// to follow (a field wire between them — the bench EPIC has four), and
// exercises scratchpad float 1023; both are left as found.
//
// NAUT_OPTOMMP_MODE picks how inputs arrive: poll (default), custom (the
// custom data area, one read a scan) or stream=20ms (pushed over UDP; the
// test checks the pointers and streaming configuration are put back as
// found).
// NAUT_OPTOMMP_HOLD=20s keeps the driver running that long after the
// checks, for measuring the processor's load.

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

func TestLiveDriver(t *testing.T) {
	host := os.Getenv("NAUT_OPTOMMP_LIVE")
	if host == "" {
		t.Skip("NAUT_OPTOMMP_LIVE not set")
	}
	c, err := Dial(context.Background(), host, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	streamWas, err := c.Read(addrStreamConfig, streamConfigLen)
	if err != nil {
		t.Fatal(err)
	}
	pointersWas, err := c.Read(addrCustomConfig, MaxBlock)
	c.Close()
	if err != nil {
		t.Fatal(err)
	}
	o := ManifestOptions{ID: "LIVE", Host: host, Latches: true}
	switch mode := os.Getenv("NAUT_OPTOMMP_MODE"); {
	case mode == "" || mode == "poll":
	case mode == "custom":
		o.CustomArea = true
	case strings.HasPrefix(mode, "stream="):
		if o.Stream, err = time.ParseDuration(strings.TrimPrefix(mode, "stream=")); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("NAUT_OPTOMMP_MODE %q: want poll, custom or stream=<interval>", mode)
	}
	if o.CustomArea || o.Stream > 0 {
		defer func() {
			c, err := Dial(context.Background(), host, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			now, err := c.Read(addrStreamConfig, streamConfigLen)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(now[8:], streamWas[8:]) {
				t.Errorf("streaming configuration not restored:\nwas % x\nnow % x", streamWas, now)
			}
			if p, err := c.Read(addrCustomConfig, MaxBlock); err != nil || !bytes.Equal(p, pointersWas) {
				t.Errorf("custom data area pointers not restored (%v)", err)
			}
		}()
	}
	m, err := GenerateManifest(ch, o)
	if err != nil {
		t.Fatal(err)
	}
	m.Tags = append(m.Tags, Binding{Name: "LIVE_SP_F1023", Point: PointScratchFloat, Index: 1023, Writable: true})
	drv, err := New(m, WithScanRate(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	drv.Start(context.Background())
	defer drv.Stop()
	eventually := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s (health %+v)", what, drv.Health())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	eventually("first scan", func() bool { return drv.Health().Reads > 2 })
	h := drv.Health()
	t.Logf("%s firmware %s, %s: %d tags, %d bad, %d requests/scan, last scan %d µs", h.Unit, h.Firmware, h.Mode, h.Tags, h.BadTags, h.Requests, h.ScanUs)
	if h.Faults != "" {
		t.Errorf("chassis faults on a fresh import: %s", h.Faults)
	}
	v, _ := drv.ReadInputs()
	q := drv.Quality()
	for _, b := range m.Tags {
		if b.Point != PointAI {
			continue
		}
		f := v[b.Name].(float64)
		if math.IsNaN(f) != (q[b.Name] == nio.Bad) {
			t.Errorf("%s = %g but quality %s", b.Name, f, q[b.Name])
		}
	}

	loop := os.Getenv("NAUT_OPTOMMP_LOOPBACK")
	if loop == "" {
		return
	}
	var os_, oc, is, ic int
	if _, err := fmt.Sscanf(loop, "%d.%d>%d.%d", &os_, &oc, &is, &ic); err != nil {
		t.Fatalf("NAUT_OPTOMMP_LOOPBACK %q: want out-slot.ch>in-slot.ch", loop)
	}
	out := fmt.Sprintf("LIVE_M%d_DO%02d", os_, oc)
	in := fmt.Sprintf("LIVE_M%d_DI%02d", is, ic)
	if v[out] != false {
		t.Fatalf("%s is %v; the loopback check wants it off to start", out, v[out])
	}
	t0 := time.Now()
	drv.WriteOutputs(nio.Values{out: true})
	eventually(in+" on", func() bool { x, _ := drv.ReadInputs(); return x[in] == true })
	t.Logf("%s on → %s on, read back in %s", out, in, time.Since(t0).Round(time.Millisecond))
	eventually(in+" on-latch", func() bool { x, _ := drv.ReadInputs(); return x[in+"_OnLatch"] == true })
	eventually(in+" on-latch cleared", func() bool { x, _ := drv.ReadInputs(); return x[in+"_OnLatch"] == false })
	drv.WriteOutputs(nio.Values{out: false})
	eventually(in+" off", func() bool { x, _ := drv.ReadInputs(); return x[in] == false })
	eventually(out+" read back off", func() bool { x, _ := drv.ReadInputs(); return x[out] == false })

	prev := v["LIVE_SP_F1023"]
	drv.WriteOutputs(nio.Values{"LIVE_SP_F1023": 12.5})
	eventually("scratch float read back", func() bool { x, _ := drv.ReadInputs(); return x["LIVE_SP_F1023"] == 12.5 })
	drv.WriteOutputs(nio.Values{"LIVE_SP_F1023": prev})
	eventually("scratch float restored", func() bool { x, _ := drv.ReadInputs(); return x["LIVE_SP_F1023"] == prev })
	if hold, _ := time.ParseDuration(os.Getenv("NAUT_OPTOMMP_HOLD")); hold > 0 {
		time.Sleep(hold)
	}
	if h := drv.Health(); h.Mode == "stream" {
		t.Logf("stream: %d datagrams, longest gap %.2f ms, late %d", h.Reads, float64(h.GapMaxUs)/1000, h.Late)
	}
}
