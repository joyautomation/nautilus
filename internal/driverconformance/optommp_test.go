package driverconformance

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/joyautomation/nautilus/optommp"
	"github.com/joyautomation/nautilus/optommp/optommptest"
)

// optommp against optommptest's bench chassis. The input is the first
// digital input the generated manifest names, the output the first
// digital output; the output is read back at the unit with a chassis scan.
func init() {
	register(subject{
		name: "optommp",
		start: func(t tb, o startOpts) *rig {
			dev := optommptest.NewBench()
			probe := func() optommp.Chassis {
				a, b := net.Pipe()
				go dev.Serve(b)
				c := optommp.NewClient(a, 2*time.Second)
				defer c.Close()
				ch, err := optommp.Scan(c)
				if err != nil {
					t.Fatalf("scan the fake: %v", err)
				}
				return ch
			}
			m, err := optommp.GenerateManifest(probe(), optommp.ManifestOptions{ID: "EPIC", Host: "127.0.0.1"})
			if err != nil {
				t.Fatalf("GenerateManifest: %v", err)
			}
			var in, out *optommp.Binding
			for i := range m.Tags {
				b := &m.Tags[i]
				switch {
				case in == nil && b.Point == optommp.PointDI:
					in = b
				case out == nil && b.Point == optommp.PointDO:
					out = b
				}
			}
			if in == nil || out == nil {
				t.Fatalf("the bench manifest has no digital input or no digital output")
				return nil
			}

			l := newTCPLink(t, "127.0.0.1:"+strconv.Itoa(dev.Listen(t)), o.cut)
			m.Host, m.Port = "127.0.0.1", l.port()
			d, err := optommp.New(m, optommp.WithScanRate(5*time.Millisecond), optommp.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("optommp.New: %v", err)
			}
			if o.install != nil {
				o.install(d)
			}
			ctx, cancel := context.WithCancel(context.Background())
			d.Start(ctx)
			stopOnCleanup(t, cancel, d.Stop)
			slot, chn := out.Slot, out.Channel
			return &rig{
				drv: d, in: in.Name, out: out.Name,
				outVals: [2]any{false, true},
				field: func() any {
					for _, mod := range probe().Modules {
						if mod.Slot == slot {
							return mod.Channels[chn].State
						}
					}
					return nil
				},
				polls: func() uint64 { return d.Health().Reads },
				link:  l,
			}
		},
		unsupported: map[string]string{
			"C3": "optommptest has no exported way to make one channel read bad (a NaN analog, the channel's quality bit, a slot fault); needs a fake hook from optommp's owner",
		},
	})
}
