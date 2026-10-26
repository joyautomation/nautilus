package optommp

import (
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// schedOutcome checks Health reports priority: honestly: applied (a box
// with CAP_SYS_NICE) or refused with the reason, never silently.
func schedOutcome(t *testing.T, h Health) {
	t.Helper()
	if h.Priority != 20 {
		t.Errorf("health priority %d", h.Priority)
	}
	if h.SchedError != "" && !strings.Contains(h.SchedError, "SCHED_FIFO priority 20") {
		t.Errorf("refusal does not say what was refused: %q", h.SchedError)
	}
	if goruntime.GOOS != "linux" && h.SchedError == "" {
		t.Error("priority reported applied off Linux")
	}
	t.Logf("priority 20: %q (empty = applied)", h.SchedError)
}

func TestDriverPriorityPolls(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{Latches: true})
	m.Priority = 20
	drv := startDriverTCP(t, dev, m)
	eventually(t, "scans", func() bool { return drv.Health().Reads > 10 })
	if v := value(t, drv, "EPIC_M0_DI03"); v != true {
		t.Errorf("DI03 = %v", v)
	}
	// A command is written between scans and read back.
	drv.WriteOutputs(nio.Values{"EPIC_M1_DO04": true})
	eventually(t, "DO04 read back", func() bool { return value(t, drv, "EPIC_M1_DO04") == true })
	dev.SetDigital(0, 5, true)
	eventually(t, "DI05", func() bool { return value(t, drv, "EPIC_M0_DI05") == true })

	h := drv.Health()
	schedOutcome(t, h)
	if h.ScanMaxUs <= 0 || h.ScanP99Us <= 0 || h.WakeMaxUs <= 0 && h.WakeP99Us <= 0 {
		t.Errorf("timing not recorded: scan p99 %d max %d, wake p99 %d max %d", h.ScanP99Us, h.ScanMaxUs, h.WakeP99Us, h.WakeMaxUs)
	}

	// A dropped connection reconnects on the same thread.
	reads := drv.Health().Reads
	dev.Drop()
	eventually(t, "reconnected and scanning", func() bool { return drv.Health().Connected && drv.Health().Reads > reads+10 })

	// Stop returns promptly from the kernel sleep.
	t0 := time.Now()
	drv.Stop()
	if d := time.Since(t0); d > 200*time.Millisecond {
		t.Errorf("Stop took %s", d)
	}
}

func TestDriverPriorityStreams(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{Stream: 5 * time.Millisecond, Latches: true})
	m.Priority = 20
	drv := startDriverTCP(t, dev, m)
	eventually(t, "datagrams", func() bool { return drv.Health().Reads > 5 })
	dev.SetDigital(0, 5, true)
	eventually(t, "DI05 over the stream", func() bool { return value(t, drv, "EPIC_M0_DI05") == true })
	drv.WriteOutputs(nio.Values{"EPIC_M1_DO04": true})
	eventually(t, "DO04 read back over the stream", func() bool { return value(t, drv, "EPIC_M1_DO04") == true })
	h := drv.Health()
	schedOutcome(t, h)
	if h.Mode != "stream" || h.ScanMaxUs <= 0 || h.WakeMaxUs != 0 {
		t.Errorf("health: mode %q scan max %d wake max %d (a stream has no polled wake-up)", h.Mode, h.ScanMaxUs, h.WakeMaxUs)
	}
	drv.Stop()
	if on := dev.U32(fakeStreamOn); on != 0 {
		t.Errorf("stream left on after Stop (%d)", on)
	}
}

func TestDriverPriorityStreamGoesLate(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{Stream: 5 * time.Millisecond})
	m.Priority = 20
	m.Timeout = Duration(400 * time.Millisecond)
	drv := startDriverTCP(t, dev, m)
	eventually(t, "datagrams", func() bool { return drv.Health().Reads > 5 })
	dev.PauseStream(true)
	eventually(t, "Stale", func() bool { return drv.Quality()["EPIC_M0_DI03"] == nio.Stale })
	dev.PauseStream(false)
	eventually(t, "Good again", func() bool { return drv.Quality()["EPIC_M0_DI03"] == nio.Good })
	if drv.Health().Late == 0 {
		t.Error("late not counted")
	}
}

func TestManifestPriority(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{})
	for _, p := range []int{-1, 100} {
		m.Priority = p
		if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "priority") {
			t.Errorf("priority %d: %v", p, err)
		}
	}
	m.Priority = 20
	if err := m.Validate(); err != nil {
		t.Error(err)
	}
	src := "host: 10.0.0.1\npriority: 20\ntags:\n  - { name: A, point: di, slot: 0, channel: 0 }\n"
	pm, err := ParseManifest([]byte(src))
	if err != nil || pm.Priority != 20 {
		t.Errorf("parse: %v priority %d", err, pm.Priority)
	}
}

func TestTimingStats(t *testing.T) {
	var tm timing
	if p, m := tm.stats(); p != 0 || m != 0 {
		t.Errorf("empty: %d %d", p, m)
	}
	for i := 0; i < 990; i++ {
		tm.add(70) // 50–100 µs
	}
	for i := 0; i < 9; i++ {
		tm.add(300) // 200–500 µs
	}
	tm.add(12000)
	if p, m := tm.stats(); p != 100 || m != 12000 {
		t.Errorf("p99 %d max %d, want 100 and 12000", p, m)
	}
	tm.add(300)
	tm.add(300) // 11 of 1002 above 100 µs: p99 moves up
	if p, _ := tm.stats(); p != 500 {
		t.Errorf("p99 %d, want 500", p)
	}
}
