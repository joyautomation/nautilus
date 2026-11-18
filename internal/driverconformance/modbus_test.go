package driverconformance

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/slave"
)

// modbus against modbus/slave. The input is an input register (FC4), so a
// failed read can be injected for it alone; the output is a holding
// register.
func init() {
	register(subject{
		name: "modbus",
		gate: dropAndAsk,
		start: func(t tb, o startOpts) *rig {
			srv := slave.NewServer("", slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err := srv.Start(); err != nil {
				t.Fatalf("slave: %v", err)
			}
			t.Cleanup(srv.Stop)
			u := srv.Unit(1)
			u.SetInput(0, 42)

			l := newTCPLink(t, srv.Addr(), o.cut)
			m := modbus.Manifest{
				Sources: []modbus.Source{{
					ID: "DEV", Host: "127.0.0.1", Port: l.port(), UnitID: 1,
					Timeout:  300 * time.Millisecond,
					RetryMin: 20 * time.Millisecond,
					RetryMax: 80 * time.Millisecond,
				}},
				Tags: []modbus.TagBinding{
					{Name: "In", Source: "DEV", Table: modbus.TableInput, Address: 0, Format: "uint16"},
					{Name: "Out", Source: "DEV", Table: modbus.TableHolding, Address: 10, Format: "uint16", Writable: true},
				},
			}
			d, err := modbus.New(m, modbus.WithScanRate(10*time.Millisecond), modbus.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("modbus.New: %v", err)
			}
			if o.install != nil {
				o.install(d)
			}
			ctx, cancel := context.WithCancel(context.Background())
			d.Start(ctx)
			stopOnCleanup(t, cancel, d.Stop)
			return &rig{
				drv: d, in: "In", out: "Out",
				outVals:  [2]any{float64(11), float64(22)},
				field:    func() any { return float64(u.Holding(10)) },
				polls:    func() uint64 { return d.Health().Reads },
				failRead: func() { u.InjectExceptionAt(4, 0, 2) }, // illegal data address
				link:     l,
			}
		},
	})
}
