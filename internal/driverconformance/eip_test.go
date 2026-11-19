package driverconformance

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/joyautomation/nautilus/eip"
	"github.com/joyautomation/nautilus/eip/logixserver"
)

// eip against logixserver, the Logix emulator. The input is a REAL, the
// output a DINT, both controller-scoped; the output is read back from the
// emulator's tag store. A failed read on a live link is the emulator
// refusing reads of that one tag (logixserver.Server.FailReads).
func init() {
	register(subject{
		name: "eip",
		gate: dropAndAsk,
		start: func(t tb, o startOpts) *rig {
			schema, tags, name, err := logixserver.CompileSurface(&logixserver.TagSurfaceSpec{
				ControllerName: "Conformance",
				Symbols: []logixserver.SymbolSpec{
					{Name: "In", Datatype: "REAL"},
					{Name: "Out", Datatype: "DINT"},
				},
				Tags: []logixserver.TagSpec{
					{Path: "In", Datatype: "REAL"},
					{Path: "Out", Datatype: "DINT"},
				},
			})
			if err != nil {
				t.Fatalf("compile surface: %v", err)
			}
			store := logixserver.NewTagStore()
			for _, tc := range tags {
				store.Set(tc.Path, tc.LeafType, tc.Default)
			}
			store.UpdateValue("In", 4.5)
			srv := logixserver.NewServer(store, schema, name, "127.0.0.1:0", nil)
			if err := srv.Listen(); err != nil {
				t.Fatalf("emulator listen: %v", err)
			}
			sctx, scancel := context.WithCancel(context.Background())
			go func() { _ = srv.Run(sctx) }()
			t.Cleanup(func() { scancel(); srv.Stop() })

			l := newTCPLink(t, srv.Addr(), o.cut)
			m := eip.Manifest{Tags: []eip.TagBinding{
				{Name: "In", Device: "In", Type: "REAL"},
				{Name: "Out", Device: "Out", Type: "DINT", Writable: true},
			}}
			d, err := eip.New("127.0.0.1", m, eip.WithPort(l.port()), eip.WithScanRate(5*time.Millisecond),
				eip.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("eip.New: %v", err)
			}
			if o.install != nil {
				o.install(d)
			}
			ctx, cancel := context.WithCancel(context.Background())
			d.Start(ctx)
			stopOnCleanup(t, cancel, d.Stop)
			return &rig{
				drv: d, in: "In", out: "Out",
				// The emulator stores every number as a float64.
				outVals: [2]any{float64(11), float64(22)},
				field: func() any {
					_, v, _ := store.Resolve("Out")
					return v
				},
				polls:    func() uint64 { return d.Health().Polls },
				failRead: func() { srv.FailReads("In", true) },
				link:     l,
			}
		},
	})
}
