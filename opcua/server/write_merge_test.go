package server

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// #578 through an OPC UA member node: a trusted client writing
// P101.Drive.Speed while the program writes P101.Running every scan. Before
// the fix a write that landed mid-scan was put back to the program's stale
// copy of Speed when the scan committed P101. Each write is checked after
// two further scans have completed, so the scan that was running when it
// landed has committed by then.
func TestOPCUAMemberWriteSurvivesProgramMemberWrite(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: `PROGRAM Main
VAR_EXTERNAL P101 : Pump; END_VAR
VAR i : DINT; n : DINT; END_VAR
FOR i := 1 TO 500 DO n := n + 1; END_FOR;
P101.Running := NOT P101.Running;
END_PROGRAM`,
		Libraries: []string{typesST},
		Driver:    nio.NewMemory(),
		Scan:      time.Second,
		Tags:      []runtime.TagDef{runtime.Typed("P101", runtime.RoleSetpoint, "Pump")},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &writeFixture{rt: rt}
	f.dir = filepath.Join(t.TempDir(), "pki")
	s, err := New(rt, Options{Endpoint: "localhost:0", Name: "merge", Sweep: 20 * time.Millisecond, PKI: f.dir,
		Writable: []string{"P101.Drive"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
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
	c := f.connect(t)

	var scans atomic.Uint64
	stop := make(chan struct{})
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		for {
			select {
			case <-stop:
				return
			default:
				rt.Scan()
				scans.Add(1)
			}
		}
	}()
	defer func() { close(stop); <-scanned }()

	lost := 0
	for i := 1; i <= 200; i++ {
		if st := write(t, c, wv("P101.Drive.Speed", float64(i))); st[0] != ua.StatusOK {
			t.Fatalf("write %d: %v", i, st[0])
		}
		after := scans.Load() + 2
		deadline := time.Now().Add(10 * time.Second)
		for scans.Load() < after {
			if time.Now().After(deadline) {
				t.Fatal("the scan loop stopped")
			}
			time.Sleep(10 * time.Microsecond)
		}
		if got := stored(t, rt, "P101.Drive.Speed"); got != float64(i) {
			lost++
		}
	}
	if lost > 0 {
		t.Errorf("%d of 200 OPC UA member writes lost to the program's commit", lost)
	}
}
