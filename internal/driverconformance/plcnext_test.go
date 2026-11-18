package driverconformance

import (
	"context"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/joyautomation/nautilus/plcnext"
	"github.com/joyautomation/nautilus/plcnext/plcnexttest"
)

// plcnext on its gRPC transport against plcnexttest's controller, on the
// bench project's manifest (examples/plcnext-bench/plcnext_manifest.yaml:
// the row reads its DI_00 and DO_00, so a change to that layout shows here). The input is DI_00, the output DO_00 (bit 0 of
// output byte 0), read back from the fake's output image. A failed read on
// a live link is a stopped Axioline bus: the firmware answers, the bytes
// are stale. This row is NOT the shm transport: there a closed gate stops
// the page writes and the shim takes the outputs to zero, where on gRPC
// they hold.
func init() {
	register(subject{
		name: "plcnext-grpc",
		start: func(t tb, o startOpts) *rig {
			raw, err := os.ReadFile("../../examples/plcnext-bench/plcnext_manifest.yaml")
			if err != nil {
				t.Fatalf("bench manifest: %v", err)
			}
			m, err := plcnext.ParseManifest(raw)
			if err != nil {
				t.Fatalf("bench manifest: %v", err)
			}
			g := plcnexttest.New()
			l := newUnixLink(t, g.Listen(t), o.cut)
			m.Transport, m.Socket = plcnext.TransportGRPC, l.path()
			d, err := plcnext.New(m, plcnext.WithScanRate(5*time.Millisecond), plcnext.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("plcnext.New: %v", err)
			}
			if o.install != nil {
				o.install(d)
			}
			ctx, cancel := context.WithCancel(context.Background())
			d.Start(ctx)
			stopOnCleanup(t, cancel, d.Stop)
			return &rig{
				drv: d, in: "DI_00", out: "DO_00",
				outVals:  [2]any{false, true},
				field:    func() any { return g.Output(0)&0x01 != 0 },
				polls:    func() uint64 { return d.Health().Reads },
				failRead: func() { g.SetRun(false) },
				link:     l,
			}
		},
	})
}
